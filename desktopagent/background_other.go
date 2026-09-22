// SPDX-License-Identifier: Apache-2.0
//go:build !darwin && !windows

package desktopagent

import (
	"context"
	"errors"
	"os/exec"
)

func configureBackgroundCommand(*exec.Cmd) {}
func readPlatformBackgroundStatus(context.Context, Paths) (BackgroundStatus, error) {
	return BackgroundStatus{Problem: "background-unsupported"}, nil
}
func enablePlatformBackground(context.Context, Paths) error {
	return errors.New("background activation is available on macOS and Windows")
}
