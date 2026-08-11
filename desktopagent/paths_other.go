// SPDX-License-Identifier: Apache-2.0
//go:build !darwin && !windows

package desktopagent

import (
	"os"
	"path/filepath"
)

func DefaultPaths() (Paths, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, err
	}
	state := filepath.Join(config, "yunpin", "sync")
	return Paths{
		StateDirectory: state, EndpointConfigPath: filepath.Join(state, "sync.json"),
		DatabasePath: filepath.Join(state, "private.db"), LockPath: filepath.Join(state, "agent.lock"),
		CredentialService: "io.github.kukuyan.inputmethod.YunPin.sync",
	}, nil
}
