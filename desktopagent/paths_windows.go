// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"errors"
	"os"
	"path/filepath"
)

func DefaultPaths() (Paths, error) {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" || !filepath.IsAbs(localAppData) {
		return Paths{}, errors.New("LOCALAPPDATA is unavailable")
	}
	state := filepath.Join(localAppData, "YunPinIME", "sync")
	return Paths{
		StateDirectory: state, EndpointConfigPath: filepath.Join(state, "sync.json"),
		DatabasePath: filepath.Join(state, "private.db"), LockPath: filepath.Join(state, "agent.lock"),
		CredentialService: "YunPinIME.sync",
	}, nil
}
