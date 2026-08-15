// SPDX-License-Identifier: Apache-2.0
//go:build yunpin_pairing_private && windows

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

func publishEmptyBaselineNoReplace(source, destination string) error {
	if source == "" || destination == "" || !filepath.IsAbs(source) || !filepath.IsAbs(destination) ||
		!strings.EqualFold(filepath.Clean(filepath.Dir(source)), filepath.Clean(filepath.Dir(destination))) {
		return errors.New("Windows empty baseline publish requires one fixed absolute directory")
	}
	chain, err := inspectWindowsPathChain(source, false)
	if err != nil || !verifyPrivateWindowsPath(source, false) {
		return errors.New("Windows empty baseline source is not an exact private file")
	}
	identity := chain[len(chain)-1].identity
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
		moveFileWriteThroughSnapshot,
	)
	if result == 0 {
		return fmt.Errorf("move empty baseline without replacement: %w", callErr)
	}
	if !verifyPrivateWindowsPath(destination, false) {
		return errors.New("Windows empty baseline destination is not an exact private file")
	}
	destinationChain, err := inspectWindowsPathChain(destination, false)
	if err != nil || destinationChain[len(destinationChain)-1].identity != identity {
		return errors.New("Windows empty baseline publish did not preserve file identity")
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Windows empty baseline temporary still resolves after publish")
	}
	return nil
}
