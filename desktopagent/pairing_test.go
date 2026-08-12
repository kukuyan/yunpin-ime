// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

type fakePairingRelay struct {
	creator           syncclient.Account
	invitation        syncclient.PairingInvitation
	joining           syncclient.Account
	registration      syncclient.DeviceRegistration
	box               protocol.SealedBox
	state             string
	claimCalls        int
	deleteCalls       int
	readyCalls        int
	finalizeCalls     int
	cancelCalls       int
	loseDeleteReply   bool
	loseReadyReply    bool
	readyError        error
	readyFailures     int
	loseFinalizeReply bool
	loseCancelReply   bool
	cancelBeforeReady bool
	terminalGetError  error
	terminalJoinError error
	claimError        error
	loseClaimReply    bool
	deleteError       error
	deleteFailures    int
	rollbackTombstone bool
}

func cloneInvitation(value syncclient.PairingInvitation) syncclient.PairingInvitation {
	value.PairingID = append([]byte(nil), value.PairingID...)
	value.PairingSecret = append([]byte(nil), value.PairingSecret...)
	value.AccountID = append([]byte(nil), value.AccountID...)
	value.CreatorDeviceID = append([]byte(nil), value.CreatorDeviceID...)
	value.CreatorEd25519PublicKey = append(ed25519.PublicKey(nil), value.CreatorEd25519PublicKey...)
	value.CreatorX25519PublicKey = append([]byte(nil), value.CreatorX25519PublicKey...)
	return value
}

func (relay *fakePairingRelay) CreatePairing(_ context.Context, creator syncclient.Account,
	invitation syncclient.PairingInvitation) (syncclient.PairingInvitation, error) {
	if relay.state == "" {
		relay.creator = creator
		relay.invitation = cloneInvitation(invitation)
		relay.invitation.ExpiresAt = time.Now().UTC().Add(time.Hour)
		relay.state = "created"
	} else if !bytes.Equal(relay.invitation.PairingID, invitation.PairingID) ||
		!bytes.Equal(relay.creator.AccountID, creator.AccountID) || relay.creator.DeviceToken != creator.DeviceToken {
		return syncclient.PairingInvitation{}, errors.New("pairing invitation changed on retry")
	}
	return cloneInvitation(relay.invitation), nil
}

func (relay *fakePairingRelay) GetPairing(_ context.Context, invitation syncclient.PairingInvitation,
	creator syncclient.Account) (syncclient.PairingStatus, error) {
	if relay.terminalGetError != nil {
		return syncclient.PairingStatus{}, relay.terminalGetError
	}
	if !bytes.Equal(invitation.PairingID, relay.invitation.PairingID) || creator.DeviceToken != relay.creator.DeviceToken {
		return syncclient.PairingStatus{}, errors.New("unexpected creator pairing lookup")
	}
	status := syncclient.PairingStatus{PairingID: append([]byte(nil), invitation.PairingID...), State: relay.state, ExpiresAt: relay.invitation.ExpiresAt}
	if relay.state != "created" {
		transcript, err := syncclient.PairingTranscript(relay.invitation, relay.joining, relay.registration)
		if err != nil {
			return syncclient.PairingStatus{}, err
		}
		proof, err := protocol.PairingJoinProof(relay.invitation.PairingSecret, transcript)
		if err != nil {
			return syncclient.PairingStatus{}, err
		}
		status.DeviceID = append([]byte(nil), relay.joining.DeviceID...)
		status.JoinProof = proof
		status.DeviceNameCiphertext = append([]byte(nil), relay.registration.DeviceNameCiphertext...)
		status.Ed25519PublicKey = append(ed25519.PublicKey(nil), relay.registration.Ed25519PublicKey...)
		status.X25519PublicKey = append([]byte(nil), relay.registration.X25519PublicKey...)
	}
	return status, nil
}

func (relay *fakePairingRelay) JoinPairing(_ context.Context, invitation syncclient.PairingInvitation,
	joining syncclient.Account, registration syncclient.DeviceRegistration) (protocol.PairingTranscript, error) {
	if relay.terminalJoinError != nil {
		return protocol.PairingTranscript{}, relay.terminalJoinError
	}
	transcript, err := syncclient.PairingTranscript(invitation, joining, registration)
	if err != nil {
		return protocol.PairingTranscript{}, err
	}
	if relay.state == "created" {
		relay.joining = joining
		relay.registration = registration
		relay.state = "joined"
	} else if !bytes.Equal(relay.joining.DeviceID, joining.DeviceID) || relay.joining.DeviceToken != joining.DeviceToken {
		return protocol.PairingTranscript{}, errors.New("joining identity changed on retry")
	}
	return transcript, nil
}

func (relay *fakePairingRelay) ApprovePairing(_ context.Context, pairingID []byte, token string, box protocol.SealedBox) error {
	if !bytes.Equal(pairingID, relay.invitation.PairingID) || token != relay.creator.DeviceToken ||
		(relay.state != "joined" && relay.state != "approved" && relay.state != "claimed") {
		return errors.New("unexpected pairing approval")
	}
	if relay.state == "joined" {
		relay.box = protocol.SealedBox{Nonce: append([]byte(nil), box.Nonce...), Ciphertext: append([]byte(nil), box.Ciphertext...)}
		relay.state = "approved"
	} else if !reflect.DeepEqual(relay.box, box) {
		return errors.New("pairing approval box changed on retry")
	}
	return nil
}

func (relay *fakePairingRelay) ReadyPairing(_ context.Context, pairingID []byte, token string) (string, error) {
	relay.readyCalls++
	if !bytes.Equal(pairingID, relay.invitation.PairingID) || token != relay.joining.DeviceToken {
		return "", errors.New("unexpected pairing readiness")
	}
	if relay.readyFailures > 0 {
		relay.readyFailures--
		return "", relay.readyError
	}
	if relay.state == "claimed" && relay.cancelBeforeReady {
		relay.cancelBeforeReady = false
		relay.terminalGetError = &syncclient.APIError{Status: 404, Code: "pairing_not_found"}
		relay.rollbackTombstone = true
		return "", &syncclient.APIError{Status: 401, Code: "invalid_device_token"}
	}
	if relay.state == "claimed" {
		relay.state = "ready"
	}
	if relay.loseReadyReply {
		relay.loseReadyReply = false
		return "", errors.New("synthetic readiness response loss")
	}
	if relay.state != "ready" && relay.state != "finalized" {
		return "", errors.New("pairing is not ready")
	}
	return relay.state, nil
}

func (relay *fakePairingRelay) FinalizePairing(_ context.Context, pairingID []byte, token string) error {
	relay.finalizeCalls++
	if !bytes.Equal(pairingID, relay.invitation.PairingID) || token != relay.creator.DeviceToken ||
		(relay.state != "ready" && relay.state != "finalized") {
		return errors.New("unexpected pairing finalization")
	}
	relay.state = "finalized"
	if relay.loseFinalizeReply {
		relay.loseFinalizeReply = false
		return errors.New("synthetic finalization response loss")
	}
	return nil
}

func (relay *fakePairingRelay) CancelPairing(_ context.Context, pairingID []byte, token string) error {
	if !bytes.Equal(pairingID, relay.invitation.PairingID) || token != relay.creator.DeviceToken {
		return errors.New("unexpected pairing cancellation")
	}
	relay.cancelCalls++
	relay.terminalGetError = &syncclient.APIError{Status: 404, Code: "pairing_not_found"}
	if relay.state == "joined" || relay.state == "approved" || relay.state == "claimed" {
		relay.rollbackTombstone = true
	}
	if relay.loseCancelReply {
		relay.loseCancelReply = false
		return errors.New("synthetic cancellation response loss")
	}
	return nil
}

func (relay *fakePairingRelay) ClaimPairing(_ context.Context, invitation syncclient.PairingInvitation,
	joining syncclient.Account, transcript protocol.PairingTranscript, private ed25519.PrivateKey) (syncclient.PairingClaim, error) {
	relay.claimCalls++
	if relay.claimError != nil {
		return syncclient.PairingClaim{}, relay.claimError
	}
	if relay.state != "approved" && relay.state != "claimed" {
		return syncclient.PairingClaim{}, errors.New("pairing is not claimable")
	}
	if !bytes.Equal(invitation.PairingID, relay.invitation.PairingID) || !bytes.Equal(joining.DeviceID, relay.joining.DeviceID) ||
		joining.DeviceToken != relay.joining.DeviceToken || !bytes.Equal(private.Public().(ed25519.PublicKey), relay.registration.Ed25519PublicKey) ||
		!bytes.Equal(transcript.JoiningDeviceID, relay.joining.DeviceID) {
		return syncclient.PairingClaim{}, errors.New("unexpected pairing claim")
	}
	relay.state = "claimed"
	if relay.loseClaimReply {
		relay.loseClaimReply = false
		return syncclient.PairingClaim{}, errors.New("synthetic claim response loss")
	}
	return syncclient.PairingClaim{Account: relay.joining, EncryptedKeyring: relay.box}, nil
}

func (relay *fakePairingRelay) DeleteCurrentDevice(_ context.Context, accountID, deviceID, pairingID []byte, rollbackToken string) error {
	relay.deleteCalls++
	if !bytes.Equal(accountID, relay.invitation.AccountID) || !bytes.Equal(pairingID, relay.invitation.PairingID) {
		return errors.New("unexpected paired-device rollback")
	}
	if relay.rollbackTombstone {
		if relay.loseDeleteReply {
			relay.loseDeleteReply = false
			return errors.New("synthetic rollback response loss")
		}
		return nil
	}
	if relay.deleteFailures > 0 {
		relay.deleteFailures--
		return relay.deleteError
	}
	if relay.state == "ready" || relay.state == "finalized" {
		return &syncclient.APIError{Status: 409, Code: "device_rollback_not_safe"}
	}
	if relay.state != "joined" && relay.state != "approved" && relay.state != "claimed" {
		return &syncclient.APIError{Status: 409, Code: "device_rollback_not_safe"}
	}
	if !bytes.Equal(deviceID, relay.joining.DeviceID) || rollbackToken != relay.joining.DeviceRollbackToken {
		return &syncclient.APIError{Status: 409, Code: "device_rollback_not_safe"}
	}
	relay.rollbackTombstone = true
	relay.terminalGetError = &syncclient.APIError{Status: 404, Code: "pairing_not_found"}
	if relay.loseDeleteReply {
		relay.loseDeleteReply = false
		return errors.New("synthetic rollback response loss")
	}
	return nil
}

func pairingRandom(seed byte) io.Reader {
	value := make([]byte, 16384)
	for index := range value {
		value[index] = byte((int(seed)+index%233)%255 + 1)
	}
	return bytes.NewReader(value)
}

func saveTestCreator(t *testing.T, secrets SecretStore, profile string) CredentialBundleV1 {
	t.Helper()
	bundle := testCredentials()
	bundle.DeviceToken = []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, 32)))
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(encoded)
	if err := secrets.Save(context.Background(), profile, encoded); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func loadTestBundle(t *testing.T, secrets SecretStore, profile string) CredentialBundleV1 {
	t.Helper()
	encoded, err := secrets.Load(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(encoded)
	bundle, err := DecodeCredentialBundle(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func establishApprovedPairing(t *testing.T, relay *fakePairingRelay, creatorSecrets, joiningSecrets SecretStore,
	creatorDB, joiningDB string) (PairingOptions, PairingOptions) {
	t.Helper()
	creatorOptions := PairingOptions{Secrets: creatorSecrets, Profile: "creator", DatabasePath: creatorDB, Random: pairingRandom(3)}
	joiningOptions := PairingOptions{Secrets: joiningSecrets, Profile: "joining", DatabasePath: joiningDB, Random: pairingRandom(91)}
	invited, err := StartPairing(context.Background(), relay, creatorOptions)
	if err != nil || invited.State != "invited" || invited.Invitation == "" {
		t.Fatalf("start pairing result=%#v err=%v", invited, err)
	}
	if joined, err := JoinPairing(context.Background(), relay, joiningOptions, invited.Invitation); err != nil || joined.State != "joined" {
		t.Fatalf("join pairing result=%#v err=%v", joined, err)
	}
	approved, err := ApprovePairing(context.Background(), relay, creatorOptions)
	if err != nil || approved.State != "awaiting_claim" {
		t.Fatalf("approve pairing result=%#v err=%v", approved, err)
	}
	return creatorOptions, joiningOptions
}

func TestPairingTwoDeviceFlowDefersCreatorTrustUntilClaimedFinalize(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creatorBefore := saveTestCreator(t, creatorSecrets, "creator")
	relay := &fakePairingRelay{}
	creatorOptions, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))

	creatorAfterApprove := loadTestBundle(t, creatorSecrets, "creator")
	if !rosterIsEmpty(creatorAfterApprove.TrustedRoster) || len(creatorAfterApprove.VerificationKeys) != 1 {
		t.Fatal("creator trusted an unclaimed joining device")
	}
	creatorAfterApprove.Zero()
	if pending, err := FinalizePairing(context.Background(), relay, creatorOptions); err != nil || pending.State != "claim_pending" {
		t.Fatalf("premature finalize result=%#v err=%v", pending, err)
	}

	ready, err := ClaimPairing(context.Background(), relay, joiningOptions)
	if err != nil || ready.State != "finalize_pending" {
		t.Fatalf("claim pairing result=%#v err=%v", ready, err)
	}
	joiningBundle := loadTestBundle(t, joiningSecrets, "joining")
	defer joiningBundle.Zero()
	if len(joiningBundle.TrustedRoster.Devices) != 2 {
		t.Fatal("joining device did not persist the exact signed two-device roster")
	}
	final, err := FinalizePairing(context.Background(), relay, creatorOptions)
	if err != nil || final.State != "ready" {
		t.Fatalf("finalize pairing result=%#v err=%v", final, err)
	}
	joinFinal, err := ClaimPairing(context.Background(), relay, joiningOptions)
	if err != nil || joinFinal.State != "ready" || relay.claimCalls != 1 {
		t.Fatalf("joining finalize replay repeated claim or failed: result=%#v claim=%d err=%v", joinFinal, relay.claimCalls, err)
	}
	creatorReady := loadTestBundle(t, creatorSecrets, "creator")
	defer creatorReady.Zero()
	if len(creatorReady.TrustedRoster.Devices) != 2 || !reflect.DeepEqual(creatorReady.TrustedRoster, joiningBundle.TrustedRoster) {
		t.Fatal("creator and joining device did not commit the same signed roster")
	}
	if _, err := creatorSecrets.Load(context.Background(), "creator.pairing-creator"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("creator journal remains after finalize: %v", err)
	}
	creatorBefore.Zero()
	if _, err := StartPairing(context.Background(), relay, creatorOptions); err == nil {
		t.Fatal("a signed two-device credential accepted a third-device invitation")
	}
}

func TestPairingTerminalRollbackRestoresCreatorSelfTrust(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{}
	creatorOptions, _ := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))

	// Simulate a crash after the creator CAS but before its journal deletion.
	journalEncoded, err := creatorSecrets.Load(context.Background(), "creator.pairing-creator")
	if err != nil {
		t.Fatal(err)
	}
	var journal creatorPairingJournal
	if err := decodePairingJournal(journalEncoded, &journal); err != nil {
		t.Fatal(err)
	}
	zeroBytes(journalEncoded)
	updated, err := decodeCanonicalBase64Bounded(journal.UpdatedCredential, maxCredentialBlobBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := creatorSecrets.Save(context.Background(), "creator", updated); err != nil {
		t.Fatal(err)
	}
	zeroBytes(updated)
	relay.terminalGetError = &syncclient.APIError{Status: 404, Code: "pairing_not_found"}
	result, err := FinalizePairing(context.Background(), relay, creatorOptions)
	if err != nil || result.State != "rolled_back" {
		t.Fatalf("terminal pairing cleanup result=%#v err=%v", result, err)
	}
	bundle := loadTestBundle(t, creatorSecrets, "creator")
	defer bundle.Zero()
	if !rosterIsEmpty(bundle.TrustedRoster) || len(bundle.VerificationKeys) != 1 {
		t.Fatal("terminal pairing left ghost trust on the creator")
	}
}

func TestPairingReadyAndFinalizeResponseLossResumeWithoutRepeatingClaim(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{loseReadyReply: true, loseFinalizeReply: true}
	creatorOptions, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	if _, err := ClaimPairing(context.Background(), relay, joiningOptions); err == nil {
		t.Fatal("lost readiness response unexpectedly reported pairing success")
	}
	if relay.claimCalls != 1 || relay.readyCalls != 1 || relay.state != "ready" {
		t.Fatalf("unexpected first ready attempt: claim=%d ready=%d state=%s", relay.claimCalls, relay.readyCalls, relay.state)
	}
	joiningJournal, err := joiningSecrets.Load(context.Background(), "joining.pairing-join")
	if err != nil {
		t.Fatalf("readiness response loss removed joining journal: %v", err)
	}
	var pending joiningPairingJournal
	if err := decodePairingJournal(joiningJournal, &pending); err != nil {
		t.Fatal(err)
	}
	zeroBytes(joiningJournal)
	if pending.PendingCredential == "" || pending.RollbackPending {
		t.Fatal("readiness response loss did not retain authenticated non-rollback material")
	}
	ready, err := ClaimPairing(context.Background(), relay, joiningOptions)
	if err != nil || ready.State != "finalize_pending" || relay.claimCalls != 1 || relay.readyCalls != 2 {
		t.Fatalf("readiness retry repeated claim or failed: result=%#v claim=%d ready=%d err=%v", ready, relay.claimCalls, relay.readyCalls, err)
	}
	if _, err := joiningSecrets.Load(context.Background(), "joining.pairing-join"); err != nil {
		t.Fatalf("ready but unfinalized joining device deleted its protected journal: %v", err)
	}
	if _, err := FinalizePairing(context.Background(), relay, creatorOptions); err == nil {
		t.Fatal("lost finalization response unexpectedly committed local creator trust")
	}
	if _, err := creatorSecrets.Load(context.Background(), "creator.pairing-creator"); err != nil {
		t.Fatalf("finalization response loss removed creator journal: %v", err)
	}
	beforeRetry := loadTestBundle(t, creatorSecrets, "creator")
	if !rosterIsEmpty(beforeRetry.TrustedRoster) {
		t.Fatal("creator committed trust before finalization response was reconciled")
	}
	beforeRetry.Zero()
	final, err := FinalizePairing(context.Background(), relay, creatorOptions)
	if err != nil || final.State != "ready" || relay.finalizeCalls != 1 {
		t.Fatalf("finalization response-loss retry failed: result=%#v finalize=%d err=%v", final, relay.finalizeCalls, err)
	}
	afterRetry := loadTestBundle(t, creatorSecrets, "creator")
	defer afterRetry.Zero()
	if len(afterRetry.TrustedRoster.Devices) != 2 {
		t.Fatal("finalized creator did not commit exact two-device trust")
	}
	if _, err := creatorSecrets.Load(context.Background(), "creator.pairing-creator"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("finalized creator journal remains after resume: %v", err)
	}
	joinFinal, err := ClaimPairing(context.Background(), relay, joiningOptions)
	if err != nil || joinFinal.State != "ready" || relay.claimCalls != 1 {
		t.Fatalf("joiner did not reconcile finalized state without re-claim: result=%#v claim=%d err=%v", joinFinal, relay.claimCalls, err)
	}
	if _, err := joiningSecrets.Load(context.Background(), "joining.pairing-join"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("finalized joiner journal remains after readiness replay: %v", err)
	}
}

type failProfileSecretStore struct {
	*memorySecretStore
	profile  string
	failures int
}

type loseDeleteResponseSecretStore struct {
	*memorySecretStore
	profile  string
	failures int
}

type failDeleteSecretStore struct {
	*memorySecretStore
	profile  string
	failures int
}

func (store *failDeleteSecretStore) Delete(ctx context.Context, profile string) error {
	if profile == store.profile && store.failures > 0 {
		store.failures--
		return errors.New("synthetic protected-delete failure")
	}
	return store.memorySecretStore.Delete(ctx, profile)
}

func (store *loseDeleteResponseSecretStore) Delete(ctx context.Context, profile string) error {
	if profile == store.profile && store.failures > 0 {
		store.failures--
		if err := store.memorySecretStore.Delete(ctx, profile); err != nil {
			return err
		}
		return errors.New("synthetic protected-delete response loss")
	}
	return store.memorySecretStore.Delete(ctx, profile)
}

func (store *failProfileSecretStore) Save(ctx context.Context, profile string, value []byte) error {
	if profile == store.profile && store.failures > 0 {
		store.failures--
		return errors.New("synthetic active credential save failure")
	}
	return store.memorySecretStore.Save(ctx, profile, value)
}

func TestPairingRollbackResponseLossResumesBeforeAnotherClaim(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningMemory := newMemorySecretStore()
	joiningSecrets := &failProfileSecretStore{memorySecretStore: joiningMemory, profile: "joining", failures: 1}
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{loseDeleteReply: true}
	_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	if _, err := ClaimPairing(context.Background(), relay, joiningOptions); err == nil {
		t.Fatal("lost rollback response unexpectedly reported pairing success")
	}
	if relay.claimCalls != 1 || relay.deleteCalls != 1 {
		t.Fatalf("unexpected first attempt calls: claim=%d delete=%d", relay.claimCalls, relay.deleteCalls)
	}
	journalEncoded, err := joiningSecrets.Load(context.Background(), "joining.pairing-join")
	if err != nil {
		t.Fatal(err)
	}
	var journal joiningPairingJournal
	if err := decodePairingJournal(journalEncoded, &journal); err != nil {
		t.Fatal(err)
	}
	zeroBytes(journalEncoded)
	if !journal.RollbackPending || journal.PendingCredential == "" {
		t.Fatal("joining rollback phase or authenticated local material was not durably journaled")
	}
	if _, err := os.Lstat(joiningOptions.DatabasePath); err != nil {
		t.Fatalf("test did not reach post-database rollback window: %v", err)
	}
	if _, err := ClaimPairing(context.Background(), relay, joiningOptions); err == nil {
		t.Fatal("completed rollback unexpectedly reported pairing success")
	}
	if relay.claimCalls != 1 || relay.deleteCalls != 2 {
		t.Fatalf("restart claimed again instead of finishing tombstone rollback: claim=%d delete=%d", relay.claimCalls, relay.deleteCalls)
	}
	if _, err := joiningSecrets.Load(context.Background(), "joining.pairing-join"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("rollback journal remains after tombstone replay: %v", err)
	}
	if _, err := os.Lstat(joiningOptions.DatabasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("verified uncommitted database remains after rollback: %v", err)
	}
}

func TestPairingCreatorCancelAfterClaimEntersRollbackBeforeAnyReclaim(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{cancelBeforeReady: true, loseDeleteReply: true}
	_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	if _, err := ClaimPairing(context.Background(), relay, joiningOptions); err == nil {
		t.Fatal("creator cancellation unexpectedly reported pairing success")
	}
	if relay.claimCalls != 1 || relay.deleteCalls != 1 {
		t.Fatalf("terminal readiness did not enter rollback: claim=%d delete=%d", relay.claimCalls, relay.deleteCalls)
	}
	journalEncoded, err := joiningSecrets.Load(context.Background(), "joining.pairing-join")
	if err != nil {
		t.Fatal(err)
	}
	var journal joiningPairingJournal
	if err := decodePairingJournal(journalEncoded, &journal); err != nil {
		t.Fatal(err)
	}
	zeroBytes(journalEncoded)
	if !journal.RollbackPending || journal.PendingCredential == "" {
		t.Fatal("creator cancellation was not durably marked rollback_pending")
	}
	if _, err := ClaimPairing(context.Background(), relay, joiningOptions); err == nil {
		t.Fatal("completed cancellation rollback unexpectedly reported pairing success")
	}
	if relay.claimCalls != 1 || relay.deleteCalls != 2 {
		t.Fatalf("restart reclaimed instead of replaying tombstone rollback: claim=%d delete=%d", relay.claimCalls, relay.deleteCalls)
	}
	if _, err := joiningSecrets.Load(context.Background(), "joining"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("rolled-back active credential remains: %v", err)
	}
	if _, err := joiningSecrets.Load(context.Background(), "joining.pairing-join"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("rolled-back joining journal remains: %v", err)
	}
	if _, err := os.Lstat(joiningOptions.DatabasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back paired database remains: %v", err)
	}
}

func TestCancelCreatorPairingBeforeApprovalRemovesOnlyPendingInvitation(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	creatorBefore := saveTestCreator(t, creatorSecrets, "creator")
	defer creatorBefore.Zero()
	relay := &fakePairingRelay{}
	options := PairingOptions{Secrets: creatorSecrets, Profile: "creator", DatabasePath: privateTestPath(t, "creator.db"), Random: pairingRandom(17)}
	invited, err := StartPairing(context.Background(), relay, options)
	if err != nil || invited.State != "invited" {
		t.Fatalf("start result=%#v err=%v", invited, err)
	}
	cancelled, err := CancelCreatorPairing(context.Background(), relay, options)
	if err != nil || cancelled.State != "cancelled" || relay.cancelCalls != 1 {
		t.Fatalf("cancel result=%#v calls=%d err=%v", cancelled, relay.cancelCalls, err)
	}
	if _, err := creatorSecrets.Load(context.Background(), "creator.pairing-creator"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("cancelled creator journal remains: %v", err)
	}
	creatorAfter := loadTestBundle(t, creatorSecrets, "creator")
	defer creatorAfter.Zero()
	if !reflect.DeepEqual(creatorBefore, creatorAfter) {
		t.Fatal("pre-approval cancellation changed the active credential")
	}
}

func TestCancelCreatorPairingReconcilesProtectedJournalDeleteResponseLoss(t *testing.T) {
	memory := newMemorySecretStore()
	creatorSecrets := &loseDeleteResponseSecretStore{
		memorySecretStore: memory, profile: "creator.pairing-creator", failures: 1,
	}
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{}
	options := PairingOptions{Secrets: creatorSecrets, Profile: "creator", DatabasePath: privateTestPath(t, "creator.db"), Random: pairingRandom(19)}
	if _, err := StartPairing(context.Background(), relay, options); err != nil {
		t.Fatal(err)
	}
	result, err := CancelCreatorPairing(context.Background(), relay, options)
	if err != nil || result.State != "cancelled" {
		t.Fatalf("protected-delete response loss was not reconciled: result=%#v err=%v", result, err)
	}
	if _, err := creatorSecrets.Load(context.Background(), "creator.pairing-creator"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("creator journal remains after reconciled protected deletion: %v", err)
	}
}

func TestCancelCreatorPairingAfterApprovalRestoresSelfTrust(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{}
	creatorOptions, _ := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	cancelled, err := CancelCreatorPairing(context.Background(), relay, creatorOptions)
	if err != nil || cancelled.State != "cancelled" || relay.cancelCalls != 1 {
		t.Fatalf("cancel result=%#v calls=%d err=%v", cancelled, relay.cancelCalls, err)
	}
	bundle := loadTestBundle(t, creatorSecrets, "creator")
	defer bundle.Zero()
	if !rosterIsEmpty(bundle.TrustedRoster) || len(bundle.VerificationKeys) != 1 || len(bundle.X25519PublicKeys) != 1 {
		t.Fatal("post-approval cancellation left joining-device trust active")
	}
	if _, err := creatorSecrets.Load(context.Background(), "creator.pairing-creator"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("cancelled creator journal remains: %v", err)
	}
}

func TestCancelCreatorPairingResponseLossResumesBeforeApprovalOrFinalize(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{loseCancelReply: true}
	creatorOptions, _ := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	if _, err := CancelCreatorPairing(context.Background(), relay, creatorOptions); err == nil {
		t.Fatal("lost cancellation response unexpectedly reported success")
	}
	journalEncoded, err := creatorSecrets.Load(context.Background(), "creator.pairing-creator")
	if err != nil {
		t.Fatal(err)
	}
	var journal creatorPairingJournal
	if err := decodePairingJournal(journalEncoded, &journal); err != nil {
		t.Fatal(err)
	}
	zeroBytes(journalEncoded)
	if !journal.CancellationPending || relay.cancelCalls != 1 {
		t.Fatalf("cancellation intent was not durable: pending=%v calls=%d", journal.CancellationPending, relay.cancelCalls)
	}
	if _, err := ApprovePairing(context.Background(), relay, creatorOptions); err == nil || !strings.Contains(err.Error(), "cancellation is pending") {
		t.Fatalf("approval crossed durable cancellation intent: %v", err)
	}
	if _, err := FinalizePairing(context.Background(), relay, creatorOptions); err == nil || !strings.Contains(err.Error(), "cancellation is pending") {
		t.Fatalf("finalize crossed durable cancellation intent: %v", err)
	}

	// Simulate response loss after the creator-side credential CAS. A retry must
	// accept either exact known credential and restore canonical self-only trust.
	updated, err := decodeCanonicalBase64Bounded(journal.UpdatedCredential, maxCredentialBlobBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := creatorSecrets.Save(context.Background(), "creator", updated); err != nil {
		t.Fatal(err)
	}
	zeroBytes(updated)
	result, err := CancelCreatorPairing(context.Background(), relay, creatorOptions)
	if err != nil || result.State != "cancelled" || relay.cancelCalls != 2 {
		t.Fatalf("cancellation retry result=%#v calls=%d err=%v", result, relay.cancelCalls, err)
	}
	bundle := loadTestBundle(t, creatorSecrets, "creator")
	defer bundle.Zero()
	if !rosterIsEmpty(bundle.TrustedRoster) || len(bundle.VerificationKeys) != 1 {
		t.Fatal("cancellation retry did not restore creator self-trust")
	}
	if _, err := creatorSecrets.Load(context.Background(), "creator.pairing-creator"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("cancellation retry left creator journal: %v", err)
	}
}

func loadJoiningJournalForTest(t *testing.T, secrets SecretStore, profile string) joiningPairingJournal {
	t.Helper()
	encoded, err := secrets.Load(context.Background(), profile+joiningPairingSuffix)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(encoded)
	var journal joiningPairingJournal
	if err := decodePairingJournal(encoded, &journal); err != nil {
		t.Fatal(err)
	}
	return journal
}

func TestAbortJoiningPairingCleansTerminalCreatedInvitationWithoutClaimEvidence(t *testing.T) {
	for name, terminal := range map[string]error{
		"cancelled": &syncclient.APIError{Status: 404, Code: "pairing_not_found"},
		"expired":   &syncclient.APIError{Status: 401, Code: "invalid_or_expired_pairing"},
	} {
		t.Run(name, func(t *testing.T) {
			creatorSecrets := newMemorySecretStore()
			joiningSecrets := newMemorySecretStore()
			creator := saveTestCreator(t, creatorSecrets, "creator")
			creator.Zero()
			relay := &fakePairingRelay{}
			creatorOptions := PairingOptions{Secrets: creatorSecrets, Profile: "creator", DatabasePath: privateTestPath(t, "creator.db"), Random: pairingRandom(31)}
			invited, err := StartPairing(context.Background(), relay, creatorOptions)
			if err != nil {
				t.Fatal(err)
			}
			relay.terminalJoinError = terminal
			joiningOptions := PairingOptions{Secrets: joiningSecrets, Profile: "joining", DatabasePath: privateTestPath(t, "joining.db"), Random: pairingRandom(61)}
			if _, err := JoinPairing(context.Background(), relay, joiningOptions, invited.Invitation); err == nil {
				t.Fatal("terminal first Join unexpectedly succeeded")
			}
			result, err := AbortJoiningPairing(context.Background(), relay, joiningOptions)
			if err != nil || result.State != "aborted" || result.Invitation != "" {
				t.Fatalf("abort result=%#v err=%v", result, err)
			}
			encodedResult, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encodedResult, []byte("secret")) || bytes.Contains(encodedResult, []byte("token")) || bytes.Contains(encodedResult, []byte("invitation")) {
				t.Fatalf("abort result exposed secret-bearing fields: %s", encodedResult)
			}
			if _, err := joiningSecrets.Load(context.Background(), "joining"+joiningPairingSuffix); !errors.Is(err, ErrSecretNotFound) {
				t.Fatalf("terminal unclaimed journal remains: %v", err)
			}
		})
	}
}

func TestAbortJoiningPairingDoesNotGeneralizeUnauthorizedJoin(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{}
	creatorOptions := PairingOptions{Secrets: creatorSecrets, Profile: "creator", DatabasePath: privateTestPath(t, "creator.db"), Random: pairingRandom(33)}
	invited, err := StartPairing(context.Background(), relay, creatorOptions)
	if err != nil {
		t.Fatal(err)
	}
	relay.terminalJoinError = &syncclient.APIError{Status: 401, Code: "invalid_pairing_claim_proof"}
	joiningOptions := PairingOptions{Secrets: joiningSecrets, Profile: "joining", DatabasePath: privateTestPath(t, "joining.db"), Random: pairingRandom(63)}
	if _, err := JoinPairing(context.Background(), relay, joiningOptions, invited.Invitation); err == nil {
		t.Fatal("synthetic unauthorized Join unexpectedly succeeded")
	}
	if _, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err == nil {
		t.Fatal("generic 401 was treated as successful terminal cleanup")
	}
	journal := loadJoiningJournalForTest(t, joiningSecrets, "joining")
	if !journal.RollbackPending {
		t.Fatal("ambiguous unauthorized state did not retain durable rollback intent")
	}
}

func TestAbortJoiningPairingAfterCreatorCancelJoinedAndApproved(t *testing.T) {
	for _, approve := range []bool{false, true} {
		name := "joined"
		if approve {
			name = "approved"
		}
		t.Run(name, func(t *testing.T) {
			creatorSecrets := newMemorySecretStore()
			joiningSecrets := newMemorySecretStore()
			creator := saveTestCreator(t, creatorSecrets, "creator")
			creator.Zero()
			relay := &fakePairingRelay{}
			creatorOptions := PairingOptions{Secrets: creatorSecrets, Profile: "creator", DatabasePath: privateTestPath(t, "creator.db"), Random: pairingRandom(35)}
			invited, err := StartPairing(context.Background(), relay, creatorOptions)
			if err != nil {
				t.Fatal(err)
			}
			joiningOptions := PairingOptions{Secrets: joiningSecrets, Profile: "joining", DatabasePath: privateTestPath(t, "joining.db"), Random: pairingRandom(65)}
			if _, err := JoinPairing(context.Background(), relay, joiningOptions, invited.Invitation); err != nil {
				t.Fatal(err)
			}
			if approve {
				if _, err := ApprovePairing(context.Background(), relay, creatorOptions); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := CancelCreatorPairing(context.Background(), relay, creatorOptions); err != nil {
				t.Fatal(err)
			}
			if result, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err != nil || result.State != "aborted" {
				t.Fatalf("abort result=%#v err=%v", result, err)
			}
			if _, err := joiningSecrets.Load(context.Background(), "joining"+joiningPairingSuffix); !errors.Is(err, ErrSecretNotFound) {
				t.Fatalf("cancelled %s journal remains: %v", name, err)
			}

			// Completed abort must reopen the exact joiner profile for a fresh invitation.
			freshRelay := &fakePairingRelay{}
			creatorOptions.Random = pairingRandom(37)
			freshInvitation, err := StartPairing(context.Background(), freshRelay, creatorOptions)
			if err != nil {
				t.Fatalf("fresh creator invitation after abort: %v", err)
			}
			joiningOptions.Random = pairingRandom(67)
			if _, err := JoinPairing(context.Background(), freshRelay, joiningOptions, freshInvitation.Invitation); err != nil {
				t.Fatalf("fresh join after completed abort: %v", err)
			}
		})
	}
}

func TestAbortJoiningPairingClaimedBeforeReady(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{loseClaimReply: true}
	_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	if _, err := ClaimPairing(context.Background(), relay, joiningOptions); err == nil {
		t.Fatal("lost claim response unexpectedly succeeded")
	}
	if relay.state != "claimed" {
		t.Fatalf("claim did not commit remotely before response loss: %s", relay.state)
	}
	if result, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err != nil || result.State != "aborted" {
		t.Fatalf("abort claimed result=%#v err=%v", result, err)
	}
	if relay.deleteCalls != 1 {
		t.Fatalf("claimed rollback calls=%d", relay.deleteCalls)
	}
}

func TestAbortJoiningPairingCleansDurableLocalCommitBeforeReady(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{
		readyError: errors.New("synthetic temporary readiness failure"), readyFailures: 1,
	}
	_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	if _, err := ClaimPairing(context.Background(), relay, joiningOptions); err == nil {
		t.Fatal("temporary readiness failure unexpectedly completed pairing")
	}
	journal := loadJoiningJournalForTest(t, joiningSecrets, "joining")
	if journal.PendingCredential == "" || journal.RollbackPending {
		t.Fatal("local commit did not leave exact authenticated pre-ready material")
	}
	if _, err := joiningSecrets.Load(context.Background(), "joining"); err != nil {
		t.Fatalf("local active credential was not committed: %v", err)
	}
	if _, err := os.Lstat(joiningOptions.DatabasePath); err != nil {
		t.Fatalf("local encrypted database was not committed: %v", err)
	}
	if result, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err != nil || result.State != "aborted" {
		t.Fatalf("abort durable pre-ready commit result=%#v err=%v", result, err)
	}
	if _, err := joiningSecrets.Load(context.Background(), "joining"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("aborted pre-ready active credential remains: %v", err)
	}
	if _, err := joiningSecrets.Load(context.Background(), "joining"+joiningPairingSuffix); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("aborted pre-ready journal remains: %v", err)
	}
	assertRollbackDatabaseAbsent(t, joiningOptions.DatabasePath)
}

func TestClaimPairingTerminalErrorEntersAbortAndOrdinary401DoesNot(t *testing.T) {
	t.Run("terminal", func(t *testing.T) {
		creatorSecrets := newMemorySecretStore()
		joiningSecrets := newMemorySecretStore()
		creator := saveTestCreator(t, creatorSecrets, "creator")
		creator.Zero()
		relay := &fakePairingRelay{claimError: &syncclient.APIError{Status: 401, Code: "invalid_or_expired_pairing"}, rollbackTombstone: true}
		_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
			privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
		if _, err := ClaimPairing(context.Background(), relay, joiningOptions); err == nil || !strings.Contains(err.Error(), "journal was aborted") {
			t.Fatalf("terminal claim did not report safe local abort: %v", err)
		}
		if _, err := joiningSecrets.Load(context.Background(), "joining"+joiningPairingSuffix); !errors.Is(err, ErrSecretNotFound) {
			t.Fatalf("terminal claim journal remains: %v", err)
		}
	})
	t.Run("ordinary-401", func(t *testing.T) {
		creatorSecrets := newMemorySecretStore()
		joiningSecrets := newMemorySecretStore()
		creator := saveTestCreator(t, creatorSecrets, "creator")
		creator.Zero()
		relay := &fakePairingRelay{claimError: &syncclient.APIError{Status: 401, Code: "invalid_pairing_claim_proof"}}
		_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
			privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
		if _, err := ClaimPairing(context.Background(), relay, joiningOptions); err == nil {
			t.Fatal("ordinary claim 401 unexpectedly succeeded")
		}
		journal := loadJoiningJournalForTest(t, joiningSecrets, "joining")
		if journal.RollbackPending {
			t.Fatal("ordinary claim 401 was generalized into rollback success")
		}
	})
}

func TestAbortJoiningPairingDeleteResponseLossAndTemporaryFailureResume(t *testing.T) {
	for name, configure := range map[string]func(*fakePairingRelay){
		"response-loss": func(relay *fakePairingRelay) { relay.loseDeleteReply = true },
		"temporary": func(relay *fakePairingRelay) {
			relay.deleteError = errors.New("synthetic temporary network failure")
			relay.deleteFailures = 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			creatorSecrets := newMemorySecretStore()
			joiningSecrets := newMemorySecretStore()
			creator := saveTestCreator(t, creatorSecrets, "creator")
			creator.Zero()
			relay := &fakePairingRelay{}
			_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
				privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
			configure(relay)
			if _, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err == nil {
				t.Fatal("ambiguous DELETE unexpectedly reported abort success")
			}
			journal := loadJoiningJournalForTest(t, joiningSecrets, "joining")
			if !journal.RollbackPending {
				t.Fatal("ambiguous DELETE did not retain durable rollback intent")
			}
			if result, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err != nil || result.State != "aborted" {
				t.Fatalf("abort retry result=%#v err=%v", result, err)
			}
			if relay.deleteCalls != 2 {
				t.Fatalf("abort retry delete calls=%d", relay.deleteCalls)
			}
		})
	}
}

func TestAbortJoiningPairingLocalJournalDeleteFailureResumes(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningMemory := newMemorySecretStore()
	joiningSecrets := &failDeleteSecretStore{memorySecretStore: joiningMemory, profile: "joining" + joiningPairingSuffix, failures: 1}
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{}
	_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	if _, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err == nil {
		t.Fatal("retained local journal unexpectedly reported abort success")
	}
	if journal := loadJoiningJournalForTest(t, joiningSecrets, "joining"); !journal.RollbackPending {
		t.Fatal("failed local journal deletion lost rollback intent")
	}
	if result, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err != nil || result.State != "aborted" {
		t.Fatalf("local cleanup retry result=%#v err=%v", result, err)
	}
	if relay.deleteCalls != 2 {
		t.Fatalf("local cleanup retry did not replay tombstone: %d", relay.deleteCalls)
	}
}

func TestAbortJoiningPairingReconcilesJournalDeleteResponseLoss(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningMemory := newMemorySecretStore()
	joiningSecrets := &loseDeleteResponseSecretStore{
		memorySecretStore: joiningMemory, profile: "joining" + joiningPairingSuffix, failures: 1,
	}
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{}
	_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	if result, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err != nil || result.State != "aborted" {
		t.Fatalf("journal delete response loss was not reconciled: result=%#v err=%v", result, err)
	}
	if _, err := joiningSecrets.Load(context.Background(), "joining"+joiningPairingSuffix); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("reconciled abort journal remains: %v", err)
	}
}

func TestAbortJoiningPairingFailsClosedOnActiveCredentialMismatch(t *testing.T) {
	creatorSecrets := newMemorySecretStore()
	joiningSecrets := newMemorySecretStore()
	creator := saveTestCreator(t, creatorSecrets, "creator")
	creator.Zero()
	relay := &fakePairingRelay{}
	_, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
		privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
	unrelated := saveTestCreator(t, joiningSecrets, "joining")
	unrelated.Zero()
	if _, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("active credential mismatch did not fail closed: %v", err)
	}
	if relay.deleteCalls != 0 {
		t.Fatalf("active mismatch mutated remote state: delete calls=%d", relay.deleteCalls)
	}
	if journal := loadJoiningJournalForTest(t, joiningSecrets, "joining"); journal.RollbackPending {
		t.Fatal("active mismatch changed protected journal before validation")
	}
}

func TestAbortJoiningPairingRetainsReadyAndFinalizedStateOnStable409(t *testing.T) {
	for _, finalize := range []bool{false, true} {
		name := "ready"
		if finalize {
			name = "finalized"
		}
		t.Run(name, func(t *testing.T) {
			creatorSecrets := newMemorySecretStore()
			joiningSecrets := newMemorySecretStore()
			creator := saveTestCreator(t, creatorSecrets, "creator")
			creator.Zero()
			relay := &fakePairingRelay{}
			creatorOptions, joiningOptions := establishApprovedPairing(t, relay, creatorSecrets, joiningSecrets,
				privateTestPath(t, "creator.db"), privateTestPath(t, "joining.db"))
			if result, err := ClaimPairing(context.Background(), relay, joiningOptions); err != nil || result.State != "finalize_pending" {
				t.Fatalf("claim result=%#v err=%v", result, err)
			}
			if finalize {
				if _, err := FinalizePairing(context.Background(), relay, creatorOptions); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := AbortJoiningPairing(context.Background(), relay, joiningOptions); err == nil {
				t.Fatalf("%s rollback 409 was treated as success", name)
			}
			journal := loadJoiningJournalForTest(t, joiningSecrets, "joining")
			if !journal.RollbackPending || journal.PendingCredential == "" {
				t.Fatalf("%s abort did not retain protected authenticated rollback state", name)
			}
			if _, err := joiningSecrets.Load(context.Background(), "joining"); err != nil {
				t.Fatalf("%s abort removed active credential: %v", name, err)
			}
			if _, err := os.Lstat(joiningOptions.DatabasePath); err != nil {
				t.Fatalf("%s abort removed encrypted database: %v", name, err)
			}
		})
	}
}

func createRollbackTestDatabase(t *testing.T, path string, bundle CredentialBundleV1) {
	t.Helper()
	if err := ensureEncryptedStore(context.Background(), path, bundle, nil); err != nil {
		t.Fatalf("create exact-key rollback database: %v", err)
	}
}

func assertRollbackDatabaseAbsent(t *testing.T, path string) {
	t.Helper()
	for _, candidate := range databaseSidecarPaths(path) {
		if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rollback artifact remains at %q: %v", candidate, err)
		}
	}
}

func presentRollbackTestFiles(t *testing.T, path string) []string {
	t.Helper()
	var present []string
	for _, candidate := range databaseSidecarPaths(path) {
		if _, err := os.Lstat(candidate); err == nil {
			present = append(present, candidate)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	return present
}

func TestCleanupUncommittedDatabaseResumesAtEveryDeletionBoundary(t *testing.T) {
	for killAt, label := range []string{"before-wal", "before-shm", "before-journal", "before-main"} {
		t.Run(label, func(t *testing.T) {
			path := privateTestPath(t, "joining.db")
			for _, candidate := range databaseSidecarPaths(path) {
				writePrivateTestFile(t, candidate, []byte("validated rollback test"))
			}
			removeCalls := 0
			ops := joiningDatabaseRollbackOps{
				remove: func(candidate string) error {
					if removeCalls == killAt {
						return errors.New("synthetic crash before unlink")
					}
					removeCalls++
					return removePrivateFile(candidate)
				},
				syncParent: syncParentDirectory,
			}
			if err := removeValidatedJoiningDatabaseFiles(path, databaseSidecarPaths(path), ops); err == nil {
				t.Fatal("synthetic unlink boundary unexpectedly completed")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("main database was removed before all sidecars were durable: %v", err)
			}
			if err := removeValidatedJoiningDatabaseFiles(path, presentRollbackTestFiles(t, path), joiningDatabaseRollbackOps{
				remove: removePrivateFile, syncParent: syncParentDirectory,
			}); err != nil {
				t.Fatalf("retry after %s: %v", label, err)
			}
			assertRollbackDatabaseAbsent(t, path)
		})
	}

	t.Run("after-main-before-final-fsync", func(t *testing.T) {
		path := privateTestPath(t, "joining.db")
		for _, candidate := range databaseSidecarPaths(path) {
			writePrivateTestFile(t, candidate, []byte("validated rollback test"))
		}
		syncCalls := 0
		ops := joiningDatabaseRollbackOps{
			remove: removePrivateFile,
			syncParent: func(parent string) error {
				syncCalls++
				if syncCalls == 2 {
					return errors.New("synthetic crash before final directory fsync")
				}
				return syncParentDirectory(parent)
			},
		}
		if err := removeValidatedJoiningDatabaseFiles(path, databaseSidecarPaths(path), ops); err == nil {
			t.Fatal("synthetic final fsync boundary unexpectedly completed")
		}
		assertRollbackDatabaseAbsent(t, path)
		if err := syncParentDirectory(filepath.Dir(path)); err != nil {
			t.Fatalf("retry final parent fsync: %v", err)
		}
	})
}

func TestCleanupUncommittedDatabaseRejectsEverySidecarWithoutMain(t *testing.T) {
	bundle := testCredentials()
	defer bundle.Zero()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			path := privateTestPath(t, "joining.db")
			writePrivateTestFile(t, path+suffix, nil)
			if err := cleanupUncommittedDatabase(context.Background(), path, bundle); err == nil || !strings.Contains(err.Error(), "sidecar remains") {
				t.Fatalf("sidecar-only state was not rejected: %v", err)
			}
			if _, err := os.Lstat(path + suffix); err != nil {
				t.Fatalf("rejected sidecar was mutated: %v", err)
			}
		})
	}
}

func TestCleanupUncommittedDatabaseNeverLoosensExactKeyVerification(t *testing.T) {
	correct := testCredentials()
	defer correct.Zero()
	path := privateTestPath(t, "joining.db")
	createRollbackTestDatabase(t, path, correct)
	store, err := localstore.OpenForDevice(context.Background(), path, correct.LocalDataKey[:], correct.ObjectIDKey[:], correct.DeviceIDHex())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveExplicit(context.Background(), localstore.Phrase{Text: "synthetic", Pinyin: "synthetic", Source: "test", Pinned: true}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := protectPrivateDatabaseFiles(path); err != nil {
		t.Fatal(err)
	}
	wrong := testCredentials()
	wrong.LocalDataKey[0] ^= 0xff
	defer wrong.Zero()
	if err := cleanupUncommittedDatabase(context.Background(), path, wrong); err == nil {
		t.Fatal("wrong database key was accepted for rollback deletion")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("wrong-key rollback mutated the main database: %v", err)
	}
}
