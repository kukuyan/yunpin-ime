// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
)

const (
	NativeEventVersion  = 1
	maxNativeEventBytes = 4096
	maxNativeBatch      = 256
	maxNativeSpoolFiles = 2048
	maxNativeSpoolBytes = 8 << 20
)

type NativeSelectionEventV1 struct {
	Version int    `json:"version"`
	EventID string `json:"event_id"`
	Phrase  string `json:"phrase"`
	Pinyin  string `json:"pinyin"`
}

type NativeEventSummary struct {
	Consumed  int
	Duplicate int
	LocalOnly int
}

func validNativePhrase(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) {
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

func validNativePinyin(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || character == ' ' || character == '\'' {
			continue
		}
		return false
	}
	return true
}

func decodeNativeEvent(file *os.File) (NativeSelectionEventV1, error) {
	decoder := json.NewDecoder(io.LimitReader(file, maxNativeEventBytes+1))
	decoder.DisallowUnknownFields()
	var event NativeSelectionEventV1
	if err := decoder.Decode(&event); err != nil {
		return NativeSelectionEventV1{}, errors.New("invalid native selection event")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return NativeSelectionEventV1{}, errors.New("native selection event contains trailing data")
	}
	if event.Version != NativeEventVersion || !validNativeEventID(event.EventID) ||
		!validNativePhrase(event.Phrase) || !validNativePinyin(event.Pinyin) {
		return NativeSelectionEventV1{}, errors.New("native selection event fields are invalid")
	}
	event.Pinyin = protocol.CanonicalPinyin(event.Pinyin)
	if !validNativePinyin(event.Pinyin) {
		return NativeSelectionEventV1{}, errors.New("native selection event Pinyin cannot be canonicalized")
	}
	return event, nil
}

func validNativeEventID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func consumeNativeEvents(ctx context.Context, directory string, store *localstore.Store, localOnly map[string]struct{}, limit int) (NativeEventSummary, error) {
	if directory == "" || !filepath.IsAbs(directory) || store == nil || limit < 1 || limit > maxNativeBatch {
		return NativeEventSummary{}, errors.New("native event consumer configuration is invalid")
	}
	directoryInfo, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return NativeEventSummary{}, nil
	}
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 ||
		!privateDirectoryPermissionsOK(directory, directoryInfo) {
		return NativeEventSummary{}, errors.New("native event spool must be a private regular directory")
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return NativeEventSummary{}, fmt.Errorf("open native event spool: %w", err)
	}
	if !openedPrivateFilePermissionsOK(directory, directoryHandle, true) {
		directoryHandle.Close()
		return NativeEventSummary{}, errors.New("native event spool path and opened handle do not identify the same private directory")
	}
	openedDirectory, statErr := directoryHandle.Stat()
	if statErr != nil || !os.SameFile(directoryInfo, openedDirectory) {
		directoryHandle.Close()
		return NativeEventSummary{}, errors.New("native event spool changed during validated open")
	}
	// Read one item beyond the payload ceiling while allowing the producer's
	// single excluded lock file. This detects legacy overflows without an
	// unbounded directory walk.
	entries, err := directoryHandle.ReadDir(maxNativeSpoolFiles + 2)
	closeDirectoryErr := directoryHandle.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return NativeEventSummary{}, fmt.Errorf("read native event spool: %w", err)
	}
	if closeDirectoryErr != nil {
		return NativeEventSummary{}, closeDirectoryErr
	}
	// Current producers serialize their quota check with .spool.lock, which is
	// strictly validated but excluded from the 2048-file/8-MiB payload quota.
	// Older producers could race and leave one extra event. Keep discovery
	// bounded, but allow a valid
	// overflow window to drain instead of permanently wedging the agent.
	// Unknown or invalid files are never removed by this recovery path.
	payloadFiles := 0
	var spoolBytes int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return NativeEventSummary{}, err
		}
		if entry.Name() == ".spool.lock" {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
				!privateFilePermissionsOK(filepath.Join(directory, entry.Name()), info) || info.Size() != 0 {
				return NativeEventSummary{}, errors.New("native event spool lock must be an empty private regular file")
			}
			continue
		}
		payloadFiles++
		if info.Mode().IsRegular() {
			spoolBytes += info.Size()
		}
	}
	overflow := payloadFiles > maxNativeSpoolFiles || spoolBytes > maxNativeSpoolBytes
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
	var summary NativeEventSummary
	for _, entry := range entries {
		if summary.Consumed+summary.Duplicate >= limit || ctx.Err() != nil {
			break
		}
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".json" || !validNativeEventID(strings.TrimSuffix(name, ".json")) {
			continue
		}
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil {
			return summary, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !privateFilePermissionsOK(path, info) ||
			info.Size() < 2 || info.Size() > maxNativeEventBytes {
			return summary, errors.New("native selection event must be a bounded regular file")
		}
		file, err := os.Open(path)
		if err != nil {
			return summary, err
		}
		if !openedPrivateFilePermissionsOK(path, file, false) {
			file.Close()
			return summary, errors.New("native selection event path and opened handle do not identify the same private file")
		}
		opened, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			file.Close()
			return summary, errors.New("native selection event changed during validated open")
		}
		event, decodeErr := decodeNativeEvent(file)
		closeErr := file.Close()
		if decodeErr != nil {
			return summary, decodeErr
		}
		if closeErr != nil {
			return summary, closeErr
		}
		if event.EventID != strings.TrimSuffix(name, ".json") {
			return summary, errors.New("native selection event ID does not match its filename")
		}
		_, isLocalOnly := localOnly[protocol.CanonicalPhrase(event.Phrase)]
		var result localstore.NativeSelectionResult
		if isLocalOnly {
			result, err = store.RecordNativeSelectionReceipt(ctx, event.EventID)
		} else {
			result, err = store.RecordNativeSelection(ctx, localstore.NativeSelection{
				EventID: event.EventID,
				Phrase:  localstore.Phrase{Text: event.Phrase, Pinyin: event.Pinyin, Source: "native_selection"},
			})
		}
		if err != nil {
			return summary, err
		}
		if err := removePrivateFile(path); err != nil {
			return summary, fmt.Errorf("remove consumed native event: %w", err)
		}
		if result.Duplicate {
			summary.Duplicate++
		} else {
			summary.Consumed++
			if isLocalOnly {
				summary.LocalOnly++
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	if overflow && summary.Consumed+summary.Duplicate == 0 {
		return summary, errors.New("native event spool exceeds quota and its bounded recovery window contains no consumable event")
	}
	return summary, nil
}

// EncodeNativeSelectionEventV1 is used by platform adapters after draining the
// fixed native queue. It emits a canonical bounded JSON document; adapters
// must stage it mode 0600, fsync and atomically rename within the incoming
// directory before returning the queue slot to their host loop.
func EncodeNativeSelectionEventV1(event NativeSelectionEventV1) ([]byte, error) {
	if event.Version != NativeEventVersion || !validNativeEventID(event.EventID) ||
		!validNativePhrase(event.Phrase) || !validNativePinyin(event.Pinyin) {
		return nil, errors.New("native selection event fields are invalid")
	}
	event.Pinyin = protocol.CanonicalPinyin(event.Pinyin)
	if !validNativePinyin(event.Pinyin) {
		return nil, errors.New("native selection event Pinyin cannot be canonicalized")
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(event); err != nil {
		return nil, err
	}
	if encoded.Len() > maxNativeEventBytes {
		return nil, errors.New("native selection event exceeds size limit")
	}
	return encoded.Bytes(), nil
}
