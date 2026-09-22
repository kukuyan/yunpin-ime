// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparePrivateVocabularyPreservesIdentityAndResumesFailedDeploy(t *testing.T) {
	bridge := testRimeBridgePaths(t)
	rime := filepath.Dir(bridge.InstallationPath)
	state := filepath.Dir(bridge.BackupPath)
	paths := Paths{StateDirectory: state, BaselinePath: filepath.Join(rime, "yunpin", "baseline.tsv"),
		SnapshotPath: filepath.Join(rime, "yunpin", "private.tsv"), SnapshotStatePath: filepath.Join(state, "snapshot-generation")}
	originalInstallation := "distribution_code_name: YunPin\ninstallation_id: " + testRimeInstallationID + "\n"
	writeTestRimeInstallation(t, bridge, originalInstallation)
	originalOverlay := settingsFixture + "  \"engine/filters/@before 0\": yunpin_filter@yunpin\n  \"yunpin/enabled\": false # explicit opt-in\n"
	overlay, err := RimeSettingsPath(paths)
	if err != nil {
		t.Fatal(err)
	}
	writePrivateTestFile(t, overlay, []byte(originalOverlay))
	writePrivateTestFile(t, paths.SnapshotStatePath, []byte("old-success-receipt\n"))
	deployFailure := errors.New("synthetic interrupted native deploy")
	calls := 0
	reload := func(ctx context.Context) error {
		calls++
		if enabled, _ := ctx.Value(settingsDeployContextKey{}).(bool); !enabled {
			t.Fatal("not a settings deploy")
		}
		if calls == 1 {
			return deployFailure
		}
		return nil
	}
	if err := PreparePrivateVocabulary(context.Background(), paths, reload); !errors.Is(err, deployFailure) {
		t.Fatalf("expected native failure: %v", err)
	}
	if err := PreparePrivateVocabulary(context.Background(), paths, reload); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("retry did not redeploy: %d", calls)
	}
	current, err := os.ReadFile(overlay)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(originalOverlay, `"yunpin/enabled": false`, `"yunpin/enabled": true`, 1)
	if string(current) != want {
		t.Fatalf("unrelated overlay settings changed: %s", current)
	}
	backup, err := os.ReadFile(filepath.Join(state, "onboarding-overlay-before.yaml"))
	if err != nil || string(backup) != originalOverlay {
		t.Fatalf("original backup was not retained: %v", err)
	}
	installationBytes, err := os.ReadFile(bridge.InstallationPath)
	if err != nil {
		t.Fatal(err)
	}
	installation, err := parseRimeInstallation(installationBytes)
	if err != nil || installation.ID != testRimeInstallationID || installation.SyncDir != bridge.SyncDirectory {
		t.Fatalf("identity/bridge: %+v %v", installation, err)
	}
	installationBackup, err := os.ReadFile(bridge.BackupPath)
	if err != nil || string(installationBackup) != originalInstallation {
		t.Fatalf("installation backup: %v", err)
	}
	if _, err := os.Stat(paths.BaselinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepare silently created a baseline: %v", err)
	}
	receipt, err := os.ReadFile(paths.SnapshotStatePath)
	if err != nil || !bytes.Equal(receipt, []byte("settings-pending\n")) {
		t.Fatalf("stale snapshot receipt survived: %q %v", receipt, err)
	}
}

func TestPrivateCandidateSettingRejectsConflictingDeclarations(t *testing.T) {
	base := "patch:\n  \"engine/filters/@before 0\": yunpin_filter@yunpin\n  \"yunpin/enabled\": false # explicit opt-in\n"
	if enabled, err := parsePrivateCandidates([]byte(base)); err != nil || enabled {
		t.Fatalf("valid disabled setting: %v %v", enabled, err)
	}
	if enabled, err := parsePrivateCandidates([]byte(strings.Replace(base, "false #", "true #", 1))); err != nil || !enabled {
		t.Fatalf("valid enabled setting: %v %v", enabled, err)
	}
	for _, extra := range []string{"  yunpin/enabled: true\n", "  'yunpin/enabled': true\n", "  \"yunpin/enabled\": true\n"} {
		if _, err := parsePrivateCandidates([]byte(base + extra)); err == nil {
			t.Fatal("duplicate opt-in accepted")
		}
	}
	if _, err := parsePrivateCandidates([]byte(strings.ReplaceAll(base, "yunpin_filter@yunpin", "other_filter"))); err == nil {
		t.Fatal("missing private candidate filter accepted")
	}
	if _, err := parsePrivateCandidates([]byte(strings.Replace(base, `  "engine/filters`, `  # "engine/filters`, 1))); err == nil {
		t.Fatal("commented-out private candidate filter accepted")
	}
	if _, err := parsePrivateCandidates([]byte(base + "  'engine/filters/@before 0': other_filter\n")); err == nil {
		t.Fatal("conflicting filter override accepted")
	}
}
