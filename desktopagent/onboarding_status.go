// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"time"

	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

// OnboardingLocalStatus contains no credential, invitation, account/device ID,
// or recovery material. Reading it neither contacts the relay nor opens a DB.
// SessionValid describes account management only; it never gates device sync.
type OnboardingLocalStatus struct {
	SessionValid      bool
	Username          string
	SessionExpires    time.Time
	CredentialPresent bool
	Paired            bool
	PairingPending    bool
	PairingRole       string
	PairingState      string
}

func ReadOnboardingLocalStatus(ctx context.Context, secrets SecretStore, profile, endpoint string) (OnboardingLocalStatus, error) {
	state := OnboardingLocalStatus{PairingState: "none"}
	if secrets == nil {
		return state, errors.New("onboarding secret store is unavailable")
	}
	if err := validateProfile(profile); err != nil {
		return state, err
	}
	active, present, err := loadOptionalSecret(ctx, secrets, profile)
	if err != nil {
		return state, err
	}
	defer zeroBytes(active)
	if present {
		bundle, err := DecodeCredentialBundle(active)
		if err != nil {
			return state, err
		}
		state.CredentialPresent = true
		state.Paired = !rosterIsEmpty(bundle.TrustedRoster) && len(bundle.TrustedRoster.Devices) >= 2 && rosterContainsDevice(bundle.TrustedRoster, bundle.DeviceID[:])
		bundle.Zero()
		if state.Paired {
			state.PairingState = "ready"
		}
	}
	for _, suffix := range []string{creatorPairingSuffix, joiningPairingSuffix, provisioningProfileSuffix} {
		journalProfile, err := pairingProfile(profile, suffix)
		if err != nil {
			return state, err
		}
		encoded, found, err := loadOptionalSecret(ctx, secrets, journalProfile)
		if err != nil {
			return state, err
		}
		if !found {
			continue
		}
		if state.PairingPending {
			zeroBytes(encoded)
			return state, errors.New("multiple protected setup journals require review")
		}
		role, phase, err := onboardingJournalState(suffix, encoded)
		zeroBytes(encoded)
		if err != nil {
			return state, err
		}
		state.PairingPending, state.PairingRole, state.PairingState = true, role, phase
		if role == "joiner" || role == "provisioning" {
			// The joining credential may already contain its future signed
			// roster before the creator has durably finalized that addition.
			state.Paired = false
		}
	}
	if endpoint != "" {
		session, err := LoadUserSession(ctx, secrets, profile, endpoint)
		if err == nil {
			state.SessionValid, state.Username, state.SessionExpires = true, session.Username, session.ExpiresAt
			session.Token = ""
		} else if !errors.Is(err, ErrUserLoginRequired) {
			return state, err
		}
	}
	return state, nil
}

// ReadCreatorOnboardingPairingState performs only the existing authenticated
// creator status request. It is used on an explicit Continue/Approve action,
// never on a passive page render, and returns no invitation or identifiers.
func ReadCreatorOnboardingPairingState(ctx context.Context, relay PairingRelay, options PairingOptions) (string, bool, error) {
	if relay == nil || options.Secrets == nil {
		return "", false, errors.New("creator status dependencies are unavailable")
	}
	profile, err := pairingProfile(options.Profile, creatorPairingSuffix)
	if err != nil {
		return "", false, err
	}
	encoded, err := options.Secrets.Load(ctx, profile)
	if err != nil {
		return "", false, err
	}
	defer zeroBytes(encoded)
	var journal creatorPairingJournal
	if err := decodePairingJournal(encoded, &journal); err != nil || journal.Version != pairingJournalVersion {
		return "", false, errors.New("creator pairing journal is invalid")
	}
	invitation, err := DecodePairingInvitation(journal.Invitation)
	if err != nil {
		return "", false, err
	}
	active, bundle, err := loadActiveCredential(ctx, options)
	if err != nil {
		return "", false, err
	}
	defer zeroBytes(active)
	defer bundle.Zero()
	account, _, err := accountAndRegistration(bundle)
	if err != nil {
		return "", false, err
	}
	status, err := relay.GetPairing(ctx, invitation, account)
	return status.State, status.Expired, err
}

func onboardingJournalState(suffix string, encoded []byte) (string, string, error) {
	switch suffix {
	case creatorPairingSuffix:
		var journal creatorPairingJournal
		if err := decodePairingJournal(encoded, &journal); err != nil || journal.Version != pairingJournalVersion {
			return "", "", errors.New("creator pairing journal is invalid")
		}
		if _, err := DecodePairingInvitation(journal.Invitation); err != nil {
			return "", "", err
		}
		if (journal.ApprovedBox == "") != (journal.UpdatedCredential == "") {
			return "", "", errors.New("creator approval journal is incomplete")
		}
		if journal.CancellationPending {
			return "creator", "cancel_pending", nil
		}
		if journal.FinalizationPending {
			return "creator", "finalize_pending", nil
		}
		if journal.UpdatedCredential != "" {
			return "creator", "awaiting_claim", nil
		}
		return "creator", "invited", nil
	case joiningPairingSuffix:
		journal, _, _, _, seed, xPrivate, localKey, err := decodeJoiningJournal(encoded)
		zeroBytes(seed)
		zeroBytes(xPrivate)
		zeroBytes(localKey)
		if err != nil {
			return "", "", err
		}
		if journal.RollbackPending {
			return "joiner", "rollback_pending", nil
		}
		if journal.PendingCredential != "" {
			return "joiner", "finalize_pending", nil
		}
		return "joiner", "joined", nil
	case provisioningProfileSuffix:
		journal, err := decodeProvisioningJournal(encoded)
		journal.Zero()
		if err != nil {
			return "", "", err
		}
		return "provisioning", "provisioning", nil
	default:
		return "", "", errors.New("unknown protected setup journal")
	}
}

// RimeBridgeConfigured is a read-only configuration check, not a claim that the
// native input engine or its maintenance IPC has been exercised.
func RimeBridgeConfigured(paths Paths) (bool, error) {
	bridge, err := DefaultRimeBridgePaths(paths)
	if err != nil {
		return false, err
	}
	contents, err := readBoundedRegular(bridge.InstallationPath, maxRimeInstallationBytes)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	configuration, err := parseRimeInstallation(contents)
	if err != nil {
		return false, err
	}
	return configuration.SyncDir == bridge.SyncDirectory, nil
}

// OnboardingServerRelay deliberately exposes only read operations. Address
// repair must be explicitly confirmed before supplying it an existing bearer.
type OnboardingServerRelay interface {
	GetRosterUpdates(context.Context, string, uint64, []byte) (syncclient.RosterUpdates, error)
	ListDevices(context.Context, string) ([]syncclient.Device, error)
}

type onboardingReadOnlyRelay struct{ OnboardingServerRelay }

func (onboardingReadOnlyRelay) PublishRosterAnchor(context.Context, string, protocol.PairingRoster) error {
	return errors.New("server address verification cannot publish trust")
}

// VerifyOnboardingServer uses the existing roster validator on an isolated
// in-memory credential copy. It never writes the real protected store, trust
// checkpoint, database, or relay. It also verifies that the target recognizes
// this exact current device and its already trusted public keys. This is not an
// anonymous service identity mechanism; the user must first confirm the target
// is an address correction for the same service, not a different provider.
func VerifyOnboardingServer(ctx context.Context, relay OnboardingServerRelay, secrets SecretStore, profile string) error {
	if relay == nil || secrets == nil {
		return errors.New("server verification dependencies are unavailable")
	}
	encoded, bundle, err := loadActiveCredential(ctx, PairingOptions{Secrets: secrets, Profile: profile})
	if err != nil {
		return err
	}
	defer zeroBytes(encoded)
	defer bundle.Zero()
	if rosterIsEmpty(bundle.TrustedRoster) {
		return errors.New("server address repair requires existing signed device trust")
	}
	devices, err := relay.ListDevices(ctx, string(bundle.DeviceToken))
	if err != nil {
		return err
	}
	matched := false
	for _, device := range devices {
		if !device.Current {
			continue
		}
		ed, x := bundle.VerificationKeys[bundle.DeviceID], bundle.X25519PublicKeys[bundle.DeviceID]
		if matched || device.Revoked || !bytes.Equal(device.ID, bundle.DeviceID[:]) || !bytes.Equal(device.Ed25519PublicKey, ed[:]) || !bytes.Equal(device.X25519PublicKey, x[:]) {
			return errors.New("server does not recognize the existing current device")
		}
		matched = true
	}
	if !matched {
		return errors.New("server did not return the existing current device")
	}
	memory := &onboardingVerificationStore{profile: profile, value: append([]byte(nil), encoded...)}
	defer func() { zeroBytes(memory.value) }()
	_, err = RefreshTrustedRoster(ctx, onboardingReadOnlyRelay{relay}, memory, profile, &bundle)
	return err
}

type onboardingVerificationStore struct {
	profile string
	value   []byte
}

func (store *onboardingVerificationStore) Load(_ context.Context, profile string) ([]byte, error) {
	if profile != store.profile {
		return nil, ErrSecretNotFound
	}
	return append([]byte(nil), store.value...), nil
}
func (store *onboardingVerificationStore) Save(_ context.Context, profile string, value []byte) error {
	if profile != store.profile {
		return errors.New("unexpected server verification secret write")
	}
	zeroBytes(store.value)
	store.value = append([]byte(nil), value...)
	return nil
}
func (*onboardingVerificationStore) Delete(context.Context, string) error {
	return errors.New("server verification cannot delete credentials")
}
