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
	rollbackToken          string
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
	twoDeviceRevocationEnabled = true
	t.Cleanup(func() { twoDeviceRevocationEnabled = false })
	first := newTestAccount(t)
	defer first.server.Close()

	pairingID := strings.Repeat("a", 32)
	pairingVerifier := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x21}, 32))
	created := apiRequest(t, first.server, http.MethodPost, "/v1/pairings", first.token, map[string]any{
		"pairing_id": pairingID, "pairing_verifier": pairingVerifier,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create pairing status=%d body=%s", created.Code, created.Body.String())
	}
	var pairing struct {
		ID string `json:"pairing_id"`
	}
	decodeResponse(t, created, &pairing)
	if pairing.ID != pairingID {
		t.Fatalf("pairing relay changed client invitation identity: %+v", pairing)
	}
	parallel := apiRequest(t, first.server, http.MethodPost, "/v1/pairings", first.token, map[string]any{
		"pairing_id": strings.Repeat("f", 32), "pairing_verifier": pairingVerifier,
	})
	if parallel.Code != http.StatusConflict {
		t.Fatalf("parallel live pairing status=%d body=%s", parallel.Code, parallel.Body.String())
	}
	replayedCreate := apiRequest(t, first.server, http.MethodPost, "/v1/pairings", first.token, map[string]any{
		"pairing_id": pairingID, "pairing_verifier": pairingVerifier,
	})
	if replayedCreate.Code != http.StatusCreated {
		t.Fatalf("idempotent create replay status=%d body=%s", replayedCreate.Code, replayedCreate.Body.String())
	}

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	xKey := bytes.Repeat([]byte{0x77}, 32)
	joinedDeviceID := strings.Repeat("b", 32)
	joinProof := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, 32))
	rollbackToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x23}, 32))
	join := apiRequest(t, first.server, http.MethodPut, "/v1/pairings/"+pairing.ID, "", map[string]any{
		"pairing_verifier":       pairingVerifier,
		"device_id":              joinedDeviceID,
		"join_proof":             joinProof,
		"rollback_token":         rollbackToken,
		"device_name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)),
		"ed25519_public_key":     base64.RawURLEncoding.EncodeToString(publicKey),
		"x25519_public_key":      base64.RawURLEncoding.EncodeToString(xKey),
	})
	if join.Code != http.StatusOK {
		t.Fatalf("join pairing status=%d body=%s", join.Code, join.Body.String())
	}
	replayedJoin := apiRequest(t, first.server, http.MethodPut, "/v1/pairings/"+pairing.ID, "", map[string]any{
		"pairing_verifier": pairingVerifier, "device_id": joinedDeviceID, "join_proof": joinProof, "rollback_token": rollbackToken,
		"device_name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)),
		"ed25519_public_key":     base64.RawURLEncoding.EncodeToString(publicKey),
		"x25519_public_key":      base64.RawURLEncoding.EncodeToString(xKey),
	})
	if replayedJoin.Code != http.StatusOK {
		t.Fatalf("idempotent join replay status=%d body=%s", replayedJoin.Code, replayedJoin.Body.String())
	}
	conflictingJoin := apiRequest(t, first.server, http.MethodPut, "/v1/pairings/"+pairing.ID, "", map[string]any{
		"pairing_verifier": pairingVerifier, "device_id": strings.Repeat("c", 32), "join_proof": joinProof, "rollback_token": rollbackToken,
		"device_name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)),
		"ed25519_public_key":     base64.RawURLEncoding.EncodeToString(publicKey),
		"x25519_public_key":      base64.RawURLEncoding.EncodeToString(xKey),
	})
	if conflictingJoin.Code != http.StatusConflict {
		t.Fatalf("conflicting join replay status=%d body=%s", conflictingJoin.Code, conflictingJoin.Body.String())
	}
	status := apiRequest(t, first.server, http.MethodGet, "/v1/pairings/"+pairing.ID, first.token, nil)
	var pairingStatus struct {
		State                string `json:"state"`
		DeviceID             string `json:"device_id"`
		JoinProof            string `json:"join_proof"`
		DeviceNameCiphertext string `json:"device_name_ciphertext"`
		Ed25519PublicKey     string `json:"ed25519_public_key"`
		X25519PublicKey      string `json:"x25519_public_key"`
	}
	decodeResponse(t, status, &pairingStatus)
	if status.Code != http.StatusOK || pairingStatus.State != "joined" ||
		pairingStatus.DeviceID != joinedDeviceID || pairingStatus.JoinProof != joinProof ||
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
	replayedApprove := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/approve", first.token, map[string]any{
		"encrypted_keyring": sealedBoxWireGolden,
	})
	if replayedApprove.Code != http.StatusOK {
		t.Fatalf("idempotent approve replay status=%d body=%s", replayedApprove.Code, replayedApprove.Body.String())
	}
	joinedToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x78}, 32))
	claimMessage, err := canonicalPairingClaimMessage(pairing.ID, first.accountID, first.deviceID,
		joinedDeviceID, first.publicKey, publicKey, first.x25519Key, xKey, joinedToken)
	if err != nil {
		t.Fatal(err)
	}
	claimProof := base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, claimMessage))
	if _, err := first.server.db.Exec(`UPDATE pairings SET claim_expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Second).UnixMilli(), pairing.ID); err != nil {
		t.Fatal(err)
	}
	expiredClaim := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/claim", "", map[string]any{
		"pairing_verifier": pairingVerifier, "device_token": joinedToken, "claim_proof": claimProof,
	})
	if expiredClaim.Code != http.StatusUnauthorized || !strings.Contains(expiredClaim.Body.String(), "invalid_or_expired_pairing") {
		t.Fatalf("expired approved claim status=%d body=%s", expiredClaim.Code, expiredClaim.Body.String())
	}
	if _, err := first.server.db.Exec(`UPDATE pairings SET claim_expires_at = ? WHERE id = ?`,
		time.Now().Add(pairingClaimLifetime).UnixMilli(), pairing.ID); err != nil {
		t.Fatal(err)
	}
	claim := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/claim", "", map[string]any{
		"pairing_verifier": pairingVerifier, "device_token": joinedToken, "claim_proof": claimProof,
	})
	if claim.Code != http.StatusCreated {
		t.Fatalf("claim pairing status=%d body=%s", claim.Code, claim.Body.String())
	}
	var second struct {
		DeviceID         string `json:"device_id"`
		DeviceToken      string `json:"device_token"`
		EncryptedKeyring string `json:"encrypted_keyring"`
	}
	decodeResponse(t, claim, &second)
	if second.DeviceID != joinedDeviceID || second.DeviceToken != joinedToken || second.EncryptedKeyring != sealedBoxWireGolden || second.EncryptedKeyring != base64.RawURLEncoding.EncodeToString(keyring) {
		t.Fatalf("unexpected claim response: %+v", second)
	}
	replayedClaim := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/claim", "", map[string]any{
		"pairing_verifier": pairingVerifier, "device_token": joinedToken, "claim_proof": claimProof,
	})
	if replayedClaim.Code != http.StatusCreated {
		t.Fatalf("idempotent claim replay status=%d body=%s", replayedClaim.Code, replayedClaim.Body.String())
	}
	pendingList := apiRequest(t, first.server, http.MethodGet, "/v1/devices", second.DeviceToken, nil)
	if pendingList.Code != http.StatusConflict || !strings.Contains(pendingList.Body.String(), "pairing_finalization_pending") {
		t.Fatalf("unfinalized paired device accessed account: status=%d body=%s", pendingList.Code, pendingList.Body.String())
	}
	ready := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/ready", second.DeviceToken, nil)
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"state":"ready"`) {
		t.Fatalf("paired device ready status=%d body=%s", ready.Code, ready.Body.String())
	}
	stillPending := apiRequest(t, first.server, http.MethodGet, "/v1/devices", second.DeviceToken, nil)
	if stillPending.Code != http.StatusConflict {
		t.Fatalf("ready but unfinalized device accessed account: status=%d body=%s", stillPending.Code, stillPending.Body.String())
	}
	finalize := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/finalize", first.token, nil)
	if finalize.Code != http.StatusOK || !strings.Contains(finalize.Body.String(), `"state":"finalized"`) {
		t.Fatalf("pairing finalize status=%d body=%s", finalize.Code, finalize.Body.String())
	}
	readyReplay := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/ready", second.DeviceToken, nil)
	if readyReplay.Code != http.StatusOK || !strings.Contains(readyReplay.Body.String(), `"state":"finalized"`) {
		t.Fatalf("finalized ready replay status=%d body=%s", readyReplay.Code, readyReplay.Body.String())
	}
	conflictingClaim := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/claim", "", map[string]any{
		"pairing_verifier": pairingVerifier,
		"device_token":     base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x79}, 32)),
		"claim_proof":      claimProof,
	})
	if conflictingClaim.Code != http.StatusUnauthorized || !strings.Contains(conflictingClaim.Body.String(), "invalid_pairing_claim_proof") {
		t.Fatalf("conflicting claim replay status=%d body=%s", conflictingClaim.Code, conflictingClaim.Body.String())
	}
	advancedJoin := apiRequest(t, first.server, http.MethodPut, "/v1/pairings/"+pairing.ID, "", map[string]any{
		"pairing_verifier": pairingVerifier, "device_id": joinedDeviceID, "join_proof": joinProof, "rollback_token": rollbackToken,
		"device_name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)),
		"ed25519_public_key":     base64.RawURLEncoding.EncodeToString(publicKey),
		"x25519_public_key":      base64.RawURLEncoding.EncodeToString(xKey),
	})
	if advancedJoin.Code != http.StatusOK {
		t.Fatalf("join replay after claim status=%d body=%s", advancedJoin.Code, advancedJoin.Body.String())
	}
	advancedApprove := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairing.ID+"/approve", first.token, map[string]any{
		"encrypted_keyring": sealedBoxWireGolden,
	})
	if advancedApprove.Code != http.StatusOK {
		t.Fatalf("approve replay after claim status=%d body=%s", advancedApprove.Code, advancedApprove.Body.String())
	}
	thirdPairing := apiRequest(t, first.server, http.MethodPost, "/v1/pairings", first.token, map[string]any{
		"pairing_id": strings.Repeat("f", 32), "pairing_verifier": pairingVerifier,
	})
	if thirdPairing.Code != http.StatusConflict {
		t.Fatalf("third-device pairing status=%d body=%s", thirdPairing.Code, thirdPairing.Body.String())
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

func TestClaimedPairingCanBeCancelledBeforeReady(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	pairingID, joinedDeviceID, joinedToken, rollbackToken := insertClaimedPairingForTest(t, first, 0x61)

	cancel := apiRequest(t, first.server, http.MethodDelete, "/v1/pairings/"+pairingID, first.token, nil)
	if cancel.Code != http.StatusNoContent || cancel.Body.Len() != 0 {
		t.Fatalf("cancel claimed pairing status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	var devices, pairings, tombstones int
	if err := first.server.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM devices WHERE account_id = ? AND revoked_at IS NULL),
		(SELECT COUNT(*) FROM pairings WHERE account_id = ?),
		(SELECT COUNT(*) FROM device_rollback_tombstones
		 WHERE account_id = ? AND device_id = ? AND pairing_id = ?)`,
		first.accountID, first.accountID, first.accountID, joinedDeviceID, pairingID).
		Scan(&devices, &pairings, &tombstones); err != nil {
		t.Fatal(err)
	}
	if devices != 1 || pairings != 0 || tombstones != 1 {
		t.Fatalf("cancelled state devices=%d pairings=%d tombstones=%d", devices, pairings, tombstones)
	}

	rollbackPath := "/v1/devices/current?account_id=" + first.accountID +
		"&device_id=" + joinedDeviceID + "&pairing_id=" + pairingID
	rollbackReplay := apiRequest(t, first.server, http.MethodDelete, rollbackPath, rollbackToken, nil)
	if rollbackReplay.Code != http.StatusNoContent {
		t.Fatalf("cancel tombstone did not make joining rollback idempotent: status=%d body=%s",
			rollbackReplay.Code, rollbackReplay.Body.String())
	}
	if rejected := apiRequest(t, first.server, http.MethodGet, "/v1/devices", joinedToken, nil); rejected.Code != http.StatusUnauthorized {
		t.Fatalf("cancelled joining token remained valid: status=%d body=%s", rejected.Code, rejected.Body.String())
	}
	if replay := apiRequest(t, first.server, http.MethodDelete, "/v1/pairings/"+pairingID, first.token, nil); replay.Code != http.StatusNoContent {
		t.Fatalf("cancel replay status=%d body=%s", replay.Code, replay.Body.String())
	}

	newPairing := apiRequest(t, first.server, http.MethodPost, "/v1/pairings", first.token, map[string]any{
		"pairing_id":       strings.Repeat("e", 32),
		"pairing_verifier": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xe1}, 32)),
	})
	if newPairing.Code != http.StatusCreated {
		t.Fatalf("creator could not start a replacement pairing: status=%d body=%s", newPairing.Code, newPairing.Body.String())
	}
}

func TestCancelledPairingRetiresInvitationAndDeviceIdentity(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	retiredPairingID, retiredDeviceID, _, rollbackToken := insertClaimedPairingForTest(t, first, 0x62)

	cancel := apiRequest(t, first.server, http.MethodDelete, "/v1/pairings/"+retiredPairingID, first.token, nil)
	if cancel.Code != http.StatusNoContent {
		t.Fatalf("cancel claimed pairing status=%d body=%s", cancel.Code, cancel.Body.String())
	}

	retiredCreate := apiRequest(t, first.server, http.MethodPost, "/v1/pairings", first.token, map[string]any{
		"pairing_id":       retiredPairingID,
		"pairing_verifier": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x70}, 32)),
	})
	if retiredCreate.Code != http.StatusConflict || !strings.Contains(retiredCreate.Body.String(), "pairing_invitation_conflict") {
		t.Fatalf("retired pairing identity was reusable: status=%d body=%s", retiredCreate.Code, retiredCreate.Body.String())
	}

	replacementPairingID := strings.Repeat("d", 32)
	replacementVerifier := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x71}, 32))
	created := apiRequest(t, first.server, http.MethodPost, "/v1/pairings", first.token, map[string]any{
		"pairing_id": replacementPairingID, "pairing_verifier": replacementVerifier,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("replacement pairing create status=%d body=%s", created.Code, created.Body.String())
	}

	rollbackToken2 := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x72}, 32))
	joinProof := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x73}, 32))
	retiredJoin := apiRequest(t, first.server, http.MethodPut, "/v1/pairings/"+replacementPairingID, "", map[string]any{
		"pairing_verifier":       replacementVerifier,
		"device_id":              retiredDeviceID,
		"join_proof":             joinProof,
		"rollback_token":         rollbackToken2,
		"device_name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x74}, 32)),
		"ed25519_public_key":     base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x75}, 32)),
		"x25519_public_key":      base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x76}, 32)),
	})
	if retiredJoin.Code != http.StatusConflict || !strings.Contains(retiredJoin.Body.String(), "pairing_join_conflict") {
		t.Fatalf("retired device identity was reusable: status=%d body=%s", retiredJoin.Code, retiredJoin.Body.String())
	}

	rollbackPath := "/v1/devices/current?account_id=" + first.accountID +
		"&device_id=" + retiredDeviceID + "&pairing_id=" + retiredPairingID
	if replay := apiRequest(t, first.server, http.MethodDelete, rollbackPath, rollbackToken, nil); replay.Code != http.StatusNoContent {
		t.Fatalf("original rollback capability lost idempotency: status=%d body=%s", replay.Code, replay.Body.String())
	}
	if conflict := apiRequest(t, first.server, http.MethodDelete, rollbackPath, rollbackToken2, nil); conflict.Code != http.StatusConflict ||
		!strings.Contains(conflict.Body.String(), "device_rollback_not_safe") {
		t.Fatalf("replacement rollback capability status=%d body=%s", conflict.Code, conflict.Body.String())
	}

	freshDeviceID := strings.Repeat("e", 32)
	freshJoin := apiRequest(t, first.server, http.MethodPut, "/v1/pairings/"+replacementPairingID, "", map[string]any{
		"pairing_verifier":       replacementVerifier,
		"device_id":              freshDeviceID,
		"join_proof":             joinProof,
		"rollback_token":         rollbackToken2,
		"device_name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x74}, 32)),
		"ed25519_public_key":     base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x75}, 32)),
		"x25519_public_key":      base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x76}, 32)),
	})
	if freshJoin.Code != http.StatusOK {
		t.Fatalf("fresh replacement tuple did not join: status=%d body=%s", freshJoin.Code, freshJoin.Body.String())
	}
}

func TestClaimedPairingCancellationValidatesExistingTombstoneHash(t *testing.T) {
	for _, test := range []struct {
		name           string
		tombstoneToken func(string) string
		wantStatus     int
		wantRemaining  int
	}{
		{
			name:           "exact tuple",
			tombstoneToken: func(token string) string { return token },
			wantStatus:     http.StatusNoContent,
			wantRemaining:  0,
		},
		{
			name: "conflicting rollback hash",
			tombstoneToken: func(string) string {
				return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xf1}, 32))
			},
			wantStatus:    http.StatusConflict,
			wantRemaining: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := newTestAccount(t)
			defer first.server.Close()
			pairingID, deviceID, _, rollbackToken := insertClaimedPairingForTest(t, first, 0x82)
			if _, err := first.server.db.Exec(`INSERT INTO device_rollback_tombstones(
				account_id, device_id, pairing_id, rollback_hash) VALUES(?, ?, ?, ?)`,
				first.accountID, deviceID, pairingID, digest(test.tombstoneToken(rollbackToken))); err != nil {
				t.Fatal(err)
			}

			response := apiRequest(t, first.server, http.MethodDelete, "/v1/pairings/"+pairingID, first.token, nil)
			if response.Code != test.wantStatus {
				t.Fatalf("cancel status=%d body=%s", response.Code, response.Body.String())
			}
			if test.wantStatus == http.StatusConflict && !strings.Contains(response.Body.String(), "pairing_cancel_not_safe") {
				t.Fatalf("conflicting tuple error=%s", response.Body.String())
			}
			var pairings, devices int
			if err := first.server.db.QueryRow(`SELECT
				(SELECT COUNT(*) FROM pairings WHERE id = ?),
				(SELECT COUNT(*) FROM devices WHERE id = ? AND account_id = ?)`,
				pairingID, deviceID, first.accountID).Scan(&pairings, &devices); err != nil {
				t.Fatal(err)
			}
			if pairings != test.wantRemaining || devices != test.wantRemaining {
				t.Fatalf("cancelled state pairings=%d devices=%d want=%d", pairings, devices, test.wantRemaining)
			}
		})
	}
}

func TestPairingReadyAndFinalizeGatesAreFailClosedAndIdempotent(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	pairingID, _, joinedToken, _ := insertClaimedPairingForTest(t, first, 0x71)

	premature := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairingID+"/finalize", first.token, nil)
	if premature.Code != http.StatusConflict || !strings.Contains(premature.Body.String(), "pairing_not_ready_to_finalize") {
		t.Fatalf("premature finalize status=%d body=%s", premature.Code, premature.Body.String())
	}
	ready := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairingID+"/ready", joinedToken, nil)
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"state":"ready"`) {
		t.Fatalf("ready status=%d body=%s", ready.Code, ready.Body.String())
	}
	cancel := apiRequest(t, first.server, http.MethodDelete, "/v1/pairings/"+pairingID, first.token, nil)
	if cancel.Code != http.StatusConflict || !strings.Contains(cancel.Body.String(), "pairing_cancel_not_safe") {
		t.Fatalf("ready pairing was cancellable: status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	finalize := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairingID+"/finalize", first.token, nil)
	if finalize.Code != http.StatusOK || !strings.Contains(finalize.Body.String(), `"state":"finalized"`) {
		t.Fatalf("finalize status=%d body=%s", finalize.Code, finalize.Body.String())
	}
	for attempt := 0; attempt < 2; attempt++ {
		replayedFinalize := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairingID+"/finalize", first.token, nil)
		if replayedFinalize.Code != http.StatusOK || !strings.Contains(replayedFinalize.Body.String(), `"state":"finalized"`) {
			t.Fatalf("finalize replay %d status=%d body=%s", attempt, replayedFinalize.Code, replayedFinalize.Body.String())
		}
		replayedReady := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairingID+"/ready", joinedToken, nil)
		if replayedReady.Code != http.StatusOK || !strings.Contains(replayedReady.Body.String(), `"state":"finalized"`) {
			t.Fatalf("ready replay %d status=%d body=%s", attempt, replayedReady.Code, replayedReady.Body.String())
		}
	}
	if list := apiRequest(t, first.server, http.MethodGet, "/v1/devices", joinedToken, nil); list.Code != http.StatusOK {
		t.Fatalf("finalized joining token did not gain normal API access: status=%d body=%s", list.Code, list.Body.String())
	}
}

func TestPairingReadyWindowExpiryRequiresSafeCancellation(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	pairingID, joinedDeviceID, joinedToken, rollbackToken := insertClaimedPairingForTest(t, first, 0x81)
	if _, err := first.server.db.Exec(`UPDATE pairings SET ready_expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Second).UnixMilli(), pairingID); err != nil {
		t.Fatal(err)
	}
	ready := apiRequest(t, first.server, http.MethodPost, "/v1/pairings/"+pairingID+"/ready", joinedToken, nil)
	if ready.Code != http.StatusConflict || !strings.Contains(ready.Body.String(), "pairing_ready_window_expired") {
		t.Fatalf("expired ready status=%d body=%s", ready.Code, ready.Body.String())
	}
	cancel := apiRequest(t, first.server, http.MethodDelete, "/v1/pairings/"+pairingID, first.token, nil)
	if cancel.Code != http.StatusNoContent {
		t.Fatalf("creator could not cancel expired unready pairing: status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	rollbackPath := "/v1/devices/current?account_id=" + first.accountID +
		"&device_id=" + joinedDeviceID + "&pairing_id=" + pairingID
	if replay := apiRequest(t, first.server, http.MethodDelete, rollbackPath, rollbackToken, nil); replay.Code != http.StatusNoContent {
		t.Fatalf("expired pairing rollback replay status=%d body=%s", replay.Code, replay.Body.String())
	}
}

func TestGetPairingDerivesExpiryFromCurrentStage(t *testing.T) {
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		state         string
		initialExpiry time.Time
		claimExpiry   time.Time
		readyExpiry   time.Time
		readyAt       *time.Time
		finalizedAt   *time.Time
		wantState     string
		wantExpired   bool
	}{
		{name: "created live", state: "created", initialExpiry: now.Add(time.Minute), wantState: "created"},
		{name: "created expired", state: "created", initialExpiry: now.Add(-time.Minute), wantState: "created", wantExpired: true},
		{name: "joined live", state: "joined", initialExpiry: now.Add(time.Minute), wantState: "joined"},
		{name: "joined expired", state: "joined", initialExpiry: now.Add(-time.Minute), wantState: "joined", wantExpired: true},
		{
			name: "approved remains live after initial invitation window", state: "approved",
			initialExpiry: now.Add(-time.Minute), claimExpiry: now.Add(time.Hour), wantState: "approved",
		},
		{
			name: "approved claim window expired", state: "approved",
			initialExpiry: now.Add(time.Hour), claimExpiry: now.Add(-time.Minute), wantState: "approved", wantExpired: true,
		},
		{
			name: "claimed remains live after earlier windows", state: "claimed",
			initialExpiry: now.Add(-2 * time.Hour), claimExpiry: now.Add(-time.Hour), readyExpiry: now.Add(time.Hour), wantState: "claimed",
		},
		{
			name: "claimed ready window expired", state: "claimed",
			initialExpiry: now.Add(time.Hour), claimExpiry: now.Add(time.Hour), readyExpiry: now.Add(-time.Minute), wantState: "claimed", wantExpired: true,
		},
		{
			name: "ready is not expired by earlier deadline", state: "claimed",
			initialExpiry: now.Add(-3 * time.Hour), claimExpiry: now.Add(-2 * time.Hour), readyExpiry: now.Add(-time.Hour),
			readyAt: timePointer(now.Add(-90 * time.Minute)), wantState: "ready",
		},
		{
			name: "finalized is not expired by earlier deadline", state: "claimed",
			initialExpiry: now.Add(-3 * time.Hour), claimExpiry: now.Add(-2 * time.Hour), readyExpiry: now.Add(-time.Hour),
			readyAt: timePointer(now.Add(-90 * time.Minute)), finalizedAt: timePointer(now.Add(-30 * time.Minute)), wantState: "finalized",
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			creator := newTestAccount(t)
			defer creator.server.Close()
			creator.server.now = func() time.Time { return now }
			pairingID := hex.EncodeToString(bytes.Repeat([]byte{byte(0x91 + index)}, 16))
			var readyAt, finalizedAt any
			if test.readyAt != nil {
				readyAt = test.readyAt.UnixMilli()
			}
			if test.finalizedAt != nil {
				finalizedAt = test.finalizedAt.UnixMilli()
			}
			if _, err := creator.server.db.Exec(`INSERT INTO pairings(
				id, account_id, creator_device_id, secret_hash, state, expires_at,
				claim_expires_at, ready_at, ready_expires_at, finalized_at, created_at)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, pairingID, creator.accountID,
				creator.deviceID, bytes.Repeat([]byte{0x44}, 32), test.state, test.initialExpiry.UnixMilli(),
				test.claimExpiry.UnixMilli(), readyAt, test.readyExpiry.UnixMilli(), finalizedAt, now.Add(-4*time.Hour).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			response := apiRequest(t, creator.server, http.MethodGet, "/v1/pairings/"+pairingID, creator.token, nil)
			var status struct {
				State   string `json:"state"`
				Expired bool   `json:"expired"`
			}
			decodeResponse(t, response, &status)
			if response.Code != http.StatusOK || status.State != test.wantState || status.Expired != test.wantExpired {
				t.Fatalf("status=%d state=%q expired=%v body=%s", response.Code, status.State, status.Expired, response.Body.String())
			}
		})
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func TestPairingCancellationRefusesAnyJoiningDeviceWrite(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	pairingID, joinedDeviceID, _, _ := insertClaimedPairingForTest(t, first, 0x89)
	if _, err := first.server.db.Exec(`INSERT INTO keyrings(account_id, epoch, ciphertext, writer_device_id, created_at)
		VALUES(?, 2, ?, ?, ?)`, first.accountID, bytes.Repeat([]byte{0xa1}, 49), joinedDeviceID,
		time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	cancel := apiRequest(t, first.server, http.MethodDelete, "/v1/pairings/"+pairingID, first.token, nil)
	if cancel.Code != http.StatusConflict || !strings.Contains(cancel.Body.String(), "pairing_cancel_not_safe") {
		t.Fatalf("unsafe pairing cancellation status=%d body=%s", cancel.Code, cancel.Body.String())
	}
	var devices, pairings, tombstones int
	if err := first.server.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM devices WHERE id = ? AND account_id = ?),
		(SELECT COUNT(*) FROM pairings WHERE id = ? AND account_id = ?),
		(SELECT COUNT(*) FROM device_rollback_tombstones
		 WHERE account_id = ? AND device_id = ? AND pairing_id = ?)`,
		joinedDeviceID, first.accountID, pairingID, first.accountID,
		first.accountID, joinedDeviceID, pairingID).Scan(&devices, &pairings, &tombstones); err != nil {
		t.Fatal(err)
	}
	if devices != 1 || pairings != 1 || tombstones != 0 {
		t.Fatalf("unsafe cancellation mutated state: devices=%d pairings=%d tombstones=%d",
			devices, pairings, tombstones)
	}
}

func TestConcurrentPairingReservationsLeaveExactlyOneLiveInvitation(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	const attempts = 8
	type result struct {
		status int
		body   string
	}
	results := make(chan result, attempts)
	for index := 0; index < attempts; index++ {
		go func(index int) {
			body, _ := json.Marshal(map[string]any{
				"pairing_id":       fmt.Sprintf("%032x", index+1),
				"pairing_verifier": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(0x91 + index)}, 32)),
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/pairings", bytes.NewReader(body))
			request.RemoteAddr = "127.0.0.1:12345"
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+first.token)
			response := httptest.NewRecorder()
			first.server.ServeHTTP(response, request)
			results <- result{status: response.Code, body: response.Body.String()}
		}(index)
	}
	created, conflicts := 0, 0
	for index := 0; index < attempts; index++ {
		outcome := <-results
		switch outcome.status {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("concurrent create returned status=%d body=%s", outcome.status, outcome.body)
		}
	}
	var live int
	if err := first.server.db.QueryRow(`SELECT COUNT(*) FROM pairings
		WHERE account_id = ? AND state IN ('created', 'joined', 'approved')`, first.accountID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if created != 1 || conflicts != attempts-1 || live != 1 {
		t.Fatalf("reservation race created=%d conflicts=%d live=%d", created, conflicts, live)
	}
}

func insertClaimedPairingForTest(t *testing.T, first testDevice, marker byte) (string, string, string, string) {
	t.Helper()
	pairingID := hex.EncodeToString(bytes.Repeat([]byte{marker}, 16))
	deviceID := hex.EncodeToString(bytes.Repeat([]byte{marker + 1}, 16))
	tokenBytes := bytes.Repeat([]byte{marker + 2}, 32)
	rollbackBytes := bytes.Repeat([]byte{marker + 3}, 32)
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	rollbackToken := base64.RawURLEncoding.EncodeToString(rollbackBytes)
	now := time.Now()
	tx, err := first.server.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO devices(id, account_id, name_ciphertext, token_hash,
		ed25519_public_key, x25519_public_key, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, deviceID, first.accountID, bytes.Repeat([]byte{marker + 4}, 32),
		digest(token), bytes.Repeat([]byte{marker + 5}, 32), bytes.Repeat([]byte{marker + 6}, 32), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO pairings(id, account_id, creator_device_id, secret_hash, state,
		pending_name_ciphertext, pending_ed25519_public_key, pending_x25519_public_key,
		pending_join_proof, rollback_hash, new_device_id, encrypted_keyring, expires_at,
		claim_expires_at, claimed_at, ready_expires_at, created_at)
		VALUES(?, ?, ?, ?, 'claimed', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, pairingID,
		first.accountID, first.deviceID, bytes.Repeat([]byte{marker + 7}, 32), bytes.Repeat([]byte{marker + 4}, 32),
		bytes.Repeat([]byte{marker + 5}, 32), bytes.Repeat([]byte{marker + 6}, 32), bytes.Repeat([]byte{marker + 8}, 32),
		digest(rollbackToken), deviceID, bytes.Repeat([]byte{marker + 9}, 49), now.Add(10*time.Minute).UnixMilli(),
		now.Add(24*time.Hour).UnixMilli(), now.UnixMilli(), now.Add(24*time.Hour).UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return pairingID, deviceID, token, rollbackToken
}

func TestGeneralRevocationDisabledInTwoDevicePreview(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	response := apiRequest(t, first.server, http.MethodDelete, "/v1/devices/"+strings.Repeat("f", 32), first.token, nil)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "device_revocation_not_available_in_two_device_preview") {
		t.Fatalf("revocation preview gate status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRecoveryDisabledInTwoDevicePreview(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	credentials := map[string]any{
		"device_id":               strings.Repeat("e", 32),
		"device_token":            base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xe1}, 32)),
		"recovery_authentication": first.recoveryAuthentication,
		"device_name_ciphertext":  base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xe2}, 32)),
		"ed25519_public_key":      base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xe3}, 32)),
		"x25519_public_key":       base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xe4}, 32)),
	}
	response := apiRequest(t, first.server, http.MethodPost, "/v1/accounts/"+first.accountID+"/recover", "", credentials)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "recovery_not_available_in_two_device_preview") {
		t.Fatalf("recovery preview gate status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRecoveryAndImmutableKeyringEpoch(t *testing.T) {
	twoDeviceRecoveryEnabled = true
	t.Cleanup(func() { twoDeviceRecoveryEnabled = false })
	first := newTestAccount(t)
	defer first.server.Close()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	recoverResponse := apiRequest(t, first.server, http.MethodPost, "/v1/accounts/"+first.accountID+"/recover", "", map[string]any{
		"device_id":               strings.Repeat("9", 32),
		"device_token":            base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x92}, 32)),
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
	return newTestAccountState(t, true)
}

func newTestUnsealedAccount(t *testing.T) testDevice {
	return newTestAccountState(t, false)
}

func newTestAccountState(t *testing.T, seal bool) testDevice {
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
	randomID := make([]byte, 16)
	randomDeviceID := make([]byte, 16)
	randomDeviceToken := make([]byte, 32)
	randomRollbackToken := make([]byte, 32)
	for _, value := range [][]byte{randomID, randomDeviceID, randomDeviceToken, randomRollbackToken} {
		if _, err := rand.Read(value); err != nil {
			t.Fatal(err)
		}
	}
	accountID := hex.EncodeToString(randomID)
	deviceID := hex.EncodeToString(randomDeviceID)
	deviceToken := base64.RawURLEncoding.EncodeToString(randomDeviceToken)
	rollbackToken := base64.RawURLEncoding.EncodeToString(randomRollbackToken)
	response := apiRequest(t, application, http.MethodPost, "/v1/accounts", "", map[string]any{
		"account_id":              accountID,
		"device_id":               deviceID,
		"device_token":            deviceToken,
		"rollback_token":          rollbackToken,
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
	if seal {
		if _, err := application.db.Exec(`UPDATE accounts SET provisioning_sealed_at = ?,
			provisioning_rollback_hash = NULL, provisioning_expires_at = NULL WHERE id = ?`,
			time.Now().UnixMilli(), account.AccountID); err != nil {
			t.Fatal(err)
		}
	}
	return testDevice{
		accountID: account.AccountID, deviceID: account.DeviceID, token: account.Token, rollbackToken: rollbackToken,
		recoveryAuthentication: recoveryAuthentication,
		publicKey:              publicKey, privateKey: privateKey, x25519Key: xKey, databasePath: databasePath, server: application, logBuffer: logs,
	}
}

func TestAccountRollbackDeletesOnlyUnusedSoleDeviceAccount(t *testing.T) {
	device := newTestUnsealedAccount(t)
	defer device.server.Close()
	// Provisioning writes the recovery keyring before committing the local
	// Keychain/DPAPI record.  That keyring must therefore not prevent rollback.
	if _, err := device.server.db.Exec(`INSERT INTO keyrings(account_id, epoch, ciphertext, writer_device_id, created_at)
		VALUES(?, 1, ?, ?, ?)`, device.accountID, bytes.Repeat([]byte{0x42}, 49), device.deviceID, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	response := apiRequest(t, device.server, http.MethodDelete, "/v1/accounts/"+device.accountID, device.rollbackToken, nil)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("rollback status=%d body=%s", response.Code, response.Body.String())
	}
	var accounts, devices, keyrings int
	if err := device.server.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM accounts),
		(SELECT COUNT(*) FROM devices),
		(SELECT COUNT(*) FROM keyrings)`).Scan(&accounts, &devices, &keyrings); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 || devices != 0 || keyrings != 0 {
		t.Fatalf("rollback did not cascade: accounts=%d devices=%d keyrings=%d", accounts, devices, keyrings)
	}
	replayed := apiRequest(t, device.server, http.MethodDelete, "/v1/accounts/"+device.accountID, device.rollbackToken, nil)
	if replayed.Code != http.StatusNoContent {
		t.Fatalf("idempotent account rollback replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	wrong := apiRequest(t, device.server, http.MethodDelete, "/v1/accounts/"+device.accountID,
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xaa}, 32)), nil)
	if wrong.Code == http.StatusNoContent {
		t.Fatal("wrong account rollback capability was accepted")
	}
}

func TestExpiredProvisioningGCCreatesIdempotentRollbackTombstone(t *testing.T) {
	device := newTestUnsealedAccount(t)
	defer device.server.Close()
	if _, err := device.server.db.Exec(`UPDATE accounts SET provisioning_expires_at = ? WHERE id = ?`,
		time.Now().Add(-time.Second).UnixMilli(), device.accountID); err != nil {
		t.Fatal(err)
	}
	if err := device.server.cleanupExpiredProvisioning(context.Background()); err != nil {
		t.Fatal(err)
	}
	response := apiRequest(t, device.server, http.MethodDelete, "/v1/accounts/"+device.accountID, device.rollbackToken, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expired provisioning rollback status=%d body=%s", response.Code, response.Body.String())
	}
	// A delayed create replay must not resurrect the same remote incarnation.
	response = apiRequest(t, device.server, http.MethodPost, "/v1/accounts", "", map[string]any{
		"account_id": device.accountID, "device_id": device.deviceID, "device_token": device.token,
		"rollback_token": device.rollbackToken, "recovery_authentication": device.recoveryAuthentication,
		"device_name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x30}, 32)),
		"ed25519_public_key":     base64.RawURLEncoding.EncodeToString(device.publicKey),
		"x25519_public_key":      base64.RawURLEncoding.EncodeToString(device.x25519Key),
	})
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "provisioning_identity_retired") {
		t.Fatalf("retired provisioning identity status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAccountProvisioningIsIdempotentAndNormalBearerCannotRollback(t *testing.T) {
	device := newTestUnsealedAccount(t)
	defer device.server.Close()
	var name, edKey, xKey []byte
	if err := device.server.db.QueryRow(`SELECT name_ciphertext, ed25519_public_key, x25519_public_key
		FROM devices WHERE id = ?`, device.deviceID).Scan(&name, &edKey, &xKey); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"account_id": device.accountID, "device_id": device.deviceID,
		"device_token": device.token, "rollback_token": device.rollbackToken,
		"recovery_authentication": device.recoveryAuthentication,
		"device_name_ciphertext":  base64.RawURLEncoding.EncodeToString(name),
		"ed25519_public_key":      base64.RawURLEncoding.EncodeToString(edKey),
		"x25519_public_key":       base64.RawURLEncoding.EncodeToString(xKey),
	}
	replayed := apiRequest(t, device.server, http.MethodPost, "/v1/accounts", "", payload)
	if replayed.Code != http.StatusCreated {
		t.Fatalf("idempotent provisioning replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	var accounts, devices int
	if err := device.server.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM accounts), (SELECT COUNT(*) FROM devices)`).Scan(&accounts, &devices); err != nil {
		t.Fatal(err)
	}
	if accounts != 1 || devices != 1 {
		t.Fatalf("provisioning replay duplicated state: accounts=%d devices=%d", accounts, devices)
	}
	wrongCapability := apiRequest(t, device.server, http.MethodDelete, "/v1/accounts/"+device.accountID, device.token, nil)
	if wrongCapability.Code != http.StatusConflict {
		t.Fatalf("normal device bearer deleted account: status=%d body=%s", wrongCapability.Code, wrongCapability.Body.String())
	}
	payload["device_token"] = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xee}, 32))
	conflict := apiRequest(t, device.server, http.MethodPost, "/v1/accounts", "", payload)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "provisioning_identity_conflict") {
		t.Fatalf("conflicting provisioning replay status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestUnsealedProvisioningAcceptsOnlyEpochOneKeyring(t *testing.T) {
	device := newTestUnsealedAccount(t)
	defer device.server.Close()
	response := apiRequest(t, device.server, http.MethodPut, "/v1/keyring", device.token, map[string]any{
		"epoch": 2, "ciphertext": sealedBoxWireGolden,
	})
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_keyring") {
		t.Fatalf("unsealed epoch-two keyring status=%d body=%s", response.Code, response.Body.String())
	}
	response = apiRequest(t, device.server, http.MethodPut, "/v1/keyring", device.token, map[string]any{
		"epoch": 1, "ciphertext": sealedBoxWireGolden,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("unsealed epoch-one keyring status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCurrentDeviceRollbackRemovesOnlyUnusedPairedDevice(t *testing.T) {
	first := newTestAccount(t)
	defer first.server.Close()
	secondID := strings.Repeat("e", 32)
	secondToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xe1}, 32))
	rollbackToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xe2}, 32))
	pairingID := strings.Repeat("d", 32)
	now := time.Now().UnixMilli()
	if _, err := first.server.db.Exec(`INSERT INTO devices(id, account_id, name_ciphertext, token_hash,
		ed25519_public_key, x25519_public_key, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		secondID, first.accountID, bytes.Repeat([]byte{0x51}, 32), digest(secondToken),
		bytes.Repeat([]byte{0x52}, 32), bytes.Repeat([]byte{0x53}, 32), now); err != nil {
		t.Fatal(err)
	}
	if _, err := first.server.db.Exec(`INSERT INTO pairings(id, account_id, creator_device_id,
		secret_hash, state, pending_name_ciphertext, pending_ed25519_public_key,
		pending_x25519_public_key, new_device_id, encrypted_keyring, rollback_hash, expires_at, claimed_at, created_at)
		VALUES(?, ?, ?, ?, 'claimed', ?, ?, ?, ?, ?, ?, ?, ?, ?)`, pairingID, first.accountID,
		first.deviceID, bytes.Repeat([]byte{0x54}, 32), bytes.Repeat([]byte{0x55}, 32),
		bytes.Repeat([]byte{0x56}, 32), bytes.Repeat([]byte{0x57}, 32), secondID,
		bytes.Repeat([]byte{0x58}, 49), digest(rollbackToken), now+60000, now, now); err != nil {
		t.Fatal(err)
	}
	path := "/v1/devices/current?account_id=" + first.accountID + "&device_id=" + secondID + "&pairing_id=" + pairingID
	response := apiRequest(t, first.server, http.MethodDelete, path, rollbackToken, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("paired device rollback status=%d body=%s", response.Code, response.Body.String())
	}
	var remainingDevices, remainingPairings int
	if err := first.server.db.QueryRow(`SELECT (SELECT COUNT(*) FROM devices),
		(SELECT COUNT(*) FROM pairings)`).Scan(&remainingDevices, &remainingPairings); err != nil {
		t.Fatal(err)
	}
	if remainingDevices != 1 || remainingPairings != 0 {
		t.Fatalf("paired rollback changed wrong state: devices=%d pairings=%d", remainingDevices, remainingPairings)
	}
	if current := apiRequest(t, first.server, http.MethodGet, "/v1/devices", first.token, nil); current.Code != http.StatusOK {
		t.Fatalf("working peer was affected: status=%d body=%s", current.Code, current.Body.String())
	}
	replayed := apiRequest(t, first.server, http.MethodDelete, path, rollbackToken, nil)
	if replayed.Code != http.StatusNoContent {
		t.Fatalf("idempotent device rollback replay status=%d body=%s", replayed.Code, replayed.Body.String())
	}
	wrong := apiRequest(t, first.server, http.MethodDelete, path,
		base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xe3}, 32)), nil)
	if wrong.Code == http.StatusNoContent {
		t.Fatal("wrong paired-device rollback capability was accepted")
	}
}

type rollbackPairingFixture struct {
	creator      testDevice
	pairingID    string
	deviceID     string
	verifier     string
	rollback     string
	deviceToken  string
	joinPayload  map[string]any
	claimPayload map[string]any
}

func newRollbackPairingFixture(t *testing.T, marker byte, stage string) rollbackPairingFixture {
	t.Helper()
	fixture := rollbackPairingFixture{
		creator:     newTestAccount(t),
		pairingID:   hex.EncodeToString(bytes.Repeat([]byte{marker}, 16)),
		deviceID:    hex.EncodeToString(bytes.Repeat([]byte{marker + 1}, 16)),
		verifier:    base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{marker + 2}, 32)),
		rollback:    base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{marker + 3}, 32)),
		deviceToken: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{marker + 4}, 32)),
	}
	created := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings", fixture.creator.token, map[string]any{
		"pairing_id": fixture.pairingID, "pairing_verifier": fixture.verifier,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create rollback fixture status=%d body=%s", created.Code, created.Body.String())
	}
	if stage == "created" {
		return fixture
	}

	joiningPublicKey, joiningPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	joiningX25519Key := bytes.Repeat([]byte{marker + 5}, 32)
	fixture.joinPayload = map[string]any{
		"pairing_verifier":       fixture.verifier,
		"device_id":              fixture.deviceID,
		"join_proof":             base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{marker + 6}, 32)),
		"rollback_token":         fixture.rollback,
		"device_name_ciphertext": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{marker + 7}, 32)),
		"ed25519_public_key":     base64.RawURLEncoding.EncodeToString(joiningPublicKey),
		"x25519_public_key":      base64.RawURLEncoding.EncodeToString(joiningX25519Key),
	}
	joined := apiRequest(t, fixture.creator.server, http.MethodPut, "/v1/pairings/"+fixture.pairingID, "", fixture.joinPayload)
	if joined.Code != http.StatusOK {
		t.Fatalf("join rollback fixture status=%d body=%s", joined.Code, joined.Body.String())
	}
	if stage == "joined" {
		return fixture
	}

	approved := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings/"+fixture.pairingID+"/approve",
		fixture.creator.token, map[string]any{"encrypted_keyring": sealedBoxWireGolden})
	if approved.Code != http.StatusOK {
		t.Fatalf("approve rollback fixture status=%d body=%s", approved.Code, approved.Body.String())
	}
	if stage == "approved" {
		return fixture
	}

	claimMessage, err := canonicalPairingClaimMessage(fixture.pairingID, fixture.creator.accountID,
		fixture.creator.deviceID, fixture.deviceID, fixture.creator.publicKey, joiningPublicKey,
		fixture.creator.x25519Key, joiningX25519Key, fixture.deviceToken)
	if err != nil {
		t.Fatal(err)
	}
	fixture.claimPayload = map[string]any{
		"pairing_verifier": fixture.verifier,
		"device_token":     fixture.deviceToken,
		"claim_proof":      base64.RawURLEncoding.EncodeToString(ed25519.Sign(joiningPrivateKey, claimMessage)),
	}
	claimed := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings/"+fixture.pairingID+"/claim", "", fixture.claimPayload)
	if claimed.Code != http.StatusCreated {
		t.Fatalf("claim rollback fixture status=%d body=%s", claimed.Code, claimed.Body.String())
	}
	if stage == "claimed" {
		return fixture
	}

	ready := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings/"+fixture.pairingID+"/ready",
		fixture.deviceToken, nil)
	if ready.Code != http.StatusOK {
		t.Fatalf("ready rollback fixture status=%d body=%s", ready.Code, ready.Body.String())
	}
	if stage == "ready" {
		return fixture
	}

	finalized := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings/"+fixture.pairingID+"/finalize",
		fixture.creator.token, nil)
	if finalized.Code != http.StatusOK {
		t.Fatalf("finalize rollback fixture status=%d body=%s", finalized.Code, finalized.Body.String())
	}
	if stage != "finalized" {
		t.Fatalf("unknown rollback fixture stage %q", stage)
	}
	return fixture
}

func (fixture rollbackPairingFixture) rollbackPath() string {
	return "/v1/devices/current?account_id=" + fixture.creator.accountID +
		"&device_id=" + fixture.deviceID + "&pairing_id=" + fixture.pairingID
}

type rollbackDatabaseSnapshot struct {
	Pairings      int
	Devices       int
	Tombstones    int
	State         string
	ReadyAt       int64
	FinalizedAt   int64
	RollbackHash  string
	JoiningDevice string
}

func snapshotRollbackDatabase(t *testing.T, fixture rollbackPairingFixture) rollbackDatabaseSnapshot {
	t.Helper()
	var snapshot rollbackDatabaseSnapshot
	err := fixture.creator.server.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM pairings WHERE id = ? AND account_id = ?),
		(SELECT COUNT(*) FROM devices WHERE account_id = ? AND revoked_at IS NULL),
		(SELECT COUNT(*) FROM device_rollback_tombstones
		 WHERE account_id = ? AND device_id = ? AND pairing_id = ?),
		COALESCE((SELECT state FROM pairings WHERE id = ?), ''),
		COALESCE((SELECT ready_at FROM pairings WHERE id = ?), 0),
		COALESCE((SELECT finalized_at FROM pairings WHERE id = ?), 0),
		COALESCE((SELECT hex(rollback_hash) FROM pairings WHERE id = ?), ''),
		COALESCE((SELECT new_device_id FROM pairings WHERE id = ?), '')`,
		fixture.pairingID, fixture.creator.accountID, fixture.creator.accountID,
		fixture.creator.accountID, fixture.deviceID, fixture.pairingID,
		fixture.pairingID, fixture.pairingID, fixture.pairingID, fixture.pairingID, fixture.pairingID).
		Scan(&snapshot.Pairings, &snapshot.Devices, &snapshot.Tombstones, &snapshot.State,
			&snapshot.ReadyAt, &snapshot.FinalizedAt, &snapshot.RollbackHash, &snapshot.JoiningDevice)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRollbackCapabilityLifecycleStages(t *testing.T) {
	tests := []struct {
		stage        string
		wantStatus   int
		wantCode     string
		wantDevices  int
		wantPairings int
		wantTombs    int
	}{
		{stage: "created", wantStatus: http.StatusConflict, wantCode: "device_rollback_not_safe", wantDevices: 1, wantPairings: 1},
		{stage: "joined", wantStatus: http.StatusNoContent, wantDevices: 1, wantTombs: 1},
		{stage: "approved", wantStatus: http.StatusNoContent, wantDevices: 1, wantTombs: 1},
		{stage: "claimed", wantStatus: http.StatusNoContent, wantDevices: 1, wantTombs: 1},
		{stage: "ready", wantStatus: http.StatusConflict, wantCode: "device_rollback_after_ready", wantDevices: 2, wantPairings: 1},
		{stage: "finalized", wantStatus: http.StatusConflict, wantCode: "device_rollback_after_ready", wantDevices: 2, wantPairings: 1},
	}
	for index, test := range tests {
		t.Run(test.stage, func(t *testing.T) {
			fixture := newRollbackPairingFixture(t, byte(0x30+index*8), test.stage)
			defer fixture.creator.server.Close()
			before := snapshotRollbackDatabase(t, fixture)
			response := apiRequest(t, fixture.creator.server, http.MethodDelete, fixture.rollbackPath(), fixture.rollback, nil)
			if response.Code != test.wantStatus || (test.wantCode != "" && !strings.Contains(response.Body.String(), test.wantCode)) {
				t.Fatalf("%s rollback status=%d body=%s", test.stage, response.Code, response.Body.String())
			}
			after := snapshotRollbackDatabase(t, fixture)
			if after.Devices != test.wantDevices || after.Pairings != test.wantPairings || after.Tombstones != test.wantTombs {
				t.Fatalf("%s rollback state=%+v", test.stage, after)
			}
			if test.wantStatus != http.StatusNoContent && before != after {
				t.Fatalf("rejected %s rollback mutated state: before=%+v after=%+v", test.stage, before, after)
			}
			if test.wantStatus == http.StatusNoContent {
				replay := apiRequest(t, fixture.creator.server, http.MethodDelete, fixture.rollbackPath(), fixture.rollback, nil)
				if replay.Code != http.StatusNoContent || snapshotRollbackDatabase(t, fixture) != after {
					t.Fatalf("%s DELETE response-loss replay status=%d body=%s", test.stage, replay.Code, replay.Body.String())
				}
				wrong := apiRequest(t, fixture.creator.server, http.MethodDelete, fixture.rollbackPath(),
					base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfe}, 32)), nil)
				if wrong.Code != http.StatusConflict || !strings.Contains(wrong.Body.String(), "device_rollback_not_safe") ||
					snapshotRollbackDatabase(t, fixture) != after {
					t.Fatalf("%s conflicting tombstone capability status=%d body=%s", test.stage, wrong.Code, wrong.Body.String())
				}
			}
		})
	}
}

func TestReadyAndFinalizedRollbackRejectEvenWithExactTombstone(t *testing.T) {
	for index, stage := range []string{"ready", "finalized"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newRollbackPairingFixture(t, byte(0x64+index*8), stage)
			defer fixture.creator.server.Close()
			if _, err := fixture.creator.server.db.Exec(`INSERT INTO device_rollback_tombstones(
				account_id, device_id, pairing_id, rollback_hash) VALUES(?, ?, ?, ?)`,
				fixture.creator.accountID, fixture.deviceID, fixture.pairingID, digest(fixture.rollback)); err != nil {
				t.Fatal(err)
			}
			before := snapshotRollbackDatabase(t, fixture)
			response := apiRequest(t, fixture.creator.server, http.MethodDelete, fixture.rollbackPath(), fixture.rollback, nil)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "device_rollback_after_ready") {
				t.Fatalf("%s rollback status=%d body=%s", stage, response.Code, response.Body.String())
			}
			if after := snapshotRollbackDatabase(t, fixture); after != before {
				t.Fatalf("%s rollback with exact tombstone mutated state: before=%+v after=%+v", stage, before, after)
			}
		})
	}
}

func TestPreclaimRollbackRequiresExactTupleAndCapability(t *testing.T) {
	fixture := newRollbackPairingFixture(t, 0x75, "joined")
	defer fixture.creator.server.Close()
	before := snapshotRollbackDatabase(t, fixture)
	wrongToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xf1}, 32))
	wrongAccount := strings.Repeat("a", 32)
	if wrongAccount == fixture.creator.accountID {
		wrongAccount = strings.Repeat("b", 32)
	}
	tests := []struct {
		name  string
		path  string
		token string
		code  int
	}{
		{name: "wrong capability", path: fixture.rollbackPath(), token: wrongToken, code: http.StatusConflict},
		{name: "wrong account", path: "/v1/devices/current?account_id=" + wrongAccount + "&device_id=" + fixture.deviceID + "&pairing_id=" + fixture.pairingID, token: fixture.rollback, code: http.StatusConflict},
		{name: "wrong device", path: "/v1/devices/current?account_id=" + fixture.creator.accountID + "&device_id=" + strings.Repeat("c", 32) + "&pairing_id=" + fixture.pairingID, token: fixture.rollback, code: http.StatusConflict},
		{name: "wrong pairing", path: "/v1/devices/current?account_id=" + fixture.creator.accountID + "&device_id=" + fixture.deviceID + "&pairing_id=" + strings.Repeat("d", 32), token: fixture.rollback, code: http.StatusConflict},
		{name: "missing row", path: "/v1/devices/current?account_id=" + fixture.creator.accountID + "&device_id=" + strings.Repeat("e", 32) + "&pairing_id=" + strings.Repeat("f", 32), token: fixture.rollback, code: http.StatusConflict},
		{name: "malformed capability", path: fixture.rollbackPath(), token: "not-canonical", code: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := apiRequest(t, fixture.creator.server, http.MethodDelete, test.path, test.token, nil)
			if response.Code != test.code || snapshotRollbackDatabase(t, fixture) != before {
				t.Fatalf("status=%d body=%s before=%+v after=%+v", response.Code, response.Body.String(),
					before, snapshotRollbackDatabase(t, fixture))
			}
		})
	}
}

func TestCreatorCancellationRetiresEveryJoinedTuple(t *testing.T) {
	for index, stage := range []string{"created", "joined", "approved", "claimed"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newRollbackPairingFixture(t, byte(0x90+index*8), stage)
			defer fixture.creator.server.Close()
			cancelled := apiRequest(t, fixture.creator.server, http.MethodDelete, "/v1/pairings/"+fixture.pairingID,
				fixture.creator.token, nil)
			if cancelled.Code != http.StatusNoContent {
				t.Fatalf("cancel %s status=%d body=%s", stage, cancelled.Code, cancelled.Body.String())
			}
			after := snapshotRollbackDatabase(t, fixture)
			wantTombstone := 1
			if stage == "created" {
				wantTombstone = 0
			}
			if after.Pairings != 0 || after.Devices != 1 || after.Tombstones != wantTombstone {
				t.Fatalf("cancel %s state=%+v", stage, after)
			}
			rollbackReplay := apiRequest(t, fixture.creator.server, http.MethodDelete, fixture.rollbackPath(), fixture.rollback, nil)
			if stage == "created" {
				if rollbackReplay.Code != http.StatusConflict {
					t.Fatalf("created cancellation authenticated a nonexistent tuple: status=%d body=%s", rollbackReplay.Code, rollbackReplay.Body.String())
				}
				reused := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings", fixture.creator.token, map[string]any{
					"pairing_id": fixture.pairingID, "pairing_verifier": fixture.verifier,
				})
				if reused.Code != http.StatusCreated {
					t.Fatalf("untouched created invitation was unnecessarily retired: status=%d body=%s", reused.Code, reused.Body.String())
				}
			} else if rollbackReplay.Code != http.StatusNoContent {
				t.Fatalf("%s creator-cancel response-loss rollback status=%d body=%s", stage, rollbackReplay.Code, rollbackReplay.Body.String())
			} else {
				retiredInvitation := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings", fixture.creator.token, map[string]any{
					"pairing_id": fixture.pairingID, "pairing_verifier": fixture.verifier,
				})
				if retiredInvitation.Code != http.StatusConflict || !strings.Contains(retiredInvitation.Body.String(), "pairing_invitation_conflict") {
					t.Fatalf("cancelled %s pairing identity was reusable: status=%d body=%s", stage, retiredInvitation.Code, retiredInvitation.Body.String())
				}

				replacementID := fmt.Sprintf("%032x", 0x1234+index)
				replacementVerifier := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(0x41 + index)}, 32))
				replacement := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings", fixture.creator.token, map[string]any{
					"pairing_id": replacementID, "pairing_verifier": replacementVerifier,
				})
				if replacement.Code != http.StatusCreated {
					t.Fatalf("replacement pairing after %s cancellation status=%d body=%s", stage, replacement.Code, replacement.Body.String())
				}
				retiredJoinPayload := make(map[string]any, len(fixture.joinPayload))
				for key, value := range fixture.joinPayload {
					retiredJoinPayload[key] = value
				}
				retiredJoinPayload["pairing_verifier"] = replacementVerifier
				retiredJoin := apiRequest(t, fixture.creator.server, http.MethodPut, "/v1/pairings/"+replacementID, "", retiredJoinPayload)
				if retiredJoin.Code != http.StatusConflict || !strings.Contains(retiredJoin.Body.String(), "pairing_join_conflict") {
					t.Fatalf("cancelled %s device identity was reusable: status=%d body=%s", stage, retiredJoin.Code, retiredJoin.Body.String())
				}
			}
		})
	}
}

func TestJoinAndClaimResponseLossCanRollback(t *testing.T) {
	for index, stage := range []string{"joined", "claimed"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newRollbackPairingFixture(t, byte(0xc0+index*8), stage)
			defer fixture.creator.server.Close()
			// The fixture intentionally discards the first transition response just
			// as a client would after a transport loss. Its protected journal still
			// has the exact rollback tuple and capability.
			rolledBack := apiRequest(t, fixture.creator.server, http.MethodDelete, fixture.rollbackPath(), fixture.rollback, nil)
			if rolledBack.Code != http.StatusNoContent {
				t.Fatalf("%s response-loss rollback status=%d body=%s", stage, rolledBack.Code, rolledBack.Body.String())
			}
			var replay *httptest.ResponseRecorder
			if stage == "joined" {
				replay = apiRequest(t, fixture.creator.server, http.MethodPut, "/v1/pairings/"+fixture.pairingID, "", fixture.joinPayload)
			} else {
				replay = apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings/"+fixture.pairingID+"/claim", "", fixture.claimPayload)
			}
			if replay.Code != http.StatusUnauthorized {
				t.Fatalf("retired %s tuple was replayable: status=%d body=%s", stage, replay.Code, replay.Body.String())
			}
		})
	}
}

func TestExpiredJoinedTupleIsRetiredBeforeReplacement(t *testing.T) {
	for index, stage := range []string{"joined", "approved"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newRollbackPairingFixture(t, byte(0xb0+index*8), stage)
			defer fixture.creator.server.Close()
			if stage == "joined" {
				if _, err := fixture.creator.server.db.Exec(`UPDATE pairings SET expires_at = ? WHERE id = ?`,
					time.Now().Add(-time.Second).UnixMilli(), fixture.pairingID); err != nil {
					t.Fatal(err)
				}
			} else if _, err := fixture.creator.server.db.Exec(`UPDATE pairings SET claim_expires_at = ? WHERE id = ?`,
				time.Now().Add(-time.Second).UnixMilli(), fixture.pairingID); err != nil {
				t.Fatal(err)
			}
			replacementID := fmt.Sprintf("%032x", 0x4321+index)
			replacement := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings", fixture.creator.token, map[string]any{
				"pairing_id":       replacementID,
				"pairing_verifier": base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{byte(0x51 + index)}, 32)),
			})
			if replacement.Code != http.StatusCreated {
				t.Fatalf("replacement after expired %s status=%d body=%s", stage, replacement.Code, replacement.Body.String())
			}
			after := snapshotRollbackDatabase(t, fixture)
			if after.Pairings != 0 || after.Devices != 1 || after.Tombstones != 1 {
				t.Fatalf("expired %s tuple was not retired: %+v", stage, after)
			}
			if replay := apiRequest(t, fixture.creator.server, http.MethodDelete, fixture.rollbackPath(), fixture.rollback, nil); replay.Code != http.StatusNoContent {
				t.Fatalf("expired %s rollback replay status=%d body=%s", stage, replay.Code, replay.Body.String())
			}
			if cancel := apiRequest(t, fixture.creator.server, http.MethodDelete, "/v1/pairings/"+replacementID,
				fixture.creator.token, nil); cancel.Code != http.StatusNoContent {
				t.Fatalf("replacement cancel status=%d body=%s", cancel.Code, cancel.Body.String())
			}
			retiredCreate := apiRequest(t, fixture.creator.server, http.MethodPost, "/v1/pairings", fixture.creator.token, map[string]any{
				"pairing_id": fixture.pairingID, "pairing_verifier": fixture.verifier,
			})
			if retiredCreate.Code != http.StatusConflict || !strings.Contains(retiredCreate.Body.String(), "pairing_invitation_conflict") {
				t.Fatalf("expired %s pairing identity resurrected: status=%d body=%s", stage, retiredCreate.Code, retiredCreate.Body.String())
			}
		})
	}
}

func TestConcurrentCreatorCancelAndJoiningRollbackConverge(t *testing.T) {
	for index, stage := range []string{"joined", "approved", "claimed"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newRollbackPairingFixture(t, byte(0xd0+index*8), stage)
			defer fixture.creator.server.Close()
			start := make(chan struct{})
			statuses := make(chan int, 2)
			go func() {
				<-start
				statuses <- apiRequest(t, fixture.creator.server, http.MethodDelete,
					"/v1/pairings/"+fixture.pairingID, fixture.creator.token, nil).Code
			}()
			go func() {
				<-start
				statuses <- apiRequest(t, fixture.creator.server, http.MethodDelete,
					fixture.rollbackPath(), fixture.rollback, nil).Code
			}()
			close(start)
			for attempt := 0; attempt < 2; attempt++ {
				if status := <-statuses; status != http.StatusNoContent {
					t.Fatalf("concurrent %s cancellation status=%d", stage, status)
				}
			}
			after := snapshotRollbackDatabase(t, fixture)
			if after.Pairings != 0 || after.Devices != 1 || after.Tombstones != 1 {
				t.Fatalf("concurrent %s cancellation did not converge: %+v", stage, after)
			}
		})
	}
}

func TestConcurrentCancelAndRollbackAcrossServerInstancesConverge(t *testing.T) {
	fixture := newRollbackPairingFixture(t, 0xe8, "claimed")
	defer fixture.creator.server.Close()
	secondServer, err := New(context.Background(), fixture.creator.databasePath, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer secondServer.Close()

	start := make(chan struct{})
	statuses := make(chan int, 2)
	go func() {
		<-start
		statuses <- apiRequest(t, fixture.creator.server, http.MethodDelete,
			"/v1/pairings/"+fixture.pairingID, fixture.creator.token, nil).Code
	}()
	go func() {
		<-start
		statuses <- apiRequest(t, secondServer, http.MethodDelete,
			fixture.rollbackPath(), fixture.rollback, nil).Code
	}()
	close(start)
	for attempt := 0; attempt < 2; attempt++ {
		if status := <-statuses; status != http.StatusNoContent {
			t.Fatalf("cross-instance cancellation status=%d", status)
		}
	}
	after := snapshotRollbackDatabase(t, fixture)
	if after.Pairings != 0 || after.Devices != 1 || after.Tombstones != 1 {
		t.Fatalf("cross-instance cancellation did not converge: %+v", after)
	}
}

func TestClaimedRollbackStillRefusesUnsafeWrites(t *testing.T) {
	fixture := newRollbackPairingFixture(t, 0xf0, "claimed")
	defer fixture.creator.server.Close()
	if _, err := fixture.creator.server.db.Exec(`INSERT INTO keyrings(account_id, epoch, ciphertext, writer_device_id, created_at)
		VALUES(?, 2, ?, ?, ?)`, fixture.creator.accountID, bytes.Repeat([]byte{0xa1}, 49),
		fixture.deviceID, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	before := snapshotRollbackDatabase(t, fixture)
	response := apiRequest(t, fixture.creator.server, http.MethodDelete, fixture.rollbackPath(), fixture.rollback, nil)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "device_rollback_not_safe") ||
		snapshotRollbackDatabase(t, fixture) != before {
		t.Fatalf("unsafe claimed rollback status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAccountRollbackFailsClosedAfterUse(t *testing.T) {
	tests := []struct {
		name  string
		stage func(*testing.T, testDevice)
	}{
		{
			name: "sealed",
			stage: func(t *testing.T, device testDevice) {
				if _, err := device.server.db.Exec(`INSERT INTO keyrings(account_id, epoch, ciphertext, writer_device_id, created_at)
					VALUES(?, 1, ?, ?, ?)`, device.accountID, bytes.Repeat([]byte{0x42}, 49), device.deviceID, time.Now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
				response := apiRequest(t, device.server, http.MethodPost, "/v1/accounts/"+device.accountID+"/seal", device.token, map[string]any{})
				if response.Code != http.StatusNoContent {
					t.Fatalf("seal status=%d body=%s", response.Code, response.Body.String())
				}
			},
		},
		{
			name: "envelope",
			stage: func(t *testing.T, device testDevice) {
				_, err := device.server.db.Exec(`INSERT INTO envelopes(
					account_id, device_id, device_seq, version, object_id, key_epoch,
					nonce, ciphertext, signature, record_hash, created_at)
					VALUES(?, ?, 1, 1, ?, 1, ?, ?, ?, ?, ?)`,
					device.accountID, device.deviceID, bytes.Repeat([]byte{0x11}, 16),
					bytes.Repeat([]byte{0x12}, 24), bytes.Repeat([]byte{0x13}, 528),
					bytes.Repeat([]byte{0x14}, 64), bytes.Repeat([]byte{0x15}, 32), time.Now().UnixMilli())
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			device := newTestUnsealedAccount(t)
			defer device.server.Close()
			test.stage(t, device)
			response := apiRequest(t, device.server, http.MethodDelete, "/v1/accounts/"+device.accountID, device.rollbackToken, nil)
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "account_rollback_not_safe") {
				t.Fatalf("unsafe rollback status=%d body=%s", response.Code, response.Body.String())
			}
			var count int
			if err := device.server.db.QueryRow("SELECT COUNT(*) FROM accounts WHERE id = ?", device.accountID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("unsafe rollback changed account: count=%d err=%v", count, err)
			}
		})
	}
}

func TestAccountRollbackRequiresOwningDevice(t *testing.T) {
	first := newTestUnsealedAccount(t)
	defer first.server.Close()
	second := newTestUnsealedAccount(t)
	defer second.server.Close()
	response := apiRequest(t, first.server, http.MethodDelete, "/v1/accounts/"+first.accountID, second.rollbackToken, nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("foreign token status=%d body=%s", response.Code, response.Body.String())
	}
	response = apiRequest(t, first.server, http.MethodDelete, "/v1/accounts/"+strings.Repeat("a", 32), first.rollbackToken, nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("foreign account status=%d body=%s", response.Code, response.Body.String())
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
	if response.Code != http.StatusConflict {
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
	if err := application.db.QueryRow(`SELECT (SELECT COUNT(*) FROM schema_migrations), checksum
		FROM schema_migrations WHERE name = '001_init.sql'`).Scan(&count, &checksum); err != nil {
		t.Fatal(err)
	}
	if count != 2 || checksum != hex.EncodeToString(expected[:]) {
		t.Fatalf("migration ledger mismatch: count=%d checksum=%q", count, checksum)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	application, err = New(context.Background(), databasePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil || count != 2 {
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
