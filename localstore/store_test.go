// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/protocol"
)

const (
	deviceA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	deviceB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private.db")
	store, err := Open(context.Background(), path, bytes.Repeat([]byte{0x21}, 32), bytes.Repeat([]byte{0x43}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func openDeviceStore(t *testing.T, deviceID string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private.db")
	store, err := OpenForDevice(context.Background(), path, bytes.Repeat([]byte{0x21}, 32), bytes.Repeat([]byte{0x43}, 32), deviceID)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.UnixMilli(1_800_000_000_000) }
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func onlyPending(t *testing.T, store *Store) PendingEvent {
	t.Helper()
	events, err := store.PendingEvents(context.Background(), 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d pending events, want 1", len(events))
	}
	return events[0]
}

func TestLearningThresholdAndProtectedContexts(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	phrase := Phrase{Text: "合成隐私测试短语", Pinyin: "he cheng yin si ce shi duan yu"}

	protected := []LearningContext{
		{PasswordField: true}, {PrivateMode: true}, {OneTimeInput: true},
	}
	for _, learning := range protected {
		result, err := store.RecordSelection(ctx, phrase, learning)
		if err != nil || result.Recorded {
			t.Fatalf("protected context learned: result=%#v err=%v", result, err)
		}
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Phrases) != 0 {
		t.Fatalf("protected contexts changed store: %#v err=%v", snapshot, err)
	}

	first, err := store.RecordSelection(ctx, phrase, LearningContext{})
	if err != nil {
		t.Fatal(err)
	}
	if first.UseCount != 1 || !first.SyncEligible {
		t.Fatalf("first selection must be sync eligible: %#v", first)
	}
	pending, err := store.PendingEventCount(ctx)
	if err != nil || pending != 1 {
		t.Fatalf("first selection was not atomically queued: pending=%d err=%v", pending, err)
	}
	second, err := store.RecordSelection(ctx, phrase, LearningContext{})
	if err != nil {
		t.Fatal(err)
	}
	if second.UseCount != 2 || !second.SyncEligible {
		t.Fatalf("second selection must remain sync eligible: %#v", second)
	}
	pending, err = store.PendingEventCount(ctx)
	if err != nil || pending != 1 {
		t.Fatalf("updated selection was not atomically queued: pending=%d err=%v", pending, err)
	}
	events, err := store.PendingEvents(ctx, 256)
	if err != nil || len(events) != 1 || events[0].Phrase.UseCount != 2 {
		t.Fatalf("outbox payload mismatch: events=%#v err=%v", events, err)
	}
	stale := events[0]
	if _, err := store.RecordSelection(ctx, phrase, LearningContext{}); err != nil {
		t.Fatal(err)
	}
	acked, err := store.AckPending(ctx, stale.ID, stale.Version)
	if err != nil || acked {
		t.Fatalf("stale acknowledgement deleted a newer count: acked=%v err=%v", acked, err)
	}
	events, err = store.PendingEvents(ctx, 256)
	if err != nil || len(events) != 1 || events[0].Phrase.UseCount != 3 {
		t.Fatalf("newer count was not retained after stale ack: events=%#v err=%v", events, err)
	}
}

func TestNativeSelectionReceiptIsAtomicAndIdempotent(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	ctx := context.Background()
	selection := NativeSelection{
		EventID: "process_nonce-00000001",
		Phrase:  Phrase{Text: "合成原生事件", Pinyin: "he cheng yuan sheng shi jian"},
	}
	first, err := store.RecordNativeSelection(ctx, selection)
	if err != nil || !first.Recorded || first.Duplicate || first.UseCount != 1 {
		t.Fatalf("first native selection mismatch: result=%#v err=%v", first, err)
	}
	second, err := store.RecordNativeSelection(ctx, selection)
	if err != nil || !second.Duplicate || second.Recorded {
		t.Fatalf("duplicate native selection was not idempotent: result=%#v err=%v", second, err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Phrases) != 1 || snapshot.Phrases[0].UseCount != 1 {
		t.Fatalf("duplicate changed encrypted phrase: snapshot=%#v err=%v", snapshot, err)
	}
	pending, err := store.PendingEventCount(ctx)
	if err != nil || pending != 1 {
		t.Fatalf("first native selection was not queued: pending=%d err=%v", pending, err)
	}
	selection.EventID = "process_nonce-00000002"
	third, err := store.RecordNativeSelection(ctx, selection)
	if err != nil || !third.Recorded || !third.SyncEligible || third.UseCount != 2 {
		t.Fatalf("second distinct native selection mismatch: result=%#v err=%v", third, err)
	}
}

func TestNativeSelectionRejectsUnsafeEventIDs(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	for _, eventID := range []string{"", "../escape", "contains.dot", "含隐私"} {
		_, err := store.RecordNativeSelection(context.Background(), NativeSelection{
			EventID: eventID, Phrase: Phrase{Text: "合成拒绝", Pinyin: "he cheng ju jue"},
		})
		if err == nil {
			t.Fatalf("unsafe native event ID accepted: %q", eventID)
		}
	}
}

func TestNativeSelectionReceiptNeverCreatesPhraseOrOutbox(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	ctx := context.Background()
	first, err := store.RecordNativeSelectionReceipt(ctx, "baseline_event-1")
	if err != nil || first.Duplicate || first.Recorded {
		t.Fatalf("first receipt mismatch: result=%#v err=%v", first, err)
	}
	second, err := store.RecordNativeSelectionReceipt(ctx, "baseline_event-1")
	if err != nil || !second.Duplicate || second.Recorded {
		t.Fatalf("duplicate receipt mismatch: result=%#v err=%v", second, err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Phrases) != 0 {
		t.Fatalf("receipt-only event created phrase state: snapshot=%#v err=%v", snapshot, err)
	}
	pending, err := store.PendingEventCount(ctx)
	if err != nil || pending != 0 {
		t.Fatalf("receipt-only event entered outbox: pending=%d err=%v", pending, err)
	}
}

func TestNativeSelectionReceiptsArePrunedToFixedBound(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	ctx := context.Background()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := transaction.PrepareContext(ctx, `INSERT INTO consumed_native_events(event_id, consumed_at) VALUES(?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxConsumedNativeReceipts+7; index++ {
		if _, err := statement.ExecContext(ctx, fmt.Sprintf("old_%08d", index), index); err != nil {
			t.Fatal(err)
		}
	}
	_ = statement.Close()
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordNativeSelectionReceipt(ctx, "new_receipt"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM consumed_native_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != maxConsumedNativeReceipts {
		t.Fatalf("receipt count=%d want=%d", count, maxConsumedNativeReceipts)
	}
}

func TestDatabaseContainsNoPhrasePlaintext(t *testing.T) {
	store, path := openTestStore(t)
	ctx := context.Background()
	phrase := Phrase{Text: "合成数据库密文探针", Pinyin: "he cheng shu ju ku mi wen tan zhen", Source: "synthetic", Pinned: true}
	if err := store.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	// Force WAL content into the main file before scanning all SQLite artifacts.
	if _, err := store.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		contents, err := os.ReadFile(candidate)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(contents, []byte(phrase.Text)) || bytes.Contains(contents, []byte(phrase.Pinyin)) {
			t.Fatalf("plaintext phrase leaked into %s", filepath.Base(candidate))
		}
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Phrases) != 1 || snapshot.Phrases[0].Text != phrase.Text {
		t.Fatalf("encrypted snapshot mismatch: %#v", snapshot)
	}
}

func TestRemoveWinsLocallyAndGenerationChanges(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	phrase := Phrase{Text: "合成删除测试", Pinyin: "he cheng shan chu ce shi", Source: "synthetic"}
	if err := store.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, phrase.Text, phrase.Pinyin); err != nil {
		t.Fatal(err)
	}
	result, err := store.RecordSelection(ctx, phrase, LearningContext{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Recorded {
		t.Fatal("ordinary count resurrected a tombstone")
	}
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Generation <= before.Generation || !after.Phrases[0].Deleted {
		t.Fatalf("delete/generation mismatch: before=%#v after=%#v", before, after)
	}
}

func TestConcurrentSelectionsDoNotLoseCounts(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()
	phrase := Phrase{Text: "合成并发计数", Pinyin: "he cheng bing fa ji shu"}
	var group sync.WaitGroup
	for index := 0; index < 12; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := store.RecordSelection(ctx, phrase, LearningContext{}); err != nil {
				t.Errorf("selection failed: %v", err)
			}
		}()
	}
	group.Wait()
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Phrases) != 1 || snapshot.Phrases[0].UseCount != 12 {
		t.Fatalf("concurrent count lost: %#v", snapshot)
	}
	pending, err := store.PendingEventCount(ctx)
	if err != nil || pending != 1 {
		t.Fatalf("threshold should enqueue exactly once: pending=%d err=%v", pending, err)
	}
	events, err := store.PendingEvents(ctx, 1)
	if err != nil || len(events) != 1 || events[0].Phrase.UseCount != 12 {
		t.Fatalf("coalesced outbox lost latest count: events=%#v err=%v", events, err)
	}
	acked, err := store.AckPending(ctx, events[0].ID, events[0].Version)
	if err != nil || !acked {
		t.Fatalf("latest outbox acknowledgement failed: acked=%v err=%v", acked, err)
	}
}

func TestCRDTOfflineMergeRemoveWinsAndExplicitReadd(t *testing.T) {
	ctx := context.Background()
	left, _ := openDeviceStore(t, deviceA)
	right, _ := openDeviceStore(t, deviceB)
	phrase := Phrase{Text: "合成离线收敛测试", Pinyin: "he cheng li xian shou lian ce shi", Source: "synthetic"}
	if err := left.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	phrase.Pinned = true
	if err := right.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	for count := 0; count < 3; count++ {
		if _, err := left.RecordSelection(ctx, phrase, LearningContext{}); err != nil {
			t.Fatal(err)
		}
	}
	for count := 0; count < 2; count++ {
		if _, err := right.RecordSelection(ctx, phrase, LearningContext{}); err != nil {
			t.Fatal(err)
		}
	}
	leftPayload, err := onlyPending(t, left).ProtocolPayload()
	if err != nil {
		t.Fatal(err)
	}
	rightPayload, err := onlyPending(t, right).ProtocolPayload()
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergePhrasePayload(leftPayload, rightPayload)
	if err != nil {
		t.Fatal(err)
	}
	if merged.State.Counts[deviceA] != 3 || merged.State.Counts[deviceB] != 2 || totalCounts(merged.State.Counts) != 5 {
		t.Fatalf("per-device G-counter did not merge: %#v", merged.State.Counts)
	}
	if !merged.State.Pinned.Value {
		t.Fatal("newer/tie-broken pinned HLC did not win")
	}

	if err := right.Delete(ctx, phrase.Text, phrase.Pinyin); err != nil {
		t.Fatal(err)
	}
	deleted, err := onlyPending(t, right).ProtocolPayload()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := left.RecordSelection(ctx, phrase, LearningContext{}); err != nil {
		t.Fatal(err)
	}
	ordinaryCount, err := onlyPending(t, left).ProtocolPayload()
	if err != nil {
		t.Fatal(err)
	}
	removed, err := MergePhrasePayload(deleted, ordinaryCount)
	if err != nil {
		t.Fatal(err)
	}
	if removed.State.Presence.Present || removed.State.Presence.Generation != 1 {
		t.Fatalf("ordinary count resurrected remove-wins tombstone: %#v", removed.State.Presence)
	}
	if err := left.MergeRemotePayload(ctx, removed); err != nil {
		t.Fatal(err)
	}
	result, err := left.RecordSelection(ctx, phrase, LearningContext{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Recorded {
		t.Fatal("materialized remote tombstone accepted an ordinary selection")
	}

	phrase.Pinned = true
	if err := right.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	readded, err := onlyPending(t, right).ProtocolPayload()
	if err != nil {
		t.Fatal(err)
	}
	if !readded.State.Presence.Present || readded.State.Presence.Generation != 2 {
		t.Fatalf("explicit re-add did not advance generation: %#v", readded.State.Presence)
	}
	converged, err := MergePhrasePayload(removed, readded)
	if err != nil {
		t.Fatal(err)
	}
	if !converged.State.Presence.Present || converged.State.Presence.Generation != 2 {
		t.Fatalf("new generation did not revive phrase: %#v", converged.State.Presence)
	}
	if err := left.MergeRemotePayload(ctx, converged); err != nil {
		t.Fatal(err)
	}
	snapshot, err := left.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Phrases) != 1 || snapshot.Phrases[0].Deleted || snapshot.Phrases[0].UseCount != 6 {
		t.Fatalf("materialized convergence mismatch: %#v", snapshot)
	}
}

func TestPendingPhraseSealsSignedProtocolEnvelope(t *testing.T) {
	ctx := context.Background()
	store, _ := openDeviceStore(t, deviceA)
	phrase := Phrase{Text: "合成加密信封测试", Pinyin: "he cheng jia mi xin feng ce shi", Source: "synthetic", UseCount: 7, Pinned: true}
	if err := store.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	event := onlyPending(t, store)
	deviceID, err := hex.DecodeString(deviceA)
	if err != nil {
		t.Fatal(err)
	}
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x66}, ed25519.SeedSize))
	epochKey := bytes.Repeat([]byte{0x77}, 32)
	envelope, err := event.SealEnvelope(EnvelopeOptions{
		AccountID: bytes.Repeat([]byte{0x11}, 16), DeviceID: deviceID,
		KeyEpoch: 1, DeviceSeq: 1, EpochKey: epochKey, SigningPrivate: private,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x88}, 2048)),
	})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := envelope.ToWire()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := protocol.EnvelopeFromWire(envelope.Header.AccountID, envelope.Header.DeviceID, wire)
	if err != nil {
		t.Fatal(err)
	}
	var payload PhrasePayload
	if err := protocol.Open(epochKey, restored, private.Public().(ed25519.PublicKey), &payload); err != nil {
		t.Fatal(err)
	}
	want, err := event.ProtocolPayload()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("opened envelope payload mismatch: got=%#v want=%#v", payload, want)
	}
	if hash, err := protocol.EnvelopeHash(restored); err != nil || len(hash) != 32 {
		t.Fatalf("invalid signed envelope hash: len=%d err=%v", len(hash), err)
	}

	localOnly, _ := openTestStore(t)
	if err := localOnly.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	if _, err := onlyPending(t, localOnly).SealEnvelope(EnvelopeOptions{DeviceID: deviceID}); err == nil {
		t.Fatal("local-only store produced a sync envelope")
	}
}
