// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
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
	"golang.org/x/crypto/curve25519"
)

const (
	legacyProvisioningJournalVersion = 1
	provisioningJournalVersion       = 2
	provisioningProfileSuffix        = ".provisioning"
	maxProvisioningJournal           = 64 * 1024
)

var (
	provisioningJournalMagic      = [4]byte{'Y', 'P', 'P', 'J'}
	ErrAccountSealPending         = errors.New("sync account sealing is pending")
	ErrAccountProvisioningExpired = errors.New("sync account provisioning identity expired and must be aborted")
)

func provisioningExpiredError(err error) bool {
	var api *syncclient.APIError
	return errors.As(err, &api) && (api.Code == "provisioning_identity_retired" || api.Code == "account_not_found")
}

type AccountRelay interface {
	CreateAccount(context.Context, syncclient.Account, syncclient.AccountRegistration) (syncclient.Account, error)
	PutKeyring(context.Context, string, uint64, protocol.SealedBox) error
	DeleteAccount(context.Context, []byte, string) error
	SealAccount(context.Context, []byte, string) error
}

type StoreInitializer func(context.Context, string, []byte, []byte, string) error

type InitAccountOptions struct {
	Secrets              SecretStore
	Profile              string
	DatabasePath         string
	Random               io.Reader
	InitializeStore      StoreInitializer
	ConfirmRecoverySaved func(context.Context, PrepareAccountResult) error
}

type PrepareAccountResult struct {
	AccountIDHex string `json:"account_id"`
	RecoveryKey  string `json:"recovery_key"`
}

type InitAccountResult struct {
	AccountIDHex string `json:"account_id"`
	State        string `json:"state"`
}

type provisioningJournal struct {
	RemoteReady            bool
	Credentials            syncclient.Account
	RecoveryAuthentication []byte
	DeviceNameCiphertext   []byte
	Ed25519PublicKey       []byte
	X25519PublicKey        []byte
	RecoveryKeyring        protocol.SealedBox
	ActiveCredential       []byte
}

func (journal *provisioningJournal) Zero() {
	if journal == nil {
		return
	}
	zeroBytes(journal.Credentials.AccountID)
	zeroBytes(journal.Credentials.DeviceID)
	journal.Credentials.DeviceToken = ""
	journal.Credentials.RollbackToken = ""
	journal.RemoteReady = false
	zeroBytes(journal.RecoveryAuthentication)
	zeroBytes(journal.DeviceNameCiphertext)
	zeroBytes(journal.Ed25519PublicKey)
	zeroBytes(journal.X25519PublicKey)
	zeroBytes(journal.RecoveryKeyring.Nonce)
	zeroBytes(journal.RecoveryKeyring.Ciphertext)
	zeroBytes(journal.ActiveCredential)
}

func provisioningProfile(profile string) (string, error) {
	if err := validateProfile(profile); err != nil {
		return "", err
	}
	if len(profile)+len(provisioningProfileSuffix) > 64 {
		return "", errors.New("profile is too long for provisioning journal")
	}
	return profile + provisioningProfileSuffix, nil
}

func validCanonicalToken(value string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func appendLength16(output *bytes.Buffer, value []byte) error {
	if len(value) > int(^uint16(0)) {
		return errors.New("provisioning journal field is too large")
	}
	_ = binary.Write(output, binary.BigEndian, uint16(len(value)))
	_, _ = output.Write(value)
	return nil
}

func appendLength32(output *bytes.Buffer, value []byte) {
	_ = binary.Write(output, binary.BigEndian, uint32(len(value)))
	_, _ = output.Write(value)
}

func encodeProvisioningJournal(journal provisioningJournal) ([]byte, error) {
	if len(journal.Credentials.AccountID) != 16 || len(journal.Credentials.DeviceID) != 16 ||
		!validCanonicalToken(journal.Credentials.DeviceToken) || !validCanonicalToken(journal.Credentials.RollbackToken) ||
		len(journal.RecoveryAuthentication) != 32 || len(journal.DeviceNameCiphertext) < 16 || len(journal.DeviceNameCiphertext) > 512 ||
		len(journal.Ed25519PublicKey) != ed25519.PublicKeySize || len(journal.X25519PublicKey) != 32 {
		return nil, errors.New("provisioning journal fields are invalid")
	}
	if _, err := protocol.EncodeSealedBox(journal.RecoveryKeyring); err != nil {
		return nil, err
	}
	bundle, err := DecodeCredentialBundle(journal.ActiveCredential)
	if err != nil {
		return nil, fmt.Errorf("provisioning active credential: %w", err)
	}
	defer bundle.Zero()
	xPublic, xErr := curve25519.X25519(bundle.X25519Private[:], curve25519.Basepoint)
	if xErr != nil || !bytes.Equal(bundle.AccountID[:], journal.Credentials.AccountID) ||
		!bytes.Equal(bundle.DeviceID[:], journal.Credentials.DeviceID) ||
		string(bundle.DeviceToken) != journal.Credentials.DeviceToken ||
		!bytes.Equal(ed25519.NewKeyFromSeed(bundle.SigningSeed[:]).Public().(ed25519.PublicKey), journal.Ed25519PublicKey) ||
		!bytes.Equal(xPublic, journal.X25519PublicKey) {
		return nil, errors.New("provisioning journal credential identities differ")
	}
	var output bytes.Buffer
	output.Write(provisioningJournalMagic[:])
	output.WriteByte(provisioningJournalVersion)
	if journal.RemoteReady {
		output.WriteByte(1)
	} else {
		output.WriteByte(0)
	}
	output.Write(journal.Credentials.AccountID)
	output.Write(journal.Credentials.DeviceID)
	_ = appendLength16(&output, []byte(journal.Credentials.DeviceToken))
	_ = appendLength16(&output, []byte(journal.Credentials.RollbackToken))
	output.Write(journal.RecoveryAuthentication)
	_ = appendLength16(&output, journal.DeviceNameCiphertext)
	output.Write(journal.Ed25519PublicKey)
	output.Write(journal.X25519PublicKey)
	output.Write(journal.RecoveryKeyring.Nonce)
	appendLength32(&output, journal.RecoveryKeyring.Ciphertext)
	appendLength32(&output, journal.ActiveCredential)
	if output.Len() > maxProvisioningJournal {
		return nil, errors.New("provisioning journal exceeds size limit")
	}
	return output.Bytes(), nil
}

type provisioningReader struct {
	value  []byte
	offset int
}

func (reader *provisioningReader) take(length int) ([]byte, error) {
	if length < 0 || reader.offset > len(reader.value)-length {
		return nil, errors.New("provisioning journal is truncated")
	}
	value := reader.value[reader.offset : reader.offset+length]
	reader.offset += length
	return value, nil
}

func (reader *provisioningReader) length16() ([]byte, error) {
	value, err := reader.take(2)
	if err != nil {
		return nil, err
	}
	return reader.take(int(binary.BigEndian.Uint16(value)))
}

func (reader *provisioningReader) length32() ([]byte, error) {
	value, err := reader.take(4)
	if err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(value)
	if uint64(length) > uint64(len(reader.value)) {
		return nil, errors.New("provisioning journal field is too large")
	}
	return reader.take(int(length))
}

func decodeProvisioningJournal(encoded []byte) (provisioningJournal, error) {
	if len(encoded) < 5 || len(encoded) > maxProvisioningJournal {
		return provisioningJournal{}, errors.New("provisioning journal length is invalid")
	}
	reader := provisioningReader{value: encoded}
	magic, _ := reader.take(4)
	version, _ := reader.take(1)
	if !bytes.Equal(magic, provisioningJournalMagic[:]) || len(version) != 1 ||
		(version[0] != legacyProvisioningJournalVersion && version[0] != provisioningJournalVersion) {
		return provisioningJournal{}, errors.New("provisioning journal header is invalid")
	}
	journal := provisioningJournal{}
	valid := false
	defer func() {
		if !valid {
			journal.Zero()
		}
	}()
	var err error
	if version[0] == provisioningJournalVersion {
		flags, flagErr := reader.take(1)
		if flagErr != nil || (flags[0] != 0 && flags[0] != 1) {
			return provisioningJournal{}, errors.New("provisioning journal phase is invalid")
		}
		journal.RemoteReady = flags[0] == 1
	}
	journal.Credentials.AccountID, err = reader.take(16)
	if err == nil {
		journal.Credentials.DeviceID, err = reader.take(16)
	}
	var token, rollback []byte
	if err == nil {
		token, err = reader.length16()
	}
	if err == nil {
		rollback, err = reader.length16()
	}
	journal.Credentials.AccountID = append([]byte(nil), journal.Credentials.AccountID...)
	journal.Credentials.DeviceID = append([]byte(nil), journal.Credentials.DeviceID...)
	journal.Credentials.DeviceToken = string(token)
	journal.Credentials.RollbackToken = string(rollback)
	if err == nil {
		journal.RecoveryAuthentication, err = reader.take(32)
	}
	if err == nil {
		journal.DeviceNameCiphertext, err = reader.length16()
	}
	if err == nil {
		journal.Ed25519PublicKey, err = reader.take(ed25519.PublicKeySize)
	}
	if err == nil {
		journal.X25519PublicKey, err = reader.take(32)
	}
	if err == nil {
		journal.RecoveryKeyring.Nonce, err = reader.take(24)
	}
	if err == nil {
		journal.RecoveryKeyring.Ciphertext, err = reader.length32()
	}
	if err == nil {
		journal.ActiveCredential, err = reader.length32()
	}
	journal.RecoveryAuthentication = append([]byte(nil), journal.RecoveryAuthentication...)
	journal.DeviceNameCiphertext = append([]byte(nil), journal.DeviceNameCiphertext...)
	journal.Ed25519PublicKey = append([]byte(nil), journal.Ed25519PublicKey...)
	journal.X25519PublicKey = append([]byte(nil), journal.X25519PublicKey...)
	journal.RecoveryKeyring.Nonce = append([]byte(nil), journal.RecoveryKeyring.Nonce...)
	journal.RecoveryKeyring.Ciphertext = append([]byte(nil), journal.RecoveryKeyring.Ciphertext...)
	journal.ActiveCredential = append([]byte(nil), journal.ActiveCredential...)
	if err != nil || reader.offset != len(encoded) {
		return provisioningJournal{}, errors.New("provisioning journal is malformed")
	}
	if _, err := encodeProvisioningJournal(journal); err != nil {
		return provisioningJournal{}, err
	}
	valid = true
	return journal, nil
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

func loadOptionalSecret(ctx context.Context, secrets SecretStore, profile string) ([]byte, bool, error) {
	value, err := secrets.Load(ctx, profile)
	if errors.Is(err, ErrSecretNotFound) {
		return nil, false, nil
	}
	return value, err == nil, err
}

func saveSecretExact(ctx context.Context, secrets SecretStore, profile string, expected []byte) error {
	existing, found, err := loadOptionalSecret(ctx, secrets, profile)
	if err != nil {
		return err
	}
	if found {
		defer zeroBytes(existing)
		if !bytes.Equal(existing, expected) {
			return errors.New("OS credential slot contains different material")
		}
		return nil
	}
	if err := secrets.Save(ctx, profile, expected); err != nil {
		// A platform store may commit and then lose its response. Resolve that
		// ambiguity by reading the exact protected slot before reporting failure.
		checkContext := context.WithoutCancel(ctx)
		committed, present, loadErr := loadOptionalSecret(checkContext, secrets, profile)
		if present {
			defer zeroBytes(committed)
		}
		if loadErr == nil && present && bytes.Equal(committed, expected) {
			return nil
		}
		return err
	}
	return nil
}

func replaceSecretExact(ctx context.Context, secrets SecretStore, profile string, previous, next []byte) error {
	current, found, err := loadOptionalSecret(ctx, secrets, profile)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("OS credential slot disappeared during protected update")
	}
	defer zeroBytes(current)
	if bytes.Equal(current, next) {
		return nil
	}
	if !bytes.Equal(current, previous) {
		return errors.New("OS credential slot changed concurrently")
	}
	if err := secrets.Save(ctx, profile, next); err != nil {
		checkContext := context.WithoutCancel(ctx)
		committed, present, loadErr := loadOptionalSecret(checkContext, secrets, profile)
		if present {
			defer zeroBytes(committed)
		}
		if loadErr == nil && present && bytes.Equal(committed, next) {
			return nil
		}
		return err
	}
	return nil
}

func deleteSecretExact(ctx context.Context, secrets SecretStore, profile string, expected []byte) error {
	current, found, err := loadOptionalSecret(ctx, secrets, profile)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	defer zeroBytes(current)
	if !bytes.Equal(current, expected) {
		return errors.New("OS credential slot changed before protected deletion")
	}
	deleteErr := secrets.Delete(ctx, profile)
	check, present, loadErr := loadOptionalSecret(context.WithoutCancel(ctx), secrets, profile)
	if present {
		defer zeroBytes(check)
	}
	if loadErr != nil {
		if deleteErr != nil {
			return errors.Join(deleteErr, loadErr)
		}
		return loadErr
	}
	if !present {
		return nil
	}
	if !bytes.Equal(check, expected) {
		return errors.New("OS credential slot changed concurrently during protected deletion")
	}
	if deleteErr != nil {
		return deleteErr
	}
	return errors.New("OS credential deletion returned success but the exact item remains")
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

func ensureEncryptedStore(ctx context.Context, path string, bundle CredentialBundleV1, initializer StoreInitializer) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("local database path must be absolute")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
			return fmt.Errorf("protect local database directory: %w", err)
		}
		if initializer != nil {
			if err := initializer(ctx, path, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex()); err != nil {
				return err
			}
			if err := protectPrivateDatabaseFiles(path); err != nil {
				return fmt.Errorf("protect initialized local database before reopen: %w", err)
			}
		}
	} else if err != nil {
		return err
	} else if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !privateFilePermissionsOK(path, info) {
		return errors.New("local database must be a private regular file")
	} else if err := verifyPrivateDatabaseFiles(path); err != nil {
		return fmt.Errorf("verify encrypted local database files: %w", err)
	}
	store, err := localstore.OpenForDevice(ctx, path, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex())
	if err != nil {
		return err
	}
	if err := protectPrivateDatabaseFiles(path); err != nil {
		store.Close()
		return fmt.Errorf("protect opened encrypted local database and sidecars: %w", err)
	}
	closeErr := store.Close()
	if closeErr != nil {
		return closeErr
	}
	if err := protectPrivateDatabaseFiles(path); err != nil {
		return fmt.Errorf("protect encrypted local database and sidecar permissions: %w", err)
	}
	return nil
}

// PrepareAccount is deliberately network-free. It delivers the recovery root
// through the required confirmation callback exactly once, then commits a
// resumable pending journal to the platform-private store and returns without
// the root. The root itself is never part of that journal.
func PrepareAccount(ctx context.Context, options InitAccountOptions) (PrepareAccountResult, error) {
	if options.Secrets == nil || options.Random == nil {
		return PrepareAccountResult{}, errors.New("OS secret store and cryptographic random source are required")
	}
	if options.ConfirmRecoverySaved == nil {
		return PrepareAccountResult{}, errors.New("recovery delivery and saved-key confirmation callback is required")
	}
	pendingProfile, err := provisioningProfile(options.Profile)
	if err != nil {
		return PrepareAccountResult{}, err
	}
	for _, profile := range []string{options.Profile, pendingProfile} {
		if existing, found, err := loadOptionalSecret(ctx, options.Secrets, profile); err != nil {
			return PrepareAccountResult{}, err
		} else if found {
			zeroBytes(existing)
			return PrepareAccountResult{}, errors.New("profile already has active or pending provisioning material")
		}
	}
	if err := preflightDatabasePath(options.DatabasePath); err != nil {
		return PrepareAccountResult{}, err
	}
	keys, err := protocol.NewDeviceKeys(options.Random)
	if err != nil {
		return PrepareAccountResult{}, err
	}
	defer zeroBytes(keys.X25519Private)
	defer zeroBytes(keys.Ed25519Private)
	recoveryRoot := make([]byte, 32)
	epochKey := make([]byte, 32)
	objectIDKey := make([]byte, 32)
	localDataKey := make([]byte, 32)
	deviceName := make([]byte, 32)
	defer zeroBytes(recoveryRoot)
	defer zeroBytes(epochKey)
	defer zeroBytes(objectIDKey)
	defer zeroBytes(localDataKey)
	defer zeroBytes(deviceName)
	if err := fillRandom(options.Random, recoveryRoot, epochKey, objectIDKey, localDataKey, deviceName); err != nil {
		return PrepareAccountResult{}, err
	}
	credentials, err := syncclient.GenerateAccountCredentials(options.Random)
	if err != nil {
		return PrepareAccountResult{}, err
	}
	_, recoveryAuthentication, err := protocol.DeriveRecoveryKeys(recoveryRoot)
	if err != nil {
		return PrepareAccountResult{}, err
	}
	defer zeroBytes(recoveryAuthentication)
	keyring, err := protocol.SealRecoveryPackage(recoveryRoot, protocol.RecoveryPackage{
		AccountID: credentials.AccountID, CurrentEpoch: 1, EpochKey: epochKey, ObjectIDKey: objectIDKey,
	}, options.Random)
	if err != nil {
		return PrepareAccountResult{}, err
	}
	bundle := CredentialBundleV1{
		Version: CredentialBundleVersion, DeviceToken: []byte(credentials.DeviceToken), CurrentEpoch: 1,
		EpochKeys: map[uint64][32]byte{1: {}},
	}
	defer bundle.Zero()
	copy(bundle.AccountID[:], credentials.AccountID)
	copy(bundle.DeviceID[:], credentials.DeviceID)
	copy(bundle.SigningSeed[:], keys.Ed25519Private.Seed())
	copy(bundle.X25519Private[:], keys.X25519Private)
	copy(bundle.LocalDataKey[:], localDataKey)
	copy(bundle.ObjectIDKey[:], objectIDKey)
	epochOne := bundle.EpochKeys[1]
	copy(epochOne[:], epochKey)
	bundle.EpochKeys[1] = epochOne
	if err := populateBootstrapTrust(&bundle); err != nil {
		return PrepareAccountResult{}, err
	}
	activeCredential, err := EncodeCredentialBundle(bundle)
	if err != nil {
		return PrepareAccountResult{}, err
	}
	defer zeroBytes(activeCredential)
	journal := provisioningJournal{
		Credentials: credentials, RecoveryAuthentication: recoveryAuthentication,
		DeviceNameCiphertext: deviceName, Ed25519PublicKey: keys.Ed25519Public, X25519PublicKey: keys.X25519Public,
		RecoveryKeyring: keyring, ActiveCredential: activeCredential,
	}
	encodedJournal, err := encodeProvisioningJournal(journal)
	if err != nil {
		return PrepareAccountResult{}, err
	}
	defer zeroBytes(encodedJournal)
	recoveryText, err := protocol.EncodeRecoveryKey(recoveryRoot)
	if err != nil {
		return PrepareAccountResult{}, err
	}
	result := PrepareAccountResult{AccountIDHex: hex.EncodeToString(credentials.AccountID), RecoveryKey: recoveryText}
	// Recovery delivery is deliberately before any pending-state persistence.
	// If stdout/UI delivery or the user's saved-key confirmation fails, this
	// process returns with no remote or local provisioning identity to orphan.
	if err := options.ConfirmRecoverySaved(ctx, result); err != nil {
		return PrepareAccountResult{}, fmt.Errorf("confirm recovery key delivery: %w", err)
	}
	if err := saveSecretExact(ctx, options.Secrets, pendingProfile, encodedJournal); err != nil {
		return PrepareAccountResult{}, fmt.Errorf("commit pending provisioning journal: %w", err)
	}
	return PrepareAccountResult{AccountIDHex: result.AccountIDHex}, nil
}

// InitAccount resumes the pending journal. Every remote and local transition
// is idempotent; process termination leaves enough protected material to call
// this function again without regenerating identity or recovery ciphertext.
func InitAccount(ctx context.Context, relay AccountRelay, options InitAccountOptions) (InitAccountResult, error) {
	if relay == nil || options.Secrets == nil {
		return InitAccountResult{}, errors.New("account relay and OS secret store are required")
	}
	pendingProfile, err := provisioningProfile(options.Profile)
	if err != nil {
		return InitAccountResult{}, err
	}
	encodedJournal, err := options.Secrets.Load(ctx, pendingProfile)
	if err != nil {
		return InitAccountResult{}, fmt.Errorf("load pending provisioning journal: %w", err)
	}
	defer zeroBytes(encodedJournal)
	journal, err := decodeProvisioningJournal(encodedJournal)
	if err != nil {
		return InitAccountResult{}, err
	}
	defer journal.Zero()
	result := InitAccountResult{AccountIDHex: hex.EncodeToString(journal.Credentials.AccountID), State: "pending"}
	existing, active, err := loadOptionalSecret(ctx, options.Secrets, options.Profile)
	if err != nil {
		return result, err
	}
	if active {
		defer zeroBytes(existing)
		if !bytes.Equal(existing, journal.ActiveCredential) {
			return result, errors.New("active OS credential differs from pending provisioning journal")
		}
	}
	if !active && !journal.RemoteReady {
		account, err := relay.CreateAccount(ctx, journal.Credentials, syncclient.AccountRegistration{
			RecoveryAuthentication: journal.RecoveryAuthentication,
			DeviceRegistration: syncclient.DeviceRegistration{
				DeviceNameCiphertext: journal.DeviceNameCiphertext,
				Ed25519PublicKey:     journal.Ed25519PublicKey, X25519PublicKey: journal.X25519PublicKey,
			},
		})
		if err != nil {
			if provisioningExpiredError(err) {
				result.State = "expired_abort_required"
				return result, fmt.Errorf("%w; run abort-account before preparing a new identity: %v", ErrAccountProvisioningExpired, err)
			}
			return result, fmt.Errorf("create or resume sync account: %w", err)
		}
		if !bytes.Equal(account.AccountID, journal.Credentials.AccountID) ||
			!bytes.Equal(account.DeviceID, journal.Credentials.DeviceID) || account.DeviceToken != journal.Credentials.DeviceToken {
			return result, errors.New("relay returned different provisioning identity")
		}
		if err := relay.PutKeyring(ctx, account.DeviceToken, 1, journal.RecoveryKeyring); err != nil {
			return result, fmt.Errorf("store recovery keyring: %w", err)
		}
		journal.RemoteReady = true
		updatedJournal, err := encodeProvisioningJournal(journal)
		if err != nil {
			return result, err
		}
		defer zeroBytes(updatedJournal)
		if err := replaceSecretExact(ctx, options.Secrets, pendingProfile, encodedJournal, updatedJournal); err != nil {
			return result, fmt.Errorf("commit remote-ready provisioning phase: %w", err)
		}
	}
	// Seal before creating any active local state. Remote-ready is itself in the
	// protected journal, so a lost seal response retries only the idempotent
	// seal operation and never replays CreateAccount. If the unsealed account
	// expires, neither an active credential nor an encrypted DB has been
	// promoted locally.
	if err := relay.SealAccount(ctx, journal.Credentials.AccountID, journal.Credentials.DeviceToken); err != nil {
		if provisioningExpiredError(err) {
			result.State = "expired_abort_required"
			return result, fmt.Errorf("%w; run abort-account before preparing a new identity: %v", ErrAccountProvisioningExpired, err)
		}
		result.State = "seal_pending"
		return result, fmt.Errorf("%w; call init-account again: %v", ErrAccountSealPending, err)
	}
	bundle, err := DecodeCredentialBundle(journal.ActiveCredential)
	if err != nil {
		return result, err
	}
	if err := ensureEncryptedStore(ctx, options.DatabasePath, bundle, options.InitializeStore); err != nil {
		bundle.Zero()
		return result, fmt.Errorf("initialize or verify sealed encrypted local database: %w", err)
	}
	bundle.Zero()
	if !active {
		if err := saveSecretExact(ctx, options.Secrets, options.Profile, journal.ActiveCredential); err != nil {
			return result, fmt.Errorf("commit sealed active device credential: %w", err)
		}
	}
	result.State = "ready"
	if err := options.Secrets.Delete(context.WithoutCancel(ctx), pendingProfile); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return result, fmt.Errorf("remove completed provisioning journal: %w", err)
	}
	return result, nil
}

// AbortAccount removes only a still-unsealed provisioning identity. The
// protected journal is retained unless the relay confirms rollback (including
// an idempotent tombstone replay) and the exact-key local database is safely
// removed. It can therefore be retried after any lost response.
func AbortAccount(ctx context.Context, relay AccountRelay, options InitAccountOptions) (InitAccountResult, error) {
	if relay == nil || options.Secrets == nil {
		return InitAccountResult{}, errors.New("account relay and OS secret store are required")
	}
	pendingProfile, err := provisioningProfile(options.Profile)
	if err != nil {
		return InitAccountResult{}, err
	}
	encoded, err := options.Secrets.Load(ctx, pendingProfile)
	if err != nil {
		return InitAccountResult{}, fmt.Errorf("load pending provisioning journal: %w", err)
	}
	defer zeroBytes(encoded)
	journal, err := decodeProvisioningJournal(encoded)
	if err != nil {
		return InitAccountResult{}, err
	}
	defer journal.Zero()
	result := InitAccountResult{AccountIDHex: hex.EncodeToString(journal.Credentials.AccountID), State: "abort_pending"}
	if active, found, err := loadOptionalSecret(ctx, options.Secrets, options.Profile); err != nil {
		return result, err
	} else if found {
		zeroBytes(active)
		return result, errors.New("active credential exists; provisioning rollback is no longer permitted")
	}
	if err := relay.DeleteAccount(ctx, journal.Credentials.AccountID, journal.Credentials.RollbackToken); err != nil {
		return result, fmt.Errorf("confirm remote provisioning rollback: %w", err)
	}
	bundle, err := DecodeCredentialBundle(journal.ActiveCredential)
	if err != nil {
		return result, err
	}
	defer bundle.Zero()
	if err := cleanupUncommittedDatabase(context.WithoutCancel(ctx), options.DatabasePath, bundle); err != nil {
		return result, fmt.Errorf("remove aborted encrypted database: %w", err)
	}
	if err := options.Secrets.Delete(context.WithoutCancel(ctx), pendingProfile); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return result, fmt.Errorf("remove aborted provisioning journal: %w", err)
	}
	result.State = "aborted"
	return result, nil
}
