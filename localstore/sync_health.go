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
	// Cursor is the sync cursor after the last round.
	Cursor int64 `json:"cursor"`
	// PendingUploads is how many local mutations were still queued.
	PendingUploads int64 `json:"pending_uploads"`
}

// Only these categories may be persisted. A code the runner does not produce is
// rejected rather than stored, so a future change cannot quietly turn this
// column into a free-text error field. These are exactly the codes the runner
// produces today. Finer
// failure categories (network vs. auth vs. relay) would need the runner to
// classify the error first; until it does, inventing them here would be dead
// schema that looks more informative than it is.
var allowedSyncHealthCodes = map[string]struct{}{
	"sync_complete":      {},
	"sync_failed":        {},
	"sync_deferred_busy": {},
}

const syncHealthSchema = `
CREATE TABLE IF NOT EXISTS sync_health (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  last_success_at INTEGER NOT NULL DEFAULT 0 CHECK(last_success_at >= 0),
  last_event_at INTEGER NOT NULL DEFAULT 0 CHECK(last_event_at >= 0),
  last_event_code TEXT NOT NULL DEFAULT '' CHECK(length(last_event_code) <= 32),
  cursor INTEGER NOT NULL DEFAULT 0 CHECK(cursor >= 0),
  pending_uploads INTEGER NOT NULL DEFAULT 0 CHECK(pending_uploads >= 0)
);`

// ErrUnknownSyncHealthCode reports a category the runner does not produce.
var ErrUnknownSyncHealthCode = errors.New("unknown sync health code")

// RecordSyncHealth stores the outcome of one synchronization round.
//
// A failed round never clears the last successful timestamp: "when did this
// last work" is the single most useful thing to know when it stops working.
func (store *Store) RecordSyncHealth(ctx context.Context, health SyncHealth) error {
	if _, ok := allowedSyncHealthCodes[health.LastEventCode]; !ok {
		return fmt.Errorf("%w: %q", ErrUnknownSyncHealthCode, health.LastEventCode)
	}
	if health.LastSuccessAt < 0 || health.LastEventAt < 0 || health.Cursor < 0 || health.PendingUploads < 0 {
		return errors.New("sync health values must not be negative")
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	_, err := store.db.ExecContext(ctx, `INSERT INTO sync_health(
  singleton, last_success_at, last_event_at, last_event_code, cursor, pending_uploads)
VALUES(1, ?, ?, ?, ?, ?)
ON CONFLICT(singleton) DO UPDATE SET
  last_success_at = MAX(sync_health.last_success_at, excluded.last_success_at),
  last_event_at = excluded.last_event_at,
  last_event_code = excluded.last_event_code,
  cursor = excluded.cursor,
  pending_uploads = excluded.pending_uploads`,
		health.LastSuccessAt, health.LastEventAt, health.LastEventCode, health.Cursor, health.PendingUploads)
	return err
}

// LoadSyncHealth reports the stored record. A database that has never
// synchronized yields the zero value rather than an error.
func (store *Store) LoadSyncHealth(ctx context.Context) (SyncHealth, error) {
	var health SyncHealth
	err := store.db.QueryRowContext(ctx, `SELECT last_success_at, last_event_at,
  last_event_code, cursor, pending_uploads FROM sync_health WHERE singleton = 1`).
		Scan(&health.LastSuccessAt, &health.LastEventAt, &health.LastEventCode,
			&health.Cursor, &health.PendingUploads)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncHealth{}, nil
	}
	return health, err
}
