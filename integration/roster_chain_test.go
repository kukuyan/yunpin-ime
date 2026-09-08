// SPDX-License-Identifier: Apache-2.0
package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	syncserver "github.com/kukuyan/yunpin-ime/sync/server"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

type rosterSecrets map[string][]byte

func (store rosterSecrets) Load(_ context.Context, profile string) ([]byte, error) {
	value, ok := store[profile]
	if !ok {
		return nil, desktopagent.ErrSecretNotFound
	}
	return append([]byte(nil), value...), nil
}
func (store rosterSecrets) Save(_ context.Context, profile string, value []byte) error {
	store[profile] = append([]byte(nil), value...)
	return nil
}
func (store rosterSecrets) Delete(_ context.Context, profile string) error {
	delete(store, profile)
	return nil
}

type loseRosterFinalizeResponse struct {
	base http.RoundTripper
	lost bool
}

type rejectRosterFinalizeOnce struct {
	base     http.RoundTripper
	rejected bool
}

func (transport *rejectRosterFinalizeOnce) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, "/finalize") && !transport.rejected {
		transport.rejected = true
		return &http.Response{StatusCode: http.StatusConflict, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"roster_conflict"}`)), Request: request}, nil
	}
	return transport.base.RoundTrip(request)
}

func (transport *loseRosterFinalizeResponse) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil && strings.HasSuffix(request.URL.Path, "/finalize") && !transport.lost {
		transport.lost = true
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		return nil, errors.New("synthetic finalized response loss")
	}
	return response, err
}

func TestFourDevicesPairThroughHTTPAndConvergeFromSignedCheckpoints(t *testing.T) {
	ctx := context.Background()
	privateDatabase := func() string {
		directory := t.TempDir()
		if err := os.Chmod(directory, 0700); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(directory, "local.db")
	}
	application, err := syncserver.New(ctx, filepath.Join(t.TempDir(), "relay.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	relay := httptest.NewServer(application)
	defer relay.Close()
	endpoint, err := syncclient.ParseEndpoint(relay.URL, syncclient.EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	session := integrationUserSession(t, ctx, endpoint)
	client := syncclient.New(endpoint, syncclient.WithUserSession(session.Token))
	registration, keys := syntheticRegistration(0x11, bytes.Repeat([]byte{0x51}, 32))
	account, err := syncclient.GenerateAccountCredentials(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	account, err = client.CreateAccount(ctx, account, registration)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PutKeyring(ctx, account.DeviceToken, 1, protocol.SealedBox{Nonce: bytes.Repeat([]byte{0x61}, 24), Ciphertext: bytes.Repeat([]byte{0x62}, 16)}); err != nil {
		t.Fatal(err)
	}
	if err := client.SealAccount(ctx, account.AccountID, account.DeviceToken); err != nil {
		t.Fatal(err)
	}
	bundle := desktopagent.CredentialBundleV1{Version: desktopagent.CredentialBundleVersion, DeviceToken: []byte(account.DeviceToken), CurrentEpoch: 1,
		EpochKeys: make(map[uint64][32]byte), VerificationKeys: make(map[[16]byte][32]byte), X25519PublicKeys: make(map[[16]byte][32]byte)}
	copy(bundle.AccountID[:], account.AccountID)
	copy(bundle.DeviceID[:], account.DeviceID)
	copy(bundle.SigningSeed[:], keys.Ed25519Private.Seed())
	copy(bundle.X25519Private[:], keys.X25519Private)
	copy(bundle.LocalDataKey[:], bytes.Repeat([]byte{0x41}, 32))
	copy(bundle.ObjectIDKey[:], bytes.Repeat([]byte{0x42}, 32))
	var epoch, ed, x [32]byte
	copy(epoch[:], bytes.Repeat([]byte{0x43}, 32))
	copy(ed[:], keys.Ed25519Public)
	copy(x[:], keys.X25519Public)
	bundle.EpochKeys[1] = epoch
	bundle.VerificationKeys[bundle.DeviceID] = ed
	bundle.X25519PublicKeys[bundle.DeviceID] = x
	encoded, err := desktopagent.EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	secrets := []rosterSecrets{{"owner": encoded}}
	options := []desktopagent.PairingOptions{{Secrets: secrets[0], Profile: "owner", DatabasePath: privateDatabase(), Random: rand.Reader}}
	load := func(index int) desktopagent.CredentialBundleV1 {
		value, err := desktopagent.DecodeCredentialBundle(secrets[index]["owner"])
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	var anchor protocol.PairingRoster
	for index := 1; index < 4; index++ {
		if index == 2 {
			// Cancelling a third-device approval must preserve the exact existing
			// two-peer credential, not reconstruct the old self-only bootstrap.
			before := append([]byte(nil), secrets[0]["owner"]...)
			invited, err := desktopagent.StartPairing(ctx, client, options[0])
			if err != nil {
				t.Fatal(err)
			}
			aborted := desktopagent.PairingOptions{Secrets: rosterSecrets{}, Profile: "owner", DatabasePath: privateDatabase(), Random: rand.Reader}
			if _, err := desktopagent.JoinPairing(ctx, client, aborted, invited.Invitation); err != nil {
				t.Fatal(err)
			}
			if _, err := desktopagent.ApprovePairing(ctx, client, options[0]); err != nil {
				t.Fatal(err)
			}
			base := load(0)
			other, err := syncclient.GeneratePairingInvitation(account, registration.DeviceRegistration, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.CreatePairingFromRoster(ctx, account, other, base.TrustedRoster); err == nil {
				t.Fatal("second reservation forked the same roster head")
			}
			if _, err := desktopagent.CancelCreatorPairing(ctx, client, options[0]); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, secrets[0]["owner"]) {
				t.Fatal("third-device cancellation changed prior credential")
			}
			if _, err := desktopagent.AbortJoiningPairing(ctx, client, aborted); err != nil {
				t.Fatal(err)
			}
		}
		creator := 0
		if index == 3 {
			creator = 1
		} // The second already-trusted device may approve another.
		newSecrets := rosterSecrets{}
		secrets = append(secrets, newSecrets)
		options = append(options, desktopagent.PairingOptions{Secrets: newSecrets, Profile: "owner", DatabasePath: privateDatabase(), Random: rand.Reader})
		invited, err := desktopagent.StartPairing(ctx, client, options[creator])
		if err != nil {
			t.Fatalf("start device %d: %v", index+1, err)
		}
		if _, err := desktopagent.JoinPairing(ctx, client, options[index], invited.Invitation); err != nil {
			t.Fatalf("join: %v", err)
		}
		if _, err := desktopagent.ApprovePairing(ctx, client, options[creator]); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if result, err := desktopagent.ClaimPairing(ctx, client, options[index]); err != nil || result.State != "finalize_pending" {
			t.Fatalf("claim: %s %v", result.State, err)
		}
		pending := load(index)
		if _, err := client.Sync(ctx, string(pending.DeviceToken), syncclient.SyncRequest{}); err == nil {
			t.Fatal("unfinalized device entered ordinary sync")
		}
		if index > 1 {
			base := load(creator)
			digest, _ := protocol.PairingRosterDigest(base.TrustedRoster)
			before, err := client.GetRosterUpdates(ctx, string(base.DeviceToken), base.TrustedRoster.Version, digest)
			if err != nil || before.HeadVersion != base.TrustedRoster.Version || len(before.Updates) != 0 {
				t.Fatal("prepared roster was published before finalize")
			}
			pairingID, _ := hex.DecodeString(invited.PairingIDHex)
			if err := client.FinalizePairing(ctx, pairingID, string(base.DeviceToken)); err == nil {
				t.Fatal("legacy finalize bypassed signed roster publication")
			}
		}
		if index == 2 {
			drop := &loseRosterFinalizeResponse{base: http.DefaultTransport}
			lossy := syncclient.New(endpoint, syncclient.WithTransport(drop), syncclient.WithUserSession(session.Token))
			if _, err := desktopagent.FinalizePairing(ctx, lossy, options[creator]); err == nil {
				t.Fatal("expected lost finalize response")
			}
			if _, err := desktopagent.CancelCreatorPairing(ctx, client, options[creator]); err == nil {
				t.Fatal("possibly published roster was cancelled")
			}
		}
		if index == 3 {
			reject := &rejectRosterFinalizeOnce{base: http.DefaultTransport}
			conflictClient := syncclient.New(endpoint, syncclient.WithTransport(reject), syncclient.WithUserSession(session.Token))
			if _, err := desktopagent.FinalizePairing(ctx, conflictClient, options[creator]); err == nil {
				t.Fatal("expected explicit finalize conflict")
			}
			if _, err := desktopagent.CancelCreatorPairing(ctx, client, options[creator]); err == nil {
				t.Fatal("finalization intent was cancelled after a conflict response")
			}
		}
		if _, err := desktopagent.FinalizePairing(ctx, client, options[creator]); err != nil {
			t.Fatalf("finalize: %v", err)
		}
		if result, err := desktopagent.ClaimPairing(ctx, client, options[index]); err != nil || result.State != "ready" {
			t.Fatalf("claim finalize: %s %v", result.State, err)
		}
		if index == 1 {
			anchor = load(index).TrustedRoster
		}
	}
	// A stale or legacy enrollment request cannot bypass roster-head CAS.
	invitation, err := syncclient.GeneratePairingInvitation(account, registration.DeviceRegistration, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreatePairing(ctx, account, invitation); err == nil {
		t.Fatal("legacy request enrolled another device")
	}
	if _, err := client.CreatePairingFromRoster(ctx, account, invitation, anchor); err == nil {
		t.Fatal("stale checkpoint enrolled another device")
	}
	stores := make([]*localstore.Store, 4)
	workers := make([]*syncclient.Worker, 4)
	for index := range stores {
		current := load(index)
		seedBefore := current.SigningSeed
		if _, err := desktopagent.RefreshTrustedRoster(ctx, client, secrets[index], "owner", &current); err != nil {
			t.Fatalf("offline refresh %d: %v", index, err)
		}
		if current.SigningSeed != seedBefore || len(current.VerificationKeys) != 4 || current.Version != desktopagent.ChainedCredentialBundleVersion {
			t.Fatal("refresh changed identity or failed to persist four-device trust")
		}
		persisted := load(index)
		digest, _ := protocol.PairingRosterDigest(current.TrustedRoster)
		response, err := client.GetRosterUpdates(ctx, string(current.DeviceToken), current.TrustedRoster.Version, digest)
		if err != nil || response.HeadHash != hex.EncodeToString(digest) || persisted.TrustedRoster.Version != current.TrustedRoster.Version {
			t.Fatal("credential and finalized relay head differ")
		}
		store, err := localstore.OpenForDevice(ctx, options[index].DatabasePath, current.LocalDataKey[:], current.ObjectIDKey[:], current.DeviceIDHex())
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		stores[index] = store
		trust := make(map[string]ed25519.PublicKey)
		for id, key := range current.VerificationKeys {
			trust[hex.EncodeToString(id[:])] = append(ed25519.PublicKey(nil), key[:]...)
		}
		epochKey := current.EpochKeys[1]
		workers[index] = &syncclient.Worker{Client: client, Store: store, Session: syncclient.Session{AccountID: current.AccountID[:], DeviceID: current.DeviceID[:], DeviceToken: string(current.DeviceToken), KeyEpoch: 1, EpochKeys: map[uint64][]byte{1: epochKey[:]}, SigningPrivate: ed25519.NewKeyFromSeed(current.SigningSeed[:]), VerificationKeys: trust}}
	}
	phrase := localstore.Phrase{Text: "合成四端往返清理", Pinyin: "he cheng si duan wang fan qing li"}
	if _, err := stores[0].RecordSelection(ctx, phrase, localstore.LearningContext{}); err != nil {
		t.Fatal(err)
	}
	for _, worker := range workers {
		if _, err := worker.SyncUntilIdle(ctx, 8); err != nil {
			t.Fatal(err)
		}
	}
	for _, store := range stores {
		snapshot, err := store.Snapshot(ctx)
		if err != nil || len(snapshot.Phrases) != 1 || snapshot.Phrases[0].UseCount != 1 || snapshot.Phrases[0].Deleted {
			t.Fatal("four-device synthetic learning failed to converge")
		}
	}
	if err := stores[3].Delete(ctx, phrase.Text, phrase.Pinyin); err != nil {
		t.Fatal(err)
	}
	if _, err := workers[3].SyncUntilIdle(ctx, 8); err != nil {
		t.Fatal(err)
	}
	for _, worker := range workers {
		if _, err := worker.SyncUntilIdle(ctx, 8); err != nil {
			t.Fatal(err)
		}
	}
	for _, store := range stores {
		snapshot, err := store.Snapshot(ctx)
		if err != nil || len(snapshot.Phrases) != 1 || !snapshot.Phrases[0].Deleted {
			t.Fatal("four-device deletion failed to converge")
		}
	}
}
