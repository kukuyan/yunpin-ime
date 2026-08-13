// SPDX-License-Identifier: Apache-2.0
//go:build yunpin_pairing_private

package desktopagent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EmptyBaselineResult contains no path, phrase, or device identifier.  It is
// intentionally safe for the private E2E CLI to print after local creation.
type EmptyBaselineResult struct {
	Created bool `json:"created"`
}

func missingPath(path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return errors.New("empty baseline initialization refuses to overwrite existing Rime state")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return errors.New("empty baseline initialization could not exclude existing Rime state")
	}
	return nil
}

func prepareEmptyBaselineDirectory(baselinePath, snapshotPath string) (string, error) {
	if baselinePath == "" || snapshotPath == "" || !filepath.IsAbs(baselinePath) ||
		!filepath.IsAbs(snapshotPath) || filepath.Clean(baselinePath) != baselinePath ||
		filepath.Clean(snapshotPath) != snapshotPath || baselinePath == snapshotPath {
		return "", errors.New("fixed normalized baseline and snapshot paths are required")
	}
	parent := filepath.Dir(baselinePath)
	if filepath.Dir(snapshotPath) != parent || filepath.Base(baselinePath) != "baseline.tsv" ||
		filepath.Base(snapshotPath) != "private.tsv" {
		return "", errors.New("empty baseline initialization requires the fixed YunPin Rime filenames")
	}
	rimeRoot := filepath.Dir(parent)
	rootInfo, err := os.Lstat(rimeRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 ||
		!bridgePathComponentsOK(rimeRoot, true) {
		return "", errors.New("fixed YunPin Rime root is unavailable or unsafe")
	}
	parentInfo, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(parent, 0700); err != nil {
			return "", fmt.Errorf("create fixed private baseline directory: %w", err)
		}
		if err := hardenExistingPrivateDirectory(parent); err != nil {
			return "", fmt.Errorf("protect fixed private baseline directory: %w", err)
		}
		if err := syncParentDirectory(rimeRoot); err != nil {
			return "", fmt.Errorf("durably publish fixed private baseline directory: %w", err)
		}
	} else if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 ||
		!privateDirectoryPermissionsOK(parent, parentInfo) || !bridgePathComponentsOK(parent, true) {
		return "", errors.New("fixed baseline directory is not an exact private directory")
	}
	if err := missingPath(baselinePath); err != nil {
		return "", err
	}
	if err := missingPath(snapshotPath); err != nil {
		return "", err
	}
	return parent, nil
}

func initializeEmptyBaselineLocked(paths Paths) (EmptyBaselineResult, error) {
	parent, err := prepareEmptyBaselineDirectory(paths.BaselinePath, paths.SnapshotPath)
	if err != nil {
		return EmptyBaselineResult{}, err
	}
	temporary, err := os.CreateTemp(parent, ".baseline.tsv.*.tmp")
	if err != nil {
		return EmptyBaselineResult{}, fmt.Errorf("create private empty baseline temporary: %w", err)
	}
	temporaryPath := temporary.Name()
	temporaryIdentity, err := temporary.Stat()
	if err != nil {
		_ = temporary.Close()
		return EmptyBaselineResult{}, errors.New("inspect empty baseline temporary identity")
	}
	// Do not clean an uncertain pathname on failure. Another process running as
	// the same OS user could replace that name between an identity check and a
	// path-based remove. Successful no-replace publication atomically consumes
	// the temporary name; a failure leaves only a private constant-header temp
	// for explicit inspection.
	if err := protectPrivateFile(temporary); err != nil {
		_ = temporary.Close()
		return EmptyBaselineResult{}, fmt.Errorf("protect private empty baseline temporary: %w", err)
	}
	temporaryIdentity, err = temporary.Stat()
	if err != nil || !openedPrivateFilePermissionsOK(temporaryPath, temporary, false) {
		_ = temporary.Close()
		return EmptyBaselineResult{}, errors.New("private empty baseline temporary identity is invalid")
	}
	if written, writeErr := temporary.Write([]byte(privateSnapshotHeader)); writeErr != nil || written != len(privateSnapshotHeader) {
		_ = temporary.Close()
		return EmptyBaselineResult{}, errors.New("write complete empty baseline header")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return EmptyBaselineResult{}, fmt.Errorf("durably write empty baseline header: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return EmptyBaselineResult{}, fmt.Errorf("close empty baseline temporary: %w", err)
	}

	// Recheck both names immediately before the no-replace publish.  A normal
	// YunPin writer is serialized by LockPath; an outside same-user race is
	// still rejected by the OS-level no-clobber operation below.
	if err := missingPath(paths.BaselinePath); err != nil {
		return EmptyBaselineResult{}, err
	}
	if err := missingPath(paths.SnapshotPath); err != nil {
		return EmptyBaselineResult{}, err
	}
	if err := publishEmptyBaselineNoReplace(temporaryPath, paths.BaselinePath); err != nil {
		return EmptyBaselineResult{}, fmt.Errorf("publish empty baseline without replacement: %w", err)
	}

	published, err := os.Lstat(paths.BaselinePath)
	if err != nil || !published.Mode().IsRegular() || published.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(temporaryIdentity, published) ||
		!privateFilePermissionsOK(paths.BaselinePath, published) {
		return EmptyBaselineResult{}, errors.New("published empty baseline identity or permissions are invalid")
	}
	contents, err := readBoundedRegular(paths.BaselinePath, int64(len(privateSnapshotHeader)))
	if err != nil || !bytes.Equal(contents, []byte(privateSnapshotHeader)) {
		return EmptyBaselineResult{}, errors.New("published empty baseline content is invalid")
	}
	if err := missingPath(paths.SnapshotPath); err != nil {
		// A racing outside writer is already beyond the common process-lock
		// contract. Preserve the constant baseline instead of deleting by path;
		// path-based rollback cannot bind the identity through the unlink itself.
		return EmptyBaselineResult{}, errors.New("private snapshot appeared during empty baseline initialization")
	}
	return EmptyBaselineResult{Created: true}, nil
}

// InitializeEmptyBaseline creates the immutable empty baseline needed by a
// clean E2E device.  It never reads or overwrites an existing baseline or
// private snapshot and is deliberately available only in private-tag builds.
func InitializeEmptyBaseline(paths Paths) (result EmptyBaselineResult, err error) {
	if paths.LockPath == "" || !filepath.IsAbs(paths.LockPath) {
		return EmptyBaselineResult{}, errors.New("fixed private process lock is required")
	}
	err = WithProcessLock(paths.LockPath, func() error {
		var initializeErr error
		result, initializeErr = initializeEmptyBaselineLocked(paths)
		return initializeErr
	})
	return result, err
}
