// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func windowsPrivateTestRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "private")
	if err := ensurePrivateWindowsDirectory(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeWindowsPrivateTestFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := ensurePrivateWindowsDirectory(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := protectPrivateFile(file); err == nil {
		_, err = file.Write(contents)
	}
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	info, err := os.Lstat(path)
	if err != nil || !privateFilePermissionsOK(path, info) {
		t.Fatalf("test private file verification failed: info=%#v err=%v", info, err)
	}
}

func applyWindowsTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := currentUserAndSystemSID()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func applyUnprotectedWindowsTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := currentUserAndSystemSID()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION,
		user, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsDefaultPathsUseKnownFoldersNotEnvironment(t *testing.T) {
	local, err := knownWindowsFolder(windows.FOLDERID_LocalAppData)
	if err != nil {
		t.Fatal(err)
	}
	roaming, err := knownWindowsFolder(windows.FOLDERID_RoamingAppData)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", `C:\untrusted-environment-local`)
	t.Setenv("APPDATA", `C:\untrusted-environment-roaming`)
	paths, err := DefaultPaths()
	if err != nil {
		t.Fatal(err)
	}
	for name, pair := range map[string][2]string{
		"state":    {local, paths.StateDirectory},
		"database": {local, paths.DatabasePath},
		"spool":    {local, paths.NativeEventsPath},
		"baseline": {roaming, paths.BaselinePath},
		"snapshot": {roaming, paths.SnapshotPath},
	} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("%s path escaped its Known Folder: root=%q path=%q relative=%q err=%v", name, pair[0], pair[1], relative, err)
		}
	}
}

func TestWindowsPrivateACLRequiresProtectedExactUserAndSystemACEs(t *testing.T) {
	root := windowsPrivateTestRoot(t)
	user, _, err := currentUserAndSystemSID()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		dacl        string
		unprotected bool
	}{
		{"extra-principal", "D:P(A;;FA;;;SY)(A;;FA;;;" + user.String() + ")(A;;FR;;;BA)", false},
		{"narrow-user-mask", "D:P(A;;FA;;;SY)(A;;FR;;;" + user.String() + ")", false},
		{"unprotected", "D:(A;;FA;;;SY)(A;;FA;;;" + user.String() + ")", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(root, test.name+".dat")
			writeWindowsPrivateTestFile(t, path, []byte("synthetic"))
			if test.unprotected {
				applyUnprotectedWindowsTestDACL(t, path, test.dacl)
			} else {
				applyWindowsTestDACL(t, path, test.dacl)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if privateFilePermissionsOK(path, info) {
				t.Fatalf("unsafe ACL %q was accepted", test.dacl)
			}
		})
	}
	directory := filepath.Join(root, "directory-with-file-only-flags")
	if err := ensurePrivateWindowsDirectory(directory); err != nil {
		t.Fatal(err)
	}
	applyWindowsTestDACL(t, directory, "D:P(A;;FA;;;SY)(A;;FA;;;"+user.String()+")")
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if privateDirectoryPermissionsOK(directory, info) {
		t.Fatal("directory ACL without exact OI+CI inheritance flags was accepted")
	}
}

func TestWindowsFileACLValidatesEffectiveNormalizedACEFlags(t *testing.T) {
	root := windowsPrivateTestRoot(t)
	path := filepath.Join(root, "normalized-file-ace-flags.dat")
	writeWindowsPrivateTestFile(t, path, []byte("synthetic"))
	user, _, err := currentUserAndSystemSID()
	if err != nil {
		t.Fatal(err)
	}
	// SetSecurityInfo normalizes container-only OI/CI bits away when applying
	// them to a non-container. Security decisions must use the effective DACL
	// read back from the handle, not the pre-normalization input SDDL.
	applyWindowsTestDACL(t, path, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;"+user.String()+")")
	handle, err := openWindowsPathNoReparse(path, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 2 {
		t.Fatalf("effective file DACL is unavailable or non-exact: dacl=%#v err=%v", dacl, err)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			t.Fatal(err)
		}
		if ace == nil || ace.Header.AceFlags != 0 {
			t.Fatalf("NTFS did not normalize non-container OI/CI flags: ace=%#v", ace)
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !privateFilePermissionsOK(path, info) {
		t.Fatalf("effective normalized exact file DACL was rejected: info=%#v err=%v", info, err)
	}
}

func TestWindowsProtectedExactDACLAllowsOnlyAutoInheritanceHistoryBit(t *testing.T) {
	root := windowsPrivateTestRoot(t)
	path := filepath.Join(root, "auto-inherited-history.dat")
	writeWindowsPrivateTestFile(t, path, []byte("synthetic"))
	user, _, err := currentUserAndSystemSID()
	if err != nil {
		t.Fatal(err)
	}
	applyWindowsTestDACL(t, path, "D:PAI(A;;FA;;;SY)(A;;FA;;;"+user.String()+")")
	info, err := os.Lstat(path)
	if err != nil || !privateFilePermissionsOK(path, info) {
		t.Fatalf("protected exact DACL was rejected solely for the NTFS auto-inheritance history bit: info=%#v err=%v", info, err)
	}
}

func TestWindowsPrivatePathParserRejectsAliasesAndTraversal(t *testing.T) {
	for _, path := range []string{
		`\\?\C:\private\file.dat`,
		`\\.\C:\private\file.dat`,
		`C:\private\..\outside.dat`,
		`C:\private\file.dat:alternate`,
		`C:\private\trailing.`,
		`C:\private\NUL.txt`,
	} {
		if _, err := windowsAbsoluteComponents(path); err == nil {
			t.Fatalf("unsafe Windows path %q was accepted", path)
		}
	}
}

func TestWindowsOpenedHandleIsBoundToPrivatePathIdentity(t *testing.T) {
	root := windowsPrivateTestRoot(t)
	first := filepath.Join(root, "first.dat")
	second := filepath.Join(root, "second.dat")
	writeWindowsPrivateTestFile(t, first, []byte("first"))
	writeWindowsPrivateTestFile(t, second, []byte("second"))
	file, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if !openedPrivateFilePermissionsOK(first, file, false) {
		t.Fatal("matching private file handle was rejected")
	}
	if openedPrivateFilePermissionsOK(second, file, false) {
		t.Fatal("file handle was accepted for a different path identity")
	}
}

func TestWindowsEveryPathComponentRejectsReparsePoints(t *testing.T) {
	root := windowsPrivateTestRoot(t)
	real := filepath.Join(root, "real")
	if err := ensurePrivateWindowsDirectory(real); err != nil {
		t.Fatal(err)
	}
	writeWindowsPrivateTestFile(t, filepath.Join(real, "payload.dat"), []byte("synthetic"))
	link := filepath.Join(root, "junction-like-link")
	if err := os.Symlink(real, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skip("Windows developer-mode symlink creation is unavailable")
		}
		t.Fatal(err)
	}
	if verifyPrivateWindowsPath(link, true) {
		t.Fatal("leaf directory reparse point was accepted")
	}
	if verifyPrivateWindowsPath(filepath.Join(link, "payload.dat"), false) {
		t.Fatal("intermediate directory reparse point was accepted")
	}
}

func TestWindowsAtomicReplacePreservesValidatedIdentityAndACL(t *testing.T) {
	root := windowsPrivateTestRoot(t)
	source := filepath.Join(root, "source.tmp")
	destination := filepath.Join(root, "destination.dat")
	writeWindowsPrivateTestFile(t, source, []byte("replacement"))
	writeWindowsPrivateTestFile(t, destination, []byte("old"))
	sourceChain, err := inspectWindowsPathChain(source, false)
	if err != nil {
		t.Fatal(err)
	}
	identity := sourceChain[len(sourceChain)-1].identity
	if err := replaceFile(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement source still exists: %v", err)
	}
	destinationChain, err := inspectWindowsPathChain(destination, false)
	if err != nil || destinationChain[len(destinationChain)-1].identity != identity {
		t.Fatalf("replacement identity mismatch: chain=%#v err=%v", destinationChain, err)
	}
	info, err := os.Lstat(destination)
	if err != nil || !privateFilePermissionsOK(destination, info) {
		t.Fatalf("replacement ACL verification failed: info=%#v err=%v", info, err)
	}
}

func TestWindowsDatabaseAndSidecarsAreHardenedTogether(t *testing.T) {
	root := windowsPrivateTestRoot(t)
	database := filepath.Join(root, "private.db")
	for _, path := range []string{database, database + "-wal", database + "-shm"} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte("synthetic")); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := protectPrivateDatabaseFiles(database); err != nil {
		t.Fatal(err)
	}
	if err := verifyPrivateDatabaseFiles(database); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsStatusRejectsNonPrivateDatabase(t *testing.T) {
	root := windowsPrivateTestRoot(t)
	endpoint := filepath.Join(root, "sync.json")
	if err := ConfigureEndpoint(endpoint, "https://sync.invalid", false); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "private.db")
	writeWindowsPrivateTestFile(t, database, []byte("synthetic-encrypted-database"))
	secrets := newMemorySecretStore()
	encoded, err := EncodeCredentialBundle(testCredentials())
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Save(context.Background(), "default", encoded); err != nil {
		zeroBytes(encoded)
		t.Fatal(err)
	}
	zeroBytes(encoded)
	user, _, err := currentUserAndSystemSID()
	if err != nil {
		t.Fatal(err)
	}
	applyWindowsTestDACL(t, database, "D:P(A;;FA;;;SY)(A;;FA;;;"+user.String()+")(A;;FR;;;BA)")
	if _, err := (Agent{
		Secrets: secrets, Profile: "default", EndpointConfigPath: endpoint, DatabasePath: database,
	}).Status(context.Background()); err == nil {
		t.Fatal("status accepted a database with an extra principal")
	}
}

func TestWindowsProcessLockIsExclusiveAndHandleValidated(t *testing.T) {
	path := filepath.Join(windowsPrivateTestRoot(t), "agent.lock")
	first, err := acquireProcessLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := acquireProcessLock(path)
	if !errors.Is(err, ErrAlreadyRunning) || second != nil {
		t.Fatalf("second lock=%#v err=%v", second, err)
	}
	windowsLock, ok := first.(*windowsProcessLock)
	if !ok {
		t.Fatalf("unexpected lock implementation %T", first)
	}
	if _, err := validatePrivateWindowsHandle(windowsLock.handle, false); err != nil {
		t.Fatalf("lock handle ACL verification failed: %v", err)
	}
}

func TestWindowsDPAPIFileIsHardenedAfterCreateAndReplace(t *testing.T) {
	local, err := knownWindowsFolder(windows.FOLDERID_LocalAppData)
	if err != nil {
		t.Fatal(err)
	}
	var randomSuffix [12]byte
	if _, err := rand.Read(randomSuffix[:]); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(local, "YunPinIME", "acl-test-"+hex.EncodeToString(randomSuffix[:]))
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	store, err := NewPlatformSecretStore(PlatformSecretStoreOptions{Service: "YunPinIME.acl-test", Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range [][]byte{[]byte("synthetic-first"), []byte("synthetic-replacement")} {
		if err := store.Save(context.Background(), "default", value); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "credentials-default.v1.dpapi")
		info, err := os.Lstat(path)
		if err != nil || !privateFilePermissionsOK(path, info) {
			t.Fatalf("DPAPI record ACL verification failed after save: info=%#v err=%v", info, err)
		}
		loaded, err := store.Load(context.Background(), "default")
		if err != nil || string(loaded) != string(value) {
			t.Fatalf("DPAPI round trip=%q err=%v", loaded, err)
		}
		zeroBytes(loaded)
	}
	if err := store.Delete(context.Background(), "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(directory, "credentials-default.v1.dpapi")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("DPAPI record remains after handle-bound deletion: %v", err)
	}
}
