// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
)

func TestWindowsAutomaticDueUsesStrictSixHoursAndFailsClosed(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		name   string
		health localstore.SyncHealth
		want   bool
	}{
		{"never synchronized", localstore.SyncHealth{}, true},
		{"exactly six hours", localstore.SyncHealth{LastNetworkAt: now.Add(-6 * time.Hour).UnixMilli(), LastSuccessAt: now.Add(-6 * time.Hour).UnixMilli(), LastEventAt: now.Add(-6 * time.Hour).UnixMilli(), LastEventCode: "sync_complete"}, false},
		{"more than six hours", localstore.SyncHealth{LastNetworkAt: now.Add(-6*time.Hour - time.Millisecond).UnixMilli(), LastSuccessAt: now.Add(-6*time.Hour - time.Millisecond).UnixMilli(), LastEventAt: now.Add(-6*time.Hour - time.Millisecond).UnixMilli(), LastEventCode: "sync_complete"}, true},
		{"future success", localstore.SyncHealth{LastNetworkAt: now.Add(time.Minute).UnixMilli(), LastSuccessAt: now.Add(time.Minute).UnixMilli(), LastEventAt: now.UnixMilli(), LastEventCode: "sync_complete"}, false},
		{"incomplete start", localstore.SyncHealth{LastEventAt: now.Add(-time.Hour).UnixMilli(), LastEventCode: "sync_started"}, false},
		{"old incomplete start", localstore.SyncHealth{LastEventAt: now.Add(-7 * time.Hour).UnixMilli(), LastEventCode: "sync_started", SyncInFlight: true}, false},
		{"recovered unknown within six hours", localstore.SyncHealth{LastNetworkAt: now.Add(-time.Hour).UnixMilli(), LastEventAt: now.Add(-7 * time.Hour).UnixMilli(), LastEventCode: "sync_started"}, false},
		{"recovered unknown after six hours", localstore.SyncHealth{LastNetworkAt: now.Add(-7 * time.Hour).UnixMilli(), LastEventAt: now.Add(-8 * time.Hour).UnixMilli(), LastEventCode: "sync_started"}, true},
		{"network success then deployment fails", localstore.SyncHealth{LastNetworkAt: now.Add(-time.Hour).UnixMilli(), LastEventAt: now.Add(-time.Hour).UnixMilli(), LastEventCode: "sync_failed"}, false},
		{"legacy manual success after stale checkpoint", localstore.SyncHealth{LastNetworkAt: now.Add(-7 * time.Hour).UnixMilli(), LastSuccessAt: now.Add(-time.Minute).UnixMilli(), LastEventAt: now.Add(-time.Minute).UnixMilli(), LastEventCode: "sync_complete"}, false},
		{"legacy uncertain failure after stale checkpoint", localstore.SyncHealth{LastNetworkAt: now.Add(-7 * time.Hour).UnixMilli(), LastSuccessAt: now.Add(-7 * time.Hour).UnixMilli(), LastEventAt: now.Add(-time.Minute).UnixMilli(), LastEventCode: "sync_failed"}, false},
		{"legacy unknown failure at six hours", localstore.SyncHealth{LastNetworkAt: now.Add(-6 * time.Hour).UnixMilli(), LastEventAt: now.Add(-6 * time.Hour).UnixMilli(), LastEventCode: "sync_failed"}, false},
		{"legacy unknown failure after six hours", localstore.SyncHealth{LastNetworkAt: now.Add(-6*time.Hour - time.Millisecond).UnixMilli(), LastEventAt: now.Add(-6*time.Hour - time.Millisecond).UnixMilli(), LastEventCode: "sync_failed"}, true},
		{"legacy failure overrides older checkpoint at boundary", localstore.SyncHealth{LastNetworkAt: now.Add(-7 * time.Hour).UnixMilli(), LastSuccessAt: now.Add(-7 * time.Hour).UnixMilli(), LastEventAt: now.Add(-6 * time.Hour).UnixMilli(), LastEventCode: "sync_failed"}, false},
		{"legacy failure overrides older checkpoint beyond boundary", localstore.SyncHealth{LastNetworkAt: now.Add(-7 * time.Hour).UnixMilli(), LastSuccessAt: now.Add(-7 * time.Hour).UnixMilli(), LastEventAt: now.Add(-6*time.Hour - time.Millisecond).UnixMilli(), LastEventCode: "sync_failed"}, true},
		{"legacy start overrides older checkpoint at boundary", localstore.SyncHealth{LastNetworkAt: now.Add(-7 * time.Hour).UnixMilli(), LastSuccessAt: now.Add(-7 * time.Hour).UnixMilli(), LastEventAt: now.Add(-6 * time.Hour).UnixMilli(), LastEventCode: "sync_started"}, false},
		{"legacy start overrides older checkpoint beyond boundary", localstore.SyncHealth{LastNetworkAt: now.Add(-7 * time.Hour).UnixMilli(), LastSuccessAt: now.Add(-7 * time.Hour).UnixMilli(), LastEventAt: now.Add(-6*time.Hour - time.Millisecond).UnixMilli(), LastEventCode: "sync_started"}, true},
		{"failure before retry", localstore.SyncHealth{LastEventAt: now.Add(-14 * time.Minute).UnixMilli(), LastEventCode: "sync_failed"}, false},
		{"failure after fifteen minutes still has unknown network outcome", localstore.SyncHealth{LastEventAt: now.Add(-15 * time.Minute).UnixMilli(), LastEventCode: "sync_failed"}, false},
		{"unknown record", localstore.SyncHealth{LastEventAt: now.Add(-time.Hour).UnixMilli(), LastEventCode: "unknown"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowsAutomaticDue(tc.health, now); got != tc.want {
				t.Fatalf("due=%v want %v", got, tc.want)
			}
		})
	}
}

func TestWindowsResidentRejectsLegacyFailureWrittenWhileTakingLock(t *testing.T) {
	start := time.Unix(1800000000, 0)
	now := start
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var rounds, waits, loads int
	var health localstore.SyncHealth
	ops := windowsIdleOperations{
		session: 1,
		observe: func() (windowsIdleSample, error) {
			return windowsIdleSample{valid: true, sessionID: 1, state: windowsIdleActive, lastInputTick: 1, uptimeMS: uint64(now.Sub(start).Milliseconds()) + 1, eventRevision: 1}, nil
		},
		locked: func(f func() error) error {
			if loads == 0 {
				health = localstore.SyncHealth{
					LastNetworkAt: now.Add(-7 * time.Hour).UnixMilli(), LastSuccessAt: now.Add(-7 * time.Hour).UnixMilli(),
					LastEventAt: now.Add(-20 * time.Minute).UnixMilli(), LastEventCode: "sync_failed",
				}
			}
			return f()
		},
		load: func(context.Context) (localstore.SyncHealth, error) { loads++; return health, nil },
		sync: func(context.Context) (SyncSummary, error) { rounds++; return SyncSummary{}, nil },
		now:  func() time.Time { return now },
		wait: func(context.Context, time.Duration) error {
			waits++
			if waits == 18 {
				cancel()
				return context.Canceled
			}
			now = now.Add(time.Minute)
			return nil
		},
	}
	if err := runWindowsIdleLoop(ctx, RunOptions{Interval: time.Minute, MinBackoff: time.Second, MaxBackoff: time.Minute}, ops); err != nil {
		t.Fatal(err)
	}
	if loads == 0 || rounds != 0 {
		t.Fatalf("legacy unknown failure was ignored: loads=%d rounds=%d", loads, rounds)
	}
}

func TestWindowsResidentChecksIdleAndReloadsManualSuccessUnderLock(t *testing.T) {
	start := time.Unix(1800000000, 0)
	now := start
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var rounds, waits, loads int
	var health localstore.SyncHealth
	ops := windowsIdleOperations{
		session: 1,
		observe: func() (windowsIdleSample, error) {
			return windowsIdleSample{valid: true, sessionID: 1, state: windowsIdleActive, lastInputTick: 1, uptimeMS: uint64(now.Sub(start).Milliseconds()) + 1, eventRevision: 1}, nil
		},
		locked: func(f func() error) error {
			// First eligible check races a manual completion while acquiring the
			// shared operation lock; it must not start an automatic round.
			if loads == 0 {
				// A legacy manual client may leave this Windows-specific
				// checkpoint stale while updating the shared health record.
				health = localstore.SyncHealth{LastNetworkAt: now.Add(-7 * time.Hour).UnixMilli(), LastSuccessAt: now.UnixMilli(), LastEventAt: now.UnixMilli(), LastEventCode: "sync_complete"}
			}
			return f()
		},
		load: func(context.Context) (localstore.SyncHealth, error) { loads++; return health, nil },
		sync: func(context.Context) (SyncSummary, error) { rounds++; return SyncSummary{}, nil },
		now:  func() time.Time { return now },
		wait: func(context.Context, time.Duration) error {
			waits++
			if waits == 18 {
				cancel()
				return context.Canceled
			}
			now = now.Add(time.Minute)
			if waits < 15 && loads != 0 {
				t.Fatal("checked schedule before fifteen minutes idle")
			}
			return nil
		},
	}
	if err := runWindowsIdleLoop(ctx, RunOptions{Interval: time.Minute, MinBackoff: time.Second, MaxBackoff: time.Minute}, ops); err != nil {
		t.Fatal(err)
	}
	if loads == 0 || rounds != 0 {
		t.Fatalf("manual completion was ignored: loads=%d rounds=%d", loads, rounds)
	}
}

func TestWindowsResidentDoesNotSyncWhenHealthCannotBeRead(t *testing.T) {
	start := time.Unix(1800000000, 0)
	now := start
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits, calls, notices int
	ops := windowsIdleOperations{session: 2,
		observe: func() (windowsIdleSample, error) {
			return windowsIdleSample{valid: true, sessionID: 2, state: windowsIdleActive, lastInputTick: 1, uptimeMS: uint64(now.Sub(start).Milliseconds()) + 1, eventRevision: 1}, nil
		},
		load: func(context.Context) (localstore.SyncHealth, error) {
			return localstore.SyncHealth{}, errors.New("synthetic unavailable")
		},
		locked: func(f func() error) error { return f() },
		sync:   func(context.Context) (SyncSummary, error) { calls++; return SyncSummary{}, nil },
		now:    func() time.Time { return now },
		wait: func(context.Context, time.Duration) error {
			waits++
			if waits > 18 {
				cancel()
				return context.Canceled
			}
			now = now.Add(time.Minute)
			return nil
		},
	}
	options := RunOptions{Interval: time.Minute, MinBackoff: time.Second, MaxBackoff: time.Minute, OnEvent: func(RunEvent) { notices++ }}
	if err := runWindowsIdleLoop(ctx, options, ops); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || notices != 1 {
		t.Fatalf("calls=%d notices=%d", calls, notices)
	}
}

func TestWindowsResidentRunsOnlyAfterNewUninterruptedIdleWindow(t *testing.T) {
	start := time.Unix(1800000000, 0)
	now := start
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits, calls int
	var tick uint32 = 1
	var health localstore.SyncHealth
	ops := windowsIdleOperations{session: 1,
		observe: func() (windowsIdleSample, error) {
			return windowsIdleSample{valid: true, sessionID: 1, state: windowsIdleActive, lastInputTick: tick, uptimeMS: uint64(now.Sub(start).Milliseconds()) + 1, eventRevision: 1}, nil
		},
		load:   func(context.Context) (localstore.SyncHealth, error) { return health, nil },
		locked: func(f func() error) error { return f() },
		sync: func(context.Context) (SyncSummary, error) {
			calls++
			if waits != 27 {
				t.Fatalf("started at minute %d after input at minute 12", waits)
			}
			health = localstore.SyncHealth{LastNetworkAt: now.UnixMilli(), LastSuccessAt: now.UnixMilli(), LastEventAt: now.UnixMilli(), LastEventCode: "sync_complete"}
			return SyncSummary{}, nil
		},
		now: func() time.Time { return now },
		wait: func(context.Context, time.Duration) error {
			waits++
			if waits > 30 {
				cancel()
				return context.Canceled
			}
			now = now.Add(time.Minute)
			if waits == 12 {
				tick = uint32(now.Sub(start).Milliseconds())
			}
			return nil
		},
	}
	if err := runWindowsIdleLoop(ctx, RunOptions{Interval: time.Minute, MinBackoff: time.Second, MaxBackoff: time.Minute}, ops); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("got %d rounds", calls)
	}
}

func TestWindowsResidentAfterNormalRestartUsesExistingOldSyncTime(t *testing.T) {
	start := time.Unix(1800000000, 0)
	now := start
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits, calls int
	old := start.Add(-48 * time.Hour).UnixMilli()
	h := localstore.SyncHealth{LastNetworkAt: old, LastSuccessAt: old, LastEventAt: old, LastEventCode: "sync_complete"}
	ops := windowsIdleOperations{session: 1,
		observe: func() (windowsIdleSample, error) {
			return windowsIdleSample{valid: true, sessionID: 1, state: windowsIdleActive, lastInputTick: 1, uptimeMS: uint64(now.Sub(start).Milliseconds()) + 1, eventRevision: 1}, nil
		},
		load:   func(context.Context) (localstore.SyncHealth, error) { return h, nil },
		locked: func(f func() error) error { return f() },
		sync: func(context.Context) (SyncSummary, error) {
			calls++
			if waits != 15 {
				t.Fatalf("normal restart waited %d minutes", waits)
			}
			cancel()
			return SyncSummary{}, nil
		},
		now: func() time.Time { return now },
		wait: func(ctx context.Context, _ time.Duration) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			waits++
			now = now.Add(time.Minute)
			return nil
		},
	}
	if err := runWindowsIdleLoop(ctx, RunOptions{Interval: time.Minute, MinBackoff: time.Second, MaxBackoff: time.Minute}, ops); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one eligible round, got %d", calls)
	}
}
