// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func independentStores(t *testing.T, count int) []*Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.db")
	stores := make([]*Store, count)
	for index := range stores {
		store, err := OpenForDevice(context.Background(), path,
			bytes.Repeat([]byte{0x21}, 32), bytes.Repeat([]byte{0x43}, 32), deviceA)
		if err != nil {
			t.Fatal(err)
		}
		stores[index] = store
		t.Cleanup(func() { _ = store.Close() })
	}
	return stores
}

func assertCountAndOutbox(t *testing.T, store *Store, count uint64) Phrase {
	t.Helper()
	snapshot, err := store.Snapshot(context.Background())
	if err != nil || len(snapshot.Phrases) != 1 {
		t.Fatalf("snapshot: phrases=%d err=%v", len(snapshot.Phrases), err)
	}
	phrase := snapshot.Phrases[0]
	if phrase.UseCount != count {
		t.Fatalf("successful selections=%d stored_count=%d", count, phrase.UseCount)
	}
	pending := onlyPending(t, store)
	if !reflect.DeepEqual(pending.Phrase, phrase) {
		t.Fatal("outbox and committed phrase diverged")
	}
	return phrase
}

func TestIndependentStoresSamePhraseSelections(t *testing.T) {
	stores := independentStores(t, 8)
	ctx := context.Background()
	phrase := Phrase{Text: "合成跨连接同词计数", Pinyin: "he cheng kua lian jie tong ci ji shu"}
	start := make(chan struct{})
	var group sync.WaitGroup
	for _, store := range stores {
		group.Add(1)
		go func(store *Store) {
			defer group.Done()
			<-start
			for index := 0; index < 100; index++ {
				if _, err := store.RecordSelection(ctx, phrase, LearningContext{}); err != nil {
					t.Errorf("selection: %v", err)
					return
				}
			}
		}(store)
	}
	close(start)
	group.Wait()
	assertCountAndOutbox(t, stores[0], 800)
}

func TestIndependentProcessesSamePhraseSelections(t *testing.T) {
	store, path := openDeviceStore(t, deviceA)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const processCount = 4
	processes := make([]*exec.Cmd, 0, processCount)
	starts := make([]io.WriteCloser, 0, processCount)
	outputs := make([]*bufio.Reader, 0, processCount)
	errors := make([]*bytes.Buffer, 0, processCount)
	for index := 0; index < processCount; index++ {
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStoreSelectionProcessHelper$")
		command.Env = append(os.Environ(), "YUNPIN_TEST_CROSS_PROCESS_DB="+path)
		input, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stderr := new(bytes.Buffer)
		command.Stderr = stderr
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		processes = append(processes, command)
		starts = append(starts, input)
		outputs = append(outputs, bufio.NewReader(output))
		errors = append(errors, stderr)
	}
	// All children open distinct SQLite connections before this barrier. There
	// is no shared Go mutex or external process lock in the actual mutation path.
	for _, output := range outputs {
		line, err := output.ReadString('\n')
		if err != nil || line != "ready\n" {
			t.Fatalf("child readiness: %q err=%v", line, err)
		}
	}
	for _, input := range starts {
		if _, err := io.WriteString(input, "x"); err != nil {
			t.Fatal(err)
		}
		_ = input.Close()
	}
	for index, process := range processes {
		remaining, _ := io.ReadAll(outputs[index])
		if err := process.Wait(); err != nil {
			t.Errorf("child %d: %v stdout=%s stderr=%s", index, err, remaining, errors[index])
		}
	}
	assertCountAndOutbox(t, store, processCount*100)
}

func TestStoreSelectionProcessHelper(t *testing.T) {
	path := os.Getenv("YUNPIN_TEST_CROSS_PROCESS_DB")
	if path == "" {
		t.Skip("child helper uses only the parent's synthetic database")
	}
	store, err := OpenForDevice(context.Background(), path,
		bytes.Repeat([]byte{0x21}, 32), bytes.Repeat([]byte{0x43}, 32), deviceA)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fmt.Println("ready")
	if _, err := io.ReadFull(os.Stdin, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	phrase := Phrase{Text: "合成跨进程同词计数", Pinyin: "he cheng kua jin cheng tong ci ji shu"}
	for index := 0; index < 100; index++ {
		if _, err := store.RecordSelection(context.Background(), phrase, LearningContext{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIndependentStoresNativeReceiptsAreIdempotent(t *testing.T) {
	for _, kind := range []string{"selection", "local_selection", "correction", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			stores := independentStores(t, 8)
			selection := NativeSelection{EventID: "synthetic_duplicate", DateBucket: "2026-09-07",
				Phrase: Phrase{Text: "合成时刻", Pinyin: "he cheng shi ke"}}
			start := make(chan struct{})
			results := make(chan NativeSelectionResult, len(stores))
			var group sync.WaitGroup
			for _, store := range stores {
				group.Add(1)
				go func(store *Store) {
					defer group.Done()
					<-start
					var result NativeSelectionResult
					var err error
					switch kind {
					case "selection":
						result, err = store.RecordNativeSelection(context.Background(), selection)
					case "local_selection":
						result, err = store.RecordNativeLocalSelection(context.Background(), selection)
					case "correction":
						result, err = store.RecordNativeCorrection(context.Background(), NativeCorrection{
							EventID: selection.EventID, DateBucket: selection.DateBucket,
							Replacement: selection.Phrase, CorrectedFrom: Phrase{Text: "合成食客", Pinyin: selection.Phrase.Pinyin}})
					case "receipt":
						result, err = store.RecordNativeSelectionReceipt(context.Background(), selection.EventID)
					}
					if err != nil {
						t.Errorf("native event: %v", err)
					}
					results <- result
				}(store)
			}
			close(start)
			group.Wait()
			close(results)
			duplicates := 0
			for result := range results {
				if result.Duplicate {
					duplicates++
				}
			}
			if duplicates != len(stores)-1 {
				t.Fatalf("duplicates=%d want=%d", duplicates, len(stores)-1)
			}
			if kind == "selection" {
				assertCountAndOutbox(t, stores[0], 1)
			}
		})
	}
}

func TestIndependentStoresSelectionDeleteReadd(t *testing.T) {
	stores := independentStores(t, 2)
	ctx := context.Background()
	phrase := Phrase{Text: "合成删除重加交错", Pinyin: "he cheng shan chu chong jia jiao cuo"}
	if err := stores[0].SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	selected := make(chan uint64, 1)
	done := make(chan struct{})
	go func() {
		<-start
		var recorded uint64
		for index := 0; index < 100; index++ {
			result, err := stores[0].RecordSelection(ctx, phrase, LearningContext{})
			if err != nil {
				t.Errorf("selection: %v", err)
				break
			}
			if result.Recorded {
				recorded++
			}
		}
		selected <- recorded
	}()
	go func() {
		defer close(done)
		<-start
		for index := 0; index < 32; index++ {
			if err := stores[1].Delete(ctx, phrase.Text, phrase.Pinyin); err != nil {
				t.Errorf("delete: %v", err)
				return
			}
			if err := stores[1].SaveExplicit(ctx, phrase); err != nil {
				t.Errorf("readd: %v", err)
				return
			}
		}
	}()
	close(start)
	count := <-selected
	<-done
	actual := assertCountAndOutbox(t, stores[0], count)
	if actual.Deleted || actual.CRDT.Presence.Generation != 33 {
		t.Fatalf("explicit readds lost: deleted=%v generation=%d", actual.Deleted, actual.CRDT.Presence.Generation)
	}
}

func TestIndependentStoresSelectionAndRemoteMerge(t *testing.T) {
	stores := independentStores(t, 2)
	remote, _ := openDeviceStore(t, deviceB)
	ctx := context.Background()
	phrase := Phrase{Text: "合成远端合并交错", Pinyin: "he cheng yuan duan he bing jiao cuo", UseCount: 100}
	if err := remote.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	payload, err := onlyPending(t, remote).ProtocolPayload()
	if err != nil {
		t.Fatal(err)
	}
	phrase.UseCount = 0
	start := make(chan struct{})
	var group sync.WaitGroup
	for side, store := range stores {
		group.Add(1)
		go func(side int, store *Store) {
			defer group.Done()
			<-start
			for index := 0; index < 100; index++ {
				var err error
				if side == 0 {
					_, err = store.RecordSelection(ctx, phrase, LearningContext{})
				} else {
					err = store.MergeRemotePayload(ctx, payload)
				}
				if err != nil {
					t.Errorf("concurrent mutation: %v", err)
					return
				}
			}
		}(side, store)
	}
	close(start)
	group.Wait()
	// Remote merges never echo an outbox write. One subsequent local selection
	// must publish the entire converged state without losing either component.
	if _, err := stores[0].RecordSelection(ctx, phrase, LearningContext{}); err != nil {
		t.Fatal(err)
	}
	actual := assertCountAndOutbox(t, stores[0], 201)
	if actual.CRDT.Counts[deviceA] != 101 || actual.CRDT.Counts[deviceB] != 100 {
		t.Fatal("a CRDT counter component was lost")
	}
}

func TestNativeMutationFailureRollsBackClockPhraseOutboxAndReceipt(t *testing.T) {
	for _, nonceBytes := range []int{0, 24, 48} {
		t.Run(fmt.Sprint(nonceBytes), func(t *testing.T) {
			store, _ := openDeviceStore(t, deviceA)
			ctx := context.Background()
			store.random = bytes.NewReader(make([]byte, nonceBytes))
			selection := NativeSelection{EventID: "synthetic_atomic_failure",
				Phrase: Phrase{Text: "合成事务回滚", Pinyin: "he cheng shi wu hui gun"}}
			if _, err := store.RecordNativeSelection(ctx, selection); err == nil {
				t.Fatal("nonce failure unexpectedly succeeded")
			}
			for _, table := range []string{"encrypted_phrases", "encrypted_outbox", "consumed_native_events", "encrypted_learning_events"} {
				var count int
				if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
			var changed int
			if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metadata WHERE value != 0").Scan(&changed); err != nil || changed != 0 {
				t.Fatalf("failed transaction advanced metadata: changed=%d err=%v", changed, err)
			}
		})
	}
}

func TestFailedMutationsPreservePreviousStateAndClock(t *testing.T) {
	for _, operation := range []string{"selection", "explicit", "delete", "remote_merge"} {
		t.Run(operation, func(t *testing.T) {
			store, _ := openDeviceStore(t, deviceA)
			ctx := context.Background()
			phrase := Phrase{Text: "合成旧状态保留", Pinyin: "he cheng jiu zhuang tai bao liu", UseCount: 3}
			if err := store.SaveExplicit(ctx, phrase); err != nil {
				t.Fatal(err)
			}
			before, err := store.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			outboxBefore := onlyPending(t, store)
			readClock := func() [2]int64 {
				var clock [2]int64
				for index, key := range []string{"hlc_wall_ms", "hlc_counter"} {
					if err := store.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = ?", key).Scan(&clock[index]); err != nil {
						t.Fatal(err)
					}
				}
				return clock
			}
			clockBefore := readClock()
			store.random = bytes.NewReader(nil)
			switch operation {
			case "selection":
				_, err = store.RecordSelection(ctx, phrase, LearningContext{})
			case "explicit":
				phrase.Pinned = true
				err = store.SaveExplicit(ctx, phrase)
			case "delete":
				err = store.Delete(ctx, phrase.Text, phrase.Pinyin)
			case "remote_merge":
				remote, _ := openDeviceStore(t, deviceB)
				remote.now = func() time.Time { return time.UnixMilli(clockBefore[0] + 10000) }
				if err := remote.SaveExplicit(ctx, phrase); err != nil {
					t.Fatal(err)
				}
				payload, payloadErr := onlyPending(t, remote).ProtocolPayload()
				if payloadErr != nil {
					t.Fatal(payloadErr)
				}
				err = store.MergeRemotePayload(ctx, payload)
			}
			if err == nil {
				t.Fatal("expected seal failure")
			}
			after, err := store.Snapshot(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failure changed snapshot: err=%v", err)
			}
			if !reflect.DeepEqual(outboxBefore, onlyPending(t, store)) || readClock() != clockBefore {
				t.Fatal("failure changed outbox or HLC")
			}
		})
	}
}
