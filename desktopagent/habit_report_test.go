// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
)

func TestHabitReportDefaultsToAggregateWithoutPhraseText(t *testing.T) {
	agent := newVocabularyAgent(t)
	ctx := context.Background()
	err := agent.withPrivateStore(ctx, func(store *localstore.Store) error {
		if _, err := store.RecordNativeLocalSelection(ctx, localstore.NativeSelection{
			EventID: "report-selection", DateBucket: "2026-08-24",
			Phrase: localstore.Phrase{Text: "办公是", Pinyin: "ban gong shi"},
		}); err != nil {
			return err
		}
		_, err := store.RecordNativeCorrection(ctx, localstore.NativeCorrection{
			EventID: "report-correction", DateBucket: "2026-08-25",
			CorrectedFrom: localstore.Phrase{Text: "办公是", Pinyin: "ban gong shi"},
			Replacement:   localstore.Phrase{Text: "办公室", Pinyin: "ban gong shi"},
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := agent.HabitReport(ctx, HabitReportQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if report.TextIncluded || len(report.Entries) != 0 || report.StatRows != 3 ||
		report.Selections != 1 || report.Corrections != 1 || len(report.Days) != 2 {
		t.Fatalf("aggregate report mismatch: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "办公是") || strings.Contains(string(encoded), "办公室") ||
		strings.Contains(string(encoded), "ban gong shi") {
		t.Fatalf("default report disclosed habit text: %s", encoded)
	}
}

func TestHabitReportShowsFilteredTextOnlyAfterOptIn(t *testing.T) {
	agent := newVocabularyAgent(t)
	ctx := context.Background()
	if err := agent.withPrivateStore(ctx, func(store *localstore.Store) error {
		_, err := store.RecordNativeCorrection(ctx, localstore.NativeCorrection{
			EventID: "report-visible-correction", DateBucket: "2026-08-25",
			CorrectedFrom: localstore.Phrase{Text: "办公是", Pinyin: "ban gong shi"},
			Replacement:   localstore.Phrase{Text: "办公室", Pinyin: "ban gong shi"},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	report, err := agent.HabitReport(ctx, HabitReportQuery{
		SinceDate: "2026-08-25", CorrectionsOnly: true, Limit: 1, IncludeText: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.TextIncluded || report.StatRows != 2 || len(report.Entries) != 1 ||
		report.Entries[0].Phrase == "" || report.Entries[0].Pinyin == "" {
		t.Fatalf("opt-in report mismatch: %#v", report)
	}
}
