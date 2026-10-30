// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	idleEventUser32       = windows.NewLazySystemDLL("user32.dll")
	idleEventWTS          = windows.NewLazySystemDLL("wtsapi32.dll")
	idleRegisterClass     = idleEventUser32.NewProc("RegisterClassExW")
	idleUnregisterClass   = idleEventUser32.NewProc("UnregisterClassW")
	idleCreateWindow      = idleEventUser32.NewProc("CreateWindowExW")
	idleDestroyWindow     = idleEventUser32.NewProc("DestroyWindow")
	idleDefaultWindowProc = idleEventUser32.NewProc("DefWindowProcW")
	idleGetMessage        = idleEventUser32.NewProc("GetMessageW")
	idleDispatchMessage   = idleEventUser32.NewProc("DispatchMessageW")
	idlePostMessage       = idleEventUser32.NewProc("PostMessageW")
	idlePostQuit          = idleEventUser32.NewProc("PostQuitMessage")
	idleSetTimer          = idleEventUser32.NewProc("SetTimer")
	idleKillTimer         = idleEventUser32.NewProc("KillTimer")
	idleRegisterWTS       = idleEventWTS.NewProc("WTSRegisterSessionNotification")
	idleUnregisterWTS     = idleEventWTS.NewProc("WTSUnRegisterSessionNotification")
)

type idleWindowClass struct {
	size, style                        uint32
	procedure                          uintptr
	classExtra, windowExtra            int32
	instance, icon, cursor, background uintptr
	menu, name                         *uint16
	smallIcon                          uintptr
}

type idleWindowMessage struct {
	window         uintptr
	message        uint32
	wparam, lparam uintptr
	time           uint32
	x, y           int32
	private        uint32
}

// The window is never shown. Its single locked OS thread continuously consumes
// WTS notifications, so a return and departure between 60-second polls still
// revokes the old qualification. It does not observe keystrokes or clipboard.
type windowsSessionEvents struct {
	epoch   atomic.Uint64
	healthy atomic.Bool
	window  uintptr
	done    chan struct{}
	once    sync.Once
	errMu   sync.Mutex
	stopErr error
}

func startWindowsSessionEvents() (*windowsSessionEvents, error) {
	watch := &windowsSessionEvents{done: make(chan struct{})}
	ready := make(chan error, 1)
	go watch.pump(ready)
	if err := <-ready; err != nil {
		<-watch.done
		return nil, err
	}
	return watch, nil
}

func (watch *windowsSessionEvents) pump(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(watch.done)
	defer watch.healthy.Store(false)
	var unique [16]byte
	if _, err := rand.Read(unique[:]); err != nil {
		ready <- errors.New("session notification window identity unavailable")
		return
	}
	name, _ := windows.UTF16PtrFromString("YunPinIdleObserver-" + hex.EncodeToString(unique[:]))
	instance, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	registered, destroyed := false, false
	procedure := syscall.NewCallback(func(window uintptr, message uint32, wp, lp uintptr) uintptr {
		switch message {
		case 0x02b1, 0x0218: // WM_WTSSESSION_CHANGE, WM_POWERBROADCAST
			// Invalidate for all sessions; another active user's session also
			// prevents qualification. State is freshly queried on the next step.
			watch.epoch.Add(1)
			return 1
		case 0x0113: // WM_TIMER: RDS services may not be ready at login.
			if wp == 1 && !registered {
				if ok, _, _ := idleRegisterWTS.Call(window, 1); ok != 0 {
					registered = true
					idleKillTimer.Call(window, 1)
					watch.epoch.Add(1)
					watch.healthy.Store(true)
				}
			}
			return 0
		case 0x0010: // WM_CLOSE: unregister before destruction on owner thread.
			watch.healthy.Store(false)
			idleKillTimer.Call(window, 1)
			if registered {
				if ok, _, _ := idleUnregisterWTS.Call(window); ok == 0 {
					watch.setError(errors.New("session notification unregister failed"))
				}
				registered = false
			}
			if ok, _, _ := idleDestroyWindow.Call(window); ok == 0 {
				watch.setError(errors.New("session notification window destruction failed"))
				idlePostQuit.Call(1)
			}
			return 0
		case 0x0002: // WM_DESTROY
			destroyed = true
			idlePostQuit.Call(0)
			return 0
		}
		result, _, _ := idleDefaultWindowProc.Call(window, uintptr(message), wp, lp)
		return result
	})
	class := idleWindowClass{size: uint32(unsafe.Sizeof(idleWindowClass{})), procedure: procedure, instance: instance, name: name}
	if atom, _, _ := idleRegisterClass.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		ready <- errors.New("session notification class registration failed")
		return
	}
	defer idleUnregisterClass.Call(uintptr(unsafe.Pointer(name)), instance)
	// An invisible top-level window also receives power broadcast messages.
	window, _, _ := idleCreateWindow.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if window == 0 {
		ready <- errors.New("session notification window creation failed")
		return
	}
	watch.window = window
	watch.epoch.Store(1)
	if ok, _, _ := idleRegisterWTS.Call(window, 1); ok != 0 { // NOTIFY_FOR_ALL_SESSIONS
		registered = true
		watch.healthy.Store(true)
	} else if timer, _, _ := idleSetTimer.Call(window, 1, 60000, 0); timer == 0 {
		idleDestroyWindow.Call(window)
		ready <- errors.New("session notification retry timer unavailable")
		return
	}
	ready <- nil
	var msg idleWindowMessage
	for {
		result, _, _ := idleGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if result == 0 {
			break
		}
		if int32(result) == -1 {
			watch.setError(errors.New("session notification message pump failed"))
			watch.healthy.Store(false)
			break
		}
		idleDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
	// Also cover an unexpected WM_QUIT/message-loop failure; only the owner
	// thread unregisters and destroys the native callback target.
	if !destroyed {
		idleKillTimer.Call(window, 1)
		if registered {
			if ok, _, _ := idleUnregisterWTS.Call(window); ok == 0 {
				watch.setError(errors.New("session notification unregister failed"))
			}
		}
		if ok, _, _ := idleDestroyWindow.Call(window); ok == 0 {
			watch.setError(errors.New("session notification window destruction failed"))
		}
	}
	runtime.KeepAlive(name)
}

func (watch *windowsSessionEvents) observe(target uint32) (windowsIdleSample, error) {
	if !watch.healthy.Load() {
		return windowsIdleSample{}, errors.New("session notifications are unavailable")
	}
	before := watch.epoch.Load()
	sample, err := observeWindowsIdle(target)
	if err != nil || !watch.healthy.Load() || before != watch.epoch.Load() {
		return windowsIdleSample{}, errors.New("session observation changed or is unavailable")
	}
	sample.eventRevision = before
	return sample, nil
}

func (watch *windowsSessionEvents) close() error {
	watch.once.Do(func() {
		watch.healthy.Store(false)
		select {
		case <-watch.done:
			return
		default:
		}
		if ok, _, _ := idlePostMessage.Call(watch.window, 0x0010, 0, 0); ok == 0 {
			watch.setError(errors.New("session notification shutdown could not be posted"))
			return
		}
		select {
		case <-watch.done:
		case <-time.After(2 * time.Second):
			// Retained callback state remains alive; never free a live callback
			// context or claim that the observer has stopped on timeout.
			watch.setError(errors.New("session notification shutdown is unconfirmed"))
		}
	})
	watch.errMu.Lock()
	defer watch.errMu.Unlock()
	return watch.stopErr
}

func (watch *windowsSessionEvents) setError(err error) {
	watch.errMu.Lock()
	defer watch.errMu.Unlock()
	watch.stopErr = errors.Join(watch.stopErr, err)
}
