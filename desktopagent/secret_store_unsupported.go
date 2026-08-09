// SPDX-License-Identifier: Apache-2.0
//go:build !darwin && !windows

package desktopagent

func NewPlatformSecretStore(options PlatformSecretStoreOptions) (SecretStore, error) {
	if err := validateStoreOptions(options); err != nil {
		return nil, err
	}
	return nil, ErrUnsupportedPlatform
}
