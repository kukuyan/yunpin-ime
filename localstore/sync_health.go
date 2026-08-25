// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Background synchronization used to expose nothing but "the process exists".
// SyncHealth is the small observational record that makes the difference
// between a healthy agent and a crash loop visible.
//
// Every field is a timestamp, a bounded category or a counter. There is no
// phrase, pinyin, ciphertext, endpoint, account or device identifier here, and
// there must never be: this record is surfaced to the user and written to a log
// file, so it has to stay safe to show and safe to keep.
type SyncHealth struct {
	// LastSuccessAt is the Unix millisecond time of the last fully successful
	// round. Zero means synchronization has never completed.
	LastSuccessAt int64 `json:"last_success_at"`
	// LastEventAt is when the most recent round finished, successful or not.
	LastEventAt int64 `json:"last_event_at"`
	// LastEventCode is the stable category of that round. It is one of the
	// runner's codes; free text is rejected.
	LastEventCode string `json:"last_event_code"`
	// LastFailureClass identifies the layer that stopped the most recent
	// failed round. It is a closed, redacted category rather than an error
	// string. Successful and deferred rounds always use "none".
	LastFailureClass string `json:"last_failure_class"`
	// Cursor is the sync cursor after the last round.
	Cursor int64 `json:"cursor"`
	// PendingUploads is how many local mutations were still queued.
	PendingUploads int64 `json:"pending_uploads"`
}

// Only these categories may be persisted. A code the runner does not produce is
// rejected rather than stored, so a future change cannot quietly turn this
// column into a free-text error field. These are exactly the codes the runner
// produces today. Failure detail is independently constrained to the closed
// classification below.
var allowedSyncHealthCodes = map[string]struct{}{
	"sync_complete":      {},
	"sync_failed":        {},
	"sync_deferred_busy": {},
}

const (
	SyncFailureNone          = "none"
	SyncFailureNetwork       = "network"
	SyncFailureAuth          = "auth"
	SyncFailureRelayProtocol = "relay_protocol"
	SyncFailureLocalStore    = "local_store"
	// SyncFailureUnknown is used only when migrating a historical
	// sync_failed row that predates failure classification. New failures must
	// always choose one of the concrete classes above.
	SyncFailureUnknown = "unknown"
)

var allowedSyncFailureClasses = map[string]struct{}{
	SyncFailureNone:          {},
	SyncFailureNetwork:       {},
	SyncFailureAuth:          {},
	SyncFailureRelayProtocol: {},
	SyncFailureLocalStore:    {},
}

const syncHealthSchema = `
CREATE TABLE IF NOT EXISTS sync_health (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  last_success_at INTEGER NOT NULL DEFAULT 0 CHECK(last_success_at >= 0),
  last_event_at INTEGER NOT NULL DEFAULT 0 CHECK(last_event_at >= 0),
  last_event_code TEXT NOT NULL DEFAULT '' CHECK(length(last_event_code) <= 32),
  last_failure_class TEXT NOT NULL DEFAULT 'none' CHECK(length(last_failure_class) <= 32),
  cursor INTEGER NOT NULL DEFAULT 0 CHECK(cursor >= 0),
  pending_uploads INTEGER NOT NULL DEFAULT 0 CHECK(pending_uploads >= 0)
);`

// ErrUnknownSyncHealthCode reports a category the runner does not produce.
var ErrUnknownSyncHealthCode = errors.New("unknown sync health code")

// ErrUnknownSyncFailureClass reports a failure class outside the closed,
// redacted set above.
var ErrUnknownSyncFailureClass = errors.New("unknown sync failure class")

// RecordSyncHealth stores the outcome of one synchronization round.
//
// A failed round never clears the last successful timestamp: "when did this
// last work" is the single most useful thing to know when it stops working.
func (store *Store) RecordSyncHealth(ctx context.Context, health SyncHealth) error {
	if _, ok := allowedSyncHealthCodes[health.LastEventCode]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSyncHealthCode, health.LastEventCode)
	}
	if _, ok := allowedSyncFailureClasses[health.LastFailureClass]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSyncFailureClass, health.LastFailureClass)
	}
	if health.LastEventCode == "sync_failed" {
		if health.LastFailureClass == SyncFailureNone {
			return errors.New("failed sync health requires a failure class")
		}
	} else if health.LastFailureClass != SyncFailureNone {
		return errors.New("non-failed sync health must use failure class none")
	}
	if health.LastSuccessAt < 0 || health.LastEventAt < 0 || health.Cursor < 0 || health.PendingUploads < 0 {
		return errors.New("sync health values must not be negative")
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	_, err := store.db.ExecContext(ctx, `INSERT INTO sync_health(
  singleton, last_success_at, last_event_at, last_event_code, last_failure_class, cursor, pending_uploads)
VALUES(1, ?, ?, ?, ?, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET
  last_success_at = MAX(sync_health.last_success_at, excluded.last_success_at),
  last_event_at = excluded.last_event_at,
  last_event_code = excluded.last_event_code,
  last_failure_class = excluded.last_failure_class,
  cursor = excluded.cursor,
  pending_uploads = excluded.pending_uploads`,
		health.LastSuccessAt, health.LastEventAt, health.LastEventCode, health.LastFailureClass,
		health.Cursor, health.PendingUploads)
	return err
}

// LoadSyncHealth reports the stored record. A database that has never
// synchronized yields the zero value rather than an error.
func (store *Store) LoadSyncHealth(ctx context.Context) (SyncHealth, error) {
	health := SyncHealth{LastFailureClass: SyncFailureNone}
	err := store.db.QueryRowContext(ctx, `SELECT last_success_at, last_event_at,
	last_event_code, last_failure_class, cursor, pending_uploads FROM sync_health WHERE singleton = 1`).
		Scan(&health.LastSuccessAt, &health.LastEventAt, &health.LastEventCode,
			&health.LastFailureClass, &health.Cursor, &health.PendingUploads)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncHealth{LastFailureClass: SyncFailureNone}, nil
	}
	return health, err
}

type syncHealthSchemaQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func syncHealthFailureColumnExists(ctx context.Context, querier syncHealthSchemaQuerier) (bool, error) {
	rows, err := querier.QueryContext(ctx, "PRAGMA table_info(sync_health)")
	if err != nil {
		return false, err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return false, err
		}
		if name == "last_failure_class" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	return found, nil
}

// migrateSyncHealth adds the classified-failure column to databases created
// before P1-04. Historical failed rows cannot be classified truthfully after
// the fact, so they receive the explicit closed value "unknown"; all future
// writes must use a concrete class.
func (store *Store) migrateSyncHealth(ctx context.Context) error {
	found, err := syncHealthFailureColumnExists(ctx, store.db)
	if err != nil || found {
		return err
	}
	transaction, err := beginImmediateWithRetry(ctx, store.db)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	// Another process may have completed the migration while this opener was
	// waiting for the immediate write transaction. Recheck under that lock so
	// concurrent first opens cannot race into duplicate ALTER TABLE statements.
	found, err = syncHealthFailureColumnExists(ctx, transaction)
	if err != nil {
		return err
	}
	if found {
		return transaction.Commit()
	}
	if _, err := transaction.ExecContext(ctx, `ALTER TABLE sync_health
ADD COLUMN last_failure_class TEXT NOT NULL DEFAULT 'none' CHECK(length(last_failure_class) <= 32)`); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE sync_health SET last_failure_class = ?
WHERE last_event_code = 'sync_failed'`, SyncFailureUnknown); err != nil {
		return err
	}
	return transaction.Commit()
}
