// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"math"
	"sort"

	"github.com/kukuyan/yunpin-ime/localstore"
)

const defaultHabitReportLimit = 50

type HabitReportQuery struct {
	SinceDate       string
	CorrectionsOnly bool
	Limit           int
	IncludeText     bool
}

type HabitDaySummary struct {
	Date          string `json:"date"`
	Selections    uint64 `json:"selections"`
	CorrectedFrom uint64 `json:"corrected_from"`
	Replacements  uint64 `json:"replacements"`
}

type HabitReport struct {
	SinceDate       string                 `json:"since,omitempty"`
	CorrectionsOnly bool                   `json:"corrections_only"`
	TextIncluded    bool                   `json:"text_included"`
	StatRows        int                    `json:"stat_rows"`
	Selections      uint64                 `json:"selections"`
	Corrections     uint64                 `json:"corrections"`
	Days            []HabitDaySummary      `json:"days"`
	Entries         []localstore.HabitStat `json:"entries,omitempty"`
}

func addHabitCount(total *uint64, value uint64) {
	if value > math.MaxUint64-*total {
		*total = math.MaxUint64
		return
	}
	*total += value
}

// HabitReport reads the encrypted word-level learning evidence. By default it
// returns only counts grouped by local date; phrase text and pinyin leave the
// store only after the caller explicitly opts in with IncludeText.
func (agent Agent) HabitReport(ctx context.Context, query HabitReportQuery) (HabitReport, error) {
	if !localstore.ValidHabitSince(query.SinceDate) {
		return HabitReport{}, errors.New("habit report date is invalid")
	}
	if query.Limit == 0 {
		query.Limit = defaultHabitReportLimit
	}
	if query.Limit < 1 || query.Limit > localstore.MaxHabitReportEntries {
		return HabitReport{}, errors.New("habit report limit is invalid")
	}
	report := HabitReport{
		SinceDate: query.SinceDate, CorrectionsOnly: query.CorrectionsOnly,
		TextIncluded: query.IncludeText, Days: []HabitDaySummary{},
	}
	err := agent.withPrivateStore(ctx, func(store *localstore.Store) error {
		stats, err := store.QueryHabits(ctx, localstore.HabitQuery{
			SinceDate: query.SinceDate, CorrectionsOnly: query.CorrectionsOnly,
			Limit: localstore.MaxHabitReportEntries,
		})
		if err != nil {
			return err
		}
		report.StatRows = len(stats)
		byDate := make(map[string]*HabitDaySummary)
		for _, stat := range stats {
			day := byDate[stat.DateBucket]
			if day == nil {
				day = &HabitDaySummary{Date: stat.DateBucket}
				byDate[stat.DateBucket] = day
			}
			addHabitCount(&day.Selections, stat.SelectionCount)
			addHabitCount(&day.CorrectedFrom, stat.CorrectedFromCount)
			addHabitCount(&day.Replacements, stat.ReplacementCount)
			addHabitCount(&report.Selections, stat.SelectionCount)
			addHabitCount(&report.Corrections, stat.CorrectedFromCount)
		}
		for _, day := range byDate {
			report.Days = append(report.Days, *day)
		}
		sort.Slice(report.Days, func(left, right int) bool {
			return report.Days[left].Date > report.Days[right].Date
		})
		if query.IncludeText {
			if len(stats) > query.Limit {
				stats = stats[:query.Limit]
			}
			report.Entries = stats
		}
		return nil
	})
	return report, err
}
