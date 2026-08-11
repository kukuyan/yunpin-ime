// SPDX-License-Identifier: Apache-2.0
//go:build windows

package replaylab

import (
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var (
	kernel32    = syscall.NewLazyDLL("kernel32.dll")
	moveFileExW = kernel32.NewProc("MoveFileExW")
)

func replaceFile(source, destination string) error {
	sourceUTF16, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationUTF16, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(sourceUTF16)),
		uintptr(unsafe.Pointer(destinationUTF16)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		return callErr
	}
	return nil
}
