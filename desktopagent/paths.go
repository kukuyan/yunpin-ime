// SPDX-License-Identifier: Apache-2.0
package desktopagent

type Paths struct {
	StateDirectory     string
	EndpointConfigPath string
	DatabasePath       string
	LockPath           string
	CredentialService  string
	NativeEventsPath   string
	BaselinePath       string
	SnapshotPath       string
	SnapshotStatePath  string
}

const DefaultProfile = "default"
