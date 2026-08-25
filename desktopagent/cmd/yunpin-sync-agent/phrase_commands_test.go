// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os"
	"strings"
	"testing"
)

// The phrase subcommands are the only ones that can put personal vocabulary on
// a terminal, so the command surface itself is asserted rather than only the
// agent API behind it.
func TestPhraseListKeepsVocabularyBehindAnExplicitFlag(t *testing.T) {
	source, err := os.ReadFile("phrase_commands.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)

	// Text is opt-in, not opt-out.
	if !strings.Contains(text, `set.Bool("show-text", false,`) {
		t.Fatal("phrase list does not default to counts only")
	}
	if !strings.Contains(text, "IncludeText: *showText") {
		t.Fatal("the listing does not honour the opt-in flag")
	}
	// The disclosure is announced on stderr so it stays out of the JSON on
	// stdout while remaining visible in a shared or recorded session.
	if !strings.Contains(text, "prints personal vocabulary") {
		t.Fatal("--show-text does not warn that it discloses vocabulary")
	}
	if !strings.Contains(text, "os.Stderr") {
		t.Fatal("the disclosure warning is not written to stderr")
	}
}

func TestPhraseSubcommandsAreDispatched(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, `case "phrase":`) {
		t.Fatal("the phrase command is not dispatched")
	}
	if !strings.Contains(text, "phraseCommandNames()") {
		t.Fatal("the usage string does not mention the phrase subcommands")
	}
}

func TestPhraseMutationsUseTheSharedOperationLock(t *testing.T) {
	source, err := os.ReadFile("phrase_commands.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if count := strings.Count(text, "desktopagent.WithProcessLock(common.lock"); count != 3 {
		t.Fatalf("phrase mutation lock count=%d, want 3", count)
	}
}
