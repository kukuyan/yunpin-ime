// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/kukuyan/yunpin-ime/protocol"
	"golang.org/x/crypto/curve25519"
)

const (
	legacyCredentialBundleVersion = 1
	CredentialBundleVersion       = 2
	maxCredentialBlobBytes        = 64 * 1024
	maxDeviceTokenBytes           = 512
	maxEpochKeys                  = 64
	maxVerificationKeys           = 256
)

var credentialMagic = [4]byte{'Y', 'P', 'C', 'B'}

// CredentialBundleV1 is the complete device-local secret required by the
// headless sync worker. It is serialized only inside an OS-protected secret
// store. Recovery roots and recovery-authentication material are deliberately
// absent.
type CredentialBundleV1 struct {
	Version          uint8
	AccountID        [16]byte
	DeviceID         [16]byte
	DeviceToken      []byte
	SigningSeed      [ed25519.SeedSize]byte
	X25519Private    [32]byte
	LocalDataKey     [32]byte
	ObjectIDKey      [32]byte
	CurrentEpoch     uint64
	EpochKeys        map[uint64][32]byte
	VerificationKeys map[[16]byte][ed25519.PublicKeySize]byte
	X25519PublicKeys map[[16]byte][32]byte
	// TrustedRoster is the complete creator-signed trust statement received
	// through pairing. A freshly created first device has a bootstrap self-only
	// trust set and an empty roster until it explicitly approves device two.
	TrustedRoster protocol.PairingRoster
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

func validDeviceToken(value []byte) bool {
	if len(value) < 16 || len(value) > maxDeviceTokenBytes {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func rosterIsEmpty(roster protocol.PairingRoster) bool {
	return roster.Version == 0 && len(roster.AccountID) == 0 && len(roster.Devices) == 0 &&
		len(roster.SignerDeviceID) == 0 && len(roster.Signature) == 0
}

func trustFromRoster(roster protocol.PairingRoster) (map[[16]byte][ed25519.PublicKeySize]byte, map[[16]byte][32]byte, error) {
	if err := protocol.VerifyPairingRoster(roster); err != nil {
		return nil, nil, err
	}
	verification := make(map[[16]byte][ed25519.PublicKeySize]byte, len(roster.Devices))
	x25519 := make(map[[16]byte][32]byte, len(roster.Devices))
	for _, device := range roster.Devices {
		var id [16]byte
		var ed [ed25519.PublicKeySize]byte
		var x [32]byte
		copy(id[:], device.DeviceID)
		copy(ed[:], device.Ed25519PublicKey)
		copy(x[:], device.X25519PublicKey)
		verification[id] = ed
		x25519[id] = x
	}
	return verification, x25519, nil
}

func equalVerificationTrust(left, right map[[16]byte][ed25519.PublicKeySize]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for id, key := range left {
		if other, ok := right[id]; !ok || other != key {
			return false
		}
	}
	return true
}

func equalX25519Trust(left, right map[[16]byte][32]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for id, key := range left {
		if other, ok := right[id]; !ok || other != key {
			return false
		}
	}
	return true
}

func clonePairingRoster(roster protocol.PairingRoster) protocol.PairingRoster {
	cloned := protocol.PairingRoster{
		Version: roster.Version, AccountID: append([]byte(nil), roster.AccountID...),
		SignerDeviceID: append([]byte(nil), roster.SignerDeviceID...), Signature: append([]byte(nil), roster.Signature...),
		Devices: make([]protocol.PairingRosterDevice, len(roster.Devices)),
	}
	for index, device := range roster.Devices {
		cloned.Devices[index] = protocol.PairingRosterDevice{
			DeviceID:         append([]byte(nil), device.DeviceID...),
			Ed25519PublicKey: append([]byte(nil), device.Ed25519PublicKey...),
			X25519PublicKey:  append([]byte(nil), device.X25519PublicKey...),
		}
	}
	return cloned
}

func applyTrustedRoster(bundle *CredentialBundleV1, roster protocol.PairingRoster) error {
	if bundle == nil || !bytes.Equal(roster.AccountID, bundle.AccountID[:]) {
		return errors.New("trusted roster account does not match credential")
	}
	verification, x25519, err := trustFromRoster(roster)
	if err != nil {
		return err
	}
	if len(roster.Devices) != 2 {
		return errors.New("this preview supports exactly two devices in its signed trust roster")
	}
	bundle.Version = CredentialBundleVersion
	bundle.TrustedRoster = clonePairingRoster(roster)
	bundle.VerificationKeys = verification
	bundle.X25519PublicKeys = x25519
	return nil
}

func populateBootstrapTrust(bundle *CredentialBundleV1) error {
	if bundle == nil {
		return errors.New("credential bundle is required")
	}
	edPublic := ed25519.NewKeyFromSeed(bundle.SigningSeed[:]).Public().(ed25519.PublicKey)
	xPublic, err := curve25519.X25519(bundle.X25519Private[:], curve25519.Basepoint)
	if err != nil {
		return err
	}
	var ed [ed25519.PublicKeySize]byte
	var x [32]byte
	copy(ed[:], edPublic)
	copy(x[:], xPublic)
	bundle.VerificationKeys = map[[16]byte][ed25519.PublicKeySize]byte{bundle.DeviceID: ed}
	bundle.X25519PublicKeys = map[[16]byte][32]byte{bundle.DeviceID: x}
	return nil
}

func (bundle CredentialBundleV1) Validate() error {
	if bundle.Version != legacyCredentialBundleVersion && bundle.Version != CredentialBundleVersion {
		return fmt.Errorf("unsupported credential bundle version %d", bundle.Version)
	}
	if allZero(bundle.AccountID[:]) || allZero(bundle.DeviceID[:]) {
		return errors.New("account and device IDs must be nonzero random identifiers")
	}
	if !validDeviceToken(bundle.DeviceToken) {
		return errors.New("device token must be bounded unpadded base64url")
	}
	if allZero(bundle.SigningSeed[:]) || allZero(bundle.X25519Private[:]) ||
		allZero(bundle.LocalDataKey[:]) || allZero(bundle.ObjectIDKey[:]) {
		return errors.New("credential bundle contains an empty private key")
	}
	if bundle.CurrentEpoch < 1 || bundle.CurrentEpoch > math.MaxInt64 ||
		len(bundle.EpochKeys) < 1 || len(bundle.EpochKeys) > maxEpochKeys {
		return errors.New("credential bundle has an invalid epoch set")
	}
	for epoch, key := range bundle.EpochKeys {
		if epoch < 1 || epoch > math.MaxInt64 || allZero(key[:]) {
			return errors.New("credential bundle has an invalid epoch key")
		}
	}
	if _, exists := bundle.EpochKeys[bundle.CurrentEpoch]; !exists {
		return errors.New("current epoch key is missing")
	}
	if len(bundle.VerificationKeys) < 1 || len(bundle.VerificationKeys) > maxVerificationKeys {
		return errors.New("credential bundle has an invalid verification-key set")
	}
	for deviceID, key := range bundle.VerificationKeys {
		if allZero(deviceID[:]) || allZero(key[:]) {
			return errors.New("credential bundle has an invalid verification key")
		}
	}
	privateCopy := ed25519.NewKeyFromSeed(bundle.SigningSeed[:])
	wantSelf := privateCopy.Public().(ed25519.PublicKey)
	self, exists := bundle.VerificationKeys[bundle.DeviceID]
	validSelf := exists && bytes.Equal(self[:], wantSelf)
	zeroBytes(privateCopy)
	zeroBytes(wantSelf)
	if !validSelf {
		return errors.New("current device verification key does not match its signing seed")
	}
	if bundle.Version == legacyCredentialBundleVersion {
		if !rosterIsEmpty(bundle.TrustedRoster) {
			return errors.New("legacy credential unexpectedly contains a trusted roster")
		}
		return nil
	}
	wantX, err := curve25519.X25519(bundle.X25519Private[:], curve25519.Basepoint)
	if err != nil {
		return errors.New("credential X25519 private key is invalid")
	}
	selfX, selfXFound := bundle.X25519PublicKeys[bundle.DeviceID]
	if !selfXFound || !bytes.Equal(selfX[:], wantX) {
		return errors.New("current device X25519 public key does not match its private key")
	}
	if rosterIsEmpty(bundle.TrustedRoster) {
		if len(bundle.VerificationKeys) != 1 || len(bundle.X25519PublicKeys) != 1 {
			return errors.New("bootstrap credential may trust only itself before its first signed roster")
		}
		return nil
	}
	if len(bundle.TrustedRoster.Devices) != 2 {
		return errors.New("this preview supports exactly two trusted devices")
	}
	if !bytes.Equal(bundle.TrustedRoster.AccountID, bundle.AccountID[:]) {
		return errors.New("trusted roster belongs to another account")
	}
	rosterVerification, rosterX25519, err := trustFromRoster(bundle.TrustedRoster)
	if err != nil {
		return fmt.Errorf("validate trusted roster: %w", err)
	}
	if !equalVerificationTrust(bundle.VerificationKeys, rosterVerification) ||
		!equalX25519Trust(bundle.X25519PublicKeys, rosterX25519) {
		return errors.New("credential trust maps are not derived exactly from the signed roster")
	}
	return nil
}

// EncodeCredentialBundle emits the v2 canonical binary record. Verification
// and X25519 trust maps are never serialized independently: after bootstrap
// they are reconstructed only from the complete signed roster.
func EncodeCredentialBundle(bundle CredentialBundleV1) ([]byte, error) {
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	if bundle.Version != CredentialBundleVersion {
		return nil, errors.New("legacy credential must be upgraded before it is encoded")
	}
	var encoded bytes.Buffer
	encoded.Grow(256 + len(bundle.DeviceToken) + len(bundle.EpochKeys)*40 + len(bundle.VerificationKeys)*48)
	encoded.Write(credentialMagic[:])
	encoded.WriteByte(bundle.Version)
	encoded.Write(bundle.AccountID[:])
	encoded.Write(bundle.DeviceID[:])
	_ = binary.Write(&encoded, binary.BigEndian, uint16(len(bundle.DeviceToken)))
	encoded.Write(bundle.DeviceToken)
	encoded.Write(bundle.SigningSeed[:])
	encoded.Write(bundle.X25519Private[:])
	encoded.Write(bundle.LocalDataKey[:])
	encoded.Write(bundle.ObjectIDKey[:])
	_ = binary.Write(&encoded, binary.BigEndian, bundle.CurrentEpoch)

	epochs := make([]uint64, 0, len(bundle.EpochKeys))
	for epoch := range bundle.EpochKeys {
		epochs = append(epochs, epoch)
	}
	sort.Slice(epochs, func(left, right int) bool { return epochs[left] < epochs[right] })
	_ = binary.Write(&encoded, binary.BigEndian, uint16(len(epochs)))
	for _, epoch := range epochs {
		_ = binary.Write(&encoded, binary.BigEndian, epoch)
		key := bundle.EpochKeys[epoch]
		encoded.Write(key[:])
	}

	if rosterIsEmpty(bundle.TrustedRoster) {
		encoded.WriteByte(0)
	} else {
		encoded.WriteByte(1)
		_ = binary.Write(&encoded, binary.BigEndian, bundle.TrustedRoster.Version)
		encoded.Write(bundle.TrustedRoster.AccountID)
		encoded.Write(bundle.TrustedRoster.SignerDeviceID)
		_ = binary.Write(&encoded, binary.BigEndian, uint16(len(bundle.TrustedRoster.Devices)))
		for _, device := range bundle.TrustedRoster.Devices {
			encoded.Write(device.DeviceID)
			encoded.Write(device.Ed25519PublicKey)
			encoded.Write(device.X25519PublicKey)
		}
		encoded.Write(bundle.TrustedRoster.Signature)
	}
	if encoded.Len() > maxCredentialBlobBytes {
		return nil, errors.New("credential bundle exceeds size limit")
	}
	return encoded.Bytes(), nil
}

type credentialReader struct {
	value  []byte
	offset int
}

func (reader *credentialReader) take(length int) ([]byte, error) {
	if length < 0 || reader.offset > len(reader.value)-length {
		return nil, errors.New("credential bundle is truncated")
	}
	value := reader.value[reader.offset : reader.offset+length]
	reader.offset += length
	return value, nil
}

func (reader *credentialReader) uint16() (uint16, error) {
	value, err := reader.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(value), nil
}

func (reader *credentialReader) uint64() (uint64, error) {
	value, err := reader.take(8)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(value), nil
}

func copyFixed(destination []byte, reader *credentialReader) error {
	value, err := reader.take(len(destination))
	if err != nil {
		return err
	}
	copy(destination, value)
	return nil
}

func DecodeCredentialBundle(encoded []byte) (CredentialBundleV1, error) {
	if len(encoded) < 5 || len(encoded) > maxCredentialBlobBytes {
		return CredentialBundleV1{}, errors.New("credential bundle length is invalid")
	}
	reader := credentialReader{value: encoded}
	magic, _ := reader.take(len(credentialMagic))
	if !bytes.Equal(magic, credentialMagic[:]) {
		return CredentialBundleV1{}, errors.New("credential bundle magic is invalid")
	}
	version, err := reader.take(1)
	if err != nil || (version[0] != legacyCredentialBundleVersion && version[0] != CredentialBundleVersion) {
		return CredentialBundleV1{}, errors.New("credential bundle version is unsupported")
	}
	bundle := CredentialBundleV1{Version: version[0]}
	decoded := false
	defer func() {
		if !decoded {
			bundle.Zero()
		}
	}()
	if err := copyFixed(bundle.AccountID[:], &reader); err != nil {
		return CredentialBundleV1{}, err
	}
	if err := copyFixed(bundle.DeviceID[:], &reader); err != nil {
		return CredentialBundleV1{}, err
	}
	tokenLength, err := reader.uint16()
	if err != nil || tokenLength < 16 || tokenLength > maxDeviceTokenBytes {
		return CredentialBundleV1{}, errors.New("credential bundle token length is invalid")
	}
	token, err := reader.take(int(tokenLength))
	if err != nil {
		return CredentialBundleV1{}, err
	}
	bundle.DeviceToken = append([]byte(nil), token...)
	if err := copyFixed(bundle.SigningSeed[:], &reader); err != nil {
		return CredentialBundleV1{}, err
	}
	if err := copyFixed(bundle.X25519Private[:], &reader); err != nil {
		return CredentialBundleV1{}, err
	}
	if err := copyFixed(bundle.LocalDataKey[:], &reader); err != nil {
		return CredentialBundleV1{}, err
	}
	if err := copyFixed(bundle.ObjectIDKey[:], &reader); err != nil {
		return CredentialBundleV1{}, err
	}
	bundle.CurrentEpoch, err = reader.uint64()
	if err != nil {
		return CredentialBundleV1{}, err
	}
	epochCount, err := reader.uint16()
	if err != nil || epochCount < 1 || epochCount > maxEpochKeys {
		return CredentialBundleV1{}, errors.New("credential bundle epoch count is invalid")
	}
	bundle.EpochKeys = make(map[uint64][32]byte, epochCount)
	var previousEpoch uint64
	for index := 0; index < int(epochCount); index++ {
		epoch, readErr := reader.uint64()
		if readErr != nil {
			return CredentialBundleV1{}, readErr
		}
		if epoch <= previousEpoch {
			return CredentialBundleV1{}, errors.New("credential bundle epochs are duplicate or out of order")
		}
		previousEpoch = epoch
		var key [32]byte
		if err := copyFixed(key[:], &reader); err != nil {
			return CredentialBundleV1{}, err
		}
		bundle.EpochKeys[epoch] = key
	}
	if bundle.Version == legacyCredentialBundleVersion {
		verificationCount, err := reader.uint16()
		if err != nil || verificationCount < 1 || verificationCount > maxVerificationKeys {
			return CredentialBundleV1{}, errors.New("credential bundle verification-key count is invalid")
		}
		bundle.VerificationKeys = make(map[[16]byte][ed25519.PublicKeySize]byte, verificationCount)
		var previousDevice [16]byte
		for index := 0; index < int(verificationCount); index++ {
			var device [16]byte
			if err := copyFixed(device[:], &reader); err != nil {
				return CredentialBundleV1{}, err
			}
			if index > 0 && bytes.Compare(previousDevice[:], device[:]) >= 0 {
				return CredentialBundleV1{}, errors.New("credential bundle device keys are duplicate or out of order")
			}
			previousDevice = device
			var key [ed25519.PublicKeySize]byte
			if err := copyFixed(key[:], &reader); err != nil {
				return CredentialBundleV1{}, err
			}
			bundle.VerificationKeys[device] = key
		}
		if xPublic, xErr := curve25519.X25519(bundle.X25519Private[:], curve25519.Basepoint); xErr == nil {
			var x [32]byte
			copy(x[:], xPublic)
			bundle.X25519PublicKeys = map[[16]byte][32]byte{bundle.DeviceID: x}
		}
	} else {
		present, err := reader.take(1)
		if err != nil || (present[0] != 0 && present[0] != 1) {
			return CredentialBundleV1{}, errors.New("credential bundle trusted-roster marker is invalid")
		}
		if present[0] == 0 {
			if err := populateBootstrapTrust(&bundle); err != nil {
				return CredentialBundleV1{}, err
			}
		} else {
			roster := protocol.PairingRoster{}
			roster.Version, err = reader.uint64()
			if err != nil {
				return CredentialBundleV1{}, err
			}
			roster.AccountID, err = reader.take(16)
			if err == nil {
				roster.SignerDeviceID, err = reader.take(16)
			}
			var count uint16
			if err == nil {
				count, err = reader.uint16()
			}
			if err != nil || count < 2 || count > maxVerificationKeys {
				return CredentialBundleV1{}, errors.New("credential bundle trusted-roster count is invalid")
			}
			roster.AccountID = append([]byte(nil), roster.AccountID...)
			roster.SignerDeviceID = append([]byte(nil), roster.SignerDeviceID...)
			roster.Devices = make([]protocol.PairingRosterDevice, count)
			for index := range roster.Devices {
				id, readErr := reader.take(16)
				if readErr == nil {
					roster.Devices[index].Ed25519PublicKey, readErr = reader.take(ed25519.PublicKeySize)
				}
				if readErr == nil {
					roster.Devices[index].X25519PublicKey, readErr = reader.take(32)
				}
				if readErr != nil {
					return CredentialBundleV1{}, readErr
				}
				roster.Devices[index].DeviceID = append([]byte(nil), id...)
				roster.Devices[index].Ed25519PublicKey = append([]byte(nil), roster.Devices[index].Ed25519PublicKey...)
				roster.Devices[index].X25519PublicKey = append([]byte(nil), roster.Devices[index].X25519PublicKey...)
			}
			roster.Signature, err = reader.take(ed25519.SignatureSize)
			if err != nil {
				return CredentialBundleV1{}, err
			}
			roster.Signature = append([]byte(nil), roster.Signature...)
			if err := applyTrustedRoster(&bundle, roster); err != nil {
				return CredentialBundleV1{}, err
			}
		}
	}
	if reader.offset != len(encoded) {
		return CredentialBundleV1{}, errors.New("credential bundle has trailing bytes")
	}
	if err := bundle.Validate(); err != nil {
		return CredentialBundleV1{}, err
	}
	decoded = true
	return bundle, nil
}

func (bundle CredentialBundleV1) DeviceIDHex() string {
	return hex.EncodeToString(bundle.DeviceID[:])
}

// Zero clears mutable secret buffers owned by this value. Copies previously
// made by Go or an OS API cannot be guaranteed to be overwritten.
func (bundle *CredentialBundleV1) Zero() {
	if bundle == nil {
		return
	}
	for index := range bundle.DeviceToken {
		bundle.DeviceToken[index] = 0
	}
	bundle.DeviceToken = nil
	clear(bundle.SigningSeed[:])
	clear(bundle.X25519Private[:])
	clear(bundle.LocalDataKey[:])
	clear(bundle.ObjectIDKey[:])
	for epoch := range bundle.EpochKeys {
		key := bundle.EpochKeys[epoch]
		clear(key[:])
		bundle.EpochKeys[epoch] = key
		delete(bundle.EpochKeys, epoch)
	}
	for device := range bundle.VerificationKeys {
		key := bundle.VerificationKeys[device]
		clear(key[:])
		bundle.VerificationKeys[device] = key
		delete(bundle.VerificationKeys, device)
	}
	for device := range bundle.X25519PublicKeys {
		key := bundle.X25519PublicKeys[device]
		clear(key[:])
		bundle.X25519PublicKeys[device] = key
		delete(bundle.X25519PublicKeys, device)
	}
	for index := range bundle.TrustedRoster.Devices {
		zeroBytes(bundle.TrustedRoster.Devices[index].DeviceID)
		zeroBytes(bundle.TrustedRoster.Devices[index].Ed25519PublicKey)
		zeroBytes(bundle.TrustedRoster.Devices[index].X25519PublicKey)
	}
	zeroBytes(bundle.TrustedRoster.AccountID)
	zeroBytes(bundle.TrustedRoster.SignerDeviceID)
	zeroBytes(bundle.TrustedRoster.Signature)
	bundle.TrustedRoster = protocol.PairingRoster{}
	bundle.CurrentEpoch = 0
}
