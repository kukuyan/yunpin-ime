// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/fxamacker/cbor/v2"
	"golang.org/x/crypto/chacha20poly1305"
)

type SealedBox struct {
	Nonce      []byte `json:"nonce" cbor:"1,keyasint"`
	Ciphertext []byte `json:"ciphertext" cbor:"2,keyasint"`
}

const (
	SealedBoxWireVersion       = 1
	sealedBoxWireHeaderSize    = 4 + 1 + 4 + chacha20poly1305.NonceSizeX
	MaxSealedBoxWireSize       = 256 * 1024
	MaxSealedBoxCiphertextSize = MaxSealedBoxWireSize - sealedBoxWireHeaderSize
)

var sealedBoxWireMagic = []byte{'Y', 'P', 'B', 'X'}

// EncodeSealedBox serializes a box as one unpadded base64url blob suitable for
// sync encrypted_keyring/ciphertext fields. The decoded binary layout is:
// "YPBX" || version:u8 || ciphertext_length:u32be || nonce:24 || ciphertext.
func EncodeSealedBox(box SealedBox) (string, error) {
	if len(box.Nonce) != chacha20poly1305.NonceSizeX {
		return "", errors.New("sealed box nonce must be 24 bytes")
	}
	if len(box.Ciphertext) < chacha20poly1305.Overhead || len(box.Ciphertext) > MaxSealedBoxCiphertextSize {
		return "", errors.New("sealed box ciphertext length is invalid")
	}
	wire := make([]byte, sealedBoxWireHeaderSize+len(box.Ciphertext))
	copy(wire[:4], sealedBoxWireMagic)
	wire[4] = SealedBoxWireVersion
	binary.BigEndian.PutUint32(wire[5:9], uint32(len(box.Ciphertext)))
	copy(wire[9:sealedBoxWireHeaderSize], box.Nonce)
	copy(wire[sealedBoxWireHeaderSize:], box.Ciphertext)
	return base64.RawURLEncoding.EncodeToString(wire), nil
}

// DecodeSealedBox strictly parses the public v1 representation. Padded base64,
// unknown versions, truncation, declared-length mismatch, and trailing bytes
// are rejected so different clients cannot interpret one blob differently.
func DecodeSealedBox(encoded string) (SealedBox, error) {
	maximumEncoded := base64.RawURLEncoding.EncodedLen(MaxSealedBoxWireSize)
	if encoded == "" || len(encoded) > maximumEncoded {
		return SealedBox{}, errors.New("sealed box wire length is invalid")
	}
	wire, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return SealedBox{}, errors.New("sealed box wire is not unpadded base64url")
	}
	if len(wire) < sealedBoxWireHeaderSize+chacha20poly1305.Overhead {
		return SealedBox{}, errors.New("sealed box wire is truncated")
	}
	if !bytes.Equal(wire[:4], sealedBoxWireMagic) {
		return SealedBox{}, errors.New("sealed box wire magic is invalid")
	}
	if wire[4] != SealedBoxWireVersion {
		return SealedBox{}, fmt.Errorf("unsupported sealed box wire version %d", wire[4])
	}
	ciphertextLength := uint64(binary.BigEndian.Uint32(wire[5:9]))
	if ciphertextLength < chacha20poly1305.Overhead || ciphertextLength > MaxSealedBoxCiphertextSize {
		return SealedBox{}, errors.New("sealed box ciphertext length is invalid")
	}
	expectedLength := uint64(sealedBoxWireHeaderSize) + ciphertextLength
	if uint64(len(wire)) != expectedLength {
		return SealedBox{}, errors.New("sealed box wire has a length mismatch or trailing bytes")
	}
	return SealedBox{
		Nonce:      append([]byte(nil), wire[9:sealedBoxWireHeaderSize]...),
		Ciphertext: append([]byte(nil), wire[sealedBoxWireHeaderSize:]...),
	}, nil
}

type RecoveryPackage struct {
	AccountID    []byte `json:"account_id" cbor:"1,keyasint"`
	CurrentEpoch uint64 `json:"current_epoch" cbor:"2,keyasint"`
	EpochKey     []byte `json:"epoch_key" cbor:"3,keyasint"`
	ObjectIDKey  []byte `json:"object_id_key" cbor:"4,keyasint"`
}

type PairingRosterDevice struct {
	DeviceID         []byte `json:"device_id" cbor:"1,keyasint"`
	Ed25519PublicKey []byte `json:"ed25519_public_key" cbor:"2,keyasint"`
	X25519PublicKey  []byte `json:"x25519_public_key" cbor:"3,keyasint"`
}

// PairingRoster is a versioned account trust statement signed by an already
// trusted device. The relay's /devices response is operational metadata only;
// it is never accepted as a verification-key trust root.
type PairingRoster struct {
	Version        uint64                `json:"version" cbor:"1,keyasint"`
	AccountID      []byte                `json:"account_id" cbor:"2,keyasint"`
	Devices        []PairingRosterDevice `json:"devices" cbor:"3,keyasint"`
	SignerDeviceID []byte                `json:"signer_device_id" cbor:"4,keyasint"`
	Signature      []byte                `json:"signature" cbor:"5,keyasint"`
}

type PairingEpochKey struct {
	Epoch uint64 `json:"epoch" cbor:"1,keyasint"`
	Key   []byte `json:"key" cbor:"2,keyasint"`
}

type PairingPackage struct {
	CurrentEpoch uint64            `json:"current_epoch" cbor:"1,keyasint"`
	EpochKeys    []PairingEpochKey `json:"epoch_keys" cbor:"2,keyasint"`
	ObjectIDKey  []byte            `json:"object_id_key" cbor:"3,keyasint"`
	Roster       PairingRoster     `json:"roster" cbor:"4,keyasint"`
}

type pairingRosterUnsigned struct {
	Version        uint64                `cbor:"1,keyasint"`
	AccountID      []byte                `cbor:"2,keyasint"`
	Devices        []PairingRosterDevice `cbor:"3,keyasint"`
	SignerDeviceID []byte                `cbor:"4,keyasint"`
}

func pairingRosterSigningBytes(roster PairingRoster) ([]byte, error) {
	if roster.Version == 0 || roster.Version > math.MaxInt64 || len(roster.AccountID) != 16 ||
		len(roster.SignerDeviceID) != 16 || len(roster.Devices) < 2 || len(roster.Devices) > 256 {
		return nil, errors.New("pairing roster metadata is invalid")
	}
	zeroID := make([]byte, 16)
	zeroKey := make([]byte, 32)
	if bytes.Equal(roster.AccountID, zeroID) || bytes.Equal(roster.SignerDeviceID, zeroID) {
		return nil, errors.New("pairing roster identifiers are invalid")
	}
	var previous []byte
	signerFound := false
	for _, device := range roster.Devices {
		if len(device.DeviceID) != 16 || len(device.Ed25519PublicKey) != ed25519.PublicKeySize ||
			len(device.X25519PublicKey) != 32 || bytes.Equal(device.DeviceID, zeroID) ||
			bytes.Equal(device.Ed25519PublicKey, zeroKey) || bytes.Equal(device.X25519PublicKey, zeroKey) {
			return nil, errors.New("pairing roster device is invalid")
		}
		if previous != nil && bytes.Compare(previous, device.DeviceID) >= 0 {
			return nil, errors.New("pairing roster devices are not uniquely sorted")
		}
		previous = device.DeviceID
		if bytes.Equal(device.DeviceID, roster.SignerDeviceID) {
			signerFound = true
		}
	}
	if !signerFound {
		return nil, errors.New("pairing roster signer is absent")
	}
	encoded, err := canonicalCBOR.Marshal(pairingRosterUnsigned{
		Version: roster.Version, AccountID: roster.AccountID, Devices: roster.Devices, SignerDeviceID: roster.SignerDeviceID,
	})
	if err != nil {
		return nil, err
	}
	return append([]byte("yunpin-pairing-roster-v1\x00"), encoded...), nil
}

func SignPairingRoster(accountID []byte, version uint64, devices []PairingRosterDevice, signerDeviceID []byte, private ed25519.PrivateKey) (PairingRoster, error) {
	if len(private) != ed25519.PrivateKeySize {
		return PairingRoster{}, errors.New("pairing roster signing key is invalid")
	}
	roster := PairingRoster{
		Version: version, AccountID: append([]byte(nil), accountID...),
		Devices: make([]PairingRosterDevice, len(devices)), SignerDeviceID: append([]byte(nil), signerDeviceID...),
	}
	for index, device := range devices {
		roster.Devices[index] = PairingRosterDevice{
			DeviceID: append([]byte(nil), device.DeviceID...), Ed25519PublicKey: append([]byte(nil), device.Ed25519PublicKey...),
			X25519PublicKey: append([]byte(nil), device.X25519PublicKey...),
		}
	}
	sort.Slice(roster.Devices, func(left, right int) bool {
		return bytes.Compare(roster.Devices[left].DeviceID, roster.Devices[right].DeviceID) < 0
	})
	signingBytes, err := pairingRosterSigningBytes(roster)
	if err != nil {
		return PairingRoster{}, err
	}
	public := private.Public().(ed25519.PublicKey)
	signerMatches := false
	for _, device := range roster.Devices {
		if bytes.Equal(device.DeviceID, roster.SignerDeviceID) {
			signerMatches = bytes.Equal(device.Ed25519PublicKey, public)
			break
		}
	}
	if !signerMatches {
		return PairingRoster{}, errors.New("pairing roster signer key does not match the roster")
	}
	roster.Signature = ed25519.Sign(private, signingBytes)
	return roster, nil
}

func VerifyPairingRoster(roster PairingRoster) error {
	if len(roster.Signature) != ed25519.SignatureSize {
		return errors.New("pairing roster signature is invalid")
	}
	signingBytes, err := pairingRosterSigningBytes(roster)
	if err != nil {
		return err
	}
	var signer ed25519.PublicKey
	for _, device := range roster.Devices {
		if bytes.Equal(device.DeviceID, roster.SignerDeviceID) {
			signer = device.Ed25519PublicKey
			break
		}
	}
	if !ed25519.Verify(signer, signingBytes, roster.Signature) {
		return errors.New("pairing roster signature verification failed")
	}
	return nil
}

func validatePairingPackage(payload PairingPackage, transcript PairingTranscript) error {
	if err := validatePairingTranscript(transcript); err != nil {
		return err
	}
	if len(payload.ObjectIDKey) != 32 || payload.CurrentEpoch == 0 || payload.CurrentEpoch > math.MaxInt64 ||
		len(payload.EpochKeys) < 1 || len(payload.EpochKeys) > 64 {
		return errors.New("pairing package key material is invalid")
	}
	currentFound := false
	var previous uint64
	for _, epoch := range payload.EpochKeys {
		if epoch.Epoch == 0 || epoch.Epoch > math.MaxInt64 || epoch.Epoch <= previous || len(epoch.Key) != 32 {
			return errors.New("pairing package epoch keys are invalid")
		}
		if epoch.Epoch == payload.CurrentEpoch {
			currentFound = true
		}
		previous = epoch.Epoch
	}
	if !currentFound {
		return errors.New("pairing package current epoch is absent")
	}
	if err := VerifyPairingRoster(payload.Roster); err != nil {
		return err
	}
	if !bytes.Equal(payload.Roster.AccountID, transcript.AccountID) {
		return errors.New("pairing roster account does not match the authenticated transcript")
	}
	if !bytes.Equal(payload.Roster.SignerDeviceID, transcript.CreatorDeviceID) {
		return errors.New("pairing roster was not signed by the invitation creator")
	}
	creatorFound, joiningFound := false, false
	for _, device := range payload.Roster.Devices {
		switch {
		case bytes.Equal(device.DeviceID, transcript.CreatorDeviceID):
			creatorFound = bytes.Equal(device.Ed25519PublicKey, transcript.CreatorEd25519PublicKey) &&
				bytes.Equal(device.X25519PublicKey, transcript.CreatorX25519PublicKey)
		case bytes.Equal(device.DeviceID, transcript.JoiningDeviceID):
			joiningFound = bytes.Equal(device.Ed25519PublicKey, transcript.JoiningEd25519PublicKey) &&
				bytes.Equal(device.X25519PublicKey, transcript.JoiningX25519PublicKey)
		}
	}
	if !creatorFound || !joiningFound {
		return errors.New("pairing roster does not match the authenticated transcript")
	}
	return nil
}

func sealBox(key, aad []byte, payload any, source io.Reader) (SealedBox, error) {
	if len(key) != chacha20poly1305.KeySize {
		return SealedBox{}, errors.New("sealed-box key must be 32 bytes")
	}
	plain, err := canonicalCBOR.Marshal(payload)
	if err != nil {
		return SealedBox{}, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return SealedBox{}, err
	}
	if source == nil {
		source = rand.Reader
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(source, nonce); err != nil {
		return SealedBox{}, err
	}
	return SealedBox{Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plain, aad)}, nil
}

func openBox(key, aad []byte, box SealedBox, destination any) error {
	if len(key) != chacha20poly1305.KeySize || len(box.Nonce) != chacha20poly1305.NonceSizeX {
		return errors.New("invalid sealed box")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	plain, err := aead.Open(nil, box.Nonce, box.Ciphertext, aad)
	if err != nil {
		return errors.New("sealed box authentication failed")
	}
	return cbor.Unmarshal(plain, destination)
}

func SealPairingPackage(private, peerPublic, pairingSecret []byte, transcript PairingTranscript, payload PairingPackage, source io.Reader) (SealedBox, error) {
	if err := validatePairingPackage(payload, transcript); err != nil {
		return SealedBox{}, err
	}
	key, err := DerivePairingKey(private, peerPublic, pairingSecret, transcript)
	if err != nil {
		return SealedBox{}, err
	}
	defer clear(key)
	encoded, err := canonicalPairingTranscript(transcript)
	if err != nil {
		return SealedBox{}, err
	}
	aad := append([]byte("yunpin-pairing-package-v2\x00"), encoded...)
	return sealBox(key, aad, payload, source)
}

func OpenPairingPackage(private, peerPublic, pairingSecret []byte, transcript PairingTranscript, box SealedBox) (PairingPackage, error) {
	key, err := DerivePairingKey(private, peerPublic, pairingSecret, transcript)
	if err != nil {
		return PairingPackage{}, err
	}
	defer clear(key)
	encoded, err := canonicalPairingTranscript(transcript)
	if err != nil {
		return PairingPackage{}, err
	}
	aad := append([]byte("yunpin-pairing-package-v2\x00"), encoded...)
	var payload PairingPackage
	if err := openBox(key, aad, box, &payload); err != nil {
		return PairingPackage{}, err
	}
	if err := validatePairingPackage(payload, transcript); err != nil {
		return PairingPackage{}, err
	}
	return payload, nil
}

func SealRecoveryPackage(recoveryKey []byte, payload RecoveryPackage, source io.Reader) (SealedBox, error) {
	if len(payload.AccountID) != 16 || len(payload.EpochKey) != 32 || len(payload.ObjectIDKey) != 32 || payload.CurrentEpoch == 0 {
		return SealedBox{}, errors.New("invalid recovery package")
	}
	encryption, _, err := DeriveRecoveryKeys(recoveryKey)
	if err != nil {
		return SealedBox{}, err
	}
	return sealBox(encryption, []byte("yunpin-recovery-package-v1"), payload, source)
}

func OpenRecoveryPackage(recoveryKey []byte, box SealedBox) (RecoveryPackage, error) {
	encryption, _, err := DeriveRecoveryKeys(recoveryKey)
	if err != nil {
		return RecoveryPackage{}, err
	}
	var payload RecoveryPackage
	if err := openBox(encryption, []byte("yunpin-recovery-package-v1"), box, &payload); err != nil {
		return RecoveryPackage{}, err
	}
	if len(payload.AccountID) != 16 || len(payload.EpochKey) != 32 || len(payload.ObjectIDKey) != 32 || payload.CurrentEpoch == 0 {
		return RecoveryPackage{}, errors.New("invalid recovery package")
	}
	return payload, nil
}

// RecoveryAuthentication is the high-entropy value sent over TLS during
// account creation/recovery. It is domain-separated from recovery-package
// encryption; the server stores only SHA-256(authentication).
func RecoveryAuthentication(recoveryKey []byte) (string, error) {
	_, authentication, err := DeriveRecoveryKeys(recoveryKey)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(authentication), nil
}
