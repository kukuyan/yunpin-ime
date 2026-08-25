// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestEncryptedHabitEventsSurviveRestartAndRemainIdempotent(t *testing.T) {
	store, path := openDeviceStore(t, deviceA)
	ctx := context.Background()
	wrong := Phrase{Text: "办公是", Pinyin: "ban gong shi", Source: "native_selection"}
	replacement := Phrase{Text: "办公室", Pinyin: "ban gong shi", Source: "native_selection"}
	for _, selection := range []NativeSelection{
		{EventID: "habit-selection-wrong", DateBucket: "2026-08-25", Phrase: wrong},
		{EventID: "habit-selection-right", DateBucket: "2026-08-25", Phrase: replacement},
	} {
		result, err := store.RecordNativeSelection(ctx, selection)
		if err != nil || !result.Recorded || result.Duplicate {
			t.Fatalf("selection result=%#v err=%v", result, err)
		}
	}
	correction := NativeCorrection{
		EventID: "habit-correction", DateBucket: "2026-08-25",
		CorrectedFrom: wrong, Replacement: replacement,
	}
	result, err := store.RecordNativeCorrection(ctx, correction)
	if err != nil || result.Duplicate {
		t.Fatalf("correction result=%#v err=%v", result, err)
	}
	duplicate, err := store.RecordNativeCorrection(ctx, correction)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("correction replay result=%#v err=%v", duplicate, err)
	}

	assertHabits := func(current *Store) {
		t.Helper()
		stats, err := current.QueryHabits(ctx, HabitQuery{SinceDate: "2026-08-25", Limit: 10})
		if err != nil || len(stats) != 2 {
			t.Fatalf("habit report=%#v err=%v", stats, err)
		}
		byPhrase := make(map[string]HabitStat)
		for _, stat := range stats {
			byPhrase[stat.Phrase] = stat
		}
		if got := byPhrase["办公是"]; got.SelectionCount != 1 || got.CorrectedFromCount != 1 || got.ReplacementCount != 0 {
			t.Fatalf("wrong-word habit mismatch: %#v", got)
		}
		if got := byPhrase["办公室"]; got.SelectionCount != 1 || got.CorrectedFromCount != 0 || got.ReplacementCount != 1 {
			t.Fatalf("replacement habit mismatch: %#v", got)
		}
		scores := CorrectionScores(stats)
		if scores["办公是\x00ban gong shi"] != -1 || scores["办公室\x00ban gong shi"] != 1 {
			t.Fatalf("persisted correction scores=%#v", scores)
		}
	}
	assertHabits(store)
	if _, err := store.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	databaseBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(databaseBytes, []byte("办公是")) || bytes.Contains(databaseBytes, []byte("办公室")) {
		t.Fatal("habit plaintext leaked into the encrypted local database")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenForDevice(ctx, path, bytes.Repeat([]byte{0x21}, 32), bytes.Repeat([]byte{0x43}, 32), deviceA)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertHabits(reopened)
}

func TestLocalOnlySelectionProducesHabitWithoutPhraseOrOutbox(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	ctx := context.Background()
	selection := NativeSelection{
		EventID: "baseline-habit", DateBucket: "2026-08-25",
		Phrase: Phrase{Text: "静态基线词", Pinyin: "jing tai ji xian ci"},
	}
	first, err := store.RecordNativeLocalSelection(ctx, selection)
	if err != nil || first.Duplicate {
		t.Fatalf("local-only selection result=%#v err=%v", first, err)
	}
	second, err := store.RecordNativeLocalSelection(ctx, selection)
	if err != nil || !second.Duplicate {
		t.Fatalf("local-only replay result=%#v err=%v", second, err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Phrases) != 0 {
		t.Fatalf("local-only habit created a phrase: %#v err=%v", snapshot, err)
	}
	if pending, err := store.PendingEventCount(ctx); err != nil || pending != 0 {
		t.Fatalf("local-only habit created sync traffic: pending=%d err=%v", pending, err)
	}
	stats, err := store.QueryHabits(ctx, HabitQuery{Limit: 10})
	if err != nil || len(stats) != 1 || stats[0].SelectionCount != 1 || stats[0].Phrase != "静态基线词" {
		t.Fatalf("local-only habit report=%#v err=%v", stats, err)
	}
}

func TestHabitQueryRejectsInvalidBoundariesWithoutWriting(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	ctx := context.Background()
	if _, err := store.RecordNativeCorrection(ctx, NativeCorrection{
		EventID: "invalid-correction", DateBucket: "2026-08-25",
		CorrectedFrom: Phrase{Text: "相同", Pinyin: "xiang tong"},
		Replacement:   Phrase{Text: "相同", Pinyin: "xiang tong"},
	}); err == nil {
		t.Fatal("same-word correction was accepted")
	}
	if _, err := store.QueryHabits(ctx, HabitQuery{SinceDate: "2026-99-99", Limit: 10}); err == nil {
		t.Fatal("invalid habit date was accepted")
	}
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM encrypted_learning_events").Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid habit request wrote %d events, err=%v", count, err)
	}
}
