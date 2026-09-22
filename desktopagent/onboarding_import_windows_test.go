// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOnboardingWindowsLegacyInheritedACLIsBackedUpThenProtected(t *testing.T) {
	paths := onboardingTestPaths(t)
	input := onboardingTSV(onboardingExtraRow)
	writePrivateTestFile(t, paths.SnapshotPath, input)
	user, _, err := currentUserAndSystemSID()
	if err != nil {
		t.Fatal(err)
	}
	// Model the old installer's inherited access and Administrators grant.
	// Do not require administrator privilege merely to run the regression test.
	applyUnprotectedWindowsTestDACL(t, paths.SnapshotPath, "D:(A;;FA;;;SY)(A;;FA;;;"+user.String()+")(A;;FA;;;BA)")
	before, err := os.Stat(paths.SnapshotPath)
	if err != nil || privateFilePermissionsOK(paths.SnapshotPath, before) {
		t.Fatal("fixture did not model the legacy permission failure")
	}
	result, err := ImportBaseline(paths, onboardingTSV(onboardingBaseRow), BaselineImportOptions{})
	if err != nil || result.LegacyBackupName == "" {
		t.Fatalf("first setup: %+v %v", result, err)
	}
	after, err := os.Stat(paths.SnapshotPath)
	if err != nil || !os.SameFile(before, after) || !privateFilePermissionsOK(paths.SnapshotPath, after) {
		t.Fatal("legacy source identity or exact ACL not preserved/repaired")
	}
	for _, path := range []string{paths.SnapshotPath, filepath.Join(filepath.Dir(paths.BaselinePath), "onboarding-backups", result.LegacyBackupName)} {
		data, err := readBoundedRegular(path, MaxBaselineImportBytes)
		if err != nil || !bytes.Equal(data, input) {
			t.Fatal("source/backup byte preservation failed")
		}
	}
}
