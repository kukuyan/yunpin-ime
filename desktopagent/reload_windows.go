// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"context"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// DefaultReloadHook invokes the deployer in the fixed per-user preview
// installation root. It never consults PATH and never starts a shell.
func DefaultReloadHook() func(context.Context) error {
	root, err := knownWindowsFolder(windows.FOLDERID_LocalAppData)
	if err != nil {
		return func(context.Context) error {
			return fmt.Errorf("resolve fixed YunPin deployer location: %w", err)
		}
	}
	return executableReloadHook(filepath.Join(root, "Programs", "YunPinIME", "Preview", "current", "YunPinDeployer.exe"), "/deploy")
}
