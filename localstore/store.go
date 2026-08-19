// SPDX-License-Identifier: Apache-2.0
// Package localstore provides the encrypted background database used to build
// immutable YunPin candidate snapshots. It is never queried by a key handler.
package localstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/kukuyan/yunpin-ime/protocol"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
	_ "modernc.org/sqlite"
)

const (
	// Every entry in the user's Rime user dictionary is part of the sync set.
	// Keeping a second-use threshold here silently omitted legitimate personal
	// vocabulary from a first full-device sync.
	learningThreshold         uint64 = 1
	maxConsumedNativeReceipts        = 16384
)

type Phrase struct {
	Text        string               `json:"text"`
	Pinyin      string               `json:"pinyin"`
	Source      string               `json:"source"`
	UseCount    uint64               `json:"use_count"`
	Pinned      bool                 `json:"pinned"`
	Deleted     bool                 `json:"deleted"`
	LastUsedDay int64                `json:"last_used_day"`
	CRDT        protocol.PhraseState `json:"crdt"`
}

type LearningContext struct {
	PasswordField bool
	PrivateMode   bool
	OneTimeInput  bool
}

func (learning LearningContext) Disabled() bool {
	return learning.PasswordField || learning.PrivateMode || learning.OneTimeInput
}

type LearnResult struct {
	Recorded     bool
	UseCount     uint64
	SyncEligible bool
}

// NativeSelection is the durable hand-off from a bounded native event sink to
// the background agent. EventID is opaque and only makes a crash between the
// SQLite commit and removal of the spool file idempotent.
type NativeSelection struct {
	EventID string
	Phrase  Phrase
}

type NativeSelectionResult struct {
	LearnResult
	Duplicate bool
}

type Snapshot struct {
	Generation uint64
	Phrases    []Phrase
}

type Store struct {
	db          *sql.DB
	dataKey     [32]byte
	idKey       [32]byte
	deviceID    string
	syncEnabled bool
	random      io.Reader
	now         func() time.Time
	mutation    sync.Mutex
}

func Open(ctx context.Context, path string, dataKey, idKey []byte) (*Store, error) {
	return openStore(ctx, path, dataKey, idKey, "local-only", false)
}

// OpenForDevice enables CRDT/outbox envelope production for one random
// 128-bit device ID represented as 32 lowercase hexadecimal characters.
func OpenForDevice(ctx context.Context, path string, dataKey, idKey []byte, deviceID string) (*Store, error) {
	if !validSyncDeviceID(deviceID) {
		return nil, errors.New("device ID must be 16 bytes encoded as lowercase hexadecimal")
	}
	return openStore(ctx, path, dataKey, idKey, deviceID, true)
}

func openStore(ctx context.Context, path string, dataKey, idKey []byte, deviceID string, syncEnabled bool) (*Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if len(dataKey) != 32 || len(idKey) != 32 {
		return nil, errors.New("data and object ID keys must be 32 bytes")
	}
	dsn := path
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		dsn = "file:" + path
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	dsn += separator + "_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, deviceID: deviceID, syncEnabled: syncEnabled, random: rand.Reader, now: time.Now}
	copy(store.dataKey[:], dataKey)
	copy(store.idKey[:], idKey)
	if err := store.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) initialize(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS encrypted_phrases (
  object_id BLOB PRIMARY KEY CHECK(length(object_id) = 16),
  nonce BLOB NOT NULL CHECK(length(nonce) = 24),
  ciphertext BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS encrypted_outbox (
  event_id INTEGER PRIMARY KEY AUTOINCREMENT,
  object_id BLOB NOT NULL UNIQUE CHECK(length(object_id) = 16),
  nonce BLOB NOT NULL CHECK(length(nonce) = 24),
  ciphertext BLOB NOT NULL,
  version INTEGER NOT NULL DEFAULT 1 CHECK(version > 0),
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS metadata (
  key TEXT PRIMARY KEY,
  value INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS consumed_native_events (
  event_id TEXT PRIMARY KEY CHECK(length(event_id) BETWEEN 1 AND 128),
  consumed_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS rime_userdb_high_water (
  device_id TEXT NOT NULL CHECK(length(device_id) = 32),
  object_id BLOB NOT NULL CHECK(length(object_id) = 16),
  commits INTEGER NOT NULL CHECK(commits >= 0),
  PRIMARY KEY(device_id, object_id)
);
INSERT OR IGNORE INTO metadata(key, value) VALUES('generation', 0);`
	const clockMetadata = `
INSERT OR IGNORE INTO metadata(key, value) VALUES('hlc_wall_ms', 0);
INSERT OR IGNORE INTO metadata(key, value) VALUES('hlc_counter', 0);`
	const syncSchema = `
CREATE TABLE IF NOT EXISTS sync_state (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  device_id TEXT NOT NULL CHECK(length(device_id) = 32),
  cursor INTEGER NOT NULL DEFAULT 0 CHECK(cursor >= 0),
  next_device_sequence INTEGER NOT NULL DEFAULT 1 CHECK(next_device_sequence >= 1),
  previous_hash BLOB CHECK(previous_hash IS NULL OR length(previous_hash) = 32),
  prepared_event_id INTEGER,
  prepared_event_version INTEGER,
  prepared_device_sequence INTEGER,
  prepared_wire BLOB,
  prepared_hash BLOB,
  CHECK (
    (prepared_event_id IS NULL AND prepared_event_version IS NULL AND
     prepared_device_sequence IS NULL AND prepared_wire IS NULL AND prepared_hash IS NULL)
    OR
    (prepared_event_id > 0 AND prepared_event_version > 0 AND
     prepared_device_sequence > 0 AND length(prepared_wire) > 0 AND length(prepared_hash) = 32)
  )
);`
	if _, err := store.db.ExecContext(ctx, schema+clockMetadata+syncSchema); err != nil {
		return fmt.Errorf("initialize local store: %w", err)
	}
	if store.syncEnabled {
		if _, err := store.db.ExecContext(ctx, `INSERT OR IGNORE INTO sync_state(singleton, device_id)
VALUES(1, ?)`, store.deviceID); err != nil {
			return fmt.Errorf("initialize sync state: %w", err)
		}
		var persistedDeviceID string
		if err := store.db.QueryRowContext(ctx, "SELECT device_id FROM sync_state WHERE singleton = 1").Scan(&persistedDeviceID); err != nil {
			return fmt.Errorf("load sync device binding: %w", err)
		}
		if persistedDeviceID != store.deviceID {
			return errors.New("local store is bound to a different sync device")
		}
	}
	return nil
}

func (store *Store) Close() error {
	for index := range store.dataKey {
		store.dataKey[index] = 0
		store.idKey[index] = 0
	}
	return store.db.Close()
}

func (store *Store) objectID(phrase Phrase) ([16]byte, error) {
	if strings.TrimSpace(phrase.Text) == "" || strings.TrimSpace(phrase.Pinyin) == "" {
		return [16]byte{}, errors.New("phrase text and Pinyin are required")
	}
	return protocol.OpaqueObjectID(store.idKey[:], phrase.Text, phrase.Pinyin)
}

func (store *Store) recordKey(objectID []byte) ([]byte, error) {
	key := make([]byte, chacha20poly1305.KeySize)
	reader := hkdf.New(sha256.New, store.dataKey[:], objectID, []byte("yunpin-local-record-v1"))
	if _, err := io.ReadFull(reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

func (store *Store) seal(objectID []byte, phrase Phrase) (nonce, ciphertext []byte, err error) {
	plain, err := json.Marshal(phrase)
	if err != nil {
		return nil, nil, err
	}
	key, err := store.recordKey(objectID)
	if err != nil {
		return nil, nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := io.ReadFull(store.random, nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plain, objectID), nil
}

func (store *Store) open(objectID, nonce, ciphertext []byte) (Phrase, error) {
	key, err := store.recordKey(objectID)
	if err != nil {
		return Phrase{}, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return Phrase{}, err
	}
	plain, err := aead.Open(nil, nonce, ciphertext, objectID)
	if err != nil {
		return Phrase{}, errors.New("local phrase authentication failed")
	}
	var phrase Phrase
	if err := json.Unmarshal(plain, &phrase); err != nil {
		return Phrase{}, errors.New("invalid encrypted phrase record")
	}
	return phrase, nil
}

func bumpGeneration(ctx context.Context, transaction *sql.Tx) error {
	_, err := transaction.ExecContext(ctx, "UPDATE metadata SET value = value + 1 WHERE key = 'generation'")
	return err
}

func pruneConsumedNativeReceipts(ctx context.Context, transaction *sql.Tx) error {
	_, err := transaction.ExecContext(ctx, `DELETE FROM consumed_native_events
WHERE event_id IN (
  SELECT event_id FROM consumed_native_events
  ORDER BY consumed_at DESC, event_id DESC
  LIMIT -1 OFFSET ?
)`, maxConsumedNativeReceipts)
	return err
}

func (store *Store) upsert(ctx context.Context, phrase Phrase, enqueue bool) error {
	return store.upsertWithNativeReceipt(ctx, phrase, enqueue, "")
}

func (store *Store) upsertWithNativeReceipt(ctx context.Context, phrase Phrase, enqueue bool, eventID string) error {
	objectID, err := store.objectID(phrase)
	if err != nil {
		return err
	}
	nonce, ciphertext, err := store.seal(objectID[:], phrase)
	if err != nil {
		return err
	}
	var eventNonce, eventCiphertext []byte
	if enqueue {
		eventNonce, eventCiphertext, err = store.seal(objectID[:], phrase)
		if err != nil {
			return err
		}
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	_, err = transaction.ExecContext(ctx, `INSERT INTO encrypted_phrases(object_id, nonce, ciphertext, updated_at)
VALUES(?, ?, ?, ?)
ON CONFLICT(object_id) DO UPDATE SET nonce=excluded.nonce, ciphertext=excluded.ciphertext, updated_at=excluded.updated_at`,
		objectID[:], nonce, ciphertext, time.Now().UnixMilli())
	if err == nil {
		err = bumpGeneration(ctx, transaction)
	}
	if err == nil && enqueue {
		_, err = transaction.ExecContext(ctx, `INSERT INTO encrypted_outbox(object_id, nonce, ciphertext, created_at)
VALUES(?, ?, ?, ?)
ON CONFLICT(object_id) DO UPDATE SET nonce=excluded.nonce, ciphertext=excluded.ciphertext,
  version=encrypted_outbox.version + 1, created_at=excluded.created_at`,
			objectID[:], eventNonce, eventCiphertext, time.Now().UnixMilli())
	}
	if err == nil && eventID != "" {
		_, err = transaction.ExecContext(ctx, `INSERT INTO consumed_native_events(event_id, consumed_at) VALUES(?, ?)`,
			eventID, store.now().UnixMilli())
		if err == nil {
			err = pruneConsumedNativeReceipts(ctx, transaction)
		}
	}
	if err != nil {
		return err
	}
	return transaction.Commit()
}

// SaveExplicit records a user-reviewed phrase. Pinned phrases are immediately
// sync-eligible in the caller; automatically learned entries use RecordSelection.
func (store *Store) SaveExplicit(ctx context.Context, phrase Phrase) error {
	store.mutation.Lock()
	defer store.mutation.Unlock()
	objectID, err := store.objectID(phrase)
	if err != nil {
		return err
	}
	phrase.Source = strings.TrimSpace(phrase.Source)
	if phrase.Source == "" {
		phrase.Source = "manual"
	}
	wantedPinned := phrase.Pinned
	wantedCount := phrase.UseCount
	wantedLastUsedDay := phrase.LastUsedDay
	existing, found, err := store.loadByID(ctx, objectID[:])
	if err != nil {
		return err
	}
	clock, err := store.nextHLC(ctx)
	if err != nil {
		return err
	}
	if found {
		existing.Text = phrase.Text
		existing.Pinyin = phrase.Pinyin
		existing.Source = phrase.Source
		if wantedLastUsedDay > existing.LastUsedDay {
			existing.LastUsedDay = wantedLastUsedDay
		}
		phrase = existing
	} else {
		// SaveExplicit is an add operation. Deletion always goes through Delete,
		// which makes the generation transition unambiguous.
		phrase.Deleted = false
		phrase.CRDT = protocol.PhraseState{}
	}
	if err := ensurePhraseState(&phrase, objectID, store.deviceID, clock); err != nil {
		return err
	}
	if !phrase.CRDT.Presence.Present {
		if phrase.CRDT.Presence.Generation == ^uint64(0) {
			return errors.New("phrase generation exhausted")
		}
		phrase.CRDT.Presence.Generation++
		phrase.CRDT.Presence.Present = true
		phrase.CRDT.Presence.Clock = clock
	}
	if phrase.CRDT.Pinned.Value != wantedPinned {
		phrase.CRDT.Pinned = protocol.LWWBool{Value: wantedPinned, Clock: clock}
	}
	currentCount := totalCounts(phrase.CRDT.Counts)
	if wantedCount > currentCount {
		delta := wantedCount - currentCount
		if ^uint64(0)-phrase.CRDT.Counts[store.deviceID] < delta {
			return errors.New("phrase use counter exhausted")
		}
		phrase.CRDT.Counts[store.deviceID] += delta
	}
	materializePhrase(&phrase)
	return store.upsert(ctx, phrase, true)
}

func (store *Store) loadByID(ctx context.Context, objectID []byte) (Phrase, bool, error) {
	var nonce, ciphertext []byte
	err := store.db.QueryRowContext(ctx, "SELECT nonce, ciphertext FROM encrypted_phrases WHERE object_id = ?", objectID).Scan(&nonce, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return Phrase{}, false, nil
	}
	if err != nil {
		return Phrase{}, false, err
	}
	phrase, err := store.open(objectID, nonce, ciphertext)
	return phrase, true, err
}

// RecordSelection never writes anything for protected contexts. The first
// explicit selection is immediately sync eligible and later selections
// coalesce the same encrypted outbox record.
func (store *Store) RecordSelection(ctx context.Context, phrase Phrase, learning LearningContext) (LearnResult, error) {
	if learning.Disabled() {
		return LearnResult{}, nil
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	return store.recordSelectionLocked(ctx, phrase, "")
}

// RecordNativeSelection records a background native event and its receipt in
// the same SQLite transaction as the encrypted phrase and outbox mutation.
// Retrying a spool file after a process crash therefore cannot increment the
// phrase again. Protected contexts never produce an event and are not accepted
// by this API.
func (store *Store) RecordNativeSelection(ctx context.Context, selection NativeSelection) (NativeSelectionResult, error) {
	if !validNativeEventID(selection.EventID) {
		return NativeSelectionResult{}, errors.New("native selection event ID is invalid")
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	var consumed int
	err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM consumed_native_events WHERE event_id = ?", selection.EventID).Scan(&consumed)
	if err != nil {
		return NativeSelectionResult{}, err
	}
	if consumed != 0 {
		return NativeSelectionResult{Duplicate: true}, nil
	}
	result, err := store.recordSelectionLocked(ctx, selection.Phrase, selection.EventID)
	if err != nil {
		return NativeSelectionResult{}, err
	}
	return NativeSelectionResult{LearnResult: result}, nil
}

// RecordNativeSelectionReceipt consumes a native spool event without storing
// or enqueueing its phrase. The desktop agent uses this for exact identities
// already present in the immutable personal baseline, so selecting a static
// imported word can never turn that private baseline entry into sync traffic.
func (store *Store) RecordNativeSelectionReceipt(ctx context.Context, eventID string) (NativeSelectionResult, error) {
	if !validNativeEventID(eventID) {
		return NativeSelectionResult{}, errors.New("native selection event ID is invalid")
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return NativeSelectionResult{}, err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `INSERT OR IGNORE INTO consumed_native_events(event_id, consumed_at) VALUES(?, ?)`,
		eventID, store.now().UnixMilli())
	if err != nil {
		return NativeSelectionResult{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return NativeSelectionResult{}, err
	}
	if err := pruneConsumedNativeReceipts(ctx, transaction); err != nil {
		return NativeSelectionResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return NativeSelectionResult{}, err
	}
	return NativeSelectionResult{Duplicate: affected == 0}, nil
}

func validNativeEventID(eventID string) bool {
	if len(eventID) < 1 || len(eventID) > 128 {
		return false
	}
	for _, value := range []byte(eventID) {
		if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
			(value >= '0' && value <= '9') || value == '_' || value == '-' {
			continue
		}
		return false
	}
	return true
}

func (store *Store) recordSelectionLocked(ctx context.Context, phrase Phrase, eventID string) (LearnResult, error) {
	objectID, err := store.objectID(phrase)
	if err != nil {
		return LearnResult{}, err
	}
	existing, found, err := store.loadByID(ctx, objectID[:])
	if err != nil {
		return LearnResult{}, err
	}
	if found {
		phrase = existing
	} else {
		phrase.Source = "learned"
	}
	clock, err := store.nextHLC(ctx)
	if err != nil {
		return LearnResult{}, err
	}
	if err := ensurePhraseState(&phrase, objectID, store.deviceID, clock); err != nil {
		return LearnResult{}, err
	}
	if !phrase.CRDT.Presence.Present {
		if eventID != "" {
			transaction, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				return LearnResult{}, err
			}
			defer transaction.Rollback()
			if _, err := transaction.ExecContext(ctx, `INSERT INTO consumed_native_events(event_id, consumed_at) VALUES(?, ?)`,
				eventID, store.now().UnixMilli()); err != nil {
				return LearnResult{}, err
			}
			if err := pruneConsumedNativeReceipts(ctx, transaction); err != nil {
				return LearnResult{}, err
			}
			if err := transaction.Commit(); err != nil {
				return LearnResult{}, err
			}
		}
		return LearnResult{Recorded: false, UseCount: phrase.UseCount}, nil
	}
	if phrase.CRDT.Counts[store.deviceID] == ^uint64(0) {
		return LearnResult{}, errors.New("phrase use counter exhausted")
	}
	phrase.CRDT.Counts[store.deviceID]++
	phrase.LastUsedDay = store.now().UTC().Unix() / 86400
	materializePhrase(&phrase)
	if err := store.upsertWithNativeReceipt(ctx, phrase, phrase.UseCount >= learningThreshold, eventID); err != nil {
		return LearnResult{}, err
	}
	return LearnResult{Recorded: true, UseCount: phrase.UseCount, SyncEligible: phrase.UseCount >= learningThreshold}, nil
}

func (store *Store) Delete(ctx context.Context, text, pinyin string) error {
	store.mutation.Lock()
	defer store.mutation.Unlock()
	probe := Phrase{Text: text, Pinyin: pinyin}
	objectID, err := store.objectID(probe)
	if err != nil {
		return err
	}
	phrase, found, err := store.loadByID(ctx, objectID[:])
	if err != nil {
		return err
	}
	if !found {
		phrase = probe
		phrase.Source = "tombstone"
	}
	clock, err := store.nextHLC(ctx)
	if err != nil {
		return err
	}
	if err := ensurePhraseState(&phrase, objectID, store.deviceID, clock); err != nil {
		return err
	}
	phrase.CRDT.Presence.Present = false
	phrase.CRDT.Presence.Clock = clock
	materializePhrase(&phrase)
	return store.upsert(ctx, phrase, true)
}

// PendingEventCount is an operational/background-worker metric. Outbox rows
// are encrypted and created in the same transaction as the phrase mutation.
func (store *Store) PendingEventCount(ctx context.Context) (uint64, error) {
	var count uint64
	err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM encrypted_outbox").Scan(&count)
	return count, err
}

type PendingEvent struct {
	ID       int64
	Version  uint64
	ObjectID [16]byte
	Phrase   Phrase
	deviceID string
	syncable bool
}

// PendingEvents decrypts a bounded outbox snapshot for the background sync
// worker. The worker converts Phrase into a protocol CRDT payload and calls
// protocol.Seal; no plaintext is sent to the relay.
func (store *Store) PendingEvents(ctx context.Context, limit int) ([]PendingEvent, error) {
	if limit < 1 || limit > 256 {
		return nil, errors.New("outbox limit must be between 1 and 256")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT event_id, version, object_id, nonce, ciphertext
FROM encrypted_outbox ORDER BY event_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []PendingEvent
	for rows.Next() {
		var event PendingEvent
		var objectID, nonce, ciphertext []byte
		if err := rows.Scan(&event.ID, &event.Version, &objectID, &nonce, &ciphertext); err != nil {
			return nil, err
		}
		if len(objectID) != len(event.ObjectID) {
			return nil, errors.New("invalid outbox object ID")
		}
		copy(event.ObjectID[:], objectID)
		event.deviceID = store.deviceID
		event.syncable = store.syncEnabled
		event.Phrase, err = store.open(objectID, nonce, ciphertext)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// AckPending removes exactly the version that was sealed and accepted. If a
// newer local count coalesced into the row meanwhile, the acknowledgement is a
// no-op so that update cannot be lost.
func (store *Store) AckPending(ctx context.Context, eventID int64, version uint64) (bool, error) {
	result, err := store.db.ExecContext(ctx, "DELETE FROM encrypted_outbox WHERE event_id = ? AND version = ?", eventID, version)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (store *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	if err := store.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'generation'").Scan(&snapshot.Generation); err != nil {
		return Snapshot{}, err
	}
	rows, err := store.db.QueryContext(ctx, "SELECT object_id, nonce, ciphertext FROM encrypted_phrases ORDER BY object_id")
	if err != nil {
		return Snapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var objectID, nonce, ciphertext []byte
		if err := rows.Scan(&objectID, &nonce, &ciphertext); err != nil {
			return Snapshot{}, err
		}
		phrase, err := store.open(objectID, nonce, ciphertext)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Phrases = append(snapshot.Phrases, phrase)
	}
	return snapshot, rows.Err()
}
