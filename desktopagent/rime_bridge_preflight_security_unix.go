// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package desktopagent

import (
	"errors"
	"os"
)

// hardenRimeBridgePreflightPath repairs only an already validated, current-user
// object left by a completed Rime backup. All permission changes are made
// through the same handle whose type, owner, and identity were checked.
func hardenRimeBridgePreflightPath(path string, directory bool) error {
	if !bridgePathComponentsOK(path, directory) {
		return errors.New("Rime preflight path contains a symlink or unsafe component")
	}
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || before.IsDir() != directory ||
		(!directory && !before.Mode().IsRegular()) {
		return errors.New("Rime preflight object has an unexpected type")
	}
	flags := os.O_RDONLY
	if !directory {
		flags = os.O_RDWR
	}
	file, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || opened.IsDir() != directory ||
		(!directory && !opened.Mode().IsRegular()) || !ownedByCurrentUser(opened) {
		return errors.New("Rime preflight object changed or is not owned by the current user")
	}
	mode := os.FileMode(0600)
	if directory {
		mode = 0700
	}
	if err := file.Chmod(mode); err != nil || !openedPrivateFilePermissionsOK(path, file, directory) {
		return errors.New("Rime preflight object permissions could not be protected")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) || !bridgePathComponentsOK(path, directory) {
		return errors.New("Rime preflight object identity changed during protection")
	}
	return nil
}
