// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func privateTestPath(t *testing.T, name string) string {
	t.Helper()
	temporary, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(temporary, "private")
	if err := makePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(directory, name)
}

func makePrivateTestDirectory(t *testing.T, path string) {
	t.Helper()
	if err := makePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !privateDirectoryPermissionsOK(path, info) {
		t.Fatalf("test private directory verification failed: info=%#v err=%v", info, err)
	}
}

// writePrivateTestFile models a producer that has completed the platform's
// private-file contract. POSIX mode arguments alone do not create the exact
// protected DACL required on Windows, so positive fixtures must protect and
// verify the opened object rather than relying on os.WriteFile(..., 0600).
func writePrivateTestFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	makePrivateTestDirectory(t, filepath.Dir(path))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = protectPrivateFile(file); err == nil {
		err = file.Truncate(0)
	}
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err == nil && len(contents) != 0 {
		var written int
		written, err = file.Write(contents)
		if err == nil && written != len(contents) {
			err = io.ErrShortWrite
		}
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
