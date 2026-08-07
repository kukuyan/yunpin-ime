// SPDX-License-Identifier: Apache-2.0

package server

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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sealedBoxWireGolden = "WVBCWAEAAAAQERERERERERERERERERERERERERERERERIiIiIiIiIiIiIiIiIiIiIg"

type testDevice struct {
	accountID              string
	deviceID               string
	token                  string
	recoveryAuthentication string
	publicKey              ed25519.PublicKey
	privateKey             ed25519.PrivateKey
	x25519Key              []byte
	databasePath           string
	server                 *Server
	logBuffer              *bytes.Buffer
}

func TestSyncIsIdempotentAndContentBlind(t *testing.T) {
	device := newTestAccount(t)
	defer device.server.Close()
	var journalMode string
	if err := device.server.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil || strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode=%q err=%v", journalMode, err)
	}

	personalPhrase := "仅存在于客户端的测试短语"
	digestBytes := sha256.Sum256([]byte(personalPhrase + "-encrypted-client-side"))
	ciphertext := bytes.Repeat([]byte{0x7a}, paddingBucket+16)
	copy(ciphertext, digestBytes[:])
	envelope, _ := signedTestEnvelope(t, device, 1, 0xaa, ciphertext, nil)

	request := map[string]any{"cursor": 0, "ack_cursor": 0, "envelopes": []Envelope{envelope}}
	first := apiRequest(t, device.server, http.MethodPost, "/v1/sync", device.token, request)
	if first.Code != http.StatusOK {
		t.Fatalf("first sync status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResponse struct {
		Accepted   []uint64   `json:"accepted_sequences"`
		NextCursor int64      `json:"next_cursor"`
		Envelopes  []Envelope `json:"envelopes"`
	}
	decodeResponse(t, first, &firstResponse)
	if len(firstResponse.Accepted) != 1 || firstResponse.Accepted[0] != 1 || firstResponse.NextCursor < 1 || len(firstResponse.Envelopes) != 1 {
		t.Fatalf("unexpected first sync response: %+v", firstResponse)
	}

	second := apiRequest(t, device.server, http.MethodPost, "/v1/sync", device.token, request)
	if second.Code != http.StatusOK {
		t.Fatalf("second sync status=%d body=%s", second.Code, second.Body.String())
	}
	var secondResponse struct {
		Accepted []uint64        `json:"accepted_sequences"`
		Rejected []syncRejection `json:"rejected_sequences"`
	}
	decodeResponse(t, second, &secondResponse)
	if len(secondResponse.Accepted) != 1 || secondResponse.Accepted[0] != 1 || len(secondResponse.Rejected) != 0 {
		t.Fatalf("duplicate upload was not idempotent: %+v", secondResponse)
	}
	var count int
	if err := device.server.db.QueryRow("SELECT COUNT(*) FROM envelopes").Scan(&count); err != nil || count != 1 {
		t.Fatalf("envelope count=%d err=%v", count, err)
	}

	if _, err := device.server.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(device.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(databaseBytes, []byte(personalPhrase)) || strings.Contains(device.logBuffer.String(), personalPhrase) {
		t.Fatal("personal plaintext leaked into database or logs")
	}
	if bytes.Contains(databaseBytes, []byte(device.token)) || strings.Contains(device.logBuffer.String(), device.token) {
		t.Fatal("raw device token leaked into database or logs")
	}
	recoveryAuthentication, _ := base64.RawURLEncoding.DecodeString(device.recoveryAuthentication)
	if bytes.Contains(databaseBytes, recoveryAuthentication) || strings.Contains(device.logBuffer.String(), device.recoveryAuthentication) {
		t.Fatal("raw recovery authentication material leaked into database or logs")
	}
}

func TestInvalidSignatureIsRejected(t *testing.T) {
	device := newTestAccount(t)
	defer device.server.Close()
	envelope := Envelope{
		Version: 1, DeviceSeq: 1, ObjectID: strings.Repeat("b", 32), KeyEpoch: 1,
		Nonce:      base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 24)),
		Ciphertext: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, paddingBucket+16)),
		Signature:  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 64)),
	}
	response := apiRequest(t, device.server, http.MethodPost, "/v1/sync", device.token, map[string]any{"cursor": 0, "ack_cursor": 0, "envelopes": []Envelope{envelope}})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_envelope_signature") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSequenceConflictGapAndPreviousHashChain(t *testing.T) {
	device := newTestAccount(t)
	defer device.server.Close()
	first, firstHash := signedTestEnvelope(t, device, 1, 0x10, bytes.Repeat([]byte{0x41}, paddingBucket+16), nil)
	response := syncRequest(t, device, []Envelope{first})
	if response.AcceptedCount() != 1 {
		t.Fatalf("first upload: %+v", response)
	}

	conflict, _ := signedTestEnvelope(t, device, 1, 0x10, bytes.Repeat([]byte{0x42}, paddingBucket+16), nil)
	response = syncRequest(t, device, []Envelope{conflict})
	if !response.rejectedWith(1, "sequence_conflict") {
		t.Fatalf("conflicting sequence reuse was not rejected: %+v", response)
	}

	third, _ := signedTestEnvelope(t, device, 3, 0x30, bytes.Repeat([]byte{0x43}, paddingBucket+16), firstHash)
	response = syncRequest(t, device, []Envelope{third})
	if !response.rejectedWith(3, "sequence_gap") {
		t.Fatalf("sequence gap was not rejected: %+v", response)
	}

	secondBad, _ := signedTestEnvelope(t, device, 2, 0x20, bytes.Repeat([]byte{0x44}, paddingBucket+16), bytes.Repeat([]byte{0xff}, 32))
	response = syncRequest(t, device, []Envelope{secondBad})
	if !response.rejectedWith(2, "previous_hash_mismatch") {
		t.Fatalf("bad previous hash was not rejected: %+v", response)
	}

	second, secondHash := signedTestEnvelope(t, device, 2, 0x20, bytes.Repeat([]byte{0x45}, paddingBucket+16), firstHash)
	third, _ = signedTestEnvelope(t, device, 3, 0x30, bytes.Repeat([]byte{0x46}, paddingBucket+16), secondHash)
	// The server sorts one-device uploads by sequence before chain validation.
	response = syncRequest(t, device, []Envelope{third, second})
	if response.AcceptedCount() != 2 || len(response.Rejected) != 0 {
		t.Fatalf("valid chain did not converge: %+v", response)
	}
}

func TestConflictingSequenceInsideOneBatchStoresNeitherRecord(t *testing.T) {
	device := newTestAccount(t)
	defer device.server.Close()
	left, _ := signedTestEnvelope(t, device, 1, 0x10, bytes.Repeat([]byte{0x51}, paddingBucket+16), nil)
	right, _ := signedTestEnvelope(t, device, 1, 0x10, bytes.Repeat([]byte{0x52}, paddingBucket+16), nil)
	response := syncRequest(t, device, []Envelope{left, right})
	if response.AcceptedCount() != 0 || !response.rejectedWith(1, "sequence_conflict") {
		t.Fatalf("in-batch conflict was not rejected atomically: %+v", response)
	}
	var count int
	if err := device.server.db.QueryRow("SELECT COUNT(*) FROM envelopes").Scan(&count); err != nil || count != 0 {
		t.Fatalf("conflicting batch stored %d records, err=%v", count, err)
	}
}

func TestSyncRejectsOversizedBatchAndIdentityOverride(t *testing.T) {
	device := newTestAccount(t)
	defer device.server.Close()
	envelope, _ := signedTestEnvelope(t, device, 1, 0x12, bytes.Repeat([]byte{0x41}, paddingBucket+16), nil)
	oversized := make([]Envelope, maxUploadBatch+1)
	for index := range oversized {
		oversized[index] = envelope
	}
	response := apiRequest(t, device.server, http.MethodPost, "/v1/sync", device.token, map[string]any{
		"cursor": 0, "ack_cursor": 0, "envelopes": oversized,
	})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_sync_batch") {
		t.Fatalf("oversized batch status=%d body=%s", response.Code, response.Body.String())
	}

	envelope.DeviceID = strings.Repeat("ff", 16)
	response = apiRequest(t, device.server, http.MethodPost, "/v1/sync", device.token, map[string]any{
		"cursor": 0, "ack_cursor": 0, "envelopes": []Envelope{envelope},
	})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_envelope_metadata") {
		t.Fatalf("identity override status=%d body=%s", response.Code, response.Body.String())
	}

	oversizedCiphertext := bytes.Repeat([]byte{0x41}, maxCiphertext+paddingBucket)
	oversizedEnvelope, _ := signedTestEnvelope(t, device, 1, 0x12, oversizedCiphertext, nil)
	response = apiRequest(t, device.server, http.MethodPost, "/v1/sync", device.token, map[string]any{
		"cursor": 0, "ack_cursor": 0, "envelopes": []Envelope{oversizedEnvelope},
	})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_envelope_ciphertext") {
		t.Fatalf("oversized ciphertext status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSyncDownloadHasCountAndCumulativeCiphertextCaps(t *testing.T) {
	device := newTestAccount(t)
	defer device.server.Close()
	ciphertext := bytes.Repeat([]byte{0x5c}, 300*1024+16)
	var previous []byte
	for sequence := uint64(1); sequence <= 3; sequence++ {
		envelope, recordHash := signedTestEnvelope(t, device, sequence, byte(0x20+sequence), ciphertext, previous)
		response := syncRequest(t, device, []Envelope{envelope})
		if response.AcceptedCount() != 1 {
			t.Fatalf("sequence %d was not accepted: %+v", sequence, response)
		}
		previous = recordHash
	}
	read := func(cursor int64) struct {
		Envelopes []Envelope `json:"envelopes"`
		Next      int64      `json:"next_cursor"`
		HasMore   bool       `json:"has_more"`
	} {
		response := apiRequest(t, device.server, http.MethodPost, "/v1/sync", device.token, map[string]any{
			"cursor": cursor, "ack_cursor": 0, "limit": maximumSyncLimit,
		})
		if response.Code != http.StatusOK {
			t.Fatalf("download status=%d body=%s", response.Code, response.Body.String())
		}
		if response.Body.Len() >= maxBodyBytes {
			t.Fatalf("bounded download response unexpectedly reached %d bytes", response.Body.Len())
		}
		var output struct {
			Envelopes []Envelope `json:"envelopes"`
			Next      int64      `json:"next_cursor"`
			HasMore   bool       `json:"has_more"`
		}
		decodeResponse(t, response, &output)
		return output
	}
	first := read(0)
	if len(first.Envelopes) != 1 || !first.HasMore || first.Next != first.Envelopes[0].Cursor {
		t.Fatalf("first byte-bounded page mismatch: %+v", first)
	}
	second := read(first.Next)
	if len(second.Envelopes) != 1 || !second.HasMore {
		t.Fatalf("second byte-bounded page mismatch: %+v", second)
	}
	third := read(second.Next)
	if len(third.Envelopes) != 1 || third.HasMore {
		t.Fatalf("final byte-bounded page mismatch: %+v", third)
	}
}

func TestEnvelopeSchemaContainsNoPlaintextRoutingFields(t *testing.T) {
	device := newTestAccount(t)
	defer device.server.Close()
	rows, err := device.server.db.Query("PRAGMA table_info(envelopes)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var ordinal, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&ordinal, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if columns["kind"] || columns["hlc"] {
		t.Fatalf("plaintext routing metadata remained in schema: %+v", columns)
	}
	for _, required := range []string{"previous_hash", "record_hash", "ciphertext", "signature"} {
		if !columns[required] {
			t.Fatalf("required opaque-chain column %q is missing", required)
		}
	}
}

func TestCanonicalHeaderGoldenProtocolV1(t *testing.T) {
	envelope := Envelope{
		Version: 1, DeviceSeq: 9, ObjectID: strings.Repeat("22", 16), KeyEpoch: 7,
	}
	previousHash := bytes.Repeat([]byte{0x55}, 32)
	nonce := bytes.Repeat([]byte{0x66}, 24)
	header, err := CanonicalHeader(strings.Repeat("11", 16), strings.Repeat("33", 16), envelope, previousHash, nonce)
	if err != nil {
		t.Fatal(err)
	}
	wantHex := "a80101025011111111111111111111111111111111035022222222222222222222222222222222040705503333333333333333333333333333333306090758205555555555555555555555555555555555555555555555555555555555555555085818666666666666666666666666666666666666666666666666"
	if got := hex.EncodeToString(header); got != wantHex {
		t.Fatalf("canonical Header CBOR changed:\n got %s\nwant %s", got, wantHex)
	}
	firstHeader, err := CanonicalHeader(strings.Repeat("11", 16), strings.Repeat("33", 16), envelope, nil, nonce)
	if err != nil {
		t.Fatal(err)
	}
	wantFirstHex := "a7010102501111111111111111111111111111111103502222222222222222222222222222222204070550333333333333333333333333333333330609085818666666666666666666666666666666666666666666666666"
	if got := hex.EncodeToString(firstHeader); got != wantFirstHex {
		t.Fatalf("canonical first-record Header CBOR changed:\n got %s\nwant %s", got, wantFirstHex)
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x77}, ed25519.SeedSize))
	ciphertext := bytes.Repeat([]byte{0x88}, paddingBucket+16)
	signed := append(append([]byte(nil), header...), ciphertext...)
	signature := ed25519.Sign(privateKey, signed)
	wantSignatureHex := "1e48dc8d6c9e3f52a3b137e4b0a627430f14014a71b529bb9a6be04a9064d4c61f0c30b928157affa35a097249532f9db4f0896e2bf4717d4e4ab3a5776a460a"
	if got := hex.EncodeToString(signature); got != wantSignatureHex {
		t.Fatalf("golden signature changed: %s", got)
	}
}

type testSyncResponse struct {
	Accepted []uint64        `json:"accepted_sequences"`
	Rejected []syncRejection `json:"rejected_sequences"`
}

func (response testSyncResponse) AcceptedCount() int { return len(response.Accepted) }

func (response testSyncResponse) rejectedWith(sequence uint64, code string) bool {
	for _, rejection := range response.Rejected {
		if rejection.DeviceSeq == sequence && rejection.Code == code {
			return true
		}
	}
	return false
}

func syncRequest(t *testing.T, device testDevice, envelopes []Envelope) testSyncResponse {
	t.Helper()
	response := apiRequest(t, device.server, http.MethodPost, "/v1/sync", device.token, map[string]any{
		"cursor": 0, "ack_cursor": 0, "envelopes": envelopes,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("sync status=%d body=%s", response.Code, response.Body.String())
	}
	var result testSyncResponse
	decodeResponse(t, response, &result)
	return result
}

func signedTestEnvelope(t *testing.T, device testDevice, sequence uint64, objectByte byte, ciphertext, previousHash []byte) (Envelope, []byte) {
	t.Helper()
	nonce := bytes.Repeat([]byte{byte(sequence)}, 24)
	envelope := Envelope{
		Version: 1, DeviceSeq: sequence, ObjectID: hex.EncodeToString(bytes.Repeat([]byte{objectByte}, 16)), KeyEpoch: 1,
		Nonce: base64.RawURLEncoding.EncodeToString(nonce), Ciphertext: base64.RawURLEncoding.EncodeToString(ciphertext),
	}
	if len(previousHash) != 0 {
		envelope.PreviousHash = base64.RawURLEncoding.EncodeToString(previousHash)
	}
	header, err := CanonicalHeader(device.accountID, device.deviceID, envelope, previousHash, nonce)
	if err != nil {
		t.Fatal(err)
	}
	signed := append(append([]byte(nil), header...), ciphertext...)
	signature := ed25519.Sign(device.privateKey, signed)
	envelope.Signature = base64.RawURLEncoding.EncodeToString(signature)
	hasher := sha256.New()
	_, _ = hasher.Write(signed)
	_, _ = hasher.Write(signature)
	return envelope, hasher.Sum(nil)
}

func TestPairingAndRevocation(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()

	created := apiRequest(t, first.server, http.MethodPost, "/v1/pairings", first.token, map[string]any{})
	if created.Code != http.StatusCreated {
		t.Fatalf("create pairing status=%d body=%s", created.Code, created.Body.String())
	}
	var pairing struct {
		ID                     string `json:"pairing_id"`
		Secret                 string `json:"pairing_secret"`
		CreatorX25519PublicKey string `json:"creator_x25519_public_key"`
	}
	decodeResponse(t, created, &pairing)
	if pairing.CreatorX25519PublicKey != base64.RawURLEncoding.EncodeToString(first.x25519Key) {
		t.Fatalf("pairing QR material omitted creator X25519 key: %+v", pairing)
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = privateKey
	xKey := bytes.Repeat([]byte{0x77}, 32)
	join := apiRequest(t, first.server, http.MethodPut, "/v1/pairings/"+pairing.ID, "", map[string]any{
		"pairing_secret":         pairing.Secret,
		"device_name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)),
		"ed25519_public_key":     base64.RawURLEncoding.EncodeToString(publicKey),
		"x25519_public_key":      base64.RawURLEncoding.EncodeToString(xKey),
	})
	if join.Code != http.StatusOK {
		t.Fatalf("join pairing status=%d body=%s", join.Code, join.Body.String())
	}
	status := apiRequest(t, first.server, http.MethodGet, "/v1/pairings/"+pairing.ID, first.token, nil)
	var pairingStatus struct {
		State                string `json:"state"`
		DeviceNameCiphertext string `json:"device_name_ciphertext"`
		Ed25519PublicKey     string `json:"ed25519_public_key"`
		X25519PublicKey      string `json:"x25519_public_key"`
	}
	decodeResponse(t, status, &pairingStatus)
	if status.Code != http.StatusOK || pairingStatus.State != "joined" ||
		pairingStatus.DeviceNameCiphertext != base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)) ||
		pairingStatus.Ed25519PublicKey != base64.RawURLEncoding.EncodeToString(publicKey) ||
		pairingStatus.X25519PublicKey != base64.RawURLEncoding.EncodeToString(xKey) {
		t.Fatalf("pairing status omitted pending public material: status=%d body=%s", status.Code, status.Body.String())
	}
	keyring, err := base64.RawURLEncoding.DecodeString(sealedBoxWireGolden)
	if err != nil {
		t.Fatal(err)
	}
	approve := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/approve", first.token, map[string]any{
		"encrypted_keyring": sealedBoxWireGolden,
	})
	if approve.Code != http.StatusOK {
		t.Fatalf("approve pairing status=%d body=%s", approve.Code, approve.Body.String())
	}
	claim := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/claim", "", map[string]any{"pairing_secret": pairing.Secret})
	if claim.Code != http.StatusCreated {
		t.Fatalf("claim pairing status=%d body=%s", claim.Code, claim.Body.String())
	}
	var second struct {
		DeviceID         string `json:"device_id"`
		DeviceToken      string `json:"device_token"`
		EncryptedKeyring string `json:"encrypted_keyring"`
	}
	decodeResponse(t, claim, &second)
	if second.DeviceToken == "" || second.EncryptedKeyring != sealedBoxWireGolden || second.EncryptedKeyring != base64.RawURLEncoding.EncodeToString(keyring) {
		t.Fatalf("unexpected claim response: %+v", second)
	}
	deviceList := apiRequest(t, first.server, http.MethodGet, "/v1/devices", first.token, nil)
	var listed struct {
		Devices []struct {
			ID               string `json:"id"`
			Ed25519PublicKey string `json:"ed25519_public_key"`
		} `json:"devices"`
	}
	decodeResponse(t, deviceList, &listed)
	foundVerificationKey := false
	for _, candidate := range listed.Devices {
		if candidate.ID == second.DeviceID && candidate.Ed25519PublicKey == base64.RawURLEncoding.EncodeToString(publicKey) {
			foundVerificationKey = true
		}
	}
	if deviceList.Code != http.StatusOK || !foundVerificationKey {
		t.Fatalf("paired device verification key missing: status=%d body=%s", deviceList.Code, deviceList.Body.String())
	}

	revoke := apiRequest(t, first.server, http.MethodDelete, "/v1/devices/"+second.DeviceID, first.token, nil)
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", revoke.Code, revoke.Body.String())
	}
	list := apiRequest(t, first.server, http.MethodGet, "/v1/devices", second.DeviceToken, nil)
	if list.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token remained valid: status=%d body=%s", list.Code, list.Body.String())
	}
}

func TestRecoveryAndImmutableKeyringEpoch(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recoverResponse := apiRequest(t, first.server, http.MethodPost, "/v1/accounts/"+first.accountID+"/recover", "", map[string]any{
		"recovery_authentication": first.recoveryAuthentication,
		"device_name_ciphertext":  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, 32)),
		"ed25519_public_key":      base64.RawURLEncoding.EncodeToString(publicKey),
		"x25519_public_key":       base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32)),
	})
	if recoverResponse.Code != http.StatusCreated {
		t.Fatalf("recover status=%d body=%s", recoverResponse.Code, recoverResponse.Body.String())
	}

	firstBlob := sealedBoxWireGolden
	put := apiRequest(t, first.server, http.MethodPut, "/v1/keyring", first.token, map[string]any{"epoch": 1, "ciphertext": firstBlob})
	if put.Code != http.StatusOK {
		t.Fatalf("put keyring status=%d body=%s", put.Code, put.Body.String())
	}
	syncStatus := apiRequest(t, first.server, http.MethodPost, "/v1/sync", first.token, map[string]any{"cursor": 0, "ack_cursor": 0})
	var keyEpoch struct {
		Current uint64 `json:"current_key_epoch"`
	}
	decodeResponse(t, syncStatus, &keyEpoch)
	if syncStatus.Code != http.StatusOK || keyEpoch.Current != 1 {
		t.Fatalf("sync current key epoch status=%d response=%+v", syncStatus.Code, keyEpoch)
	}
	repeat := apiRequest(t, first.server, http.MethodPut, "/v1/keyring", first.token, map[string]any{"epoch": 1, "ciphertext": firstBlob})
	if repeat.Code != http.StatusOK {
		t.Fatalf("repeat keyring status=%d body=%s", repeat.Code, repeat.Body.String())
	}
	conflictWire, err := base64.RawURLEncoding.DecodeString(sealedBoxWireGolden)
	if err != nil {
		t.Fatal(err)
	}
	conflictWire[len(conflictWire)-1] ^= 1
	conflictBlob := base64.RawURLEncoding.EncodeToString(conflictWire)
	conflict := apiRequest(t, first.server, http.MethodPut, "/v1/keyring", first.token, map[string]any{"epoch": 1, "ciphertext": conflictBlob})
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting keyring status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestSealedBoxWireLimitMatchesProtocol(t *testing.T) {
	device := newTestAccount(t)
	defer device.server.Close()
	const headerSize = 4 + 1 + 4 + 24
	maximum := make([]byte, maxSealedBoxWire)
	copy(maximum[:4], "YPBX")
	maximum[4] = 1
	binary.BigEndian.PutUint32(maximum[5:9], uint32(len(maximum)-headerSize))
	copy(maximum[9:headerSize], bytes.Repeat([]byte{0x31}, 24))
	copy(maximum[headerSize:], bytes.Repeat([]byte{0x42}, len(maximum)-headerSize))
	encoded := base64.RawURLEncoding.EncodeToString(maximum)
	response := apiRequest(t, device.server, http.MethodPut, "/v1/keyring", device.token, map[string]any{"epoch": 1, "ciphertext": encoded})
	if response.Code != http.StatusOK {
		t.Fatalf("maximum 256 KiB YPBX wire status=%d body=%s", response.Code, response.Body.String())
	}
	tooLarge := base64.RawURLEncoding.EncodeToString(append(maximum, 0))
	response = apiRequest(t, device.server, http.MethodPut, "/v1/keyring", device.token, map[string]any{"epoch": 2, "ciphertext": tooLarge})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_keyring") {
		t.Fatalf("oversized YPBX wire status=%d body=%s", response.Code, response.Body.String())
	}
	trailing := append(append([]byte(nil), maximum[:headerSize+16]...), 0)
	binary.BigEndian.PutUint32(trailing[5:9], 16)
	response = apiRequest(t, device.server, http.MethodPut, "/v1/keyring", device.token, map[string]any{
		"epoch": 2, "ciphertext": base64.RawURLEncoding.EncodeToString(trailing),
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("YPBX trailing bytes were accepted: status=%d body=%s", response.Code, response.Body.String())
	}
}

func newTestAccount(t *testing.T) testDevice {
	t.Helper()
	databasePath := filepath.Join(t.TempDir(), "sync.db")
	logs := &bytes.Buffer{}
	application, err := New(context.Background(), databasePath, logs)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	xKey := bytes.Repeat([]byte{0x21}, 32)
	recoveryAuthentication := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x91}, 32))
	response := apiRequest(t, application, http.MethodPost, "/v1/accounts", "", map[string]any{
		"recovery_authentication": recoveryAuthentication,
		"device_name_ciphertext":  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x30}, 32)),
		"ed25519_public_key":      base64.RawURLEncoding.EncodeToString(publicKey),
		"x25519_public_key":       base64.RawURLEncoding.EncodeToString(xKey),
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("create account status=%d body=%s", response.Code, response.Body.String())
	}
	var responseFields map[string]json.RawMessage
	decodeResponse(t, response, &responseFields)
	if _, leaked := responseFields["recovery_secret"]; leaked {
		t.Fatal("server returned a value mislabeled as the human recovery key")
	}
	var account struct {
		AccountID string `json:"account_id"`
		DeviceID  string `json:"device_id"`
		Token     string `json:"device_token"`
	}
	decodeResponse(t, response, &account)
	return testDevice{
		accountID: account.AccountID, deviceID: account.DeviceID, token: account.Token, recoveryAuthentication: recoveryAuthentication,
		publicKey: publicKey, privateKey: privateKey, x25519Key: xKey, databasePath: databasePath, server: application, logBuffer: logs,
	}
}

func apiRequest(t *testing.T, handler http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&requestBody).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &requestBody)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decodeResponse(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response %q: %v", response.Body.String(), err)
	}
}

func TestRouteLogsAreRedacted(t *testing.T) {
	device := newTestAccount(t)
	defer device.server.Close()
	secretID := strings.Repeat("c", 32)
	response := apiRequest(t, device.server, http.MethodDelete, fmt.Sprintf("/v1/devices/%s", secretID), device.token, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	logs := device.logBuffer.String()
	if strings.Contains(logs, secretID) || strings.Contains(logs, device.token) {
		t.Fatalf("identifier or token leaked into access log: %s", logs)
	}
}

func TestMigrationLedgerIsIdempotentAndChecksumProtected(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "migration.db")
	application, err := New(context.Background(), databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(body)
	var count int
	var checksum string
	if err := application.db.QueryRow("SELECT COUNT(*), MIN(checksum) FROM schema_migrations").Scan(&count, &checksum); err != nil {
		t.Fatal(err)
	}
	if count != 1 || checksum != hex.EncodeToString(expected[:]) {
		t.Fatalf("migration ledger mismatch: count=%d checksum=%q", count, checksum)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	application, err = New(context.Background(), databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration reapplied: count=%d err=%v", count, err)
	}
	if _, err := application.db.Exec("UPDATE schema_migrations SET checksum = ? WHERE name = ?", strings.Repeat("0", 64), "001_init.sql"); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), databasePath, nil); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered migration ledger was accepted: %v", err)
	}
}

func TestRateLimiterEvictsExpiredIPs(t *testing.T) {
	limiter := &ipLimiter{entries: make(map[string]rateEntry), limit: 10, window: time.Minute}
	start := time.Unix(1_900_000_000, 0)
	if !limiter.allow("192.0.2.1", start) || !limiter.allow("192.0.2.2", start.Add(time.Minute)) {
		t.Fatal("fresh limiter entries were rejected")
	}
	if _, present := limiter.entries["192.0.2.1"]; present || len(limiter.entries) != 1 {
		t.Fatalf("expired limiter entry was retained: %#v", limiter.entries)
	}
}

func TestRateLimiterIgnoresForwardedHeaders(t *testing.T) {
	application, err := New(context.Background(), filepath.Join(t.TempDir(), "rate.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	application.limiter.limit = 1
	application.now = func() time.Time { return time.Unix(1_900_000_000, 0) }
	request := func(forwarded string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/not-found", nil)
		r.RemoteAddr = "198.51.100.7:4567"
		r.Header.Set("X-Forwarded-For", forwarded)
		w := httptest.NewRecorder()
		application.ServeHTTP(w, r)
		return w
	}
	if response := request("203.0.113.1"); response.Code != http.StatusNotFound {
		t.Fatalf("first request status=%d", response.Code)
	}
	if response := request("203.0.113.2"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed forwarded address bypassed peer limit: status=%d", response.Code)
	}
}

func TestOpenAPISplitsInputAndOneTimeResponseSecrets(t *testing.T) {
	document, err := os.ReadFile(filepath.Join("..", "..", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(document)
	if !strings.Contains(text, "SecretInput:") || !strings.Contains(text, "OneTimeSecret:") || strings.Contains(text, "#/components/schemas/Secret\"") {
		t.Fatal("OpenAPI does not separate write-only inputs from one-time response secrets")
	}
}
