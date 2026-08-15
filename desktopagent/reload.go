// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// executableReloadHook runs one platform-owned executable directly, without a
// shell or inherited standard streams. Arguments are compile-time constants in
// the platform-specific DefaultReloadHook implementations.
func executableReloadHook(path string, arguments ...string) func(context.Context) error {
	return func(ctx context.Context) error {
		if path == "" || !filepath.IsAbs(path) {
			return errors.New("reload executable path must be absolute")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !platformReloadExecutableOK(info) {
			return errors.New("reload executable must be an executable regular file")
		}
		command := exec.CommandContext(ctx, filepath.Clean(path), arguments...)
		command.Stdin = nil
		command.Stdout = nil
		command.Stderr = nil
		return command.Run()
	}
}
