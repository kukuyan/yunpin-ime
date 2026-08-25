// SPDX-License-Identifier: Apache-2.0
//go:build windows

package main

import (
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func openLocalSettingsURL(pageURL string) error {
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	return exec.Command(filepath.Join(systemDirectory, "rundll32.exe"),
		"url.dll,FileProtocolHandler", pageURL).Start()
}
