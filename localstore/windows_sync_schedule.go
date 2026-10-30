// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"context"
	"errors"
	"time"
)

// EnsureWindowsSyncSchedule seeds older databases from their last complete
// round. Call under the same operation lock used by manual synchronization.
func (store *Store) EnsureWindowsSyncSchedule(ctx context.Context) error {
	store.mutation.Lock()
	defer store.mutation.Unlock()
	tx, err := beginImmediateWithRetry(ctx, store.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS windows_idle_sync (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 last_network_at INTEGER NOT NULL CHECK(last_network_at>=0),
 in_flight INTEGER NOT NULL CHECK(in_flight IN (0,1)))`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO windows_idle_sync(singleton,last_network_at,in_flight)
 VALUES(1,COALESCE((SELECT last_success_at FROM sync_health WHERE singleton=1),0),0)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) updateWindowsSchedule(ctx context.Context, query string, args ...any) error {
	store.mutation.Lock()
	defer store.mutation.Unlock()
	result, err := store.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("Windows synchronization checkpoint is unavailable")
	}
	return nil
}

// The existing operation lock serializes manual and automatic rounds. A
// surviving in-flight marker is not evidence of a successful operation.
func (store *Store) BeginWindowsSync(ctx context.Context) error {
	return store.updateWindowsSchedule(ctx, `UPDATE windows_idle_sync SET in_flight=1 WHERE singleton=1`)
}

func (store *Store) RecordWindowsNetworkCompleted(ctx context.Context, now time.Time) error {
	if now.IsZero() || now.UnixMilli() <= 0 {
		return errors.New("invalid network completion time")
	}
	return store.updateWindowsSchedule(ctx, `UPDATE windows_idle_sync SET last_network_at=MAX(last_network_at,?) WHERE singleton=1 AND in_flight=1`, now.UnixMilli())
}

func (store *Store) FinishWindowsSync(ctx context.Context) error {
	return store.updateWindowsSchedule(ctx, `UPDATE windows_idle_sync SET in_flight=0 WHERE singleton=1 AND in_flight=1`)
}

// A previous process may have disappeared with an unknown outcome. Start a
// fresh conservative six-hour delay at recovery, without declaring success or
// retrying the old operation. Call only while holding the operation lock.
func (store *Store) RecoverWindowsSyncDelay(ctx context.Context, now time.Time) error {
	if now.IsZero() || now.UnixMilli() <= 0 {
		return errors.New("invalid recovery time")
	}
	store.mutation.Lock()
	defer store.mutation.Unlock()
	_, err := store.db.ExecContext(ctx, `UPDATE windows_idle_sync SET last_network_at=MAX(last_network_at,?), in_flight=0 WHERE singleton=1 AND in_flight=1`, now.UnixMilli())
	return err
}
