// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessLock struct {
	handle windows.Handle
}

func openExclusiveWindowsLock(path string, disposition uint32, security *windows.SecurityAttributes) (windows.Handle, error) {
	encoded, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(
		encoded,
		windows.GENERIC_READ|windows.GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES|
			windows.READ_CONTROL|windows.WRITE_DAC|windows.WRITE_OWNER,
		0,
		security,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
}

func validateExclusiveWindowsLock(path string, handle windows.Handle) error {
	_, err := validatePrivateWindowsHandle(handle, false)
	if err != nil {
		return err
	}
	finalPath, err := finalWindowsPathForHandle(handle)
	if err != nil || !filepath.IsAbs(finalPath) || !strings.EqualFold(filepath.Clean(path), finalPath) {
		return errors.New("agent lock path and exclusive handle do not identify the same private file")
	}
	return nil
}

func acquireProcessLock(path string) (processLock, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("agent lock path must be absolute")
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("create agent state directory: %w", err)
	}
	sd, _, _, err := privateWindowsSecurityDescriptor(false)
	if err != nil {
		return nil, err
	}
	security := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	handle, createErr := openExclusiveWindowsLock(path, windows.CREATE_NEW, security)
	runtime.KeepAlive(sd)
	created := createErr == nil
	if errors.Is(createErr, windows.ERROR_FILE_EXISTS) || errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
		handle, err = openExclusiveWindowsLock(path, windows.OPEN_EXISTING, nil)
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrAlreadyRunning
		}
		if err != nil {
			return nil, fmt.Errorf("open existing agent lock: %w", err)
		}
	} else if createErr != nil {
		if errors.Is(createErr, windows.ERROR_SHARING_VIOLATION) || errors.Is(createErr, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("create agent lock: %w", createErr)
	}
	if created {
		if err := setPrivateWindowsSecurityOnHandle(handle, false); err != nil {
			windows.CloseHandle(handle)
			_ = os.Remove(path)
			return nil, fmt.Errorf("protect new agent lock: %w", err)
		}
	}
	if err := validateExclusiveWindowsLock(path, handle); err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("validate agent lock: %w", err)
	}
	return &windowsProcessLock{handle: handle}, nil
}

func (lock *windowsProcessLock) Release() error {
	if lock == nil || lock.handle == 0 || lock.handle == windows.InvalidHandle {
		return nil
	}
	err := windows.CloseHandle(lock.handle)
	lock.handle = 0
	return err
}
