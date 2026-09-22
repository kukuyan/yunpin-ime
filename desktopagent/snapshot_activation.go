// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
)

// SnapshotActivation contains no private phrases. Applied means the exact
// current file matches the platform's last successful native reload receipt.
// It is not a claim that every possible candidate ranking has been tested.
type SnapshotActivation struct {
	BaselinePresent bool
	SnapshotPresent bool
	SnapshotApplied bool
	BaselineRows    int
	SnapshotRows    int
}

func ReadSnapshotActivation(paths Paths) (SnapshotActivation, error) {
	var result SnapshotActivation
	baselineContents, err := readBoundedRegular(paths.BaselinePath, maxBaselineBytes)
	if err == nil {
		baseline, parseErr := parseBaselineBytes(baselineContents)
		if parseErr != nil {
			return result, parseErr
		}
		result.BaselinePresent = true
		result.BaselineRows = len(baseline)
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	contents, err := readBoundedRegular(paths.SnapshotPath, maxBaselineBytes)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !bytes.HasPrefix(contents, []byte(generatedSnapshotHeader)) {
		// A legacy five-column file is import input, not proof of activation.
		return result, nil
	}
	rows, err := parseBaselineBytes(contents)
	if err != nil {
		return result, err
	}
	result.SnapshotPresent = true
	result.SnapshotRows = len(rows)
	pending, err := snapshotReloadPending(paths.SnapshotStatePath, sha256.Sum256(contents))
	if err != nil {
		return result, err
	}
	result.SnapshotApplied = !pending
	return result, nil
}
