// SPDX-License-Identifier: Apache-2.0
//go:build yunpin_pairing_private

package main

import (
	"context"
	"strings"
	"testing"
)

func TestPrivatePairingCancelRequiresExactConfirmation(t *testing.T) {
	for _, arguments := range [][]string{{"pairing-cancel"}, {"pairing-cancel", "--confirm=false"}} {
		err := run(context.Background(), arguments)
		if err == nil || !strings.Contains(err.Error(), "pairing-cancel requires --confirm") {
			t.Fatalf("private cancellation crossed confirmation gate for %v: %v", arguments, err)
		}
	}
}

func TestPrivatePairingAbortRequiresExactConfirmation(t *testing.T) {
	for _, arguments := range [][]string{{"pairing-abort"}, {"pairing-abort", "--confirm=false"}} {
		err := run(context.Background(), arguments)
		if err == nil || !strings.Contains(err.Error(), "pairing-abort requires --confirm") {
			t.Fatalf("private joiner abort crossed confirmation gate for %v: %v", arguments, err)
		}
	}
}

func TestPrivatePairingCommandDispatcherDoesNotCaptureUnknownCommands(t *testing.T) {
	err := run(context.Background(), []string{"pairing-cancel-typo"})
	if err == nil || err.Error() != "unknown command" {
		t.Fatalf("private dispatcher captured an unknown command: %v", err)
	}
}

func TestPrivatePairingCommandsAreRegisteredOnlyByThePrivateBuild(t *testing.T) {
	for command, expectedGate := range map[string]string{
		"pairing-invite":          "--confirm-display-invitation",
		"pairing-cancel":          "--confirm",
		"pairing-abort":           "--confirm",
		"e2e-init-empty-baseline": "--confirm-create-empty-baseline",
	} {
		err := run(context.Background(), []string{command})
		if err == nil || !strings.Contains(err.Error(), expectedGate) || err.Error() == "unknown command" {
			t.Fatalf("private command %q was not registered behind its local gate: %v", command, err)
		}
	}
}
