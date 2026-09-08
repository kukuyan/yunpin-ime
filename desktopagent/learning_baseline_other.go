// SPDX-License-Identifier: Apache-2.0
//go:build !darwin && !linux && !windows

package desktopagent

import "errors"

func publishEmptyBaselineNoReplace(string, string) error {
	return errors.New("empty baseline initialization is unsupported on this platform")
}
