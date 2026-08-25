// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
)

// Every phrase in these tests is synthetic. No real vocabulary is read.
const (
	testPhraseText   = "合成词条"
	testPhrasePinyin = "he cheng ci tiao"
)

func newVocabularyAgent(t *testing.T) Agent {
	t.Helper()
	root := t.TempDir()
	bundle := testCredentials()
	defer bundle.Zero()
	database := filepath.Join(root, "sync", "private.db")
	if err := ensureEncryptedStore(context.Background(), database, bundle, nil); err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newMemorySecretStore()
	if err := secrets.Save(context.Background(), "default", encoded); err != nil {
		t.Fatal(err)
	}
	zeroBytes(encoded)

	// An empty baseline keeps the test's expectations about the edits alone.
	//
	// It is written through the production writer rather than os.WriteFile:
	// readBoundedRegular rejects anything privateFilePermissionsOK does not
	// accept, and on Windows a hand-created file inherits the parent directory's
	// DACL instead of the restricted one protectPrivateFile applies. Building a
	// private file by hand therefore passes on macOS and fails on Windows.
	baseline := filepath.Join(root, "rime", "baseline.tsv")
	if _, err := writeAtomicPrivateFile(baseline, []byte("phrase\tpinyin\tsource\tuse_count\tpinned\n")); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	return Agent{
		Secrets: secrets, Profile: "default", DatabasePath: database,
		EndpointConfigPath: filepath.Join(root, "sync", "sync.json"),
		BaselinePath:       baseline,
		SnapshotPath:       filepath.Join(root, "rime", "private.tsv"),
		SnapshotStatePath:  filepath.Join(root, "sync", "snapshot-state"),
		Reload:             func(context.Context) error { reloads++; return nil },
	}
}

func TestAddPhraseReachesTheSnapshot(t *testing.T) {
	agent := newVocabularyAgent(t)
	ctx := context.Background()

	change, err := agent.AddPhrase(ctx, testPhraseText, testPhrasePinyin, true)
	if err != nil {
		t.Fatalf("AddPhrase: %v", err)
	}
	if !change.Applied || !change.Pinned {
		t.Fatalf("unexpected change: %+v", change)
	}
	// The point of the command is that the phrase becomes reachable as a
	// candidate, which means it has to land in the generated snapshot rather
	// than only in the database.
	if !change.SnapshotChanged {
		t.Fatal("the snapshot did not change after an explicit add")
	}
	if !change.SnapshotReloaded {
		t.Fatal("the host was not reloaded after the snapshot changed")
	}
	snapshot, err := os.ReadFile(agent.SnapshotPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if !strings.Contains(string(snapshot), testPhraseText) {
		t.Fatal("the added phrase is absent from the published snapshot")
	}
}

func TestRemovePhraseLeavesTheSnapshot(t *testing.T) {
	agent := newVocabularyAgent(t)
	ctx := context.Background()
	if _, err := agent.AddPhrase(ctx, testPhraseText, testPhrasePinyin, false); err != nil {
		t.Fatalf("AddPhrase: %v", err)
	}
	change, err := agent.RemovePhrase(ctx, testPhraseText, testPhrasePinyin)
	if err != nil {
		t.Fatalf("RemovePhrase: %v", err)
	}
	if !change.Applied {
		t.Fatalf("unexpected change: %+v", change)
	}
	snapshot, err := os.ReadFile(agent.SnapshotPath)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if strings.Contains(string(snapshot), testPhraseText) {
		t.Fatal("the removed phrase is still in the published snapshot")
	}
}

func TestPinAndUnpinRoundTrip(t *testing.T) {
	agent := newVocabularyAgent(t)
	ctx := context.Background()
	if _, err := agent.AddPhrase(ctx, testPhraseText, testPhrasePinyin, false); err != nil {
		t.Fatalf("AddPhrase: %v", err)
	}
	pinned, err := agent.SetPhrasePinned(ctx, testPhraseText, testPhrasePinyin, true)
	if err != nil {
		t.Fatalf("pin: %v", err)
	}
	if !pinned.Pinned {
		t.Fatalf("pin did not take effect: %+v", pinned)
	}
	unpinned, err := agent.SetPhrasePinned(ctx, testPhraseText, testPhrasePinyin, false)
	if err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if unpinned.Pinned {
		t.Fatalf("unpin did not take effect: %+v", unpinned)
	}
}

func TestEditingAnAbsentPhraseFailsRatherThanCreatingOne(t *testing.T) {
	agent := newVocabularyAgent(t)
	ctx := context.Background()
	if _, err := agent.SetPhrasePinned(ctx, testPhraseText, testPhrasePinyin, true); err == nil {
		t.Fatal("pinning an absent phrase silently created one")
	}
	if _, err := agent.RemovePhrase(ctx, testPhraseText, testPhrasePinyin); err == nil {
		t.Fatal("removing an absent phrase reported success")
	}
}

// The listing is the only way personal vocabulary leaves the store through this
// API, so the default must not carry it.
func TestListDefaultsToCountsWithoutVocabulary(t *testing.T) {
	agent := newVocabularyAgent(t)
	ctx := context.Background()
	if _, err := agent.AddPhrase(ctx, testPhraseText, testPhrasePinyin, true); err != nil {
		t.Fatalf("AddPhrase: %v", err)
	}

	summary, err := agent.ListVocabulary(ctx, VocabularyQuery{})
	if err != nil {
		t.Fatalf("ListVocabulary: %v", err)
	}
	if summary.Total != 1 || summary.Pinned != 1 {
		t.Fatalf("unexpected counts: %+v", summary)
	}
	if summary.TextIncluded || len(summary.Entries) != 0 {
		t.Fatalf("the default listing carried vocabulary: %+v", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), testPhraseText) ||
		strings.Contains(string(encoded), testPhrasePinyin) {
		t.Fatalf("the serialized default listing contains vocabulary: %s", encoded)
	}
}

func TestListIncludesVocabularyOnlyWhenAsked(t *testing.T) {
	agent := newVocabularyAgent(t)
	ctx := context.Background()
	if _, err := agent.AddPhrase(ctx, testPhraseText, testPhrasePinyin, true); err != nil {
		t.Fatalf("AddPhrase: %v", err)
	}
	summary, err := agent.ListVocabulary(ctx, VocabularyQuery{IncludeText: true})
	if err != nil {
		t.Fatalf("ListVocabulary: %v", err)
	}
	if !summary.TextIncluded || len(summary.Entries) != 1 {
		t.Fatalf("opting in did not return the phrase: %+v", summary)
	}
	if summary.Entries[0].Text != testPhraseText || summary.Entries[0].Pinyin != testPhrasePinyin {
		t.Fatalf("unexpected entry: %+v", summary.Entries[0])
	}
}

func TestVocabularyInputIsValidated(t *testing.T) {
	cases := []struct {
		name   string
		text   string
		pinyin string
	}{
		{"empty text", "", testPhrasePinyin},
		{"empty pinyin", testPhraseText, ""},
		{"tab in text", "合成\t词条", testPhrasePinyin},
		{"control character in text", "合成\x00词条", testPhrasePinyin},
		{"uppercase pinyin", testPhraseText, "He Cheng"},
		{"digits in pinyin", testPhraseText, "he2 cheng2"},
		{"overlong text", strings.Repeat("词", maxVocabularyTextRunes+1), testPhrasePinyin},
		{"overlong pinyin", testPhraseText, strings.Repeat("a", maxVocabularyPinyinRunes+1)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, _, err := validateVocabularyInput(testCase.text, testCase.pinyin); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
	text, pinyin, err := validateVocabularyInput("  "+testPhraseText+"  ", "  "+testPhrasePinyin+"  ")
	if err != nil {
		t.Fatalf("valid input was rejected: %v", err)
	}
	if text != testPhraseText || pinyin != testPhrasePinyin {
		t.Fatalf("surrounding whitespace was not trimmed: %q %q", text, pinyin)
	}
}

// An explicit edit must queue for the other devices exactly like a learned one,
// otherwise a correction made here would never reach R0W.
func TestExplicitEditsQueueForSynchronization(t *testing.T) {
	agent := newVocabularyAgent(t)
	ctx := context.Background()
	if _, err := agent.AddPhrase(ctx, testPhraseText, testPhrasePinyin, true); err != nil {
		t.Fatalf("AddPhrase: %v", err)
	}
	var pending uint64
	if err := agent.withPrivateStore(ctx, func(store *localstore.Store) error {
		count, err := store.PendingEventCount(ctx)
		pending = count
		return err
	}); err != nil {
		t.Fatalf("inspect outbox: %v", err)
	}
	if pending == 0 {
		t.Fatal("an explicit edit did not queue anything for the other devices")
	}
}
