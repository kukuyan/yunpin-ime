// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
	"golang.org/x/crypto/curve25519"
)

type onboardingTestSecrets struct {
	values      map[string][]byte
	interactive int
	background  int
}

func (store *onboardingTestSecrets) read(profile string) ([]byte, error) {
	value, ok := store.values[profile]
	if !ok {
		return nil, desktopagent.ErrSecretNotFound
	}
	return append([]byte(nil), value...), nil
}
func (store *onboardingTestSecrets) Load(_ context.Context, profile string) ([]byte, error) {
	store.interactive++
	return store.read(profile)
}
func (store *onboardingTestSecrets) LoadWithoutUserInteraction(_ context.Context, profile string) ([]byte, error) {
	store.background++
	return store.read(profile)
}
func (store *onboardingTestSecrets) Save(_ context.Context, profile string, value []byte) error {
	store.values[profile] = append([]byte(nil), value...)
	return nil
}
func (store *onboardingTestSecrets) Delete(_ context.Context, profile string) error {
	delete(store.values, profile)
	return nil
}

func onboardingTestOperations(t *testing.T) (*localSettingsOperations, *onboardingTestSecrets) {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "Sync")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := desktopagent.Paths{StateDirectory: state, EndpointConfigPath: filepath.Join(state, "endpoint.json"), LockPath: filepath.Join(state, "agent.lock"), DatabasePath: filepath.Join(state, "state.db"), BaselinePath: filepath.Join(root, "Rime", "yunpin", "baseline.tsv")}
	secrets := &onboardingTestSecrets{values: make(map[string][]byte)}
	return &localSettingsOperations{defaults: paths, agent: desktopagent.Agent{Profile: "default", Secrets: secrets}}, secrets
}

func onboardingTestPairedBundle(t *testing.T, secrets *onboardingTestSecrets) desktopagent.CredentialBundleV1 {
	t.Helper()
	fill16 := func(value byte) (result [16]byte) { copy(result[:], bytes.Repeat([]byte{value}, 16)); return }
	fill32 := func(value byte) (result [32]byte) { copy(result[:], bytes.Repeat([]byte{value}, 32)); return }
	account, self, second := fill16(0x11), fill16(0x22), fill16(0x23)
	seed, xPrivate := fill32(0x33), fill32(0x44)
	private := ed25519.NewKeyFromSeed(seed[:])
	var ed, x [32]byte
	copy(ed[:], private.Public().(ed25519.PublicKey))
	xBytes, _ := curve25519.X25519(xPrivate[:], curve25519.Basepoint)
	copy(x[:], xBytes)
	secondEd, secondX := fill32(0x45), fill32(0x46)
	roster, err := protocol.SignPairingRoster(account[:], 1, []protocol.PairingRosterDevice{
		{DeviceID: self[:], Ed25519PublicKey: ed[:], X25519PublicKey: x[:]},
		{DeviceID: second[:], Ed25519PublicKey: secondEd[:], X25519PublicKey: secondX[:]},
	}, self[:], private)
	if err != nil {
		t.Fatal(err)
	}
	bundle := desktopagent.CredentialBundleV1{Version: desktopagent.CredentialBundleVersion, AccountID: account, DeviceID: self, DeviceToken: []byte("synthetic_existing_device_bearer"), SigningSeed: seed, X25519Private: xPrivate, LocalDataKey: fill32(0x55), ObjectIDKey: fill32(0x66), CurrentEpoch: 1, EpochKeys: map[uint64][32]byte{1: fill32(0x77)}, VerificationKeys: map[[16]byte][32]byte{self: ed, second: secondEd}, X25519PublicKeys: map[[16]byte][32]byte{self: x, second: secondX}, TrustedRoster: roster}
	encoded, err := desktopagent.EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	secrets.values["default"] = encoded
	return bundle
}

func expectOnboardingCode(t *testing.T, err error, code string) {
	t.Helper()
	var safe *settingsOnboardingError
	if !errors.As(err, &safe) || safe.Code != code || err.Error() != code {
		t.Fatalf("expected fixed code %s, got %v", code, err)
	}
}

func TestOnboardingServerCheckAnonymousAndStatusNonInteractive(t *testing.T) {
	operations, secrets := onboardingTestOperations(t)
	bundle := onboardingTestPairedBundle(t, secrets)
	defer bundle.Zero()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/healthz" || r.Header.Get("Authorization") != "" {
			t.Error("connection check sent a credential or non-health request")
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()
	result, err := operations.ApplyOnboarding(context.Background(), settingsOnboardingAction{Kind: "server-check", Server: server.URL, AllowPrivateHTTP: true})
	if err != nil || calls != 1 || !result.State.Paired || secrets.interactive != 0 || secrets.background == 0 {
		t.Fatalf("anonymous check/state failed: calls=%d state=%+v err=%v", calls, result.State, err)
	}
	if _, err := os.Stat(operations.defaults.EndpointConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("connection check saved an endpoint")
	}
	_, err = operations.ApplyOnboarding(context.Background(), settingsOnboardingAction{Kind: "server-check", Server: "https://private:password@invalid.test/?secret=hidden"})
	expectOnboardingCode(t, err, "server-invalid")
	if strings.Contains(err.Error(), "password") || calls != 1 {
		t.Fatal("invalid address was exposed or contacted")
	}
}

func TestOnboardingConnectionCheckRejectsRedirectsAndUnboundedHealth(t *testing.T) {
	for _, scenario := range []string{"redirect", "oversized", "wrong_service"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch scenario {
				case "redirect":
					http.Redirect(w, r, "/other", http.StatusFound)
				case "oversized":
					_, _ = w.Write([]byte(strings.Repeat(" ", 4097)))
				default:
					_, _ = w.Write([]byte(`{"status":"not-yunpin-health"}`))
				}
			}))
			defer server.Close()
			endpoint, _ := syncclient.ParseEndpoint(server.URL, syncclient.EndpointPolicy{AllowPrivateHTTP: true})
			expectOnboardingCode(t, checkOnboardingServer(context.Background(), endpoint), "server-unreachable")
			if calls != 1 {
				t.Fatal("health checker followed a redirect")
			}
		})
	}
}

func TestOnboardingServerCorrectionRequiresConfirmationThenExistingTrust(t *testing.T) {
	for _, scenario := range []string{"unconfirmed", "wrong_identity", "verified"} {
		t.Run(scenario, func(t *testing.T) {
			operations, secrets := onboardingTestOperations(t)
			bundle := onboardingTestPairedBundle(t, secrets)
			defer bundle.Zero()
			beforeSecret := append([]byte(nil), secrets.values["default"]...)
			if err := desktopagent.ConfigureEndpoint(operations.defaults.EndpointConfigPath, "https://original.test", false); err != nil {
				t.Fatal(err)
			}
			beforeConfig, _ := os.ReadFile(operations.defaults.EndpointConfigPath)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/v1/devices":
					if r.Header.Get("Authorization") != "Bearer "+string(bundle.DeviceToken) {
						t.Error("confirmed verification omitted existing device identity")
					}
					ed, x := bundle.VerificationKeys[bundle.DeviceID], bundle.X25519PublicKeys[bundle.DeviceID]
					deviceID := append([]byte(nil), bundle.DeviceID[:]...)
					if scenario == "wrong_identity" {
						deviceID[0] ^= 1
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"devices": []any{map[string]any{"id": hex.EncodeToString(deviceID), "name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), "ed25519_public_key": base64.RawURLEncoding.EncodeToString(ed[:]), "x25519_public_key": base64.RawURLEncoding.EncodeToString(x[:]), "created_at": time.Now().UTC(), "current": true, "revoked": false}}})
				case "/v1/roster":
					digest, _ := protocol.PairingRosterDigest(bundle.TrustedRoster)
					_ = json.NewEncoder(w).Encode(syncclient.RosterUpdates{HeadVersion: 1, HeadHash: hex.EncodeToString(digest)})
				case "/healthz":
					if r.Header.Get("Authorization") != "" {
						t.Error("health request leaked a bearer")
					}
					_, _ = w.Write([]byte(`{"status":"ok"}`))
				default:
					t.Error("unexpected write or network route")
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			_, err := operations.ApplyOnboarding(context.Background(), settingsOnboardingAction{Kind: "server-save", Server: server.URL, AllowPrivateHTTP: true, ConfirmServerChange: scenario != "unconfirmed"})
			switch scenario {
			case "unconfirmed":
				expectOnboardingCode(t, err, "server-confirm-required")
				if calls != 0 {
					t.Fatal("unconfirmed address received existing identity")
				}
			case "wrong_identity":
				expectOnboardingCode(t, err, "server-identity-mismatch")
			case "verified":
				if err != nil {
					t.Fatal(err)
				}
				got, _ := syncclient.LoadEndpointConfig(operations.defaults.EndpointConfigPath)
				if got.String() != server.URL {
					t.Fatal("verified correction was not saved")
				}
			}
			if scenario != "verified" {
				after, _ := os.ReadFile(operations.defaults.EndpointConfigPath)
				if !bytes.Equal(beforeConfig, after) {
					t.Fatal("rejected correction changed the original endpoint")
				}
			}
			if !bytes.Equal(beforeSecret, secrets.values["default"]) {
				t.Fatal("address correction rewrote device credentials")
			}
		})
	}
}

func TestOnboardingLoginReusesExistingEndpointBoundSession(t *testing.T) {
	operations, secrets := onboardingTestOperations(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/auth/login" || r.Method != http.MethodPost {
			t.Error("unexpected account operation")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"username": "alice", "token": strings.Repeat("A", 43), "expires_at": time.Now().Add(time.Hour).UTC()})
	}))
	defer server.Close()
	if err := desktopagent.ConfigureEndpoint(operations.defaults.EndpointConfigPath, server.URL, true); err != nil {
		t.Fatal(err)
	}
	for _, action := range []settingsOnboardingAction{{Kind: "login", Username: "alice", Password: "synthetic-password"}, {Kind: "login"}, {Kind: "register", Username: "alice"}} {
		result, err := operations.ApplyOnboarding(context.Background(), action)
		if err != nil || !result.State.SessionValid || result.State.Username != "alice" {
			t.Fatalf("session reuse failed: state=%+v err=%v", result.State, err)
		}
	}
	if calls != 1 || len(secrets.values) != 1 {
		t.Fatal("existing login was repeated or a sync account was invented")
	}
	if _, err := desktopagent.LoadUserSession(context.Background(), secrets, "default", "https://another.test"); !errors.Is(err, desktopagent.ErrUserLoginRequired) {
		t.Fatal("session silently crossed server boundary")
	}
}

func TestOnboardingErrorsAndSerializedResultsExcludeSecrets(t *testing.T) {
	for _, err := range []error{errors.New("private-invitation-and-password"), &syncclient.APIError{Status: 403, Code: "private-device-token"}} {
		got := classifyOnboardingError(err, "pairing-failed")
		expectOnboardingCode(t, got, "pairing-failed")
		if errors.Unwrap(got) != nil {
			t.Fatal("UI error retains an untrusted underlying error")
		}
	}
	encoded, err := json.Marshal(settingsOnboardingResult{Invitation: "synthetic-private-invitation", Notice: "配对进度已保存。"})
	if err != nil || bytes.Contains(encoded, []byte("synthetic-private-invitation")) {
		t.Fatal("invitation entered generic result serialization")
	}
}

func TestOnboardingJoiningContinuationReusesProtectedTransactionWithoutLogin(t *testing.T) {
	operations, secrets := onboardingTestOperations(t)
	_, creatorSecrets := onboardingTestOperations(t)
	bundle := onboardingTestPairedBundle(t, creatorSecrets)
	defer bundle.Zero()
	ed, x := bundle.VerificationKeys[bundle.DeviceID], bundle.X25519PublicKeys[bundle.DeviceID]
	invitation, err := desktopagent.EncodePairingInvitation(syncclient.PairingInvitation{
		PairingID: bytes.Repeat([]byte{0x40}, 16), PairingSecret: bytes.Repeat([]byte{0x41}, 32),
		AccountID: bundle.AccountID[:], CreatorDeviceID: bundle.DeviceID[:],
		CreatorEd25519PublicKey: ed[:], CreatorX25519PublicKey: x[:], ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	joins, claims := 0, 0
	var originalJoin []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("joining operation added an unnecessary login/device bearer")
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/pairings/"+strings.Repeat("40", 16):
			joins++
			body, _ := io.ReadAll(r.Body)
			if joins == 1 {
				originalJoin = body
			} else if !bytes.Equal(body, originalJoin) {
				t.Error("continued join replaced its protected device/key/rollback identity")
			}
			_, _ = w.Write([]byte(`{"state":"joined"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/claim"):
			claims++
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"pairing_not_ready"}`))
		default:
			t.Error("joining continuation invoked an unexpected route")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	if err := desktopagent.ConfigureEndpoint(operations.defaults.EndpointConfigPath, server.URL, true); err != nil {
		t.Fatal(err)
	}
	joined, err := operations.ApplyOnboarding(context.Background(), settingsOnboardingAction{Kind: "pairing-join", Invitation: invitation})
	if err != nil || joined.State.PairingRole != "joiner" || joined.State.PairingState != "joined" || joined.State.Paired || joined.State.SessionValid || joined.Invitation != "" {
		t.Fatalf("initial join state=%+v err=%v", joined.State, err)
	}
	before := append([]byte(nil), secrets.values["default.pairing-join"]...)
	_, err = operations.ApplyOnboarding(context.Background(), settingsOnboardingAction{Kind: "pairing-continue"})
	expectOnboardingCode(t, err, "pairing-waiting")
	if joins != 2 || claims != 1 || !bytes.Equal(before, secrets.values["default.pairing-join"]) || len(secrets.values) != 1 {
		t.Fatal("waiting continuation changed/discarded its original protected transaction")
	}
	if _, err := os.Stat(operations.defaults.DatabasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("waiting for approval created an active database")
	}
}
