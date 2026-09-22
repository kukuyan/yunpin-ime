// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotActivationRequiresMatchingNativeReceipt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	makePrivateTestDirectory(t, root)
	paths := Paths{BaselinePath: filepath.Join(root, "baseline.tsv"), SnapshotPath: filepath.Join(root, "private.tsv"), SnapshotStatePath: filepath.Join(root, "snapshot-generation")}
	baseline := []byte(privateSnapshotHeader + "办公室\tban gong shi\tpersonal\t3\ttrue\n")
	snapshot := []byte(generatedSnapshotHeader + "办公室\tban gong shi\tpersonal\t3\ttrue\t0\t0\n")
	if _, err := writeAtomicPrivateFile(paths.BaselinePath, baseline); err != nil {
		t.Fatal(err)
	}
	if _, err := writeAtomicPrivateFile(paths.SnapshotPath, snapshot); err != nil {
		t.Fatal(err)
	}
	state, err := ReadSnapshotActivation(paths)
	if err != nil || !state.BaselinePresent || !state.SnapshotPresent || state.SnapshotApplied {
		t.Fatalf("unacknowledged snapshot: %+v %v", state, err)
	}
	if err := markSnapshotReloaded(paths.SnapshotStatePath, sha256.Sum256(snapshot)); err != nil {
		t.Fatal(err)
	}
	state, err = ReadSnapshotActivation(paths)
	if err != nil || !state.SnapshotApplied || state.SnapshotRows != 1 {
		t.Fatalf("acknowledged snapshot: %+v %v", state, err)
	}
	changed := append(append([]byte(nil), snapshot...), []byte("工作\tgong zuo\tpersonal\t1\tfalse\t0\t0\n")...)
	if _, err := writeAtomicPrivateFile(paths.SnapshotPath, changed); err != nil {
		t.Fatal(err)
	}
	state, err = ReadSnapshotActivation(paths)
	if err != nil || state.SnapshotApplied {
		t.Fatalf("stale receipt accepted: %+v %v", state, err)
	}
	if err := os.Remove(paths.SnapshotPath); err != nil {
		t.Fatal(err)
	}
	state, err = ReadSnapshotActivation(paths)
	if err != nil || state.SnapshotPresent || state.SnapshotApplied {
		t.Fatalf("missing snapshot accepted: %+v %v", state, err)
	}
}

func TestSnapshotActivationDoesNotAcceptLegacyAsGenerated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private")
	makePrivateTestDirectory(t, root)
	paths := Paths{BaselinePath: filepath.Join(root, "baseline.tsv"), SnapshotPath: filepath.Join(root, "private.tsv"), SnapshotStatePath: filepath.Join(root, "snapshot-generation")}
	if _, err := writeAtomicPrivateFile(paths.SnapshotPath, []byte(privateSnapshotHeader+"词库\tci ku\tlegacy\t24\ttrue\n")); err != nil {
		t.Fatal(err)
	}
	state, err := ReadSnapshotActivation(paths)
	if err != nil || state.SnapshotPresent || state.SnapshotApplied || state.BaselinePresent {
		t.Fatalf("legacy misrepresented: %+v %v", state, err)
	}
}
