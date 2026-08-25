// SPDX-License-Identifier: Apache-2.0
package mobilecore

import (
	"bufio"
	"bytes"
	"context"
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
	mobileSnapshotHeader = "phrase\tpinyin\tsource\tuse_count\tpinned\n"
	maximumSnapshotRows  = 100000
	maximumSnapshotBytes = 64 << 20
)

type SnapshotReport struct {
	Generation        uint64
	Rows              int
	Changed           bool
	RollbackAvailable bool
}

type mobileSnapshotRow struct {
	text        string
	pinyin      string
	useCount    uint64
	pinned      bool
	lastUsedDay int64
}

func validSnapshotText(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == '\t' || character == '\n' || character == '\r' ||
			(character >= 0x202a && character <= 0x202e) || (character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}

func validSnapshotPinyin(value string) bool {
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

func snapshotRows(snapshot localstore.Snapshot) ([]mobileSnapshotRow, error) {
	rows := make([]mobileSnapshotRow, 0, len(snapshot.Phrases))
	seen := make(map[string]struct{}, len(snapshot.Phrases))
	for _, phrase := range snapshot.Phrases {
		if phrase.Deleted {
			continue
		}
		pinyin := protocol.CanonicalPinyin(phrase.Pinyin)
		if phrase.UseCount == 0 || phrase.LastUsedDay < 0 ||
			!validSnapshotText(phrase.Text) || protocol.CanonicalPhrase(phrase.Text) == "" ||
			!validSnapshotPinyin(pinyin) {
			return nil, errors.New("mobile snapshot source contains an invalid active row")
		}
		key := protocol.CanonicalPhrase(phrase.Text) + "\x00" + pinyin
		if _, duplicate := seen[key]; duplicate {
			return nil, errors.New("mobile snapshot source contains a duplicate identity")
		}
		seen[key] = struct{}{}
		rows = append(rows, mobileSnapshotRow{
			text: phrase.Text, pinyin: pinyin, useCount: phrase.UseCount,
			pinned: phrase.Pinned, lastUsedDay: phrase.LastUsedDay,
		})
	}
	sort.Slice(rows, func(left, right int) bool {
		if rows[left].pinned != rows[right].pinned {
			return rows[left].pinned
		}
		if rows[left].lastUsedDay != rows[right].lastUsedDay {
			return rows[left].lastUsedDay > rows[right].lastUsedDay
		}
		if rows[left].useCount != rows[right].useCount {
			return rows[left].useCount > rows[right].useCount
		}
		if rows[left].pinyin != rows[right].pinyin {
			return rows[left].pinyin < rows[right].pinyin
		}
		return rows[left].text < rows[right].text
	})
	if len(rows) > maximumSnapshotRows {
		rows = rows[:maximumSnapshotRows]
	}
	return rows, nil
}

func encodeMobileSnapshot(rows []mobileSnapshotRow) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString(mobileSnapshotHeader)
	for _, row := range rows {
		if !validSnapshotText(row.text) || !validSnapshotPinyin(row.pinyin) || row.useCount == 0 || row.lastUsedDay < 0 {
			return nil, errors.New("mobile snapshot contains an invalid row")
		}
		source := "synced_learning"
		if row.lastUsedDay > 0 {
			source = fmt.Sprintf("synced_learning@%d", row.lastUsedDay)
		}
		fmt.Fprintf(&output, "%s\t%s\t%s\t%d\t%t\n",
			row.text, row.pinyin, source, row.useCount, row.pinned)
		if output.Len() > maximumSnapshotBytes {
			return nil, errors.New("mobile snapshot exceeds size limit")
		}
	}
	contents := output.Bytes()
	if err := validateMobileSnapshot(contents); err != nil {
		return nil, err
	}
	return contents, nil
}

func validLearningSource(value string) bool {
	if value == "synced_learning" {
		return true
	}
	const prefix = "synced_learning@"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	day := strings.TrimPrefix(value, prefix)
	if day == "" {
		return false
	}
	for _, character := range []byte(day) {
		if character < '0' || character > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseInt(day, 10, 64)
	return err == nil && parsed > 0
}

func validateMobileSnapshot(contents []byte) error {
	if len(contents) == 0 || len(contents) > maximumSnapshotBytes ||
		!bytes.HasSuffix(contents, []byte{'\n'}) {
		return errors.New("mobile snapshot framing is invalid")
	}
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	scanner.Buffer(make([]byte, 2048), 2048)
	if !scanner.Scan() || scanner.Text()+"\n" != mobileSnapshotHeader {
		return errors.New("mobile snapshot header is invalid")
	}
	seen := make(map[string]struct{})
	rows := 0
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 5 || !validSnapshotText(fields[0]) ||
			!validSnapshotPinyin(fields[1]) ||
			protocol.CanonicalPinyin(fields[1]) != fields[1] ||
			!validLearningSource(fields[2]) {
			return errors.New("mobile snapshot row is invalid")
		}
		count, countErr := strconv.ParseUint(fields[3], 10, 64)
		if countErr != nil || count == 0 || (fields[4] != "true" && fields[4] != "false") {
			return errors.New("mobile snapshot metadata is invalid")
		}
		identity := protocol.CanonicalPhrase(fields[0]) + "\x00" + fields[1]
		if _, duplicate := seen[identity]; duplicate {
			return errors.New("mobile snapshot identity is duplicated")
		}
		seen[identity] = struct{}{}
		rows++
		if rows > maximumSnapshotRows {
			return errors.New("mobile snapshot exceeds row limit")
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("mobile snapshot scan failed")
	}
	return nil
}

func readPrivateRegular(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > maximumSnapshotBytes {
		return nil, errors.New("private snapshot is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("private snapshot changed while opening")
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximumSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maximumSnapshotBytes {
		return nil, errors.New("private snapshot exceeds size limit")
	}
	return contents, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func writeAtomicPrivate(path string, contents []byte) error {
	if err := validatePrivatePath(path, "snapshot"); err != nil {
		return err
	}
	if len(contents) > maximumSnapshotBytes {
		return errors.New("private snapshot exceeds size limit")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".yunpin-snapshot-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(contents)
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func snapshotRollbackPath(path string) string {
	return path + ".rollback"
}

func validatedSnapshotPresent(path string) bool {
	contents, err := readPrivateRegular(path)
	return err == nil && validateMobileSnapshot(contents) == nil
}

func publishSnapshot(ctx context.Context, store *localstore.Store, path string) (SnapshotReport, error) {
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return SnapshotReport{}, err
	}
	rows, err := snapshotRows(snapshot)
	if err != nil {
		return SnapshotReport{}, err
	}
	contents, err := encodeMobileSnapshot(rows)
	if err != nil {
		return SnapshotReport{}, err
	}
	report := SnapshotReport{
		Generation:        snapshot.Generation,
		Rows:              len(rows),
		RollbackAvailable: validatedSnapshotPresent(snapshotRollbackPath(path)),
	}
	current, err := readPrivateRegular(path)
	switch {
	case err == nil:
		if bytes.Equal(current, contents) {
			return report, nil
		}
		if validateMobileSnapshot(current) == nil {
			if err := writeAtomicPrivate(snapshotRollbackPath(path), current); err != nil {
				return SnapshotReport{}, fmt.Errorf("retain previous private snapshot: %w", err)
			}
			report.RollbackAvailable = true
		}
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return SnapshotReport{}, err
	}
	if err := writeAtomicPrivate(path, contents); err != nil {
		return SnapshotReport{}, err
	}
	report.Changed = true
	return report, nil
}

func rollbackSnapshot(path string) error {
	contents, err := readPrivateRegular(snapshotRollbackPath(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("no retained snapshot rollback is available")
		}
		return err
	}
	if err := validateMobileSnapshot(contents); err != nil {
		return errors.New("retained snapshot rollback is invalid")
	}
	return writeAtomicPrivate(path, contents)
}
