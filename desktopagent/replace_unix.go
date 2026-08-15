// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package desktopagent

import "os"

func replaceFile(source, destination string) error { return os.Rename(source, destination) }

func syncParentDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
