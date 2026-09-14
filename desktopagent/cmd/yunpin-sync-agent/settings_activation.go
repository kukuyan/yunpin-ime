// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

func (operations *localSettingsOperations) ActivationStatus(ctx context.Context) (settingsActivationState, error) {
	var state settingsActivationState
	snapshot, snapshotErr := desktopagent.ReadSnapshotActivation(operations.defaults)
	state.BaselinePresent, state.SnapshotPresent, state.SnapshotApplied = snapshot.BaselinePresent, snapshot.SnapshotPresent, snapshot.SnapshotApplied
	state.PrivateCandidatesEnabled, _ = desktopagent.PrivateCandidatesEnabled(operations.defaults)
	background, backgroundErr := desktopagent.ReadBackgroundStatus(ctx, operations.defaults)
	state.BackgroundAvailable = backgroundErr == nil && background.Supported && background.Installed
	state.BackgroundEnabled, state.BackgroundRunning = background.Enabled, background.Running
	status, statusErr := operations.agent.StatusWithoutUserInteraction(ctx)
	if statusErr == nil && status.HealthAvailable {
		state.LastSuccessAt, state.PendingUploads = status.Health.LastSuccessAt, status.Health.PendingUploads
		state.LastEventCode, state.LastFailureClass = status.Health.LastEventCode, status.Health.LastFailureClass
	}
	switch {
	case snapshotErr != nil:
		state.ProblemCode = "local-permissions"
	case !state.BaselinePresent:
		state.ProblemCode = "baseline-missing"
	case !state.PrivateCandidatesEnabled:
		state.ProblemCode = "private-candidates-disabled"
	case !state.SnapshotPresent:
		state.ProblemCode = "snapshot-missing"
	case !state.SnapshotApplied:
		state.ProblemCode = "snapshot-not-applied"
	case !state.BackgroundAvailable:
		state.ProblemCode = "background-unavailable"
	case !state.BackgroundEnabled:
		state.ProblemCode = "background-disabled"
	case !state.BackgroundRunning:
		state.ProblemCode = "background-not-running"
	}
	// An unpaired first installation is expected, not a page-render failure.
	// Every unavailable component is represented explicitly above.
	return state, nil
}

func (operations *localSettingsOperations) PrepareVocabulary(ctx context.Context) error {
	err := desktopagent.WithProcessLock(operations.defaults.LockPath, func() error {
		return desktopagent.PreparePrivateVocabulary(ctx, operations.defaults, operations.agent.Reload)
	})
	return classifyOnboardingError(err, "local-state")
}

func (operations *localSettingsOperations) EnableBackground(ctx context.Context) error {
	err := desktopagent.WithProcessLock(operations.defaults.LockPath, func() error {
		snapshot, err := desktopagent.ReadSnapshotActivation(operations.defaults)
		if err != nil {
			return &settingsOnboardingError{Code: "local-permissions"}
		}
		if !snapshot.BaselinePresent {
			return &settingsOnboardingError{Code: "baseline-missing"}
		}
		if !snapshot.SnapshotPresent {
			return &settingsOnboardingError{Code: "snapshot-missing"}
		}
		if !snapshot.SnapshotApplied {
			return &settingsOnboardingError{Code: "snapshot-not-applied"}
		}
		enabled, err := desktopagent.PrivateCandidatesEnabled(operations.defaults)
		if err != nil {
			return &settingsOnboardingError{Code: "local-permissions"}
		}
		if !enabled {
			return &settingsOnboardingError{Code: "private-candidates-disabled"}
		}
		_, err = operations.agent.ResidentReady(ctx)
		return err
	})
	if err != nil {
		return classifyOnboardingError(err, "local-state")
	}
	// Release the same lock before invoking the standard resident-ready gate
	// in the installer. A successful login is not repeated here.
	background, err := desktopagent.EnableBackground(ctx, operations.defaults)
	if err != nil && (!background.Supported || !background.Installed) {
		return &settingsOnboardingError{Code: "background-unavailable"}
	}
	return classifyOnboardingError(err, "background-not-running")
}
