// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestPairingBoxRoundTrip(t *testing.T) {
	alice, err := NewDeviceKeys(&fixedReader{next: 4})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := NewDeviceKeys(&fixedReader{next: 104})
	if err != nil {
		t.Fatal(err)
	}
	transcript := PairingTranscript{
		PairingID: bytes.Repeat([]byte{0x70}, 16), AccountID: bytes.Repeat([]byte{0x73}, 16), CreatorDeviceID: bytes.Repeat([]byte{0x71}, 16),
		JoiningDeviceID: bytes.Repeat([]byte{0x72}, 16), CreatorEd25519PublicKey: alice.Ed25519Public,
		JoiningEd25519PublicKey: bob.Ed25519Public, CreatorX25519PublicKey: alice.X25519Public,
		JoiningX25519PublicKey: bob.X25519Public,
	}
	roster, err := SignPairingRoster(transcript.AccountID, 1, []PairingRosterDevice{
		{DeviceID: transcript.JoiningDeviceID, Ed25519PublicKey: bob.Ed25519Public, X25519PublicKey: bob.X25519Public},
		{DeviceID: transcript.CreatorDeviceID, Ed25519PublicKey: alice.Ed25519Public, X25519PublicKey: alice.X25519Public},
	}, transcript.CreatorDeviceID, alice.Ed25519Private)
	if err != nil {
		t.Fatal(err)
	}
	want := PairingPackage{
		CurrentEpoch: 2, EpochKeys: []PairingEpochKey{
			{Epoch: 1, Key: bytes.Repeat([]byte{0x81}, 32)}, {Epoch: 2, Key: bytes.Repeat([]byte{0x82}, 32)},
		}, ObjectIDKey: bytes.Repeat([]byte{0x83}, 32), Roster: roster,
	}
	secret := bytes.Repeat([]byte{0x77}, PairingSecretSize)
	box, err := SealPairingPackage(alice.X25519Private, bob.X25519Public, secret, transcript, want, &fixedReader{next: 33})
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenPairingPackage(bob.X25519Private, alice.X25519Public, secret, transcript, box)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pairing payload mismatch: got=%#v want=%#v", got, want)
	}
	box.Ciphertext[0] ^= 1
	if _, err := OpenPairingPackage(bob.X25519Private, alice.X25519Public, secret, transcript, box); err == nil {
		t.Fatal("tampered pairing box was accepted")
	}
	attacker, err := NewDeviceKeys(&fixedReader{next: 204})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPairingPackage(attacker.X25519Private, alice.X25519Public, secret, transcript, box); err == nil {
		t.Fatal("relay-substituted private key opened the pairing package")
	}
}

func TestSealedBoxWireGoldenAndStrictParsing(t *testing.T) {
	box := SealedBox{Nonce: bytes.Repeat([]byte{0x11}, 24), Ciphertext: bytes.Repeat([]byte{0x22}, 16)}
	wire, err := EncodeSealedBox(box)
	if err != nil {
		t.Fatal(err)
	}
	want := "WVBCWAEAAAAQERERERERERERERERERERERERERERERERIiIiIiIiIiIiIiIiIiIiIg"
	if wire != want {
		t.Fatalf("golden sealed-box wire changed: %s", wire)
	}
	decoded, err := DecodeSealedBox(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, box) {
		t.Fatalf("sealed-box wire round trip mismatch: got=%#v want=%#v", decoded, box)
	}

	raw, err := base64.RawURLEncoding.DecodeString(wire)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"padded":    wire + "=",
		"truncated": base64.RawURLEncoding.EncodeToString(raw[:len(raw)-1]),
		"trailing":  base64.RawURLEncoding.EncodeToString(append(append([]byte(nil), raw...), 0)),
	}
	badVersion := append([]byte(nil), raw...)
	badVersion[4] = 2
	tests["version"] = base64.RawURLEncoding.EncodeToString(badVersion)
	badLength := append([]byte(nil), raw...)
	binary.BigEndian.PutUint32(badLength[5:9], uint32(len(box.Ciphertext)+1))
	tests["declared-length"] = base64.RawURLEncoding.EncodeToString(badLength)
	for name, malformed := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSealedBox(malformed); err == nil {
				t.Fatal("malformed sealed-box wire was accepted")
			}
		})
	}
	if _, err := EncodeSealedBox(SealedBox{Nonce: box.Nonce, Ciphertext: []byte{1}}); err == nil {
		t.Fatal("too-short AEAD ciphertext was encoded")
	}
	maximum := SealedBox{Nonce: box.Nonce, Ciphertext: bytes.Repeat([]byte{0x33}, MaxSealedBoxCiphertextSize)}
	maximumWire, err := EncodeSealedBox(maximum)
	if err != nil {
		t.Fatal(err)
	}
	maximumRaw, err := base64.RawURLEncoding.DecodeString(maximumWire)
	if err != nil {
		t.Fatal(err)
	}
	if len(maximumRaw) != MaxSealedBoxWireSize {
		t.Fatalf("maximum sealed-box wire=%d want=%d", len(maximumRaw), MaxSealedBoxWireSize)
	}
	if _, err := DecodeSealedBox(maximumWire); err != nil {
		t.Fatal(err)
	}
	maximum.Ciphertext = append(maximum.Ciphertext, 0)
	if _, err := EncodeSealedBox(maximum); err == nil {
		t.Fatal("sealed-box wire larger than 256 KiB was encoded")
	}
}

func TestRecoveryPackageAndAuthenticationSeparation(t *testing.T) {
	recoveryKey := bytes.Repeat([]byte{0x19}, 32)
	want := RecoveryPackage{
		AccountID:    bytes.Repeat([]byte{0x20}, 16),
		CurrentEpoch: 5,
		EpochKey:     bytes.Repeat([]byte{0x30}, 32),
		ObjectIDKey:  bytes.Repeat([]byte{0x40}, 32),
	}
	box, err := SealRecoveryPackage(recoveryKey, want, &fixedReader{next: 55})
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenRecoveryPackage(recoveryKey, box)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recovery package mismatch: got=%#v want=%#v", got, want)
	}
	auth, err := RecoveryAuthentication(recoveryKey)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "cMckcJzbHXSLzoCnhA3qLl0JvoLHgtn2lwjRdtWuLKw" {
		t.Fatalf("golden recovery authentication changed: %s", auth)
	}
	if _, err := OpenRecoveryPackage(bytes.Repeat([]byte{0x18}, 32), box); err == nil {
		t.Fatal("wrong recovery key opened package")
	}
}
