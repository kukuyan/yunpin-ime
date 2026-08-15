// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package desktopagent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func bridgePathComponentsOK(path string, targetDirectory bool) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(current)
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	opened := make([]int, 0, len(components))
	defer func() {
		for _, descriptor := range opened {
			_ = unix.Close(descriptor)
		}
	}()
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			return false
		}
		final := index == len(components)-1
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
		if !final || targetDirectory {
			flags |= unix.O_DIRECTORY
		}
		next, openErr := unix.Openat(current, component, flags, 0)
		if openErr != nil {
			return false
		}
		opened = append(opened, next)
		current = next
		var status unix.Stat_t
		if unix.Fstat(current, &status) != nil {
			return false
		}
		mode := status.Mode & unix.S_IFMT
		if (!final || targetDirectory) && mode != unix.S_IFDIR {
			return false
		}
		if final && !targetDirectory && mode != unix.S_IFREG {
			return false
		}
	}
	return true
}

func hardenExistingPrivateDirectory(path string) error {
	if !bridgePathComponentsOK(path, true) {
		return errors.New("private directory path contains a symlink or non-directory component")
	}
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return errors.New("private directory must be a real directory")
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	opened, err := directory.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return errors.New("private directory changed during validated open")
	}
	if err := directory.Chmod(0700); err != nil || !openedPrivateFilePermissionsOK(path, directory, true) {
		return errors.New("private directory permissions could not be verified")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, after) {
		return errors.New("private directory identity changed during protection")
	}
	return nil
}
