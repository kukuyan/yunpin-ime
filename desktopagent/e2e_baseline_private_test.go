// SPDX-License-Identifier: Apache-2.0
//go:build yunpin_pairing_private

package desktopagent

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func emptyBaselineTestPaths(t *testing.T) Paths {
	t.Helper()
	root := filepath.Dir(privateTestPath(t, "root-anchor"))
	rimeRoot := filepath.Join(root, "Rime")
	if err := makePrivateDirectory(rimeRoot); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	return Paths{
		StateDirectory: state,
		LockPath:       filepath.Join(state, "agent.lock"),
		BaselinePath:   filepath.Join(rimeRoot, "yunpin", "baseline.tsv"),
		SnapshotPath:   filepath.Join(rimeRoot, "yunpin", "private.tsv"),
	}
}

func TestInitializeEmptyBaselineCreatesOnlyExactPrivateHeader(t *testing.T) {
	paths := emptyBaselineTestPaths(t)
	result, err := InitializeEmptyBaseline(paths)
	if err != nil || !result.Created {
		t.Fatalf("initialize empty baseline: result=%#v err=%v", result, err)
	}
	contents, err := os.ReadFile(paths.BaselinePath)
	if err != nil || !bytes.Equal(contents, []byte(privateSnapshotHeader)) {
		t.Fatalf("empty baseline content mismatch: %q err=%v", contents, err)
	}
	info, err := os.Lstat(paths.BaselinePath)
	if err != nil || !privateFilePermissionsOK(paths.BaselinePath, info) {
		t.Fatalf("empty baseline is not an exact private file: %v", err)
	}
	if _, err := os.Lstat(paths.SnapshotPath); !os.IsNotExist(err) {
		t.Fatalf("initializer created or accepted a private snapshot: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(paths.BaselinePath))
	if err != nil || len(entries) != 1 || entries[0].Name() != "baseline.tsv" {
		t.Fatalf("initializer left unexpected Rime files: entries=%v err=%v", entries, err)
	}
}

func TestInitializeEmptyBaselineNeverOverwritesExistingBaseline(t *testing.T) {
	paths := emptyBaselineTestPaths(t)
	if err := makePrivateDirectory(filepath.Dir(paths.BaselinePath)); err != nil {
		t.Fatal(err)
	}
	original := []byte("existing baseline must remain opaque\n")
	writePrivateTestFile(t, paths.BaselinePath, original)
	if _, err := InitializeEmptyBaseline(paths); err == nil || !strings.Contains(err.Error(), "refuses to overwrite") {
		t.Fatalf("existing baseline did not stop initialization: %v", err)
	}
	after, err := os.ReadFile(paths.BaselinePath)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("existing baseline changed: %q err=%v", after, err)
	}
}

func TestInitializeEmptyBaselineNeverOverwritesExistingSnapshot(t *testing.T) {
	paths := emptyBaselineTestPaths(t)
	if err := makePrivateDirectory(filepath.Dir(paths.SnapshotPath)); err != nil {
		t.Fatal(err)
	}
	original := []byte("existing snapshot remains opaque\n")
	writePrivateTestFile(t, paths.SnapshotPath, original)
	if _, err := InitializeEmptyBaseline(paths); err == nil || !strings.Contains(err.Error(), "refuses to overwrite") {
		t.Fatalf("existing snapshot did not stop initialization: %v", err)
	}
	if _, err := os.Lstat(paths.BaselinePath); !os.IsNotExist(err) {
		t.Fatalf("baseline was created beside an existing snapshot: %v", err)
	}
	after, err := os.ReadFile(paths.SnapshotPath)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("existing snapshot changed: %q err=%v", after, err)
	}
}

func TestInitializeEmptyBaselineIsNoClobberOnReplay(t *testing.T) {
	paths := emptyBaselineTestPaths(t)
	if _, err := InitializeEmptyBaseline(paths); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(paths.BaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeEmptyBaseline(paths); err == nil {
		t.Fatal("initializer replay replaced an existing baseline")
	}
	after, err := os.Lstat(paths.BaselinePath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("initializer replay changed baseline identity: %v", err)
	}
}

func TestInitializeEmptyBaselineRejectsUnsafeFixedObject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reparse-point rejection has native path-security coverage")
	}
	paths := emptyBaselineTestPaths(t)
	if err := makePrivateDirectory(filepath.Dir(paths.BaselinePath)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("outside\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths.BaselinePath); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeEmptyBaseline(paths); err == nil {
		t.Fatal("initializer accepted a symlink at the fixed baseline path")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "outside\n" {
		t.Fatalf("symlink target changed: %q err=%v", contents, err)
	}
}
