// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

import "context"

// DefaultReloadHook asks the installed YunPin InputMethodKit host to deploy
// the atomically replaced snapshot. Both executable and argument are fixed;
// no shell or user-controlled command line is involved.
func DefaultReloadHook() func(context.Context) error {
	return executableReloadHook("/Library/Input Methods/YunPin.app/Contents/MacOS/YunPin", "--reload")
}
