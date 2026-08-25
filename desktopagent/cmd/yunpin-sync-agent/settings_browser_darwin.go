// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package main

import "os/exec"

func openLocalSettingsURL(pageURL string) error {
	return exec.Command("/usr/bin/open", pageURL).Start()
}
