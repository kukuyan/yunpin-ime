// SPDX-License-Identifier: Apache-2.0
//go:build !darwin && !windows

package main

import "errors"

func openLocalSettingsURL(string) error {
	return errors.New("local settings page is supported on macOS and Windows")
}
