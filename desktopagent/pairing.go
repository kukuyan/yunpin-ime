// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
	"golang.org/x/crypto/curve25519"
)

const (
	pairingInvitationPrefix = "yppair2."
	creatorPairingSuffix    = ".pairing-creator"
	joiningPairingSuffix    = ".pairing-join"
	pairingJournalVersion   = 1
	maxPairingTextBytes     = 4096
	maxPairingJournalBytes  = 64 * 1024
)

type PairingRelay interface {
	CreatePairing(context.Context, syncclient.Account, syncclient.PairingInvitation) (syncclient.PairingInvitation, error)
	GetPairing(context.Context, syncclient.PairingInvitation, syncclient.Account) (syncclient.PairingStatus, error)
	ApprovePairing(context.Context, []byte, string, protocol.SealedBox) error
	ReadyPairing(context.Context, []byte, string) (string, error)
	FinalizePairing(context.Context, []byte, string) error
	CancelPairing(context.Context, []byte, string) error
	JoinPairing(context.Context, syncclient.PairingInvitation, syncclient.Account, syncclient.DeviceRegistration) (protocol.PairingTranscript, error)
	ClaimPairing(context.Context, syncclient.PairingInvitation, syncclient.Account, protocol.PairingTranscript, ed25519.PrivateKey) (syncclient.PairingClaim, error)
	DeleteCurrentDevice(context.Context, []byte, []byte, []byte, string) error
}

type PairingOptions struct {
	Secrets      SecretStore
	Profile      string
	DatabasePath string
	Random       io.Reader
}

type PairingResult struct {
	AccountIDHex string `json:"account_id"`
	PairingIDHex string `json:"pairing_id"`
	DeviceIDHex  string `json:"device_id,omitempty"`
	State        string `json:"state"`
	Invitation   string `json:"invitation,omitempty"`
}

type pairingInvitationWire struct {
	Version                 int    `json:"version"`
	PairingID               string `json:"pairing_id"`
	PairingSecret           string `json:"pairing_secret"`
	AccountID               string `json:"account_id"`
	CreatorDeviceID         string `json:"creator_device_id"`
	CreatorEd25519PublicKey string `json:"creator_ed25519_public_key"`
	CreatorX25519PublicKey  string `json:"creator_x25519_public_key"`
	ExpiresAt               string `json:"expires_at,omitempty"`
}

func decodeCanonicalBase64(value string, size int) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != size || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("pairing material is not canonical base64url")
	}
	return decoded, nil
}

func decodeCanonicalBase64Bounded(value string, maximum int) ([]byte, error) {
	if value == "" || maximum < 1 || len(value) > base64.RawURLEncoding.EncodedLen(maximum) {
		return nil, errors.New("pairing material exceeds its size limit")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || len(decoded) > maximum || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("pairing material is not canonical base64url")
	}
	return decoded, nil
}

func decodeCanonicalHex(value string, size int) ([]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size || hex.EncodeToString(decoded) != value {
		return nil, errors.New("pairing identifier is not canonical hexadecimal")
	}
	return decoded, nil
}

func validateInvitation(invitation syncclient.PairingInvitation) error {
	zero16 := make([]byte, 16)
	zero32 := make([]byte, 32)
	if len(invitation.PairingID) != 16 || len(invitation.PairingSecret) != protocol.PairingSecretSize ||
		len(invitation.AccountID) != 16 || len(invitation.CreatorDeviceID) != 16 ||
		len(invitation.CreatorEd25519PublicKey) != ed25519.PublicKeySize || len(invitation.CreatorX25519PublicKey) != 32 ||
		bytes.Equal(invitation.PairingID, zero16) || bytes.Equal(invitation.AccountID, zero16) ||
		bytes.Equal(invitation.CreatorDeviceID, zero16) || bytes.Equal(invitation.CreatorEd25519PublicKey, zero32) ||
		bytes.Equal(invitation.CreatorX25519PublicKey, zero32) {
		return errors.New("pairing invitation is invalid")
	}
	_, err := protocol.PairingRelayVerifier(invitation.PairingSecret, invitation.PairingID)
	return err
}

func EncodePairingInvitation(invitation syncclient.PairingInvitation) (string, error) {
	if err := validateInvitation(invitation); err != nil {
		return "", err
	}
	wire := pairingInvitationWire{
		Version: 2, PairingID: hex.EncodeToString(invitation.PairingID),
		PairingSecret: base64.RawURLEncoding.EncodeToString(invitation.PairingSecret),
		AccountID:     hex.EncodeToString(invitation.AccountID), CreatorDeviceID: hex.EncodeToString(invitation.CreatorDeviceID),
		CreatorEd25519PublicKey: base64.RawURLEncoding.EncodeToString(invitation.CreatorEd25519PublicKey),
		CreatorX25519PublicKey:  base64.RawURLEncoding.EncodeToString(invitation.CreatorX25519PublicKey),
	}
	if !invitation.ExpiresAt.IsZero() {
		wire.ExpiresAt = invitation.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	text := pairingInvitationPrefix + base64.RawURLEncoding.EncodeToString(encoded)
	if len(text) > maxPairingTextBytes {
		return "", errors.New("pairing invitation exceeds size limit")
	}
	return text, nil
}

func DecodePairingInvitation(text string) (syncclient.PairingInvitation, error) {
	text = strings.TrimSpace(text)
	if len(text) <= len(pairingInvitationPrefix) || len(text) > maxPairingTextBytes ||
		!strings.HasPrefix(text, pairingInvitationPrefix) {
		return syncclient.PairingInvitation{}, errors.New("pairing invitation text is invalid")
	}
	payloadText := strings.TrimPrefix(text, pairingInvitationPrefix)
	payload, err := base64.RawURLEncoding.DecodeString(payloadText)
	if err != nil || base64.RawURLEncoding.EncodeToString(payload) != payloadText {
		return syncclient.PairingInvitation{}, errors.New("pairing invitation text is not canonical")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var wire pairingInvitationWire
	if err := decoder.Decode(&wire); err != nil {
		return syncclient.PairingInvitation{}, errors.New("pairing invitation payload is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || wire.Version != 2 {
		return syncclient.PairingInvitation{}, errors.New("pairing invitation payload is invalid")
	}
	invitation := syncclient.PairingInvitation{}
	if invitation.PairingID, err = decodeCanonicalHex(wire.PairingID, 16); err == nil {
		invitation.PairingSecret, err = decodeCanonicalBase64(wire.PairingSecret, protocol.PairingSecretSize)
	}
	if err == nil {
		invitation.AccountID, err = decodeCanonicalHex(wire.AccountID, 16)
	}
	if err == nil {
		invitation.CreatorDeviceID, err = decodeCanonicalHex(wire.CreatorDeviceID, 16)
	}
	if err == nil {
		invitation.CreatorEd25519PublicKey, err = decodeCanonicalBase64(wire.CreatorEd25519PublicKey, ed25519.PublicKeySize)
	}
	if err == nil {
		invitation.CreatorX25519PublicKey, err = decodeCanonicalBase64(wire.CreatorX25519PublicKey, 32)
	}
	if err == nil && wire.ExpiresAt != "" {
		invitation.ExpiresAt, err = time.Parse(time.RFC3339Nano, wire.ExpiresAt)
	}
	if err != nil || validateInvitation(invitation) != nil {
		return syncclient.PairingInvitation{}, errors.New("pairing invitation fields are invalid")
	}
	return invitation, nil
}

func ReadPrivatePairingInvitation(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("pairing invitation file path must be absolute")
	}
	contents, err := readBoundedRegular(path, maxPairingTextBytes)
	if err != nil {
		return "", fmt.Errorf("read private pairing invitation: %w", err)
	}
	text := strings.TrimSpace(string(contents))
	if _, err := DecodePairingInvitation(text); err != nil {
		return "", err
	}
	return text, nil
}

func pairingProfile(profile, suffix string) (string, error) {
	if err := validateProfile(profile); err != nil {
		return "", err
	}
	if len(profile)+len(suffix) > 64 {
		return "", errors.New("profile is too long for pairing journal")
	}
	return profile + suffix, nil
}

type creatorPairingJournal struct {
	Version             int    `json:"version"`
	Invitation          string `json:"invitation"`
	ActiveDigest        string `json:"active_digest"`
	ApprovedBox         string `json:"approved_box,omitempty"`
	UpdatedCredential   string `json:"updated_credential,omitempty"`
	CancellationPending bool   `json:"cancellation_pending,omitempty"`
}

type joiningPairingJournal struct {
	Version           int    `json:"version"`
	RollbackPending   bool   `json:"rollback_pending,omitempty"`
	Invitation        string `json:"invitation"`
	DeviceID          string `json:"device_id"`
	DeviceToken       string `json:"device_token"`
	RollbackToken     string `json:"rollback_token"`
	SigningSeed       string `json:"signing_seed"`
	X25519Private     string `json:"x25519_private"`
	LocalDataKey      string `json:"local_data_key"`
	DeviceName        string `json:"device_name_ciphertext"`
	PendingCredential string `json:"pending_credential,omitempty"`
}

func encodePairingJournal(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded) < 2 || len(encoded) > maxPairingJournalBytes {
		return nil, errors.New("pairing journal exceeds size limit")
	}
	return encoded, nil
}

func decodePairingJournal(encoded []byte, destination any) error {
	if len(encoded) < 2 || len(encoded) > maxPairingJournalBytes {
		return errors.New("pairing journal length is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("pairing journal is invalid")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("pairing journal has trailing data")
	}
	return nil
}

func loadActiveCredential(ctx context.Context, options PairingOptions) ([]byte, CredentialBundleV1, error) {
	if options.Secrets == nil {
		return nil, CredentialBundleV1{}, errors.New("OS secret store is required")
	}
	if err := validateProfile(options.Profile); err != nil {
		return nil, CredentialBundleV1{}, err
	}
	encoded, err := options.Secrets.Load(ctx, options.Profile)
	if err != nil {
		return nil, CredentialBundleV1{}, err
	}
	bundle, err := DecodeCredentialBundle(encoded)
	if err != nil {
		zeroBytes(encoded)
		return nil, CredentialBundleV1{}, err
	}
	return encoded, bundle, nil
}

func upgradeBootstrapCredential(bundle *CredentialBundleV1) error {
	if bundle == nil {
		return errors.New("credential is required")
	}
	if bundle.Version == CredentialBundleVersion {
		return bundle.Validate()
	}
	if bundle.Version != legacyCredentialBundleVersion || len(bundle.VerificationKeys) != 1 {
		return errors.New("legacy multi-device credential lacks a signed X25519 roster and cannot be paired safely")
	}
	bundle.Version = CredentialBundleVersion
	bundle.TrustedRoster = protocol.PairingRoster{}
	return populateBootstrapTrust(bundle)
}

func replaceActiveByDigest(ctx context.Context, secrets SecretStore, profile, expectedDigest string, next []byte) error {
	current, err := secrets.Load(ctx, profile)
	if err != nil {
		return err
	}
	defer zeroBytes(current)
	if bytes.Equal(current, next) {
		return nil
	}
	digest := sha256.Sum256(current)
	if hex.EncodeToString(digest[:]) != expectedDigest {
		return errors.New("active credential changed after pairing invitation")
	}
	return replaceSecretExact(ctx, secrets, profile, current, next)
}

func accountAndRegistration(bundle CredentialBundleV1) (syncclient.Account, syncclient.DeviceRegistration, error) {
	ed, okEd := bundle.VerificationKeys[bundle.DeviceID]
	x, okX := bundle.X25519PublicKeys[bundle.DeviceID]
	if !okEd || !okX {
		return syncclient.Account{}, syncclient.DeviceRegistration{}, errors.New("current device is absent from local trust")
	}
	return syncclient.Account{
			AccountID: append([]byte(nil), bundle.AccountID[:]...), DeviceID: append([]byte(nil), bundle.DeviceID[:]...),
			DeviceToken: string(bundle.DeviceToken),
		}, syncclient.DeviceRegistration{
			DeviceNameCiphertext: make([]byte, 16), Ed25519PublicKey: append([]byte(nil), ed[:]...), X25519PublicKey: append([]byte(nil), x[:]...),
		}, nil
}

func StartPairing(ctx context.Context, relay PairingRelay, options PairingOptions) (PairingResult, error) {
	if relay == nil || options.Secrets == nil || options.Random == nil {
		return PairingResult{}, errors.New("pairing relay, OS secret store, and random source are required")
	}
	journalProfile, err := pairingProfile(options.Profile, creatorPairingSuffix)
	if err != nil {
		return PairingResult{}, err
	}
	activeEncoded, bundle, err := loadActiveCredential(ctx, options)
	if err != nil {
		return PairingResult{}, err
	}
	defer zeroBytes(activeEncoded)
	defer bundle.Zero()
	if err := upgradeBootstrapCredential(&bundle); err != nil {
		return PairingResult{}, err
	}
	if !rosterIsEmpty(bundle.TrustedRoster) || len(bundle.VerificationKeys) != 1 || len(bundle.X25519PublicKeys) != 1 {
		return PairingResult{}, errors.New("two-device preview is already paired; a third device is not supported")
	}
	upgraded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		return PairingResult{}, err
	}
	defer zeroBytes(upgraded)
	if !bytes.Equal(activeEncoded, upgraded) {
		if err := replaceSecretExact(ctx, options.Secrets, options.Profile, activeEncoded, upgraded); err != nil {
			return PairingResult{}, fmt.Errorf("upgrade active credential before pairing: %w", err)
		}
		activeEncoded = append(activeEncoded[:0], upgraded...)
	}
	digest := sha256.Sum256(activeEncoded)
	encodedJournal, found, err := loadOptionalSecret(ctx, options.Secrets, journalProfile)
	if err != nil {
		return PairingResult{}, err
	}
	var journal creatorPairingJournal
	if found {
		defer zeroBytes(encodedJournal)
		if err := decodePairingJournal(encodedJournal, &journal); err != nil || journal.Version != pairingJournalVersion ||
			journal.ActiveDigest != hex.EncodeToString(digest[:]) || journal.UpdatedCredential != "" || journal.ApprovedBox != "" ||
			journal.CancellationPending {
			return PairingResult{}, errors.New("creator pairing journal is invalid or already awaiting approval completion")
		}
	} else {
		account, registration, err := accountAndRegistration(bundle)
		if err != nil {
			return PairingResult{}, err
		}
		invitation, err := syncclient.GeneratePairingInvitation(account, registration, options.Random)
		if err != nil {
			return PairingResult{}, err
		}
		text, err := EncodePairingInvitation(invitation)
		if err != nil {
			return PairingResult{}, err
		}
		journal = creatorPairingJournal{Version: pairingJournalVersion, Invitation: text, ActiveDigest: hex.EncodeToString(digest[:])}
		encodedJournal, err = encodePairingJournal(journal)
		if err != nil {
			return PairingResult{}, err
		}
		defer zeroBytes(encodedJournal)
		if err := saveSecretExact(ctx, options.Secrets, journalProfile, encodedJournal); err != nil {
			return PairingResult{}, fmt.Errorf("save protected creator pairing journal: %w", err)
		}
	}
	invitation, err := DecodePairingInvitation(journal.Invitation)
	if err != nil {
		return PairingResult{}, err
	}
	account, _, err := accountAndRegistration(bundle)
	if err != nil {
		return PairingResult{}, err
	}
	created, err := relay.CreatePairing(ctx, account, invitation)
	if err != nil {
		return PairingResult{}, fmt.Errorf("create or resume pairing invitation: %w", err)
	}
	text, err := EncodePairingInvitation(created)
	if err != nil {
		return PairingResult{}, err
	}
	if text != journal.Invitation {
		updated := journal
		updated.Invitation = text
		updatedEncoded, err := encodePairingJournal(updated)
		if err != nil {
			return PairingResult{}, err
		}
		defer zeroBytes(updatedEncoded)
		if err := replaceSecretExact(ctx, options.Secrets, journalProfile, encodedJournal, updatedEncoded); err != nil {
			return PairingResult{}, fmt.Errorf("save pairing invitation expiry: %w", err)
		}
		journal = updated
	}
	return PairingResult{AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), State: "invited", Invitation: journal.Invitation}, nil
}

func transcriptFromStatus(invitation syncclient.PairingInvitation, status syncclient.PairingStatus) (protocol.PairingTranscript, error) {
	transcript := protocol.PairingTranscript{
		PairingID: invitation.PairingID, AccountID: invitation.AccountID,
		CreatorDeviceID: invitation.CreatorDeviceID, JoiningDeviceID: status.DeviceID,
		CreatorEd25519PublicKey: invitation.CreatorEd25519PublicKey, JoiningEd25519PublicKey: status.Ed25519PublicKey,
		CreatorX25519PublicKey: invitation.CreatorX25519PublicKey, JoiningX25519PublicKey: status.X25519PublicKey,
	}
	if err := protocol.VerifyPairingJoinProof(invitation.PairingSecret, transcript, status.JoinProof); err != nil {
		return protocol.PairingTranscript{}, err
	}
	return transcript, nil
}

func pairingPackageFromBundle(bundle CredentialBundleV1, transcript protocol.PairingTranscript) (protocol.PairingPackage, error) {
	if !rosterIsEmpty(bundle.TrustedRoster) || len(bundle.VerificationKeys) != 1 || len(bundle.X25519PublicKeys) != 1 {
		return protocol.PairingPackage{}, errors.New("two-device preview cannot approve a third trusted device")
	}
	devices := make([]protocol.PairingRosterDevice, 0, len(bundle.VerificationKeys)+1)
	for id, ed := range bundle.VerificationKeys {
		x, ok := bundle.X25519PublicKeys[id]
		if !ok {
			return protocol.PairingPackage{}, errors.New("trusted device lacks X25519 key")
		}
		devices = append(devices, protocol.PairingRosterDevice{DeviceID: append([]byte(nil), id[:]...), Ed25519PublicKey: append([]byte(nil), ed[:]...), X25519PublicKey: append([]byte(nil), x[:]...)})
	}
	for _, device := range devices {
		if bytes.Equal(device.DeviceID, transcript.JoiningDeviceID) {
			return protocol.PairingPackage{}, errors.New("joining device already exists in the signed trust roster")
		}
	}
	devices = append(devices, protocol.PairingRosterDevice{
		DeviceID: append([]byte(nil), transcript.JoiningDeviceID...), Ed25519PublicKey: append([]byte(nil), transcript.JoiningEd25519PublicKey...),
		X25519PublicKey: append([]byte(nil), transcript.JoiningX25519PublicKey...),
	})
	version := uint64(1)
	roster, err := protocol.SignPairingRoster(bundle.AccountID[:], version, devices, bundle.DeviceID[:], ed25519.NewKeyFromSeed(bundle.SigningSeed[:]))
	if err != nil {
		return protocol.PairingPackage{}, err
	}
	epochs := make([]uint64, 0, len(bundle.EpochKeys))
	for epoch := range bundle.EpochKeys {
		epochs = append(epochs, epoch)
	}
	sort.Slice(epochs, func(left, right int) bool { return epochs[left] < epochs[right] })
	payload := protocol.PairingPackage{CurrentEpoch: bundle.CurrentEpoch, ObjectIDKey: append([]byte(nil), bundle.ObjectIDKey[:]...), Roster: roster}
	for _, epoch := range epochs {
		key := bundle.EpochKeys[epoch]
		payload.EpochKeys = append(payload.EpochKeys, protocol.PairingEpochKey{Epoch: epoch, Key: append([]byte(nil), key[:]...)})
	}
	return payload, nil
}

func decodeCreatorUpdatedCredential(journal creatorPairingJournal) ([]byte, CredentialBundleV1, error) {
	if journal.ApprovedBox == "" || journal.UpdatedCredential == "" {
		return nil, CredentialBundleV1{}, errors.New("creator pairing journal has an incomplete approval phase")
	}
	if _, err := protocol.DecodeSealedBox(journal.ApprovedBox); err != nil {
		return nil, CredentialBundleV1{}, errors.New("creator pairing journal contains an invalid sealed package")
	}
	encoded, err := decodeCanonicalBase64Bounded(journal.UpdatedCredential, maxCredentialBlobBytes)
	if err != nil {
		return nil, CredentialBundleV1{}, errors.New("creator pairing journal contains an invalid updated credential")
	}
	bundle, err := DecodeCredentialBundle(encoded)
	if err != nil {
		zeroBytes(encoded)
		return nil, CredentialBundleV1{}, err
	}
	if rosterIsEmpty(bundle.TrustedRoster) || len(bundle.TrustedRoster.Devices) != 2 {
		zeroBytes(encoded)
		bundle.Zero()
		return nil, CredentialBundleV1{}, errors.New("creator pairing journal lacks an exact two-device signed roster")
	}
	return encoded, bundle, nil
}

func originalBootstrapCredential(updated CredentialBundleV1, expectedDigest string) ([]byte, error) {
	if rosterIsEmpty(updated.TrustedRoster) || len(updated.TrustedRoster.Devices) != 2 {
		return nil, errors.New("updated credential does not contain a two-device roster")
	}
	updated.TrustedRoster = protocol.PairingRoster{}
	if err := populateBootstrapTrust(&updated); err != nil {
		return nil, err
	}
	original, err := EncodeCredentialBundle(updated)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(original)
	if hex.EncodeToString(digest[:]) != expectedDigest {
		zeroBytes(original)
		return nil, errors.New("creator journal cannot reconstruct its original bootstrap credential")
	}
	return original, nil
}

func verifyCreatorClaimedStatus(invitation syncclient.PairingInvitation, status syncclient.PairingStatus, updated CredentialBundleV1) error {
	if status.State != "ready" && status.State != "finalized" {
		return errors.New("pairing has not reached an authenticated durable-ready state")
	}
	transcript, err := transcriptFromStatus(invitation, status)
	if err != nil {
		return fmt.Errorf("verify claimed pairing transcript: %w", err)
	}
	if !bytes.Equal(updated.AccountID[:], transcript.AccountID) || !bytes.Equal(updated.DeviceID[:], transcript.CreatorDeviceID) ||
		!bytes.Equal(updated.TrustedRoster.SignerDeviceID, transcript.CreatorDeviceID) || updated.TrustedRoster.Version != 1 ||
		len(updated.TrustedRoster.Devices) != 2 {
		return errors.New("claimed pairing differs from the protected creator credential")
	}
	creatorFound, joiningFound := false, false
	for _, device := range updated.TrustedRoster.Devices {
		switch {
		case bytes.Equal(device.DeviceID, transcript.CreatorDeviceID):
			creatorFound = bytes.Equal(device.Ed25519PublicKey, transcript.CreatorEd25519PublicKey) &&
				bytes.Equal(device.X25519PublicKey, transcript.CreatorX25519PublicKey)
		case bytes.Equal(device.DeviceID, transcript.JoiningDeviceID):
			joiningFound = bytes.Equal(device.Ed25519PublicKey, transcript.JoiningEd25519PublicKey) &&
				bytes.Equal(device.X25519PublicKey, transcript.JoiningX25519PublicKey)
		default:
			return errors.New("claimed pairing roster contains an unrelated device")
		}
	}
	if !creatorFound || !joiningFound {
		return errors.New("claimed pairing roster does not match both authenticated devices")
	}
	return nil
}

func terminalPairingError(err error) bool {
	var api *syncclient.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.Code {
	case "pairing_not_found", "pairing_expired", "invalid_or_expired_pairing":
		return true
	default:
		return false
	}
}

func cleanupCreatorGhostTrust(ctx context.Context, options PairingOptions, journalProfile string,
	journalEncoded []byte, journal creatorPairingJournal, updatedEncoded []byte, updated CredentialBundleV1) error {
	original, err := originalBootstrapCredential(updated, journal.ActiveDigest)
	if err != nil {
		return err
	}
	defer zeroBytes(original)
	current, err := options.Secrets.Load(context.WithoutCancel(ctx), options.Profile)
	if err != nil {
		return err
	}
	defer zeroBytes(current)
	switch {
	case bytes.Equal(current, original):
	case bytes.Equal(current, updatedEncoded):
		if err := replaceSecretExact(context.WithoutCancel(ctx), options.Secrets, options.Profile, current, original); err != nil {
			return fmt.Errorf("restore bootstrap trust after terminal pairing: %w", err)
		}
	default:
		return errors.New("active credential changed while terminal pairing cleanup was pending")
	}
	if err := deleteSecretExact(context.WithoutCancel(ctx), options.Secrets, journalProfile, journalEncoded); err != nil {
		return fmt.Errorf("remove terminal creator pairing journal: %w", err)
	}
	return nil
}

// CancelCreatorPairing is a crash-resumable creator-side cancellation state
// machine. It records operator intent in the protected journal before making
// the remote request, and removes local pending trust only after the relay has
// durably accepted (or idempotently replayed) the cancellation.
func CancelCreatorPairing(ctx context.Context, relay PairingRelay, options PairingOptions) (PairingResult, error) {
	if relay == nil || options.Secrets == nil {
		return PairingResult{}, errors.New("pairing relay and OS secret store are required")
	}
	journalProfile, err := pairingProfile(options.Profile, creatorPairingSuffix)
	if err != nil {
		return PairingResult{}, err
	}
	encodedJournal, err := options.Secrets.Load(ctx, journalProfile)
	if err != nil {
		return PairingResult{}, err
	}
	defer zeroBytes(encodedJournal)
	var journal creatorPairingJournal
	if err := decodePairingJournal(encodedJournal, &journal); err != nil || journal.Version != pairingJournalVersion {
		return PairingResult{}, errors.New("creator pairing journal is invalid")
	}
	invitation, err := DecodePairingInvitation(journal.Invitation)
	if err != nil {
		return PairingResult{}, err
	}
	activeEncoded, active, err := loadActiveCredential(ctx, options)
	if err != nil {
		return PairingResult{}, err
	}
	defer zeroBytes(activeEncoded)
	defer active.Zero()
	account, _, err := accountAndRegistration(active)
	if err != nil {
		return PairingResult{}, err
	}
	if !bytes.Equal(account.AccountID, invitation.AccountID) || !bytes.Equal(account.DeviceID, invitation.CreatorDeviceID) {
		return PairingResult{}, errors.New("active credential differs from the protected creator invitation")
	}

	hasApprovedMaterial := journal.ApprovedBox != "" || journal.UpdatedCredential != ""
	if (journal.ApprovedBox == "") != (journal.UpdatedCredential == "") {
		return PairingResult{}, errors.New("creator pairing journal has an incomplete approval phase")
	}
	var updatedEncoded []byte
	var updated CredentialBundleV1
	if hasApprovedMaterial {
		updatedEncoded, updated, err = decodeCreatorUpdatedCredential(journal)
		if err != nil {
			return PairingResult{}, err
		}
		defer zeroBytes(updatedEncoded)
		defer updated.Zero()
		original, err := originalBootstrapCredential(updated, journal.ActiveDigest)
		if err != nil {
			return PairingResult{}, err
		}
		matchesKnownCredential := bytes.Equal(activeEncoded, original) || bytes.Equal(activeEncoded, updatedEncoded)
		zeroBytes(original)
		if !matchesKnownCredential {
			return PairingResult{}, errors.New("active credential changed while pairing cancellation was pending")
		}
	} else {
		digest := sha256.Sum256(activeEncoded)
		if hex.EncodeToString(digest[:]) != journal.ActiveDigest {
			return PairingResult{}, errors.New("active credential changed after pairing invitation")
		}
	}

	currentJournal := encodedJournal
	if !journal.CancellationPending {
		journal.CancellationPending = true
		pendingJournal, err := encodePairingJournal(journal)
		if err != nil {
			return PairingResult{}, err
		}
		defer zeroBytes(pendingJournal)
		if err := replaceSecretExact(ctx, options.Secrets, journalProfile, encodedJournal, pendingJournal); err != nil {
			return PairingResult{}, fmt.Errorf("persist creator pairing cancellation intent: %w", err)
		}
		currentJournal = pendingJournal
	}
	if err := relay.CancelPairing(ctx, invitation.PairingID, account.DeviceToken); err != nil {
		return PairingResult{}, fmt.Errorf("cancel creator pairing on relay: %w", err)
	}
	cleanupContext := context.WithoutCancel(ctx)
	if hasApprovedMaterial {
		if err := cleanupCreatorGhostTrust(cleanupContext, options, journalProfile, currentJournal, journal, updatedEncoded, updated); err != nil {
			return PairingResult{}, fmt.Errorf("finish creator pairing cancellation: %w", err)
		}
	} else if err := deleteSecretExact(cleanupContext, options.Secrets, journalProfile, currentJournal); err != nil {
		return PairingResult{}, fmt.Errorf("finish creator pairing cancellation: %w", err)
	}
	return PairingResult{
		AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), State: "cancelled",
	}, nil
}

func ApprovePairing(ctx context.Context, relay PairingRelay, options PairingOptions) (PairingResult, error) {
	if relay == nil || options.Secrets == nil || options.Random == nil {
		return PairingResult{}, errors.New("pairing relay, OS secret store, and random source are required")
	}
	journalProfile, err := pairingProfile(options.Profile, creatorPairingSuffix)
	if err != nil {
		return PairingResult{}, err
	}
	encodedJournal, err := options.Secrets.Load(ctx, journalProfile)
	if err != nil {
		return PairingResult{}, err
	}
	defer zeroBytes(encodedJournal)
	var journal creatorPairingJournal
	if err := decodePairingJournal(encodedJournal, &journal); err != nil || journal.Version != pairingJournalVersion {
		return PairingResult{}, errors.New("creator pairing journal is invalid")
	}
	if journal.CancellationPending {
		return PairingResult{}, errors.New("creator pairing cancellation is pending")
	}
	invitation, err := DecodePairingInvitation(journal.Invitation)
	if err != nil {
		return PairingResult{}, err
	}
	activeEncoded, bundle, err := loadActiveCredential(ctx, options)
	if err != nil {
		return PairingResult{}, err
	}
	defer zeroBytes(activeEncoded)
	defer bundle.Zero()
	account, _, err := accountAndRegistration(bundle)
	if err != nil {
		return PairingResult{}, err
	}
	var box protocol.SealedBox
	var updatedCredential []byte
	if journal.ApprovedBox == "" && journal.UpdatedCredential == "" {
		digest := sha256.Sum256(activeEncoded)
		if hex.EncodeToString(digest[:]) != journal.ActiveDigest {
			return PairingResult{}, errors.New("active credential changed after pairing invitation")
		}
		status, err := relay.GetPairing(ctx, invitation, account)
		if err != nil {
			return PairingResult{}, fmt.Errorf("load authenticated joining device: %w", err)
		}
		if status.State != "joined" && status.State != "approved" && status.State != "claimed" {
			return PairingResult{}, errors.New("joining device has not joined this invitation")
		}
		transcript, err := transcriptFromStatus(invitation, status)
		if err != nil {
			return PairingResult{}, fmt.Errorf("verify pairing join proof: %w", err)
		}
		payload, err := pairingPackageFromBundle(bundle, transcript)
		if err != nil {
			return PairingResult{}, err
		}
		box, err = syncclient.SealPairingPackage(invitation, transcript, payload, bundle.X25519Private[:], options.Random)
		if err != nil {
			return PairingResult{}, err
		}
		if err := applyTrustedRoster(&bundle, payload.Roster); err != nil {
			return PairingResult{}, err
		}
		updatedCredential, err = EncodeCredentialBundle(bundle)
		if err != nil {
			return PairingResult{}, err
		}
		defer zeroBytes(updatedCredential)
		encodedBox, err := protocol.EncodeSealedBox(box)
		if err != nil {
			return PairingResult{}, err
		}
		updatedJournal := journal
		updatedJournal.ApprovedBox = encodedBox
		updatedJournal.UpdatedCredential = base64.RawURLEncoding.EncodeToString(updatedCredential)
		updatedEncoded, err := encodePairingJournal(updatedJournal)
		if err != nil {
			return PairingResult{}, err
		}
		defer zeroBytes(updatedEncoded)
		if err := replaceSecretExact(ctx, options.Secrets, journalProfile, encodedJournal, updatedEncoded); err != nil {
			return PairingResult{}, fmt.Errorf("save resumable pairing approval: %w", err)
		}
		journal = updatedJournal
	} else {
		var updatedBundle CredentialBundleV1
		updatedCredential, updatedBundle, err = decodeCreatorUpdatedCredential(journal)
		if err != nil {
			return PairingResult{}, err
		}
		defer zeroBytes(updatedCredential)
		updatedBundle.Zero()
		box, err = protocol.DecodeSealedBox(journal.ApprovedBox)
		if err != nil {
			return PairingResult{}, err
		}
	}
	if err := relay.ApprovePairing(ctx, invitation.PairingID, account.DeviceToken, box); err != nil {
		return PairingResult{}, fmt.Errorf("approve or resume pairing: %w", err)
	}
	// Approval only uploads an opaque package. The creator deliberately keeps
	// its self-only trust until a separate resume/finalize observes that the
	// joining device has durably committed and marked this exact transcript ready.
	return PairingResult{AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), State: "awaiting_claim"}, nil
}

// FinalizePairing is intentionally separate from approval. It closes the
// creator-side ghost-trust window: a second device enters active trust only
// after the relay reports a ready (or already finalized) pairing whose
// PSK-authenticated public material exactly matches the protected signed roster.
// Terminal rollback or expiry restores the canonical self-only credential
// before journal removal.
func FinalizePairing(ctx context.Context, relay PairingRelay, options PairingOptions) (PairingResult, error) {
	if relay == nil || options.Secrets == nil {
		return PairingResult{}, errors.New("pairing relay and OS secret store are required")
	}
	journalProfile, err := pairingProfile(options.Profile, creatorPairingSuffix)
	if err != nil {
		return PairingResult{}, err
	}
	encodedJournal, err := options.Secrets.Load(ctx, journalProfile)
	if err != nil {
		return PairingResult{}, err
	}
	defer zeroBytes(encodedJournal)
	var journal creatorPairingJournal
	if err := decodePairingJournal(encodedJournal, &journal); err != nil || journal.Version != pairingJournalVersion {
		return PairingResult{}, errors.New("creator pairing journal is invalid")
	}
	if journal.CancellationPending {
		return PairingResult{}, errors.New("creator pairing cancellation is pending")
	}
	invitation, err := DecodePairingInvitation(journal.Invitation)
	if err != nil {
		return PairingResult{}, err
	}
	updatedEncoded, updated, err := decodeCreatorUpdatedCredential(journal)
	if err != nil {
		return PairingResult{}, err
	}
	defer zeroBytes(updatedEncoded)
	defer updated.Zero()
	account := syncclient.Account{AccountID: append([]byte(nil), updated.AccountID[:]...), DeviceID: append([]byte(nil), updated.DeviceID[:]...), DeviceToken: string(updated.DeviceToken)}
	status, err := relay.GetPairing(ctx, invitation, account)
	if err != nil {
		if !terminalPairingError(err) {
			return PairingResult{}, fmt.Errorf("load pairing finalization state: %w", err)
		}
		if cancelErr := relay.CancelPairing(context.WithoutCancel(ctx), invitation.PairingID, account.DeviceToken); cancelErr != nil {
			return PairingResult{}, errors.Join(err, fmt.Errorf("confirm terminal pairing cancellation: %w", cancelErr))
		}
		if cleanupErr := cleanupCreatorGhostTrust(ctx, options, journalProfile, encodedJournal, journal, updatedEncoded, updated); cleanupErr != nil {
			return PairingResult{}, errors.Join(err, cleanupErr)
		}
		return PairingResult{AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), State: "rolled_back"}, nil
	}
	if status.Expired && status.State != "ready" && status.State != "finalized" {
		if err := relay.CancelPairing(context.WithoutCancel(ctx), invitation.PairingID, account.DeviceToken); err != nil {
			return PairingResult{}, fmt.Errorf("cancel expired pairing before local cleanup: %w", err)
		}
		if err := cleanupCreatorGhostTrust(ctx, options, journalProfile, encodedJournal, journal, updatedEncoded, updated); err != nil {
			return PairingResult{}, err
		}
		return PairingResult{AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), State: "expired"}, nil
	}
	if status.State != "ready" && status.State != "finalized" {
		return PairingResult{AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), State: "claim_pending"}, nil
	}
	if err := verifyCreatorClaimedStatus(invitation, status, updated); err != nil {
		return PairingResult{}, err
	}
	if status.State == "ready" {
		if err := relay.FinalizePairing(ctx, invitation.PairingID, account.DeviceToken); err != nil {
			return PairingResult{}, fmt.Errorf("finalize ready pairing on relay: %w", err)
		}
	}
	if err := replaceActiveByDigest(ctx, options.Secrets, options.Profile, journal.ActiveDigest, updatedEncoded); err != nil {
		return PairingResult{}, fmt.Errorf("commit claimed signed trust roster: %w", err)
	}
	if err := options.Secrets.Delete(context.WithoutCancel(ctx), journalProfile); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return PairingResult{}, fmt.Errorf("claimed trust is active but creator journal cleanup is pending: %w", err)
	}
	return PairingResult{AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), State: "ready"}, nil
}

func preflightJoin(ctx context.Context, options PairingOptions, journalProfile string) error {
	for _, profile := range []string{options.Profile, journalProfile} {
		value, found, err := loadOptionalSecret(ctx, options.Secrets, profile)
		if found {
			zeroBytes(value)
		}
		if err != nil {
			return err
		}
		if found {
			return errors.New("joining profile already has active or pending pairing material")
		}
	}
	provisioning, err := provisioningProfile(options.Profile)
	if err != nil {
		return err
	}
	if value, found, err := loadOptionalSecret(ctx, options.Secrets, provisioning); err != nil {
		return err
	} else if found {
		zeroBytes(value)
		return errors.New("account provisioning is already pending for this profile")
	}
	return preflightDatabasePath(options.DatabasePath)
}

func decodeJoiningJournal(encoded []byte) (joiningPairingJournal, syncclient.PairingInvitation, syncclient.Account, syncclient.DeviceRegistration, []byte, []byte, []byte, error) {
	var journal joiningPairingJournal
	if err := decodePairingJournal(encoded, &journal); err != nil || journal.Version != pairingJournalVersion {
		return journal, syncclient.PairingInvitation{}, syncclient.Account{}, syncclient.DeviceRegistration{}, nil, nil, nil, errors.New("joining pairing journal is invalid")
	}
	invitation, err := DecodePairingInvitation(journal.Invitation)
	if err != nil {
		return journal, invitation, syncclient.Account{}, syncclient.DeviceRegistration{}, nil, nil, nil, err
	}
	deviceID, err := decodeCanonicalHex(journal.DeviceID, 16)
	if err != nil || !validCanonicalToken(journal.DeviceToken) || !validCanonicalToken(journal.RollbackToken) {
		return journal, invitation, syncclient.Account{}, syncclient.DeviceRegistration{}, nil, nil, nil, errors.New("joining pairing credentials are invalid")
	}
	seed, err := decodeCanonicalBase64(journal.SigningSeed, ed25519.SeedSize)
	if err != nil {
		return journal, invitation, syncclient.Account{}, syncclient.DeviceRegistration{}, nil, nil, nil, err
	}
	xPrivate, err := decodeCanonicalBase64(journal.X25519Private, 32)
	if err != nil {
		zeroBytes(seed)
		return journal, invitation, syncclient.Account{}, syncclient.DeviceRegistration{}, nil, nil, nil, err
	}
	localDataKey, err := decodeCanonicalBase64(journal.LocalDataKey, 32)
	if err != nil {
		zeroBytes(seed)
		zeroBytes(xPrivate)
		return journal, invitation, syncclient.Account{}, syncclient.DeviceRegistration{}, nil, nil, nil, err
	}
	name, err := decodeCanonicalBase64(journal.DeviceName, 32)
	if err != nil {
		zeroBytes(seed)
		zeroBytes(xPrivate)
		zeroBytes(localDataKey)
		return journal, invitation, syncclient.Account{}, syncclient.DeviceRegistration{}, nil, nil, nil, err
	}
	xPublic, err := curve25519.X25519(xPrivate, curve25519.Basepoint)
	if err != nil {
		return journal, invitation, syncclient.Account{}, syncclient.DeviceRegistration{}, nil, nil, nil, err
	}
	account := syncclient.Account{AccountID: append([]byte(nil), invitation.AccountID...), DeviceID: deviceID,
		DeviceToken: journal.DeviceToken, DeviceRollbackToken: journal.RollbackToken}
	registration := syncclient.DeviceRegistration{DeviceNameCiphertext: name, Ed25519PublicKey: ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), X25519PublicKey: xPublic}
	return journal, invitation, account, registration, seed, xPrivate, localDataKey, nil
}

func JoinPairing(ctx context.Context, relay PairingRelay, options PairingOptions, invitationText string) (PairingResult, error) {
	if relay == nil || options.Secrets == nil || options.Random == nil {
		return PairingResult{}, errors.New("pairing relay, OS secret store, and random source are required")
	}
	journalProfile, err := pairingProfile(options.Profile, joiningPairingSuffix)
	if err != nil {
		return PairingResult{}, err
	}
	encoded, found, err := loadOptionalSecret(ctx, options.Secrets, journalProfile)
	if err != nil {
		return PairingResult{}, err
	}
	if found {
		defer zeroBytes(encoded)
		var existing joiningPairingJournal
		if err := decodePairingJournal(encoded, &existing); err != nil || existing.Version != pairingJournalVersion {
			return PairingResult{}, errors.New("joining pairing journal is invalid")
		}
		if strings.TrimSpace(invitationText) != "" && strings.TrimSpace(invitationText) != existing.Invitation {
			return PairingResult{}, errors.New("supplied invitation differs from protected pending pairing")
		}
	} else {
		if err := preflightJoin(ctx, options, journalProfile); err != nil {
			return PairingResult{}, err
		}
		invitation, err := DecodePairingInvitation(invitationText)
		if err != nil {
			return PairingResult{}, err
		}
		keys, err := protocol.NewDeviceKeys(options.Random)
		if err != nil {
			return PairingResult{}, err
		}
		defer zeroBytes(keys.Ed25519Private)
		defer zeroBytes(keys.X25519Private)
		credentials, err := syncclient.GenerateDeviceCredentials(invitation.AccountID, options.Random)
		if err != nil {
			return PairingResult{}, err
		}
		localDataKey := make([]byte, 32)
		deviceName := make([]byte, 32)
		defer zeroBytes(localDataKey)
		defer zeroBytes(deviceName)
		if err := fillRandom(options.Random, localDataKey, deviceName); err != nil {
			return PairingResult{}, err
		}
		journal := joiningPairingJournal{
			Version: pairingJournalVersion, Invitation: strings.TrimSpace(invitationText), DeviceID: hex.EncodeToString(credentials.DeviceID),
			DeviceToken: credentials.DeviceToken, RollbackToken: credentials.DeviceRollbackToken,
			SigningSeed:   base64.RawURLEncoding.EncodeToString(keys.Ed25519Private.Seed()),
			X25519Private: base64.RawURLEncoding.EncodeToString(keys.X25519Private), LocalDataKey: base64.RawURLEncoding.EncodeToString(localDataKey),
			DeviceName: base64.RawURLEncoding.EncodeToString(deviceName),
		}
		encoded, err = encodePairingJournal(journal)
		if err != nil {
			return PairingResult{}, err
		}
		defer zeroBytes(encoded)
		if err := saveSecretExact(ctx, options.Secrets, journalProfile, encoded); err != nil {
			return PairingResult{}, fmt.Errorf("save protected joining pairing journal: %w", err)
		}
	}
	journal, invitation, account, registration, seed, xPrivate, localDataKey, err := decodeJoiningJournal(encoded)
	defer zeroBytes(seed)
	defer zeroBytes(xPrivate)
	defer zeroBytes(localDataKey)
	if err != nil {
		return PairingResult{}, err
	}
	if _, err := relay.JoinPairing(ctx, invitation, account, registration); err != nil {
		return PairingResult{}, fmt.Errorf("join or resume pairing: %w", err)
	}
	return PairingResult{AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), DeviceIDHex: journal.DeviceID, State: "joined"}, nil
}

func bundleFromPairingPackage(account syncclient.Account, seed, xPrivate, localDataKey []byte, payload protocol.PairingPackage) (CredentialBundleV1, error) {
	bundle := CredentialBundleV1{
		Version: CredentialBundleVersion, DeviceToken: []byte(account.DeviceToken), CurrentEpoch: payload.CurrentEpoch,
		EpochKeys: make(map[uint64][32]byte, len(payload.EpochKeys)),
	}
	copy(bundle.AccountID[:], account.AccountID)
	copy(bundle.DeviceID[:], account.DeviceID)
	copy(bundle.SigningSeed[:], seed)
	copy(bundle.X25519Private[:], xPrivate)
	copy(bundle.LocalDataKey[:], localDataKey)
	copy(bundle.ObjectIDKey[:], payload.ObjectIDKey)
	for _, epoch := range payload.EpochKeys {
		var key [32]byte
		copy(key[:], epoch.Key)
		bundle.EpochKeys[epoch.Epoch] = key
	}
	if err := applyTrustedRoster(&bundle, payload.Roster); err != nil {
		bundle.Zero()
		return CredentialBundleV1{}, err
	}
	if err := bundle.Validate(); err != nil {
		bundle.Zero()
		return CredentialBundleV1{}, err
	}
	return bundle, nil
}

func decodeJoiningPendingCredential(journal joiningPairingJournal, account syncclient.Account,
	seed, xPrivate, localDataKey []byte) ([]byte, CredentialBundleV1, error) {
	if journal.PendingCredential == "" {
		return nil, CredentialBundleV1{}, errors.New("joining journal has no authenticated pending credential")
	}
	encoded, err := decodeCanonicalBase64Bounded(journal.PendingCredential, maxCredentialBlobBytes)
	if err != nil {
		return nil, CredentialBundleV1{}, errors.New("joining journal pending credential is invalid")
	}
	bundle, err := DecodeCredentialBundle(encoded)
	if err != nil {
		zeroBytes(encoded)
		return nil, CredentialBundleV1{}, err
	}
	if !bytes.Equal(bundle.AccountID[:], account.AccountID) || !bytes.Equal(bundle.DeviceID[:], account.DeviceID) ||
		string(bundle.DeviceToken) != account.DeviceToken || !bytes.Equal(bundle.SigningSeed[:], seed) ||
		!bytes.Equal(bundle.X25519Private[:], xPrivate) || !bytes.Equal(bundle.LocalDataKey[:], localDataKey) ||
		rosterIsEmpty(bundle.TrustedRoster) || len(bundle.TrustedRoster.Devices) != 2 {
		zeroBytes(encoded)
		bundle.Zero()
		return nil, CredentialBundleV1{}, errors.New("joining journal credential identities differ from its protected pairing material")
	}
	return encoded, bundle, nil
}

func persistJoiningPendingCredential(ctx context.Context, options PairingOptions, journalProfile string, credential []byte) error {
	encoded, err := options.Secrets.Load(ctx, journalProfile)
	if err != nil {
		return err
	}
	defer zeroBytes(encoded)
	var journal joiningPairingJournal
	if err := decodePairingJournal(encoded, &journal); err != nil || journal.Version != pairingJournalVersion || journal.RollbackPending {
		return errors.New("joining pairing journal cannot enter local commit phase")
	}
	canonical := base64.RawURLEncoding.EncodeToString(credential)
	if journal.PendingCredential != "" {
		if journal.PendingCredential != canonical {
			return errors.New("joining pairing journal already contains different authenticated material")
		}
		return nil
	}
	journal.PendingCredential = canonical
	updated, err := encodePairingJournal(journal)
	if err != nil {
		return err
	}
	defer zeroBytes(updated)
	if err := replaceSecretExact(ctx, options.Secrets, journalProfile, encoded, updated); err != nil {
		return fmt.Errorf("persist authenticated joining credential before local commit: %w", err)
	}
	return nil
}

type joiningDatabaseRollbackOps struct {
	remove     func(string) error
	syncParent func(string) error
}

func removeValidatedJoiningDatabaseFiles(path string, validated []string, ops joiningDatabaseRollbackOps) error {
	if ops.remove == nil || ops.syncParent == nil || len(validated) == 0 || validated[0] != path {
		return errors.New("validated local database rollback set is required")
	}
	allowed := databaseSidecarPaths(path)
	previous := -1
	for _, candidate := range validated {
		index := -1
		for allowedIndex, allowedPath := range allowed {
			if candidate == allowedPath {
				index = allowedIndex
				break
			}
		}
		if index <= previous {
			return errors.New("validated local database rollback set is not canonical")
		}
		previous = index
	}
	// Keep the exact-key-verifiable main database until every sidecar has been
	// removed and that directory state has been made durable. A crash at any
	// sidecar boundary can therefore restart validation from the main file.
	for _, sidecar := range validated[1:] {
		if err := ops.remove(sidecar); err != nil {
			return fmt.Errorf("remove verified local database sidecar: %w", err)
		}
	}
	parent := filepath.Dir(path)
	if err := ops.syncParent(parent); err != nil {
		return fmt.Errorf("persist local database sidecar rollback: %w", err)
	}
	if err := ops.remove(path); err != nil {
		return fmt.Errorf("remove verified local database: %w", err)
	}
	return ops.syncParent(parent)
}

func cleanupUncommittedDatabase(ctx context.Context, path string, bundle CredentialBundleV1) error {
	return cleanupUncommittedDatabaseWithOps(ctx, path, bundle, joiningDatabaseRollbackOps{
		remove: removePrivateFile, syncParent: syncParentDirectory,
	})
}

func cleanupUncommittedDatabaseWithOps(ctx context.Context, path string, bundle CredentialBundleV1, ops joiningDatabaseRollbackOps) error {
	if ops.remove == nil || ops.syncParent == nil {
		return errors.New("local database rollback operations are required")
	}
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		for _, sidecar := range databaseSidecarPaths(path)[1:] {
			if _, sidecarErr := os.Lstat(sidecar); sidecarErr == nil {
				return errors.New("unverified local database sidecar remains without its database")
			} else if !errors.Is(sidecarErr, os.ErrNotExist) {
				return fmt.Errorf("inspect local database sidecar: %w", sidecarErr)
			}
		}
		// This also reconciles a crash after the final unlink but before its
		// directory fsync. Replaying the parent sync is harmless and durable.
		return ops.syncParent(filepath.Dir(path))
	}
	if err != nil {
		return fmt.Errorf("inspect local database before rollback: %w", err)
	}
	if err := ensureEncryptedStore(ctx, path, bundle, nil); err != nil {
		return fmt.Errorf("refuse to remove an unverified local database: %w", err)
	}
	store, err := localstore.OpenForDevice(ctx, path, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex())
	if err != nil {
		return fmt.Errorf("open exact joining database before rollback: %w", err)
	}
	_, snapshotErr := store.Snapshot(ctx)
	closeErr := store.Close()
	if snapshotErr != nil {
		return fmt.Errorf("authenticate joining database before rollback: %w", snapshotErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close authenticated joining database before rollback: %w", closeErr)
	}
	if err := protectPrivateDatabaseFiles(path); err != nil {
		return fmt.Errorf("verify authenticated joining database permissions: %w", err)
	}
	validated := make([]string, 0, len(databaseSidecarPaths(path)))
	for index, candidate := range databaseSidecarPaths(path) {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) && index > 0 {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect local database rollback file: %w", err)
		}
		file, err := os.OpenFile(candidate, os.O_RDONLY, 0)
		if err != nil {
			return fmt.Errorf("open local database rollback file: %w", err)
		}
		opened, statErr := file.Stat()
		openedPrivate := openedPrivateFilePermissionsOK(candidate, file, false)
		closeErr := file.Close()
		after, afterErr := os.Lstat(candidate)
		if statErr != nil || closeErr != nil || afterErr != nil || !info.Mode().IsRegular() ||
			info.Mode()&os.ModeSymlink != 0 || !privateFilePermissionsOK(candidate, info) ||
			!openedPrivate || !os.SameFile(info, opened) ||
			!os.SameFile(opened, after) {
			return errors.New("local database rollback file changed or is not an exact private owned regular file")
		}
		validated = append(validated, candidate)
	}
	return removeValidatedJoiningDatabaseFiles(path, validated, ops)
}

type joiningRollbackLocalState struct {
	journalEncoded []byte
	journal        joiningPairingJournal
	invitation     syncclient.PairingInvitation
	account        syncclient.Account
	registration   syncclient.DeviceRegistration
	credential     []byte
	bundle         CredentialBundleV1
	hasCredential  bool
	activePresent  bool
	databaseExists bool
}

func (state *joiningRollbackLocalState) zero() {
	if state == nil {
		return
	}
	zeroBytes(state.journalEncoded)
	zeroBytes(state.credential)
	state.bundle.Zero()
}

func databaseArtifactsPresent(path string) (bool, bool, error) {
	if path == "" || !filepath.IsAbs(path) {
		return false, false, errors.New("local database path must be absolute")
	}
	mainPresent := false
	anyPresent := false
	for index, candidate := range databaseSidecarPaths(path) {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, false, fmt.Errorf("inspect local database rollback artifact: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !privateFilePermissionsOK(candidate, info) {
			return false, false, errors.New("local database rollback artifact is not a private regular file")
		}
		anyPresent = true
		if index == 0 {
			mainPresent = true
		}
	}
	return anyPresent, mainPresent, nil
}

func inspectJoiningRollbackLocalState(ctx context.Context, options PairingOptions, journalProfile string) (joiningRollbackLocalState, error) {
	var state joiningRollbackLocalState
	encoded, err := options.Secrets.Load(ctx, journalProfile)
	if err != nil {
		return state, err
	}
	state.journalEncoded = encoded
	journal, invitation, account, registration, seed, xPrivate, localDataKey, err := decodeJoiningJournal(encoded)
	defer zeroBytes(seed)
	defer zeroBytes(xPrivate)
	defer zeroBytes(localDataKey)
	if err != nil {
		state.zero()
		return joiningRollbackLocalState{}, err
	}
	state.journal = journal
	state.invitation = invitation
	state.account = account
	state.registration = registration
	if journal.PendingCredential != "" {
		state.credential, state.bundle, err = decodeJoiningPendingCredential(journal, account, seed, xPrivate, localDataKey)
		if err != nil {
			state.zero()
			return joiningRollbackLocalState{}, err
		}
		state.hasCredential = true
	}
	active, found, err := loadOptionalSecret(ctx, options.Secrets, options.Profile)
	if found {
		defer zeroBytes(active)
	}
	if err != nil {
		state.zero()
		return joiningRollbackLocalState{}, err
	}
	state.activePresent = found
	if found && (!state.hasCredential || !bytes.Equal(active, state.credential)) {
		state.zero()
		return joiningRollbackLocalState{}, errors.New("active credential differs from the protected joining journal")
	}
	artifacts, mainPresent, err := databaseArtifactsPresent(options.DatabasePath)
	if err != nil {
		state.zero()
		return joiningRollbackLocalState{}, err
	}
	state.databaseExists = artifacts
	if artifacts && !state.hasCredential {
		state.zero()
		return joiningRollbackLocalState{}, errors.New("local database exists without an authenticated pending joining credential")
	}
	if artifacts && !mainPresent {
		state.zero()
		return joiningRollbackLocalState{}, errors.New("local database sidecar exists without its authenticated database")
	}
	if mainPresent {
		if err := ensureEncryptedStore(ctx, options.DatabasePath, state.bundle, nil); err != nil {
			state.zero()
			return joiningRollbackLocalState{}, fmt.Errorf("verify joining database before rollback: %w", err)
		}
	}
	return state, nil
}

func markJoiningRollbackPending(ctx context.Context, options PairingOptions, journalProfile string) (syncclient.PairingInvitation, syncclient.Account, error) {
	encoded, err := options.Secrets.Load(ctx, journalProfile)
	if err != nil {
		return syncclient.PairingInvitation{}, syncclient.Account{}, err
	}
	defer zeroBytes(encoded)
	journal, invitation, account, _, seed, xPrivate, localDataKey, err := decodeJoiningJournal(encoded)
	zeroBytes(seed)
	zeroBytes(xPrivate)
	zeroBytes(localDataKey)
	if err != nil {
		return syncclient.PairingInvitation{}, syncclient.Account{}, err
	}
	if journal.RollbackPending {
		return invitation, account, nil
	}
	journal.RollbackPending = true
	updated, err := encodePairingJournal(journal)
	if err != nil {
		return syncclient.PairingInvitation{}, syncclient.Account{}, err
	}
	defer zeroBytes(updated)
	if err := replaceSecretExact(ctx, options.Secrets, journalProfile, encoded, updated); err != nil {
		return syncclient.PairingInvitation{}, syncclient.Account{}, fmt.Errorf("persist paired-device rollback phase: %w", err)
	}
	return invitation, account, nil
}

func exactUnclaimedTerminalJoinError(err error) bool {
	var api *syncclient.APIError
	if !errors.As(err, &api) {
		return false
	}
	switch api.Code {
	case "pairing_not_found":
		return api.Status == 404
	case "invalid_or_expired_pairing":
		return api.Status == 401
	case "pairing_expired":
		return api.Status == 401 || api.Status == 410
	default:
		return false
	}
}

func deviceRollbackNotSafeError(err error) bool {
	var api *syncclient.APIError
	return errors.As(err, &api) && api.Status == 409 && api.Code == "device_rollback_not_safe"
}

func finishJoiningRollback(ctx context.Context, relay PairingRelay, options PairingOptions, journalProfile string, bundle *CredentialBundleV1) error {
	// The protected joining journal is the only rollback identity. The optional
	// in-memory bundle is deliberately ignored: every restart must be able to
	// reach the same decision using only durable protected state.
	_ = bundle
	preflight, err := inspectJoiningRollbackLocalState(context.WithoutCancel(ctx), options, journalProfile)
	if err != nil {
		return err
	}
	preflight.zero()
	invitation, account, err := markJoiningRollbackPending(context.WithoutCancel(ctx), options, journalProfile)
	if err != nil {
		return err
	}
	state, err := inspectJoiningRollbackLocalState(context.WithoutCancel(ctx), options, journalProfile)
	if err != nil {
		return err
	}
	defer state.zero()
	if !state.journal.RollbackPending || !bytes.Equal(state.account.AccountID, account.AccountID) ||
		!bytes.Equal(state.account.DeviceID, account.DeviceID) ||
		!bytes.Equal(state.invitation.PairingID, invitation.PairingID) {
		return errors.New("paired-device rollback journal changed identity")
	}
	deleteErr := relay.DeleteCurrentDevice(context.WithoutCancel(ctx), account.AccountID, account.DeviceID,
		invitation.PairingID, account.DeviceRollbackToken)
	if deleteErr != nil {
		// A journal can exist before its first Join request reaches the relay. Only
		// when there is no authenticated claim material, active credential, or DB
		// may an exact Join replay resolve a stable terminal invitation. A generic
		// 401, a transport error, or a successful replay is never treated as proof
		// that remote cleanup happened.
		if !state.hasCredential && !state.activePresent && !state.databaseExists && deviceRollbackNotSafeError(deleteErr) {
			_, joinErr := relay.JoinPairing(context.WithoutCancel(ctx), state.invitation, state.account, state.registration)
			switch {
			case exactUnclaimedTerminalJoinError(joinErr):
				if err := deleteSecretExact(context.WithoutCancel(ctx), options.Secrets, journalProfile, state.journalEncoded); err != nil {
					return fmt.Errorf("remove terminal unclaimed joining journal: %w", err)
				}
				return nil
			case joinErr == nil:
				return errors.Join(deleteErr, errors.New("exact Join replay created or confirmed a rollback tuple; retry pairing-abort"))
			default:
				return errors.Join(deleteErr, fmt.Errorf("confirm terminal unclaimed pairing state: %w", joinErr))
			}
		}
		return fmt.Errorf("rollback joining relay device: %w", deleteErr)
	}
	if state.hasCredential {
		if err := cleanupUncommittedDatabase(context.WithoutCancel(ctx), options.DatabasePath, state.bundle); err != nil {
			return err
		}
		if err := deleteSecretExact(context.WithoutCancel(ctx), options.Secrets, options.Profile, state.credential); err != nil {
			return fmt.Errorf("remove rolled-back paired active credential: %w", err)
		}
	}
	if err := deleteSecretExact(context.WithoutCancel(ctx), options.Secrets, journalProfile, state.journalEncoded); err != nil {
		return fmt.Errorf("remove rolled-back joining journal: %w", err)
	}
	return nil
}

// AbortJoiningPairing is a crash-resumable joiner-side rollback state machine.
// It derives the exact account/device/pairing tuple and one-purpose rollback
// capability only from the protected joining journal, persists rollback intent
// before contacting the relay, and retains all local material on every
// ambiguous or rejected remote result.
func AbortJoiningPairing(ctx context.Context, relay PairingRelay, options PairingOptions) (PairingResult, error) {
	if relay == nil || options.Secrets == nil {
		return PairingResult{}, errors.New("pairing relay and OS secret store are required")
	}
	journalProfile, err := pairingProfile(options.Profile, joiningPairingSuffix)
	if err != nil {
		return PairingResult{}, err
	}
	state, err := inspectJoiningRollbackLocalState(ctx, options, journalProfile)
	if err != nil {
		return PairingResult{}, err
	}
	result := PairingResult{
		AccountIDHex: hex.EncodeToString(state.invitation.AccountID),
		PairingIDHex: hex.EncodeToString(state.invitation.PairingID),
		DeviceIDHex:  state.journal.DeviceID,
		State:        "aborted",
	}
	state.zero()
	if err := finishJoiningRollback(ctx, relay, options, journalProfile, nil); err != nil {
		return PairingResult{}, err
	}
	return result, nil
}

func rollbackClaimedDevice(ctx context.Context, relay PairingRelay, options PairingOptions, journalProfile string, bundle CredentialBundleV1, account syncclient.Account, pairingID []byte) error {
	_ = account
	_ = pairingID
	return finishJoiningRollback(ctx, relay, options, journalProfile, &bundle)
}

func rollbackUnmaterializedClaim(ctx context.Context, relay PairingRelay, options PairingOptions, journalProfile string, account syncclient.Account, pairingID []byte) error {
	_ = account
	_ = pairingID
	return finishJoiningRollback(ctx, relay, options, journalProfile, nil)
}

func ClaimPairing(ctx context.Context, relay PairingRelay, options PairingOptions) (PairingResult, error) {
	if relay == nil || options.Secrets == nil {
		return PairingResult{}, errors.New("pairing relay and OS secret store are required")
	}
	journalProfile, err := pairingProfile(options.Profile, joiningPairingSuffix)
	if err != nil {
		return PairingResult{}, err
	}
	encoded, err := options.Secrets.Load(ctx, journalProfile)
	if err != nil {
		return PairingResult{}, err
	}
	defer zeroBytes(encoded)
	journal, invitation, account, registration, seed, xPrivate, localDataKey, err := decodeJoiningJournal(encoded)
	defer zeroBytes(seed)
	defer zeroBytes(xPrivate)
	defer zeroBytes(localDataKey)
	if err != nil {
		return PairingResult{}, err
	}
	if journal.RollbackPending {
		if err := finishJoiningRollback(ctx, relay, options, journalProfile, nil); err != nil {
			return PairingResult{}, err
		}
		return PairingResult{}, errors.New("paired-device rollback completed; start a new invitation")
	}
	active, found, err := loadOptionalSecret(ctx, options.Secrets, options.Profile)
	if err != nil {
		return PairingResult{}, err
	}
	if found {
		defer zeroBytes(active)
	}
	var bundle CredentialBundleV1
	var credential []byte
	if journal.PendingCredential != "" {
		credential, bundle, err = decodeJoiningPendingCredential(journal, account, seed, xPrivate, localDataKey)
		if err != nil {
			return PairingResult{}, err
		}
	} else {
		transcript, transcriptErr := syncclient.PairingTranscript(invitation, account, registration)
		if transcriptErr != nil {
			return PairingResult{}, transcriptErr
		}
		private := ed25519.NewKeyFromSeed(seed)
		claim, claimErr := relay.ClaimPairing(ctx, invitation, account, transcript, private)
		zeroBytes(private)
		if claimErr != nil {
			if exactUnclaimedTerminalJoinError(claimErr) {
				rollbackErr := finishJoiningRollback(ctx, relay, options, journalProfile, nil)
				if rollbackErr != nil {
					return PairingResult{}, errors.Join(fmt.Errorf("approved pairing terminated before package delivery: %w", claimErr), rollbackErr)
				}
				return PairingResult{}, fmt.Errorf("approved pairing terminated and its local joining journal was aborted: %w", claimErr)
			}
			return PairingResult{}, fmt.Errorf("claim or resume approved pairing: %w", claimErr)
		}
		payload, openErr := syncclient.OpenPairingClaim(invitation, account, transcript, xPrivate, claim)
		if openErr != nil {
			if found {
				return PairingResult{}, fmt.Errorf("authenticate pairing package for existing active credential: %w", openErr)
			}
			rollbackErr := rollbackUnmaterializedClaim(ctx, relay, options, journalProfile, account, invitation.PairingID)
			return PairingResult{}, errors.Join(fmt.Errorf("authenticate pairing package: %w", openErr), rollbackErr)
		}
		bundle, err = bundleFromPairingPackage(account, seed, xPrivate, localDataKey, payload)
		if err != nil {
			if found {
				return PairingResult{}, err
			}
			rollbackErr := rollbackUnmaterializedClaim(ctx, relay, options, journalProfile, account, invitation.PairingID)
			return PairingResult{}, errors.Join(err, rollbackErr)
		}
		credential, err = EncodeCredentialBundle(bundle)
		if err != nil {
			if found {
				bundle.Zero()
				return PairingResult{}, err
			}
			rollbackErr := rollbackClaimedDevice(ctx, relay, options, journalProfile, bundle, account, invitation.PairingID)
			bundle.Zero()
			return PairingResult{}, errors.Join(err, rollbackErr)
		}
		if err := persistJoiningPendingCredential(ctx, options, journalProfile, credential); err != nil {
			if found {
				zeroBytes(credential)
				bundle.Zero()
				return PairingResult{}, err
			}
			rollbackErr := rollbackClaimedDevice(ctx, relay, options, journalProfile, bundle, account, invitation.PairingID)
			zeroBytes(credential)
			bundle.Zero()
			return PairingResult{}, errors.Join(err, rollbackErr)
		}
	}
	defer bundle.Zero()
	defer zeroBytes(credential)
	if found && !bytes.Equal(active, credential) {
		return PairingResult{}, errors.New("active credential differs from authenticated pairing claim")
	}
	if err := ensureEncryptedStore(ctx, options.DatabasePath, bundle, nil); err != nil {
		if found {
			return PairingResult{}, fmt.Errorf("verify paired encrypted database: %w", err)
		}
		rollbackErr := rollbackClaimedDevice(ctx, relay, options, journalProfile, bundle, account, invitation.PairingID)
		return PairingResult{}, errors.Join(fmt.Errorf("initialize paired encrypted database: %w", err), rollbackErr)
	}
	if !found {
		if err := saveSecretExact(ctx, options.Secrets, options.Profile, credential); err != nil {
			rollbackErr := rollbackClaimedDevice(ctx, relay, options, journalProfile, bundle, account, invitation.PairingID)
			return PairingResult{}, errors.Join(fmt.Errorf("commit paired active credential: %w", err), rollbackErr)
		}
	}
	readyState, err := relay.ReadyPairing(ctx, invitation.PairingID, account.DeviceToken)
	if err != nil {
		var api *syncclient.APIError
		if errors.As(err, &api) && (api.Code == "invalid_device_token" || api.Code == "pairing_not_found" || api.Code == "pairing_ready_window_expired") {
			rollbackErr := finishJoiningRollback(ctx, relay, options, journalProfile, &bundle)
			return PairingResult{}, errors.Join(fmt.Errorf("paired relay state terminated before finalization: %w", err), rollbackErr)
		}
		return PairingResult{}, fmt.Errorf("paired local commit is durable but relay readiness acknowledgement is pending: %w", err)
	}
	if readyState != "finalized" {
		return PairingResult{AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), DeviceIDHex: journal.DeviceID, State: "finalize_pending"}, nil
	}
	if err := options.Secrets.Delete(context.WithoutCancel(ctx), journalProfile); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return PairingResult{}, fmt.Errorf("paired device is ready but joining journal cleanup is pending: %w", err)
	}
	return PairingResult{AccountIDHex: hex.EncodeToString(invitation.AccountID), PairingIDHex: hex.EncodeToString(invitation.PairingID), DeviceIDHex: journal.DeviceID, State: "ready"}, nil
}
