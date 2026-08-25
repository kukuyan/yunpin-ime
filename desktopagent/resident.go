// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// ResidentOptions configures one background synchronization process.
//
// The resident deliberately takes no path overrides. Every location comes from
// DefaultPaths, so the scheduled task, the LaunchAgent and the `run` subcommand
// cannot drift apart, and a task definition cannot be edited to point the agent
// at another user's state.
type ResidentOptions struct {
	// Interval between successful synchronization rounds. Zero uses the
	// runner's default.
	Interval time.Duration
	// Events receives the redacted run events. It is called from the runner
	// goroutine and must not block for long. A nil value discards them.
	Events func(RunEvent)
}

// NewResidentAgent builds the agent the background process uses, wired to the
// fixed platform paths and the fixed Rime maintenance bridge.
func NewResidentAgent(defaults Paths) (Agent, error) {
	secrets, err := NewPlatformSecretStore(PlatformSecretStoreOptions{
		Service:   defaults.CredentialService,
		Directory: defaults.StateDirectory,
	})
	if err != nil {
		return Agent{}, err
	}
	bridgePaths, err := DefaultRimeBridgePaths(defaults)
	if err != nil {
		return Agent{}, err
	}
	refresh, err := NewDefaultRimeUserDBRefresh(bridgePaths)
	if err != nil {
		return Agent{}, err
	}
	return Agent{
		Secrets:              secrets,
		Profile:              DefaultProfile,
		StateDirectory:       defaults.StateDirectory,
		EndpointConfigPath:   defaults.EndpointConfigPath,
		DatabasePath:         defaults.DatabasePath,
		NativeEventsPath:     defaults.NativeEventsPath,
		RimeUserDBExportPath: bridgePaths.StagingPath,
		RimeUserDBRefresh:    refresh,
		BaselinePath:         defaults.BaselinePath,
		SnapshotPath:         defaults.SnapshotPath,
		SnapshotStatePath:    defaults.SnapshotStatePath,
		Reload:               DefaultReloadHook(),
	}, nil
}

// RunResident runs the background synchronization loop until ctx is cancelled.
//
// Both the `run` subcommand of the console binary and the windowless resident
// binary call this, so the two cannot diverge in what they configure.
func RunResident(ctx context.Context, defaults Paths, options ResidentOptions) error {
	agent, err := NewResidentAgent(defaults)
	if err != nil {
		return err
	}
	events, closeLog := residentEventSink(defaults, options.Events)
	defer closeLog()
	return agent.Run(ctx, RunOptions{
		LockPath: defaults.LockPath,
		Interval: options.Interval,
		OnEvent:  events,
	})
}

func residentEventSink(defaults Paths, caller func(RunEvent)) (func(RunEvent), func()) {
	// Diagnostics are optional. A linked, permission-invalid or otherwise
	// unavailable log is dropped while the synchronization loop continues; the
	// status command exposes event_log_available=false.
	log, logErr := OpenEventLog(defaults)
	if logErr != nil {
		log = nil
	}
	events := func(event RunEvent) {
		if log != nil {
			log.Write(event)
		}
		if caller != nil {
			caller(event)
		}
	}
	closeLog := func() {
		if log != nil {
			_ = log.Close()
		}
	}
	return events, closeLog
}

// WriteRunEvent renders one run event as a single redacted JSON line and
// reports how many bytes it wrote.
//
// RunEvent carries a stable code and a numeric summary and nothing else: no
// phrase, pinyin, ciphertext, endpoint, account or device identifier. That is
// what makes it safe to persist, so this helper is the only supported way to
// serialize it for a log.
func WriteRunEvent(writer io.Writer, event RunEvent, now time.Time) (int, error) {
	if writer == nil {
		return 0, nil
	}
	if err := validateRunEvent(event); err != nil {
		return 0, err
	}
	encoded, err := json.Marshal(struct {
		Time         string      `json:"time"`
		Code         string      `json:"code"`
		FailureClass string      `json:"failure_class"`
		Successful   bool        `json:"successful"`
		Summary      SyncSummary `json:"summary"`
	}{
		Time:         now.UTC().Format(time.RFC3339),
		Code:         event.Code,
		FailureClass: event.FailureClass,
		Successful:   event.Successful,
		Summary:      event.Summary,
	})
	if err != nil {
		return 0, err
	}
	return fmt.Fprintln(writer, string(encoded))
}
