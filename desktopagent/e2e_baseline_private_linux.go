// SPDX-License-Identifier: Apache-2.0
//go:build yunpin_pairing_private && linux

package desktopagent

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func publishEmptyBaselineNoReplace(source, destination string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	if err := syncParentDirectory(filepath.Dir(destination)); err != nil {
		return fmt.Errorf("sync baseline directory after no-replace publish: %w", err)
	}
	return nil
}
