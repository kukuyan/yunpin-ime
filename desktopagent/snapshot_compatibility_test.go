// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
)

func TestNativeSnapshotCompatibility(t *testing.T) {
	for _, test := range []struct {
		name, pinyin string
		want         bool
	}{
		{"separated", "ni hao", true},
		{"canonical tones and boundaries", "LǙ4---SÈ", true},
		{"continuous syllables are not guessed", "nihao", false},
		{"arbitrary Latin token", "codex", false},
		{"unknown token among valid syllables", "ni zz hao", false},
		{"separated acronym", "r o w", true},
		{"finite legacy spellings", "fiao kei tei", true},
		{"legacy one letter too broad", "b", false},
		{"standard one letter syllable", "a", true},
		{"empty canonical code", "---", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := nativeSnapshotCompatible("测试", protocol.CanonicalPinyin(test.pinyin), 0); got != test.want {
				t.Fatalf("compatibility=%t want=%t", got, test.want)
			}
		})
	}
	if nativeSnapshotCompatible("测试\u0085词", "ce shi ci", 0) {
		t.Fatal("native-rejected C1 control reached projection")
	}
	if nativeSnapshotCompatible("测试", "ce shi", -1) {
		t.Fatal("native-rejected negative recency reached projection")
	}
}

func TestGeneratedSnapshotSyllablesMatchNativeAuthority(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate native source authority")
	}
	root := filepath.Dir(filepath.Dir(source))
	for _, test := range []struct {
		path, declaration string
		generated         map[string]struct{}
		quoted            bool
	}{
		{"engine/src/phrase_engine.cpp", `(?s)kSyllables\s*=\s*R"\((.*?)\)"`, nativeSnapshotSyllables, false},
		{"librime-yunpin/src/snapshot_store.cpp", `(?s)kLegacyPrivateSpellings\s*=\s*\{(.*?)\};`, nativeSnapshotLegacySyllables, true},
	} {
		contents, err := os.ReadFile(filepath.Join(root, test.path))
		if err != nil {
			t.Fatal(err)
		}
		match := regexp.MustCompile(test.declaration).FindSubmatch(contents)
		if len(match) != 2 {
			t.Fatal("native declaration changed; review compatibility contract before regenerating")
		}
		authority := snapshotSyllableSet(string(match[1]))
		if test.quoted {
			authority = make(map[string]struct{})
			for _, token := range regexp.MustCompile(`"([a-z]+)"`).FindAllSubmatch(match[1], -1) {
				authority[string(token[1])] = struct{}{}
			}
		}
		if len(authority) == 0 || !reflect.DeepEqual(authority, test.generated) {
			t.Fatalf("generated snapshot syllables drifted from %s; run go generate", test.path)
		}
	}
}

func TestRebuildExcludesIncompatibleLearnedProjectionWithoutMutatingStore(t *testing.T) {
	ctx := context.Background()
	store := openBridgeStore(t)
	phrases := []localstore.Phrase{
		{Text: "连码示例", Pinyin: "nihao", UseCount: 23, Pinned: true},
		{Text: "伪码示例", Pinyin: "codex", UseCount: 7},
		{Text: "正常词", Pinyin: "zheng chang ci", UseCount: 31, Pinned: true},
		{Text: "兼容词", Pinyin: "fiao kei tei", UseCount: 5},
		{Text: "字母词", Pinyin: "r o w", UseCount: 13, Pinned: true},
	}
	for _, phrase := range phrases {
		if err := store.SaveExplicit(ctx, phrase); err != nil {
			t.Fatal(err)
		}
	}
	before, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pendingBefore, err := store.PendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "private")
	makePrivateTestDirectory(t, root)
	baseline := filepath.Join(root, "baseline.tsv")
	snapshot := filepath.Join(root, "private.tsv")
	baselineBytes := []byte(privateSnapshotHeader + "静态词\tjing tai ci\tsogou_import\t9\tfalse\n")
	writePrivateTestFile(t, baseline, baselineBytes)
	for attempt := 0; attempt < 2; attempt++ {
		summary, err := rebuildPrivateSnapshot(ctx, store, baseline, snapshot)
		if err != nil || summary.ExcludedLearnedRows != 2 || summary.LearnedRows != 3 || summary.TotalRows != 4 || summary.Changed != (attempt == 0) {
			t.Fatalf("attempt=%d summary=%#v err=%v", attempt, summary, err)
		}
	}
	after, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pendingAfter, err := store.PendingEvents(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(pendingBefore, pendingAfter) {
		t.Fatal("candidate projection mutated encrypted phrase identity/count/pin/clock or outbox")
	}
	baselineAfter, err := os.ReadFile(baseline)
	if err != nil || !bytes.Equal(baselineBytes, baselineAfter) {
		t.Fatal("candidate projection changed immutable baseline bytes")
	}
	generated, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, phrase := range phrases[:2] {
		if bytes.Contains(generated, []byte(phrase.Text)) {
			t.Fatal("incompatible learned phrase reached native snapshot")
		}
	}
	for _, expected := range []string{
		"正常词\tzheng chang ci\tsynced_learning\t31\ttrue",
		"兼容词\tfiao kei tei\tsynced_learning\t5\tfalse",
		"字母词\tr o w\tsynced_learning\t13\ttrue",
	} {
		if !strings.Contains(string(generated), expected) {
			t.Fatal("compatible learned count/pin or code was changed")
		}
	}
}
