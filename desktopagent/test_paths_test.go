// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
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
