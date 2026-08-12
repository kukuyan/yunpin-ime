// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
)

func openBridgeStore(t *testing.T) *localstore.Store {
	t.Helper()
	store, err := localstore.OpenForDevice(
		context.Background(), privateTestPath(t, "private.db"),
		bytes.Repeat([]byte{0x61}, 32), bytes.Repeat([]byte{0x62}, 32),
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestConsumeNativeEventsRecordsAndRemovesSpoolFile(t *testing.T) {
	store := openBridgeStore(t)
	directory := filepath.Join(t.TempDir(), "native-events", "incoming")
	makePrivateTestDirectory(t, directory)
	event := NativeSelectionEventV1{
		Version: 1, EventID: "process_nonce-1", Phrase: "合成桌面事件", Pinyin: "he cheng zhuo mian shi jian",
	}
	encoded, err := EncodeNativeSelectionEventV1(event)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, event.EventID+".json")
	writePrivateTestFile(t, path, encoded)
	summary, err := consumeNativeEvents(context.Background(), directory, store, nil, maxNativeBatch)
	if err != nil || summary.Consumed != 1 || summary.Duplicate != 0 {
		t.Fatalf("consume mismatch: summary=%#v err=%v", summary, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("consumed spool file remains: %v", err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || len(snapshot.Phrases) != 1 || snapshot.Phrases[0].UseCount != 1 {
		t.Fatalf("native event did not reach encrypted store: snapshot=%#v err=%v", snapshot, err)
	}
}

func TestConsumeNativeEventsRemovesCrashRetryWithoutDoubleCount(t *testing.T) {
	store := openBridgeStore(t)
	directory := filepath.Join(t.TempDir(), "incoming")
	makePrivateTestDirectory(t, directory)
	event := NativeSelectionEventV1{Version: 1, EventID: "process_nonce-2", Phrase: "合成重试", Pinyin: "he cheng chong shi"}
	if _, err := store.RecordNativeSelection(context.Background(), localstore.NativeSelection{
		EventID: event.EventID, Phrase: localstore.Phrase{Text: event.Phrase, Pinyin: event.Pinyin},
	}); err != nil {
		t.Fatal(err)
	}
	encoded, _ := EncodeNativeSelectionEventV1(event)
	writePrivateTestFile(t, filepath.Join(directory, event.EventID+".json"), encoded)
	summary, err := consumeNativeEvents(context.Background(), directory, store, nil, maxNativeBatch)
	if err != nil || summary.Duplicate != 1 || summary.Consumed != 0 {
		t.Fatalf("crash retry mismatch: summary=%#v err=%v", summary, err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	if len(snapshot.Phrases) != 1 || snapshot.Phrases[0].UseCount != 1 {
		t.Fatalf("crash retry double-counted: %#v", snapshot)
	}
}

func TestNativeEventDecoderRejectsProtectedContextFields(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "incoming")
	makePrivateTestDirectory(t, directory)
	path := filepath.Join(directory, "synthetic-1.json")
	writePrivateTestFile(t, path, []byte(`{"version":1,"event_id":"synthetic-1","phrase":"合成","pinyin":"he cheng","password":false}`))
	if _, err := consumeNativeEvents(context.Background(), directory, openBridgeStore(t), nil, maxNativeBatch); err == nil {
		t.Fatal("native event containing a protected-context field was accepted")
	}
}

func TestConsumeBaselineIdentityCreatesReceiptButNeverOutbox(t *testing.T) {
	store := openBridgeStore(t)
	directory := filepath.Join(t.TempDir(), "incoming")
	makePrivateTestDirectory(t, directory)
	event := NativeSelectionEventV1{Version: 1, EventID: "baseline-1", Phrase: "个人静态词", Pinyin: "ge ren jing tai ci"}
	encoded, err := EncodeNativeSelectionEventV1(event)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateTestFile(t, filepath.Join(directory, event.EventID+".json"), encoded)
	localOnly := map[string]struct{}{protocol.CanonicalPhrase(event.Phrase): {}}
	summary, err := consumeNativeEvents(context.Background(), directory, store, localOnly, maxNativeBatch)
	if err != nil || summary.Consumed != 1 || summary.LocalOnly != 1 {
		t.Fatalf("local-only consume mismatch: summary=%#v err=%v", summary, err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || len(snapshot.Phrases) != 0 {
		t.Fatalf("baseline identity entered learned store: snapshot=%#v err=%v", snapshot, err)
	}
	pending, err := store.PendingEventCount(context.Background())
	if err != nil || pending != 0 {
		t.Fatalf("baseline identity entered sync outbox: pending=%d err=%v", pending, err)
	}
}

func TestConsumeBaselinePhraseBlocksDifferentPronunciationToo(t *testing.T) {
	store := openBridgeStore(t)
	directory := filepath.Join(t.TempDir(), "incoming")
	makePrivateTestDirectory(t, directory)
	event := NativeSelectionEventV1{Version: 1, EventID: "baseline-heteronym", Phrase: "个人静态词", Pinyin: "wan quan bu tong du yin"}
	encoded, err := EncodeNativeSelectionEventV1(event)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateTestFile(t, filepath.Join(directory, event.EventID+".json"), encoded)
	summary, err := consumeNativeEvents(context.Background(), directory, store, map[string]struct{}{protocol.CanonicalPhrase(event.Phrase): {}}, maxNativeBatch)
	if err != nil || summary.LocalOnly != 1 {
		t.Fatalf("heteronym baseline event crossed phrase-only deny set: summary=%#v err=%v", summary, err)
	}
	pending, err := store.PendingEventCount(context.Background())
	if err != nil || pending != 0 {
		t.Fatalf("heteronym baseline event entered outbox: pending=%d err=%v", pending, err)
	}
}

func TestConsumeBaselinePhraseBlocksCanonicalUnicodeAndWhitespaceVariant(t *testing.T) {
	store := openBridgeStore(t)
	directory := filepath.Join(t.TempDir(), "incoming")
	makePrivateTestDirectory(t, directory)
	event := NativeSelectionEventV1{Version: 1, EventID: "baseline-canonical", Phrase: "Ａ B", Pinyin: "wan quan bu tong"}
	encoded, err := EncodeNativeSelectionEventV1(event)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateTestFile(t, filepath.Join(directory, event.EventID+".json"), encoded)
	localOnly := map[string]struct{}{protocol.CanonicalPhrase("AB"): {}}
	summary, err := consumeNativeEvents(context.Background(), directory, store, localOnly, maxNativeBatch)
	if err != nil || summary.LocalOnly != 1 {
		t.Fatalf("canonical baseline variant crossed deny set: summary=%#v err=%v", summary, err)
	}
	pending, err := store.PendingEventCount(context.Background())
	if err != nil || pending != 0 {
		t.Fatalf("canonical baseline variant entered outbox: pending=%d err=%v", pending, err)
	}
}

func TestConsumeRejectsNonPrivateOrOversizedSpool(t *testing.T) {
	store := openBridgeStore(t)
	private := filepath.Join(t.TempDir(), "private")
	makePrivateTestDirectory(t, private)
	for index := 0; index <= maxNativeSpoolFiles; index++ {
		writePrivateTestFile(t, filepath.Join(private, fmt.Sprintf("ignored-%04d.tmp", index)), nil)
	}
	if _, err := consumeNativeEvents(context.Background(), private, store, nil, maxNativeBatch); err == nil {
		t.Fatal("file-count oversized spool was accepted")
	}
	if os.Geteuid() == os.Getuid() {
		world := filepath.Join(t.TempDir(), "world")
		if err := os.MkdirAll(world, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := consumeNativeEvents(context.Background(), world, store, nil, maxNativeBatch); err == nil {
			t.Fatal("non-private spool directory was accepted")
		}
	}
}

func TestConsumeExcludesOnlyStrictSpoolLockFromQuota(t *testing.T) {
	store := openBridgeStore(t)
	directory := filepath.Join(t.TempDir(), "incoming")
	makePrivateTestDirectory(t, directory)
	writePrivateTestFile(t, filepath.Join(directory, ".spool.lock"), []byte("not empty"))
	if _, err := consumeNativeEvents(context.Background(), directory, store, nil, maxNativeBatch); err == nil {
		t.Fatal("non-empty spool lock was accepted")
	}
	writePrivateTestFile(t, filepath.Join(directory, ".spool.lock"), nil)
	if _, err := consumeNativeEvents(context.Background(), directory, store, nil, maxNativeBatch); err != nil {
		t.Fatalf("strict empty spool lock counted as payload: %v", err)
	}
}

func TestConsumeDrainsOneLegacyOverflowEvent(t *testing.T) {
	store := openBridgeStore(t)
	directory := filepath.Join(t.TempDir(), "incoming")
	makePrivateTestDirectory(t, directory)
	writePrivateTestFile(t, filepath.Join(directory, ".spool.lock"), nil)
	for index := 0; index <= maxNativeSpoolFiles; index++ {
		eventID := fmt.Sprintf("legacy-%04d", index)
		encoded, err := EncodeNativeSelectionEventV1(NativeSelectionEventV1{
			Version: NativeEventVersion, EventID: eventID, Phrase: "测试", Pinyin: "ce shi",
		})
		if err != nil {
			t.Fatal(err)
		}
		writePrivateTestFile(t, filepath.Join(directory, eventID+".json"), encoded)
	}
	summary, err := consumeNativeEvents(context.Background(), directory, store, nil, 1)
	if err != nil || summary.Consumed != 1 {
		t.Fatalf("legacy overflow did not make bounded progress: summary=%#v err=%v", summary, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxNativeSpoolFiles+1 {
		t.Fatalf("bounded recovery removed an unexpected number of entries: %d", len(entries))
	}
}
