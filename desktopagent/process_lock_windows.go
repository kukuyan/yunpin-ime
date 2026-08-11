// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const errorSharingViolation syscall.Errno = 32

type windowsProcessLock struct {
	handle syscall.Handle
}

func acquireProcessLock(path string) (processLock, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("agent lock path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create agent state directory: %w", err)
	}
	pathUTF16, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pathUTF16, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0,
	)
	if errors.Is(err, errorSharingViolation) {
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, fmt.Errorf("acquire agent lock: %w", err)
	}
	return &windowsProcessLock{handle: handle}, nil
}

func (lock *windowsProcessLock) Release() error {
	if lock == nil || lock.handle == 0 || lock.handle == syscall.InvalidHandle {
		return nil
	}
	err := syscall.CloseHandle(lock.handle)
	lock.handle = 0
	return err
}
