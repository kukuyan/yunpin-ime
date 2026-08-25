// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestSyncHealthStartsEmpty(t *testing.T) {
	store, _ := openTestStore(t)
	health, err := store.LoadSyncHealth(context.Background())
	if err != nil {
		t.Fatalf("LoadSyncHealth: %v", err)
	}
	if health != (SyncHealth{}) {
		t.Fatalf("a database that never synchronized reported %+v", health)
	}
}

func TestSyncHealthRoundTrips(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	want := SyncHealth{
		LastSuccessAt: 1700000000000, LastEventAt: 1700000000000,
		LastEventCode: "sync_complete", Cursor: 42, PendingUploads: 3,
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
		LastEventCode: "sync_complete", Cursor: 42,
	}); err != nil {
		t.Fatalf("record success: %v", err)
	}
	if err := store.RecordSyncHealth(ctx, SyncHealth{
		LastSuccessAt: 0, LastEventAt: 1700000600000,
		LastEventCode: "sync_failed", Cursor: 42, PendingUploads: 7,
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
	if health.LastEventCode != "sync_failed" || health.LastEventAt != 1700000600000 {
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
		LastEventCode: "dial tcp 203.0.113.10:443: connection refused",
	})
	if !errors.Is(err, ErrUnknownSyncHealthCode) {
		t.Fatalf("expected an unknown-code rejection, got %v", err)
	}
}

func TestSyncHealthSerializesWithoutIdentifiers(t *testing.T) {
	encoded, err := json.Marshal(SyncHealth{
		LastSuccessAt: 1, LastEventAt: 2, LastEventCode: "sync_complete",
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
		"cursor": {}, "pending_uploads": {},
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
