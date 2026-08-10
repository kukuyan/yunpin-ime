// SPDX-License-Identifier: Apache-2.0
package replaylab

import (
	"bytes"
	"strings"
	"testing"
)

const (
	testSession = "11111111111111111111111111111111"
	testEpisode = "22222222222222222222222222222222"
)

func TestEventV1StrictBounds(t *testing.T) {
	if _, err := DecodeEventV1(bytes.Repeat([]byte{'x'}, MaxEventBytes+1)); err == nil {
		t.Fatal("oversized encoded event was accepted")
	}
	event := syntheticEvent(1, EventComposition)
	event.Composition = syntheticComposition()
	encoded, err := EncodeEventV1(event)
	if err != nil {
		t.Fatalf("encode valid event: %v", err)
	}
	if len(encoded) > MaxEventBytes {
		t.Fatalf("event exceeded bound: %d", len(encoded))
	}
	if _, err := DecodeEventV1(append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)); err == nil {
		t.Fatal("unknown JSON field was accepted")
	}

	event.Composition.Candidates = make([]CandidateV1, MaxCandidates+1)
	for index := range event.Composition.Candidates {
		event.Composition.Candidates[index].Text = "x"
	}
	if err := event.Validate(); err == nil {
		t.Fatal("candidate overflow was accepted")
	}

	event = syntheticEvent(1, EventComposition)
	event.Composition = syntheticComposition()
	event.Composition.RawInput = strings.Repeat("a", maxInputBytes+1)
	if err := event.Validate(); err == nil {
		t.Fatal("oversized input was accepted")
	}
}

func TestEventV1TypeShapeAndCanonicalTime(t *testing.T) {
	event := syntheticEvent(1, EventCommit)
	event.Composition = syntheticComposition()
	event.Commit = &CommitV1{Text: "synthetic final", Source: "candidate"}
	event.FinalText = &FinalTextV1{Text: "synthetic final", Scope: "episode", Confidence: "user_confirmed"}
	if err := event.Validate(); err != nil {
		t.Fatalf("valid commit rejected: %v", err)
	}
	event.FinalText.Text = "mismatch"
	if err := event.Validate(); err == nil {
		t.Fatal("mismatched final text was accepted")
	}
	event = syntheticEvent(1, EventResume)
	event.UTC = "2026-01-01T08:00:00+08:00"
	if err := event.Validate(); err == nil {
		t.Fatal("non-UTC timestamp was accepted")
	}
}

func TestSequenceRejectsGapsAndEpisodeReuse(t *testing.T) {
	var state SequenceState
	resume := syntheticEvent(1, EventResume)
	if err := state.Accept(resume); err != nil {
		t.Fatalf("accept resume: %v", err)
	}
	gap := syntheticEvent(3, EventComposition)
	gap.Composition = syntheticComposition()
	if err := state.Accept(gap); err == nil {
		t.Fatal("sequence gap was accepted")
	}
	snapshot := syntheticEvent(2, EventComposition)
	snapshot.Composition = syntheticComposition()
	if err := state.Accept(snapshot); err != nil {
		t.Fatalf("accept snapshot: %v", err)
	}
	commit := syntheticEvent(3, EventCommit)
	commit.Composition = syntheticComposition()
	commit.Commit = &CommitV1{Text: "synthetic final", Source: "candidate"}
	commit.FinalText = &FinalTextV1{Text: "synthetic final", Scope: "episode", Confidence: "observed"}
	if err := state.Accept(commit); err != nil {
		t.Fatalf("accept commit: %v", err)
	}
	reused := syntheticEvent(4, EventComposition)
	reused.Composition = syntheticComposition()
	if err := state.Accept(reused); err == nil {
		t.Fatal("closed episode reuse was accepted")
	}
	newEpisode := reused
	newEpisode.EpisodeID = "33333333333333333333333333333333"
	if err := state.Accept(newEpisode); err != nil {
		t.Fatalf("new episode rejected: %v", err)
	}
	secondCommit := syntheticEvent(5, EventCommit)
	secondCommit.EpisodeID = newEpisode.EpisodeID
	secondCommit.Composition = syntheticComposition()
	secondCommit.Commit = &CommitV1{Text: "second final", Source: "candidate"}
	secondCommit.FinalText = &FinalTextV1{Text: "second final", Scope: "episode", Confidence: "observed"}
	if err := state.Accept(secondCommit); err != nil {
		t.Fatalf("second commit rejected: %v", err)
	}
	reopenOld := syntheticEvent(6, EventComposition)
	reopenOld.Composition = syntheticComposition()
	if err := state.Accept(reopenOld); err == nil {
		t.Fatal("previously closed episode_id was reused")
	}
}

func syntheticEvent(seq uint64, kind EventType) EventV1 {
	return EventV1{
		Version:     EventVersionV1,
		SessionID:   testSession,
		EpisodeID:   testEpisode,
		Seq:         seq,
		MonotonicUS: seq * 100,
		UTC:         "2026-01-01T00:00:00Z",
		Type:        kind,
	}
}

func syntheticComposition() *CompositionV1 {
	return &CompositionV1{
		RawInput:           "synthetic",
		NormalizedPinyin:   "synthetic",
		CaretByte:          len("synthetic"),
		ExactPathAvailable: true,
		Candidates: []CandidateV1{
			{Text: "synthetic first", Highlighted: true},
			{Text: "synthetic second"},
		},
	}
}
