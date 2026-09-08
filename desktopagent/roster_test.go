// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

type rosterReply struct {
	value syncclient.RosterUpdates
	reads int
}

func (relay *rosterReply) GetRosterUpdates(context.Context, string, uint64, []byte) (syncclient.RosterUpdates, error) {
	relay.reads++
	return relay.value, nil
}
func (*rosterReply) PublishRosterAnchor(context.Context, string, protocol.PairingRoster) error {
	return errors.New("unexpected anchor write")
}

func rosterRefreshFixture(t *testing.T) (CredentialBundleV1, protocol.PairingRoster, *memorySecretStore) {
	t.Helper()
	bundle := testCredentials()
	selfEd := bundle.VerificationKeys[bundle.DeviceID]
	selfX := bundle.X25519PublicKeys[bundle.DeviceID]
	secondID := filled16(0x77)
	secondEd := filled32(0x78)
	secondX := filled32(0x79)
	private := ed25519.NewKeyFromSeed(bundle.SigningSeed[:])
	anchor, err := protocol.SignPairingRoster(bundle.AccountID[:], 1, []protocol.PairingRosterDevice{
		{DeviceID: bundle.DeviceID[:], Ed25519PublicKey: selfEd[:], X25519PublicKey: selfX[:]},
		{DeviceID: secondID[:], Ed25519PublicKey: secondEd[:], X25519PublicKey: secondX[:]},
	}, bundle.DeviceID[:], private)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyTrustedRoster(&bundle, anchor); err != nil {
		t.Fatal(err)
	}
	next, err := protocol.SignPairingRosterAdvance(anchor, protocol.PairingRosterDevice{DeviceID: bytes.Repeat([]byte{0x88}, 16), Ed25519PublicKey: bytes.Repeat([]byte{0x89}, 32), X25519PublicKey: bytes.Repeat([]byte{0x90}, 32)}, bundle.DeviceID[:], private)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newMemorySecretStore()
	secrets.values["owner"] = encoded
	return bundle, next, secrets
}

func TestRosterRefreshPersistsBeforeReplacingResidentTrust(t *testing.T) {
	bundle, next, secrets := rosterRefreshFixture(t)
	before := append([]byte(nil), secrets.values["owner"]...)
	digest, _ := protocol.PairingRosterDigest(next)
	relay := &rosterReply{value: syncclient.RosterUpdates{HeadVersion: next.Version, HeadHash: hex.EncodeToString(digest), Updates: []protocol.PairingRoster{next}}}
	failure := &failProfileSecretStore{memorySecretStore: secrets, profile: "owner", failures: 1}
	if changed, err := RefreshTrustedRoster(context.Background(), relay, failure, "owner", &bundle); err == nil || changed {
		t.Fatal("failed protected save accepted new trust")
	}
	still, err := EncodeCredentialBundle(bundle)
	if err != nil || !bytes.Equal(still, before) || !bytes.Equal(secrets.values["owner"], before) {
		t.Fatal("failed save mutated active or resident credential")
	}
	if changed, err := RefreshTrustedRoster(context.Background(), relay, secrets, "owner", &bundle); err != nil || !changed {
		t.Fatalf("refresh: changed=%v err=%v", changed, err)
	}
	if bundle.Version != ChainedCredentialBundleVersion || len(bundle.VerificationKeys) != 3 {
		t.Fatal("new checkpoint did not enter resident trust")
	}
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, secrets.values["owner"]) {
		t.Fatal("resident trust differs from durable checkpoint")
	}
	encoded[4] = CredentialBundleVersion
	if _, err := DecodeCredentialBundle(encoded); err == nil {
		t.Fatal("v3 checkpoint masqueraded as legacy v2")
	}
}

func TestRosterRefreshRejectsBadOrUncommittableProgress(t *testing.T) {
	for _, scenario := range []string{"no_progress", "duplicate", "bad_hash", "tampered_parent", "pending_pairing", "concurrent_credential"} {
		t.Run(scenario, func(t *testing.T) {
			bundle, next, secrets := rosterRefreshFixture(t)
			before, _ := EncodeCredentialBundle(bundle)
			digest, _ := protocol.PairingRosterDigest(next)
			response := syncclient.RosterUpdates{HeadVersion: next.Version, HeadHash: hex.EncodeToString(digest), Updates: []protocol.PairingRoster{next}}
			switch scenario {
			case "no_progress":
				response.Updates = nil
			case "duplicate":
				response.Updates = []protocol.PairingRoster{bundle.TrustedRoster}
			case "bad_hash":
				response.HeadHash = "invalid"
			case "tampered_parent":
				response.Updates[0].PreviousHash[0] ^= 1
			case "pending_pairing":
				secrets.values["owner"+creatorPairingSuffix] = []byte("synthetic pending journal")
			case "concurrent_credential":
				secrets.values["owner"] = []byte("different synthetic protected value")
			}
			relay := &rosterReply{value: response}
			if changed, err := RefreshTrustedRoster(context.Background(), relay, secrets, "owner", &bundle); err == nil || changed {
				t.Fatal("unsafe roster progress accepted")
			} else if scenario != "pending_pairing" && scenario != "concurrent_credential" {
				var protocolErr *syncclient.RelayProtocolError
				if !errors.As(err, &protocolErr) {
					t.Fatal("invalid remote trust was misclassified as a local store failure")
				}
			}
			after, _ := EncodeCredentialBundle(bundle)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected progress changed resident trust")
			}
			if relay.reads > 1 {
				t.Fatal("non-progress response was repeatedly polled")
			}
		})
	}
}

func TestCreatorRollbackRetainsExactExistingMultiDeviceRoster(t *testing.T) {
	bundle, next, secrets := rosterRefreshFixture(t)
	original := append([]byte(nil), secrets.values["owner"]...)
	digest := sha256.Sum256(original)
	journal := creatorPairingJournal{ActiveDigest: hex.EncodeToString(digest[:]), OriginalCredential: base64.RawURLEncoding.EncodeToString(original)}
	if err := applyTrustedRoster(&bundle, next); err != nil {
		t.Fatal(err)
	}
	restored, err := originalCreatorCredential(journal, bundle)
	if err != nil || !bytes.Equal(restored, original) {
		t.Fatalf("exact two-peer rollback failed: %v", err)
	}
	journal.OriginalCredential = ""
	if _, err := originalCreatorCredential(journal, bundle); err == nil {
		t.Fatal("multi-device rollback used the self-only fallback")
	}
}

type pagedRosterReply struct {
	chain                      []protocol.PairingRoster
	pages, totalBytes, maxPage int
}

func (relay *pagedRosterReply) GetRosterUpdates(_ context.Context, _ string, version uint64, _ []byte) (syncclient.RosterUpdates, error) {
	head := relay.chain[len(relay.chain)-1]
	digest, _ := protocol.PairingRosterDigest(head)
	end := int(version) + 16
	if end > len(relay.chain) {
		end = len(relay.chain)
	}
	response := syncclient.RosterUpdates{HeadVersion: head.Version, HeadHash: hex.EncodeToString(digest), Updates: relay.chain[int(version):end]}
	encoded, _ := json.Marshal(response)
	relay.pages++
	relay.totalBytes += len(encoded)
	if len(encoded) > relay.maxPage {
		relay.maxPage = len(encoded)
	}
	return response, nil
}
func (*pagedRosterReply) PublishRosterAnchor(context.Context, string, protocol.PairingRoster) error {
	return errors.New("unexpected anchor")
}

func TestMaximumRosterFitsProtectedJournalAndBoundedOfflineRefresh(t *testing.T) {
	bundle, _, secrets := rosterRefreshFixture(t)
	anchorBytes := append([]byte(nil), secrets.values["owner"]...)
	chain := []protocol.PairingRoster{bundle.TrustedRoster}
	private := ed25519.NewKeyFromSeed(bundle.SigningSeed[:])
	for count := 3; count < protocol.MaxChainedRosterDevices; count++ {
		id := make([]byte, 16)
		binary.BigEndian.PutUint64(id, uint64(1000+count))
		roster, err := protocol.SignPairingRosterAdvance(chain[len(chain)-1], protocol.PairingRosterDevice{DeviceID: id, Ed25519PublicKey: bytes.Repeat([]byte{0xA1}, 32), X25519PublicKey: bytes.Repeat([]byte{0xA2}, 32)}, bundle.DeviceID[:], private)
		if err != nil {
			t.Fatal(err)
		}
		chain = append(chain, roster)
	}
	if err := applyTrustedRoster(&bundle, chain[len(chain)-1]); err != nil {
		t.Fatal(err)
	}
	bundle.DeviceToken = bytes.Repeat([]byte{'a'}, maxDeviceTokenBytes)
	bundle.EpochKeys = make(map[uint64][32]byte, maxEpochKeys)
	for epoch := uint64(1); epoch <= maxEpochKeys; epoch++ {
		bundle.EpochKeys[epoch] = filled32(byte(epoch))
	}
	bundle.CurrentEpoch = maxEpochKeys
	original, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	joining, err := protocol.NewDeviceKeys(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	selfEd := bundle.VerificationKeys[bundle.DeviceID]
	selfX := bundle.X25519PublicKeys[bundle.DeviceID]
	transcript := protocol.PairingTranscript{PairingID: bytes.Repeat([]byte{0xE0}, 16), AccountID: bundle.AccountID[:], CreatorDeviceID: bundle.DeviceID[:], JoiningDeviceID: bytes.Repeat([]byte{0xE1}, 16), CreatorEd25519PublicKey: selfEd[:], CreatorX25519PublicKey: selfX[:], JoiningEd25519PublicKey: joining.Ed25519Public, JoiningX25519PublicKey: joining.X25519Public}
	payload, err := pairingPackageFromBundle(bundle, transcript)
	if err != nil {
		t.Fatal(err)
	}
	box, err := protocol.SealPairingPackage(bundle.X25519Private[:], joining.X25519Public, bytes.Repeat([]byte{0xE2}, 32), transcript, payload, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	boxText, err := protocol.EncodeSealedBox(box)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyTrustedRoster(&bundle, payload.Roster); err != nil {
		t.Fatal(err)
	}
	updated, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	journal, err := encodePairingJournal(creatorPairingJournal{Version: pairingJournalVersion, Invitation: string(bytes.Repeat([]byte{'x'}, maxPairingTextBytes)), ActiveDigest: hex.EncodeToString(digest[:]), OriginalCredential: base64.RawURLEncoding.EncodeToString(original), UpdatedCredential: base64.RawURLEncoding.EncodeToString(updated), ApprovedBox: boxText, FinalizationPending: true})
	if err != nil || len(journal) > maxCredentialBlobBytes {
		t.Fatalf("worst-case protected journal bytes=%d err=%v", len(journal), err)
	}
	chain = append(chain, payload.Roster)
	if len(payload.Roster.Devices) != protocol.MaxChainedRosterDevices {
		t.Fatal("size fixture did not reach the device resource cap")
	}
	tooMany := protocol.PairingRosterDevice{DeviceID: bytes.Repeat([]byte{0xE3}, 16), Ed25519PublicKey: joining.Ed25519Public, X25519PublicKey: joining.X25519Public}
	if _, err := protocol.SignPairingRosterAdvance(payload.Roster, tooMany, bundle.DeviceID[:], private); err == nil {
		t.Fatal("accepted an addition above the tested resource cap")
	}
	offline, err := DecodeCredentialBundle(anchorBytes)
	if err != nil {
		t.Fatal(err)
	}
	secrets.values["owner"] = anchorBytes
	relay := &pagedRosterReply{chain: chain}
	if changed, err := RefreshTrustedRoster(context.Background(), relay, secrets, "owner", &offline); err != nil || !changed {
		t.Fatalf("maximum offline catch-up: changed=%v err=%v", changed, err)
	}
	if len(offline.VerificationKeys) != protocol.MaxChainedRosterDevices || relay.pages > 32 || relay.totalBytes > 8<<20 || relay.maxPage > 2<<20 {
		t.Fatal("maximum chain exceeds bounded refresh transport")
	}
	t.Logf("max_devices=%d journal_bytes=%d pages=%d total_json_bytes=%d max_page_bytes=%d", protocol.MaxChainedRosterDevices, len(journal), relay.pages, relay.totalBytes, relay.maxPage)
}
