// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
)

func TestRebuildMigratesStaticBaselineAndAppendsOnlyLearnedOverlay(t *testing.T) {
	store := openBridgeStore(t)
	root := filepath.Join(t.TempDir(), "private")
	makePrivateTestDirectory(t, root)
	baseline := filepath.Join(root, "yunpin", "baseline.tsv")
	snapshotPath := filepath.Join(root, "yunpin", "private.tsv")
	original := privateSnapshotHeader +
		"静态旧词\tr o w\tsogou_import\t7\tfalse\n" +
		"重合词\tchong he ci\tsogou_import\t3\tfalse\n"
	makePrivateTestDirectory(t, filepath.Dir(snapshotPath))
	writePrivateTestFile(t, snapshotPath, []byte(original))
	if err := store.SaveExplicit(context.Background(), localstore.Phrase{
		Text: "同步新词", Pinyin: "tong bu xin ci", Source: "native_selection", UseCount: 5, Pinned: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveExplicit(context.Background(), localstore.Phrase{
		Text: "重合词", Pinyin: "chong he ci", Source: "native_selection", UseCount: 9, Pinned: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveExplicit(context.Background(), localstore.Phrase{
		Text: "重合词", Pinyin: "wan quan bu tong", Source: "native_selection", UseCount: 12, Pinned: true,
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := rebuildPrivateSnapshot(context.Background(), store, baseline, snapshotPath)
	if err != nil || !summary.Changed || summary.BaselineRows != 2 || summary.LearnedRows != 1 || summary.TotalRows != 3 {
		t.Fatalf("rebuild summary=%#v err=%v", summary, err)
	}
	baselineBytes, err := os.ReadFile(baseline)
	if err != nil || string(baselineBytes) != original {
		t.Fatalf("static baseline changed: err=%v bytes=%q", err, baselineBytes)
	}
	generated, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, expected := range []string{
		"静态旧词\tr o w\tsogou_import\t7\tfalse",
		"重合词\tchong he ci\tsogou_import\t3\tfalse",
		"同步新词\ttong bu xin ci\tsynced_learning\t5\ttrue",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("generated snapshot lacks %q: %q", expected, text)
		}
	}
	if strings.Contains(text, "重合词\tchong he ci\tsogou_import\t9\ttrue") {
		t.Fatal("stale learned overlay rewrote reviewed static baseline metadata")
	}
	if strings.Contains(text, "重合词\twan quan bu tong\tsynced_learning") {
		t.Fatal("stale heteronym overlay crossed the phrase-only static boundary")
	}
	if info, err := os.Lstat(snapshotPath); err != nil {
		t.Fatal(err)
	} else if !privateFilePermissionsOK(snapshotPath, info) {
		t.Fatalf("snapshot is not an exact private regular file: mode=%v", info.Mode())
	}
	statePath := filepath.Join(root, "sync", "snapshot-generation")
	pending, err := snapshotReloadPending(statePath, summary.digest)
	if err != nil || !pending {
		t.Fatalf("new snapshot did not require reload: pending=%t err=%v", pending, err)
	}
	if err := markSnapshotReloaded(statePath, summary.digest); err != nil {
		t.Fatal(err)
	}
	pending, err = snapshotReloadPending(statePath, summary.digest)
	if err != nil || pending {
		t.Fatalf("committed reload marker was not recognized: pending=%t err=%v", pending, err)
	}
	otherDigest := summary.digest
	otherDigest[0] ^= 0xff
	pending, err = snapshotReloadPending(statePath, otherDigest)
	if err != nil || !pending {
		t.Fatalf("stale reload marker was accepted: pending=%t err=%v", pending, err)
	}
}

func TestRebuildDoesNotReplaceMalformedExistingSnapshot(t *testing.T) {
	store := openBridgeStore(t)
	root := filepath.Join(t.TempDir(), "private")
	makePrivateTestDirectory(t, root)
	baseline := filepath.Join(root, "baseline.tsv")
	snapshotPath := filepath.Join(root, "private.tsv")
	original := []byte("not-a-private-snapshot\n")
	writePrivateTestFile(t, snapshotPath, original)
	if _, err := rebuildPrivateSnapshot(context.Background(), store, baseline, snapshotPath); err == nil {
		t.Fatal("malformed existing snapshot was overwritten")
	}
	current, _ := os.ReadFile(snapshotPath)
	if string(current) != string(original) {
		t.Fatalf("malformed snapshot changed: %q", current)
	}
}

func TestMergeCanonicalizesLearnedPinyinAndDropsUnsafeRemoteRows(t *testing.T) {
	rows, learned := mergeSnapshotRows(nil, []localstore.Phrase{
		{Text: "测试", Pinyin: "CÈ---SHI4", UseCount: 3},
		{Text: "方向\u202e词", Pinyin: "fang xiang ci", UseCount: 9},
		{Text: "空拼音", Pinyin: "---", UseCount: 9},
	})
	if learned != 1 || len(rows) != 1 || rows[0].Phrase != "测试" || rows[0].Pinyin != "ce shi" {
		t.Fatalf("unsafe or noncanonical learned rows reached snapshot: learned=%d rows=%#v", learned, rows)
	}
}
