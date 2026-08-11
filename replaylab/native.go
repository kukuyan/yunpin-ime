// SPDX-License-Identifier: Apache-2.0
package replaylab

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

const NativeEventVersionV1 = "yunpin.replay.native.v1"

type NativeFrameV1 struct {
	Version     string         `json:"version"`
	MonotonicUS uint64         `json:"monotonic_us"`
	UTCUnixUS   uint64         `json:"utc_unix_us"`
	Type        EventType      `json:"type"`
	Composition *CompositionV1 `json:"composition,omitempty"`
	Selection   *SelectionV1   `json:"selection,omitempty"`
	EditCount   uint32         `json:"edit_count,omitempty"`
	DropCount   uint64         `json:"drop_count,omitempty"`
	FinalText   string         `json:"final_text,omitempty"`
}

func DecodeNativeFrameV1(line []byte) (NativeFrameV1, error) {
	if len(line) == 0 || len(line) > MaxEventBytes {
		return NativeFrameV1{}, fmt.Errorf("native frame size %d is outside 1..%d", len(line), MaxEventBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var frame NativeFrameV1
	if err := decoder.Decode(&frame); err != nil {
		return NativeFrameV1{}, fmt.Errorf("decode native frame: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return NativeFrameV1{}, errors.New("native frame contains trailing JSON")
		}
		return NativeFrameV1{}, err
	}
	if err := frame.Validate(); err != nil {
		return NativeFrameV1{}, err
	}
	return frame, nil
}

func (frame NativeFrameV1) Validate() error {
	if frame.Version != NativeEventVersionV1 {
		return fmt.Errorf("unsupported native frame version %q", frame.Version)
	}
	if frame.UTCUnixUS == 0 {
		return errors.New("native utc_unix_us must be greater than zero")
	}
	if frame.Composition != nil {
		if err := frame.Composition.validate(); err != nil {
			return fmt.Errorf("native composition: %w", err)
		}
	}
	if frame.Selection != nil {
		if frame.Composition == nil || frame.Selection.Rank < 1 || frame.Selection.Rank > len(frame.Composition.Candidates) || frame.Selection.Text != frame.Composition.Candidates[frame.Selection.Rank-1].Text {
			return errors.New("native selection does not match its candidate snapshot")
		}
	}
	switch frame.Type {
	case EventComposition:
		if frame.Composition == nil || frame.Selection != nil || frame.EditCount != 0 || frame.DropCount != 0 || frame.FinalText != "" {
			return errors.New("native composition_snapshot has an invalid payload")
		}
	case EventSelect:
		if frame.Composition == nil || frame.Selection == nil || frame.EditCount != 0 || frame.DropCount != 0 || frame.FinalText != "" {
			return errors.New("native select has an invalid payload")
		}
	case EventCommit:
		if frame.Composition == nil || frame.Selection != nil || frame.EditCount != 0 || frame.DropCount != 0 {
			return errors.New("native commit has an invalid payload")
		}
		if err := validateText("native final_text", frame.FinalText, maxCommitBytes, false); err != nil {
			return err
		}
	case EventBackspace, EventDelete:
		if frame.Composition == nil || frame.Selection != nil || frame.EditCount == 0 || frame.EditCount > maxEditCount || frame.DropCount != 0 || frame.FinalText != "" {
			return fmt.Errorf("native %s has an invalid payload", frame.Type)
		}
	case EventAbort:
		if frame.Composition == nil || frame.Selection != nil || frame.EditCount != 0 || frame.DropCount != 0 || frame.FinalText != "" {
			return errors.New("native abort has an invalid payload")
		}
	case EventDropCount:
		if frame.Composition != nil || frame.Selection != nil || frame.EditCount != 0 || frame.DropCount == 0 || frame.DropCount > maxDropCount || frame.FinalText != "" {
			return errors.New("native drop_count has an invalid payload")
		}
	default:
		return fmt.Errorf("native frame type %q is unsupported", frame.Type)
	}
	return nil
}

type NativeEventConverter struct {
	sessionID string
	seq       uint64
	mono      uint64
	utc       time.Time
	episode   string
	counter   uint64
	closed    bool
}

func NewNativeEventConverter(sessionID string) (*NativeEventConverter, error) {
	if !validID(sessionID) {
		return nil, errors.New("native converter requires a valid session id")
	}
	return &NativeEventConverter{sessionID: sessionID}, nil
}

func (converter *NativeEventConverter) Convert(frame NativeFrameV1) ([]EventV1, error) {
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	frameUTC := time.UnixMicro(int64(frame.UTCUnixUS)).UTC()
	if !converter.utc.IsZero() && frameUTC.Before(converter.utc) {
		frameUTC = converter.utc
	}
	frameMono := frame.MonotonicUS
	if converter.seq != 0 && frameMono <= converter.mono {
		frameMono = converter.mono + 1
	}
	var converted []EventV1
	if converter.seq == 0 {
		converter.nextEpisode()
		converter.seq = 1
		converter.mono = frameMono
		converter.utc = frameUTC
		converted = append(converted, EventV1{
			Version: EventVersionV1, SessionID: converter.sessionID,
			EpisodeID: converter.episode, Seq: converter.seq,
			MonotonicUS: converter.mono, UTC: canonicalUTC(converter.utc),
			Type: EventResume,
		})
		frameMono++
	}
	if converter.closed && frame.Type != EventDropCount {
		converter.nextEpisode()
		converter.closed = false
	}
	converter.seq++
	converter.mono = frameMono
	converter.utc = frameUTC
	event := EventV1{
		Version: EventVersionV1, SessionID: converter.sessionID,
		EpisodeID: converter.episode, Seq: converter.seq,
		MonotonicUS: converter.mono, UTC: canonicalUTC(converter.utc),
		Type: frame.Type, Composition: frame.Composition,
		Selection: frame.Selection, EditCount: frame.EditCount,
		DropCount: frame.DropCount,
	}
	if frame.Type == EventCommit {
		source := "unknown"
		if highlightedText(frame.Composition) == frame.FinalText {
			source = "candidate"
		}
		event.Commit = &CommitV1{Text: frame.FinalText, Source: source}
		event.FinalText = &FinalTextV1{
			Text: frame.FinalText, Scope: "composition", Confidence: "observed",
		}
	}
	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("convert native frame: %w", err)
	}
	converted = append(converted, event)
	if frame.Type == EventCommit || frame.Type == EventAbort {
		converter.closed = true
	}
	return converted, nil
}

func (converter *NativeEventConverter) nextEpisode() {
	converter.counter++
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", converter.sessionID, converter.counter)))
	converter.episode = hex.EncodeToString(digest[:16])
}

func analyzeNativeFile(path, sessionID string) (Report, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return Report{}, 0, err
	}
	defer file.Close()
	converter, err := NewNativeEventConverter(sessionID)
	if err != nil {
		return Report{}, 0, err
	}
	analyzer := NewAnalyzer()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), MaxEventBytes+1)
	frames := 0
	for scanner.Scan() {
		frames++
		frame, err := DecodeNativeFrameV1(scanner.Bytes())
		if err != nil {
			return Report{}, frames, fmt.Errorf("native frame line %d: %w", frames, err)
		}
		events, err := converter.Convert(frame)
		if err != nil {
			return Report{}, frames, fmt.Errorf("native frame line %d: %w", frames, err)
		}
		for _, event := range events {
			if err := analyzer.Accept(event); err != nil {
				return Report{}, frames, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Report{}, frames, fmt.Errorf("read native spool: %w", err)
	}
	report := analyzer.Finish()
	report.NativeEventCount = frames
	return report, frames, nil
}
