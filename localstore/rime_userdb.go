// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/kukuyan/yunpin-ime/protocol"
)

// RimeUserDBObservation is one cumulative counter from a trusted, local Rime
// userdb export. LocalOnly advances the device-local high water but never
// materializes the phrase or creates sync traffic.
type RimeUserDBObservation struct {
	Phrase    Phrase
	Commits   uint64
	LocalOnly bool
}

type RimeUserDBImportResult struct {
	Rows      int
	Advanced  int
	Resets    int
	LocalOnly int
	Ignored   int
}

func canonicalPhraseDenySet(values []string) (map[string]struct{}, error) {
	if len(values) > 100000 {
		return nil, errors.New("Rime userdb local-only phrase set exceeds the import row limit")
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		canonical := protocol.CanonicalPhrase(value)
		if canonical == "" {
			return nil, errors.New("Rime userdb local-only phrase is invalid")
		}
		result[canonical] = struct{}{}
	}
	return result, nil
}

// scrubRimeBaselineOutbox prevents a pre-existing pronunciation or source for
// a baseline phrase from crossing the phrase-only privacy boundary. A durable
// prepared upload cannot be discarded safely after a possible lost response,
// so that ambiguous state fails closed and remains available for repair.
func (store *Store) scrubRimeBaselineOutbox(ctx context.Context, transaction *sql.Tx,
	localOnly map[string]struct{}) error {
	if len(localOnly) == 0 {
		return nil
	}
	var prepared sql.NullInt64
	if err := transaction.QueryRowContext(ctx,
		"SELECT prepared_event_id FROM sync_state WHERE singleton = 1 AND device_id = ?",
		store.deviceID,
	).Scan(&prepared); err != nil {
		return err
	}
	rows, err := transaction.QueryContext(ctx,
		"SELECT event_id, object_id, nonce, ciphertext FROM encrypted_outbox ORDER BY event_id",
	)
	if err != nil {
		return err
	}
	var deleteIDs []int64
	preparedMatched := !prepared.Valid
	for rows.Next() {
		var eventID int64
		var objectID, nonce, ciphertext []byte
		if err := rows.Scan(&eventID, &objectID, &nonce, &ciphertext); err != nil {
			rows.Close()
			return err
		}
		phrase, err := store.open(objectID, nonce, ciphertext)
		if err != nil {
			rows.Close()
			return err
		}
		if prepared.Valid && prepared.Int64 == eventID {
			preparedMatched = true
		}
		if _, denied := localOnly[protocol.CanonicalPhrase(phrase.Text)]; !denied {
			continue
		}
		if prepared.Valid && prepared.Int64 == eventID {
			rows.Close()
			return errors.New("a local-only baseline phrase has an ambiguous prepared upload")
		}
		deleteIDs = append(deleteIDs, eventID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !preparedMatched {
		return errors.New("prepared upload is not bound to its encrypted outbox row")
	}
	for _, eventID := range deleteIDs {
		if _, err := transaction.ExecContext(ctx,
			"DELETE FROM encrypted_outbox WHERE event_id = ?", eventID,
		); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) nextHLCInTransaction(ctx context.Context, transaction *sql.Tx) (protocol.HLC, error) {
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
	return protocol.HLC{WallMillis: wallMillis, Counter: uint32(counter), Node: store.deviceID}, nil
}

func (store *Store) loadByIDInTransaction(ctx context.Context, transaction *sql.Tx, objectID []byte) (Phrase, bool, error) {
	var nonce, ciphertext []byte
	err := transaction.QueryRowContext(ctx,
		"SELECT nonce, ciphertext FROM encrypted_phrases WHERE object_id = ?", objectID,
	).Scan(&nonce, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return Phrase{}, false, nil
	}
	if err != nil {
		return Phrase{}, false, err
	}
	phrase, err := store.open(objectID, nonce, ciphertext)
	return phrase, true, err
}

func (store *Store) upsertRimeObservation(ctx context.Context, transaction *sql.Tx, objectID [16]byte, phrase Phrase, enqueue bool) error {
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
	now := store.now().UnixMilli()
	if _, err := transaction.ExecContext(ctx, `INSERT INTO encrypted_phrases(object_id, nonce, ciphertext, updated_at)
VALUES(?, ?, ?, ?)
ON CONFLICT(object_id) DO UPDATE SET nonce=excluded.nonce, ciphertext=excluded.ciphertext, updated_at=excluded.updated_at`,
		objectID[:], nonce, ciphertext, now); err != nil {
		return err
	}
	if err := bumpGeneration(ctx, transaction); err != nil {
		return err
	}
	if enqueue {
		_, err = transaction.ExecContext(ctx, `INSERT INTO encrypted_outbox(object_id, nonce, ciphertext, created_at)
VALUES(?, ?, ?, ?)
ON CONFLICT(object_id) DO UPDATE SET nonce=excluded.nonce, ciphertext=excluded.ciphertext,
  version=encrypted_outbox.version + 1, created_at=excluded.created_at`,
			objectID[:], eventNonce, eventCiphertext, now)
	}
	return err
}

// ImportRimeUserDB applies one complete local export atomically. The database
// is bound to one random device ID, so each opaque object high water is also a
// per-device counter. A lower cumulative count is treated as a local userdb
// reset: the high water moves down, while synced CRDT counts never decrement.
func (store *Store) ImportRimeUserDB(ctx context.Context, observations []RimeUserDBObservation,
	localOnlyPhrases []string) (RimeUserDBImportResult, error) {
	if store == nil || !store.syncEnabled || !validSyncDeviceID(store.deviceID) {
		return RimeUserDBImportResult{}, errors.New("Rime userdb import requires a device-bound store")
	}
	if len(observations) > 100000 {
		return RimeUserDBImportResult{}, errors.New("Rime userdb export exceeds the import row limit")
	}
	localOnly, err := canonicalPhraseDenySet(localOnlyPhrases)
	if err != nil {
		return RimeUserDBImportResult{}, err
	}
	for _, observation := range observations {
		if observation.LocalOnly {
			canonical := protocol.CanonicalPhrase(observation.Phrase.Text)
			if canonical == "" {
				return RimeUserDBImportResult{}, errors.New("Rime userdb local-only observation has an invalid phrase")
			}
			localOnly[canonical] = struct{}{}
		}
	}
	if len(localOnly) > 100000 {
		return RimeUserDBImportResult{}, errors.New("Rime userdb local-only phrase set exceeds the import row limit")
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return RimeUserDBImportResult{}, err
	}
	defer transaction.Rollback()
	if err := store.scrubRimeBaselineOutbox(ctx, transaction, localOnly); err != nil {
		return RimeUserDBImportResult{}, err
	}
	result := RimeUserDBImportResult{Rows: len(observations)}
	seen := make(map[[16]byte]struct{}, len(observations))
	for _, observation := range observations {
		if observation.Commits > math.MaxInt64 {
			return RimeUserDBImportResult{}, errors.New("Rime userdb cumulative count exceeds the local high-water range")
		}
		objectID, err := store.objectID(observation.Phrase)
		if err != nil {
			return RimeUserDBImportResult{}, err
		}
		if _, duplicate := seen[objectID]; duplicate {
			return RimeUserDBImportResult{}, errors.New("Rime userdb export contains a duplicate canonical phrase identity")
		}
		seen[objectID] = struct{}{}
		var previous int64
		found := true
		if err := transaction.QueryRowContext(ctx,
			"SELECT commits FROM rime_userdb_high_water WHERE device_id = ? AND object_id = ?",
			store.deviceID, objectID[:],
		).Scan(&previous); errors.Is(err, sql.ErrNoRows) {
			found = false
			previous = 0
		} else if err != nil {
			return RimeUserDBImportResult{}, err
		}
		current := int64(observation.Commits)
		if _, err := transaction.ExecContext(ctx, `INSERT INTO rime_userdb_high_water(device_id, object_id, commits)
VALUES(?, ?, ?)
ON CONFLICT(device_id, object_id) DO UPDATE SET commits=excluded.commits`,
			store.deviceID, objectID[:], current); err != nil {
			return RimeUserDBImportResult{}, err
		}
		_, denied := localOnly[protocol.CanonicalPhrase(observation.Phrase.Text)]
		if found && current < previous {
			result.Resets++
			continue
		}
		if current == previous {
			continue
		}
		delta := uint64(current - previous)
		if denied {
			result.LocalOnly++
			continue
		}
		phrase, exists, err := store.loadByIDInTransaction(ctx, transaction, objectID[:])
		if err != nil {
			return RimeUserDBImportResult{}, err
		}
		if !exists {
			phrase = observation.Phrase
			phrase.Source = "rime_userdb"
		}
		clock, err := store.nextHLCInTransaction(ctx, transaction)
		if err != nil {
			return RimeUserDBImportResult{}, err
		}
		if err := ensurePhraseState(&phrase, objectID, store.deviceID, clock); err != nil {
			return RimeUserDBImportResult{}, err
		}
		if !phrase.CRDT.Presence.Present {
			result.Ignored++
			continue
		}
		if math.MaxUint64-phrase.CRDT.Counts[store.deviceID] < delta {
			return RimeUserDBImportResult{}, errors.New("Rime userdb import would overflow the device CRDT counter")
		}
		phrase.CRDT.Counts[store.deviceID] += delta
		phrase.LastUsedDay = store.now().UTC().Unix() / 86400
		materializePhrase(&phrase)
		if err := store.upsertRimeObservation(ctx, transaction, objectID, phrase, phrase.UseCount >= learningThreshold); err != nil {
			return RimeUserDBImportResult{}, err
		}
		result.Advanced++
	}
	if err := transaction.Commit(); err != nil {
		return RimeUserDBImportResult{}, err
	}
	return result, nil
}
