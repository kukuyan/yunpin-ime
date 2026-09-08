// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotReloadOldHostDoesNotReceiveUnknownArgument(t *testing.T) {
	for _, scenario := range []string{"old", "new", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "YunPin")
			help := "echo '--reload deploy'"
			if scenario == "new" {
				help = "echo '--reload-snapshot <request nonce>'"
			}
			if scenario == "oversized" {
				help = "printf '%17000s' 'x'"
			}
			script := "#!/bin/sh\nif [ \"$1\" = \"--help\" ]; then\n" + help +
				"\nexit 0\nfi\nprintf invoked > \"$0.invoked\"\n"
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := invokeDarwinSnapshotReload(ctx, path, strings.Repeat("a", 32))
			_, invoked := os.Stat(path + ".invoked")
			if scenario == "new" {
				if err != nil || invoked != nil {
					t.Fatalf("capable host was not invoked: %v %v", err, invoked)
				}
			} else if err == nil || !os.IsNotExist(invoked) {
				t.Fatal("legacy/invalid host received the new command")
			}
		})
	}
}

func TestSettingsExplicitDeployDoesNotWeakenSnapshotAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "YunPin")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n[ \"$1\" = \"--reload\" ] || exit 64\nprintf invoked > \"$0.invoked\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	invoke := func(ctx context.Context) error { return runDarwinReload(ctx, path, "") }
	if err := invoke(context.Background()); err == nil {
		t.Fatal("unmarked snapshot call silently downgraded")
	}
	if _, err := os.Stat(path + ".invoked"); !os.IsNotExist(err) {
		t.Fatal("unmarked call invoked host")
	}
	settings := writeSettingsFixture(t, settingsFixture)
	result, err := ApplyGuardSettings(context.Background(), settings,
		GuardSettings{ShortInputGuard: true, LongCorrectionGuard: true}, invoke)
	if err != nil || !result.Reloaded {
		t.Fatalf("explicit settings deploy regressed: %v", err)
	}
	if _, err := os.Stat(path + ".invoked"); err != nil {
		t.Fatal("settings did not invoke deploy")
	}
	both := context.WithValue(snapshotReloadContext(context.Background(), 1, sha256.Sum256(nil)), settingsDeployContextKey{}, true)
	if err := invoke(both); err == nil {
		t.Fatal("settings marker bypassed an explicit snapshot request")
	}
}

func TestSnapshotReloadRejectsLegacyNotificationOnlyMarker(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	makePrivateTestDirectory(t, directory)
	state := filepath.Join(directory, "snapshot.state")
	digest := sha256.Sum256([]byte("public fixture"))
	writePrivateTestFile(t, state, []byte("v1\t"+hex.EncodeToString(digest[:])+"\n"))
	if pending, err := snapshotReloadPending(state, digest); err != nil || !pending {
		t.Fatal("legacy notification-only receipt suppressed the first real application")
	}
	if err := markSnapshotReloaded(state, digest); err != nil {
		t.Fatal(err)
	}
	if pending, err := snapshotReloadPending(state, digest); err != nil || pending {
		t.Fatal("engine-applied marker was not recognized")
	}
}
