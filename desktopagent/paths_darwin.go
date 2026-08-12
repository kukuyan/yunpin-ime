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
	rime := filepath.Join(home, "Library", "Application Support", "YunPin", "Rime")
	return Paths{
		StateDirectory: state, EndpointConfigPath: filepath.Join(state, "sync.json"),
		DatabasePath: filepath.Join(state, "private.db"), LockPath: filepath.Join(state, "agent.lock"),
		CredentialService: "io.github.kukuyan.inputmethod.YunPin.sync",
		NativeEventsPath:  filepath.Join(state, "native-events", "incoming"),
		BaselinePath:      filepath.Join(rime, "yunpin", "baseline.tsv"),
		SnapshotPath:      filepath.Join(rime, "yunpin", "private.tsv"),
		SnapshotStatePath: filepath.Join(state, "snapshot-generation"),
	}, nil
}
