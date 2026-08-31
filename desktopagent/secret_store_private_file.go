// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	privateCredentialFileSuffix = ".v1.local"
	privateCredentialLockName   = ".credentials.v1.lock"
	privateCredentialTombstone  = "yunpin-private-credential-deleted-v1\n"
)

// legacySecretLoader is deliberately read-only. The macOS Keychain remains a
// rollback source for the one installed default device credential until its
// migration commits. Login sessions and all new saves use only private files.
type legacySecretLoader interface {
	Load(context.Context, string) ([]byte, error)
}

// privateFileSecretStore keeps the opaque credential blob in the same private
// per-user state root as the encrypted local database. DefaultProfile has a
// single-file state machine: missing may migrate once, a credential is active,
// and a fixed tombstone permanently blocks legacy fallback after deletion.
type privateFileSecretStore struct {
	directory string
	legacy    legacySecretLoader
}

func newPrivateFileSecretStore(directory string, legacy legacySecretLoader) (*privateFileSecretStore, error) {
	if directory == "" || !filepath.IsAbs(directory) {
		return nil, errors.New("private credential directory must be absolute")
	}
	return &privateFileSecretStore{directory: filepath.Clean(directory), legacy: legacy}, nil
}

func (store *privateFileSecretStore) path(profile string) (string, error) {
	if err := validateProfile(profile); err != nil {
		return "", err
	}
	return filepath.Join(store.directory, "credentials-"+profile+privateCredentialFileSuffix), nil
}

func (store *privateFileSecretStore) validateExistingDirectory() error {
	info, err := os.Lstat(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		return ErrSecretNotFound
	}
	if err != nil {
		return fmt.Errorf("inspect private credential directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!privateDirectoryPermissionsOK(store.directory, info) {
		return errors.New("private credential directory must be owned by the current user with private permissions")
	}
	return nil
}

func (store *privateFileSecretStore) acquireLock(ctx context.Context) (processLock, error) {
	lockPath := filepath.Join(store.directory, privateCredentialLockName)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := acquireProcessLock(lockPath)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, ErrAlreadyRunning) {
			return nil, fmt.Errorf("acquire private credential lock: %w", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (store *privateFileSecretStore) withLock(ctx context.Context, operation func() error) (err error) {
	lock, err := store.acquireLock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()
	return operation()
}

func (store *privateFileSecretStore) loadPrimary(ctx context.Context, profile string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := store.path(profile)
	if err != nil {
		return nil, err
	}
	if err := store.validateExistingDirectory(); err != nil {
		return nil, err
	}
	value, err := readBoundedRegular(path, maxCredentialBlobBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSecretNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read private YunPin credential: %w", err)
	}
	if len(value) < 1 || len(value) > maxCredentialBlobBytes {
		zeroBytes(value)
		return nil, errors.New("private YunPin credential length is invalid")
	}
	if err := ctx.Err(); err != nil {
		zeroBytes(value)
		return nil, err
	}
	return value, nil
}

func (store *privateFileSecretStore) loadState(ctx context.Context, profile string) ([]byte, bool, error) {
	value, err := store.loadPrimary(ctx, profile)
	if err != nil {
		return nil, false, err
	}
	if profile == DefaultProfile && bytes.Equal(value, []byte(privateCredentialTombstone)) {
		zeroBytes(value)
		return nil, true, ErrSecretNotFound
	}
	return value, false, nil
}

func (store *privateFileSecretStore) savePrimary(ctx context.Context, profile string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(value) < 1 || len(value) > maxCredentialBlobBytes {
		return errors.New("credential value length is invalid")
	}
	path, err := store.path(profile)
	if err != nil {
		return err
	}
	if _, err := writeAtomicPrivateFile(path, value); err != nil {
		return fmt.Errorf("write private YunPin credential: %w", err)
	}
	return nil
}

func (store *privateFileSecretStore) deletePrimary(ctx context.Context, profile string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := store.path(profile)
	if err != nil {
		return err
	}
	if err := store.validateExistingDirectory(); err != nil {
		return err
	}
	if err := removePrivateFile(path); errors.Is(err, os.ErrNotExist) {
		return ErrSecretNotFound
	} else if err != nil {
		return fmt.Errorf("delete private YunPin credential: %w", err)
	}
	return nil
}

func validateLegacyMigration(profile string, value []byte) error {
	if profile != DefaultProfile {
		return errors.New("legacy credential migration is unavailable for this profile")
	}
	if len(value) < 1 || len(value) > maxCredentialBlobBytes {
		return errors.New("legacy YunPin credential length is invalid")
	}
	bundle, err := DecodeCredentialBundle(value)
	if err != nil {
		return errors.New("legacy YunPin device credential is invalid")
	}
	bundle.Zero()
	return nil
}

func (store *privateFileSecretStore) Load(ctx context.Context, profile string) (value []byte, err error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	err = store.withLock(ctx, func() error {
		current, tombstone, currentErr := store.loadState(ctx, profile)
		if currentErr == nil {
			value = current
			return nil
		}
		if !errors.Is(currentErr, ErrSecretNotFound) {
			return currentErr
		}
		if profile != DefaultProfile || tombstone || store.legacy == nil {
			return ErrSecretNotFound
		}

		// The lock is held across the only allowed interactive Keychain read and
		// atomic primary-file commit. A concurrent Save therefore waits and wins
		// after migration instead of being overwritten by the legacy value.
		legacyValue, loadErr := store.legacy.Load(ctx, profile)
		if loadErr != nil {
			return loadErr
		}
		defer zeroBytes(legacyValue)
		if err := validateLegacyMigration(profile, legacyValue); err != nil {
			return err
		}
		if err := store.savePrimary(ctx, profile, legacyValue); err != nil {
			return err
		}
		verified, blocked, verifyErr := store.loadState(context.WithoutCancel(ctx), profile)
		if verifyErr != nil || blocked {
			zeroBytes(verified)
			return fmt.Errorf("verify migrated YunPin credential: %w", verifyErr)
		}
		if !bytes.Equal(verified, legacyValue) {
			zeroBytes(verified)
			return errors.New("migrated YunPin credential changed during verification")
		}
		value = verified
		return nil
	})
	if err != nil {
		zeroBytes(value)
		return nil, err
	}
	return value, nil
}

func (store *privateFileSecretStore) LoadWithoutUserInteraction(ctx context.Context, profile string) ([]byte, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	// The primary file is atomically replaced, so a background read needs no
	// migration lock. It must return promptly even while an interactive Keychain
	// authorization is holding that lock, and it never accesses legacy storage.
	value, _, err := store.loadState(ctx, profile)
	return value, err
}

func (store *privateFileSecretStore) Save(ctx context.Context, profile string, value []byte) error {
	if err := validateProfile(profile); err != nil {
		return err
	}
	return store.withLock(ctx, func() error { return store.savePrimary(ctx, profile, value) })
}

func (store *privateFileSecretStore) Delete(ctx context.Context, profile string) error {
	if err := validateProfile(profile); err != nil {
		return err
	}
	return store.withLock(ctx, func() error {
		if profile != DefaultProfile {
			return store.deletePrimary(ctx, profile)
		}
		current, tombstone, loadErr := store.loadState(ctx, profile)
		zeroBytes(current)
		if loadErr != nil && !errors.Is(loadErr, ErrSecretNotFound) {
			return loadErr
		}
		if err := store.savePrimary(ctx, profile, []byte(privateCredentialTombstone)); err != nil {
			return err
		}
		if tombstone || errors.Is(loadErr, ErrSecretNotFound) {
			return ErrSecretNotFound
		}
		return nil
	})
}
