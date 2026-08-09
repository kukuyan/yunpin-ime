// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"bytes"
	"context"
	"errors"
	"math"
)

const maxPreparedWireBytes = 1 << 20

// PreparedUpload is a ciphertext-only, durably staged upload. Persisting the
// exact wire bytes before a request makes a lost HTTP response safely
// retryable: the relay sees an identical (device_id, device_seq) record.
type PreparedUpload struct {
	EventID        int64
	EventVersion   uint64
	DeviceSequence uint64
	Wire           []byte
	EnvelopeHash   []byte
}

// SyncState contains non-secret relay progress. Account tokens, encryption
// keys and signing keys deliberately do not enter the SQLite database.
type SyncState struct {
	DeviceID           string
	Cursor             int64
	NextDeviceSequence uint64
	PreviousHash       []byte
	Prepared           *PreparedUpload
}

func (store *Store) requireSync() error {
	if !store.syncEnabled || !validSyncDeviceID(store.deviceID) {
		return errors.New("store was not opened with OpenForDevice")
	}
	return nil
}

// LoadSyncState returns a defensive copy of the persisted background-worker
// checkpoint.
func (store *Store) LoadSyncState(ctx context.Context) (SyncState, error) {
	if err := store.requireSync(); err != nil {
		return SyncState{}, err
	}
	var state SyncState
	var next int64
	var previous []byte
	var eventID, eventVersion, preparedSequence interface{}
	var wire, hash []byte
	err := store.db.QueryRowContext(ctx, `SELECT cursor, next_device_sequence, previous_hash,
prepared_event_id, prepared_event_version, prepared_device_sequence, prepared_wire, prepared_hash
FROM sync_state WHERE singleton = 1 AND device_id = ?`, store.deviceID).Scan(
		&state.Cursor, &next, &previous, &eventID, &eventVersion, &preparedSequence, &wire, &hash,
	)
	if err != nil {
		return SyncState{}, err
	}
	if next < 1 {
		return SyncState{}, errors.New("invalid persisted device sequence")
	}
	state.NextDeviceSequence = uint64(next)
	state.DeviceID = store.deviceID
	state.PreviousHash = append([]byte(nil), previous...)
	if eventID != nil || eventVersion != nil || preparedSequence != nil || wire != nil || hash != nil {
		id, okID := eventID.(int64)
		version, okVersion := eventVersion.(int64)
		sequence, okSequence := preparedSequence.(int64)
		if !okID || !okVersion || !okSequence || id < 1 || version < 1 || sequence < 1 || len(wire) == 0 || len(hash) != 32 {
			return SyncState{}, errors.New("invalid persisted prepared upload")
		}
		state.Prepared = &PreparedUpload{
			EventID: id, EventVersion: uint64(version), DeviceSequence: uint64(sequence),
			Wire: append([]byte(nil), wire...), EnvelopeHash: append([]byte(nil), hash...),
		}
	}
	return state, nil
}

// SavePreparedUpload stages one exact envelope before any network request.
// A concurrent local mutation may replace the outbox version; in that case the
// caller retries from a fresh PendingEvents snapshot.
func (store *Store) SavePreparedUpload(ctx context.Context, upload PreparedUpload) error {
	if err := store.requireSync(); err != nil {
		return err
	}
	if upload.EventID < 1 || upload.EventVersion < 1 || upload.DeviceSequence < 1 ||
		upload.EventVersion > math.MaxInt64 || upload.DeviceSequence >= math.MaxInt64 ||
		len(upload.Wire) == 0 || len(upload.Wire) > maxPreparedWireBytes || len(upload.EnvelopeHash) != 32 {
		return errors.New("invalid prepared upload")
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	var existingID, existingVersion, existingSequence interface{}
	var existingWire, existingHash []byte
	var next int64
	if err := transaction.QueryRowContext(ctx, `SELECT next_device_sequence, prepared_event_id,
prepared_event_version, prepared_device_sequence, prepared_wire, prepared_hash
FROM sync_state WHERE singleton = 1 AND device_id = ?`, store.deviceID).Scan(
		&next, &existingID, &existingVersion, &existingSequence, &existingWire, &existingHash,
	); err != nil {
		return err
	}
	if existingID != nil {
		if existingID == upload.EventID && existingVersion == int64(upload.EventVersion) &&
			existingSequence == int64(upload.DeviceSequence) && bytes.Equal(existingWire, upload.Wire) &&
			bytes.Equal(existingHash, upload.EnvelopeHash) {
			return transaction.Commit()
		}
		return errors.New("a different upload is already prepared")
	}
	if uint64(next) != upload.DeviceSequence {
		return errors.New("prepared upload sequence does not match checkpoint")
	}
	var found int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM encrypted_outbox
WHERE event_id = ? AND version = ?`, upload.EventID, upload.EventVersion).Scan(&found); err != nil {
		return err
	}
	if found != 1 {
		return errors.New("outbox event changed before upload was prepared")
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE sync_state SET
prepared_event_id = ?, prepared_event_version = ?, prepared_device_sequence = ?,
prepared_wire = ?, prepared_hash = ? WHERE singleton = 1 AND device_id = ?`,
		upload.EventID, upload.EventVersion, upload.DeviceSequence, upload.Wire, upload.EnvelopeHash, store.deviceID); err != nil {
		return err
	}
	return transaction.Commit()
}

// CommitPreparedUpload atomically advances the signed sequence chain, clears
// the staged envelope and acknowledges only the outbox version that was sent.
// The returned bool is false when a newer local mutation replaced that version.
func (store *Store) CommitPreparedUpload(ctx context.Context) (bool, error) {
	if err := store.requireSync(); err != nil {
		return false, err
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer transaction.Rollback()
	var eventID, eventVersion, sequence int64
	var hash []byte
	if err := transaction.QueryRowContext(ctx, `SELECT prepared_event_id, prepared_event_version,
prepared_device_sequence, prepared_hash FROM sync_state
WHERE singleton = 1 AND device_id = ?`, store.deviceID).Scan(&eventID, &eventVersion, &sequence, &hash); err != nil {
		return false, err
	}
	if eventID < 1 || eventVersion < 1 || sequence < 1 || len(hash) != 32 || sequence == math.MaxInt64 {
		return false, errors.New("invalid prepared upload checkpoint")
	}
	result, err := transaction.ExecContext(ctx, `DELETE FROM encrypted_outbox
WHERE event_id = ? AND version = ?`, eventID, eventVersion)
	if err != nil {
		return false, err
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE sync_state SET
next_device_sequence = ?, previous_hash = ?, prepared_event_id = NULL,
prepared_event_version = NULL, prepared_device_sequence = NULL, prepared_wire = NULL,
prepared_hash = NULL WHERE singleton = 1 AND device_id = ?`, sequence+1, hash, store.deviceID); err != nil {
		return false, err
	}
	if err := transaction.Commit(); err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// AdvanceSyncCursor records a fully verified and locally merged download page.
// The compare-and-swap guard prevents concurrent workers from skipping pages.
func (store *Store) AdvanceSyncCursor(ctx context.Context, expected, next int64) error {
	if err := store.requireSync(); err != nil {
		return err
	}
	if expected < 0 || next < expected {
		return errors.New("invalid sync cursor transition")
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	result, err := store.db.ExecContext(ctx, `UPDATE sync_state SET cursor = ?
WHERE singleton = 1 AND device_id = ? AND cursor = ?`, next, store.deviceID, expected)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("sync cursor changed concurrently")
	}
	return nil
}
