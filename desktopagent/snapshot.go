// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
)

const (
	privateSnapshotHeader  = "phrase\tpinyin\tsource\tuse_count\tpinned\n"
	maxPrivateSnapshotRows = 100000
	maxBaselineBytes       = 64 << 20
)

type SnapshotSummary struct {
	Generation   uint64
	BaselineRows int
	LearnedRows  int
	TotalRows    int
	Changed      bool
	digest       [sha256.Size]byte
}

type snapshotRow struct {
	Phrase      string
	Pinyin      string
	Source      string
	UseCount    uint64
	Pinned      bool
	LastUsedDay int64
}

func validBaselinePinyin(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || character == ' ' || character == '\'' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validSnapshotSource(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

func snapshotKey(row snapshotRow) string {
	return protocol.CanonicalPhrase(row.Phrase) + "\x00" + protocol.CanonicalPinyin(row.Pinyin)
}

func ensurePrivateDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("private directory path must be absolute")
	}
	if err := makePrivateDirectory(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privateDirectoryPermissionsOK(path, info) {
		return errors.New("directory must be owned by the current user with private permissions")
	}
	return nil
}

func readBoundedRegular(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!privateFilePermissionsOK(path, info) || info.Size() > maximum {
		return nil, errors.New("file must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if !openedPrivateFilePermissionsOK(path, file, false) {
		return nil, errors.New("opened file handle does not match the validated private path")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("file changed during validated open")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, errors.New("file exceeds size limit")
	}
	return contents, nil
}

func parseBaselineBytes(contents []byte) ([]snapshotRow, error) {
	reader := bufio.NewReader(bytes.NewReader(contents))
	header, err := reader.ReadString('\n')
	if err != nil || strings.TrimSuffix(strings.TrimSuffix(header, "\n"), "\r") != strings.TrimSuffix(privateSnapshotHeader, "\n") {
		return nil, errors.New("baseline snapshot header is invalid")
	}
	rows := make([]snapshotRow, 0, maxPrivateSnapshotRows)
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 2048)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 || !validNativePhrase(fields[0]) || !validBaselinePinyin(fields[1]) || !validSnapshotSource(fields[2]) {
			return nil, errors.New("baseline snapshot contains an invalid row")
		}
		count, countErr := strconv.ParseUint(fields[3], 10, 64)
		pinned, pinnedOK := parsePinned(fields[4])
		if countErr != nil || count == 0 || !pinnedOK {
			return nil, errors.New("baseline snapshot contains invalid metadata")
		}
		row := snapshotRow{Phrase: fields[0], Pinyin: fields[1], Source: fields[2], UseCount: count, Pinned: pinned}
		if _, exists := seen[snapshotKey(row)]; exists {
			return nil, errors.New("baseline snapshot contains a duplicate phrase identity")
		}
		seen[snapshotKey(row)] = struct{}{}
		rows = append(rows, row)
		if len(rows) > maxPrivateSnapshotRows {
			return nil, errors.New("baseline snapshot exceeds the private snapshot capacity")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

func parsePinned(value string) (bool, bool) {
	switch strings.ToLower(value) {
	case "0", "false", "no", "":
		return false, true
	case "1", "true", "yes", "pinned":
		return true, true
	default:
		return false, false
	}
}

func parseBaseline(path string) ([]snapshotRow, error) {
	if path == "" {
		return nil, nil
	}
	contents, err := readBoundedRegular(path, maxBaselineBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read baseline snapshot: %w", err)
	}
	return parseBaselineBytes(contents)
}

// ensureBaseline preserves a pre-sync reviewed private.tsv as immutable static
// vocabulary on first activation. It never imports those rows into localstore
// or the sync outbox. Once baseline.tsv exists it is never replaced implicitly.
func ensureBaseline(baselinePath, existingSnapshotPath string) (bool, error) {
	if _, err := os.Lstat(baselinePath); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if _, err := os.Lstat(existingSnapshotPath); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	contents, err := readBoundedRegular(existingSnapshotPath, maxBaselineBytes)
	if err != nil {
		return false, fmt.Errorf("read existing private snapshot before baseline migration: %w", err)
	}
	if _, err := parseBaselineBytes(contents); err != nil {
		return false, fmt.Errorf("validate existing private snapshot before baseline migration: %w", err)
	}
	return writeAtomicPrivateFile(baselinePath, contents)
}

func mergeSnapshotRows(baseline []snapshotRow, learned []localstore.Phrase) ([]snapshotRow, int) {
	rows := append([]snapshotRow(nil), baseline...)
	baselinePhrases := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		baselinePhrases[protocol.CanonicalPhrase(row.Phrase)] = struct{}{}
	}
	learnedOnly := make(map[string]snapshotRow)
	for _, phrase := range learned {
		pinyin := protocol.CanonicalPinyin(phrase.Pinyin)
		if phrase.Deleted || phrase.UseCount == 0 || !validNativePhrase(phrase.Text) || !validNativePinyin(pinyin) {
			continue
		}
		row := snapshotRow{Phrase: phrase.Text, Pinyin: pinyin, Source: "synced_learning", UseCount: phrase.UseCount, Pinned: phrase.Pinned, LastUsedDay: phrase.LastUsedDay}
		key := snapshotKey(row)
		if _, found := baselinePhrases[protocol.CanonicalPhrase(row.Phrase)]; found {
			// The reviewed static baseline is byte-semantic input, not learned
			// state. A stale pre-activation or remote overlay must never rewrite
			// its source/count/pin metadata or append another pronunciation. This
			// matches the phrase-only local boundary used during event ingestion.
			continue
		}
		learnedOnly[key] = row
	}
	additions := make([]snapshotRow, 0, len(learnedOnly))
	for _, row := range learnedOnly {
		additions = append(additions, row)
	}
	sort.Slice(additions, func(left, right int) bool {
		if additions[left].Pinned != additions[right].Pinned {
			return additions[left].Pinned
		}
		if additions[left].LastUsedDay != additions[right].LastUsedDay {
			return additions[left].LastUsedDay > additions[right].LastUsedDay
		}
		if additions[left].UseCount != additions[right].UseCount {
			return additions[left].UseCount > additions[right].UseCount
		}
		if additions[left].Pinyin != additions[right].Pinyin {
			return additions[left].Pinyin < additions[right].Pinyin
		}
		return additions[left].Phrase < additions[right].Phrase
	})
	capacity := maxPrivateSnapshotRows - len(rows)
	if capacity < len(additions) {
		additions = additions[:capacity]
	}
	rows = append(rows, additions...)
	return rows, len(additions)
}

func encodeSnapshot(rows []snapshotRow) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString(privateSnapshotHeader)
	for _, row := range rows {
		if !validNativePhrase(row.Phrase) || !validBaselinePinyin(row.Pinyin) ||
			!validSnapshotSource(row.Source) || row.UseCount == 0 || row.LastUsedDay < 0 {
			return nil, errors.New("snapshot row cannot be encoded safely")
		}
		source := row.Source
		if row.LastUsedDay > 0 {
			if source != "synced_learning" {
				return nil, errors.New("only synchronized learning rows may encode recency")
			}
			source = fmt.Sprintf("synced_learning@%d", row.LastUsedDay)
		}
		fmt.Fprintf(&output, "%s\t%s\t%s\t%d\t%t\n", row.Phrase, row.Pinyin, source, row.UseCount, row.Pinned)
	}
	return output.Bytes(), nil
}

func writeAtomicPrivateFile(path string, contents []byte) (bool, error) {
	if path == "" || !filepath.IsAbs(path) {
		return false, errors.New("snapshot destination must be absolute")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("snapshot destination must be a regular file, not a symlink")
		}
		if !privateFilePermissionsOK(path, info) {
			return false, errors.New("snapshot destination permissions are not private")
		}
		if info.Size() > maxBaselineBytes {
			return false, errors.New("snapshot destination exceeds size limit")
		}
		current, err := readBoundedRegular(path, maxBaselineBytes)
		if err != nil {
			return false, err
		}
		if bytes.Equal(current, contents) {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".private.tsv.*.tmp")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = protectPrivateFile(temporary); err == nil {
		_, err = temporary.Write(contents)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return false, err
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return false, err
	}
	return true, syncParentDirectory(filepath.Dir(path))
}

func rebuildPrivateSnapshot(ctx context.Context, store *localstore.Store, baselinePath, snapshotPath string) (SnapshotSummary, error) {
	if _, err := ensureBaseline(baselinePath, snapshotPath); err != nil {
		return SnapshotSummary{}, err
	}
	baseline, err := parseBaseline(baselinePath)
	if err != nil {
		return SnapshotSummary{}, err
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return SnapshotSummary{}, err
	}
	rows, learnedRows := mergeSnapshotRows(baseline, snapshot.Phrases)
	contents, err := encodeSnapshot(rows)
	if err != nil {
		return SnapshotSummary{}, err
	}
	changed, err := writeAtomicPrivateFile(snapshotPath, contents)
	return SnapshotSummary{
		Generation: snapshot.Generation, BaselineRows: len(baseline), LearnedRows: learnedRows,
		TotalRows: len(rows), Changed: changed, digest: sha256.Sum256(contents),
	}, err
}

func snapshotReloadMarker(digest [sha256.Size]byte) []byte {
	return []byte("v1\t" + hex.EncodeToString(digest[:]) + "\n")
}

// snapshotReloadPending compares the generated snapshot with the last digest
// whose platform reload completed. This closes the crash window between the
// atomic private.tsv replacement and the reload notification: a later run
// retries the reload even when private.tsv itself no longer changes.
func snapshotReloadPending(path string, digest [sha256.Size]byte) (bool, error) {
	if path == "" || !filepath.IsAbs(path) {
		return false, errors.New("snapshot reload state path must be absolute")
	}
	contents, err := readBoundedRegular(path, 256)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read snapshot reload state: %w", err)
	}
	return !bytes.Equal(contents, snapshotReloadMarker(digest)), nil
}

func markSnapshotReloaded(path string, digest [sha256.Size]byte) error {
	_, err := writeAtomicPrivateFile(path, snapshotReloadMarker(digest))
	return err
}
