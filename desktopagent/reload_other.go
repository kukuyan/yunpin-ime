// SPDX-License-Identifier: Apache-2.0
//go:build !darwin && !windows

package desktopagent

import "context"

// DefaultReloadHook is intentionally unavailable off the two packaged desktop
// targets; SyncOnce fails closed if a generated snapshot is pending reload.
func DefaultReloadHook() func(context.Context) error { return nil }
