// SPDX-License-Identifier: Apache-2.0
//go:build windows && amd64

package desktopagent

import (
	"encoding/binary"
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	idleUser32            = windows.NewLazySystemDLL("user32.dll")
	idleKernel32          = windows.NewLazySystemDLL("kernel32.dll")
	idleWTSAPI32          = windows.NewLazySystemDLL("wtsapi32.dll")
	idleNTDLL             = windows.NewLazySystemDLL("ntdll.dll")
	idleGetLastInput      = idleUser32.NewProc("GetLastInputInfo")
	idleGetTickCount64    = idleKernel32.NewProc("GetTickCount64")
	idleGetCurrentPID     = idleKernel32.NewProc("GetCurrentProcessId")
	idleProcessSession    = idleKernel32.NewProc("ProcessIdToSessionId")
	idleQuerySession      = idleWTSAPI32.NewProc("WTSQuerySessionInformationW")
	idleEnumerateSessions = idleWTSAPI32.NewProc("WTSEnumerateSessionsW")
	idleFreeMemory        = idleWTSAPI32.NewProc("WTSFreeMemory")
	idleGetVersion        = idleNTDLL.NewProc("RtlGetVersion")
)

type idleLastInputInfo struct{ size, tick uint32 }
type idleVersionInfo struct {
	size, major, minor, build, platform uint32
	servicePack                         [128]uint16
}

// Native x64 WTS_SESSION_INFOW layout.
type idleSessionInfo struct {
	id      uint32
	station uintptr
	state   uint32
}

// observeWindowsIdle is restricted to the current process's interactive
// session. Any API error or another active session is a conservative wait.
// It observes but does not provide a host-side atomic maintenance gate.
func observeWindowsIdle(targetSession uint32) (windowsIdleSample, error) {
	var sample windowsIdleSample
	var version idleVersionInfo
	version.size = uint32(unsafe.Sizeof(version))
	status, _, _ := idleGetVersion.Call(uintptr(unsafe.Pointer(&version)))
	if status != 0 || version.major < 6 || (version.major == 6 && version.minor < 2) {
		return sample, errors.New("unsupported or unknown Windows session state semantics")
	}
	own, err := currentWindowsSessionID()
	if err != nil || own != targetSession {
		return sample, errors.New("target is not this process's interactive session")
	}
	var ptr uintptr
	var size uint32
	// WTSSessionInfoEx (25), WTS_CURRENT_SERVER_HANDLE (0).
	ok, _, _ := idleQuerySession.Call(0, uintptr(own), 25, uintptr(unsafe.Pointer(&ptr)), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || ptr == 0 {
		return sample, errors.New("session information unavailable")
	}
	defer idleFreeMemory.Call(ptr)
	// WTSINFOEXW's level-1 union is 8-byte aligned in the x64 ABI.
	if size < 20 {
		return sample, errors.New("session information truncated")
	}
	data := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), 20)
	if binary.LittleEndian.Uint32(data[0:4]) != 1 || binary.LittleEndian.Uint32(data[8:12]) != own {
		return sample, errors.New("session identity or information level mismatch")
	}
	state := binary.LittleEndian.Uint32(data[12:16])
	flags := binary.LittleEndian.Uint32(data[16:20])
	switch {
	case state == 4: // WTSDisconnected
		sample.state = windowsIdleDisconnected
	case state == 0 && flags == 0: // WTSActive, locked
		sample.state = windowsIdleLocked
	case state == 0 && flags == 1: // WTSActive, unlocked
		sample.state = windowsIdleActive
	default:
		return sample, errors.New("interactive session state is unknown")
	}
	var entries uintptr
	var count uint32
	ok, _, _ = idleEnumerateSessions.Call(0, 0, 1, uintptr(unsafe.Pointer(&entries)), uintptr(unsafe.Pointer(&count)))
	if ok == 0 || entries == 0 || count == 0 || count > 65536 {
		return windowsIdleSample{}, errors.New("session enumeration unavailable")
	}
	defer idleFreeMemory.Call(entries)
	found := false
	for _, e := range unsafe.Slice((*idleSessionInfo)(unsafe.Pointer(entries)), int(count)) {
		if e.id == own {
			found = true
			if e.state != state {
				return windowsIdleSample{}, errors.New("session state changed during observation")
			}
		}
		if e.id != own && e.state == 0 {
			sample.otherActive = true
		}
	}
	if !found {
		return windowsIdleSample{}, errors.New("target session not enumerated")
	}
	info := idleLastInputInfo{size: uint32(unsafe.Sizeof(idleLastInputInfo{}))}
	ok, _, _ = idleGetLastInput.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		return windowsIdleSample{}, errors.New("last input unavailable")
	}
	tick, _, _ := idleGetTickCount64.Call()
	sample.valid = true
	sample.sessionID = own
	sample.lastInputTick = info.tick
	sample.uptimeMS = uint64(tick)
	return sample, nil
}

func currentWindowsSessionID() (uint32, error) {
	pid, _, _ := idleGetCurrentPID.Call()
	var own uint32
	ok, _, _ := idleProcessSession.Call(pid, uintptr(unsafe.Pointer(&own)))
	if ok == 0 || own == 0 {
		return 0, errors.New("current process has no verified interactive session")
	}
	return own, nil
}
