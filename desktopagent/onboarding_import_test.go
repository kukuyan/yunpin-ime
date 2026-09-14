// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const onboardingBaseRow = "静态词条\tjing tai ci tiao\treviewed_static\t8\ttrue\n"
const onboardingExtraRow = "合成词条\the cheng ci tiao\told_selection\t24\ttrue\n"

func onboardingTSV(rows string) []byte { return []byte(privateSnapshotHeader + rows) }

func onboardingTestPaths(t *testing.T) Paths {
	paths := emptyBaselineTestPaths(t)
	paths.SnapshotStatePath = filepath.Join(paths.StateDirectory, "snapshot-generation")
	return paths
}

func TestOnboardingBaselineImportExportRoundTripAndReplay(t *testing.T) {
	paths := onboardingTestPaths(t)
	input := onboardingTSV(onboardingBaseRow)
	result, err := ImportBaseline(paths, input, BaselineImportOptions{})
	if err != nil || !result.Changed || result.Rows != 1 {
		t.Fatalf("import: %+v %v", result, err)
	}
	before, _ := os.Stat(paths.BaselinePath)
	again, err := ImportBaseline(paths, input, BaselineImportOptions{})
	after, _ := os.Stat(paths.BaselinePath)
	if err != nil || again.Changed || !os.SameFile(before, after) {
		t.Fatalf("replay changed baseline identity: %+v %v", again, err)
	}
	contents, exported, err := ExportBaseline(paths)
	if err != nil || !bytes.Equal(contents, input) || exported.SHA256 != result.SHA256 || exported.ContentSHA256 != result.SHA256 {
		t.Fatalf("export: %+v %v", exported, err)
	}
	second := onboardingTestPaths(t)
	cloned, err := ImportBaseline(second, contents, BaselineImportOptions{})
	if err != nil || cloned.SHA256 != result.SHA256 {
		t.Fatalf("new-device import: %+v %v", cloned, err)
	}
	if _, err := os.Stat(paths.SnapshotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("import/export created generated snapshot")
	}
}

func TestOnboardingBaselineConfirmationBackupAndNonDestructiveMerge(t *testing.T) {
	paths := onboardingTestPaths(t)
	original := onboardingTSV(onboardingBaseRow)
	first, err := ImportBaseline(paths, original, BaselineImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writePrivateTestFile(t, paths.SnapshotPath, []byte("generated snapshot remains untouched"))
	upload := onboardingTSV(strings.Replace(onboardingBaseRow, "8\ttrue", "1\tfalse", 1) + onboardingExtraRow)
	if _, err := ImportBaseline(paths, upload, BaselineImportOptions{}); !errors.Is(err, ErrBaselineConfirmationRequired) {
		t.Fatalf("no confirmation: %v", err)
	}
	if _, err := ImportBaseline(paths, upload, BaselineImportOptions{Mode: "replace", ExpectedSHA256: strings.Repeat("0", 64)}); !errors.Is(err, ErrBaselineChanged) {
		t.Fatalf("stale confirmation: %v", err)
	}
	merged, err := ImportBaseline(paths, upload, BaselineImportOptions{Mode: "merge", ExpectedSHA256: first.SHA256})
	if err != nil || !merged.Changed || merged.Rows != 2 || merged.BackupName == "" {
		t.Fatalf("merge: %+v %v", merged, err)
	}
	contents, _, err := ExportBaseline(paths)
	if err != nil || !bytes.Equal(contents, onboardingTSV(onboardingBaseRow+onboardingExtraRow)) {
		t.Fatal("merge downgraded original count/pin/source")
	}
	backup := filepath.Join(filepath.Dir(paths.BaselinePath), "onboarding-backups", merged.BackupName)
	retained, err := readBoundedRegular(backup, MaxBaselineImportBytes)
	if err != nil || !bytes.Equal(retained, original) {
		t.Fatal("exact original backup missing")
	}
	if data, _ := os.ReadFile(paths.SnapshotPath); string(data) != "generated snapshot remains untouched" {
		t.Fatal("import touched snapshot")
	}
	replayed, err := ImportBaseline(paths, upload, BaselineImportOptions{Mode: "merge", ExpectedSHA256: merged.SHA256})
	if err != nil || replayed.Changed {
		t.Fatalf("merge replay: %+v %v", replayed, err)
	}
	replacement := onboardingTSV(onboardingExtraRow)
	replaced, err := ImportBaseline(paths, replacement, BaselineImportOptions{Mode: "replace", ExpectedSHA256: merged.SHA256})
	if err != nil || !replaced.Changed || replaced.Rows != 1 {
		t.Fatalf("explicit replace: %+v %v", replaced, err)
	}
	oldMerge, err := readBoundedRegular(filepath.Join(filepath.Dir(backup), replaced.BackupName), MaxBaselineImportBytes)
	if err != nil || !bytes.Equal(oldMerge, contents) {
		t.Fatal("replacement did not preserve prior complete baseline")
	}
}

func TestOnboardingBaselineRejectsEntireInvalidOrGeneratedUpload(t *testing.T) {
	cases := map[string][]byte{
		"empty":                     []byte(privateSnapshotHeader),
		"generated":                 []byte(generatedSnapshotHeader + "合成词条\the cheng ci tiao\tsynced_learning\t2\tfalse\t0\t0\n"),
		"generated stripped header": onboardingTSV(onboardingBaseRow + strings.Replace(onboardingExtraRow, "old_selection", "synced_learning@21000", 1)),
		"duplicate":                 onboardingTSV(onboardingBaseRow + onboardingBaseRow),
		"invalid final row":         onboardingTSV(onboardingBaseRow + "坏词\thuai ci\tfixture\t0\ttrue\n"),
		"continuous code":           onboardingTSV("合成词条\thechengcitiao\tfixture\t1\tfalse\n"),
		"invalid UTF8":              append(onboardingTSV(onboardingBaseRow), 0xff),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			paths := onboardingTestPaths(t)
			if _, err := ImportBaseline(paths, input, BaselineImportOptions{}); err == nil {
				t.Fatal("invalid upload accepted")
			}
			if _, err := os.Lstat(paths.BaselinePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("partial baseline created")
			}
		})
	}
}

func TestOnboardingStaticNativeFixturesAndHistoricalExport(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "platform", "macos", "tests", "fixtures", "private.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	paths := onboardingTestPaths(t)
	if _, err := ImportBaseline(paths, fixture, BaselineImportOptions{}); err != nil {
		t.Fatalf("repository native static fixture rejected: %v", err)
	}
	// Historical seven-column static files can be exported without pretending
	// that a generated seven-column user upload is safe to reclassify.
	rows, _ := parseBaselineBytes(onboardingTSV(onboardingBaseRow))
	seven, err := encodeSnapshot(rows)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateTestFile(t, paths.BaselinePath, seven)
	exported, result, err := ExportBaseline(paths)
	if err != nil || !bytes.Equal(exported, onboardingTSV(onboardingBaseRow)) || result.SHA256 == result.ContentSHA256 {
		t.Fatalf("historical export: %+v %v", result, err)
	}
	for _, pinyin := range []string{"x i", "fiao kei tei", "xi'an", "xi-an", "yi  hui"} {
		if _, err := parseOnboardingStatic(onboardingTSV("测试\t"+pinyin+"\tfixture\t1\tfalse\n"), false); err != nil {
			t.Fatalf("native-supported reading rejected: %q: %v", pinyin, err)
		}
	}
}

func TestOnboardingLegacySnapshotBackupSurvivesWizardReopen(t *testing.T) {
	paths := onboardingTestPaths(t)
	legacy := onboardingTSV(onboardingExtraRow)
	writePrivateTestFile(t, paths.SnapshotPath, legacy)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(paths.SnapshotPath, 0644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := ImportBaseline(paths, onboardingTSV(onboardingBaseRow), BaselineImportOptions{})
	if err != nil || result.LegacyBackupName == "" || result.LegacyRows != 1 {
		t.Fatalf("legacy backup: %+v %v", result, err)
	}
	retained, err := readBoundedRegular(paths.SnapshotPath, MaxBaselineImportBytes)
	if err != nil || !bytes.Equal(retained, legacy) {
		t.Fatal("legacy source content or repaired permissions differ")
	}
	status, err := InspectBaselineImport(paths)
	if err != nil || !status.Exists || len(status.PendingLegacy) != 1 || status.PendingLegacy[0].Name != result.LegacyBackupName || status.PendingLegacy[0].Rows != 1 {
		t.Fatalf("reopened wizard lost legacy restore: %+v %v", status, err)
	}
}

func TestOnboardingRejectsRedirectedOldSnapshotAndBusyImports(t *testing.T) {
	paths := onboardingTestPaths(t)
	if runtime.GOOS != "windows" {
		outside := privateTestPath(t, "outside.tsv")
		writePrivateTestFile(t, outside, onboardingTSV(onboardingExtraRow))
		makePrivateTestDirectory(t, filepath.Dir(paths.SnapshotPath))
		if err := os.Symlink(outside, paths.SnapshotPath); err != nil {
			t.Fatal(err)
		}
		if _, err := ImportBaseline(paths, onboardingTSV(onboardingBaseRow), BaselineImportOptions{}); err == nil {
			t.Fatal("redirected snapshot accepted")
		}
	}
	other := onboardingTestPaths(t)
	if err := WithProcessLock(other.LockPath, func() error {
		_, err := ImportBaseline(other, onboardingTSV(onboardingBaseRow), BaselineImportOptions{})
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("import ignored active process lock: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOnboardingLargeStaticBaselineRoundTrip(t *testing.T) {
	// Match the established baseline's scale using only generated fixtures;
	// this test never reads a developer's personal/static vocabulary files.
	var body bytes.Buffer
	body.WriteString(privateSnapshotHeader)
	for i := 0; i < 94384; i++ {
		fmt.Fprintf(&body, "合成基线%05d\the cheng ji xian\tfixture\t%d\t%t\n", i, i%31+1, i%7 == 0)
	}
	paths := onboardingTestPaths(t)
	imported, err := ImportBaseline(paths, body.Bytes(), BaselineImportOptions{})
	if err != nil || imported.Rows != 94384 {
		t.Fatalf("large baseline import: %+v %v", imported, err)
	}
	exported, summary, err := ExportBaseline(paths)
	if err != nil || summary.Rows != 94384 || !bytes.Equal(exported, body.Bytes()) || summary.ContentSHA256 != imported.SHA256 {
		t.Fatalf("large baseline export: %+v %v", summary, err)
	}
}

func TestOnboardingChangedBaselineInvalidatesOldEngineReceiptBeforePublish(t *testing.T) {
	paths := onboardingTestPaths(t)
	original := onboardingTSV(onboardingBaseRow)
	first, err := ImportBaseline(paths, original, BaselineImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := parseBaselineBytes(original)
	snapshot, _ := encodeSnapshot(rows)
	writePrivateTestFile(t, paths.SnapshotPath, snapshot)
	digest := sha256.Sum256(snapshot)
	if err := markSnapshotReloaded(paths.SnapshotStatePath, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportBaseline(paths, original, BaselineImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if pending, err := snapshotReloadPending(paths.SnapshotStatePath, digest); err != nil || pending {
		t.Fatalf("unchanged import invalidated valid ACK: %v", err)
	}
	if _, err := ImportBaseline(paths, onboardingTSV(onboardingExtraRow), BaselineImportOptions{Mode: "replace", ExpectedSHA256: first.SHA256}); err != nil {
		t.Fatal(err)
	}
	activation, err := ReadSnapshotActivation(paths)
	if err != nil || activation.SnapshotApplied {
		t.Fatalf("new baseline reused old snapshot ACK: %+v %v", activation, err)
	}
	if data, _ := os.ReadFile(paths.SnapshotPath); !bytes.Equal(data, snapshot) {
		t.Fatal("import unexpectedly published snapshot itself")
	}
}

func TestOnboardingReceiptFailurePreventsBaselineReplacement(t *testing.T) {
	paths := onboardingTestPaths(t)
	original := onboardingTSV(onboardingBaseRow)
	first, err := ImportBaseline(paths, original, BaselineImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.SnapshotStatePath); err != nil {
		t.Fatal(err)
	}
	makePrivateTestDirectory(t, paths.SnapshotStatePath)
	if _, err := ImportBaseline(paths, onboardingTSV(onboardingExtraRow), BaselineImportOptions{Mode: "replace", ExpectedSHA256: first.SHA256}); err == nil {
		t.Fatal("receipt failure did not stop replacement")
	}
	retained, err := readBoundedRegular(paths.BaselinePath, MaxBaselineImportBytes)
	if err != nil || !bytes.Equal(retained, original) {
		t.Fatal("baseline changed before receipt was invalidated")
	}
}

func TestOnboardingExplicitEmptyPreservesLegacyAndNeverClearsBaseline(t *testing.T) {
	paths := onboardingTestPaths(t)
	legacy := onboardingTSV(onboardingExtraRow)
	writePrivateTestFile(t, paths.SnapshotPath, legacy)
	result, err := InitializeOnboardingEmptyBaseline(paths)
	if err != nil || !result.Changed || result.Rows != 0 || result.LegacyRows != 1 || result.LegacyBackupName == "" {
		t.Fatalf("explicit empty setup: %+v %v", result, err)
	}
	baseline, err := readBoundedRegular(paths.BaselinePath, MaxBaselineImportBytes)
	if err != nil || string(baseline) != privateSnapshotHeader {
		t.Fatal("empty setup content differs")
	}
	status, err := InspectBaselineImport(paths)
	if err != nil || len(status.PendingLegacy) != 1 || status.PendingLegacy[0].Name != result.LegacyBackupName {
		t.Fatal("empty setup orphaned legacy backup")
	}
	if replay, err := InitializeOnboardingEmptyBaseline(paths); err != nil || replay.Changed {
		t.Fatalf("empty replay: %+v %v", replay, err)
	}
	if _, err := ImportBaseline(paths, []byte(privateSnapshotHeader), BaselineImportOptions{}); err == nil {
		t.Fatal("explicit empty action weakened ordinary upload validation")
	}
	writePrivateTestFile(t, paths.BaselinePath, onboardingTSV(onboardingBaseRow))
	if _, err := InitializeOnboardingEmptyBaseline(paths); !errors.Is(err, ErrBaselineConfirmationRequired) {
		t.Fatalf("explicit empty cleared an existing baseline: %v", err)
	}
}
