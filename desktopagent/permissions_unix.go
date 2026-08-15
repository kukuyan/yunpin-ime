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

func makePrivateDirectory(path string) error { return os.MkdirAll(path, 0700) }

func ownedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func privateFilePermissionsOK(_ string, info os.FileInfo) bool {
	return ownedByCurrentUser(info) && info.Mode().Perm() == 0600
}

func privateDirectoryPermissionsOK(_ string, info os.FileInfo) bool {
	return ownedByCurrentUser(info) && info.Mode().Perm() == 0700
}

func openedPrivateFilePermissionsOK(_ string, file *os.File, directory bool) bool {
	if file == nil {
		return false
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() != directory || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if directory {
		return privateDirectoryPermissionsOK("", info)
	}
	return info.Mode().IsRegular() && privateFilePermissionsOK("", info)
}

func protectPrivateFile(file *os.File) error {
	if file == nil {
		return errors.New("private file handle is required")
	}
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if !openedPrivateFilePermissionsOK(file.Name(), file, false) {
		return errors.New("private file permissions could not be verified")
	}
	return nil
}

func removePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !privateFilePermissionsOK(path, info) {
		return errors.New("private file deletion requires an owned regular file")
	}
	return os.Remove(path)
}

func databaseSidecarPaths(path string) []string {
	return []string{path, path + "-wal", path + "-shm", path + "-journal"}
}

func verifyPrivateDatabaseFiles(path string) error {
	for index, candidate := range databaseSidecarPaths(path) {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) && index > 0 {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !privateFilePermissionsOK(candidate, info) {
			return fmt.Errorf("encrypted SQLite file %q is not private", candidate)
		}
	}
	return nil
}

func protectPrivateDatabaseFiles(path string) error {
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	for index, candidate := range databaseSidecarPaths(path) {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) && index > 0 {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("encrypted SQLite file %q is not regular", candidate)
		}
		if err := os.Chmod(candidate, 0600); err != nil {
			return err
		}
	}
	return verifyPrivateDatabaseFiles(path)
}
