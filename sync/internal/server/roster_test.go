// SPDX-License-Identifier: Apache-2.0
package server

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

// Values are shared with protocol.TestRosterChainCanonicalGolden. Keeping
// verification in the independently built relay catches codec/domain drift.
func TestRosterCanonicalGoldenMatchesProtocol(t *testing.T) {
	decode := func(value string) []byte {
		result, err := hex.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	public := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, 32)).Public().(ed25519.PublicKey)
	anchor := signedRoster{Version: 1, AccountID: bytes.Repeat([]byte{0x10}, 16), SignerDeviceID: bytes.Repeat([]byte{1}, 16), Devices: []rosterDevice{
		{DeviceID: bytes.Repeat([]byte{1}, 16), Ed25519PublicKey: public, X25519PublicKey: bytes.Repeat([]byte{0x21}, 32)},
		{DeviceID: bytes.Repeat([]byte{2}, 16), Ed25519PublicKey: bytes.Repeat([]byte{0x32}, 32), X25519PublicKey: bytes.Repeat([]byte{0x22}, 32)},
	}, Signature: decode("c4a6b6f50275c400710493cca12a89f36dfd04dc2e6efd3f662e8d412f44c8e645196224aa875a000e05db608ab080b8ceaa3933a2dc7a69aeded4b038da650b")}
	digest, err := rosterDigest(anchor)
	if err != nil || hex.EncodeToString(digest) != "69697faa4d4e78eb58ddaf9a5e246d8cd50d16193ce97b247c37187d31ce5385" {
		t.Fatalf("v1 canonical golden: %v", err)
	}
	next := anchor
	next.Version = 2
	next.PreviousHash = digest
	next.Devices = append(append([]rosterDevice(nil), anchor.Devices...), rosterDevice{DeviceID: bytes.Repeat([]byte{3}, 16), Ed25519PublicKey: bytes.Repeat([]byte{0x33}, 32), X25519PublicKey: bytes.Repeat([]byte{0x23}, 32)})
	next.Signature = decode("d224438123a748c6e96df87e951e4df06455b12a30ce639bec96ca7b8baea07f0cbd6eb835387514085d7bce6e2d4cd12e7c1cd384571c127b8426ae948b7901")
	digest, err = rosterDigest(next)
	if err != nil || hex.EncodeToString(digest) != "924b2c7a5c606f6cbfeaa85cece5919bd5c9a7e09a6f828d268c78b8e9b5dea3" {
		t.Fatalf("chain canonical golden: %v", err)
	}
	if err := verifyRosterAdvance(anchor, next); err != nil {
		t.Fatal(err)
	}
	next.PreviousHash[0] ^= 1
	if _, err := rosterDigest(next); err == nil {
		t.Fatal("relay accepted an unsigned predecessor change")
	}
}
