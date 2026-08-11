// SPDX-License-Identifier: Apache-2.0
package replaylab

import (
	"fmt"
	"sort"
)

type Report struct {
	Version                         string         `json:"version"`
	SessionID                       string         `json:"session_id"`
	EventCount                      int            `json:"event_count"`
	NativeEventCount                int            `json:"native_event_count"`
	EpisodeCount                    int            `json:"episode_count"`
	CommitCount                     int            `json:"commit_count"`
	BackspaceCount                  uint64         `json:"backspace_count"`
	DeleteCount                     uint64         `json:"delete_count"`
	DroppedEventCount               uint64         `json:"dropped_event_count"`
	EditedEpisodeCount              int            `json:"edited_episode_count"`
	CandidateSelections             int            `json:"candidate_selections"`
	SelectionRankHistogram          map[string]int `json:"selection_rank_histogram"`
	MeanSelectionRank               float64        `json:"mean_selection_rank"`
	ExactPathCorrectionFirstCount   int            `json:"exact_path_correction_first_count"`
	SamePinyinRerankCount           int            `json:"same_pinyin_rerank_count"`
	SamePinyinAfterEditReplaceCount int            `json:"same_pinyin_after_edit_replace_count"`
	Suggestions                     []string       `json:"suggestions"`
}

type episodeReportState struct {
	edited             bool
	selectionObserved  bool
	lastComposition    *CompositionV1
	beforeEditPinyin   string
	beforeEditText     string
	alreadyCountedEdit bool
}

type Analyzer struct {
	report           Report
	sequence         SequenceState
	currentEpisodeID string
	currentEpisode   episodeReportState
	selectionRankSum int
	finished         bool
}

func NewAnalyzer() *Analyzer {
	return &Analyzer{report: Report{
		Version:                "yunpin.replay.report.v1",
		SelectionRankHistogram: make(map[string]int),
	}}
}

func (analyzer *Analyzer) Accept(event EventV1) error {
	if analyzer.finished {
		return fmt.Errorf("analyzer is already finished")
	}
	if err := analyzer.sequence.Accept(event); err != nil {
		return fmt.Errorf("event %d: %w", analyzer.report.EventCount+1, err)
	}
	analyzer.report.EventCount++
	if analyzer.report.SessionID == "" {
		analyzer.report.SessionID = event.SessionID
	}
	if event.EpisodeID != analyzer.currentEpisodeID {
		analyzer.finishEpisode()
		analyzer.currentEpisodeID = event.EpisodeID
		analyzer.currentEpisode = episodeReportState{}
		analyzer.report.EpisodeCount++
	}
	state := &analyzer.currentEpisode

	if event.Composition != nil {
		copy := *event.Composition
		state.lastComposition = &copy
		if event.Type == EventComposition && copy.ExactPathAvailable && len(copy.Candidates) > 1 && copy.Candidates[0].IsCorrection && hasOrdinaryCandidate(copy.Candidates[1:]) {
			analyzer.report.ExactPathCorrectionFirstCount++
		}
	}
	switch event.Type {
	case EventSelect:
		state.selectionObserved = true
		analyzer.report.CandidateSelections++
		analyzer.selectionRankSum += event.Selection.Rank
		analyzer.report.SelectionRankHistogram[fmt.Sprintf("%d", event.Selection.Rank)]++
		if isSamePinyinRerank(event) {
			analyzer.report.SamePinyinRerankCount++
		}
	case EventBackspace:
		analyzer.report.BackspaceCount += uint64(event.EditCount)
		markFirstEdit(state, event.Composition)
	case EventDelete:
		analyzer.report.DeleteCount += uint64(event.EditCount)
		markFirstEdit(state, event.Composition)
	case EventDropCount:
		analyzer.report.DroppedEventCount += event.DropCount
	case EventCommit:
		analyzer.report.CommitCount++
		if !state.selectionObserved {
			if rank := highlightedRank(event.Composition); rank > 0 {
				analyzer.report.CandidateSelections++
				analyzer.selectionRankSum += rank
				analyzer.report.SelectionRankHistogram[fmt.Sprintf("%d", rank)]++
			}
		}
		if state.edited && event.Composition.NormalizedPinyin == state.beforeEditPinyin && state.beforeEditText != "" && event.FinalText.Text != state.beforeEditText {
			analyzer.report.SamePinyinAfterEditReplaceCount++
		}
	}
	return nil
}

func (analyzer *Analyzer) Finish() Report {
	if !analyzer.finished {
		analyzer.finishEpisode()
		if analyzer.report.CandidateSelections > 0 {
			analyzer.report.MeanSelectionRank = float64(analyzer.selectionRankSum) / float64(analyzer.report.CandidateSelections)
		}
		if analyzer.report.EventCount == 0 {
			analyzer.report.Suggestions = []string{"No events are available; connect a dedicated IME sidecar or experiment harness before drawing conclusions."}
		} else {
			analyzer.report.Suggestions = buildSuggestions(analyzer.report)
		}
		analyzer.finished = true
	}
	return analyzer.report
}

func (analyzer *Analyzer) finishEpisode() {
	if analyzer.currentEpisodeID != "" && analyzer.currentEpisode.edited {
		analyzer.report.EditedEpisodeCount++
	}
}

func Analyze(events []EventV1) (Report, error) {
	analyzer := NewAnalyzer()
	for _, event := range events {
		if err := analyzer.Accept(event); err != nil {
			return Report{}, err
		}
	}
	return analyzer.Finish(), nil
}

func markFirstEdit(state *episodeReportState, composition *CompositionV1) {
	state.edited = true
	if state.alreadyCountedEdit {
		return
	}
	state.alreadyCountedEdit = true
	if composition == nil {
		composition = state.lastComposition
	}
	if composition == nil {
		return
	}
	state.beforeEditPinyin = composition.NormalizedPinyin
	state.beforeEditText = highlightedText(composition)
	if state.beforeEditText == "" && len(composition.Candidates) > 0 {
		state.beforeEditText = composition.Candidates[0].Text
	}
}

func isSamePinyinRerank(event EventV1) bool {
	composition := event.Composition
	selection := event.Selection
	if composition == nil || selection == nil || !composition.ExactPathAvailable || selection.Rank <= 1 {
		return false
	}
	selected := composition.Candidates[selection.Rank-1]
	return !selected.IsCorrection && len(composition.Candidates) > 0 && !composition.Candidates[0].IsCorrection
}

func hasOrdinaryCandidate(candidates []CandidateV1) bool {
	for _, candidate := range candidates {
		if !candidate.IsCorrection {
			return true
		}
	}
	return false
}

func highlightedText(composition *CompositionV1) string {
	for _, candidate := range composition.Candidates {
		if candidate.Highlighted {
			return candidate.Text
		}
	}
	return ""
}

func highlightedRank(composition *CompositionV1) int {
	if composition == nil {
		return 0
	}
	for index, candidate := range composition.Candidates {
		if candidate.Highlighted {
			return index + 1
		}
	}
	return 0
}

func buildSuggestions(report Report) []string {
	var suggestions []string
	if report.ExactPathCorrectionFirstCount > 0 {
		suggestions = append(suggestions, "Keep every viable exact/non-correction path ahead of typo-correction candidates; add these exposures as ranking golden tests.")
	}
	if report.SamePinyinRerankCount > 0 || report.SamePinyinAfterEditReplaceCount > 0 {
		suggestions = append(suggestions, "Treat same-pinyin wording changes as phrase segmentation or personal-language-model learning, not keyboard typo expansion.")
	}
	if report.EditedEpisodeCount > 0 {
		suggestions = append(suggestions, "Review edited episodes first and turn repeated backspace/retype trajectories into synthetic regression cases before changing production ranking.")
	}
	if report.MeanSelectionRank > 1.5 {
		suggestions = append(suggestions, "Candidate choices are frequently below rank 1; prefer bounded personal reranking over adding more generated candidates.")
	}
	if report.DroppedEventCount > 0 {
		suggestions = append(suggestions, "Trace loss was reported; increase the future native bounded ring capacity before trusting frequency estimates.")
	}
	if len(suggestions) == 0 {
		suggestions = append(suggestions, "No deterministic regression signal crossed the current thresholds; collect more dedicated IME episodes without changing ranking yet.")
	}
	sort.Strings(suggestions)
	return suggestions
}
