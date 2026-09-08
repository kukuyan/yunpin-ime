// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Execute the production script with only its HOME path substituted. All
// service/signature tools are stubs, so no actual user job can be touched.
func TestMacInstallerFailureRestoresOnlyItsOwnChanges(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS shell utilities required")
	}
	for _, scenario := range []string{"loaded", "mkdir", "backup", "install", "lint", "disable", "probe", "success"} {
		for _, disabled := range []string{"true", "false"} {
			t.Run(scenario+"/disabled="+disabled, func(t *testing.T) {
				root := t.TempDir()
				fixture := filepath.Join(root, "fixture")
				bin := filepath.Join(fixture, "Library/Application Support/YunPin/Sync/bin")
				launch := filepath.Join(fixture, "Library/LaunchAgents")
				stubs := filepath.Join(root, "stubs")
				for _, dir := range []string{bin, launch, stubs} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
				write := func(path, contents string) {
					t.Helper()
					if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
						t.Fatal(err)
					}
				}
				installed := filepath.Join(bin, "yunpin-sync-agent")
				plist := filepath.Join(launch, "io.github.kukuyan.inputmethod.YunPin.sync-agent.plist")
				write(installed, "old agent\n")
				write(plist, "old plist\n")
				write(filepath.Join(root, "disabled"), disabled+"\n")
				write(filepath.Join(root, "source"), "#!/bin/sh\n[ \"$YUNPIN_FIXTURE_FAILURE\" != probe ]\n")
				write(filepath.Join(stubs, "codesign"), "#!/bin/sh\n[ \"$1\" != -d ] || echo Identifier=io.github.kukuyan.inputmethod.YunPin.sync-agent\nexit 0\n")
				write(filepath.Join(stubs, "launchctl"), `#!/bin/sh
case "$1" in
print) [ "$YUNPIN_FIXTURE_FAILURE" = loaded ] ;;
print-disabled) printf '"io.github.kukuyan.inputmethod.YunPin.sync-agent" => '; /bin/cat "$YUNPIN_FIXTURE_ROOT/disabled" ;;
disable|enable)
  echo "$1" >> "$YUNPIN_FIXTURE_ROOT/mutations"
  if [ "$1" = disable ]; then echo true > "$YUNPIN_FIXTURE_ROOT/disabled"; else echo false > "$YUNPIN_FIXTURE_ROOT/disabled"; fi
  [ "$YUNPIN_FIXTURE_FAILURE" != disable ] ;;
*) exit 90 ;;
esac
`)
				for _, utility := range []string{"mkdir", "cp", "install", "plutil"} {
					real, err := exec.LookPath(utility)
					if err != nil {
						t.Fatal(err)
					}
					condition := "false"
					switch utility {
					case "mkdir":
						condition = `[ "$YUNPIN_FIXTURE_FAILURE" = mkdir ]`
					case "cp":
						condition = `[ "$YUNPIN_FIXTURE_FAILURE" = backup ]`
					case "install":
						condition = `[ "$YUNPIN_FIXTURE_FAILURE" = install ]`
					case "plutil":
						condition = `[ "$YUNPIN_FIXTURE_FAILURE" = lint ] && [ "$1" = -lint ]`
					}
					write(filepath.Join(stubs, utility), "#!/bin/sh\nif "+condition+"; then exit 42; fi\nexec "+real+" \"$@\"\n")
				}
				original, err := os.ReadFile("install/macos/Install-LaunchAgent.sh")
				if err != nil {
					t.Fatal(err)
				}
				isolated := strings.ReplaceAll(string(original), "$HOME", fixture)
				if strings.Contains(isolated, "$HOME") {
					t.Fatal("unisolated path")
				}
				script := filepath.Join(root, "install.sh")
				write(script, isolated)
				cmd := exec.Command("/bin/sh", script, filepath.Join(root, "source"))
				cmd.Env = append(os.Environ(), "PATH="+stubs+":/usr/bin:/bin:/usr/sbin:/sbin", "YUNPIN_FIXTURE_ROOT="+root, "YUNPIN_FIXTURE_FAILURE="+scenario)
				output, err := cmd.CombinedOutput()
				if scenario == "success" {
					if err != nil {
						t.Fatalf("success: %v: %s", err, output)
					}
					return
				}
				if err == nil {
					t.Fatalf("fault did not fail: %s", output)
				}
				for path, expected := range map[string]string{installed: "old agent\n", plist: "old plist\n", filepath.Join(root, "disabled"): disabled + "\n"} {
					actual, err := os.ReadFile(path)
					if err != nil || string(actual) != expected {
						t.Errorf("prior state changed for %s: %q %v; script: %s", filepath.Base(path), actual, err, output)
					}
				}
				if scenario == "loaded" {
					if _, err := os.Stat(filepath.Join(root, "mutations")); !os.IsNotExist(err) {
						t.Error("refusal mutated launchctl state")
					}
				}
			})
		}
	}
}
