// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import "os"

// Windows executability is determined by the PE loader and file extension; the
// POSIX execute bits reported by os.FileInfo are not populated for normal .exe
// files and must not reject the fixed deployer path before it can be invoked.
func platformReloadExecutableOK(_ os.FileInfo) bool { return true }
