// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

func rosterFixture(t *testing.T) (PairingRoster, PairingRosterDevice, ed25519.PrivateKey) {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, 32))
	devices := []PairingRosterDevice{
		{DeviceID: bytes.Repeat([]byte{1}, 16), Ed25519PublicKey: private.Public().(ed25519.PublicKey), X25519PublicKey: bytes.Repeat([]byte{0x21}, 32)},
		{DeviceID: bytes.Repeat([]byte{2}, 16), Ed25519PublicKey: bytes.Repeat([]byte{0x32}, 32), X25519PublicKey: bytes.Repeat([]byte{0x22}, 32)},
	}
	anchor, err := SignPairingRoster(bytes.Repeat([]byte{0x10}, 16), 1, devices, devices[0].DeviceID, private)
	if err != nil {
		t.Fatal(err)
	}
	joining := PairingRosterDevice{DeviceID: bytes.Repeat([]byte{3}, 16), Ed25519PublicKey: bytes.Repeat([]byte{0x33}, 32), X25519PublicKey: bytes.Repeat([]byte{0x23}, 32)}
	return anchor, joining, private
}

func TestRosterChainCanonicalGolden(t *testing.T) {
	anchor, joining, private := rosterFixture(t)
	next, err := SignPairingRosterAdvance(anchor, joining, anchor.SignerDeviceID, private)
	if err != nil {
		t.Fatal(err)
	}
	for _, roster := range []PairingRoster{anchor, next} {
		digest, err := PairingRosterDigest(roster)
		if err != nil {
			t.Fatal(err)
		}
		want := map[uint64]string{1: "69697faa4d4e78eb58ddaf9a5e246d8cd50d16193ce97b247c37187d31ce5385", 2: "924b2c7a5c606f6cbfeaa85cece5919bd5c9a7e09a6f828d268c78b8e9b5dea3"}
		if hex.EncodeToString(digest) != want[roster.Version] {
			t.Fatal("signed roster canonical digest changed")
		}
	}
}

func TestRosterAdvanceRejectsValidlySignedTrustReplacement(t *testing.T) {
	anchor, joining, private := rosterFixture(t)
	digest, _ := PairingRosterDigest(anchor)
	for _, mutation := range []string{"replace_key", "remove_member", "new_signer", "skip_version", "wrong_account", "wrong_parent"} {
		t.Run(mutation, func(t *testing.T) {
			devices := append(append([]PairingRosterDevice(nil), anchor.Devices...), joining)
			account := append([]byte(nil), anchor.AccountID...)
			previous := append([]byte(nil), digest...)
			version := uint64(2)
			signer := anchor.SignerDeviceID
			key := private
			switch mutation {
			case "replace_key":
				devices[1].Ed25519PublicKey = bytes.Repeat([]byte{0x55}, 32)
			case "remove_member":
				devices = devices[:2]
			case "new_signer":
				key = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x44}, 32))
				signer = joining.DeviceID
				devices[2].Ed25519PublicKey = key.Public().(ed25519.PublicKey)
			case "skip_version":
				version = 3
			case "wrong_account":
				account[0] ^= 1
			case "wrong_parent":
				previous[0] ^= 1
			}
			next, err := signPairingRoster(account, version, devices, signer, previous, key)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyPairingRoster(next); err != nil {
				t.Fatal(err)
			}
			if err := VerifyPairingRosterAdvance(anchor, next); err == nil {
				t.Fatal("unauthorized but signed change was accepted")
			}
		})
	}
	next, err := SignPairingRosterAdvance(anchor, joining, anchor.SignerDeviceID, private)
	if err != nil {
		t.Fatal(err)
	}
	next.PreviousHash[0] ^= 1
	if err := VerifyPairingRoster(next); err == nil {
		t.Fatal("predecessor was not covered by the signature")
	}
	if err := VerifyPairingRosterAdvance(anchor, anchor); err == nil {
		t.Fatal("duplicate checkpoint was accepted as progress")
	}
}
