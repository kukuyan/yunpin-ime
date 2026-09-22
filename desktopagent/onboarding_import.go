// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kukuyan/yunpin-ime/protocol"
)

// MaxBaselineImportBytes bounds an upload before the GUI allocates its body.
const MaxBaselineImportBytes = maxBaselineBytes

var (
	ErrBaselineConfirmationRequired = errors.New("existing baseline requires an explicit merge or replacement")
	ErrBaselineChanged              = errors.New("baseline changed since confirmation; review it again")
	ErrGeneratedSnapshotImport      = errors.New("generated private snapshots cannot be imported as static vocabulary")
)

type BaselineImportOptions struct {
	// The default is create. Merge keeps existing rows and adds new identities;
	// replacement is explicit. Both require the current file's SHA256, obtained
	// from ExportBaseline, so a stale confirmation cannot overwrite newer data.
	Mode           string `json:"mode"`
	ExpectedSHA256 string `json:"expected_sha256"`
}

// These results contain no phrases. BackupName is relative to the baseline's
// fixed onboarding-backups directory, never an uploaded filename or path.
type BaselineImportResult struct {
	Changed          bool   `json:"changed"`
	Rows             int    `json:"rows"`
	SHA256           string `json:"sha256"`
	BackupName       string `json:"backup_name,omitempty"`
	LegacyBackupName string `json:"legacy_backup_name,omitempty"`
	LegacyRows       int    `json:"legacy_rows"`
}

type BaselineExportResult struct {
	Rows int `json:"rows"`
	// SHA256 identifies the current on-disk baseline for replacement approval.
	SHA256 string `json:"sha256"`
	// ContentSHA256 identifies the downloadable five-column representation.
	ContentSHA256 string `json:"content_sha256"`
}

type LegacyBackupInfo struct {
	Name string `json:"name"`
	Rows int    `json:"rows"`
}

type BaselineImportStatus struct {
	Exists        bool               `json:"exists"`
	Rows          int                `json:"rows"`
	SHA256        string             `json:"sha256"`
	PendingLegacy []LegacyBackupInfo `json:"pending_legacy"`
}

// InspectBaselineImport recovers the wizard's durable state after its window is
// closed. It returns metadata only and does not open the learned database.
func InspectBaselineImport(paths Paths) (result BaselineImportStatus, err error) {
	if err := validateOnboardingPaths(paths); err != nil {
		return result, err
	}
	err = WithProcessLock(paths.LockPath, func() error {
		parent := filepath.Dir(paths.BaselinePath)
		if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if !bridgePathComponentsOK(parent, true) {
			return errors.New("baseline directory is redirected")
		}
		current, readErr := readBoundedRegular(paths.BaselinePath, MaxBaselineImportBytes)
		if readErr == nil {
			rows, err := parseOnboardingStatic(current, true)
			if err != nil {
				return err
			}
			result.Exists, result.Rows, result.SHA256 = true, len(rows), onboardingDigest(current)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		directory := filepath.Join(filepath.Dir(paths.BaselinePath), "onboarding-backups")
		info, statErr := os.Lstat(directory)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil || !info.IsDir() || !privateDirectoryPermissionsOK(directory, info) || !bridgePathComponentsOK(directory, true) {
			return ErrLegacyBackupInvalid
		}
		dir, err := os.Open(directory)
		if err != nil {
			return err
		}
		entries, readDirErr := dir.ReadDir(257)
		closeErr := dir.Close()
		if readDirErr != nil && !errors.Is(readDirErr, io.EOF) {
			return readDirErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(entries) > 256 {
			return errors.New("too many onboarding backup entries")
		}
		var totalBytes int64
		for _, entry := range entries {
			digest, ok := legacyBackupDigest(entry.Name())
			if !ok {
				continue
			}
			marker := filepath.Join(directory, "legacy-imported-"+digest+".complete")
			complete, err := readBoundedRegular(marker, 128)
			if err == nil {
				if !bytes.Equal(complete, legacyCompletionMarker(digest)) {
					return ErrLegacyBackupInvalid
				}
				continue
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			totalBytes += info.Size()
			if totalBytes > 2*MaxBaselineImportBytes {
				return errors.New("pending legacy backups exceed inspection limit")
			}
			contents, err := readBoundedRegular(filepath.Join(directory, entry.Name()), MaxBaselineImportBytes)
			if err != nil || onboardingDigest(contents) != digest {
				return ErrLegacyBackupInvalid
			}
			rows, err := parseOnboardingStatic(contents, false)
			if err != nil {
				return err
			}
			result.PendingLegacy = append(result.PendingLegacy, LegacyBackupInfo{Name: entry.Name(), Rows: len(rows)})
		}
		sort.Slice(result.PendingLegacy, func(i, j int) bool { return result.PendingLegacy[i].Name < result.PendingLegacy[j].Name })
		return nil
	})
	return result, err
}

func onboardingDigest(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func parseOnboardingStatic(contents []byte, allowHistoricalHeader bool) ([]snapshotRow, error) {
	if len(contents) > MaxBaselineImportBytes || !utf8.Valid(contents) {
		return nil, errors.New("baseline upload exceeds the limit or is not UTF-8")
	}
	// Existing baseline files may have the historical seven-column header, but
	// the public upload route deliberately never accepts that generated format.
	header, _, _ := bytes.Cut(contents, []byte("\n"))
	header = bytes.TrimSuffix(header, []byte("\r"))
	if !allowHistoricalHeader && string(header) == strings.TrimSuffix(generatedSnapshotHeader, "\n") {
		return nil, ErrGeneratedSnapshotImport
	}
	rows, err := parseBaselineBytes(contents)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Source, "synced_learning") || row.LastUsedDay != 0 || row.CorrectionScore != 0 {
			return nil, ErrGeneratedSnapshotImport
		}
		if protocol.CanonicalPhrase(row.Phrase) == "" || strings.TrimSpace(row.Source) != row.Source ||
			!nativeSnapshotCompatible(row.Phrase, protocol.CanonicalPinyin(row.Pinyin), 0) {
			return nil, errors.New("baseline contains a row the input method cannot load")
		}
	}
	return rows, nil
}

func encodeOnboardingStatic(rows []snapshotRow) []byte {
	var out bytes.Buffer
	out.WriteString(privateSnapshotHeader)
	for _, row := range rows {
		fmt.Fprintf(&out, "%s\t%s\t%s\t%d\t%t\n", row.Phrase, row.Pinyin, row.Source, row.UseCount, row.Pinned)
	}
	return out.Bytes()
}

func validateOnboardingPaths(paths Paths) error {
	for _, path := range []string{paths.LockPath, paths.BaselinePath, paths.SnapshotPath} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("normalized absolute onboarding paths are required")
		}
	}
	if filepath.Base(paths.BaselinePath) != "baseline.tsv" || filepath.Base(paths.SnapshotPath) != "private.tsv" ||
		filepath.Dir(paths.BaselinePath) != filepath.Dir(paths.SnapshotPath) {
		return errors.New("onboarding requires the fixed baseline and private snapshot filenames")
	}
	return nil
}

func prepareOnboardingDirectory(paths Paths) error {
	parent := filepath.Dir(paths.BaselinePath)
	root := filepath.Dir(parent)
	if !bridgePathComponentsOK(root, true) {
		return errors.New("Rime root is unavailable or contains a redirected path")
	}
	if _, err := os.Lstat(parent); err == nil {
		if err := hardenExistingPrivateDirectory(parent); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensurePrivateDirectory(parent); err != nil {
		return err
	}
	if !bridgePathComponentsOK(parent, true) {
		return errors.New("baseline directory contains a redirected path")
	}
	return nil
}

// Older Windows installers left private.tsv with an Administrators owner and
// inherited ACLs. This one fixed first-setup source is allowed to be read before
// hardening: bind its handle and pathname, validate all bytes, retain and verify
// a private backup, then protect that same source without changing its content.
// No other Rime file or database is read through this exception.
func backupOnboardingLegacySnapshot(paths Paths) (name string, rows int, returnErr error) {
	path := paths.SnapshotPath
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, nil
	}
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > MaxBaselineImportBytes || !bridgePathComponentsOK(path, false) {
		return "", 0, errors.New("existing private snapshot is not a bounded non-redirected file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() {
		if err := file.Close(); returnErr == nil {
			returnErr = err
		}
	}()
	opened, err := file.Stat()
	current, statErr := os.Lstat(path)
	if err != nil || statErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(current, opened) {
		return "", 0, errors.New("legacy snapshot changed during open")
	}
	contents, err := io.ReadAll(io.LimitReader(file, MaxBaselineImportBytes+1))
	if err != nil {
		return "", 0, err
	}
	parsed, err := parseOnboardingStatic(contents, false)
	if err != nil {
		return "", 0, err
	}
	name, err = backupOnboardingBytes(paths, "legacy-private", contents)
	if err != nil {
		return "", 0, err
	}
	retained, err := readBoundedRegular(filepath.Join(filepath.Dir(path), "onboarding-backups", name), MaxBaselineImportBytes)
	if err != nil || !bytes.Equal(retained, contents) {
		return "", 0, errors.New("legacy backup verification failed")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", 0, err
	}
	verified, err := io.ReadAll(io.LimitReader(file, MaxBaselineImportBytes+1))
	current, statErr = os.Lstat(path)
	if err != nil || statErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(current, opened) || !bytes.Equal(verified, contents) || !bridgePathComponentsOK(path, false) {
		return "", 0, errors.New("legacy snapshot changed before permission repair")
	}
	if err := protectPrivateFile(file); err != nil {
		return "", 0, err
	}
	if !openedPrivateFilePermissionsOK(path, file, false) {
		return "", 0, errors.New("legacy snapshot permission repair did not verify")
	}
	return name, len(parsed), nil
}

// publishOnboardingNew uses the existing cross-platform no-replace primitive.
// Failed publication leaves a private temporary for review, not a path-based
// rollback that could remove another process's replacement.
func publishOnboardingNew(path string, contents []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".onboarding.*.tmp")
	if err != nil {
		return err
	}
	if err = protectPrivateFile(file); err == nil {
		_, err = file.Write(contents)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return publishEmptyBaselineNoReplace(file.Name(), path)
}

func backupOnboardingBytes(paths Paths, prefix string, contents []byte) (string, error) {
	directory := filepath.Join(filepath.Dir(paths.BaselinePath), "onboarding-backups")
	if err := ensurePrivateDirectory(directory); err != nil {
		return "", err
	}
	if !bridgePathComponentsOK(directory, true) {
		return "", errors.New("backup directory is redirected")
	}
	name := prefix + "-" + onboardingDigest(contents) + ".tsv"
	path := filepath.Join(directory, name)
	existing, err := readBoundedRegular(path, MaxBaselineImportBytes)
	if err == nil {
		if !bytes.Equal(existing, contents) {
			return "", errors.New("retained backup differs")
		}
		return name, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := publishOnboardingNew(path, contents); err != nil {
		return "", err
	}
	return name, nil
}

// ImportBaseline validates the complete upload before mutation and acquires the
// standard process lock itself. It changes baseline.tsv, private backup files
// and the reload-pending receipt. It never edits the encrypted store, its
// deletion records, the generated private.tsv, or the running input method.
// A subsequent normal sync publishes
// the combined candidate snapshot. Do not call while already holding LockPath.
func ImportBaseline(paths Paths, contents []byte, options BaselineImportOptions) (result BaselineImportResult, err error) {
	return importOnboardingBaseline(paths, contents, options, false)
}

// InitializeOnboardingEmptyBaseline is the explicit "start empty" wizard action.
// It shares the import transaction so existing legacy private.tsv is backed up
// and can be restored into the learned Store after initial sync. It never
// replaces a nonempty baseline, and does not make empty uploaded files valid.
func InitializeOnboardingEmptyBaseline(paths Paths) (BaselineImportResult, error) {
	return importOnboardingBaseline(paths, []byte(privateSnapshotHeader), BaselineImportOptions{Mode: "create"}, true)
}

func importOnboardingBaseline(paths Paths, contents []byte, options BaselineImportOptions, allowEmpty bool) (result BaselineImportResult, err error) {
	if err := validateOnboardingPaths(paths); err != nil {
		return result, err
	}
	if !filepath.IsAbs(paths.SnapshotStatePath) || filepath.Clean(paths.SnapshotStatePath) != paths.SnapshotStatePath ||
		filepath.Base(paths.SnapshotStatePath) != "snapshot-generation" || filepath.Dir(paths.SnapshotStatePath) != filepath.Dir(paths.LockPath) {
		return result, errors.New("baseline import requires the fixed snapshot reload receipt path")
	}
	rows, err := parseOnboardingStatic(contents, false)
	if err != nil {
		return result, err
	}
	// Empty is a separate, explicit setup choice, not an accidental empty upload.
	if len(rows) == 0 && !allowEmpty {
		return result, errors.New("baseline upload contains no entries; choose empty setup explicitly")
	}
	mode := options.Mode
	if mode == "" {
		mode = "create"
	}
	if mode != "create" && mode != "merge" && mode != "replace" {
		return result, errors.New("unknown baseline import mode")
	}
	contents = bytes.Clone(contents)
	err = WithProcessLock(paths.LockPath, func() error {
		if err := prepareOnboardingDirectory(paths); err != nil {
			return err
		}
		current, readErr := readBoundedRegular(paths.BaselinePath, MaxBaselineImportBytes)
		exists := readErr == nil
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		var existingRows []snapshotRow
		if exists {
			var err error
			existingRows, err = parseOnboardingStatic(current, true)
			if err != nil {
				return err
			}
			result.Rows, result.SHA256 = len(existingRows), onboardingDigest(current)
			if bytes.Equal(current, contents) {
				return nil
			}
			if mode == "create" {
				return ErrBaselineConfirmationRequired
			}
			if options.ExpectedSHA256 != result.SHA256 {
				return ErrBaselineChanged
			}
		} else if options.ExpectedSHA256 != "" || mode != "create" {
			return ErrBaselineChanged
		}
		if mode == "merge" {
			merged := append([]snapshotRow(nil), existingRows...)
			seen := make(map[string]bool, len(merged))
			for _, row := range merged {
				seen[snapshotKey(row)] = true
			}
			for _, row := range rows {
				if !seen[snapshotKey(row)] {
					merged = append(merged, row)
					seen[snapshotKey(row)] = true
				}
			}
			if len(merged) > maxPrivateSnapshotRows {
				return errors.New("merged baseline exceeds input method capacity")
			}
			if len(merged) == len(existingRows) {
				return nil
			}
			rows, contents = merged, encodeOnboardingStatic(merged)
		}
		if exists {
			result.BackupName, err = backupOnboardingBytes(paths, "baseline", current)
			if err != nil {
				return err
			}
		} else {
			// First setup must retain the original small private vocabulary before
			// normal sync later replaces its generated destination. The separate
			// legacy Store importer can preserve its unique counts and pins.
			result.LegacyBackupName, result.LegacyRows, err = backupOnboardingLegacySnapshot(paths)
			if err != nil {
				return err
			}
		}
		// Invalidate the previous engine ACK before publishing a changed
		// baseline. The old generated private.tsv can otherwise still match its
		// old receipt and make first-setup activation look complete. If this
		// write fails the original baseline stays in place; if publication later
		// fails, a normal sync can safely obtain a fresh ACK for the old baseline.
		if _, err := writeAtomicPrivateFile(paths.SnapshotStatePath, []byte("baseline-pending\n")); err != nil {
			return err
		}
		if exists {
			result.Changed, err = writeAtomicPrivateFile(paths.BaselinePath, contents)
		} else {
			err = publishOnboardingNew(paths.BaselinePath, contents)
			result.Changed = err == nil
		}
		if err == nil {
			result.Rows, result.SHA256 = len(rows), onboardingDigest(contents)
		}
		return err
	})
	return result, err
}

// ExportBaseline returns only the static baseline, never the generated private
// snapshot or learned database. The caller explicitly requested a download and
// must not include its content in logs. The process lock is acquired internally.
func ExportBaseline(paths Paths) (contents []byte, result BaselineExportResult, err error) {
	if err := validateOnboardingPaths(paths); err != nil {
		return nil, result, err
	}
	err = WithProcessLock(paths.LockPath, func() error {
		if !bridgePathComponentsOK(filepath.Dir(paths.BaselinePath), true) {
			return errors.New("baseline directory is unavailable or redirected")
		}
		current, err := readBoundedRegular(paths.BaselinePath, MaxBaselineImportBytes)
		if err != nil {
			return err
		}
		rows, err := parseOnboardingStatic(current, true)
		if err != nil {
			return err
		}
		contents = current
		if bytes.HasPrefix(current, []byte(strings.TrimSuffix(generatedSnapshotHeader, "\n"))) {
			contents = encodeOnboardingStatic(rows)
		}
		result = BaselineExportResult{Rows: len(rows), SHA256: onboardingDigest(current), ContentSHA256: onboardingDigest(contents)}
		return nil
	})
	return contents, result, err
}
