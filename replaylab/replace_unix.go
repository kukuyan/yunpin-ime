// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package replaylab

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
