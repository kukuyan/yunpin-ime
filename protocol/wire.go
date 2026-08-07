// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// WireEnvelope is the JSON representation exchanged by POST /v1/sync. Uploads
// omit account/device IDs because the bearer token authenticates them. A
// download includes the source DeviceID so the client can select its Ed25519
// key and reconstruct the canonical header; account ID remains client-known.
type WireEnvelope struct {
	Version      uint64 `json:"version"`
	DeviceID     string `json:"device_id,omitempty"`
	DeviceSeq    uint64 `json:"device_seq"`
	ObjectID     string `json:"object_id"`
	KeyEpoch     uint64 `json:"key_epoch"`
	PreviousHash string `json:"previous_hash,omitempty"`
	Nonce        string `json:"nonce"`
	Ciphertext   string `json:"ciphertext"`
	Signature    string `json:"signature"`
	Cursor       uint64 `json:"cursor,omitempty"`
}

func validateEnvelopeBody(ciphertext, signature []byte) error {
	if len(signature) != 64 {
		return errors.New("invalid envelope signature length")
	}
	if len(ciphertext) < PaddingBucket+16 || len(ciphertext) > MaxEnvelopeCiphertext || (len(ciphertext)-16)%PaddingBucket != 0 {
		return errors.New("invalid envelope ciphertext length")
	}
	return nil
}

func (envelope Envelope) ToWire() (WireEnvelope, error) {
	if err := validateHeader(envelope.Header); err != nil {
		return WireEnvelope{}, err
	}
	if err := validateEnvelopeBody(envelope.Ciphertext, envelope.Signature); err != nil {
		return WireEnvelope{}, err
	}
	wire := WireEnvelope{
		Version:    envelope.Header.Version,
		DeviceSeq:  envelope.Header.DeviceSeq,
		ObjectID:   hex.EncodeToString(envelope.Header.ObjectID),
		KeyEpoch:   envelope.Header.KeyEpoch,
		Nonce:      base64.RawURLEncoding.EncodeToString(envelope.Header.Nonce),
		Ciphertext: base64.RawURLEncoding.EncodeToString(envelope.Ciphertext),
		Signature:  base64.RawURLEncoding.EncodeToString(envelope.Signature),
	}
	if len(envelope.Header.PreviousHash) != 0 {
		wire.PreviousHash = base64.RawURLEncoding.EncodeToString(envelope.Header.PreviousHash)
	}
	return wire, nil
}

func EnvelopeFromWire(accountID, deviceID []byte, wire WireEnvelope) (Envelope, error) {
	objectID, err := decodeHexWireID(wire.ObjectID)
	if err != nil {
		return Envelope{}, errors.New("invalid wire object ID")
	}
	if wire.DeviceID != "" {
		wireDeviceID, err := decodeHexWireID(wire.DeviceID)
		if err != nil || !bytes.Equal(wireDeviceID, deviceID) {
			return Envelope{}, errors.New("wire device ID does not match authenticated device")
		}
	}
	decode := func(value string) ([]byte, error) {
		return base64.RawURLEncoding.DecodeString(value)
	}
	nonce, err := decode(wire.Nonce)
	if err != nil {
		return Envelope{}, errors.New("invalid wire nonce")
	}
	ciphertext, err := decode(wire.Ciphertext)
	if err != nil {
		return Envelope{}, errors.New("invalid wire ciphertext")
	}
	signature, err := decode(wire.Signature)
	if err != nil {
		return Envelope{}, errors.New("invalid wire signature")
	}
	var previous []byte
	if wire.PreviousHash != "" {
		previous, err = decode(wire.PreviousHash)
		if err != nil {
			return Envelope{}, errors.New("invalid wire previous hash")
		}
	}
	envelope := Envelope{
		Header: Header{
			Version:      wire.Version,
			AccountID:    append([]byte(nil), accountID...),
			ObjectID:     objectID,
			KeyEpoch:     wire.KeyEpoch,
			DeviceID:     append([]byte(nil), deviceID...),
			DeviceSeq:    wire.DeviceSeq,
			PreviousHash: previous,
			Nonce:        nonce,
		},
		Ciphertext: ciphertext,
		Signature:  signature,
	}
	if err := validateHeader(envelope.Header); err != nil {
		return Envelope{}, err
	}
	if err := validateEnvelopeBody(ciphertext, signature); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

// EnvelopeFromDownload reconstructs a downloaded header using the source
// device ID carried by the relay. Callers use that ID to select the device's
// Ed25519 verification key before protocol.Open.
func EnvelopeFromDownload(accountID []byte, wire WireEnvelope) (Envelope, error) {
	if wire.DeviceID == "" {
		return Envelope{}, errors.New("downloaded envelope is missing a device ID")
	}
	deviceID, err := decodeHexWireID(wire.DeviceID)
	if err != nil {
		return Envelope{}, errors.New("invalid downloaded device ID")
	}
	return EnvelopeFromWire(accountID, deviceID, wire)
}

func decodeHexWireID(value string) ([]byte, error) {
	if len(value) != 32 || value != strings.ToLower(value) {
		return nil, errors.New("invalid hexadecimal ID")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		return nil, errors.New("invalid hexadecimal ID")
	}
	return decoded, nil
}
