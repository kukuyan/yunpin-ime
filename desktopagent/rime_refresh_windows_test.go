// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"errors"
	"testing"
)

func TestWindowsRimeMaintenanceExitCodeContract(t *testing.T) {
	if err := windowsRimeMaintenanceExitCodeError(0); err != nil {
		t.Fatalf("successful fixed deployer result was rejected: %v", err)
	}
	if err := windowsRimeMaintenanceExitCodeError(windowsRimeMaintenanceBusyExitCode); !errors.Is(err, ErrRimeMaintenanceBusy) {
		t.Fatalf("retryable idle-gate result did not map to ErrRimeMaintenanceBusy: %v", err)
	}
	if err := windowsRimeMaintenanceExitCodeError(1); err == nil || errors.Is(err, ErrRimeMaintenanceBusy) {
		t.Fatalf("hard deployer failure was misclassified as retryable: %v", err)
	}
}
