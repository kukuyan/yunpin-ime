// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

import (
	"os"
	"path/filepath"
)

func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	state := filepath.Join(home, "Library", "Application Support", "YunPin", "Sync")
	return Paths{
		StateDirectory: state, EndpointConfigPath: filepath.Join(state, "sync.json"),
		DatabasePath: filepath.Join(state, "private.db"), LockPath: filepath.Join(state, "agent.lock"),
		CredentialService: "io.github.kukuyan.inputmethod.YunPin.sync",
	}, nil
}
