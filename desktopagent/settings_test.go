// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const settingsFixture = `# keep this comment
patch:
  "yunpin/short_input_guard": true
  "yunpin/long_correction_guard": true # keep inline
  "yunpin/typo_correction": false
  "yunpin/unrelated": true
`

func writeSettingsFixture(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rime_ice.custom.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGuardSettingsPreserveUnrelatedOverlayBytesAndDeploy(t *testing.T) {
	path := writeSettingsFixture(t, settingsFixture)
	reloads := 0
	result, err := ApplyGuardSettings(context.Background(), path, GuardSettings{
		ShortInputGuard: false, LongCorrectionGuard: true, TypoCorrection: true,
	}, func(context.Context) error {
		reloads++
		return nil
	})
	if err != nil || !result.Changed || !result.Reloaded || reloads != 1 {
		t.Fatalf("apply result=%#v reloads=%d err=%v", result, reloads, err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(settingsFixture, `"yunpin/short_input_guard": true`, `"yunpin/short_input_guard": false`, 1)
	want = strings.Replace(want, `"yunpin/typo_correction": false`, `"yunpin/typo_correction": true`, 1)
	if string(contents) != want {
		t.Fatalf("unrelated overlay bytes changed:\n%s", contents)
	}
	loaded, err := LoadGuardSettings(path)
	if err != nil || loaded != (GuardSettings{ShortInputGuard: false, LongCorrectionGuard: true, TypoCorrection: true}) {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestGuardSettingsDeployEvenWhenAlreadySelected(t *testing.T) {
	path := writeSettingsFixture(t, settingsFixture)
	reloads := 0
	result, err := ApplyGuardSettings(context.Background(), path, GuardSettings{
		ShortInputGuard: true, LongCorrectionGuard: true, TypoCorrection: false,
	}, func(context.Context) error {
		reloads++
		return nil
	})
	if err != nil || result.Changed || !result.Reloaded || reloads != 1 {
		t.Fatalf("apply result=%#v reloads=%d err=%v", result, reloads, err)
	}
}

func TestGuardSettingsRejectMissingDuplicateAndLinkedFiles(t *testing.T) {
	missing := writeSettingsFixture(t, strings.Replace(settingsFixture,
		`  "yunpin/typo_correction": false`+"\n", "", 1))
	if _, err := LoadGuardSettings(missing); err == nil {
		t.Fatal("settings missing a required guard were accepted")
	}
	duplicate := writeSettingsFixture(t, settingsFixture+`  "yunpin/typo_correction": true`+"\n")
	if _, err := LoadGuardSettings(duplicate); err == nil {
		t.Fatal("duplicate guard settings were accepted")
	}
	target := writeSettingsFixture(t, settingsFixture)
	linked := filepath.Join(t.TempDir(), "linked.yaml")
	if err := os.Symlink(target, linked); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	if _, err := LoadGuardSettings(linked); err == nil {
		t.Fatal("linked Rime settings were accepted")
	}
}

func TestRimeSettingsPathIsFixedBesideSnapshotRoot(t *testing.T) {
	root := t.TempDir()
	path, err := RimeSettingsPath(Paths{BaselinePath: filepath.Join(root, "yunpin", "baseline.tsv")})
	if err != nil || path != filepath.Join(root, "rime_ice.custom.yaml") {
		t.Fatalf("path=%q err=%v", path, err)
	}
	if _, err := RimeSettingsPath(Paths{BaselinePath: filepath.Join(root, "other.tsv")}); err == nil {
		t.Fatal("nonfixed baseline shape was accepted")
	}
}
