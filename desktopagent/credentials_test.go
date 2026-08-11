// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"reflect"
	"testing"
)

func filled16(value byte) [16]byte {
	var result [16]byte
	for index := range result {
		result[index] = value
	}
	return result
}

func filled32(value byte) [32]byte {
	var result [32]byte
	for index := range result {
		result[index] = value
	}
	return result
}

func testCredentials() CredentialBundleV1 {
	account := filled16(0x11)
	device := filled16(0x22)
	seed := filled32(0x33)
	public := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	var self [ed25519.PublicKeySize]byte
	copy(self[:], public)
	return CredentialBundleV1{
		Version:       CredentialBundleVersion,
		AccountID:     account,
		DeviceID:      device,
		DeviceToken:   []byte("synthetic_device_token_123456789"),
		SigningSeed:   seed,
		X25519Private: filled32(0x44),
		LocalDataKey:  filled32(0x55),
		ObjectIDKey:   filled32(0x66),
		CurrentEpoch:  2,
		EpochKeys: map[uint64][32]byte{
			2: filled32(0x72),
			1: filled32(0x71),
		},
		VerificationKeys: map[[16]byte][ed25519.PublicKeySize]byte{device: self},
	}
}

func TestCredentialBundleCanonicalRoundTrip(t *testing.T) {
	want := testCredentials()
	encoded, err := EncodeCredentialBundle(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCredentialBundle(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got=%#v\nwant=%#v", got, want)
	}

	reordered := testCredentials()
	reordered.EpochKeys = map[uint64][32]byte{1: filled32(0x71), 2: filled32(0x72)}
	second, err := EncodeCredentialBundle(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, second) {
		t.Fatal("map insertion order changed the canonical credential encoding")
	}
}

func TestCredentialBundleRejectsMalformedRecords(t *testing.T) {
	bundle := testCredentials()
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string][]byte{
		"empty":     nil,
		"truncated": append([]byte(nil), encoded[:len(encoded)-1]...),
		"trailing":  append(append([]byte(nil), encoded...), 0),
		"bad magic": append([]byte(nil), encoded...),
		"version":   append([]byte(nil), encoded...),
	}
	mutations["bad magic"][0] ^= 0xff
	mutations["version"][4] = 9
	for name, candidate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCredentialBundle(candidate); err == nil {
				t.Fatal("malformed credential was accepted")
			}
		})
	}

	// The first variable-width field is the device token. Each epoch record is
	// exactly 8 bytes of ID plus 32 bytes of key. Replacing epoch 2's ID with
	// epoch 1 produces a duplicate canonical map key.
	epochStart := 4 + 1 + 16 + 16 + 2 + len(bundle.DeviceToken) + 32*4 + 8 + 2
	duplicateEpoch := append([]byte(nil), encoded...)
	copy(duplicateEpoch[epochStart+40:epochStart+48], duplicateEpoch[epochStart:epochStart+8])
	if _, err := DecodeCredentialBundle(duplicateEpoch); err == nil {
		t.Fatal("duplicate epoch key was accepted")
	}

	zeroEpoch := append([]byte(nil), encoded...)
	binary.BigEndian.PutUint64(zeroEpoch[epochStart:epochStart+8], 0)
	if _, err := DecodeCredentialBundle(zeroEpoch); err == nil {
		t.Fatal("zero epoch key ID was accepted")
	}
}

func TestCredentialBundleValidatesSelfTrustAndToken(t *testing.T) {
	wrongSelf := testCredentials()
	wrongSelf.VerificationKeys[wrongSelf.DeviceID] = filled32(0x99)
	if _, err := EncodeCredentialBundle(wrongSelf); err == nil {
		t.Fatal("signing seed and verification-key mismatch was accepted")
	}
	badToken := testCredentials()
	badToken.DeviceToken = []byte("synthetic token with spaces")
	if _, err := EncodeCredentialBundle(badToken); err == nil {
		t.Fatal("unsafe device token was accepted")
	}
	missingCurrent := testCredentials()
	delete(missingCurrent.EpochKeys, missingCurrent.CurrentEpoch)
	if _, err := EncodeCredentialBundle(missingCurrent); err == nil {
		t.Fatal("missing current epoch was accepted")
	}
}

func TestCredentialBundleZeroClearsMutableSecrets(t *testing.T) {
	bundle := testCredentials()
	bundle.Zero()
	if bundle.DeviceToken != nil || bundle.CurrentEpoch != 0 ||
		len(bundle.EpochKeys) != 0 || len(bundle.VerificationKeys) != 0 ||
		!allZero(bundle.SigningSeed[:]) || !allZero(bundle.LocalDataKey[:]) ||
		!allZero(bundle.ObjectIDKey[:]) || !allZero(bundle.X25519Private[:]) {
		t.Fatal("credential bundle retained mutable secret material")
	}
}

func FuzzDecodeCredentialBundle(fuzz *testing.F) {
	encoded, err := EncodeCredentialBundle(testCredentials())
	if err != nil {
		fuzz.Fatal(err)
	}
	fuzz.Add(encoded)
	fuzz.Add([]byte("not-a-credential"))
	fuzz.Fuzz(func(t *testing.T, value []byte) {
		bundle, err := DecodeCredentialBundle(value)
		if err == nil {
			if validateErr := bundle.Validate(); validateErr != nil {
				t.Fatalf("decoder returned invalid credential: %v", validateErr)
			}
			bundle.Zero()
		}
	})
}
