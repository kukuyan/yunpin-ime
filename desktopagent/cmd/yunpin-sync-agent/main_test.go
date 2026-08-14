// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

func TestInstallProbeIsIdentifierFreeAndNeedsNoConfiguredState(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	t.Cleanup(func() { os.Stdout = previous })
	err = run(context.Background(), []string{"install-probe"})
	if closeErr := write.Close(); err == nil {
		err = closeErr
	}
	os.Stdout = previous
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if err != nil || readErr != nil {
		t.Fatalf("install probe failed without configured state: err=%v read=%v", err, readErr)
	}
	if string(output) != "{\"ready\":true}\n" {
		t.Fatalf("install probe exposed nonconstant material: %q", output)
	}
	if err := run(context.Background(), []string{"install-probe", "unexpected"}); err == nil {
		t.Fatal("install probe accepted an argument")
	}
}

func TestResidentReadyMasksLocalStateDetailsBeforePlatformAccess(t *testing.T) {
	sensitive := filepath.Join(t.TempDir(), "private-user-state")
	err := run(context.Background(), []string{
		"resident-ready", "--state-dir", "relative-state", "--database", sensitive,
	})
	if err == nil || err.Error() != "resident readiness check failed" {
		t.Fatalf("resident readiness did not fail with its fixed redacted error: %v", err)
	}
	if strings.Contains(err.Error(), sensitive) || strings.Contains(err.Error(), "relative-state") {
		t.Fatalf("resident readiness exposed a local state path: %v", err)
	}
}

func TestResidentReadySuppressesFlagHelpAndDefaultPaths(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = write
	err = run(context.Background(), []string{"resident-ready", "--unknown-flag"})
	if closeErr := write.Close(); err == nil {
		err = closeErr
	}
	os.Stderr = previous
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if err == nil || err.Error() != "resident readiness check failed" || readErr != nil {
		t.Fatalf("resident readiness flag failure was not redacted: err=%v read=%v", err, readErr)
	}
	if len(output) != 0 {
		t.Fatalf("resident readiness emitted flag help or local paths: %q", output)
	}
}

func TestResidentReadinessOutputIsConstantAndIdentifierFree(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	err = writeJSON(desktopagent.ResidentReadiness{Ready: true})
	if closeErr := write.Close(); err == nil {
		err = closeErr
	}
	os.Stdout = previous
	output, readErr := io.ReadAll(read)
	_ = read.Close()
	if err != nil || readErr != nil || string(output) != "{\"ready\":true}\n" {
		t.Fatalf("resident readiness output is not constant: output=%q err=%v read=%v", output, err, readErr)
	}
}

func TestInitAccountRequiresExplicitRecoveryDisplayAcknowledgement(t *testing.T) {
	err := run(context.Background(), []string{"init-account"})
	if err == nil || !strings.Contains(err.Error(), "--confirm-saved-recovery-key") {
		t.Fatalf("init-account crossed confirmation gate: %v", err)
	}
}

func TestPrepareAccountRequiresExplicitRecoveryDisplayAcknowledgement(t *testing.T) {
	err := run(context.Background(), []string{"prepare-account"})
	if err == nil || !strings.Contains(err.Error(), "--confirm-display-recovery-key") {
		t.Fatalf("prepare-account crossed display gate: %v", err)
	}
}

func TestConfirmedInitAccountValidatesLocalPathsBeforePlatformOrNetworkAccess(t *testing.T) {
	err := run(context.Background(), []string{
		"init-account", "--confirm-saved-recovery-key", "--state-dir", "relative-state",
	})
	if err == nil || !strings.Contains(err.Error(), "state directory path must be absolute") {
		t.Fatalf("confirmed init-account crossed local validation gate: %v", err)
	}
}

func TestConfigureRimeBridgeRequiresExplicitConfirmation(t *testing.T) {
	err := run(context.Background(), []string{"configure-rime-bridge"})
	if err == nil || !strings.Contains(err.Error(), "requires --confirm") {
		t.Fatalf("Rime bridge setup crossed confirmation gate: %v", err)
	}
}

func TestConfigureServerAliasPersistsOnlySelectedEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("desktopagent package owns the exact Windows ACL fixture; this command test covers Unix dispatch")
	}
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "sync.json")
	if err := run(context.Background(), []string{
		"configure-server", "--endpoint-config", path, "--endpoint", "https://sync.example.test",
	}); err != nil {
		t.Fatal(err)
	}
	endpoint, err := syncclient.LoadEndpointConfig(path)
	if err != nil || endpoint.String() != "https://sync.example.test" {
		t.Fatalf("configured endpoint=%q err=%v", endpoint.String(), err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"token", "password", "recovery"} {
		if strings.Contains(strings.ToLower(string(contents)), forbidden) {
			t.Fatalf("endpoint configuration leaked %q: %s", forbidden, contents)
		}
	}
}

func TestUnknownCommandFailsWithoutPlatformAccess(t *testing.T) {
	err := run(context.Background(), []string{"unknown-command"})
	if err == nil || err.Error() != "unknown command" {
		t.Fatalf("unknown command error=%v", err)
	}
}

func TestPairingCommandsRemainOutsidePublicSwitch(t *testing.T) {
	if privatePairingCommandsEnabled {
		t.Skip("private E2E build intentionally registers pairing commands")
	}
	for _, command := range []string{
		"pairing-invite", "pairing-approve", "pairing-finalize", "pairing-join", "pairing-claim",
	} {
		if err := run(context.Background(), []string{command}); err == nil || err.Error() != "unknown command" {
			t.Fatalf("preview pairing command %q became public: %v", command, err)
		}
	}
}

func TestRimeUserDBExportIsExplicitlyInjectedByAbsolutePath(t *testing.T) {
	defaults, err := desktopagent.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	set := flag.NewFlagSet("sync-once", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	if err := parse(set, []string{"--rime-userdb-export", "relative.userdb.txt"}); err != nil {
		t.Fatal(err)
	}
	if err := common.validate(); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative Rime userdb export crossed the path gate: %v", err)
	}

	wanted := filepath.Join(t.TempDir(), "yunpin.userdb.txt")
	set = flag.NewFlagSet("sync-once", flag.ContinueOnError)
	common = addCommonFlags(set, defaults)
	if err := parse(set, []string{"--rime-userdb-export", wanted}); err != nil {
		t.Fatal(err)
	}
	if err := common.validate(); err != nil || common.rimeUserDB != wanted {
		t.Fatalf("absolute Rime userdb export was not injected: path=%q err=%v", common.rimeUserDB, err)
	}
}

func TestResidentRunRejectsCustomStateAndLockBeforePlatformAccess(t *testing.T) {
	defaults, err := desktopagent.DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := commandRun(context.Background(), defaults, []string{"--state-dir", filepath.Join(t.TempDir(), "state")}); err == nil || !strings.Contains(err.Error(), "fixed platform state directory") {
		t.Fatalf("resident accepted custom ack state root: %v", err)
	}
	if err := commandRun(context.Background(), defaults, []string{"--lock", filepath.Join(t.TempDir(), "agent.lock")}); err == nil || !strings.Contains(err.Error(), "fixed platform process lock") {
		t.Fatalf("resident accepted a bypass process lock: %v", err)
	}
}
