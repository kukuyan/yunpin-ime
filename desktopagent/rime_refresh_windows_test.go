// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"errors"
	"testing"
)

func TestWindowsRimeMaintenanceExitCodeContract(t *testing.T) {
	if windowsRimeMaintenanceUnavailableExitCode <= 0 ||
		windowsRimeMaintenanceUnavailableExitCode == windowsRimeMaintenanceBusyExitCode {
		t.Fatalf("maintenance exit codes are not distinct nonzero values: unavailable=%d busy=%d",
			windowsRimeMaintenanceUnavailableExitCode, windowsRimeMaintenanceBusyExitCode)
	}
	if err := windowsRimeMaintenanceExitCodeError(0); err != nil {
		t.Fatalf("successful fixed deployer result was rejected: %v", err)
	}
	unavailable := windowsRimeMaintenanceExitCodeError(windowsRimeMaintenanceUnavailableExitCode)
	if !errors.Is(unavailable, ErrRimeMaintenanceUnavailable) || errors.Is(unavailable, ErrRimeMaintenanceBusy) {
		t.Fatalf("IPC-unavailable result was not kept distinct: %v", unavailable)
	}
	busy := windowsRimeMaintenanceExitCodeError(windowsRimeMaintenanceBusyExitCode)
	if !errors.Is(busy, ErrRimeMaintenanceBusy) || errors.Is(busy, ErrRimeMaintenanceUnavailable) {
		t.Fatalf("retryable idle-gate result did not map exclusively to ErrRimeMaintenanceBusy: %v", busy)
	}
	if err := windowsRimeMaintenanceExitCodeError(1); err == nil ||
		errors.Is(err, ErrRimeMaintenanceBusy) || errors.Is(err, ErrRimeMaintenanceUnavailable) {
		t.Fatalf("hard deployer failure was misclassified as retryable: %v", err)
	}
}
