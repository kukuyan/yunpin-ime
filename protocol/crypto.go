// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode"

	"github.com/fxamacker/cbor/v2"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/text/unicode/norm"
)

const (
	ProtocolVersion       = 1
	PaddingBucket         = 512
	MaxEnvelopePayload    = 512 * 1024
	MaxEnvelopeCiphertext = ((MaxEnvelopePayload + 4 + PaddingBucket - 1) / PaddingBucket * PaddingBucket) + chacha20poly1305.Overhead
)

var canonicalCBOR cbor.EncMode

func init() {
	mode, err := cbor.CanonicalEncOptions().EncMode()
	if err != nil {
		panic(err)
	}
	canonicalCBOR = mode
}

// Header is authenticated but deliberately visible to the sync service.
// Byte identifiers are fixed-width random values and disclose no phrase text.
type Header struct {
	Version      uint64 `cbor:"1,keyasint" json:"version"`
	AccountID    []byte `cbor:"2,keyasint" json:"account_id"`
	ObjectID     []byte `cbor:"3,keyasint" json:"object_id"`
	KeyEpoch     uint64 `cbor:"4,keyasint" json:"key_epoch"`
	DeviceID     []byte `cbor:"5,keyasint" json:"device_id"`
	DeviceSeq    uint64 `cbor:"6,keyasint" json:"device_seq"`
	PreviousHash []byte `cbor:"7,keyasint,omitempty" json:"previous_hash,omitempty"`
	Nonce        []byte `cbor:"8,keyasint" json:"nonce"`
}

type Envelope struct {
	Header     Header `json:"header"`
	Ciphertext []byte `json:"ciphertext"`
	Signature  []byte `json:"signature"`
}

type DeviceKeys struct {
	X25519Private  []byte
	X25519Public   []byte
	Ed25519Public  ed25519.PublicKey
	Ed25519Private ed25519.PrivateKey
}

func NewDeviceKeys(source io.Reader) (DeviceKeys, error) {
	if source == nil {
		source = rand.Reader
	}
	private := make([]byte, curve25519.ScalarSize)
	if _, err := io.ReadFull(source, private); err != nil {
		return DeviceKeys{}, err
	}
	public, err := curve25519.X25519(private, curve25519.Basepoint)
	if err != nil {
		return DeviceKeys{}, err
	}
	edPublic, edPrivate, err := ed25519.GenerateKey(source)
	if err != nil {
		return DeviceKeys{}, err
	}
	return DeviceKeys{
		X25519Private:  private,
		X25519Public:   public,
		Ed25519Public:  edPublic,
		Ed25519Private: edPrivate,
	}, nil
}

func validateHeader(header Header) error {
	if header.Version != ProtocolVersion {
		return fmt.Errorf("unsupported protocol version %d", header.Version)
	}
	if len(header.AccountID) != 16 || len(header.ObjectID) != 16 || len(header.DeviceID) != 16 {
		return errors.New("account, object, and device identifiers must be 16 bytes")
	}
	if header.KeyEpoch < 1 || header.KeyEpoch > math.MaxInt64 {
		return errors.New("key epoch must be between 1 and MaxInt64")
	}
	if header.DeviceSeq < 1 || header.DeviceSeq > math.MaxInt64 {
		return errors.New("device sequence must be between 1 and MaxInt64")
	}
	if len(header.PreviousHash) != 0 && len(header.PreviousHash) != sha256.Size {
		return errors.New("previous hash must be empty or 32 bytes")
	}
	if len(header.Nonce) != chacha20poly1305.NonceSizeX {
		return errors.New("XChaCha20 nonce must be 24 bytes")
	}
	return nil
}

func deriveObjectKey(epochKey, objectID []byte) ([]byte, error) {
	if len(epochKey) != chacha20poly1305.KeySize {
		return nil, errors.New("epoch key must be 32 bytes")
	}
	key := make([]byte, chacha20poly1305.KeySize)
	reader := hkdf.New(sha256.New, epochKey, objectID, []byte("yunpin-envelope-v1"))
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

func canonicalHeader(header Header) ([]byte, error) {
	return canonicalCBOR.Marshal(header)
}

func paddedPayload(payload any, source io.Reader) ([]byte, error) {
	encoded, err := canonicalCBOR.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxEnvelopePayload {
		return nil, errors.New("payload too large")
	}
	needed := 4 + len(encoded)
	target := ((needed + PaddingBucket - 1) / PaddingBucket) * PaddingBucket
	if target < PaddingBucket {
		target = PaddingBucket
	}
	plain := make([]byte, target)
	binary.BigEndian.PutUint32(plain[:4], uint32(len(encoded)))
	copy(plain[4:], encoded)
	if source == nil {
		source = rand.Reader
	}
	if _, err := io.ReadFull(source, plain[4+len(encoded):]); err != nil {
		return nil, err
	}
	return plain, nil
}

// Seal encrypts and signs one client-side object. The sync server never needs
// epochKey or signingPrivate.
func Seal(epochKey []byte, header Header, payload any, signingPrivate ed25519.PrivateKey, source io.Reader) (Envelope, error) {
	if source == nil {
		source = rand.Reader
	}
	header.Version = ProtocolVersion
	header.Nonce = make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(source, header.Nonce); err != nil {
		return Envelope{}, err
	}
	if err := validateHeader(header); err != nil {
		return Envelope{}, err
	}
	if len(signingPrivate) != ed25519.PrivateKeySize {
		return Envelope{}, errors.New("invalid Ed25519 private key")
	}
	aad, err := canonicalHeader(header)
	if err != nil {
		return Envelope{}, err
	}
	plain, err := paddedPayload(payload, source)
	if err != nil {
		return Envelope{}, err
	}
	key, err := deriveObjectKey(epochKey, header.ObjectID)
	if err != nil {
		return Envelope{}, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return Envelope{}, err
	}
	ciphertext := aead.Seal(nil, header.Nonce, plain, aad)
	signed := make([]byte, 0, len(aad)+len(ciphertext))
	signed = append(signed, aad...)
	signed = append(signed, ciphertext...)
	return Envelope{
		Header:     header,
		Ciphertext: ciphertext,
		Signature:  ed25519.Sign(signingPrivate, signed),
	}, nil
}

func Open(epochKey []byte, envelope Envelope, signingPublic ed25519.PublicKey, destination any) error {
	if err := validateHeader(envelope.Header); err != nil {
		return err
	}
	if len(signingPublic) != ed25519.PublicKeySize {
		return errors.New("invalid Ed25519 public key")
	}
	if err := validateEnvelopeBody(envelope.Ciphertext, envelope.Signature); err != nil {
		return err
	}
	aad, err := canonicalHeader(envelope.Header)
	if err != nil {
		return err
	}
	signed := make([]byte, 0, len(aad)+len(envelope.Ciphertext))
	signed = append(signed, aad...)
	signed = append(signed, envelope.Ciphertext...)
	if !ed25519.Verify(signingPublic, signed, envelope.Signature) {
		return errors.New("invalid envelope signature")
	}
	key, err := deriveObjectKey(epochKey, envelope.Header.ObjectID)
	if err != nil {
		return err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	plain, err := aead.Open(nil, envelope.Header.Nonce, envelope.Ciphertext, aad)
	if err != nil {
		return errors.New("unable to authenticate envelope")
	}
	if len(plain) < 4 {
		return errors.New("invalid padded payload")
	}
	length := int(binary.BigEndian.Uint32(plain[:4]))
	if length < 0 || length > len(plain)-4 {
		return errors.New("invalid padded payload length")
	}
	return cbor.Unmarshal(plain[4:4+length], destination)
}

func OpaqueObjectID(idKey []byte, phrase, pinyin string) ([16]byte, error) {
	var result [16]byte
	if len(idKey) != 32 {
		return result, errors.New("object ID key must be 32 bytes")
	}
	phrase = CanonicalPhrase(phrase)
	pinyin = CanonicalPinyin(pinyin)
	if phrase == "" || pinyin == "" {
		return result, errors.New("phrase and canonical Pinyin are required")
	}
	mac := hmac.New(sha256.New, idKey)
	_, _ = mac.Write([]byte(phrase))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(pinyin))
	copy(result[:], mac.Sum(nil)[:16])
	return result, nil
}

// CanonicalPhrase matches the importer and public-pack identity rules: NFKC
// followed by removal of Unicode whitespace. It is deliberately exported so
// every client computes the same opaque object identifier.
func CanonicalPhrase(value string) string {
	value = norm.NFKC.String(strings.TrimSpace(value))
	return strings.Join(strings.Fields(value), "")
}

// CanonicalPinyin is the protocol-wide identity representation. It accepts
// numbered tones, tone marks, u:/ü/v spellings, apostrophe-separated syllables
// and arbitrary punctuation, then returns lower-case ASCII syllables separated
// by one space. Keep this byte-for-byte aligned with both offline importers.
func CanonicalPinyin(value string) string {
	value = strings.ToLower(norm.NFKC.String(strings.TrimSpace(value)))
	value = strings.ReplaceAll(value, "u:", "v")
	value = strings.NewReplacer(
		"ü", "v", "ǖ", "v", "ǘ", "v", "ǚ", "v", "ǜ", "v",
	).Replace(value)
	value = norm.NFD.String(value)

	var canonical strings.Builder
	separatorPending := false
	for _, character := range value {
		switch {
		case unicode.Is(unicode.Mn, character):
			continue
		case character >= '1' && character <= '5':
			continue
		case character >= 'a' && character <= 'z':
			if separatorPending && canonical.Len() > 0 {
				canonical.WriteByte(' ')
			}
			canonical.WriteRune(character)
			separatorPending = false
		case unicode.IsSpace(character), character == '\'', character == '’':
			separatorPending = canonical.Len() > 0
		default:
			separatorPending = canonical.Len() > 0
		}
	}
	return canonical.String()
}

func DerivePairingKey(private, peerPublic, sessionNonce []byte) ([]byte, error) {
	if len(private) != curve25519.ScalarSize || len(peerPublic) != curve25519.PointSize {
		return nil, errors.New("X25519 keys must be 32 bytes")
	}
	if len(sessionNonce) < 16 {
		return nil, errors.New("pairing nonce must be at least 16 bytes")
	}
	shared, err := curve25519.X25519(private, peerPublic)
	if err != nil {
		return nil, err
	}
	key := make([]byte, chacha20poly1305.KeySize)
	reader := hkdf.New(sha256.New, shared, sessionNonce, []byte("yunpin-pairing-v1"))
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

func DeriveRecoveryKeys(recoveryKey []byte) (encryption, authentication []byte, err error) {
	if len(recoveryKey) != 32 {
		return nil, nil, errors.New("recovery key must be 32 bytes")
	}
	expand := func(info string) ([]byte, error) {
		out := make([]byte, 32)
		reader := hkdf.New(sha256.New, recoveryKey, nil, []byte(info))
		_, readErr := io.ReadFull(reader, out)
		return out, readErr
	}
	encryption, err = expand("yunpin-recovery-encryption-v1")
	if err != nil {
		return nil, nil, err
	}
	authentication, err = expand("yunpin-recovery-authentication-v1")
	if err != nil {
		return nil, nil, err
	}
	return encryption, authentication, nil
}

func EnvelopeHash(envelope Envelope) ([]byte, error) {
	if err := validateHeader(envelope.Header); err != nil {
		return nil, err
	}
	if err := validateEnvelopeBody(envelope.Ciphertext, envelope.Signature); err != nil {
		return nil, err
	}
	aad, err := canonicalHeader(envelope.Header)
	if err != nil {
		return nil, err
	}
	digest := sha256.New()
	_, _ = digest.Write(aad)
	_, _ = digest.Write(envelope.Ciphertext)
	_, _ = digest.Write(envelope.Signature)
	return digest.Sum(nil), nil
}
