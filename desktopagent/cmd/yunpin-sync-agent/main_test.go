// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"strings"
	"testing"
)

func TestInitAccountRequiresExplicitRecoveryDisplayAcknowledgement(t *testing.T) {
	err := run(context.Background(), []string{"init-account"})
	if err == nil || !strings.Contains(err.Error(), "--confirm-display-recovery-key") {
		t.Fatalf("init-account crossed confirmation gate: %v", err)
	}
}

func TestConfirmedInitAccountStillFailsClosedBeforePlatformAccess(t *testing.T) {
	err := run(context.Background(), []string{"init-account", "--confirm-display-recovery-key"})
	if err == nil || !strings.Contains(err.Error(), "rollback-safe") {
		t.Fatalf("confirmed init-account was not disabled: %v", err)
	}
}

func TestUnknownCommandFailsWithoutPlatformAccess(t *testing.T) {
	err := run(context.Background(), []string{"unknown-command"})
	if err == nil || err.Error() != "unknown command" {
		t.Fatalf("unknown command error=%v", err)
	}
}
