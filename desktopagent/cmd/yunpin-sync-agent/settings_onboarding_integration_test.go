//go:build onboarding_integration

// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	syncserver "github.com/kukuyan/yunpin-ime/sync/server"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

// Run using scripts/test_settings_onboarding_integration.py. The temporary
// modfile adds the repository's actual relay only for this test build; the
// shipping desktop agent does not acquire a server runtime dependency.
func TestOnboardingHTTPRealRelaySixStepsResumeAndThirdDevice(t *testing.T) {
	ctx := context.Background()
	application, err := syncserver.New(ctx, filepath.Join(t.TempDir(), "relay.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	var dropFinalize atomic.Bool
	var claims, finalizedPosts atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/claim") && strings.HasPrefix(r.URL.Path, "/v1/pairings/") {
			claims.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/finalize") {
			finalizedPosts.Add(1)
			if dropFinalize.Swap(false) {
				committed := httptest.NewRecorder()
				application.ServeHTTP(committed, r)
				if committed.Code != http.StatusOK {
					t.Error("synthetic response loss did not follow a real successful finalize")
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":"synthetic_response_lost_after_commit"}`))
				return
			}
		}
		application.ServeHTTP(w, r)
	}))
	defer relay.Close()
	endpoint, _ := syncclient.ParseEndpoint(relay.URL, syncclient.EndpointPolicy{AllowPrivateHTTP: true})
	creator, creatorSecrets := onboardingTestOperations(t)
	apply := func(operations *localSettingsOperations, action settingsOnboardingAction) settingsOnboardingResult {
		t.Helper()
		result, err := operations.ApplyOnboarding(ctx, action)
		if err != nil {
			t.Fatalf("action %s failed with fixed UI code: %v", action.Kind, err)
		}
		return result
	}
	reopen := func(previous *localSettingsOperations) *localSettingsOperations {
		// A new settings process has no invitation cache and no old active
		// credential cache. Only the original protected store/paths survive.
		secrets := previous.onboardingSecrets()
		cache := &settingsSecretStore{underlying: secrets}
		agent := previous.agent
		agent.Secrets = cache
		return &localSettingsOperations{defaults: previous.defaults, agent: agent, secrets: cache}
	}
	apply(creator, settingsOnboardingAction{Kind: "server-save", Server: relay.URL, AllowPrivateHTTP: true})
	apply(creator, settingsOnboardingAction{Kind: "register", Username: "onboarding-user", Password: "synthetic-onboarding-password-only"})
	apply(creator, settingsOnboardingAction{Kind: "login"}) // reuses protected session
	// Bootstrap only a disposable, already-existing account fixture. No
	// first-account provisioning/recovery API is exposed by the GUI wrapper.
	bootstrapOnboardingHTTPAccount(t, ctx, creator, creatorSecrets, endpoint)
	creator = reopen(creator)
	for index := 0; index < 2; index++ {
		joining, joiningSecrets := onboardingTestOperations(t)
		apply(joining, settingsOnboardingAction{Kind: "server-save", Server: relay.URL, AllowPrivateHTTP: true})
		invited := apply(creator, settingsOnboardingAction{Kind: "pairing-invite"})
		if invited.Invitation == "" || invited.State.PairingRole != "creator" {
			t.Fatal("invite did not return its one-response material")
		}
		creator = reopen(creator)
		joined := apply(joining, settingsOnboardingAction{Kind: "pairing-join", Invitation: invited.Invitation})
		invited.Invitation = ""
		if joined.State.PairingState != "joined" || joined.State.SessionValid || joined.State.Paired {
			t.Fatal("new device bypassed pairing or unexpectedly required account login")
		}
		joining = reopen(joining)
		approved := apply(creator, settingsOnboardingAction{Kind: "pairing-continue"})
		if approved.State.PairingState != "awaiting_claim" {
			t.Fatal("creator did not preserve the approval journal")
		}
		creator = reopen(creator)
		claimed := apply(joining, settingsOnboardingAction{Kind: "pairing-continue"})
		if claimed.State.Paired || claimed.State.PairingState != "finalize_pending" {
			t.Fatal("joining device reported completion before creator finalization")
		}
		if info, err := os.Stat(joining.defaults.DatabasePath); err != nil || info.Size() == 0 {
			t.Fatal("claim did not initialize the actual configured encrypted database")
		}
		joining = reopen(joining)
		if index == 0 {
			dropFinalize.Store(true)
			_, err := creator.ApplyOnboarding(ctx, settingsOnboardingAction{Kind: "pairing-continue"})
			expectOnboardingCode(t, err, "pairing-failed")
			creator = reopen(creator)
			state, err := creator.OnboardingStatus(ctx)
			if err != nil || state.PairingState != "finalize_pending" || !state.PairingPending {
				t.Fatal("lost finalize response erased the protected continuation")
			}
			// Cancellation after publication intent is refused; the same
			// journal remains available to Continue after closing the page.
			before := append([]byte(nil), creatorSecrets.values["default.pairing-creator"]...)
			_, err = creator.ApplyOnboarding(ctx, settingsOnboardingAction{Kind: "pairing-cancel"})
			expectOnboardingCode(t, err, "pairing-failed")
			if !bytes.Equal(before, creatorSecrets.values["default.pairing-creator"]) {
				t.Fatal("refused cancellation changed the published transaction")
			}
		}
		finished := apply(creator, settingsOnboardingAction{Kind: "pairing-continue"})
		if !finished.State.Paired || finished.State.PairingPending {
			t.Fatal("creator finalization did not reach ready")
		}
		creator = reopen(creator)
		ready := apply(joining, settingsOnboardingAction{Kind: "pairing-continue"})
		if !ready.State.Paired || ready.State.PairingPending || ready.State.SessionValid {
			t.Fatal("final joining cleanup did not reuse device trust independently of login")
		}
		bundle, err := desktopagent.DecodeCredentialBundle(joiningSecrets.values["default"])
		if err != nil {
			t.Fatal(err)
		}
		if len(bundle.TrustedRoster.Devices) != index+2 {
			t.Fatal("new device did not receive the expected signed account roster")
		}
		store, err := localstore.OpenForDevice(ctx, joining.defaults.DatabasePath, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex())
		bundle.Zero()
		if err != nil {
			t.Fatal("configured encrypted DB cannot reopen with paired credentials")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if claims.Load() != 2 || finalizedPosts.Load() != 2 {
		t.Fatalf("resume repeated one-shot claim/finalize: claims=%d finalized=%d", claims.Load(), finalizedPosts.Load())
	}
	// A separately cancelled invitation keeps all three existing devices and
	// does not initialize the abandoned joining device's database.
	before := append([]byte(nil), creatorSecrets.values["default"]...)
	abandoned, abandonedSecrets := onboardingTestOperations(t)
	apply(abandoned, settingsOnboardingAction{Kind: "server-save", Server: relay.URL, AllowPrivateHTTP: true})
	invite := apply(creator, settingsOnboardingAction{Kind: "pairing-invite"})
	apply(abandoned, settingsOnboardingAction{Kind: "pairing-join", Invitation: invite.Invitation})
	invite.Invitation = ""
	apply(creator, settingsOnboardingAction{Kind: "pairing-cancel"})
	apply(reopen(abandoned), settingsOnboardingAction{Kind: "pairing-cancel"})
	if !bytes.Equal(before, creatorSecrets.values["default"]) || len(abandonedSecrets.values) != 0 {
		t.Fatal("cancellation did not preserve existing trust and clear only the abandoned join")
	}
}

func bootstrapOnboardingHTTPAccount(t *testing.T, ctx context.Context, operations *localSettingsOperations, secrets *onboardingTestSecrets, endpoint syncclient.Endpoint) {
	t.Helper()
	session, err := desktopagent.LoadUserSession(ctx, secrets, "default", endpoint.String())
	if err != nil {
		t.Fatal(err)
	}
	client := syncclient.New(endpoint, syncclient.WithUserSession(session.Token))
	keys, err := protocol.NewDeviceKeys(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	account, err := syncclient.GenerateAccountCredentials(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	account, err = client.CreateAccount(ctx, account, syncclient.AccountRegistration{RecoveryAuthentication: bytes.Repeat([]byte{0x51}, 32), DeviceRegistration: syncclient.DeviceRegistration{DeviceNameCiphertext: bytes.Repeat([]byte{0x52}, 32), Ed25519PublicKey: keys.Ed25519Public, X25519PublicKey: keys.X25519Public}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PutKeyring(ctx, account.DeviceToken, 1, protocol.SealedBox{Nonce: bytes.Repeat([]byte{0x53}, 24), Ciphertext: bytes.Repeat([]byte{0x54}, 16)}); err != nil {
		t.Fatal(err)
	}
	if err := client.SealAccount(ctx, account.AccountID, account.DeviceToken); err != nil {
		t.Fatal(err)
	}
	bundle := desktopagent.CredentialBundleV1{Version: desktopagent.CredentialBundleVersion, DeviceToken: []byte(account.DeviceToken), CurrentEpoch: 1, EpochKeys: make(map[uint64][32]byte), VerificationKeys: make(map[[16]byte][32]byte), X25519PublicKeys: make(map[[16]byte][32]byte)}
	copy(bundle.AccountID[:], account.AccountID)
	copy(bundle.DeviceID[:], account.DeviceID)
	copy(bundle.SigningSeed[:], keys.Ed25519Private.Seed())
	copy(bundle.X25519Private[:], keys.X25519Private)
	copy(bundle.LocalDataKey[:], bytes.Repeat([]byte{0x55}, 32))
	copy(bundle.ObjectIDKey[:], bytes.Repeat([]byte{0x56}, 32))
	var epoch, ed, x [32]byte
	copy(epoch[:], bytes.Repeat([]byte{0x57}, 32))
	copy(ed[:], keys.Ed25519Public)
	copy(x[:], keys.X25519Public)
	bundle.EpochKeys[1], bundle.VerificationKeys[bundle.DeviceID], bundle.X25519PublicKeys[bundle.DeviceID] = epoch, ed, x
	defer bundle.Zero()
	encoded, err := desktopagent.EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	secrets.values["default"] = encoded
	store, err := localstore.OpenForDevice(ctx, operations.defaults.DatabasePath, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
