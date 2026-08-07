// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"strings"

	"github.com/kukuyan/yunpin-ime/protocol"
)

// PhrasePayload is the client-encrypted value carried by a phrase envelope.
// Relay-visible headers contain only opaque identifiers and sequencing data.
type PhrasePayload struct {
	Text        string               `json:"text" cbor:"1,keyasint"`
	Pinyin      string               `json:"pinyin" cbor:"2,keyasint"`
	Source      string               `json:"source" cbor:"3,keyasint"`
	LastUsedDay int64                `json:"last_used_day" cbor:"4,keyasint"`
	State       protocol.PhraseState `json:"state" cbor:"5,keyasint"`
}

// EnvelopeOptions supplies the device-local sequence and cryptographic keys
// that are intentionally not stored in an outbox phrase record.
type EnvelopeOptions struct {
	AccountID      []byte
	DeviceID       []byte
	KeyEpoch       uint64
	DeviceSeq      uint64
	PreviousHash   []byte
	EpochKey       []byte
	SigningPrivate ed25519.PrivateKey
	Random         io.Reader
}

func validSyncDeviceID(deviceID string) bool {
	if len(deviceID) != 32 || deviceID != strings.ToLower(deviceID) {
		return false
	}
	decoded, err := hex.DecodeString(deviceID)
	return err == nil && len(decoded) == 16
}

func isZeroClock(clock protocol.HLC) bool {
	return clock.WallMillis == 0 && clock.Counter == 0 && clock.Node == ""
}

func clonePhraseState(state protocol.PhraseState) protocol.PhraseState {
	clone := state
	clone.Counts = make(map[string]uint64, len(state.Counts))
	for device, count := range state.Counts {
		clone.Counts[device] = count
	}
	return clone
}

func totalCounts(counts map[string]uint64) uint64 {
	var total uint64
	for _, count := range counts {
		if math.MaxUint64-total < count {
			return math.MaxUint64
		}
		total += count
	}
	return total
}

func materializePhrase(phrase *Phrase) {
	phrase.UseCount = totalCounts(phrase.CRDT.Counts)
	phrase.Pinned = phrase.CRDT.Pinned.Value
	phrase.Deleted = !phrase.CRDT.Presence.Present
}

func ensurePhraseState(phrase *Phrase, objectID [16]byte, deviceID string, eventClock protocol.HLC) error {
	opaqueID := hex.EncodeToString(objectID[:])
	if phrase.CRDT.ObjectID == "" {
		phrase.CRDT = protocol.PhraseState{
			ObjectID: opaqueID,
			Counts:   make(map[string]uint64),
			Pinned:   protocol.LWWBool{Value: phrase.Pinned, Clock: eventClock},
			Presence: protocol.Presence{Present: !phrase.Deleted, Clock: eventClock, Generation: 1},
		}
	} else if phrase.CRDT.ObjectID != opaqueID {
		return errors.New("phrase CRDT object ID does not match encrypted record")
	}
	if phrase.CRDT.Counts == nil {
		phrase.CRDT.Counts = make(map[string]uint64)
	}
	// Migrate an older encrypted Phrase record without losing its aggregate.
	if aggregate := totalCounts(phrase.CRDT.Counts); aggregate < phrase.UseCount {
		phrase.CRDT.Counts[deviceID] += phrase.UseCount - aggregate
	}
	if phrase.CRDT.Presence.Generation == 0 {
		phrase.CRDT.Presence.Generation = 1
		phrase.CRDT.Presence.Present = !phrase.Deleted
	}
	if isZeroClock(phrase.CRDT.Pinned.Clock) {
		phrase.CRDT.Pinned = protocol.LWWBool{Value: phrase.Pinned, Clock: eventClock}
	}
	if isZeroClock(phrase.CRDT.Presence.Clock) {
		phrase.CRDT.Presence.Clock = eventClock
	}
	materializePhrase(phrase)
	return nil
}

func (store *Store) nextHLC(ctx context.Context) (protocol.HLC, error) {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.HLC{}, err
	}
	defer transaction.Rollback()
	var wallMillis, counter int64
	if err := transaction.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'hlc_wall_ms'").Scan(&wallMillis); err != nil {
		return protocol.HLC{}, err
	}
	if err := transaction.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'hlc_counter'").Scan(&counter); err != nil {
		return protocol.HLC{}, err
	}
	physical := store.now().UnixMilli()
	if physical > wallMillis {
		wallMillis = physical
		counter = 0
	} else if counter >= math.MaxUint32 {
		wallMillis++
		counter = 0
	} else {
		counter++
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE metadata SET value = ? WHERE key = 'hlc_wall_ms'", wallMillis); err != nil {
		return protocol.HLC{}, err
	}
	if _, err := transaction.ExecContext(ctx, "UPDATE metadata SET value = ? WHERE key = 'hlc_counter'", counter); err != nil {
		return protocol.HLC{}, err
	}
	if err := transaction.Commit(); err != nil {
		return protocol.HLC{}, err
	}
	return protocol.HLC{WallMillis: wallMillis, Counter: uint32(counter), Node: store.deviceID}, nil
}

func (store *Store) observeHLC(ctx context.Context, observed protocol.HLC) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	var wallMillis, counter int64
	if err := transaction.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'hlc_wall_ms'").Scan(&wallMillis); err != nil {
		return err
	}
	if err := transaction.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'hlc_counter'").Scan(&counter); err != nil {
		return err
	}
	if observed.WallMillis > wallMillis || (observed.WallMillis == wallMillis && int64(observed.Counter) > counter) {
		wallMillis = observed.WallMillis
		counter = int64(observed.Counter)
		if _, err := transaction.ExecContext(ctx, "UPDATE metadata SET value = ? WHERE key = 'hlc_wall_ms'", wallMillis); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, "UPDATE metadata SET value = ? WHERE key = 'hlc_counter'", counter); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func validatePayloadObject(payload PhrasePayload, idKey []byte) ([16]byte, error) {
	if strings.TrimSpace(payload.Text) == "" || strings.TrimSpace(payload.Pinyin) == "" {
		return [16]byte{}, errors.New("phrase payload text and Pinyin are required")
	}
	objectID, err := protocol.OpaqueObjectID(idKey, payload.Text, payload.Pinyin)
	if err != nil {
		return [16]byte{}, err
	}
	if payload.State.ObjectID != hex.EncodeToString(objectID[:]) || payload.State.Presence.Generation == 0 {
		return [16]byte{}, errors.New("phrase payload object ID or generation is invalid")
	}
	if payload.State.Counts == nil {
		return [16]byte{}, errors.New("phrase payload counter is missing")
	}
	for device := range payload.State.Counts {
		if !validSyncDeviceID(device) {
			return [16]byte{}, errors.New("phrase payload contains an invalid counter component")
		}
	}
	if !validSyncDeviceID(payload.State.Pinned.Clock.Node) || !validSyncDeviceID(payload.State.Presence.Clock.Node) {
		return [16]byte{}, errors.New("phrase payload contains an invalid HLC node")
	}
	return objectID, nil
}

// ProtocolState converts one decrypted outbox row into a detached CRDT value.
func (event PendingEvent) ProtocolState() (protocol.PhraseState, error) {
	wantID := hex.EncodeToString(event.ObjectID[:])
	if event.Phrase.CRDT.ObjectID != wantID || event.Phrase.CRDT.Presence.Generation == 0 || event.Phrase.CRDT.Counts == nil {
		return protocol.PhraseState{}, errors.New("outbox phrase has invalid CRDT metadata")
	}
	return clonePhraseState(event.Phrase.CRDT), nil
}

// ProtocolPayload returns the content and state that are encrypted together.
func (event PendingEvent) ProtocolPayload() (PhrasePayload, error) {
	state, err := event.ProtocolState()
	if err != nil {
		return PhrasePayload{}, err
	}
	return PhrasePayload{
		Text: event.Phrase.Text, Pinyin: event.Phrase.Pinyin, Source: event.Phrase.Source,
		LastUsedDay: event.Phrase.LastUsedDay, State: state,
	}, nil
}

// SealEnvelope produces the protocol.Envelope uploaded by the background sync
// worker. The returned value can be converted with protocol.Envelope.ToWire.
func (event PendingEvent) SealEnvelope(options EnvelopeOptions) (protocol.Envelope, error) {
	if !event.syncable || !validSyncDeviceID(event.deviceID) {
		return protocol.Envelope{}, errors.New("store was not opened with OpenForDevice")
	}
	if len(options.DeviceID) != 16 || hex.EncodeToString(options.DeviceID) != event.deviceID {
		return protocol.Envelope{}, errors.New("envelope device ID does not match the local CRDT component")
	}
	payload, err := event.ProtocolPayload()
	if err != nil {
		return protocol.Envelope{}, err
	}
	if !bytes.Equal(event.ObjectID[:], mustDecodeObjectID(payload.State.ObjectID)) {
		return protocol.Envelope{}, errors.New("outbox object ID changed during adaptation")
	}
	header := protocol.Header{
		AccountID: append([]byte(nil), options.AccountID...), ObjectID: append([]byte(nil), event.ObjectID[:]...),
		KeyEpoch: options.KeyEpoch, DeviceID: append([]byte(nil), options.DeviceID...), DeviceSeq: options.DeviceSeq,
		PreviousHash: append([]byte(nil), options.PreviousHash...),
	}
	return protocol.Seal(options.EpochKey, header, payload, options.SigningPrivate, options.Random)
}

func mustDecodeObjectID(encoded string) []byte {
	decoded, _ := hex.DecodeString(encoded)
	return decoded
}

func chooseStableString(left, right string) string {
	if left == "" {
		return right
	}
	if right == "" || left <= right {
		return left
	}
	return right
}

// MergePhrasePayload deterministically combines content metadata and delegates
// all replicated state to protocol.MergePhrase.
func MergePhrasePayload(left, right PhrasePayload) (PhrasePayload, error) {
	state, err := protocol.MergePhrase(left.State, right.State)
	if err != nil {
		return PhrasePayload{}, err
	}
	lastUsedDay := left.LastUsedDay
	if right.LastUsedDay > lastUsedDay {
		lastUsedDay = right.LastUsedDay
	}
	return PhrasePayload{
		Text: chooseStableString(left.Text, right.Text), Pinyin: chooseStableString(left.Pinyin, right.Pinyin),
		Source: chooseStableString(left.Source, right.Source), LastUsedDay: lastUsedDay, State: state,
	}, nil
}

// MergeRemotePayload applies a verified/decrypted remote payload without
// echoing it to the outbox. A subsequent local mutation advances the observed
// HLC and is therefore causally newer.
func (store *Store) MergeRemotePayload(ctx context.Context, payload PhrasePayload) error {
	store.mutation.Lock()
	defer store.mutation.Unlock()
	objectID, err := validatePayloadObject(payload, store.idKey[:])
	if err != nil {
		return err
	}
	existing, found, err := store.loadByID(ctx, objectID[:])
	if err != nil {
		return err
	}
	merged := payload
	if found {
		if existing.CRDT.ObjectID == "" {
			clock, err := store.nextHLC(ctx)
			if err != nil {
				return err
			}
			if err := ensurePhraseState(&existing, objectID, store.deviceID, clock); err != nil {
				return err
			}
		}
		merged, err = MergePhrasePayload(PhrasePayload{
			Text: existing.Text, Pinyin: existing.Pinyin, Source: existing.Source,
			LastUsedDay: existing.LastUsedDay, State: existing.CRDT,
		}, payload)
		if err != nil {
			return err
		}
	}
	if _, err := validatePayloadObject(merged, store.idKey[:]); err != nil {
		return err
	}
	phrase := Phrase{
		Text: merged.Text, Pinyin: merged.Pinyin, Source: merged.Source,
		LastUsedDay: merged.LastUsedDay, CRDT: clonePhraseState(merged.State),
	}
	materializePhrase(&phrase)
	latestClock := phrase.CRDT.Pinned.Clock
	if latestClock.Compare(phrase.CRDT.Presence.Clock) < 0 {
		latestClock = phrase.CRDT.Presence.Clock
	}
	if err := store.observeHLC(ctx, latestClock); err != nil {
		return err
	}
	return store.upsert(ctx, phrase, false)
}
