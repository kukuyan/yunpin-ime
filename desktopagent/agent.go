// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
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

const defaultSyncMaxRounds = 1024

type Agent struct {
	Secrets              SecretStore
	Profile              string
	StateDirectory       string
	EndpointConfigPath   string
	DatabasePath         string
	MaxRounds            int
	NativeEventsPath     string
	RimeUserDBExportPath string
	RimeUserDBRefresh    func(context.Context) error
	BaselinePath         string
	SnapshotPath         string
	SnapshotStatePath    string
	Reload               func(context.Context) error
}

type Status struct {
	// Configuration readiness. These say the agent *can* run; they say nothing
	// about whether synchronization is actually working, and must not be read
	// as if they did.
	Ready              bool `json:"ready"`
	CredentialVersion  int  `json:"credential_version"`
	EndpointConfigured bool `json:"endpoint_configured"`
	DatabasePresent    bool `json:"database_present"`
	// HealthAvailable distinguishes an unreadable health record from a device
	// that has simply never synchronized.
	HealthAvailable bool `json:"health_available"`
	// EventLogAvailable reports whether the bounded redacted log path currently
	// satisfies its private regular-file contract. Logging remains optional and
	// never controls whether synchronization runs.
	EventLogAvailable bool `json:"event_log_available"`
	// Observed health of the background loop. Timestamps are Unix
	// milliseconds; zero means it has never happened. LastEventCode is a
	// bounded category, never an error string. A failed round leaves
	// LastSuccessAt untouched, so "when did this last work" survives the
	// failure that made the question worth asking.
	Health localstore.SyncHealth `json:"health"`
}

// ResidentReadiness is deliberately identifier-free. It is the only result
// emitted by the resident activation gate.
type ResidentReadiness struct {
	Ready bool `json:"ready"`
}

func rosterContainsDevice(roster protocol.PairingRoster, deviceID []byte) bool {
	for _, device := range roster.Devices {
		if bytes.Equal(device.DeviceID, deviceID) {
			return true
		}
	}
	return false
}

func (agent Agent) loadBundle(ctx context.Context) (CredentialBundleV1, error) {
	if agent.Secrets == nil {
		return CredentialBundleV1{}, errors.New("OS secret store is required")
	}
	if err := validateProfile(agent.Profile); err != nil {
		return CredentialBundleV1{}, err
	}
	return agent.loadBundleWith(ctx, agent.Secrets.Load)
}

func (agent Agent) loadResidentBundle(ctx context.Context) (CredentialBundleV1, error) {
	if agent.Secrets == nil {
		return CredentialBundleV1{}, errors.New("OS secret store is required")
	}
	if err := validateProfile(agent.Profile); err != nil {
		return CredentialBundleV1{}, err
	}
	nonInteractive, ok := agent.Secrets.(nonInteractiveSecretStore)
	if !ok {
		return CredentialBundleV1{}, errors.New("non-interactive OS secret access is unavailable")
	}
	return agent.loadBundleWith(ctx, nonInteractive.LoadWithoutUserInteraction)
}

func (agent Agent) loadBundleWith(
	ctx context.Context,
	load func(context.Context, string) ([]byte, error),
) (CredentialBundleV1, error) {
	encoded, err := load(ctx, agent.Profile)
	if err != nil {
		return CredentialBundleV1{}, err
	}
	defer zeroBytes(encoded)
	return DecodeCredentialBundle(encoded)
}

func (agent Agent) validateLocalState() error {
	if _, err := syncclient.LoadEndpointConfig(agent.EndpointConfigPath); err != nil {
		return err
	}
	if agent.DatabasePath == "" || !filepath.IsAbs(agent.DatabasePath) {
		return errors.New("encrypted local database path must be absolute")
	}
	info, err := os.Lstat(agent.DatabasePath)
	if err != nil {
		return fmt.Errorf("inspect encrypted local database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!privateFilePermissionsOK(agent.DatabasePath, info) {
		return errors.New("encrypted local database must be a private regular file")
	}
	if err := verifyPrivateDatabaseFiles(agent.DatabasePath); err != nil {
		return fmt.Errorf("verify encrypted local database files: %w", err)
	}
	return nil
}

// validateResidentDatabaseHeader rejects a merely permission-correct opaque
// file without opening SQLite, creating a WAL/SHM sidecar, or reading encrypted
// phrase rows. Full database access remains inside the locked resident run.
func (agent Agent) validateResidentDatabaseHeader() error {
	info, err := os.Lstat(agent.DatabasePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 512 {
		return errors.New("encrypted local database is not a SQLite database file")
	}
	file, err := os.Open(agent.DatabasePath)
	if err != nil {
		return errors.New("open encrypted local database header")
	}
	defer file.Close()
	if !openedPrivateFilePermissionsOK(agent.DatabasePath, file, false) {
		return errors.New("encrypted local database path and private handle differ")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return errors.New("encrypted local database changed during validated header read")
	}
	header := make([]byte, 100)
	if _, err := io.ReadFull(file, header); err != nil {
		return errors.New("read encrypted local database header")
	}
	defer zeroBytes(header)
	if !bytes.Equal(header[:16], []byte("SQLite format 3\x00")) ||
		(header[18] != 1 && header[18] != 2) || (header[19] != 1 && header[19] != 2) ||
		header[20] != 0 || header[21] != 64 || header[22] != 32 || header[23] != 32 {
		return errors.New("encrypted local database header is invalid")
	}
	pageSize := int64(binary.BigEndian.Uint16(header[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 || info.Size()%pageSize != 0 {
		return errors.New("encrypted local database page geometry is invalid")
	}
	schemaFormat := binary.BigEndian.Uint32(header[44:48])
	if schemaFormat < 1 || schemaFormat > 4 {
		return errors.New("encrypted local database schema header is invalid")
	}
	after, err := os.Lstat(agent.DatabasePath)
	if err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() ||
		!after.ModTime().Equal(opened.ModTime()) {
		return errors.New("encrypted local database changed during validated header read")
	}
	return nil
}

// validateResidentRimeState checks only fixed bridge metadata and filesystem
// identities. It does not invoke host maintenance or read vocabulary rows.
func (agent Agent) validateResidentRimeState() error {
	paths, err := DefaultRimeBridgePaths(Paths{
		StateDirectory: agent.StateDirectory,
		BaselinePath:   agent.BaselinePath,
	})
	if err != nil {
		return err
	}
	installationBytes, err := readBoundedRegular(paths.InstallationPath, maxRimeInstallationBytes)
	if err != nil {
		return err
	}
	installation, err := parseRimeInstallation(installationBytes)
	zeroBytes(installationBytes)
	if err != nil || !filepath.IsAbs(installation.SyncDir) ||
		filepath.Clean(installation.SyncDir) != paths.SyncDirectory {
		return errors.New("Rime maintenance output is not configured to the fixed directory")
	}
	backupBytes, err := readBoundedRegular(paths.BackupPath, maxRimeInstallationBytes)
	if err != nil {
		return err
	}
	backup, err := parseRimeInstallation(backupBytes)
	zeroBytes(backupBytes)
	if err != nil || backup.ID != installation.ID {
		return errors.New("Rime bridge backup does not match the active installation")
	}
	directory, err := os.Lstat(paths.SyncDirectory)
	if err != nil || !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 ||
		!privateDirectoryPermissionsOK(paths.SyncDirectory, directory) ||
		!bridgePathComponentsOK(paths.SyncDirectory, true) {
		return errors.New("Rime maintenance output is not a fixed private directory")
	}
	rootEntries, err := os.ReadDir(paths.SyncDirectory)
	if err != nil || len(rootEntries) > 1 {
		return errors.New("Rime maintenance output has unexpected device metadata")
	}
	if len(rootEntries) == 1 {
		entry := rootEntries[0]
		if entry.Name() != installation.ID || entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return errors.New("Rime maintenance output has unexpected device metadata")
		}
		deviceDirectory := filepath.Join(paths.SyncDirectory, installation.ID)
		deviceInfo, statErr := os.Lstat(deviceDirectory)
		if statErr != nil || !deviceInfo.IsDir() || deviceInfo.Mode()&os.ModeSymlink != 0 ||
			!privateDirectoryPermissionsOK(deviceDirectory, deviceInfo) ||
			!bridgePathComponentsOK(deviceDirectory, true) {
			return errors.New("Rime maintenance device metadata is not private")
		}
		deviceEntries, readErr := os.ReadDir(deviceDirectory)
		if readErr != nil || len(deviceEntries) > maxRimeSyncEntries {
			return errors.New("Rime maintenance device metadata is invalid")
		}
		for _, deviceEntry := range deviceEntries {
			path := filepath.Join(deviceDirectory, deviceEntry.Name())
			info, statErr := os.Lstat(path)
			if statErr != nil || deviceEntry.Type()&os.ModeSymlink != 0 || deviceEntry.IsDir() ||
				!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
				!privateFilePermissionsOK(path, info) || !bridgePathComponentsOK(path, false) {
				return errors.New("Rime maintenance snapshot metadata is not private")
			}
		}
	}

	// SyncOnce can preserve an existing reviewed snapshot as its immutable
	// baseline, but it fails closed when both are absent. Inspect only metadata
	// here; phrase contents remain unread until the normal locked sync run.
	learningPath := agent.BaselinePath
	if _, statErr := os.Lstat(learningPath); errors.Is(statErr, os.ErrNotExist) {
		learningPath = agent.SnapshotPath
	} else if statErr != nil {
		return statErr
	}
	if learningPath == "" || !filepath.IsAbs(learningPath) || filepath.Clean(learningPath) != learningPath {
		return errors.New("Rime learning baseline path is invalid")
	}
	learning, err := os.Lstat(learningPath)
	if err != nil || !learning.Mode().IsRegular() || learning.Mode()&os.ModeSymlink != 0 ||
		!privateFilePermissionsOK(learningPath, learning) || learning.Size() < 1 ||
		learning.Size() > maxBaselineBytes || !bridgePathComponentsOK(learningPath, false) {
		return errors.New("Rime learning baseline metadata is not private and bounded")
	}
	return nil
}

// Status validates local configuration and credential material without making
// a network request or exposing identifiers, tokens, or key bytes.
func (agent Agent) Status(ctx context.Context) (Status, error) {
	return agent.statusWithBundleLoader(ctx, agent.loadBundle)
}

// StatusWithoutUserInteraction provides the same redacted local readiness
// result for monitoring and diagnostics, but it must never become an implicit
// authorization request or make SecurityAgent appear. Explicit human-facing
// settings and sync actions retain Status and the normal interactive load.
func (agent Agent) StatusWithoutUserInteraction(ctx context.Context) (Status, error) {
	return agent.statusWithBundleLoader(ctx, agent.loadResidentBundle)
}

func (agent Agent) statusWithBundleLoader(
	ctx context.Context,
	load func(context.Context) (CredentialBundleV1, error),
) (Status, error) {
	bundle, err := load(ctx)
	if err != nil {
		return Status{}, err
	}
	defer bundle.Zero()
	if err := agent.validateLocalState(); err != nil {
		return Status{}, err
	}
	status := Status{
		Ready: true, CredentialVersion: int(bundle.Version), EndpointConfigured: true, DatabasePresent: true,
		EventLogAvailable: EventLogAvailable(Paths{StateDirectory: agent.StateDirectory}),
		Health:            localstore.SyncHealth{LastFailureClass: localstore.SyncFailureNone},
	}
	// Health is best effort for configuration readiness, but its availability is
	// explicit: an unreadable record is never presented as "never synchronized".
	if store, err := localstore.OpenForDevice(
		ctx, agent.DatabasePath, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex(),
	); err == nil {
		if health, healthErr := store.LoadSyncHealth(ctx); healthErr == nil {
			status.Health = health
			status.HealthAvailable = true
		}
		_ = store.Close()
	}
	return status, nil
}

// ResidentReady is a local, redacted, fail-closed activation gate. Unlike
// Status, it requires finalized two-device trust and proves that no protected
// provisioning or pairing journal is present. No network request is made.
func (agent Agent) ResidentReady(ctx context.Context) (ResidentReadiness, error) {
	bundle, err := agent.loadBundle(ctx)
	if err != nil {
		return ResidentReadiness{}, errors.New("resident activation credential is unavailable or invalid")
	}
	defer bundle.Zero()
	if bundle.Version != CredentialBundleVersion || rosterIsEmpty(bundle.TrustedRoster) ||
		len(bundle.TrustedRoster.Devices) != 2 {
		return ResidentReadiness{}, errors.New("resident activation requires finalized two-device trust")
	}
	verification, x25519, err := trustFromRoster(bundle.TrustedRoster)
	if err != nil || !equalVerificationTrust(bundle.VerificationKeys, verification) ||
		!equalX25519Trust(bundle.X25519PublicKeys, x25519) {
		return ResidentReadiness{}, errors.New("resident activation requires valid signed trust")
	}
	if !rosterContainsDevice(bundle.TrustedRoster, bundle.DeviceID[:]) {
		return ResidentReadiness{}, errors.New("resident activation credential is not in its signed trust roster")
	}

	provisioning, err := provisioningProfile(agent.Profile)
	if err != nil {
		return ResidentReadiness{}, errors.New("resident activation profile cannot safely represent protected journals")
	}
	creator, err := pairingProfile(agent.Profile, creatorPairingSuffix)
	if err != nil {
		return ResidentReadiness{}, errors.New("resident activation profile cannot safely represent protected journals")
	}
	joining, err := pairingProfile(agent.Profile, joiningPairingSuffix)
	if err != nil {
		return ResidentReadiness{}, errors.New("resident activation profile cannot safely represent protected journals")
	}
	for _, profile := range []string{provisioning, creator, joining} {
		value, loadErr := agent.Secrets.Load(ctx, profile)
		if loadErr == nil {
			zeroBytes(value)
			return ResidentReadiness{}, errors.New("resident activation is blocked by pending protected setup state")
		}
		if !errors.Is(loadErr, ErrSecretNotFound) {
			return ResidentReadiness{}, errors.New("resident activation could not exclude pending protected setup state")
		}
	}
	if err := agent.validateLocalState(); err != nil {
		return ResidentReadiness{}, errors.New("resident activation requires complete private local state")
	}
	if err := agent.validateResidentDatabaseHeader(); err != nil {
		return ResidentReadiness{}, errors.New("resident activation requires a valid private local database")
	}
	if err := agent.validateResidentRimeState(); err != nil {
		return ResidentReadiness{}, errors.New("resident activation requires complete private Rime bridge state")
	}
	return ResidentReadiness{Ready: true}, nil
}

type SyncSummary struct {
	Rounds              int
	Uploaded            int
	Downloaded          int
	Cursor              int64
	NativeEvents        int
	NativeDuplicates    int
	NativeLocalOnly     int
	NativeCorrections   int
	RimeUserDBRows      int
	RimeUserDBAdvanced  int
	RimeUserDBResets    int
	RimeUserDBLocalOnly int
	RimeUserDBIgnored   int
	SnapshotRows        int
	SnapshotChanged     bool
	SnapshotReloaded    bool
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

func (agent Agent) nativeEventIngestionEnabled() bool {
	return agent.NativeEventsPath != "" && agent.RimeUserDBExportPath == ""
}

// SyncOnce is the only network-bearing desktop operation. Native input event
// handlers never call it; they continue using their current memory snapshot.
func (agent Agent) SyncOnce(ctx context.Context) (summary SyncSummary, returnErr error) {
	if agent.RimeUserDBRefresh != nil && agent.RimeUserDBExportPath == "" {
		return SyncSummary{}, errors.New("Rime userdb refresh requires a fixed private staging path")
	}
	bundle, err := agent.loadBundle(ctx)
	if err != nil {
		return SyncSummary{}, err
	}
	defer bundle.Zero()
	return agent.syncOnceWithBundle(ctx, &bundle)
}

// syncOnceWithBundle performs one round with credential material already held
// by its caller. Explicit operations use SyncOnce and therefore retain their
// load-per-command behavior. The resident loads once after login and keeps the
// decoded bundle only in process memory until termination, so locking the login
// Keychain cannot strand later background rounds. The resident owner zeros the
// bundle when its process exits.
func (agent Agent) syncOnceWithBundle(ctx context.Context, bundle *CredentialBundleV1) (summary SyncSummary, returnErr error) {
	if bundle == nil {
		return SyncSummary{}, errors.New("decoded resident credential is required")
	}
	endpoint, err := syncclient.LoadEndpointConfig(agent.EndpointConfigPath)
	if err != nil {
		return SyncSummary{}, err
	}
	if agent.DatabasePath == "" || !filepath.IsAbs(agent.DatabasePath) {
		return SyncSummary{}, errors.New("encrypted local database path must be absolute")
	}
	info, err := os.Lstat(agent.DatabasePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!privateFilePermissionsOK(agent.DatabasePath, info) {
		return SyncSummary{}, errors.New("encrypted local database must be an existing regular file")
	}
	if err := verifyPrivateDatabaseFiles(agent.DatabasePath); err != nil {
		return SyncSummary{}, fmt.Errorf("verify encrypted local database files: %w", err)
	}
	store, err := localstore.OpenForDevice(
		ctx, agent.DatabasePath, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex(),
	)
	if err != nil {
		return SyncSummary{}, err
	}
	defer func() {
		closeErr := store.Close()
		permissionErr := protectPrivateDatabaseFiles(agent.DatabasePath)
		if returnErr == nil && closeErr != nil {
			returnErr = closeErr
		}
		if returnErr == nil && permissionErr != nil {
			returnErr = fmt.Errorf("protect encrypted local database and sidecar permissions: %w", permissionErr)
		}
	}()
	// Register after the close defer so the health write runs first, through
	// this exact store handle. This removes the former credential reload and
	// second database open between a sync result and its health observation.
	defer func() { recordSyncHealth(ctx, store, summary, returnErr) }()
	if err := protectPrivateDatabaseFiles(agent.DatabasePath); err != nil {
		return SyncSummary{}, fmt.Errorf("protect opened encrypted local database and sidecars: %w", err)
	}
	if agent.RimeUserDBRefresh != nil {
		if err := agent.RimeUserDBRefresh(ctx); err != nil {
			return SyncSummary{}, fmt.Errorf("refresh Rime userdb snapshot: %w", err)
		}
	}
	var nativeSummary NativeEventSummary
	var rimeSummary localstore.RimeUserDBImportResult
	if agent.NativeEventsPath != "" || agent.RimeUserDBExportPath != "" {
		if agent.BaselinePath == "" || agent.SnapshotPath == "" {
			return SyncSummary{}, errors.New("local learning ingestion requires static baseline and snapshot paths")
		}
		if _, err := ensureBaseline(agent.BaselinePath, agent.SnapshotPath); err != nil {
			return SyncSummary{}, err
		}
		if _, err := os.Lstat(agent.BaselinePath); errors.Is(err, os.ErrNotExist) {
			return SyncSummary{}, errors.New("local learning ingestion is blocked because both baseline and private snapshot are missing")
		} else if err != nil {
			return SyncSummary{}, fmt.Errorf("inspect required static baseline: %w", err)
		}
		baselineRows, err := parseBaseline(agent.BaselinePath)
		if err != nil {
			return SyncSummary{}, err
		}
		localOnly := make(map[string]struct{}, len(baselineRows))
		for _, row := range baselineRows {
			localOnly[protocol.CanonicalPhrase(row.Phrase)] = struct{}{}
		}
		// A cumulative Rime userdb snapshot and per-selection native events
		// represent the same user actions. Selecting the Rime bridge therefore
		// disables native spool consumption for this run to prevent double count.
		if agent.nativeEventIngestionEnabled() {
			nativeSummary, err = consumeNativeEvents(ctx, agent.NativeEventsPath, store, localOnly, maxNativeBatch)
			if err != nil {
				return SyncSummary{}, err
			}
		}
		if agent.RimeUserDBExportPath != "" {
			rimeSummary, err = ingestRimeUserDBExport(ctx, agent.RimeUserDBExportPath, store, localOnly)
			if err != nil {
				return SyncSummary{}, err
			}
		}
	}
	session := sessionFromBundle(*bundle)
	defer zeroSession(&session)
	maxRounds := agent.MaxRounds
	if maxRounds == 0 {
		maxRounds = defaultSyncMaxRounds
	}
	client := syncclient.New(endpoint)
	if err := client.SealAccount(ctx, bundle.AccountID[:], string(bundle.DeviceToken)); err != nil {
		return SyncSummary{}, fmt.Errorf("ensure sync account is sealed: %w", err)
	}
	worker := syncclient.Worker{Client: client, Store: store, Session: session}
	results, err := worker.SyncUntilIdle(ctx, maxRounds)
	if err != nil {
		return SyncSummary{}, err
	}
	summary = SyncSummary{
		Rounds: len(results), NativeEvents: nativeSummary.Consumed,
		NativeDuplicates: nativeSummary.Duplicate, NativeLocalOnly: nativeSummary.LocalOnly,
		NativeCorrections: nativeSummary.Corrections,
		RimeUserDBRows:    rimeSummary.Rows, RimeUserDBAdvanced: rimeSummary.Advanced,
		RimeUserDBResets: rimeSummary.Resets, RimeUserDBLocalOnly: rimeSummary.LocalOnly,
		RimeUserDBIgnored: rimeSummary.Ignored,
	}
	for _, result := range results {
		if result.Uploaded {
			summary.Uploaded++
		}
		summary.Downloaded += result.Downloaded
		summary.Cursor = result.Cursor
	}
	if agent.SnapshotPath != "" {
		rebuilt, err := rebuildPrivateSnapshot(ctx, store, agent.BaselinePath, agent.SnapshotPath)
		if err != nil {
			return SyncSummary{}, err
		}
		summary.SnapshotRows = rebuilt.TotalRows
		summary.SnapshotChanged = rebuilt.Changed
		reloadPending, err := snapshotReloadPending(agent.SnapshotStatePath, rebuilt.digest)
		if err != nil {
			return SyncSummary{}, err
		}
		if reloadPending {
			if agent.Reload == nil {
				return SyncSummary{}, errors.New("private snapshot is pending reload but no platform reload hook is available")
			}
			if err := agent.Reload(ctx); err != nil {
				return SyncSummary{}, fmt.Errorf("reload Rime after atomic snapshot replacement: %w", err)
			}
			if err := markSnapshotReloaded(agent.SnapshotStatePath, rebuilt.digest); err != nil {
				return SyncSummary{}, fmt.Errorf("commit Rime snapshot reload state: %w", err)
			}
			summary.SnapshotReloaded = true
		}
	}
	return summary, nil
}
