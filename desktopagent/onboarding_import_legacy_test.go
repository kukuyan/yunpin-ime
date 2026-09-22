// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
)

const onboardingSecondRow = "二号词条\ter hao ci tiao\tlegacy\t16\tfalse\n"

func newOnboardingLegacyAgent(t *testing.T, initialSync bool) (Agent, Paths) {
	t.Helper()
	paths := onboardingTestPaths(t)
	paths.DatabasePath = filepath.Join(paths.StateDirectory, "private.db")
	paths.SnapshotStatePath = filepath.Join(paths.StateDirectory, "snapshot-generation")
	bundle := testCredentials()
	defer bundle.Zero()
	if err := ensureEncryptedStore(context.Background(), paths.DatabasePath, bundle, nil); err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newMemorySecretStore()
	if err := secrets.Save(context.Background(), DefaultProfile, encoded); err != nil {
		t.Fatal(err)
	}
	zeroBytes(encoded)
	if _, err := ImportBaseline(paths, onboardingTSV(onboardingBaseRow), BaselineImportOptions{}); err != nil {
		t.Fatal(err)
	}
	agent := Agent{Secrets: secrets, Profile: DefaultProfile, DatabasePath: paths.DatabasePath, BaselinePath: paths.BaselinePath,
		SnapshotPath: paths.SnapshotPath, SnapshotStatePath: paths.SnapshotStatePath,
		Reload: func(context.Context) error { t.Fatal("legacy import invoked native reload"); return nil },
	}
	if initialSync {
		withOnboardingStore(t, agent, func(store *localstore.Store) error {
			return store.RecordSyncHealth(context.Background(), localstore.SyncHealth{LastSuccessAt: 1, LastEventAt: 1, LastEventCode: "sync_complete", LastFailureClass: localstore.SyncFailureNone, Cursor: 0})
		})
	}
	return agent, paths
}

func withOnboardingStore(t *testing.T, agent Agent, fn func(*localstore.Store) error) {
	t.Helper()
	if err := agent.withPrivateStore(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

type onboardingStoreState struct {
	Snapshot localstore.Snapshot
	Pending  []localstore.PendingEvent
	Sync     localstore.SyncState
}

func readOnboardingStore(t *testing.T, agent Agent) (result onboardingStoreState) {
	t.Helper()
	withOnboardingStore(t, agent, func(store *localstore.Store) (err error) {
		if result.Snapshot, err = store.Snapshot(context.Background()); err != nil {
			return err
		}
		if result.Pending, err = store.PendingEvents(context.Background(), 256); err != nil {
			return err
		}
		result.Sync, err = store.LoadSyncState(context.Background())
		return err
	})
	return result
}

func TestOnboardingLegacyPreservesCountsPinsSourceAndReplayOutbox(t *testing.T) {
	agent, paths := newOnboardingLegacyAgent(t, true)
	withOnboardingStore(t, agent, func(store *localstore.Store) error {
		if err := store.SaveExplicit(context.Background(), localstore.Phrase{Text: "合成词条", Pinyin: "he cheng ci tiao", Source: "existing_source", UseCount: 3, LastUsedDay: 21000}); err != nil {
			return err
		}
		return store.SaveExplicit(context.Background(), localstore.Phrase{Text: "二号词条", Pinyin: "er hao ci tiao", Source: "existing_second", UseCount: 40, Pinned: true})
	})
	input := onboardingTSV(onboardingBaseRow + onboardingExtraRow + onboardingSecondRow)
	result, err := agent.ImportLegacyVocabulary(context.Background(), paths, input)
	if err != nil || result.Applied != 1 || result.Unchanged != 1 || result.StaticRowsSkipped != 1 || !result.Completed {
		t.Fatalf("import: %+v %v", result, err)
	}
	before := readOnboardingStore(t, agent)
	byText := map[string]localstore.Phrase{}
	for _, phrase := range before.Snapshot.Phrases {
		byText[phrase.Text] = phrase
	}
	first, second := byText["合成词条"], byText["二号词条"]
	if first.UseCount != 24 || !first.Pinned || first.Source != "existing_source" || first.LastUsedDay != 21000 || second.UseCount != 40 || !second.Pinned || second.Source != "existing_second" {
		t.Fatal("original metadata was downgraded or changed")
	}
	again, err := agent.ImportLegacyVocabulary(context.Background(), paths, input)
	if err != nil || again.Applied != 0 || again.Unchanged != 2 || !reflect.DeepEqual(before, readOnboardingStore(t, agent)) {
		t.Fatalf("repeat changed full store/CRDT/outbox: %+v %v", again, err)
	}
	status, err := InspectBaselineImport(paths)
	if err != nil || len(status.PendingLegacy) != 0 {
		t.Fatalf("completed restore still pending: %+v %v", status, err)
	}
}

func TestOnboardingLegacySavedBackupNewCountsAndSafeName(t *testing.T) {
	agent, paths := newOnboardingLegacyAgent(t, true)
	input := onboardingTSV(onboardingExtraRow + onboardingSecondRow)
	name, err := backupOnboardingBytes(paths, "legacy-private", input)
	if err != nil {
		t.Fatal(err)
	}
	status, err := InspectBaselineImport(paths)
	if err != nil || len(status.PendingLegacy) != 1 {
		t.Fatalf("saved restore not discoverable: %+v %v", status, err)
	}
	for _, name := range []string{"../private.tsv", paths.SnapshotPath, "legacy-private-../../etc/passwd.tsv"} {
		if _, err := agent.ImportSavedLegacyVocabulary(context.Background(), paths, name); !errors.Is(err, ErrLegacyBackupInvalid) {
			t.Fatalf("unsafe backup name accepted: %v", err)
		}
	}
	result, err := agent.ImportSavedLegacyVocabulary(context.Background(), paths, name)
	if err != nil || result.Applied != 2 || !result.SnapshotPending {
		t.Fatalf("saved import: %+v %v", result, err)
	}
	state := readOnboardingStore(t, agent)
	counts := map[string]uint64{}
	for _, phrase := range state.Snapshot.Phrases {
		counts[phrase.Text] = phrase.UseCount
	}
	if counts["合成词条"] != 24 || counts["二号词条"] != 16 || len(state.Pending) != 2 {
		t.Fatal("new counts or official outbox entries differ")
	}
}

func TestOnboardingLegacyInitialSyncGateAndDeletedSecondRowAreNoMutation(t *testing.T) {
	agent, paths := newOnboardingLegacyAgent(t, false)
	input := onboardingTSV(onboardingExtraRow + onboardingSecondRow)
	before := readOnboardingStore(t, agent)
	if _, err := agent.ImportLegacyVocabulary(context.Background(), paths, input); !errors.Is(err, ErrLegacyInitialSyncRequired) {
		t.Fatalf("first pull gate: %v", err)
	}
	if !reflect.DeepEqual(before, readOnboardingStore(t, agent)) {
		t.Fatal("unsynced import modified store")
	}
	withOnboardingStore(t, agent, func(store *localstore.Store) error {
		ctx := context.Background()
		if err := store.RecordSyncHealth(ctx, localstore.SyncHealth{LastSuccessAt: 1, LastEventAt: 1, LastEventCode: "sync_complete", LastFailureClass: localstore.SyncFailureNone}); err != nil {
			return err
		}
		if err := store.SaveExplicit(ctx, localstore.Phrase{Text: "二号词条", Pinyin: "er hao ci tiao", Source: "old", UseCount: 1}); err != nil {
			return err
		}
		return store.Delete(ctx, "二号词条", "er hao ci tiao")
	})
	before = readOnboardingStore(t, agent)
	if _, err := agent.ImportLegacyVocabulary(context.Background(), paths, input); !errors.Is(err, ErrLegacyDeletedConflict) {
		t.Fatalf("deleted second row gate: %v", err)
	}
	if !reflect.DeepEqual(before, readOnboardingStore(t, agent)) {
		t.Fatal("first row written before deleted second row was checked")
	}
}

func TestOnboardingLegacyPartialResumeDoesNotRewritePriorRow(t *testing.T) {
	agent, paths := newOnboardingLegacyAgent(t, true)
	withOnboardingStore(t, agent, func(store *localstore.Store) error {
		return store.SaveExplicit(context.Background(), localstore.Phrase{Text: "合成词条", Pinyin: "he cheng ci tiao", Source: "old_selection", UseCount: 24, Pinned: true})
	})
	before := readOnboardingStore(t, agent)
	result, err := agent.ImportLegacyVocabulary(context.Background(), paths, onboardingTSV(onboardingExtraRow+onboardingSecondRow))
	if err != nil || result.Applied != 1 || result.Unchanged != 1 {
		t.Fatalf("partial resume: %+v %v", result, err)
	}
	after := readOnboardingStore(t, agent)
	if len(after.Pending) != 2 || !reflect.DeepEqual(before.Pending[0], after.Pending[0]) {
		t.Fatal("resume rewrote already-imported CRDT/outbox entry")
	}
}

func TestOnboardingLegacyCompletedImportCannotUndoLaterUnpin(t *testing.T) {
	agent, paths := newOnboardingLegacyAgent(t, true)
	input := onboardingTSV(onboardingExtraRow)
	if _, err := agent.ImportLegacyVocabulary(context.Background(), paths, input); err != nil {
		t.Fatal(err)
	}
	withOnboardingStore(t, agent, func(store *localstore.Store) error {
		return store.SaveExplicit(context.Background(), localstore.Phrase{Text: "合成词条", Pinyin: "he cheng ci tiao", Source: "later_user_edit", UseCount: 24, Pinned: false})
	})
	before := readOnboardingStore(t, agent)
	if _, err := agent.ImportLegacyVocabulary(context.Background(), paths, input); !errors.Is(err, ErrLegacyImportChanged) {
		t.Fatalf("completed replay unpinned entry: %v", err)
	}
	if !reflect.DeepEqual(before, readOnboardingStore(t, agent)) {
		t.Fatal("completed replay modified later user edit")
	}
}

func TestOnboardingLegacyRefusesStaticPronunciationConflictAndMalformedTail(t *testing.T) {
	agent, paths := newOnboardingLegacyAgent(t, true)
	before := readOnboardingStore(t, agent)
	input := onboardingTSV(onboardingExtraRow + "静态词条\tjing tai ci\tlegacy\t16\tfalse\n")
	if _, err := agent.ImportLegacyVocabulary(context.Background(), paths, input); !errors.Is(err, ErrLegacyBaselineConflict) {
		t.Fatalf("static pronunciation conflict: %v", err)
	}
	input = onboardingTSV(onboardingExtraRow + "二号词条\ter hao ci tiao\tlegacy\tinvalid\tfalse\n")
	if _, err := agent.ImportLegacyVocabulary(context.Background(), paths, input); err == nil {
		t.Fatal("invalid final metadata accepted")
	}
	if !reflect.DeepEqual(before, readOnboardingStore(t, agent)) {
		t.Fatal("rejected import modified store")
	}
}

func TestOnboardingLegacyPreparedUploadAndBusyStateArePreserved(t *testing.T) {
	agent, paths := newOnboardingLegacyAgent(t, true)
	withOnboardingStore(t, agent, func(store *localstore.Store) error {
		ctx := context.Background()
		if err := store.SaveExplicit(ctx, localstore.Phrase{Text: "待传词条", Pinyin: "dai chuan ci tiao", Source: "fixture", UseCount: 2}); err != nil {
			return err
		}
		events, err := store.PendingEvents(ctx, 1)
		if err != nil {
			return err
		}
		return store.SavePreparedUpload(ctx, localstore.PreparedUpload{EventID: events[0].ID, EventVersion: events[0].Version, DeviceSequence: 1, Wire: []byte("synthetic prepared envelope"), EnvelopeHash: make([]byte, 32)})
	})
	before := readOnboardingStore(t, agent)
	input := onboardingTSV(onboardingExtraRow)
	if _, err := agent.ImportLegacyVocabulary(context.Background(), paths, input); !errors.Is(err, ErrLegacyInitialSyncRequired) {
		t.Fatalf("prepared outbox ignored: %v", err)
	}
	if err := WithProcessLock(paths.LockPath, func() error {
		_, err := agent.ImportLegacyVocabulary(context.Background(), paths, input)
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("legacy import ignored active lock: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, readOnboardingStore(t, agent)) {
		t.Fatal("prepared state or pending outbox changed")
	}
}
