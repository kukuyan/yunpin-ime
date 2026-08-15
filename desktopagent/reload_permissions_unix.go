// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package desktopagent

import "os"

func platformReloadExecutableOK(info os.FileInfo) bool {
	return info.Mode().Perm()&0111 != 0
}
