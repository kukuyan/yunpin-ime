// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
)

const windowsRequiredIdle = 15 * time.Minute
const windowsMinimumSyncInterval = 6 * time.Hour
const windowsFailedRetryInterval = 15 * time.Minute

// An unfinished start marker is intentionally treated conservatively after a
// crash: the last round may have reached the network without a final receipt.
func windowsAutomaticDue(h localstore.SyncHealth, now time.Time) bool {
	if now.IsZero() || h.LastSuccessAt < 0 || h.LastEventAt < 0 || h.LastNetworkAt < 0 || h.SyncInFlight {
		return false
	}
	if h.LastNetworkAt > 0 && now.Sub(time.UnixMilli(h.LastNetworkAt)) <= windowsMinimumSyncInterval {
		return false
	}
	switch h.LastEventCode {
	case "":
		return h.LastEventAt == 0
	case "sync_complete":
		return h.LastSuccessAt > 0 && h.LastEventAt > 0 && now.After(time.UnixMilli(h.LastEventAt))
	case "sync_started":
		return h.LastNetworkAt > 0 && h.LastEventAt > 0 && now.After(time.UnixMilli(h.LastEventAt))
	case "sync_deferred_busy":
		return h.LastEventAt > 0 && now.Sub(time.UnixMilli(h.LastEventAt)) >= windowsFailedRetryInterval
	case "sync_failed":
		return h.LastEventAt > 0 && now.Sub(time.UnixMilli(h.LastEventAt)) >= windowsFailedRetryInterval
	default:
		return false
	}
}

type windowsIdleContextKey struct{}

func checkAutomaticIdle(ctx context.Context) error {
	check, ok := ctx.Value(windowsIdleContextKey{}).(func() bool)
	if ok && !check() {
		return ErrRimeMaintenanceBusy
	}
	return nil
}

type windowsIdleOperations struct {
	session uint32
	observe func() (windowsIdleSample, error)
	load    func(context.Context) (localstore.SyncHealth, error)
	locked  func(func() error) error
	sync    func(context.Context) (SyncSummary, error)
	now     func() time.Time
	wait    func(context.Context, time.Duration) error
}

// This loop changes only automatic admission. The existing sync operation,
// manual action, process lock, /sync and /deploy implementations are reused.
func runWindowsIdleLoop(ctx context.Context, options RunOptions, ops windowsIdleOperations) error {
	if ops.session == 0 || ops.observe == nil || ops.load == nil || ops.locked == nil || ops.sync == nil || ops.now == nil || ops.wait == nil || options.Interval <= 0 {
		return errors.New("Windows automatic scheduling boundaries unavailable")
	}
	var tracker windowsIdleTracker
	var lastAttempt time.Time
	var lastDiagnostic string
	clockOrigin := ops.now()
	var clockBlockedUntil time.Time
	observe := func() bool {
		s, err := ops.observe()
		if err != nil || s.sessionID != ops.session {
			s = windowsIdleSample{}
		}
		return tracker.eligible(s, windowsRequiredIdle, 2*options.Interval)
	}
	observe() // Never synchronize immediately when the resident starts.
	for {
		if err := ops.wait(ctx, options.Interval); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		now := ops.now()
		elapsed := now.Sub(clockOrigin)
		wallElapsed := time.Duration(now.UnixMilli()-clockOrigin.UnixMilli()) * time.Millisecond
		if elapsed < 0 || wallElapsed-elapsed > 5*time.Second || elapsed-wallElapsed > 5*time.Second {
			// A clock jump invalidates both elapsed scheduling and idle evidence.
			clockOrigin = now
			clockBlockedUntil = now.Add(windowsMinimumSyncInterval)
			tracker = windowsIdleTracker{}
			observe()
			continue
		}
		if !observe() {
			continue
		}
		if !clockBlockedUntil.IsZero() && !now.After(clockBlockedUntil) {
			continue
		}
		if !lastAttempt.IsZero() && now.Sub(lastAttempt) < windowsFailedRetryInterval {
			continue
		}
		var attempted bool
		var summary SyncSummary
		err := ops.locked(func() error {
			// A manual synchronization may have completed while acquiring the
			// lock. Reload its record and recheck the actual session before work.
			readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			health, err := ops.load(readCtx)
			cancel()
			if err != nil {
				return err
			}
			if !windowsAutomaticDue(health, ops.now()) || !observe() {
				return nil
			}
			attempted = true
			lastAttempt = ops.now()
			summary, err = ops.sync(context.WithValue(ctx, windowsIdleContextKey{}, observe))
			return err
		})
		if !attempted && errors.Is(err, ErrAlreadyRunning) {
			continue
		}
		if !attempted && err == nil {
			lastDiagnostic = ""
			continue
		}
		if !attempted && err != nil {
			// Do not interpret a failed health read as never synchronized or
			// spam the event log once per minute while the fault persists.
			if lastDiagnostic == "unavailable" {
				continue
			}
			lastDiagnostic = "unavailable"
		} else {
			lastDiagnostic = ""
		}
		if options.OnEvent != nil {
			event, _, _ := classifyRunResult(summary, err, options, options.MinBackoff)
			options.OnEvent(event)
		}
	}
}
