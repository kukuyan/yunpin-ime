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
)

const (
	CredentialBundleVersion = 1
	maxCredentialBlobBytes  = 64 * 1024
	maxDeviceTokenBytes     = 512
	maxEpochKeys            = 64
	maxVerificationKeys     = 256
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

func (bundle CredentialBundleV1) Validate() error {
	if bundle.Version != CredentialBundleVersion {
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
	return nil
}

// EncodeCredentialBundle emits one canonical, fixed-width binary record:
// magic, version, IDs, token, device secrets, ordered epochs, then ordered
// verification keys. Maps are sorted so the same credential has one encoding.
func EncodeCredentialBundle(bundle CredentialBundleV1) ([]byte, error) {
	if err := bundle.Validate(); err != nil {
		return nil, err
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

	devices := make([][16]byte, 0, len(bundle.VerificationKeys))
	for device := range bundle.VerificationKeys {
		devices = append(devices, device)
	}
	sort.Slice(devices, func(left, right int) bool {
		return bytes.Compare(devices[left][:], devices[right][:]) < 0
	})
	_ = binary.Write(&encoded, binary.BigEndian, uint16(len(devices)))
	for _, device := range devices {
		encoded.Write(device[:])
		key := bundle.VerificationKeys[device]
		encoded.Write(key[:])
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
	if err != nil || version[0] != CredentialBundleVersion {
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
	bundle.CurrentEpoch = 0
}
