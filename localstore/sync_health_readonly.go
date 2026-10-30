// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
)

// ReadSyncHealthForSchedule reads only existing scheduling metadata. It does
// not initialize a database, decrypt vocabulary or create an empty history.
// The caller checks the private path and holds the existing operation lock.
func ReadSyncHealthForSchedule(ctx context.Context, path string) (SyncHealth, error) {
	if !filepath.IsAbs(path) {
		return SyncHealth{}, errors.New("sync health database path must be absolute")
	}
	p := filepath.ToSlash(path)
	if len(p) >= 2 && p[1] == ':' {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(ON)")
	q.Add("_pragma", "busy_timeout(1000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return SyncHealth{}, err
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	h := SyncHealth{LastFailureClass: SyncFailureNone}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SyncHealth{}, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT last_success_at, last_event_at, last_event_code
 FROM sync_health WHERE singleton = 1`).Scan(&h.LastSuccessAt, &h.LastEventAt, &h.LastEventCode)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil || h.LastSuccessAt < 0 || h.LastEventAt < 0 {
		return SyncHealth{}, errors.New("sync health history is unavailable or invalid")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT last_network_at,in_flight FROM windows_idle_sync WHERE singleton=1`).Scan(&h.LastNetworkAt, &active); err != nil || h.LastNetworkAt < 0 || (active != 0 && active != 1) {
		return SyncHealth{}, errors.New("Windows synchronization checkpoint is unavailable or invalid")
	}
	h.SyncInFlight = active == 1
	if err := tx.Commit(); err != nil {
		return SyncHealth{}, err
	}
	return h, nil
}
