// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"os"
	"strings"
	"testing"
)

func TestResidentInstallersContainNoCredentialOrVocabularyMaterial(t *testing.T) {
	paths := []string{
		"install/macos/Install-LaunchAgent.sh", "install/macos/Uninstall-LaunchAgent.sh",
		"install/macos/Verify-LaunchAgent.sh", "install/macos/Enable-LaunchAgent.sh",
		"install/windows/Install-SyncAgent.ps1", "install/windows/Uninstall-SyncAgent.ps1",
		"install/windows/Verify-SyncAgent.ps1", "install/windows/Enable-SyncAgent.ps1",
	}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		for _, forbidden := range []string{"yprec", "recovery_key", "device_token", "rollback_token", "private.tsv", "baseline.tsv", "sync.json"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("installer %s contains forbidden private material marker %q", path, forbidden)
			}
		}
	}
}

func TestResidentInstallAndVerifyRemainDisabledAndStopped(t *testing.T) {
	for _, path := range []string{"install/macos/Install-LaunchAgent.sh", "install/macos/Verify-LaunchAgent.sh"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		for _, required := range []string{"disabled", "install-probe"} {
			if !strings.Contains(lower, required) {
				t.Fatalf("macOS staging script %s lacks %q", path, required)
			}
		}
		for _, forbidden := range []string{"launchctl bootstrap", "launchctl kickstart"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("macOS staging script %s starts the resident via %q", path, forbidden)
			}
		}
		parser := `$1 == target && $2 == "=>" && ($3 == "true" || $3 == "disabled")`
		if !strings.Contains(string(contents), parser) {
			t.Fatalf("macOS staging script %s does not accept both documented launchctl disabled spellings exactly", path)
		}
		for _, unsafe := range []string{`$3 == "false"`, `$3 == "enabled"`} {
			if strings.Contains(string(contents), unsafe) {
				t.Fatalf("macOS staging script %s accepts enabled launchctl state via %q", path, unsafe)
			}
		}
	}
	macInstall, err := os.ReadFile("install/macos/Install-LaunchAgent.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(macInstall)), "launchctl disable") {
		t.Fatal("macOS installer does not persist the disabled registration")
	}
	for _, path := range []string{"install/windows/Install-SyncAgent.ps1", "install/windows/Verify-SyncAgent.ps1"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		for _, required := range []string{"disabled", "install-probe"} {
			if !strings.Contains(lower, required) {
				t.Fatalf("Windows staging script %s lacks %q", path, required)
			}
		}
		if strings.Contains(lower, "start-process -filepath $destination") {
			t.Fatalf("Windows staging script %s starts the resident", path)
		}
		if path == "install/windows/Verify-SyncAgent.ps1" && strings.Contains(lower, "start-scheduledtask") {
			t.Fatalf("Windows verifier %s starts the resident", path)
		}
	}
	windowsInstall, err := os.ReadFile("install/windows/Install-SyncAgent.ps1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(windowsInstall)), "disable-scheduledtask") {
		t.Fatal("Windows installer does not persist the disabled registration")
	}
	if !strings.Contains(strings.ToLower(string(windowsInstall)), "if ($previoustaskwasrunning)") {
		t.Fatal("Windows installer does not scope task restart to rollback restoration")
	}
}

func TestResidentEnableIsExplicitAndFailClosedOnRedactedSetupGate(t *testing.T) {
	tests := []struct {
		path     string
		required []string
	}{
		{path: "install/macos/Enable-LaunchAgent.sh", required: []string{"\"$installed_agent\" resident-ready >/dev/null 2>&1", "launchctl enable", "launchctl kickstart", "launchctl disable"}},
		{path: "install/windows/Enable-SyncAgent.ps1", required: []string{"& $destination resident-ready *> $null", "enable-scheduledtask", "start-scheduledtask", "disable-scheduledtask"}},
	}
	for _, test := range tests {
		contents, err := os.ReadFile(test.path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		for _, required := range test.required {
			if !strings.Contains(lower, required) {
				t.Fatalf("explicit enabler %s lacks %q", test.path, required)
			}
		}
		for _, forbidden := range []string{"write-output $destination", "write-host $destination", "cat \"$installed_agent\""} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("explicit enabler %s may log protected material via %q", test.path, forbidden)
			}
		}
		for _, forbidden := range []string{"\"$installed_agent\" status", "& $destination status"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("explicit enabler %s bypasses resident readiness via %q", test.path, forbidden)
			}
		}
	}
}

func TestResidentInstallersUseOnlyLocalRedactedReadinessChecks(t *testing.T) {
	for _, path := range []string{
		"install/macos/Install-LaunchAgent.sh", "install/macos/Verify-LaunchAgent.sh",
		"install/windows/Install-SyncAgent.ps1", "install/windows/Verify-SyncAgent.ps1",
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		if !strings.Contains(lower, " install-probe") || strings.Contains(lower, "\n\"$installed_agent\" status") ||
			strings.Contains(lower, "\n& $destination status") ||
			strings.Contains(lower, "sync-once") ||
			strings.Contains(lower, "http://") || strings.Contains(lower, "https://") {
			t.Fatalf("resident verifier %s is not a state-free local install probe", path)
		}
	}
}

// Moving the background loop to its own windowless binary changed the scheduled
// task's Execute path. The installer only replaces a task it recognises, so
// without the previous shape in that list an upgrade over an existing install
// would refuse to proceed and demand a manual uninstall first. The uninstaller
// has the mirror-image problem: it must accept both shapes and stop whichever
// generation is still running.
func TestWindowsInstallScriptsAcceptBothTaskGenerations(t *testing.T) {
	install, err := os.ReadFile("install/windows/Install-SyncAgent.ps1")
	if err != nil {
		t.Fatal(err)
	}
	lowerInstall := strings.ToLower(string(install))
	for _, required := range []string{
		// The current registration and the one earlier versions produced.
		"execute = $residentdestination; arguments = \"--interval 1m\"",
		"execute = $destination; arguments = \"run --interval 1m\"",
		"refusing to replace a different yunpinsyncagent scheduled task",
	} {
		if !strings.Contains(lowerInstall, required) {
			t.Fatalf("Windows installer upgrade path lacks %q", required)
		}
	}

	uninstall, err := os.ReadFile("install/windows/Uninstall-SyncAgent.ps1")
	if err != nil {
		t.Fatal(err)
	}
	lowerUninstall := strings.ToLower(string(uninstall))
	for _, required := range []string{
		"$task.actions[0].execute -cne $residentdestination -and",
		"$task.actions[0].execute -cne $destination",
		"foreach ($installed in @($residentdestination, $destination))",
	} {
		if !strings.Contains(lowerUninstall, required) {
			t.Fatalf("Windows uninstaller lacks %q", required)
		}
	}

	// Enable and Verify run against a freshly installed layout, so they may
	// stay strict -- but both must confirm the binary the task actually starts
	// exists, not only the interactive one.
	for _, name := range []string{"Enable-SyncAgent.ps1", "Verify-SyncAgent.ps1"} {
		contents, err := os.ReadFile("install/windows/" + name)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		if !strings.Contains(lower, "test-path -literalpath $residentdestination -pathtype leaf") {
			t.Fatalf("%s does not verify the resident binary the task starts", name)
		}
	}
}

func TestWindowsInstallerPinsManifestHashAndRestoresOnlyExactPreviousTask(t *testing.T) {
	contents, err := os.ReadFile("install/windows/Install-SyncAgent.ps1")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(contents))
	for _, required := range []string{
		"expectedsha256", "get-filehash -algorithm sha256",
		"refusing to replace a different yunpinsyncagent scheduled task",
		// The task runs the windowless resident, not the interactive agent:
		// Go links console-subsystem binaries by default and a scheduled task
		// starting one in the interactive session leaves a console window up
		// for the life of the process. The resident implements only the
		// background loop, so no "run" subcommand appears in the arguments.
		"new-scheduledtaskaction -execute $residentdestination -argument \"--interval 1m\"",
		"$previoustaskxml = export-scheduledtask",
		"register-scheduledtask -taskname $taskname -xml $previoustaskxml",
		"if ($previoustaskwasrunning)",
		"[io.file]::replace($temporary, $destination, $replacebackup, $true)",
		"[io.file]::replace($residenttemporary, $residentdestination, $residentreplacebackup, $true)",
		"remove-item -literalpath $temporary, $backup, $replacebackup",
		// Both binaries are hash-pinned against the bundle manifest.
		"residentexpectedsha256",
	} {
		if !strings.Contains(lower, required) {
			t.Fatalf("Windows installer transactional boundary lacks %q", required)
		}
	}
	for _, forbidden := range []string{"powershell.exe", "pwsh.exe", "cmd.exe"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("Windows resident task uses a foreground command wrapper %q", forbidden)
		}
	}
	if strings.Contains(lower, "[io.file]::replace($temporary, $destination, $null") {
		t.Fatal("Windows installer still uses the unsupported null File.Replace backup path")
	}
	// Registering the action on the interactive binary would put the console
	// window back. The legacy argument string may still appear elsewhere in the
	// script: the installer has to recognise the previous generation's task in
	// order to replace it during an upgrade.
	if strings.Contains(lower, "new-scheduledtaskaction -execute $destination") {
		t.Fatal("Windows scheduled task still runs the console-subsystem agent")
	}
}

func TestWindowsResidentTaskHasNoAutomaticSeventyTwoHourOrBatteryStop(t *testing.T) {
	checks := map[string][]string{
		"install/windows/Install-SyncAgent.ps1": {
			"-executiontimelimit ([timespan]::zero)",
			"-allowstartifonbatteries",
			"-dontstopifgoingonbatteries",
			"[string]$registered.settings.executiontimelimit -cne \"pt0s\"",
		},
		"install/windows/Verify-SyncAgent.ps1": {
			"[string]$registered.settings.executiontimelimit -cne \"pt0s\"",
			"$registered.settings.disallowstartifonbatteries",
			"$registered.settings.stopifgoingonbatteries",
		},
		"install/windows/Enable-SyncAgent.ps1": {
			"[string]$registered.settings.executiontimelimit -cne \"pt0s\"",
			"$registered.settings.disallowstartifonbatteries",
			"$registered.settings.stopifgoingonbatteries",
		},
	}
	for path, requiredValues := range checks {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		for _, required := range requiredValues {
			if !strings.Contains(lower, required) {
				t.Fatalf("Windows resident task %s can still stop automatically; missing %q", path, required)
			}
		}
	}
}

func TestPackageIntegrationKeepsPublicAndPrivateArtifactsSeparated(t *testing.T) {
	contents, err := os.ReadFile("install/README.md")
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(contents))
	for _, required := range []string{
		"public default-tag", "pairing-invite", "exactly `unknown command`",
		"independent private e2e artifacts", "exact seven public assets",
		"disabled and stopped", "mac and r0w", "invitation/ready/finalize",
		"retaining the private recovery state",
	} {
		if !strings.Contains(lower, required) {
			t.Fatalf("background-install release gate is missing %q", required)
		}
	}
}

func TestResidentUninstallersExplicitlyRetainPrivateState(t *testing.T) {
	for _, path := range []string{"install/macos/Uninstall-LaunchAgent.sh", "install/windows/Uninstall-SyncAgent.ps1"} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		if !strings.Contains(lower, "retained") || !strings.Contains(lower, "encrypted db") {
			t.Fatalf("uninstaller %s does not state its data-retention boundary", path)
		}
	}
}
