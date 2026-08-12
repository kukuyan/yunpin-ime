// SPDX-License-Identifier: Apache-2.0
package syncclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/protocol"
)

type dropFirstResponse struct {
	base http.RoundTripper
	drop bool
}

func (transport *dropFirstResponse) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil || !transport.drop {
		return response, err
	}
	transport.drop = false
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return nil, errors.New("synthetic lost provisioning response")
}

func TestDeleteAccountUsesAuthenticatedExactPath(t *testing.T) {
	accountID := bytes.Repeat([]byte{0x31}, 16)
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete || request.URL.Path != "/v1/accounts/31313131313131313131313131313131" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer rollback-token" {
			t.Fatalf("unexpected authorization header %q", got)
		}
		if request.ContentLength != 0 {
			t.Fatalf("rollback unexpectedly sent a body: %d bytes", request.ContentLength)
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer relay.Close()
	endpoint, err := ParseEndpoint(relay.URL, EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := New(endpoint).DeleteAccount(context.Background(), accountID, "rollback-token"); err != nil {
		t.Fatal(err)
	}
}

func TestPairingAndRosterClientRoundTrip(t *testing.T) {
	pairingID := bytes.Repeat([]byte{0x41}, 16)
	pairingSecret := bytes.Repeat([]byte{0x42}, protocol.PairingSecretSize)
	accountID := bytes.Repeat([]byte{0x51}, 16)
	creatorDeviceID := bytes.Repeat([]byte{0x50}, 16)
	joiningDeviceID := bytes.Repeat([]byte{0x52}, 16)
	creatorKey := bytes.Repeat([]byte{0x43}, 32)
	creatorEd := ed25519.PublicKey(bytes.Repeat([]byte{0x40}, ed25519.PublicKeySize))
	pendingPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x44}, ed25519.SeedSize))
	pendingEd := pendingPrivate.Public().(ed25519.PublicKey)
	pendingX := bytes.Repeat([]byte{0x45}, 32)
	deviceName := bytes.Repeat([]byte{0x46}, 24)
	joinedToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x49}, 32))
	nonce := bytes.Repeat([]byte{0x47}, 24)
	box := protocol.SealedBox{Nonce: nonce, Ciphertext: bytes.Repeat([]byte{0x48}, 32)}
	encodedBox, err := protocol.EncodeSealedBox(box)
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	encode := base64.RawURLEncoding.EncodeToString
	creatorToken := encode(bytes.Repeat([]byte{0x4a}, 32))
	creator := Account{AccountID: accountID, DeviceID: creatorDeviceID, DeviceToken: creatorToken}
	joining := Account{AccountID: accountID, DeviceID: joiningDeviceID, DeviceToken: joinedToken,
		DeviceRollbackToken: encode(bytes.Repeat([]byte{0x4b}, 32))}
	invitation := PairingInvitation{
		PairingID: pairingID, PairingSecret: pairingSecret, AccountID: accountID,
		CreatorDeviceID: creatorDeviceID, CreatorEd25519PublicKey: creatorEd, CreatorX25519PublicKey: creatorKey,
	}
	transcript, err := PairingTranscript(invitation, joining, DeviceRegistration{
		DeviceNameCiphertext: deviceName, Ed25519PublicKey: pendingEd, X25519PublicKey: pendingX,
	})
	if err != nil {
		t.Fatal(err)
	}
	joinProof, err := protocol.PairingJoinProof(pairingSecret, transcript)
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "POST /v1/pairings":
			if request.Header.Get("Authorization") != "Bearer "+creatorToken {
				t.Fatal("creator token missing")
			}
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]any{
				"pairing_id": "41414141414141414141414141414141", "expires_at": expires,
			})
		case "PUT /v1/pairings/41414141414141414141414141414141":
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "joined"})
		case "GET /v1/pairings/41414141414141414141414141414141":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"pairing_id": "41414141414141414141414141414141", "state": "joined", "expires_at": expires, "expired": false,
				"device_id": "52525252525252525252525252525252", "join_proof": encode(joinProof),
				"device_name_ciphertext": encode(deviceName), "ed25519_public_key": encode(pendingEd), "x25519_public_key": encode(pendingX),
			})
		case "POST /v1/pairings/41414141414141414141414141414141/approve":
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "approved"})
		case "POST /v1/pairings/41414141414141414141414141414141/claim":
			response.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(response).Encode(map[string]string{
				"account_id": "51515151515151515151515151515151", "device_id": "52525252525252525252525252525252",
				"device_token": joinedToken, "encrypted_keyring": encodedBox,
			})
		case "GET /v1/devices":
			_ = json.NewEncoder(response).Encode(map[string]any{"devices": []map[string]any{
				{"id": "51515151515151515151515151515151", "name_ciphertext": encode(deviceName),
					"ed25519_public_key": encode(pendingEd), "x25519_public_key": encode(pendingX),
					"created_at": expires, "current": true, "revoked": false},
			}})
		default:
			http.NotFound(response, request)
		}
	}))
	defer relay.Close()
	endpoint, err := ParseEndpoint(relay.URL, EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	client := New(endpoint)
	invitation, err = client.CreatePairing(context.Background(), creator, invitation)
	if err != nil || !bytes.Equal(invitation.PairingID, pairingID) || !bytes.Equal(invitation.PairingSecret, pairingSecret) {
		t.Fatalf("invitation=%#v err=%v", invitation, err)
	}
	if _, err := client.CreatePairing(context.Background(), creator, invitation); err != nil {
		t.Fatalf("exact create retry: %v", err)
	}
	registration := DeviceRegistration{DeviceNameCiphertext: deviceName, Ed25519PublicKey: pendingEd, X25519PublicKey: pendingX}
	if _, err := client.JoinPairing(context.Background(), invitation, joining, registration); err != nil {
		t.Fatal(err)
	}
	if _, err := client.JoinPairing(context.Background(), invitation, joining, registration); err != nil {
		t.Fatalf("exact join retry: %v", err)
	}
	status, err := client.GetPairing(context.Background(), invitation, creator)
	if err != nil || status.State != "joined" || !bytes.Equal(status.Ed25519PublicKey, pendingEd) {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	if err := client.ApprovePairing(context.Background(), pairingID, creatorToken, box); err != nil {
		t.Fatal(err)
	}
	if err := client.ApprovePairing(context.Background(), pairingID, creatorToken, box); err != nil {
		t.Fatalf("exact approval retry: %v", err)
	}
	claim, err := client.ClaimPairing(context.Background(), invitation, joining, transcript, pendingPrivate)
	if err != nil || claim.DeviceToken != joinedToken || !bytes.Equal(claim.EncryptedKeyring.Nonce, nonce) {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	claim, err = client.ClaimPairing(context.Background(), invitation, joining, transcript, pendingPrivate)
	if err != nil || claim.DeviceToken != joinedToken || !bytes.Equal(claim.EncryptedKeyring.Nonce, nonce) {
		t.Fatalf("exact claim retry=%#v err=%v", claim, err)
	}
	devices, err := client.ListDevices(context.Background(), joinedToken)
	if err != nil || len(devices) != 1 || !devices[0].Current || !bytes.Equal(devices[0].Ed25519PublicKey, pendingEd) {
		t.Fatalf("devices=%#v err=%v", devices, err)
	}
}

func TestJoinPairingRejectsLaterStateResponse(t *testing.T) {
	accountID := bytes.Repeat([]byte{0x31}, 16)
	creatorDeviceID := bytes.Repeat([]byte{0x32}, 16)
	joiningDeviceID := bytes.Repeat([]byte{0x33}, 16)
	invitation := PairingInvitation{
		PairingID: bytes.Repeat([]byte{0x38}, 16), PairingSecret: bytes.Repeat([]byte{0x39}, 32),
		AccountID: accountID, CreatorDeviceID: creatorDeviceID,
		CreatorEd25519PublicKey: bytes.Repeat([]byte{0x34}, 32),
		CreatorX25519PublicKey:  bytes.Repeat([]byte{0x35}, 32),
	}
	joining := Account{AccountID: accountID, DeviceID: joiningDeviceID,
		DeviceToken:         base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x40}, 32)),
		DeviceRollbackToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))}
	registration := DeviceRegistration{DeviceNameCiphertext: bytes.Repeat([]byte{0x42}, 24),
		Ed25519PublicKey: bytes.Repeat([]byte{0x36}, 32), X25519PublicKey: bytes.Repeat([]byte{0x37}, 32)}
	for _, invalidState := range []string{"approved", "claimed", "ready", "finalized"} {
		t.Run(invalidState, func(t *testing.T) {
			relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(response).Encode(map[string]string{"state": invalidState})
			}))
			defer relay.Close()
			endpoint, err := ParseEndpoint(relay.URL, EndpointPolicy{AllowPrivateHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := New(endpoint).JoinPairing(context.Background(), invitation, joining, registration); err == nil ||
				!strings.Contains(err.Error(), "invalid pairing state") {
				t.Fatalf("relay state %q was accepted: %v", invalidState, err)
			}
		})
	}
}

func TestDeleteCurrentDeviceUsesDedicatedAuthenticatedPath(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete || request.URL.Path != "/v1/devices/current" ||
			request.URL.RawQuery != "account_id=31313131313131313131313131313131&device_id=32323232323232323232323232323232&pairing_id=33333333333333333333333333333333" ||
			request.Header.Get("Authorization") != "Bearer rollback-token" {
			t.Fatalf("unexpected rollback request %s %s", request.Method, request.URL.Path)
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer relay.Close()
	endpoint, err := ParseEndpoint(relay.URL, EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := New(endpoint).DeleteCurrentDevice(context.Background(), bytes.Repeat([]byte{0x31}, 16),
		bytes.Repeat([]byte{0x32}, 16), bytes.Repeat([]byte{0x33}, 16), "rollback-token"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteAccountRejectsMissingCredentials(t *testing.T) {
	endpoint, err := ParseEndpoint("http://127.0.0.1:1", EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	client := New(endpoint)
	if err := client.DeleteAccount(context.Background(), nil, "token"); err == nil {
		t.Fatal("missing account ID was accepted")
	}
	if err := client.DeleteAccount(context.Background(), bytes.Repeat([]byte{1}, 16), ""); err == nil {
		t.Fatal("missing device token was accepted")
	}
}

func TestPairingCommitLifecycleRoutesAndExactRetries(t *testing.T) {
	pairingID := bytes.Repeat([]byte{0x61}, 16)
	const pairingPath = "/v1/pairings/61616161616161616161616161616161"
	const creatorToken = "creator-token"
	const joiningToken = "joining-token"
	readyCalls, finalizeCalls, cancelCalls := 0, 0, 0
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "POST " + pairingPath + "/ready":
			readyCalls++
			if got := request.Header.Get("Authorization"); got != "Bearer "+joiningToken {
				t.Errorf("ready used wrong authorization header %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body) != 0 {
				t.Errorf("ready body=%v err=%v, want empty JSON object", body, err)
			}
			state := "ready"
			if readyCalls > 1 {
				state = "finalized"
			}
			_ = json.NewEncoder(response).Encode(map[string]string{"state": state})
		case "POST " + pairingPath + "/finalize":
			finalizeCalls++
			if got := request.Header.Get("Authorization"); got != "Bearer "+creatorToken {
				t.Errorf("finalize used wrong authorization header %q", got)
			}
			_ = json.NewEncoder(response).Encode(map[string]string{"state": "finalized"})
		case "DELETE " + pairingPath:
			cancelCalls++
			if got := request.Header.Get("Authorization"); got != "Bearer "+creatorToken {
				t.Errorf("cancel used wrong authorization header %q", got)
			}
			if request.Body != nil && request.ContentLength != 0 {
				t.Errorf("cancel unexpectedly sent a body of %d bytes", request.ContentLength)
			}
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer relay.Close()
	endpoint, err := ParseEndpoint(relay.URL, EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	client := New(endpoint)
	state, err := client.ReadyPairing(context.Background(), pairingID, joiningToken)
	if err != nil || state != "ready" {
		t.Fatalf("first ready state=%q err=%v", state, err)
	}
	state, err = client.ReadyPairing(context.Background(), pairingID, joiningToken)
	if err != nil || state != "finalized" {
		t.Fatalf("ready replay state=%q err=%v", state, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := client.FinalizePairing(context.Background(), pairingID, creatorToken); err != nil {
			t.Fatalf("finalize attempt %d: %v", attempt+1, err)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := client.CancelPairing(context.Background(), pairingID, creatorToken); err != nil {
			t.Fatalf("cancel attempt %d: %v", attempt+1, err)
		}
	}
	if readyCalls != 2 || finalizeCalls != 2 || cancelCalls != 2 {
		t.Fatalf("calls ready=%d finalize=%d cancel=%d", readyCalls, finalizeCalls, cancelCalls)
	}
}

func TestGetPairingAcceptsReadyAndFinalizedStatus(t *testing.T) {
	pairingID := bytes.Repeat([]byte{0x62}, 16)
	expiresAt := time.Date(2030, 2, 3, 4, 5, 6, 0, time.UTC)
	claimExpiresAt := expiresAt.Add(24 * time.Hour)
	readyExpiresAt := claimExpiresAt.Add(24 * time.Hour)
	calls := 0
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/pairings/62626262626262626262626262626262" {
			http.NotFound(response, request)
			return
		}
		calls++
		state := "ready"
		if calls > 1 {
			state = "finalized"
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"pairing_id": "62626262626262626262626262626262", "state": state,
			"expires_at": expiresAt, "claim_expires_at": claimExpiresAt,
			"ready_expires_at": readyExpiresAt, "expired": true,
		})
	}))
	defer relay.Close()
	endpoint, err := ParseEndpoint(relay.URL, EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	invitation := PairingInvitation{
		PairingID: pairingID, PairingSecret: bytes.Repeat([]byte{0x63}, protocol.PairingSecretSize),
		AccountID: bytes.Repeat([]byte{0x64}, 16), CreatorDeviceID: bytes.Repeat([]byte{0x65}, 16),
		CreatorEd25519PublicKey: ed25519.PublicKey(bytes.Repeat([]byte{0x66}, ed25519.PublicKeySize)),
		CreatorX25519PublicKey:  bytes.Repeat([]byte{0x67}, 32),
	}
	creator := Account{AccountID: invitation.AccountID, DeviceID: invitation.CreatorDeviceID, DeviceToken: "creator-token"}
	for _, wantState := range []string{"ready", "finalized"} {
		status, err := New(endpoint).GetPairing(context.Background(), invitation, creator)
		if err != nil || status.State != wantState || !status.ClaimExpiresAt.Equal(claimExpiresAt) ||
			!status.ReadyExpiresAt.Equal(readyExpiresAt) || !status.Expired {
			t.Fatalf("status=%#v err=%v, want state=%s and both later deadlines", status, err, wantState)
		}
	}
}

func TestPairingLifecyclePreservesStableRelayErrors(t *testing.T) {
	pairingID := bytes.Repeat([]byte{0x68}, 16)
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusConflict)
		code := "pairing_cancel_not_safe"
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/pairings/68686868686868686868686868686868/ready":
			code = "pairing_ready_window_expired"
		case request.Method == http.MethodPost && request.URL.Path == "/v1/pairings/68686868686868686868686868686868/finalize":
			code = "pairing_not_ready_to_finalize"
		case request.Method == http.MethodGet && request.URL.Path == "/v1/devices":
			code = "pairing_finalization_pending"
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"error": code})
	}))
	defer relay.Close()
	endpoint, err := ParseEndpoint(relay.URL, EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	client := New(endpoint)
	assertAPIError := func(label string, err error, code string) {
		t.Helper()
		var apiError *APIError
		if !errors.As(err, &apiError) || apiError.Status != http.StatusConflict || apiError.Code != code {
			t.Fatalf("%s error=%v, want HTTP 409 %s", label, err, code)
		}
	}
	_, err = client.ReadyPairing(context.Background(), pairingID, "joining-token")
	assertAPIError("ready", err, "pairing_ready_window_expired")
	assertAPIError("finalize", client.FinalizePairing(context.Background(), pairingID, "creator-token"), "pairing_not_ready_to_finalize")
	assertAPIError("cancel", client.CancelPairing(context.Background(), pairingID, "creator-token"), "pairing_cancel_not_safe")
	_, err = client.ListDevices(context.Background(), "joining-token")
	assertAPIError("ordinary API quarantine", err, "pairing_finalization_pending")
}
