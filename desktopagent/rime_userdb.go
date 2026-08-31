// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
)

const (
	maxRimeUserDBExportBytes = 64 << 20
	maxRimeUserDBRows        = 100000
	maxRimeUserDBLineBytes   = 2048
)

var rimeUserDBFiniteDecimal = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:e[+-]?[0-9]+)?$`)

var errRimeUserDBUnsupportedCode = errors.New("Rime userdb code uses a supported non-Pinyin syntax")

func canonicalRimeUserDBCode(value string) (string, error) {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.HasPrefix(value, " ") {
		return "", errors.New("Rime userdb code is invalid")
	}
	code := strings.TrimSuffix(value, " ")
	if code == "" || strings.HasSuffix(code, " ") {
		return "", errors.New("Rime userdb code has invalid trailing separators")
	}
	separator := false
	unsupported := false
	for index, character := range []byte(code) {
		switch {
		case character >= 'a' && character <= 'z':
			separator = false
		case character >= 0x21 && character <= 0x7e && character != '\'':
			// Rime may export rows owned by another translator, including
			// uppercase, digits, or visible ASCII symbols. Scan the entire code
			// before classifying it as unsupported: controls, non-ASCII bytes,
			// and malformed separators must still fail the complete snapshot.
			separator = false
			unsupported = true
		case character == ' ' || character == '\'':
			if index == 0 || separator {
				return "", errors.New("Rime userdb code has repeated separators")
			}
			separator = true
		default:
			return "", errors.New("Rime userdb code contains unsafe bytes")
		}
	}
	if separator {
		return "", errors.New("Rime userdb code ends with an invalid separator")
	}
	if unsupported {
		return "", errRimeUserDBUnsupportedCode
	}
	canonical := protocol.CanonicalPinyin(code)
	if !validNativePinyin(canonical) {
		return "", errors.New("Rime userdb code cannot be canonicalized")
	}
	return canonical, nil
}

func parseRimeUserDBMetadata(value string) (uint64, error) {
	parts := strings.Split(value, " ")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "c=") ||
		!strings.HasPrefix(parts[1], "d=") || !strings.HasPrefix(parts[2], "t=") {
		return 0, errors.New("Rime userdb metadata must be exactly c, d, and t")
	}
	commitsText := strings.TrimPrefix(parts[0], "c=")
	commits, err := strconv.ParseInt(commitsText, 10, 32)
	if err != nil || strconv.FormatInt(commits, 10) != commitsText {
		return 0, errors.New("Rime userdb commit count is not a canonical signed integer")
	}
	deeText := strings.TrimPrefix(parts[1], "d=")
	dee, err := strconv.ParseFloat(deeText, 64)
	if err != nil || !rimeUserDBFiniteDecimal.MatchString(deeText) || math.IsNaN(dee) || math.IsInf(dee, 0) || dee < 0 || dee > 10000 {
		return 0, errors.New("Rime userdb dynamic score is invalid")
	}
	tickText := strings.TrimPrefix(parts[2], "t=")
	tick, err := strconv.ParseUint(tickText, 10, 64)
	if err != nil || strconv.FormatUint(tick, 10) != tickText {
		return 0, errors.New("Rime userdb tick is not a canonical unsigned integer")
	}
	if commits <= 0 {
		// Negative commits are Rime deletion markers. This one-way learning bridge
		// treats them as a zero counter/reset and never emits a synchronized delete.
		return 0, nil
	}
	return uint64(commits), nil
}

func parseRimeUserDBExportBytes(contents []byte, localOnly map[string]struct{}) ([]localstore.RimeUserDBObservation, int, error) {
	if len(contents) > maxRimeUserDBExportBytes || !utf8.Valid(contents) || bytes.IndexByte(contents, 0) >= 0 {
		return nil, 0, errors.New("Rime userdb export is not bounded UTF-8 text")
	}
	reader := bufio.NewScanner(bytes.NewReader(contents))
	reader.Buffer(make([]byte, 1024), maxRimeUserDBLineBytes)
	observations := make([]localstore.RimeUserDBObservation, 0)
	seen := make(map[string]struct{})
	ignored := 0
	parsedRows := 0
	lineNumber := 0
	for reader.Scan() {
		lineNumber++
		line := strings.TrimSuffix(reader.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, 0, fmt.Errorf("Rime userdb row %d must contain exactly three tab-separated fields", lineNumber)
		}
		parsedRows++
		if parsedRows > maxRimeUserDBRows {
			return nil, 0, errors.New("Rime userdb export exceeds the row limit")
		}
		pinyin, codeErr := canonicalRimeUserDBCode(fields[0])
		if codeErr != nil && !errors.Is(codeErr, errRimeUserDBUnsupportedCode) {
			return nil, 0, fmt.Errorf("Rime userdb row %d: %w", lineNumber, codeErr)
		}
		if !validNativePhrase(fields[1]) || protocol.CanonicalPhrase(fields[1]) == "" {
			return nil, 0, fmt.Errorf("Rime userdb row %d contains an invalid phrase", lineNumber)
		}
		commits, err := parseRimeUserDBMetadata(fields[2])
		if err != nil {
			return nil, 0, fmt.Errorf("Rime userdb row %d: %w", lineNumber, err)
		}
		// Rime userdb exports may contain rows from non-Pinyin translators
		// (for example an uppercase or symbol code). They are valid local Rime
		// state but cannot be represented by YunPin's phrase identity. Ignore
		// only those fully validated rows instead of blocking every Pinyin row.
		if errors.Is(codeErr, errRimeUserDBUnsupportedCode) {
			ignored++
			continue
		}
		identity := protocol.CanonicalPhrase(fields[1]) + "\x00" + pinyin
		if _, duplicate := seen[identity]; duplicate {
			return nil, 0, fmt.Errorf("Rime userdb row %d duplicates a canonical phrase identity", lineNumber)
		}
		seen[identity] = struct{}{}
		_, private := localOnly[protocol.CanonicalPhrase(fields[1])]
		observations = append(observations, localstore.RimeUserDBObservation{
			Phrase:  localstore.Phrase{Text: fields[1], Pinyin: pinyin, Source: "rime_userdb"},
			Commits: commits, LocalOnly: private,
		})
	}
	if err := reader.Err(); err != nil {
		return nil, 0, fmt.Errorf("scan Rime userdb export: %w", err)
	}
	return observations, ignored, nil
}

func ingestRimeUserDBExport(ctx context.Context, path string, store *localstore.Store,
	localOnly map[string]struct{}) (localstore.RimeUserDBImportResult, error) {
	if path == "" || !filepath.IsAbs(path) || store == nil {
		return localstore.RimeUserDBImportResult{}, errors.New("Rime userdb export path and local store are required")
	}
	contents, err := readBoundedRegular(path, maxRimeUserDBExportBytes)
	if err != nil {
		return localstore.RimeUserDBImportResult{}, fmt.Errorf("read private Rime userdb export: %w", err)
	}
	observations, ignored, err := parseRimeUserDBExportBytes(contents, localOnly)
	if err != nil {
		return localstore.RimeUserDBImportResult{}, err
	}
	localOnlyPhrases := make([]string, 0, len(localOnly))
	for phrase := range localOnly {
		localOnlyPhrases = append(localOnlyPhrases, phrase)
	}
	result, err := store.ImportRimeUserDB(ctx, observations, localOnlyPhrases)
	result.Ignored += ignored
	return result, err
}
