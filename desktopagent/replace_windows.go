// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExistingSnapshot = 0x1
	moveFileWriteThroughSnapshot    = 0x8
)

var (
	snapshotKernel32    = syscall.NewLazyDLL("kernel32.dll")
	snapshotMoveFileExW = snapshotKernel32.NewProc("MoveFileExW")
)

func replaceFile(source, destination string) error {
	if source == "" || destination == "" || !filepath.IsAbs(source) || !filepath.IsAbs(destination) {
		return errors.New("Windows atomic replacement requires absolute source and destination paths")
	}
	if !strings.EqualFold(filepath.Clean(filepath.Dir(source)), filepath.Clean(filepath.Dir(destination))) {
		return errors.New("Windows atomic replacement must stay within one private directory")
	}
	parent := filepath.Dir(source)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !privateDirectoryPermissionsOK(parent, parentInfo) {
		return errors.New("Windows atomic replacement parent is not an exact private directory")
	}
	sourceChain, err := inspectWindowsPathChain(source, false)
	if err != nil || !verifyPrivateWindowsPath(source, false) {
		return errors.New("Windows atomic replacement source is not an exact private regular file")
	}
	sourceIdentity := sourceChain[len(sourceChain)-1].identity
	if destinationInfo, destinationErr := os.Lstat(destination); destinationErr == nil {
		if !privateFilePermissionsOK(destination, destinationInfo) {
			return errors.New("Windows atomic replacement destination is not an exact private regular file")
		}
	} else if !errors.Is(destinationErr, os.ErrNotExist) {
		return destinationErr
	}
	sourceUTF16, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationUTF16, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := snapshotMoveFileExW.Call(
		uintptr(unsafe.Pointer(sourceUTF16)), uintptr(unsafe.Pointer(destinationUTF16)),
		moveFileReplaceExistingSnapshot|moveFileWriteThroughSnapshot,
	)
	if result == 0 {
		return fmt.Errorf("replace private Windows file: %w", callErr)
	}
	if err := setAndVerifyPrivateWindowsPath(destination, false); err != nil {
		return fmt.Errorf("verify private Windows destination after replacement: %w", err)
	}
	destinationChain, err := inspectWindowsPathChain(destination, false)
	if err != nil || destinationChain[len(destinationChain)-1].identity != sourceIdentity {
		return errors.New("Windows atomic replacement did not preserve the validated source file identity")
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Windows atomic replacement source still resolves after replacement")
	}
	return nil
}

func syncParentDirectory(string) error { return nil }
