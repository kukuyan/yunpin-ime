// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

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

func SealPairingPayload(private, peerPublic, sessionNonce []byte, payload any, source io.Reader) (SealedBox, error) {
	key, err := DerivePairingKey(private, peerPublic, sessionNonce)
	if err != nil {
		return SealedBox{}, err
	}
	aad := append([]byte("yunpin-pairing-package-v1\x00"), sessionNonce...)
	return sealBox(key, aad, payload, source)
}

func OpenPairingPayload(private, peerPublic, sessionNonce []byte, box SealedBox, destination any) error {
	key, err := DerivePairingKey(private, peerPublic, sessionNonce)
	if err != nil {
		return err
	}
	aad := append([]byte("yunpin-pairing-package-v1\x00"), sessionNonce...)
	return openBox(key, aad, box, destination)
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
