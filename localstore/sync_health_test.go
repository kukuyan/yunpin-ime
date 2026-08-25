// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestSyncHealthStartsEmpty(t *testing.T) {
	store, _ := openTestStore(t)
	health, err := store.LoadSyncHealth(context.Background())
	if err != nil {
		t.Fatalf("LoadSyncHealth: %v", err)
	}
	if health != (SyncHealth{LastFailureClass: SyncFailureNone}) {
		t.Fatalf("a database that never synchronized reported %+v", health)
	}
}

func TestSyncHealthRoundTrips(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	want := SyncHealth{
		LastSuccessAt: 1700000000000, LastEventAt: 1700000000000,
		LastEventCode: "sync_complete", LastFailureClass: SyncFailureNone,
		Cursor: 42, PendingUploads: 3,
	}
	if err := store.RecordSyncHealth(ctx, want); err != nil {
		t.Fatalf("RecordSyncHealth: %v", err)
	}
	got, err := store.LoadSyncHealth(ctx)
	if err != nil {
		t.Fatalf("LoadSyncHealth: %v", err)
	}
	if got != want {
		t.Fatalf("round trip changed the record:\n got %+v\nwant %+v", got, want)
	}
}

// The whole point of recording health is to answer "when did this last work"
// after it stopped working. A failure must not erase that answer.
func TestSyncHealthFailureKeepsLastSuccess(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	if err := store.RecordSyncHealth(ctx, SyncHealth{
		LastSuccessAt: 1700000000000, LastEventAt: 1700000000000,
		LastEventCode: "sync_complete", LastFailureClass: SyncFailureNone, Cursor: 42,
	}); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if err := store.RecordSyncHealth(ctx, SyncHealth{
		LastSuccessAt: 0, LastEventAt: 1700000600000,
		LastEventCode: "sync_failed", LastFailureClass: SyncFailureNetwork,
		Cursor: 42, PendingUploads: 7,
	}); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	health, err := store.LoadSyncHealth(ctx)
	if err != nil {
		t.Fatalf("LoadSyncHealth: %v", err)
	}
	if health.LastSuccessAt != 1700000000000 {
		t.Fatalf("a failed round cleared the last success time: %+v", health)
	}
	if health.LastEventCode != "sync_failed" || health.LastFailureClass != SyncFailureNetwork ||
		health.LastEventAt != 1700000600000 {
		t.Fatalf("the failure was not recorded: %+v", health)
	}
	if health.PendingUploads != 7 {
		t.Fatalf("pending uploads not updated: %+v", health)
	}
}

// The record is shown to the user and written to a log file, so the code column
// must stay a bounded category and never become a place to stash an error
// string.
func TestSyncHealthRejectsUnknownCode(t *testing.T) {
	store, _ := openTestStore(t)
	err := store.RecordSyncHealth(context.Background(), SyncHealth{
		LastEventCode: "dial tcp 203.0.113.10:443: connection refused", LastFailureClass: SyncFailureNone,
	})
	if !errors.Is(err, ErrUnknownSyncHealthCode) {
		t.Fatalf("expected an unknown-code rejection, got %v", err)
	}
}

func TestSyncHealthRejectsUnknownOrInconsistentFailureClass(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	for _, health := range []SyncHealth{
		{LastEventCode: "sync_failed", LastFailureClass: "dial tcp secret.invalid"},
		{LastEventCode: "sync_failed", LastFailureClass: SyncFailureUnknown},
		{LastEventCode: "sync_failed", LastFailureClass: SyncFailureNone},
		{LastEventCode: "sync_complete", LastFailureClass: SyncFailureNetwork},
	} {
		if err := store.RecordSyncHealth(ctx, health); err == nil {
			t.Fatalf("accepted inconsistent or unbounded failure class: %+v", health)
		}
	}
}

func TestSyncHealthSerializesWithoutIdentifiers(t *testing.T) {
	encoded, err := json.Marshal(SyncHealth{
		LastSuccessAt: 1, LastEventAt: 2, LastEventCode: "sync_complete", LastFailureClass: SyncFailureNone,
		Cursor: 3, PendingUploads: 4,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	expected := map[string]struct{}{
		"last_success_at": {}, "last_event_at": {}, "last_event_code": {},
		"last_failure_class": {}, "cursor": {}, "pending_uploads": {},
	}
	for name := range decoded {
		if _, ok := expected[name]; !ok {
			t.Fatalf("unexpected field %q in the user-visible health record", name)
		}
	}
	for name := range expected {
		if _, ok := decoded[name]; !ok {
			t.Fatalf("missing field %q", name)
		}
	}
}

func TestSyncHealthMigratesHistoricalFailureWithoutInventingItsCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`CREATE TABLE sync_health (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  last_success_at INTEGER NOT NULL DEFAULT 0,
  last_event_at INTEGER NOT NULL DEFAULT 0,
  last_event_code TEXT NOT NULL DEFAULT '',
  cursor INTEGER NOT NULL DEFAULT 0,
  pending_uploads INTEGER NOT NULL DEFAULT 0
);
INSERT INTO sync_health(singleton, last_success_at, last_event_at, last_event_code, cursor, pending_uploads)
VALUES(1, 1700000000000, 1700000600000, 'sync_failed', 42, 7);`)
	closeErr := database.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	store, err := Open(context.Background(), path, bytes.Repeat([]byte{0x21}, 32), bytes.Repeat([]byte{0x43}, 32))
	if err != nil {
		t.Fatalf("open and migrate historical store: %v", err)
	}
	defer store.Close()
	health, err := store.LoadSyncHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if health.LastSuccessAt != 1700000000000 || health.LastEventCode != "sync_failed" ||
		health.LastFailureClass != SyncFailureUnknown || health.Cursor != 42 || health.PendingUploads != 7 {
		t.Fatalf("historical health changed or was misclassified: %+v", health)
	}
}

func TestSyncHealthMigrationSerializesConcurrentFirstOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE sync_health (
  singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
  last_success_at INTEGER NOT NULL DEFAULT 0,
  last_event_at INTEGER NOT NULL DEFAULT 0,
  last_event_code TEXT NOT NULL DEFAULT '',
  cursor INTEGER NOT NULL DEFAULT 0,
  pending_uploads INTEGER NOT NULL DEFAULT 0
);`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			store, err := Open(context.Background(), path,
				bytes.Repeat([]byte{0x21}, 32), bytes.Repeat([]byte{0x43}, 32))
			if err == nil {
				err = store.Close()
			}
			errorsSeen <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent first open failed migration: %v", err)
		}
	}
	store, err := Open(context.Background(), path,
		bytes.Repeat([]byte{0x21}, 32), bytes.Repeat([]byte{0x43}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	health, err := store.LoadSyncHealth(context.Background())
	if err != nil || health.LastFailureClass != SyncFailureNone {
		t.Fatalf("migrated store health=%+v err=%v", health, err)
	}
}
