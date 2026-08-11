// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"bytes"
	"context"
	"testing"
)

func TestPreparedUploadPersistsAndCommitsExactOutboxVersion(t *testing.T) {
	ctx := context.Background()
	store, _ := openDeviceStore(t, deviceA)
	defer store.Close()
	if err := store.SaveExplicit(ctx, Phrase{Text: "合成状态测试", Pinyin: "he cheng zhuang tai ce shi", Pinned: true}); err != nil {
		t.Fatal(err)
	}
	event := onlyPending(t, store)
	wire := []byte(`{"version":1,"ciphertext":"synthetic"}`)
	hash := bytes.Repeat([]byte{0x5a}, 32)
	prepared := PreparedUpload{
		EventID: event.ID, EventVersion: event.Version, DeviceSequence: 1,
		Wire: wire, EnvelopeHash: hash,
	}
	if err := store.SavePreparedUpload(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadSyncState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Cursor != 0 || state.NextDeviceSequence != 1 || state.Prepared == nil ||
		!bytes.Equal(state.Prepared.Wire, wire) || !bytes.Equal(state.Prepared.EnvelopeHash, hash) {
		t.Fatalf("unexpected prepared state: %#v", state)
	}
	acked, err := store.CommitPreparedUpload(ctx)
	if err != nil || !acked {
		t.Fatalf("commit prepared upload: acked=%v err=%v", acked, err)
	}
	state, err = store.LoadSyncState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Prepared != nil || state.NextDeviceSequence != 2 || !bytes.Equal(state.PreviousHash, hash) {
		t.Fatalf("unexpected committed state: %#v", state)
	}
	if count, err := store.PendingEventCount(ctx); err != nil || count != 0 {
		t.Fatalf("outbox was not acknowledged: count=%d err=%v", count, err)
	}
}

func TestPreparedUploadDoesNotLoseNewerOutboxVersion(t *testing.T) {
	ctx := context.Background()
	store, _ := openDeviceStore(t, deviceA)
	defer store.Close()
	phrase := Phrase{Text: "合成并发测试", Pinyin: "he cheng bing fa ce shi", Pinned: true}
	if err := store.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	event := onlyPending(t, store)
	if err := store.SavePreparedUpload(ctx, PreparedUpload{
		EventID: event.ID, EventVersion: event.Version, DeviceSequence: 1,
		Wire: []byte(`{"version":1}`), EnvelopeHash: bytes.Repeat([]byte{0x6b}, 32),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordSelection(ctx, phrase, LearningContext{}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordSelection(ctx, phrase, LearningContext{}); err != nil {
		t.Fatal(err)
	}
	acked, err := store.CommitPreparedUpload(ctx)
	if err != nil || acked {
		t.Fatalf("newer outbox version was incorrectly acknowledged: acked=%v err=%v", acked, err)
	}
	if count, err := store.PendingEventCount(ctx); err != nil || count != 1 {
		t.Fatalf("newer outbox event missing: count=%d err=%v", count, err)
	}
}

func TestSyncStateBindsDatabaseToOneDeviceAndAdvancesCursor(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
	dataKey := bytes.Repeat([]byte{0x21}, 32)
	idKey := bytes.Repeat([]byte{0x43}, 32)
	store, err := OpenForDevice(ctx, path, dataKey, idKey, deviceA)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceSyncCursor(ctx, 0, 7); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceSyncCursor(ctx, 0, 8); err == nil {
		t.Fatal("stale cursor compare-and-swap was accepted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenForDevice(ctx, path, dataKey, idKey, deviceB); err == nil {
		t.Fatal("database opened with a different device binding")
	}
	localOnly, err := Open(ctx, t.TempDir()+"/local.db", dataKey, idKey)
	if err != nil {
		t.Fatal(err)
	}
	defer localOnly.Close()
	if _, err := localOnly.LoadSyncState(ctx); err == nil {
		t.Fatal("local-only store exposed sync state")
	}
}
