// SPDX-License-Identifier: Apache-2.0
package replaylab

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	EventVersionV1 = "yunpin.replay.event.v1"
	MaxEventBytes  = 8 * 1024
	MaxCandidates  = 8
	MaxEpisodes    = 1_000_000

	maxInputBytes     = 512
	maxPinyinBytes    = 512
	maxCandidateBytes = 256
	maxCommitBytes    = 2048
	maxEditCount      = 1024
	maxDropCount      = 1 << 40
)

type EventType string

const (
	EventComposition EventType = "composition_snapshot"
	EventSelect      EventType = "select"
	EventCommit      EventType = "commit"
	EventBackspace   EventType = "backspace"
	EventDelete      EventType = "delete"
	EventAbort       EventType = "abort"
	EventPause       EventType = "pause"
	EventResume      EventType = "resume"
	EventDropCount   EventType = "drop_count"
)

type CandidateV1 struct {
	Text         string `json:"text"`
	IsCorrection bool   `json:"is_correction"`
	Highlighted  bool   `json:"highlighted"`
}

type CompositionV1 struct {
	RawInput           string        `json:"raw_input"`
	NormalizedPinyin   string        `json:"normalized_pinyin"`
	CaretByte          int           `json:"caret_byte"`
	ExactPathAvailable bool          `json:"exact_path_available"`
	Candidates         []CandidateV1 `json:"candidates"`
}

type SelectionV1 struct {
	// Rank is one-based, matching the number shown in the candidate window.
	Rank int    `json:"rank"`
	Text string `json:"text"`
}

type CommitV1 struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type FinalTextV1 struct {
	Text       string `json:"text"`
	Scope      string `json:"scope"`
	Confidence string `json:"confidence"`
}

// EventV1 is the bounded interchange record between a native IME sidecar (or
// a dedicated experiment harness) and Replay Lab. It is not a system-wide
// keyboard event format.
type EventV1 struct {
	Version     string         `json:"version"`
	SessionID   string         `json:"session_id"`
	EpisodeID   string         `json:"episode_id"`
	Seq         uint64         `json:"seq"`
	MonotonicUS uint64         `json:"monotonic_us"`
	UTC         string         `json:"utc"`
	Type        EventType      `json:"type"`
	Composition *CompositionV1 `json:"composition,omitempty"`
	Selection   *SelectionV1   `json:"selection,omitempty"`
	Commit      *CommitV1      `json:"commit,omitempty"`
	EditCount   uint32         `json:"edit_count,omitempty"`
	DropCount   uint64         `json:"drop_count,omitempty"`
	FinalText   *FinalTextV1   `json:"final_text,omitempty"`
}

func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate random id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func DecodeEventV1(line []byte) (EventV1, error) {
	if len(line) == 0 {
		return EventV1{}, errors.New("empty event")
	}
	if len(line) > MaxEventBytes {
		return EventV1{}, fmt.Errorf("event is %d bytes; maximum is %d", len(line), MaxEventBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	var event EventV1
	if err := dec.Decode(&event); err != nil {
		return EventV1{}, fmt.Errorf("decode event v1: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return EventV1{}, errors.New("event contains trailing JSON value")
		}
		return EventV1{}, fmt.Errorf("decode trailing data: %w", err)
	}
	if err := event.Validate(); err != nil {
		return EventV1{}, err
	}
	return event, nil
}

func EncodeEventV1(event EventV1) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode event v1: %w", err)
	}
	if len(encoded) > MaxEventBytes {
		return nil, fmt.Errorf("encoded event is %d bytes; maximum is %d", len(encoded), MaxEventBytes)
	}
	return encoded, nil
}

func (event EventV1) Validate() error {
	if event.Version != EventVersionV1 {
		return fmt.Errorf("unsupported event version %q", event.Version)
	}
	if !validID(event.SessionID) || !validID(event.EpisodeID) {
		return errors.New("session_id and episode_id must be 32 lowercase hexadecimal characters")
	}
	if event.Seq == 0 {
		return errors.New("seq must start at 1")
	}
	if !isCanonicalUTC(event.UTC) {
		return errors.New("utc must be canonical RFC3339Nano in UTC (ending in Z)")
	}
	if event.Composition != nil {
		if err := event.Composition.validate(); err != nil {
			return fmt.Errorf("composition: %w", err)
		}
	}
	if event.Selection != nil {
		if event.Composition == nil {
			return errors.New("selection requires a composition snapshot")
		}
		if event.Selection.Rank < 1 || event.Selection.Rank > len(event.Composition.Candidates) {
			return errors.New("selection rank is outside the candidate snapshot")
		}
		if event.Selection.Text != event.Composition.Candidates[event.Selection.Rank-1].Text {
			return errors.New("selection text does not match its candidate rank")
		}
	}
	if event.Commit != nil {
		if err := validateText("commit text", event.Commit.Text, maxCommitBytes, false); err != nil {
			return err
		}
		switch event.Commit.Source {
		case "candidate", "raw", "prediction", "unknown":
		default:
			return fmt.Errorf("unsupported commit source %q", event.Commit.Source)
		}
	}
	if event.FinalText != nil {
		if err := event.FinalText.validate(); err != nil {
			return err
		}
	}

	if err := event.validateTypeShape(); err != nil {
		return err
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("measure event: %w", err)
	}
	if len(encoded) > MaxEventBytes {
		return fmt.Errorf("encoded event is %d bytes; maximum is %d", len(encoded), MaxEventBytes)
	}
	return nil
}

func (event EventV1) validateTypeShape() error {
	unexpected := func(allowComposition, allowSelection, allowCommit, allowFinal bool) error {
		if (!allowComposition && event.Composition != nil) ||
			(!allowSelection && event.Selection != nil) ||
			(!allowCommit && event.Commit != nil) ||
			(!allowFinal && event.FinalText != nil) {
			return fmt.Errorf("event type %q contains an unsupported payload", event.Type)
		}
		return nil
	}

	switch event.Type {
	case EventComposition:
		if event.Composition == nil || event.Selection != nil || event.Commit != nil || event.FinalText != nil || event.EditCount != 0 || event.DropCount != 0 {
			return errors.New("composition_snapshot requires only composition")
		}
	case EventSelect:
		if event.Composition == nil || event.Selection == nil || event.Commit != nil || event.FinalText != nil || event.EditCount != 0 || event.DropCount != 0 {
			return errors.New("select requires composition and selection only")
		}
	case EventCommit:
		if event.Composition == nil || event.Commit == nil || event.FinalText == nil || event.EditCount != 0 || event.DropCount != 0 {
			return errors.New("commit requires composition, commit, and final_text")
		}
		if event.Commit.Text != event.FinalText.Text {
			return errors.New("commit text and final_text text must match")
		}
	case EventBackspace, EventDelete:
		if event.EditCount == 0 || event.EditCount > maxEditCount || event.DropCount != 0 {
			return fmt.Errorf("%s requires edit_count between 1 and %d", event.Type, maxEditCount)
		}
		if err := unexpected(true, false, false, false); err != nil {
			return err
		}
	case EventAbort:
		if event.EditCount != 0 || event.DropCount != 0 {
			return errors.New("abort does not accept counters")
		}
		if err := unexpected(true, false, false, false); err != nil {
			return err
		}
	case EventPause, EventResume:
		if event.EditCount != 0 || event.DropCount != 0 {
			return fmt.Errorf("%s does not accept counters", event.Type)
		}
		if err := unexpected(false, false, false, false); err != nil {
			return err
		}
	case EventDropCount:
		if event.DropCount == 0 || event.DropCount > maxDropCount || event.EditCount != 0 {
			return fmt.Errorf("drop_count requires drop_count between 1 and %d", uint64(maxDropCount))
		}
		if err := unexpected(false, false, false, false); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported event type %q", event.Type)
	}
	return nil
}

func (composition CompositionV1) validate() error {
	if err := validateText("raw_input", composition.RawInput, maxInputBytes, true); err != nil {
		return err
	}
	if err := validateText("normalized_pinyin", composition.NormalizedPinyin, maxPinyinBytes, true); err != nil {
		return err
	}
	if composition.CaretByte < 0 || composition.CaretByte > len(composition.RawInput) {
		return errors.New("caret_byte is outside raw_input")
	}
	if composition.CaretByte > 0 && composition.CaretByte != len(composition.RawInput) && !utf8.RuneStart(composition.RawInput[composition.CaretByte]) {
		return errors.New("caret_byte splits a UTF-8 code point")
	}
	if len(composition.Candidates) > MaxCandidates {
		return fmt.Errorf("candidate count %d exceeds maximum %d", len(composition.Candidates), MaxCandidates)
	}
	highlighted := 0
	for index, candidate := range composition.Candidates {
		if err := validateText(fmt.Sprintf("candidate[%d].text", index), candidate.Text, maxCandidateBytes, false); err != nil {
			return err
		}
		if candidate.Highlighted {
			highlighted++
		}
	}
	if highlighted > 1 {
		return errors.New("at most one candidate may be highlighted")
	}
	return nil
}

func (final FinalTextV1) validate() error {
	if err := validateText("final_text.text", final.Text, maxCommitBytes, false); err != nil {
		return err
	}
	switch final.Scope {
	case "composition", "clause", "episode":
	default:
		return fmt.Errorf("unsupported final_text scope %q", final.Scope)
	}
	switch final.Confidence {
	case "observed", "user_confirmed", "inferred":
	default:
		return fmt.Errorf("unsupported final_text confidence %q", final.Confidence)
	}
	return nil
}

func validateText(name, value string, maxBytes int, allowEmpty bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s is %d bytes; maximum is %d", name, len(value), maxBytes)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s contains NUL", name)
	}
	return nil
}

func validID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func isCanonicalUTC(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC && parsed.Format(time.RFC3339Nano) == value
}
