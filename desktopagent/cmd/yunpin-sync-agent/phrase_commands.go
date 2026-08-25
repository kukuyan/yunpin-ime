// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

// The `phrase` subcommands are the supported way to correct the personal
// vocabulary. Before them the only reachable lever was hand-editing
// yunpin/private.tsv, which is a generated snapshot that the next rebuild
// overwrites, so corrections did not survive.
//
// Every operation goes through the same storage path a learned phrase takes, so
// a manual edit lands in the same mutation-plus-outbox transaction and converges
// on the other devices by the ordinary merge rules.

const phraseUsage = "usage: yunpin-sync-agent phrase <add|pin|unpin|remove|list|report> [options]"

func phraseTarget(set *flag.FlagSet) (*string, *string) {
	return set.String("text", "", "phrase text"),
		set.String("pinyin", "", "phrase reading, lowercase letters with optional spaces")
}

func commandPhrase(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	if len(arguments) < 1 {
		return errors.New(phraseUsage)
	}
	switch arguments[0] {
	case "add":
		return commandPhraseAdd(ctx, defaults, arguments[1:])
	case "pin":
		return commandPhrasePin(ctx, defaults, arguments[1:], true)
	case "unpin":
		return commandPhrasePin(ctx, defaults, arguments[1:], false)
	case "remove":
		return commandPhraseRemove(ctx, defaults, arguments[1:])
	case "list":
		return commandPhraseList(ctx, defaults, arguments[1:])
	case "report":
		return commandPhraseReport(ctx, defaults, arguments[1:])
	default:
		return errors.New(phraseUsage)
	}
}

func commandPhraseReport(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("phrase report", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	since := set.String("since", "", "include local learning on or after YYYY-MM-DD")
	correctionsOnly := set.Bool("corrections-only", false, "show only corrected and replacement entries")
	limit := set.Int("limit", 50, "maximum entries to show when text is included")
	showText := set.Bool("show-text", false,
		"include personal phrases and readings; without this the report is aggregate only")
	if err := parse(set, arguments); err != nil {
		return err
	}
	_, agent, err := common.components()
	if err != nil {
		return err
	}
	if *showText {
		_, _ = fmt.Fprintln(os.Stderr,
			"yunpin-sync-agent: --show-text prints personal learning habits; do not paste this output into a report")
	}
	report, err := agent.HabitReport(ctx, desktopagent.HabitReportQuery{
		SinceDate: *since, CorrectionsOnly: *correctionsOnly,
		Limit: *limit, IncludeText: *showText,
	})
	if err != nil {
		return err
	}
	return writeJSON(report)
}

func commandPhraseAdd(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("phrase add", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	text, pinyin := phraseTarget(set)
	pin := set.Bool("pin", false, "keep this phrase ahead of ordinary candidates")
	if err := parse(set, arguments); err != nil {
		return err
	}
	_, agent, err := common.components()
	if err != nil {
		return err
	}
	var change desktopagent.VocabularyChange
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var changeErr error
		change, changeErr = agent.AddPhrase(ctx, *text, *pinyin, *pin)
		return changeErr
	})
	if err != nil {
		return err
	}
	return writeJSON(change)
}

func commandPhrasePin(ctx context.Context, defaults desktopagent.Paths, arguments []string, pinned bool) error {
	name := "phrase unpin"
	if pinned {
		name = "phrase pin"
	}
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	text, pinyin := phraseTarget(set)
	if err := parse(set, arguments); err != nil {
		return err
	}
	_, agent, err := common.components()
	if err != nil {
		return err
	}
	var change desktopagent.VocabularyChange
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var changeErr error
		change, changeErr = agent.SetPhrasePinned(ctx, *text, *pinyin, pinned)
		return changeErr
	})
	if err != nil {
		return err
	}
	return writeJSON(change)
}

func commandPhraseRemove(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("phrase remove", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	text, pinyin := phraseTarget(set)
	if err := parse(set, arguments); err != nil {
		return err
	}
	_, agent, err := common.components()
	if err != nil {
		return err
	}
	var change desktopagent.VocabularyChange
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var changeErr error
		change, changeErr = agent.RemovePhrase(ctx, *text, *pinyin)
		return changeErr
	})
	if err != nil {
		return err
	}
	return writeJSON(change)
}

func commandPhraseList(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("phrase list", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	limit := set.Int("limit", 50, "maximum entries to show when text is included")
	pinnedOnly := set.Bool("pinned", false, "restrict the listing to pinned phrases")
	showText := set.Bool("show-text", false,
		"include the phrases themselves; without this the listing is counts only")
	if err := parse(set, arguments); err != nil {
		return err
	}
	_, agent, err := common.components()
	if err != nil {
		return err
	}
	if *showText {
		// The one place this tool prints personal vocabulary to a terminal.
		// Say so on stderr so it is visible in a shared session or a recorded
		// screen without polluting the JSON on stdout.
		_, _ = fmt.Fprintln(os.Stderr,
			"yunpin-sync-agent: --show-text prints personal vocabulary; do not paste this output into a report")
	}
	summary, err := agent.ListVocabulary(ctx, desktopagent.VocabularyQuery{
		Limit: *limit, PinnedOnly: *pinnedOnly, IncludeText: *showText,
	})
	if err != nil {
		return err
	}
	return writeJSON(summary)
}

// phraseCommandNames lists the subcommands for the top-level usage string.
func phraseCommandNames() string {
	return strings.Join([]string{"add", "pin", "unpin", "remove", "list", "report"}, "|")
}
