// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"context"
	"testing"
)

func TestRimeUserDBImportUsesCumulativeHighWaterAndResetWithoutNegativeDelta(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	ctx := context.Background()
	learning := RimeUserDBObservation{
		Phrase: Phrase{Text: "累计学习词", Pinyin: "lei ji xue xi ci"}, Commits: 5,
	}
	baseline := RimeUserDBObservation{
		Phrase: Phrase{Text: "静态个人词", Pinyin: "jing tai ge ren ci"}, Commits: 9, LocalOnly: true,
	}
	result, err := store.ImportRimeUserDB(ctx, []RimeUserDBObservation{learning, baseline}, nil)
	if err != nil || result.Rows != 2 || result.Advanced != 1 || result.LocalOnly != 1 || result.Resets != 0 {
		t.Fatalf("initial cumulative import=%#v err=%v", result, err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Phrases) != 1 || snapshot.Phrases[0].Text != learning.Phrase.Text || snapshot.Phrases[0].UseCount != 5 {
		t.Fatalf("initial cumulative commits were not materialized exactly once: snapshot=%#v err=%v", snapshot, err)
	}
	event := onlyPending(t, store)
	if event.Phrase.UseCount != 5 {
		t.Fatalf("initial cumulative commits were not atomically queued: %#v", event.Phrase)
	}
	if acked, err := store.AckPending(ctx, event.ID, event.Version); err != nil || !acked {
		t.Fatalf("ack initial import: acked=%v err=%v", acked, err)
	}

	result, err = store.ImportRimeUserDB(ctx, []RimeUserDBObservation{learning, baseline}, nil)
	if err != nil || result.Advanced != 0 || result.LocalOnly != 0 || result.Resets != 0 {
		t.Fatalf("identical export was not idempotent: result=%#v err=%v", result, err)
	}
	if pending, err := store.PendingEventCount(ctx); err != nil || pending != 0 {
		t.Fatalf("identical export recreated outbox traffic: pending=%d err=%v", pending, err)
	}

	learning.Commits = 2
	baseline.Commits = 1
	result, err = store.ImportRimeUserDB(ctx, []RimeUserDBObservation{learning, baseline}, nil)
	if err != nil || result.Resets != 2 || result.Advanced != 0 || result.LocalOnly != 0 {
		t.Fatalf("lower counters were not treated as resets: result=%#v err=%v", result, err)
	}
	snapshot, err = store.Snapshot(ctx)
	if err != nil || snapshot.Phrases[0].UseCount != 5 {
		t.Fatalf("counter reset subtracted learned state: snapshot=%#v err=%v", snapshot, err)
	}

	learning.Commits = 3
	baseline.Commits = 2
	result, err = store.ImportRimeUserDB(ctx, []RimeUserDBObservation{learning, baseline}, nil)
	if err != nil || result.Advanced != 1 || result.LocalOnly != 1 || result.Resets != 0 {
		t.Fatalf("post-reset positive deltas were not isolated: result=%#v err=%v", result, err)
	}
	snapshot, err = store.Snapshot(ctx)
	if err != nil || len(snapshot.Phrases) != 1 || snapshot.Phrases[0].UseCount != 6 {
		t.Fatalf("post-reset import added anything except one new commit: snapshot=%#v err=%v", snapshot, err)
	}
}

func TestRimeUserDBImportRollsBackHighWaterPhraseAndOutboxTogether(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	ctx := context.Background()
	first := RimeUserDBObservation{Phrase: Phrase{Text: "原子导入", Pinyin: "yuan zi dao ru"}, Commits: 3}
	duplicate := RimeUserDBObservation{Phrase: Phrase{Text: " 原子 导入 ", Pinyin: "yuan---zi---dao---ru"}, Commits: 8}
	if _, err := store.ImportRimeUserDB(ctx, []RimeUserDBObservation{first, duplicate}, nil); err == nil {
		t.Fatal("duplicate canonical identity did not abort the complete export")
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil || len(snapshot.Phrases) != 0 {
		t.Fatalf("failed export committed phrase state: snapshot=%#v err=%v", snapshot, err)
	}
	if pending, err := store.PendingEventCount(ctx); err != nil || pending != 0 {
		t.Fatalf("failed export committed outbox state: pending=%d err=%v", pending, err)
	}
	result, err := store.ImportRimeUserDB(ctx, []RimeUserDBObservation{first}, nil)
	if err != nil || result.Advanced != 1 {
		t.Fatalf("retry after rollback did not import full cumulative count: result=%#v err=%v", result, err)
	}
	snapshot, err = store.Snapshot(ctx)
	if err != nil || len(snapshot.Phrases) != 1 || snapshot.Phrases[0].UseCount != 3 {
		t.Fatalf("rolled-back high water suppressed retry: snapshot=%#v err=%v", snapshot, err)
	}
}

func TestRimeUserDBImportScrubsPhraseOnlyBaselineOutbox(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	ctx := context.Background()
	if err := store.SaveExplicit(ctx, Phrase{
		Text: "个人静态词", Pinyin: "wan quan bu tong du yin", UseCount: 4,
	}); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.PendingEventCount(ctx); err != nil || pending != 1 {
		t.Fatalf("test outbox setup failed: pending=%d err=%v", pending, err)
	}
	result, err := store.ImportRimeUserDB(ctx, nil, []string{"个人静态词"})
	if err != nil || result.Rows != 0 {
		t.Fatalf("phrase-only deny scrub failed: result=%#v err=%v", result, err)
	}
	if pending, err := store.PendingEventCount(ctx); err != nil || pending != 0 {
		t.Fatalf("different-pronunciation baseline phrase remained in outbox: pending=%d err=%v", pending, err)
	}
}

func TestRimeUserDBImportFailsClosedForBaselinePreparedUpload(t *testing.T) {
	store, _ := openDeviceStore(t, deviceA)
	ctx := context.Background()
	if err := store.SaveExplicit(ctx, Phrase{
		Text: "准备中的静态词", Pinyin: "zhun bei zhong de jing tai ci", UseCount: 3,
	}); err != nil {
		t.Fatal(err)
	}
	event := onlyPending(t, store)
	if err := store.SavePreparedUpload(ctx, PreparedUpload{
		EventID: event.ID, EventVersion: event.Version, DeviceSequence: 1,
		Wire: []byte{1}, EnvelopeHash: make([]byte, 32),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportRimeUserDB(ctx, []RimeUserDBObservation{{
		Phrase: Phrase{Text: "其他导入词", Pinyin: "qi ta dao ru ci"}, Commits: 8,
	}}, []string{"准备中的静态词"}); err == nil {
		t.Fatal("ambiguous prepared baseline upload was silently discarded or allowed")
	}
	if snapshot, err := store.Snapshot(ctx); err != nil || len(snapshot.Phrases) != 1 {
		t.Fatalf("failed-closed import partially committed phrase state: snapshot=%#v err=%v", snapshot, err)
	}
	if pending, err := store.PendingEventCount(ctx); err != nil || pending != 1 {
		t.Fatalf("failed-closed import changed prepared outbox: pending=%d err=%v", pending, err)
	}
}
