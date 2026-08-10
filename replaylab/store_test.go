// SPDX-License-Identifier: Apache-2.0
package replaylab

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLifecycleAndExplicitClear(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lab")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store, err := Init(root, now)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	status, err := store.Status()
	if err != nil || status.State != "disabled" {
		t.Fatalf("initial status = %+v, %v", status, err)
	}
	status, err = store.Start(now)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if status.State != "running" || status.LastSeq != 1 {
		t.Fatalf("started status = %+v", status)
	}
	snapshot := EventV1{
		Version:     EventVersionV1,
		SessionID:   status.SessionID,
		EpisodeID:   status.LastEpisodeID,
		Seq:         2,
		MonotonicUS: 10,
		UTC:         "2026-01-01T00:00:01Z",
		Type:        EventComposition,
		Composition: syntheticComposition(),
	}
	if err := store.Append(snapshot); err != nil {
		t.Fatalf("append: %v", err)
	}
	status, err = store.Pause(now.Add(2 * time.Second))
	if err != nil || status.State != "paused" || status.LastSeq != 3 {
		t.Fatalf("pause status = %+v, %v", status, err)
	}
	status, err = store.Resume(now.Add(3 * time.Second))
	if err != nil || status.State != "running" || status.LastSeq != 4 {
		t.Fatalf("resume status = %+v, %v", status, err)
	}
	events, err := store.Events()
	if err != nil || len(events) != 4 {
		t.Fatalf("events = %d, %v", len(events), err)
	}
	if err := Clear(root, false); err == nil {
		t.Fatal("clear without confirmation succeeded")
	}
	if err := Clear(root, true); err != nil {
		t.Fatalf("confirmed clear: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("root still exists after clear: %v", err)
	}
}

func TestClearRejectsUnmarkedAndBroadRoots(t *testing.T) {
	unmarked := filepath.Join(t.TempDir(), "not-a-lab")
	if err := os.MkdirAll(unmarked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Clear(unmarked, true); err == nil {
		t.Fatal("unmarked directory was cleared")
	}
	if err := Clear(string(os.PathSeparator), true); err == nil {
		t.Fatal("filesystem root was accepted")
	}
	home, err := os.UserHomeDir()
	if err == nil {
		if err := Clear(home, true); err == nil {
			t.Fatal("home directory was accepted")
		}
	}
}

func TestInitRejectsGitWorktreeRoot(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Init(filepath.Join(workingDirectory, "private-lab"), time.Now()); err == nil {
		t.Fatal("lab root inside Git worktree was accepted")
	}
}

func TestStatusRepairsOnlyFsyncedEventAheadOfMetadata(t *testing.T) {
	root := filepath.Join(t.TempDir(), "lab")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store, err := Init(root, now)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Start(now)
	if err != nil {
		t.Fatal(err)
	}
	event := EventV1{
		Version:     EventVersionV1,
		SessionID:   metadata.SessionID,
		EpisodeID:   metadata.LastEpisodeID,
		Seq:         2,
		MonotonicUS: 10,
		UTC:         "2026-01-01T00:00:01Z",
		Type:        EventComposition,
		Composition: syntheticComposition(),
	}
	eventPath, err := store.sessionPath(metadata.EventFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendEventFile(eventPath, event); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := reopened.Status()
	if err != nil {
		t.Fatalf("repair fsynced append: %v", err)
	}
	if repaired.LastSeq != 2 || repaired.LastMonotonicUS != 10 {
		t.Fatalf("metadata was not repaired: %+v", repaired)
	}

	repaired.LastSeq = 3
	if err := reopened.saveMetadata(repaired); err != nil {
		t.Fatal(err)
	}
	reopened, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Status(); err == nil {
		t.Fatal("metadata ahead of append-only log was silently accepted")
	}
}
