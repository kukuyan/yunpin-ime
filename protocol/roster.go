// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
)

// MaxChainedRosterDevices fits a worst-case creator recovery journal (original
// credential, proposed credential and encrypted grant) in existing 64 KiB OS
// secret slots, including 64 epochs and a maximum-size device token.
const MaxChainedRosterDevices = 128

// PairingRosterDigest names an exact signed checkpoint, including its signature.
// The v1 two-device checkpoint remains the local trust anchor during migration.
func PairingRosterDigest(roster PairingRoster) ([]byte, error) {
	if err := VerifyPairingRoster(roster); err != nil {
		return nil, err
	}
	encoded, err := canonicalCBOR.Marshal(roster)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return append([]byte(nil), digest[:]...), nil
}

// VerifyPairingRosterAdvance authorizes only one add against an already trusted
// checkpoint. Self-contained signature verification is not authorization: every
// old member/key must survive unchanged and the signer must be an old member.
func VerifyPairingRosterAdvance(previous, next PairingRoster) error {
	digest, err := PairingRosterDigest(previous)
	if err != nil {
		return err
	}
	if err := VerifyPairingRoster(next); err != nil {
		return err
	}
	if next.Version != previous.Version+1 || !bytes.Equal(next.PreviousHash, digest) ||
		!bytes.Equal(next.AccountID, previous.AccountID) || len(next.Devices) != len(previous.Devices)+1 || len(next.Devices) > MaxChainedRosterDevices {
		return errors.New("roster update does not extend the trusted checkpoint by one device")
	}
	signerTrusted := false
	for _, old := range previous.Devices {
		preserved := false
		for _, device := range next.Devices {
			if bytes.Equal(old.DeviceID, device.DeviceID) {
				preserved = bytes.Equal(old.Ed25519PublicKey, device.Ed25519PublicKey) &&
					bytes.Equal(old.X25519PublicKey, device.X25519PublicKey)
				break
			}
		}
		if !preserved {
			return errors.New("roster update removes or changes a trusted device")
		}
		if bytes.Equal(old.DeviceID, next.SignerDeviceID) {
			signerTrusted = true
		}
	}
	if !signerTrusted {
		return errors.New("roster update signer is not previously trusted")
	}
	return nil
}

func SignPairingRosterAdvance(previous PairingRoster, joining PairingRosterDevice,
	signerDeviceID []byte, private ed25519.PrivateKey) (PairingRoster, error) {
	digest, err := PairingRosterDigest(previous)
	if err != nil {
		return PairingRoster{}, err
	}
	devices := append(append([]PairingRosterDevice(nil), previous.Devices...), joining)
	next, err := signPairingRoster(previous.AccountID, previous.Version+1, devices, signerDeviceID, digest, private)
	if err == nil {
		err = VerifyPairingRosterAdvance(previous, next)
	}
	if err != nil {
		return PairingRoster{}, err
	}
	return next, nil
}
