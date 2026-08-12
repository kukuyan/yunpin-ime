// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testRimeInstallationID = "01234567-89ab-cdef-0123-456789abcdef"

func testRimeBridgePaths(t *testing.T) RimeBridgePaths {
	t.Helper()
	temporary, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(temporary, "private")
	state := filepath.Join(root, "state")
	rime := filepath.Join(root, "rime")
	if err := makePrivateDirectory(state); err != nil {
		t.Fatal(err)
	}
	if err := makePrivateDirectory(rime); err != nil {
		t.Fatal(err)
	}
	return RimeBridgePaths{
		InstallationPath: filepath.Join(rime, "installation.yaml"),
		SyncDirectory:    filepath.Join(state, "rime-sync"),
		StagingPath:      filepath.Join(state, "rime-userdb.snapshot"),
		BackupPath:       filepath.Join(state, "rime-installation.pre-bridge.yaml"),
		AckPath:          filepath.Join(state, "rime-maintenance.ack"),
	}
}

func writeTestRimeInstallation(t *testing.T, paths RimeBridgePaths, contents string) {
	t.Helper()
	if err := os.WriteFile(paths.InstallationPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureRimeBridgeBacksUpThenAtomicallySetsDedicatedSyncDirectory(t *testing.T) {
	paths := testRimeBridgePaths(t)
	original := "distribution_code_name: YunPin\ninstallation_id: " + testRimeInstallationID + "\n"
	writeTestRimeInstallation(t, paths, original)
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	backup, err := readBoundedRegular(paths.BackupPath, maxRimeInstallationBytes)
	if err != nil || string(backup) != original {
		t.Fatalf("first-state backup mismatch: err=%v", err)
	}
	configured, err := readBoundedRegular(paths.InstallationPath, maxRimeInstallationBytes)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseRimeInstallation(configured)
	if err != nil || parsed.ID != testRimeInstallationID || parsed.SyncDir != paths.SyncDirectory {
		t.Fatalf("configured Rime identity/path mismatch: id_length=%d sync_match=%t err=%v",
			len(parsed.ID), parsed.SyncDir == paths.SyncDirectory, err)
	}
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatalf("idempotent setup failed: %v", err)
	}
	again, err := readBoundedRegular(paths.BackupPath, maxRimeInstallationBytes)
	if err != nil || !bytes.Equal(again, backup) {
		t.Fatalf("idempotent setup changed first-state backup: err=%v", err)
	}
	info, err := os.Lstat(paths.SyncDirectory)
	if err != nil || !privateDirectoryPermissionsOK(paths.SyncDirectory, info) {
		t.Fatalf("dedicated sync directory is not private: err=%v", err)
	}
}

func TestConfigureRimeBridgeRejectsDuplicateTopLevelIdentityBeforeBackup(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths,
		"installation_id: "+testRimeInstallationID+"\ninstallation_id: second\n")
	err := ConfigureRimeBridge(paths)
	if err == nil || !strings.Contains(err.Error(), "one safe top-level") {
		t.Fatalf("duplicate installation identity crossed setup gate: %v", err)
	}
	if _, statErr := os.Lstat(paths.BackupPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid installation produced a backup: %v", statErr)
	}
}

func TestRefreshRimeUserDBInvokesMaintenanceAndCopiesOnlyFreshStableSnapshot(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("# Rime user dictionary\n#@/db_name\trime_ice\n#@/db_type\tuserdb\n#@/user_id\t" +
		testRimeInstallationID + "\nni hao \t你好\tc=2 d=1 t=1\n")
	invoked := 0
	err := refreshRimeUserDB(context.Background(), paths, fixedRimeMaintenanceInvoker{Invoke: func(context.Context, string) error {
		invoked++
		device := filepath.Join(paths.SyncDirectory, testRimeInstallationID)
		if err := os.Mkdir(device, 0755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(device, "rime_ice.userdb.txt"), snapshot, 0644)
	}})
	if err != nil || invoked != 1 {
		t.Fatalf("fixed maintenance refresh failed: invoked=%d err=%v", invoked, err)
	}
	staged, err := readBoundedRegular(paths.StagingPath, maxRimeUserDBExportBytes)
	if err != nil || !bytes.Equal(staged, snapshot) {
		t.Fatalf("private staging mismatch: bytes=%d err=%v", len(staged), err)
	}
	devicePath := filepath.Join(paths.SyncDirectory, testRimeInstallationID)
	deviceInfo, err := os.Lstat(devicePath)
	if err != nil || !privateDirectoryPermissionsOK(devicePath, deviceInfo) {
		t.Fatalf("device snapshot directory was not hardened: %v", err)
	}
	sourcePath := filepath.Join(devicePath, "rime_ice.userdb.txt")
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil || !privateFilePermissionsOK(sourcePath, sourceInfo) {
		t.Fatalf("host snapshot was not hardened: %v", err)
	}
}

func TestRefreshRimeUserDBRejectsUnexpectedDeviceWithoutReadingBody(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	secretPhrase := "正文绝不能出现在错误里"
	err := refreshRimeUserDB(context.Background(), paths, fixedRimeMaintenanceInvoker{Invoke: func(context.Context, string) error {
		if err := os.Mkdir(filepath.Join(paths.SyncDirectory, testRimeInstallationID), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(paths.SyncDirectory, testRimeInstallationID, "rime_ice.userdb.txt"),
			[]byte("ni hao \t"+secretPhrase+"\tc=1 d=1 t=1\n"), 0600); err != nil {
			return err
		}
		return os.Mkdir(filepath.Join(paths.SyncDirectory, "unexpected-device"), 0700)
	}})
	if err == nil || !strings.Contains(err.Error(), "only the configured device") || strings.Contains(err.Error(), secretPhrase) {
		t.Fatalf("unexpected device/body privacy gate mismatch: %v", err)
	}
}

func TestRimeMaintenancePreflightRejectsUnexpectedDeviceBeforeInvoker(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(paths.SyncDirectory, "unexpected-device"), 0700); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := refreshRimeUserDB(context.Background(), paths, fixedRimeMaintenanceInvoker{
		Invoke: func(context.Context, string) error {
			calls++
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected device before host invocation") || calls != 0 {
		t.Fatalf("unexpected device crossed maintenance preflight: calls=%d err=%v", calls, err)
	}
}

func TestRimeMaintenancePreflightRejectsSymlinkBeforeInvoker(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(paths.SyncDirectory), "untrusted-device")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(paths.SyncDirectory, testRimeInstallationID)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := refreshRimeUserDB(context.Background(), paths, fixedRimeMaintenanceInvoker{
		Invoke: func(context.Context, string) error {
			calls++
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected device before host invocation") || calls != 0 {
		t.Fatalf("symlink device crossed maintenance preflight: calls=%d err=%v", calls, err)
	}
}

func TestRimeMaintenancePreflightHardensOwnedWeakModeCrashResidueBeforeInvoker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX crash-residue mode repair is covered by Windows ACL-native tests")
	}
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	device := filepath.Join(paths.SyncDirectory, testRimeInstallationID)
	if err := os.Mkdir(device, 0755); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("# Rime user dictionary\n#@/db_name\trime_ice\n#@/db_type\tuserdb\n#@/user_id\t" +
		testRimeInstallationID + "\nni hao \t你好\tc=2 d=1 t=1\n")
	source := filepath.Join(device, "rime_ice.userdb.txt")
	if err := os.WriteFile(source, snapshot, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(device, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(source, old, old); err != nil {
		t.Fatal(err)
	}
	calls := 0
	err := refreshRimeUserDB(context.Background(), paths, fixedRimeMaintenanceInvoker{
		Invoke: func(context.Context, string) error {
			calls++
			deviceInfo, deviceErr := os.Lstat(device)
			sourceInfo, sourceErr := os.Lstat(source)
			if deviceErr != nil || sourceErr != nil ||
				!privateDirectoryPermissionsOK(device, deviceInfo) ||
				!privateFilePermissionsOK(source, sourceInfo) {
				return errors.New("owned crash residue was not hardened before host invocation")
			}
			return os.WriteFile(source, snapshot, 0600)
		},
	})
	if err != nil || calls != 1 {
		t.Fatalf("owned weak-mode crash residue was not safely recovered: calls=%d err=%v", calls, err)
	}
}

func TestRefreshRimeUserDBRequiresMatchingPrivateHostAcknowledgement(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("# Rime user dictionary\n#@/db_name\trime_ice\n#@/db_type\tuserdb\n#@/user_id\t" +
		testRimeInstallationID + "\nni hao \t你好\tc=2 d=1 t=1\n")
	err := refreshRimeUserDB(context.Background(), paths, fixedRimeMaintenanceInvoker{
		RequiresAck: true,
		Invoke: func(_ context.Context, nonce string) error {
			if !safeMaintenanceNonce(nonce) {
				return errors.New("invalid generated nonce")
			}
			device := filepath.Join(paths.SyncDirectory, testRimeInstallationID)
			if err := os.Mkdir(device, 0700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(device, "rime_ice.userdb.txt"), snapshot, 0600); err != nil {
				return err
			}
			return os.WriteFile(paths.AckPath, []byte(nonce+"\n"), 0600)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(paths.AckPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("matched acknowledgement was not consumed: %v", err)
	}
}

func TestMatchingHostAcknowledgementRejectsUnchangedStaleSnapshot(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	device := filepath.Join(paths.SyncDirectory, testRimeInstallationID)
	if err := os.Mkdir(device, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("# Rime user dictionary\n#@/db_name\trime_ice\n#@/db_type\tuserdb\n#@/user_id\t" +
		testRimeInstallationID + "\nni hao \t你好\tc=2 d=1 t=1\n")
	if err := os.WriteFile(filepath.Join(device, "rime_ice.userdb.txt"), snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	err := refreshRimeUserDB(ctx, paths, fixedRimeMaintenanceInvoker{
		RequiresAck: true,
		Invoke: func(_ context.Context, nonce string) error {
			return os.WriteFile(paths.AckPath, []byte(nonce+"\n"), 0600)
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("matching acknowledgement accepted an unchanged stale snapshot: %v", err)
	}
	if _, statErr := os.Lstat(paths.StagingPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unchanged stale snapshot reached private staging: %v", statErr)
	}
}

func TestMatchingHostAcknowledgementAcceptsSameContentActualRewrite(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	device := filepath.Join(paths.SyncDirectory, testRimeInstallationID)
	if err := os.Mkdir(device, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("# Rime user dictionary\n#@/db_name\trime_ice\n#@/db_type\tuserdb\n#@/user_id\t" +
		testRimeInstallationID + "\nni hao \t你好\tc=2 d=1 t=1\n")
	source := filepath.Join(device, "rime_ice.userdb.txt")
	if err := os.WriteFile(source, snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(source, old, old); err != nil {
		t.Fatal(err)
	}
	err := refreshRimeUserDB(context.Background(), paths, fixedRimeMaintenanceInvoker{
		RequiresAck: true,
		Invoke: func(_ context.Context, nonce string) error {
			if err := os.WriteFile(source, snapshot, 0600); err != nil {
				return err
			}
			return os.WriteFile(paths.AckPath, []byte(nonce+"\n"), 0600)
		},
	})
	if err != nil {
		t.Fatalf("same-content snapshot rewrite was rejected: %v", err)
	}
	staged, err := readBoundedRegular(paths.StagingPath, maxRimeUserDBExportBytes)
	if err != nil || !bytes.Equal(staged, snapshot) {
		t.Fatalf("same-content rewritten snapshot was not staged: bytes=%d err=%v", len(staged), err)
	}
}

func TestStaleAcknowledgementIsRemovedBeforeNewNonceAndCannotReplay(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AckPath, []byte(strings.Repeat("a", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	device := filepath.Join(paths.SyncDirectory, testRimeInstallationID)
	if err := os.Mkdir(device, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("# Rime user dictionary\n#@/db_name\trime_ice\n#@/db_type\tuserdb\n#@/user_id\t" +
		testRimeInstallationID + "\nni hao \t你好\tc=2 d=1 t=1\n")
	if err := os.WriteFile(filepath.Join(device, "rime_ice.userdb.txt"), snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(device, "rime_ice.userdb.txt")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(source, old, old); err != nil {
		t.Fatal(err)
	}
	err := refreshRimeUserDB(context.Background(), paths, fixedRimeMaintenanceInvoker{
		RequiresAck: true,
		Invoke: func(_ context.Context, nonce string) error {
			if _, err := os.Lstat(paths.AckPath); !errors.Is(err, os.ErrNotExist) {
				return errors.New("stale acknowledgement remained at host invocation")
			}
			if err := os.WriteFile(source, snapshot, 0600); err != nil {
				return err
			}
			return os.WriteFile(paths.AckPath, []byte(nonce+"\n"), 0600)
		},
	})
	if err != nil {
		t.Fatalf("stale acknowledgement isolation failed: %v", err)
	}
}

func TestMatchingBusyAcknowledgementIsConsumedAndDoesNotStageOldSnapshot(t *testing.T) {
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	device := filepath.Join(paths.SyncDirectory, testRimeInstallationID)
	if err := os.Mkdir(device, 0700); err != nil {
		t.Fatal(err)
	}
	snapshot := []byte("# Rime user dictionary\n#@/db_name\trime_ice\n#@/db_type\tuserdb\n#@/user_id\t" +
		testRimeInstallationID + "\nni hao \t你好\tc=2 d=1 t=1\n")
	if err := os.WriteFile(filepath.Join(device, "rime_ice.userdb.txt"), snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AckPath, []byte("busy:"+strings.Repeat("a", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := refreshRimeUserDB(context.Background(), paths, fixedRimeMaintenanceInvoker{
		RequiresAck: true,
		Invoke: func(_ context.Context, nonce string) error {
			if _, err := os.Lstat(paths.AckPath); !errors.Is(err, os.ErrNotExist) {
				return errors.New("stale busy acknowledgement remained at host invocation")
			}
			return os.WriteFile(paths.AckPath, []byte("busy:"+nonce+"\n"), 0600)
		},
	})
	if !errors.Is(err, ErrRimeMaintenanceBusy) {
		t.Fatalf("matching busy acknowledgement was not returned as transient: %v", err)
	}
	if _, statErr := os.Lstat(paths.AckPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("matching busy acknowledgement was not consumed: %v", statErr)
	}
	if _, statErr := os.Lstat(paths.StagingPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("old snapshot was staged after busy acknowledgement: %v", statErr)
	}
}

func TestMismatchedBusyAcknowledgementCannotReplay(t *testing.T) {
	paths := testRimeBridgePaths(t)
	nonce := strings.Repeat("a", 32)
	if err := os.WriteFile(paths.AckPath, []byte("busy:"+strings.Repeat("b", 32)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := waitForMaintenanceAck(ctx, paths.AckPath, nonce)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrRimeMaintenanceBusy) {
		t.Fatalf("mismatched busy acknowledgement was accepted: %v", err)
	}
	if _, statErr := os.Lstat(paths.AckPath); statErr != nil {
		t.Fatalf("mismatched busy acknowledgement was consumed: %v", statErr)
	}
}

func TestBusyAcknowledgementRejectsOversizeAndSymlinkArtifacts(t *testing.T) {
	paths := testRimeBridgePaths(t)
	nonce := strings.Repeat("a", 32)
	if err := os.WriteFile(paths.AckPath, []byte(strings.Repeat("x", 129)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := waitForMaintenanceAck(context.Background(), paths.AckPath, nonce); err == nil ||
		errors.Is(err, ErrRimeMaintenanceBusy) {
		t.Fatalf("oversized acknowledgement crossed the private artifact gate: %v", err)
	}
	if err := os.Remove(paths.AckPath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(paths.AckPath), "untrusted-maintenance.ack")
	if err := os.WriteFile(target, []byte("busy:"+nonce+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths.AckPath); err != nil {
		t.Fatal(err)
	}
	if err := waitForMaintenanceAck(context.Background(), paths.AckPath, nonce); err == nil ||
		errors.Is(err, ErrRimeMaintenanceBusy) {
		t.Fatalf("symlink acknowledgement crossed the private artifact gate: %v", err)
	}
}
