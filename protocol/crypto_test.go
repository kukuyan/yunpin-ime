// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"math"
	"reflect"
	"strings"
	"testing"
)

type fixedReader struct {
	next byte
}

func (reader *fixedReader) Read(target []byte) (int, error) {
	for index := range target {
		target[index] = reader.next
		reader.next++
	}
	return len(target), nil
}

type phrasePayload struct {
	Phrase   string `cbor:"1,keyasint"`
	Pinyin   string `cbor:"2,keyasint"`
	UseCount uint64 `cbor:"3,keyasint"`
}

func TestEnvelopeRoundTripAndTamperDetection(t *testing.T) {
	source := &fixedReader{next: 1}
	keys, err := NewDeviceKeys(source)
	if err != nil {
		t.Fatal(err)
	}
	header := Header{
		AccountID: bytes.Repeat([]byte{0x11}, 16),
		ObjectID:  bytes.Repeat([]byte{0x22}, 16),
		KeyEpoch:  7,
		DeviceID:  bytes.Repeat([]byte{0x33}, 16),
		DeviceSeq: 9,
	}
	epochKey := bytes.Repeat([]byte{0x44}, 32)
	want := phrasePayload{Phrase: "公开测试词组", Pinyin: "gong kai ce shi ci zu", UseCount: 2}
	envelope, err := Seal(epochKey, header, want, keys.Ed25519Private, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope.Ciphertext) != PaddingBucket+16 {
		t.Fatalf("ciphertext should contain one padded bucket plus tag, got %d", len(envelope.Ciphertext))
	}
	var got phrasePayload
	if err := Open(epochKey, envelope, keys.Ed25519Public, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch: got %#v want %#v", got, want)
	}
	tampered := envelope
	tampered.Ciphertext = append([]byte(nil), envelope.Ciphertext...)
	tampered.Ciphertext[3] ^= 0x80
	if err := Open(epochKey, tampered, keys.Ed25519Public, &got); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

func TestHeaderSequenceAndEpochUseSignedInt64Range(t *testing.T) {
	valid := Header{
		Version: ProtocolVersion, AccountID: bytes.Repeat([]byte{1}, 16), ObjectID: bytes.Repeat([]byte{2}, 16),
		KeyEpoch: 1, DeviceID: bytes.Repeat([]byte{3}, 16), DeviceSeq: 1, Nonce: bytes.Repeat([]byte{4}, 24),
	}
	if err := validateHeader(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Header){
		"zero sequence":     func(header *Header) { header.DeviceSeq = 0 },
		"overflow sequence": func(header *Header) { header.DeviceSeq = uint64(math.MaxInt64) + 1 },
		"zero epoch":        func(header *Header) { header.KeyEpoch = 0 },
		"overflow epoch":    func(header *Header) { header.KeyEpoch = uint64(math.MaxInt64) + 1 },
	} {
		t.Run(name, func(t *testing.T) {
			header := valid
			mutate(&header)
			if err := validateHeader(header); err == nil {
				t.Fatal("out-of-range header was accepted")
			}
		})
	}
}

func TestEnvelopePayloadAndWireLimitsAlign(t *testing.T) {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x61}, ed25519.SeedSize))
	header := Header{
		AccountID: bytes.Repeat([]byte{1}, 16), ObjectID: bytes.Repeat([]byte{2}, 16),
		KeyEpoch: 1, DeviceID: bytes.Repeat([]byte{3}, 16), DeviceSeq: 1,
	}
	// Canonical CBOR adds a five-byte byte-string prefix at this size.
	payload := bytes.Repeat([]byte{0x44}, MaxEnvelopePayload-5)
	envelope, err := Seal(bytes.Repeat([]byte{0x55}, 32), header, payload, private, &fixedReader{next: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope.Ciphertext) != MaxEnvelopeCiphertext {
		t.Fatalf("maximum envelope ciphertext=%d want=%d", len(envelope.Ciphertext), MaxEnvelopeCiphertext)
	}
	wire, err := envelope.ToWire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EnvelopeFromWire(header.AccountID, header.DeviceID, wire); err != nil {
		t.Fatal(err)
	}
	if _, err := Seal(bytes.Repeat([]byte{0x55}, 32), header, append(payload, 0), private, &fixedReader{next: 1}); err == nil {
		t.Fatal("oversized canonical payload was sealed")
	}

	misaligned := envelope
	misaligned.Ciphertext = append([]byte(nil), envelope.Ciphertext[:len(envelope.Ciphertext)-1]...)
	if _, err := misaligned.ToWire(); err == nil {
		t.Fatal("misaligned ciphertext was serialized")
	}
	oversized := envelope
	oversized.Ciphertext = append(append([]byte(nil), envelope.Ciphertext...), bytes.Repeat([]byte{0}, PaddingBucket)...)
	if _, err := oversized.ToWire(); err == nil {
		t.Fatal("oversized ciphertext was serialized")
	}
	wire.Ciphertext = "AA"
	if _, err := EnvelopeFromWire(header.AccountID, header.DeviceID, wire); err == nil {
		t.Fatal("undersized wire ciphertext was parsed")
	}
}

func TestCanonicalHeaderAndSignatureGoldenMatchesRelay(t *testing.T) {
	header := Header{
		Version:      ProtocolVersion,
		AccountID:    bytes.Repeat([]byte{0x11}, 16),
		ObjectID:     bytes.Repeat([]byte{0x22}, 16),
		KeyEpoch:     7,
		DeviceID:     bytes.Repeat([]byte{0x33}, 16),
		DeviceSeq:    9,
		PreviousHash: bytes.Repeat([]byte{0x55}, 32),
		Nonce:        bytes.Repeat([]byte{0x66}, 24),
	}
	encoded, err := canonicalHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	wantHeader := "a80101025011111111111111111111111111111111035022222222222222222222222222222222040705503333333333333333333333333333333306090758205555555555555555555555555555555555555555555555555555555555555555085818666666666666666666666666666666666666666666666666"
	if got := hex.EncodeToString(encoded); got != wantHeader {
		t.Fatalf("relay-compatible header changed: got %s want %s", got, wantHeader)
	}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x77}, ed25519.SeedSize))
	ciphertext := bytes.Repeat([]byte{0x88}, PaddingBucket+16)
	signed := append(append([]byte(nil), encoded...), ciphertext...)
	wantSignature := "1e48dc8d6c9e3f52a3b137e4b0a627430f14014a71b529bb9a6be04a9064d4c61f0c30b928157affa35a097249532f9db4f0896e2bf4717d4e4ab3a5776a460a"
	if got := hex.EncodeToString(ed25519.Sign(privateKey, signed)); got != wantSignature {
		t.Fatalf("relay-compatible signature changed: %s", got)
	}
}

func TestWireEnvelopeRoundTrip(t *testing.T) {
	source := &fixedReader{next: 17}
	keys, err := NewDeviceKeys(source)
	if err != nil {
		t.Fatal(err)
	}
	header := Header{
		AccountID:    bytes.Repeat([]byte{0x10}, 16),
		ObjectID:     bytes.Repeat([]byte{0x20}, 16),
		KeyEpoch:     4,
		DeviceID:     bytes.Repeat([]byte{0x30}, 16),
		DeviceSeq:    2,
		PreviousHash: bytes.Repeat([]byte{0x40}, 32),
	}
	envelope, err := Seal(bytes.Repeat([]byte{0x50}, 32), header, phrasePayload{Phrase: "合成线缆测试"}, keys.Ed25519Private, source)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := envelope.ToWire()
	if err != nil {
		t.Fatal(err)
	}
	if wire.DeviceID != "" {
		t.Fatal("upload wire unexpectedly disclosed its authenticated device ID")
	}
	restored, err := EnvelopeFromWire(header.AccountID, header.DeviceID, wire)
	if err != nil {
		t.Fatal(err)
	}
	var payload phrasePayload
	if err := Open(bytes.Repeat([]byte{0x50}, 32), restored, keys.Ed25519Public, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Phrase != "合成线缆测试" {
		t.Fatalf("wire payload mismatch: %#v", payload)
	}
	wire.DeviceID = hex.EncodeToString(header.DeviceID)
	downloaded, err := EnvelopeFromDownload(header.AccountID, wire)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(downloaded.Header.DeviceID, header.DeviceID) {
		t.Fatal("download helper did not restore the source device ID")
	}
	for name, invalid := range map[string]string{
		"missing": "", "uppercase": "A" + wire.DeviceID[1:], "short": wire.DeviceID[:30], "non-hex": strings.Repeat("z", 32),
	} {
		t.Run("download-device-"+name, func(t *testing.T) {
			bad := wire
			bad.DeviceID = invalid
			if _, err := EnvelopeFromDownload(header.AccountID, bad); err == nil {
				t.Fatal("invalid downloaded device ID was accepted")
			}
		})
	}
}

func TestStableOpaqueObjectID(t *testing.T) {
	key := bytes.Repeat([]byte{0x55}, 32)
	first, err := OpaqueObjectID(key, "ＡＢＣ", "  ZHONG   GUO ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpaqueObjectID(key, "ABC", "zhong guo")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("NFKC and Pinyin whitespace normalization must be stable")
	}
	if got := hex.EncodeToString(first[:]); got != "2038d52367a29325df5cc0c78bcecff8" {
		t.Fatalf("golden object ID changed: %s", got)
	}
}

func TestOpaqueObjectIDUsesProtocolWidePinyinCanonicalization(t *testing.T) {
	key := bytes.Repeat([]byte{0x41}, 32)
	variants := []string{"nǚ'ér", "nu:3 er2", "NV ER", "nü，ér"}
	var first [16]byte
	for index, pinyin := range variants {
		got, err := OpaqueObjectID(key, " 女\u3000儿 ", pinyin)
		if err != nil {
			t.Fatalf("variant %q: %v", pinyin, err)
		}
		if index == 0 {
			first = got
		} else if got != first {
			t.Fatalf("variant %q produced a different object ID", pinyin)
		}
	}
	if got := CanonicalPinyin(" lǜ4-se  '  xī1 ān1 "); got != "lv se xi an" {
		t.Fatalf("unexpected canonical Pinyin %q", got)
	}
	if _, err := OpaqueObjectID(key, "女儿", "---"); err == nil {
		t.Fatal("empty canonical Pinyin was accepted")
	}
}

func TestRecoveryKeyRoundTripAndChecksum(t *testing.T) {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index)
	}
	encoded, err := EncodeRecoveryKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if encoded != "yprec1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0su339c6" {
		t.Fatalf("golden recovery text changed: %s", encoded)
	}
	decoded, err := DecodeRecoveryKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, key) {
		t.Fatal("recovery round trip mismatch")
	}
	mutated := encoded[:len(encoded)-1] + "q"
	if _, err := DecodeRecoveryKey(mutated); err == nil {
		t.Fatal("bad checksum was accepted")
	}
}

func TestX25519PairingAgreement(t *testing.T) {
	alice, err := NewDeviceKeys(&fixedReader{next: 7})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := NewDeviceKeys(&fixedReader{next: 91})
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{0x5a}, PairingSecretSize)
	transcript := PairingTranscript{
		PairingID: bytes.Repeat([]byte{1}, 16), AccountID: bytes.Repeat([]byte{4}, 16), CreatorDeviceID: bytes.Repeat([]byte{2}, 16),
		JoiningDeviceID: bytes.Repeat([]byte{3}, 16), CreatorEd25519PublicKey: alice.Ed25519Public,
		JoiningEd25519PublicKey: bob.Ed25519Public, CreatorX25519PublicKey: alice.X25519Public,
		JoiningX25519PublicKey: bob.X25519Public,
	}
	left, err := DerivePairingKey(alice.X25519Private, bob.X25519Public, secret, transcript)
	if err != nil {
		t.Fatal(err)
	}
	right, err := DerivePairingKey(bob.X25519Private, alice.X25519Public, secret, transcript)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatal("pairing parties derived different keys")
	}
	if len(keysOrPanic(t, alice.Ed25519Private)) != ed25519.PrivateKeySize {
		t.Fatal("invalid signing key")
	}
	verifier, err := PairingRelayVerifier(secret, transcript.PairingID)
	if err != nil || len(verifier) != 32 || bytes.Equal(verifier, secret) {
		t.Fatal("pairing relay verifier is invalid or disclosed the PSK")
	}
	proof, err := PairingJoinProof(secret, transcript)
	if err != nil || VerifyPairingJoinProof(secret, transcript, proof) != nil {
		t.Fatal("pairing join proof did not verify")
	}
	substituted := transcript
	substituted.JoiningX25519PublicKey = alice.X25519Public
	if VerifyPairingJoinProof(secret, substituted, proof) == nil {
		t.Fatal("relay public-key substitution retained a valid join proof")
	}
	deviceToken := "synthetic-device-token"
	claimProof, err := PairingClaimProof(transcript, deviceToken, bob.Ed25519Private)
	if err != nil || VerifyPairingClaimProof(transcript, deviceToken, bob.Ed25519Public, claimProof) != nil {
		t.Fatal("pairing claim proof did not verify")
	}
	if VerifyPairingClaimProof(transcript, deviceToken+"-changed", bob.Ed25519Public, claimProof) == nil {
		t.Fatal("pairing claim proof was not bound to the device token")
	}
	if VerifyPairingClaimProof(transcript, deviceToken, alice.Ed25519Public, claimProof) == nil {
		t.Fatal("pairing claim proof accepted the wrong signing device")
	}
	wrongSecret := append([]byte(nil), secret...)
	wrongSecret[0] ^= 1
	if _, err := DerivePairingKey(alice.X25519Private, bob.X25519Public, wrongSecret, transcript); err != nil {
		t.Fatal(err)
	} else if wrong, _ := DerivePairingKey(alice.X25519Private, bob.X25519Public, wrongSecret, transcript); bytes.Equal(left, wrong) {
		t.Fatal("different pairing PSK derived the same key")
	}
}

func keysOrPanic(t *testing.T, key []byte) []byte {
	t.Helper()
	return key
}

func TestRecoveryDomainSeparation(t *testing.T) {
	recovery := bytes.Repeat([]byte{0xa7}, 32)
	encryption, authentication, err := DeriveRecoveryKeys(recovery)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(encryption, authentication) {
		t.Fatal("recovery encryption and authentication keys are not separated")
	}
}
