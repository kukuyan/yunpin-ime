// SPDX-License-Identifier: Apache-2.0
package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kukuyan/yunpin-ime/protocol"
	syncserver "github.com/kukuyan/yunpin-ime/sync/server"
)

type deterministicReader struct{ next byte }

func (reader *deterministicReader) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = reader.next
		reader.next++
	}
	return len(target), nil
}

type integrationPayload struct {
	Phrase string `cbor:"1,keyasint"`
	Count  uint64 `cbor:"2,keyasint"`
}

func request(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&encoded).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, &encoded)
	r.RemoteAddr = "192.0.2.10:4321"
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestProtocolEnvelopeIsAcceptedAndReturnedBySyncHandler(t *testing.T) {
	application, err := syncserver.New(context.Background(), filepath.Join(t.TempDir(), "sync.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x51}, ed25519.SeedSize))
	deviceToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x64}, 32))
	created := request(t, application, http.MethodPost, "/v1/accounts", "", map[string]any{
		"account_id":              hex.EncodeToString(bytes.Repeat([]byte{0x65}, 16)),
		"device_id":               hex.EncodeToString(bytes.Repeat([]byte{0x66}, 16)),
		"device_token":            deviceToken,
		"rollback_token":          base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x67}, 32)),
		"recovery_authentication": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32)),
		"device_name_ciphertext":  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, 32)),
		"ed25519_public_key":      base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey)),
		"x25519_public_key":       base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x63}, 32)),
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("account creation status=%d body=%s", created.Code, created.Body.String())
	}
	var account struct {
		AccountID   string `json:"account_id"`
		DeviceID    string `json:"device_id"`
		DeviceToken string `json:"device_token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	const sealedBoxWire = "WVBCWAEAAAAQERERERERERERERERERERERERERERERERIiIiIiIiIiIiIiIiIiIiIg"
	keyring := request(t, application, http.MethodPut, "/v1/keyring", account.DeviceToken, map[string]any{
		"epoch": 1, "ciphertext": sealedBoxWire,
	})
	if keyring.Code != http.StatusOK {
		t.Fatalf("keyring status=%d body=%s", keyring.Code, keyring.Body.String())
	}
	sealed := request(t, application, http.MethodPost, "/v1/accounts/"+account.AccountID+"/seal", account.DeviceToken, map[string]any{})
	if sealed.Code != http.StatusNoContent {
		t.Fatalf("seal status=%d body=%s", sealed.Code, sealed.Body.String())
	}
	accountID, err := hex.DecodeString(account.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	deviceID, err := hex.DecodeString(account.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	epochKey := bytes.Repeat([]byte{0x71}, 32)
	want := integrationPayload{Phrase: "合成跨模块协议测试", Count: 2}
	envelope, err := protocol.Seal(epochKey, protocol.Header{
		AccountID: accountID, ObjectID: bytes.Repeat([]byte{0x72}, 16), KeyEpoch: 1,
		DeviceID: deviceID, DeviceSeq: 1,
	}, want, private, &deterministicReader{next: 1})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := envelope.ToWire()
	if err != nil {
		t.Fatal(err)
	}
	synced := request(t, application, http.MethodPost, "/v1/sync", account.DeviceToken, map[string]any{
		"cursor": 0, "ack_cursor": 0, "envelopes": []protocol.WireEnvelope{wire},
	})
	if synced.Code != http.StatusOK {
		t.Fatalf("protocol wire rejected by sync handler: status=%d body=%s", synced.Code, synced.Body.String())
	}
	var response struct {
		Accepted []uint64                `json:"accepted_sequences"`
		Records  []protocol.WireEnvelope `json:"envelopes"`
	}
	if err := json.Unmarshal(synced.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(response.Accepted, []uint64{1}) || len(response.Records) != 1 {
		t.Fatalf("unexpected sync response: %+v", response)
	}
	if response.Records[0].DeviceID != account.DeviceID {
		t.Fatalf("sync response omitted source device ID: %#v", response.Records[0])
	}
	restored, err := protocol.EnvelopeFromDownload(accountID, response.Records[0])
	if err != nil {
		t.Fatal(err)
	}
	var got integrationPayload
	if err := protocol.Open(epochKey, restored, private.Public().(ed25519.PublicKey), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("downloaded payload mismatch: got=%#v want=%#v", got, want)
	}
}
