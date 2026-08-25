// SPDX-License-Identifier: Apache-2.0
// Package mobilecore is the background-only YunPin mobile sync facade.
// Keyboard key handlers must never call this package.
package mobilecore

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

const (
	defaultMaxRounds = 8
	maximumMaxRounds = 64
)

// Options contains only one selected relay profile and app-private paths.
// Credential is an opaque YPCB bundle loaded by the native Keychain/Keystore
// adapter for this run. The core never persists it.
type Options struct {
	DatabasePath     string
	SnapshotPath     string
	Endpoint         string
	AllowPrivateHTTP bool
	Credential       []byte
	MaxRounds        int

	// Transport exists for deterministic host tests. Native callers leave it
	// nil so syncclient applies its redirect and timeout policy.
	Transport http.RoundTripper
}

// Core owns one encrypted local database and one in-memory device session.
// A Core should be short lived: open it after the native secret store unlocks,
// perform one bounded operation, then Close it to overwrite mutable key buffers
// and release the session. Go strings, including the bearer required by the
// existing syncclient API, cannot be guaranteed to be scrubbed in place.
type Core struct {
	mu           sync.Mutex
	store        *localstore.Store
	client       *syncclient.Client
	session      syncclient.Session
	snapshotPath string
	maxRounds    int
	closed       bool
}

// SyncReport contains only operational counters. It deliberately excludes
// phrases, endpoint addresses, account/device identifiers and key material.
type SyncReport struct {
	Rounds          int
	Uploaded        int
	Downloaded      int
	Cursor          int64
	Pending         uint64
	SnapshotRows    int
	SnapshotChanged bool
}

// Status is safe to show in a containing app or persist as redacted telemetry.
type Status struct {
	Cursor           int64
	Pending          uint64
	Prepared         bool
	SnapshotPresent  bool
	RollbackPresent  bool
	ControlPlaneGate string
}

func validatePrivatePath(path, label string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%s path must be absolute", label)
	}
	// localstore currently supplies the path through SQLite's `file:` URI
	// syntax. Reject URI metacharacters so the path validated here is exactly
	// the filesystem object SQLite opens.
	if strings.ContainsAny(path, "\x00%?#") {
		return fmt.Errorf("%s path contains unsafe URI characters", label)
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create private %s directory: %w", label, err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("protect private %s directory: %w", label, err)
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("private %s directory is invalid", label)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("private %s path is not a regular file", label)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect private %s path: %w", label, err)
	}
	return nil
}

func validateSeparatedStorageRoots(databasePath, snapshotPath string) error {
	databaseRoot := filepath.Clean(filepath.Dir(databasePath))
	snapshotRoot := filepath.Clean(filepath.Dir(snapshotPath))
	if strings.EqualFold(databaseRoot, snapshotRoot) {
		return errors.New("database and snapshot directories must be distinct")
	}
	databaseInfo, databaseErr := os.Stat(databaseRoot)
	snapshotInfo, snapshotErr := os.Stat(snapshotRoot)
	if databaseErr == nil && snapshotErr == nil && os.SameFile(databaseInfo, snapshotInfo) {
		return errors.New("database and snapshot directories must be distinct")
	}
	return nil
}

func validateDistinctPrivatePaths(paths ...string) error {
	cleaned := make([]string, len(paths))
	infos := make([]os.FileInfo, len(paths))
	for index, path := range paths {
		cleaned[index] = filepath.Clean(path)
		info, err := os.Lstat(path)
		if err == nil {
			infos[index] = info
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("inspect private mobile path identity")
		}
	}
	for left := range cleaned {
		for right := left + 1; right < len(cleaned); right++ {
			// EqualFold is deliberately conservative on case-sensitive systems;
			// mobile app paths never need names that differ only by case.
			if strings.EqualFold(cleaned[left], cleaned[right]) ||
				(infos[left] != nil && infos[right] != nil && os.SameFile(infos[left], infos[right])) {
				return errors.New("private mobile state paths must be distinct")
			}
		}
	}
	return nil
}

func sessionFromBundle(bundle desktopagent.CredentialBundleV1) syncclient.Session {
	epochs := make(map[uint64][]byte, len(bundle.EpochKeys))
	for epoch, key := range bundle.EpochKeys {
		epochs[epoch] = append([]byte(nil), key[:]...)
	}
	verification := make(map[string]ed25519.PublicKey, len(bundle.VerificationKeys))
	for device, key := range bundle.VerificationKeys {
		verification[hex.EncodeToString(device[:])] = append(ed25519.PublicKey(nil), key[:]...)
	}
	return syncclient.Session{
		AccountID:        append([]byte(nil), bundle.AccountID[:]...),
		DeviceID:         append([]byte(nil), bundle.DeviceID[:]...),
		DeviceToken:      string(bundle.DeviceToken),
		KeyEpoch:         bundle.CurrentEpoch,
		EpochKeys:        epochs,
		SigningPrivate:   ed25519.NewKeyFromSeed(bundle.SigningSeed[:]),
		VerificationKeys: verification,
	}
}

func zeroBytes(value []byte) {
	clear(value)
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

// Open validates the selected endpoint and opaque paired-device credential,
// then opens the existing encrypted queue or creates a new app-private one.
// It never invokes account creation, recovery or pairing.
func Open(ctx context.Context, options Options) (_ *Core, returnErr error) {
	if err := validatePrivatePath(options.DatabasePath, "database"); err != nil {
		return nil, err
	}
	if err := validatePrivatePath(options.SnapshotPath, "snapshot"); err != nil {
		return nil, err
	}
	if err := validateSeparatedStorageRoots(options.DatabasePath, options.SnapshotPath); err != nil {
		return nil, err
	}
	if err := validateDistinctPrivatePaths(
		options.DatabasePath,
		options.DatabasePath+"-wal",
		options.DatabasePath+"-shm",
		options.DatabasePath+"-journal",
		options.SnapshotPath,
		snapshotRollbackPath(options.SnapshotPath),
	); err != nil {
		return nil, err
	}
	endpoint, err := syncclient.ParseEndpoint(options.Endpoint, syncclient.EndpointPolicy{
		AllowPrivateHTTP: options.AllowPrivateHTTP,
	})
	if err != nil {
		return nil, err
	}
	if len(options.Credential) == 0 {
		return nil, errors.New("paired-device credential is required")
	}
	bundle, err := desktopagent.DecodeCredentialBundle(options.Credential)
	if err != nil {
		return nil, errors.New("paired-device credential is invalid")
	}
	defer bundle.Zero()
	if bundle.Version != desktopagent.CredentialBundleVersion || bundle.TrustedRoster.Version == 0 || len(bundle.TrustedRoster.Devices) == 0 {
		return nil, errors.New("finalized signed device trust is required")
	}
	store, err := localstore.OpenForDevice(ctx, options.DatabasePath, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex())
	if err != nil {
		return nil, fmt.Errorf("open encrypted mobile store: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = store.Close()
		}
	}()
	if err := os.Chmod(options.DatabasePath, 0o600); err != nil {
		return nil, fmt.Errorf("protect encrypted mobile store: %w", err)
	}
	maxRounds := options.MaxRounds
	if maxRounds == 0 {
		maxRounds = defaultMaxRounds
	}
	if maxRounds < 1 || maxRounds > maximumMaxRounds {
		return nil, errors.New("mobile sync round limit is invalid")
	}
	clientOptions := make([]syncclient.Option, 0, 1)
	if options.Transport != nil {
		clientOptions = append(clientOptions, syncclient.WithTransport(options.Transport))
	}
	return &Core{
		store:        store,
		client:       syncclient.New(endpoint, clientOptions...),
		session:      sessionFromBundle(bundle),
		snapshotPath: options.SnapshotPath,
		maxRounds:    maxRounds,
	}, nil
}

func (core *Core) requireOpen() error {
	if core == nil || core.closed || core.store == nil || core.client == nil {
		return errors.New("mobile sync core is closed")
	}
	return nil
}

func normalizeMobilePhrase(text, pinyin string) (string, string, error) {
	if !validSnapshotText(text) || !validSnapshotText(pinyin) {
		return "", "", errors.New("mobile phrase identity is invalid")
	}
	text = strings.TrimSpace(text)
	pinyin = protocol.CanonicalPinyin(pinyin)
	if protocol.CanonicalPhrase(text) == "" || !validSnapshotText(text) ||
		!validSnapshotPinyin(pinyin) {
		return "", "", errors.New("mobile phrase identity is invalid")
	}
	return text, pinyin, nil
}

// Close best-effort overwrites mutable key copies and closes the encrypted
// database. It is safe to call more than once.
func (core *Core) Close() error {
	if core == nil {
		return nil
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.closed {
		return nil
	}
	core.closed = true
	zeroSession(&core.session)
	if core.store == nil {
		return nil
	}
	err := core.store.Close()
	core.store = nil
	core.client = nil
	return err
}

// RecordSelection appends only the selected phrase identity and aggregate
// count. Raw keystrokes, surrounding text and application identity are never
// accepted. Protected contexts produce no write and no queue item.
func (core *Core) RecordSelection(ctx context.Context, text, pinyin string, learning localstore.LearningContext) (localstore.LearnResult, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	if err := core.requireOpen(); err != nil {
		return localstore.LearnResult{}, err
	}
	text, pinyin, err := normalizeMobilePhrase(text, pinyin)
	if err != nil {
		return localstore.LearnResult{}, err
	}
	return core.store.RecordSelection(ctx, localstore.Phrase{Text: text, Pinyin: pinyin}, learning)
}

// SaveExplicit queues one user-reviewed phrase. It is a containing-app action,
// never an input-key callback.
func (core *Core) SaveExplicit(ctx context.Context, text, pinyin string, useCount uint64, pinned bool) error {
	core.mu.Lock()
	defer core.mu.Unlock()
	if err := core.requireOpen(); err != nil {
		return err
	}
	text, pinyin, err := normalizeMobilePhrase(text, pinyin)
	if err != nil {
		return err
	}
	return core.store.SaveExplicit(ctx, localstore.Phrase{
		Text: text, Pinyin: pinyin, Source: "manual", UseCount: useCount, Pinned: pinned,
	})
}

// Delete queues a remove-wins tombstone. Explicit re-add remains a distinct
// later operation in localstore.
func (core *Core) Delete(ctx context.Context, text, pinyin string) error {
	core.mu.Lock()
	defer core.mu.Unlock()
	if err := core.requireOpen(); err != nil {
		return err
	}
	text, pinyin, err := normalizeMobilePhrase(text, pinyin)
	if err != nil {
		return err
	}
	return core.store.Delete(ctx, text, pinyin)
}

// Sync performs bounded background work and publishes a new immutable
// snapshot only after all accepted/verified local merges are durable.
func (core *Core) Sync(ctx context.Context) (SyncReport, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	if err := core.requireOpen(); err != nil {
		return SyncReport{}, err
	}
	worker := syncclient.Worker{Client: core.client, Store: core.store, Session: core.session}
	results, err := worker.SyncUntilIdle(ctx, core.maxRounds)
	report := SyncReport{Rounds: len(results)}
	for _, result := range results {
		if result.Uploaded {
			report.Uploaded++
		}
		report.Downloaded += result.Downloaded
		report.Cursor = result.Cursor
	}
	if pending, pendingErr := core.store.PendingEventCount(ctx); pendingErr == nil {
		report.Pending = pending
	}
	if err == nil || len(results) > 0 {
		published, publishErr := publishSnapshot(ctx, core.store, core.snapshotPath)
		report.SnapshotRows = published.Rows
		report.SnapshotChanged = published.Changed
		if publishErr != nil {
			if err != nil {
				return report, errors.Join(err, publishErr)
			}
			return report, publishErr
		}
	}
	return report, err
}

// PublishSnapshot rebuilds the immutable keyboard snapshot without network
// access. It is useful after a local manual edit.
func (core *Core) PublishSnapshot(ctx context.Context) (SnapshotReport, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	if err := core.requireOpen(); err != nil {
		return SnapshotReport{}, err
	}
	return publishSnapshot(ctx, core.store, core.snapshotPath)
}

// Status reads only redacted operational state and performs no network I/O.
func (core *Core) Status(ctx context.Context) (Status, error) {
	core.mu.Lock()
	defer core.mu.Unlock()
	if err := core.requireOpen(); err != nil {
		return Status{}, err
	}
	state, err := core.store.LoadSyncState(ctx)
	if err != nil {
		return Status{}, err
	}
	pending, err := core.store.PendingEventCount(ctx)
	if err != nil {
		return Status{}, err
	}
	return Status{
		Cursor:           state.Cursor,
		Pending:          pending,
		Prepared:         state.Prepared != nil,
		SnapshotPresent:  validatedSnapshotPresent(core.snapshotPath),
		RollbackPresent:  validatedSnapshotPresent(snapshotRollbackPath(core.snapshotPath)),
		ControlPlaneGate: "signed_roster_chain_required",
	}, nil
}

// RollbackSnapshot restores the last atomically retained snapshot. It never
// changes the encrypted queue, cursor or credential.
func (core *Core) RollbackSnapshot() error {
	core.mu.Lock()
	defer core.mu.Unlock()
	if err := core.requireOpen(); err != nil {
		return err
	}
	return rollbackSnapshot(core.snapshotPath)
}
