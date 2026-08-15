// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func DefaultPaths() (Paths, error) {
	localAppData, err := knownWindowsFolder(windows.FOLDERID_LocalAppData)
	if err != nil {
		return Paths{}, err
	}
	state := filepath.Join(localAppData, "YunPinIME", "sync")
	appData, err := knownWindowsFolder(windows.FOLDERID_RoamingAppData)
	if err != nil {
		return Paths{}, err
	}
	rime := filepath.Join(appData, "YunPin", "Rime")
	return Paths{
		StateDirectory: state, EndpointConfigPath: filepath.Join(state, "sync.json"),
		DatabasePath: filepath.Join(state, "private.db"), LockPath: filepath.Join(state, "agent.lock"),
		CredentialService: "YunPinIME.sync",
		NativeEventsPath:  filepath.Join(state, "native-events", "incoming"),
		BaselinePath:      filepath.Join(rime, "yunpin", "baseline.tsv"),
		SnapshotPath:      filepath.Join(rime, "yunpin", "private.tsv"),
		SnapshotStatePath: filepath.Join(state, "snapshot-generation"),
	}, nil
}
