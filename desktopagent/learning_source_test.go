// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
)

func TestCumulativeRimeCountsAndNativeEvidenceRemainIndependentAcrossRounds(t *testing.T) {
	ctx := context.Background()
	store := openBridgeStore(t)
	root := privateTestPath(t, "source-fixture")
	makePrivateTestDirectory(t, root)
	spool := filepath.Join(root, "incoming")
	makePrivateTestDirectory(t, spool)
	export := filepath.Join(root, "rime.userdb.txt")
	var last []byte
	var lastPath string
	for round := 1; round <= 3; round++ {
		writePrivateTestFile(t, export, []byte(fmt.Sprintf("ban gong shi \t办公室\tc=%d d=1 t=%d\n", round, round)))
		if _, err := ingestRimeUserDBExport(ctx, export, store, nil); err != nil {
			t.Fatal(err)
		}
		event := NativeLearningEventV2{Version: NativeEventVersion, EventID: fmt.Sprintf("selection-%d", round),
			Kind: nativeEventSelection, DateBucket: "2026-09-07", Phrase: "办公室", Pinyin: "ban gong shi"}
		encoded, err := EncodeNativeLearningEventV2(event)
		if err != nil {
			t.Fatal(err)
		}
		last, lastPath = encoded, filepath.Join(spool, event.EventID+".json")
		writePrivateTestFile(t, lastPath, encoded)
		result, err := consumeNativeEventsWithSelectionCounts(ctx, spool, store, nil, maxNativeBatch, false)
		if err != nil || result.Consumed != 1 {
			t.Fatalf("round %d: result=%+v err=%v", round, result, err)
		}
		state, err := store.Snapshot(ctx)
		if err != nil || len(state.Phrases) != 1 || state.Phrases[0].UseCount != uint64(round) {
			t.Fatalf("round %d double-counted: snapshot=%+v err=%v", round, state, err)
		}
	}
	// A crash after receipt commit but before spool deletion must be idempotent.
	writePrivateTestFile(t, lastPath, last)
	result, err := consumeNativeEventsWithSelectionCounts(ctx, spool, store, nil, maxNativeBatch, false)
	if err != nil || result.Duplicate != 1 {
		t.Fatalf("retry=%+v err=%v", result, err)
	}
	correction := NativeLearningEventV2{Version: NativeEventVersion, EventID: "correction-independent", Kind: nativeEventCorrection,
		DateBucket: "2026-09-07", Phrase: "办公室", Pinyin: "ban gong shi", CorrectedFromPhrase: "办公是", CorrectedFromPinyin: "ban gong shi"}
	encoded, err := EncodeNativeLearningEventV2(correction)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateTestFile(t, filepath.Join(spool, correction.EventID+".json"), encoded)
	result, err = consumeNativeEventsWithSelectionCounts(ctx, spool, store, nil, maxNativeBatch, false)
	if err != nil || result.Corrections != 1 {
		t.Fatalf("correction=%+v err=%v", result, err)
	}
	stats, err := store.QueryHabits(ctx, localstore.HabitQuery{Limit: 10})
	if err != nil || len(stats) != 2 {
		t.Fatalf("habits=%+v err=%v", stats, err)
	}
	var selections, replacements, correctedFrom uint64
	for _, stat := range stats {
		selections += stat.SelectionCount
		replacements += stat.ReplacementCount
		correctedFrom += stat.CorrectedFromCount
	}
	if selections != 3 || replacements != 1 || correctedFrom != 1 {
		t.Fatalf("habit counts=%d/%d/%d", selections, replacements, correctedFrom)
	}
	state, err := store.Snapshot(ctx)
	if err != nil || len(state.Phrases) != 1 || state.Phrases[0].UseCount != 3 {
		t.Fatal("local evidence altered the cumulative phrase count")
	}
}
