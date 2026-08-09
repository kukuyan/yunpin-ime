// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

type AccountRelay interface {
	CreateAccount(context.Context, syncclient.AccountRegistration) (syncclient.Account, error)
	PutKeyring(context.Context, string, uint64, protocol.SealedBox) error
}

type StoreInitializer func(context.Context, string, []byte, []byte, string) error

type InitAccountOptions struct {
	Secrets         SecretStore
	Profile         string
	DatabasePath    string
	Random          io.Reader
	InitializeStore StoreInitializer
}

type InitAccountResult struct {
	AccountIDHex string
	RecoveryKey  string
}

func initializeEncryptedStore(ctx context.Context, path string, dataKey, objectIDKey []byte, deviceID string) error {
	if err := preflightDatabasePath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create local database directory: %w", err)
	}
	store, err := localstore.OpenForDevice(ctx, path, dataKey, objectIDKey, deviceID)
	if err != nil {
		return err
	}
	return store.Close()
}

func preflightDatabasePath(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("local database path must be absolute")
	}
	if _, err := os.Lstat(path); err == nil {
		return errors.New("local database already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect local database path: %w", err)
	}
	return nil
}

func fillRandom(source io.Reader, destinations ...[]byte) error {
	if source == nil {
		return errors.New("cryptographic random source is required")
	}
	for _, destination := range destinations {
		if _, err := io.ReadFull(source, destination); err != nil {
			return fmt.Errorf("generate account secret: %w", err)
		}
	}
	return nil
}

// InitAccount is intentionally fail-closed. The relay has no account-delete
// rollback, so exposing a production switch for the non-atomic flow could
// strand an unrecoverable account after a local Keychain/DPAPI or SQLite
// failure. The synthetic protocol tests call the unexported helper below.
func InitAccount(context.Context, AccountRelay, InitAccountOptions) (InitAccountResult, error) {
	return InitAccountResult{}, errors.New("account creation is disabled until the relay supports rollback-safe provisioning")
}

// initAccountForSyntheticProtocolTest exercises the future provisioning order
// against in-memory fakes. It is deliberately unexported so external library
// callers cannot opt into a remote write that production cannot roll back.
func initAccountForSyntheticProtocolTest(ctx context.Context, relay AccountRelay, options InitAccountOptions) (InitAccountResult, error) {
	if relay == nil || options.Secrets == nil {
		return InitAccountResult{}, errors.New("account relay and OS secret store are required")
	}
	if err := validateProfile(options.Profile); err != nil {
		return InitAccountResult{}, err
	}
	if existing, err := options.Secrets.Load(ctx, options.Profile); err == nil {
		zeroBytes(existing)
		return InitAccountResult{}, errors.New("profile is already initialized")
	} else if !errors.Is(err, ErrSecretNotFound) {
		return InitAccountResult{}, fmt.Errorf("preflight OS credential store: %w", err)
	}
	if options.Random == nil {
		return InitAccountResult{}, errors.New("cryptographic random source is required")
	}
	initializer := options.InitializeStore
	if initializer == nil {
		// Reject an unsafe or pre-existing path before the first relay write. The
		// initializer repeats this check immediately before opening SQLite.
		if err := preflightDatabasePath(options.DatabasePath); err != nil {
			return InitAccountResult{}, err
		}
		initializer = initializeEncryptedStore
	}

	keys, err := protocol.NewDeviceKeys(options.Random)
	if err != nil {
		return InitAccountResult{}, fmt.Errorf("generate device keys: %w", err)
	}
	defer zeroBytes(keys.X25519Private)
	defer zeroBytes(keys.Ed25519Private)

	recoveryKey := make([]byte, 32)
	epochKey := make([]byte, 32)
	objectIDKey := make([]byte, 32)
	localDataKey := make([]byte, 32)
	deviceNameCiphertext := make([]byte, 32)
	defer zeroBytes(recoveryKey)
	defer zeroBytes(epochKey)
	defer zeroBytes(objectIDKey)
	defer zeroBytes(localDataKey)
	defer zeroBytes(deviceNameCiphertext)
	if err := fillRandom(options.Random, recoveryKey, epochKey, objectIDKey, localDataKey, deviceNameCiphertext); err != nil {
		return InitAccountResult{}, err
	}
	_, recoveryAuthentication, err := protocol.DeriveRecoveryKeys(recoveryKey)
	if err != nil {
		return InitAccountResult{}, err
	}
	defer zeroBytes(recoveryAuthentication)
	account, err := relay.CreateAccount(ctx, syncclient.AccountRegistration{
		RecoveryAuthentication: recoveryAuthentication,
		DeviceRegistration: syncclient.DeviceRegistration{
			// The minimum preview uses an opaque random label. A domain-separated
			// encrypted human device name belongs with the future trust roster.
			DeviceNameCiphertext: deviceNameCiphertext,
			Ed25519PublicKey:     keys.Ed25519Public,
			X25519PublicKey:      keys.X25519Public,
		},
	})
	if err != nil {
		return InitAccountResult{}, fmt.Errorf("create sync account: %w", err)
	}
	keyring, err := protocol.SealRecoveryPackage(recoveryKey, protocol.RecoveryPackage{
		AccountID: account.AccountID, CurrentEpoch: 1, EpochKey: epochKey, ObjectIDKey: objectIDKey,
	}, options.Random)
	if err != nil {
		return InitAccountResult{}, err
	}
	if err := relay.PutKeyring(ctx, account.DeviceToken, 1, keyring); err != nil {
		return InitAccountResult{}, fmt.Errorf("store recovery keyring: %w", err)
	}
	if err := initializer(ctx, options.DatabasePath, localDataKey, objectIDKey, hex.EncodeToString(account.DeviceID)); err != nil {
		return InitAccountResult{}, fmt.Errorf("initialize encrypted local database: %w", err)
	}

	bundle := CredentialBundleV1{
		Version:          CredentialBundleVersion,
		DeviceToken:      append([]byte(nil), account.DeviceToken...),
		CurrentEpoch:     1,
		EpochKeys:        map[uint64][32]byte{1: {}},
		VerificationKeys: make(map[[16]byte][ed25519.PublicKeySize]byte, 1),
	}
	defer bundle.Zero()
	if len(account.AccountID) == len(bundle.AccountID) {
		copy(bundle.AccountID[:], account.AccountID)
	}
	if len(account.DeviceID) == len(bundle.DeviceID) {
		copy(bundle.DeviceID[:], account.DeviceID)
	}
	copy(bundle.SigningSeed[:], keys.Ed25519Private.Seed())
	copy(bundle.X25519Private[:], keys.X25519Private)
	copy(bundle.LocalDataKey[:], localDataKey)
	copy(bundle.ObjectIDKey[:], objectIDKey)
	epochOne := bundle.EpochKeys[1]
	copy(epochOne[:], epochKey)
	bundle.EpochKeys[1] = epochOne
	var self [ed25519.PublicKeySize]byte
	copy(self[:], keys.Ed25519Public)
	bundle.VerificationKeys[bundle.DeviceID] = self
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		return InitAccountResult{}, fmt.Errorf("assemble device credential: %w", err)
	}
	defer zeroBytes(encoded)
	if err := options.Secrets.Save(ctx, options.Profile, encoded); err != nil {
		return InitAccountResult{}, fmt.Errorf("commit device credential: %w", err)
	}
	recoveryText, err := protocol.EncodeRecoveryKey(recoveryKey)
	if err != nil {
		return InitAccountResult{}, err
	}
	return InitAccountResult{AccountIDHex: hex.EncodeToString(account.AccountID), RecoveryKey: recoveryText}, nil
}

type Agent struct {
	Secrets            SecretStore
	Profile            string
	EndpointConfigPath string
	DatabasePath       string
	MaxRounds          int
}

type Status struct {
	Ready              bool `json:"ready"`
	CredentialVersion  int  `json:"credential_version"`
	EndpointConfigured bool `json:"endpoint_configured"`
	DatabasePresent    bool `json:"database_present"`
}

func (agent Agent) loadBundle(ctx context.Context) (CredentialBundleV1, error) {
	if agent.Secrets == nil {
		return CredentialBundleV1{}, errors.New("OS secret store is required")
	}
	if err := validateProfile(agent.Profile); err != nil {
		return CredentialBundleV1{}, err
	}
	encoded, err := agent.Secrets.Load(ctx, agent.Profile)
	if err != nil {
		return CredentialBundleV1{}, err
	}
	defer zeroBytes(encoded)
	return DecodeCredentialBundle(encoded)
}

// Status validates local configuration and credential material without making
// a network request or exposing identifiers, tokens, or key bytes.
func (agent Agent) Status(ctx context.Context) (Status, error) {
	bundle, err := agent.loadBundle(ctx)
	if err != nil {
		return Status{}, err
	}
	defer bundle.Zero()
	if _, err := syncclient.LoadEndpointConfig(agent.EndpointConfigPath); err != nil {
		return Status{}, err
	}
	if agent.DatabasePath == "" || !filepath.IsAbs(agent.DatabasePath) {
		return Status{}, errors.New("encrypted local database path must be absolute")
	}
	info, err := os.Lstat(agent.DatabasePath)
	if err != nil {
		return Status{}, fmt.Errorf("inspect encrypted local database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Status{}, errors.New("encrypted local database must be a regular file")
	}
	return Status{
		Ready: true, CredentialVersion: int(bundle.Version), EndpointConfigured: true, DatabasePresent: true,
	}, nil
}

type SyncSummary struct {
	Rounds     int
	Uploaded   int
	Downloaded int
	Cursor     int64
}

func sessionFromBundle(bundle CredentialBundleV1) syncclient.Session {
	epochs := make(map[uint64][]byte, len(bundle.EpochKeys))
	for epoch, key := range bundle.EpochKeys {
		epochs[epoch] = append([]byte(nil), key[:]...)
	}
	verification := make(map[string]ed25519.PublicKey, len(bundle.VerificationKeys))
	for device, key := range bundle.VerificationKeys {
		verification[hex.EncodeToString(device[:])] = append(ed25519.PublicKey(nil), key[:]...)
	}
	return syncclient.Session{
		AccountID: append([]byte(nil), bundle.AccountID[:]...), DeviceID: append([]byte(nil), bundle.DeviceID[:]...),
		DeviceToken: string(bundle.DeviceToken), KeyEpoch: bundle.CurrentEpoch, EpochKeys: epochs,
		SigningPrivate: ed25519.NewKeyFromSeed(bundle.SigningSeed[:]), VerificationKeys: verification,
	}
}

func zeroSession(session *syncclient.Session) {
	if session == nil {
		return
	}
	zeroBytes(session.AccountID)
	zeroBytes(session.DeviceID)
	zeroBytes(session.SigningPrivate)
	for epoch, key := range session.EpochKeys {
		zeroBytes(key)
		delete(session.EpochKeys, epoch)
	}
	for device, key := range session.VerificationKeys {
		zeroBytes(key)
		delete(session.VerificationKeys, device)
	}
	session.DeviceToken = ""
}

// SyncOnce is the only network-bearing desktop operation. Native input event
// handlers never call it; they continue using their current memory snapshot.
func (agent Agent) SyncOnce(ctx context.Context) (SyncSummary, error) {
	bundle, err := agent.loadBundle(ctx)
	if err != nil {
		return SyncSummary{}, err
	}
	defer bundle.Zero()
	endpoint, err := syncclient.LoadEndpointConfig(agent.EndpointConfigPath)
	if err != nil {
		return SyncSummary{}, err
	}
	if agent.DatabasePath == "" || !filepath.IsAbs(agent.DatabasePath) {
		return SyncSummary{}, errors.New("encrypted local database path must be absolute")
	}
	info, err := os.Lstat(agent.DatabasePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return SyncSummary{}, errors.New("encrypted local database must be an existing regular file")
	}
	store, err := localstore.OpenForDevice(
		ctx, agent.DatabasePath, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex(),
	)
	if err != nil {
		return SyncSummary{}, err
	}
	defer store.Close()
	session := sessionFromBundle(bundle)
	defer zeroSession(&session)
	maxRounds := agent.MaxRounds
	if maxRounds == 0 {
		maxRounds = 32
	}
	worker := syncclient.Worker{Client: syncclient.New(endpoint), Store: store, Session: session}
	results, err := worker.SyncUntilIdle(ctx, maxRounds)
	if err != nil {
		return SyncSummary{}, err
	}
	summary := SyncSummary{Rounds: len(results)}
	for _, result := range results {
		if result.Uploaded {
			summary.Uploaded++
		}
		summary.Downloaded += result.Downloaded
		summary.Cursor = result.Cursor
	}
	return summary, nil
}
