// SPDX-License-Identifier: Apache-2.0
package replaylab

import "testing"

func TestAnalyzeSyntheticRepairTrajectory(t *testing.T) {
	composition := &CompositionV1{
		RawInput:           "alpha",
		NormalizedPinyin:   "alpha",
		CaretByte:          len("alpha"),
		ExactPathAvailable: true,
		Candidates: []CandidateV1{
			{Text: "correction guess", IsCorrection: true, Highlighted: true},
			{Text: "exact phrase", IsCorrection: false},
		},
	}
	events := []EventV1{
		syntheticEvent(1, EventResume),
		{Version: EventVersionV1, SessionID: testSession, EpisodeID: testEpisode, Seq: 2, MonotonicUS: 200, UTC: "2026-01-01T00:00:01Z", Type: EventComposition, Composition: composition},
		{Version: EventVersionV1, SessionID: testSession, EpisodeID: testEpisode, Seq: 3, MonotonicUS: 300, UTC: "2026-01-01T00:00:02Z", Type: EventSelect, Composition: composition, Selection: &SelectionV1{Rank: 2, Text: "exact phrase"}},
		{Version: EventVersionV1, SessionID: testSession, EpisodeID: testEpisode, Seq: 4, MonotonicUS: 400, UTC: "2026-01-01T00:00:03Z", Type: EventBackspace, Composition: composition, EditCount: 2},
		{Version: EventVersionV1, SessionID: testSession, EpisodeID: testEpisode, Seq: 5, MonotonicUS: 500, UTC: "2026-01-01T00:00:04Z", Type: EventCommit, Composition: composition, Commit: &CommitV1{Text: "exact phrase", Source: "candidate"}, FinalText: &FinalTextV1{Text: "exact phrase", Scope: "episode", Confidence: "user_confirmed"}},
	}
	segmentation := &CompositionV1{
		RawInput:           "beta",
		NormalizedPinyin:   "beta",
		CaretByte:          len("beta"),
		ExactPathAvailable: true,
		Candidates: []CandidateV1{
			{Text: "word one", Highlighted: true},
			{Text: "word two"},
		},
	}
	secondEpisode := "33333333333333333333333333333333"
	events = append(events,
		EventV1{Version: EventVersionV1, SessionID: testSession, EpisodeID: secondEpisode, Seq: 6, MonotonicUS: 600, UTC: "2026-01-01T00:00:05Z", Type: EventComposition, Composition: segmentation},
		EventV1{Version: EventVersionV1, SessionID: testSession, EpisodeID: secondEpisode, Seq: 7, MonotonicUS: 700, UTC: "2026-01-01T00:00:06Z", Type: EventSelect, Composition: segmentation, Selection: &SelectionV1{Rank: 2, Text: "word two"}},
		EventV1{Version: EventVersionV1, SessionID: testSession, EpisodeID: secondEpisode, Seq: 8, MonotonicUS: 800, UTC: "2026-01-01T00:00:07Z", Type: EventCommit, Composition: segmentation, Commit: &CommitV1{Text: "word two", Source: "candidate"}, FinalText: &FinalTextV1{Text: "word two", Scope: "episode", Confidence: "observed"}},
	)
	report, err := Analyze(events)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if report.ExactPathCorrectionFirstCount == 0 {
		t.Fatal("missed exact-path correction-first regression")
	}
	if report.SamePinyinRerankCount != 1 {
		t.Fatalf("same-pinyin rerank count = %d", report.SamePinyinRerankCount)
	}
	if report.SamePinyinAfterEditReplaceCount != 1 {
		t.Fatalf("same-pinyin edit replacement count = %d", report.SamePinyinAfterEditReplaceCount)
	}
	if report.BackspaceCount != 2 || report.EditedEpisodeCount != 1 {
		t.Fatalf("edit metrics = backspace %d, episodes %d", report.BackspaceCount, report.EditedEpisodeCount)
	}
	if report.SelectionRankHistogram["2"] != 2 || report.MeanSelectionRank != 2 {
		t.Fatalf("selection metrics = %#v, mean %f", report.SelectionRankHistogram, report.MeanSelectionRank)
	}
}
