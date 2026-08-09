// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package desktopagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type unixProcessLock struct {
	file *os.File
}

func acquireProcessLock(path string) (processLock, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("agent lock path must be absolute")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create agent state directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open agent lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("acquire agent lock: %w", err)
	}
	return &unixProcessLock{file: file}, nil
}

func (lock *unixProcessLock) Release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := syscall.Flock(int(lock.file.Fd()), syscall.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	if err != nil {
		return err
	}
	return closeErr
}
