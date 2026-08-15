// SPDX-License-Identifier: Apache-2.0

package desktopagent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/syncclient"
)

func TestLoginSessionIsSecretProtectedAndEndpointBound(t *testing.T) {
	store := &memorySecretStore{values: make(map[string][]byte)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/login" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["password"] != "a-long-enough-password" {
			t.Fatalf("unexpected login body: %#v err=%v", body, err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"username": "alice", "token": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"expires_at": time.Now().Add(time.Hour).UTC(),
		})
	}))
	defer server.Close()
	endpoint, err := syncclient.ParseEndpoint(server.URL, syncclient.EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := LoginUser(context.Background(), syncclient.New(endpoint), store, "default", endpoint.String(), "alice", "a-long-enough-password")
	if err != nil || result.Username != "alice" {
		t.Fatalf("login result=%#v err=%v", result, err)
	}
	session, err := LoadUserSession(context.Background(), store, "default", endpoint.String())
	if err != nil || session.Username != "alice" || session.Token == "" {
		t.Fatalf("stored session=%#v err=%v", session, err)
	}
	if _, err := LoadUserSession(context.Background(), store, "default", "https://other.invalid"); !errors.Is(err, ErrUserLoginRequired) {
		t.Fatalf("session crossed endpoint boundary: %v", err)
	}
}

func TestMalformedOrExpiredUserSessionFailsClosed(t *testing.T) {
	store := &memorySecretStore{values: make(map[string][]byte)}
	if err := store.Save(context.Background(), "default.user-session", []byte(`{"version":1,"endpoint":"https://sync.invalid","username":"alice","token":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","expires_at_unix_ms":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUserSession(context.Background(), store, "default", "https://sync.invalid"); !errors.Is(err, ErrUserLoginRequired) {
		t.Fatalf("expired session was accepted: %v", err)
	}
	if err := store.Save(context.Background(), "default.user-session", []byte(`{"version":1,"endpoint":"https://sync.invalid","unknown":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUserSession(context.Background(), store, "default", "https://sync.invalid"); !errors.Is(err, ErrUserLoginRequired) {
		t.Fatalf("unknown-field session was accepted: %v", err)
	}
}

func TestClaimCurrentAccountDecodesStoredCanonicalDeviceToken(t *testing.T) {
	store := &memorySecretStore{values: make(map[string][]byte)}
	bundle := testCredentials()
	bundle.DeviceToken = []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x7e}, 32)))
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), "default", encoded); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), "default.user-session", []byte(`{"version":1,"endpoint":"https://sync.invalid","username":"alice","token":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","expires_at_unix_ms":4102444800000}`)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts/"+hex.EncodeToString(bundle.AccountID[:])+"/claim" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			DeviceID string `json:"device_id"`
			DeviceToken string `json:"device_token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.DeviceID != hex.EncodeToString(bundle.DeviceID[:]) || body.DeviceToken != string(bundle.DeviceToken) {
			t.Fatalf("claim did not preserve canonical device capability")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	endpoint, err := syncclient.ParseEndpoint(server.URL, syncclient.EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := ClaimCurrentAccount(context.Background(), syncclient.New(endpoint, syncclient.WithUserSession("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")), store, "default", "https://sync.invalid"); err != nil {
		t.Fatalf("claim current account: %v", err)
	}
}
