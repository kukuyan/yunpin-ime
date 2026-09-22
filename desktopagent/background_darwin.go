// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed install/macos/Enable-LaunchAgent.sh
var backgroundEnableScript []byte

const backgroundLabel = "io.github.kukuyan.inputmethod.YunPin.sync-agent"

func configureBackgroundCommand(*exec.Cmd) {}

func readPlatformBackgroundStatus(ctx context.Context, paths Paths) (BackgroundStatus, error) {
	state := BackgroundStatus{Supported: true}
	home, err := os.UserHomeDir()
	if err != nil {
		return state, err
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", backgroundLabel+".plist")
	info, err := os.Lstat(plist)
	if errors.Is(err, os.ErrNotExist) {
		state.Problem = "background-uninstalled"
		return state, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return state, errors.New("background registration is unavailable or unsafe")
	}
	encoded, err := backgroundCommand(ctx, "/usr/bin/plutil", "-extract", "ProgramArguments", "json", "-o", "-", plist)
	var args []string
	if err != nil || json.Unmarshal(encoded, &args) != nil || len(args) != 4 || args[0] != filepath.Join(paths.StateDirectory, "bin", "yunpin-sync-agent") || args[1] != "run" || args[2] != "--interval" || args[3] != "1m" {
		return state, errors.New("background registration does not match YunPin")
	}
	state.Installed = true
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	disabled, err := backgroundCommand(ctx, "/bin/launchctl", "print-disabled", domain)
	if err != nil {
		return state, err
	}
	state.Enabled = !strings.Contains(string(disabled), `"`+backgroundLabel+`" => true`) && !strings.Contains(string(disabled), `"`+backgroundLabel+`" => disabled`)
	loaded, err := backgroundCommand(ctx, "/bin/launchctl", "print", domain+"/"+backgroundLabel)
	if err == nil {
		for _, line := range strings.Split(string(loaded), "\n") {
			if strings.TrimSpace(line) == "state = running" {
				state.Running = true
				break
			}
		}
	}
	return state, nil
}

func enablePlatformBackground(ctx context.Context, paths Paths) error {
	return withBackgroundScript(paths, ".sh", backgroundEnableScript, func(name string) error {
		_, err := backgroundCommand(ctx, "/bin/sh", name)
		return err
	})
}
