// SPDX-License-Identifier: Apache-2.0
package main

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

func syntheticSourceAgent(t *testing.T) (desktopagent.Paths, desktopagent.Agent) {
	t.Helper()
	root := t.TempDir()
	paths := desktopagent.Paths{StateDirectory: root, DatabasePath: filepath.Join(root, "private.db"),
		NativeEventsPath: filepath.Join(root, "incoming"), BaselinePath: filepath.Join(root, "rime", "yunpin", "baseline.tsv"),
		SnapshotPath: filepath.Join(root, "rime", "yunpin", "private.tsv"), SnapshotStatePath: filepath.Join(root, "applied.json")}
	return paths, desktopagent.Agent{Profile: desktopagent.DefaultProfile, StateDirectory: paths.StateDirectory,
		DatabasePath: paths.DatabasePath, NativeEventsPath: paths.NativeEventsPath, BaselinePath: paths.BaselinePath,
		SnapshotPath: paths.SnapshotPath, SnapshotStatePath: paths.SnapshotStatePath}
}

func TestSyncOnceAndResidentConfigureSameDefaultLearningSource(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("desktop maintenance supported only on desktop platforms")
	}
	paths, manual := syntheticSourceAgent(t)
	resident := manual
	if err := desktopagent.ConfigureDefaultLearningSource(&resident, paths); err != nil {
		t.Fatal(err)
	}
	if err := configureSyncOnceLearning(&manual, paths, ""); err != nil {
		t.Fatal(err)
	}
	if manual.RimeUserDBExportPath == "" || manual.RimeUserDBExportPath != resident.RimeUserDBExportPath ||
		manual.RimeUserDBRefresh == nil || resident.RimeUserDBRefresh == nil {
		t.Fatal("manual/resident learning source diverged")
	}
}

func TestSyncOnceCustomStateNeedsExplicitSnapshotWithoutProductionMaintenance(t *testing.T) {
	paths, agent := syntheticSourceAgent(t)
	agent.StateDirectory = filepath.Join(t.TempDir(), "custom")
	if err := configureSyncOnceLearning(&agent, paths, ""); err == nil || agent.RimeUserDBRefresh != nil {
		t.Fatal("custom state implicitly invoked the production host")
	}
	fixture := filepath.Join(t.TempDir(), "explicit.userdb.txt")
	if err := configureSyncOnceLearning(&agent, paths, fixture); err != nil {
		t.Fatal(err)
	}
	if agent.RimeUserDBExportPath != fixture || agent.RimeUserDBRefresh != nil {
		t.Fatal("explicit snapshot was not isolated")
	}
}
