// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrSecretNotFound      = errors.New("YunPin credential not found")
	ErrUnsupportedPlatform = errors.New("YunPin OS credential store is unsupported on this platform")
)

type SecretStore interface {
	Load(context.Context, string) ([]byte, error)
	Save(context.Context, string, []byte) error
	Delete(context.Context, string) error
}

// nonInteractiveSecretStore is implemented by platform stores that can
// explicitly forbid authentication UI. Background residents use this path so
// a locked store or a changed executable identity fails closed instead of
// creating a repeating password-dialog loop. Interactive commands continue to
// use SecretStore.Load and may therefore present the platform's normal,
// user-controlled authorization UI.
type nonInteractiveSecretStore interface {
	LoadWithoutUserInteraction(context.Context, string) ([]byte, error)
}

type PlatformSecretStoreOptions struct {
	Service   string
	Directory string
}

func validateProfile(profile string) error {
	if len(profile) < 1 || len(profile) > 64 {
		return errors.New("profile must contain between 1 and 64 characters")
	}
	for _, character := range profile {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.') {
			return errors.New("profile contains an unsafe character")
		}
	}
	return nil
}

func validateStoreOptions(options PlatformSecretStoreOptions) error {
	if strings.TrimSpace(options.Service) == "" || len(options.Service) > 128 || strings.ContainsRune(options.Service, 0) {
		return fmt.Errorf("credential service identifier is invalid")
	}
	return nil
}

func zeroBytes(value []byte) {
	clear(value)
}
