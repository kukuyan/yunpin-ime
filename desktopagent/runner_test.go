// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

func TestProcessLockRejectsConcurrentAgent(t *testing.T) {
	path := privateTestPath(t, "agent.lock")
	first, err := acquireProcessLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	second, err := acquireProcessLock(path)
	if !errors.Is(err, ErrAlreadyRunning) || second != nil {
		t.Fatalf("second lock=%#v err=%v", second, err)
	}
}

func TestRunLoopRetriesWithRedactedEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	events := make(chan RunEvent, 4)
	err := runLoop(ctx, func(context.Context) (SyncSummary, error) {
		call := calls.Add(1)
		if call == 1 {
			return SyncSummary{}, errors.New("synthetic secret-bearing detail must not enter event")
		}
		cancel()
		return SyncSummary{Rounds: 1}, nil
	}, RunOptions{
		LockPath: privateTestPath(t, "agent.lock"), Interval: time.Second,
		MinBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond,
		OnEvent: func(event RunEvent) { events <- event },
	})
	if err == nil {
		// Millisecond timings are intentionally rejected in production. Exercise
		// the loop with accepted timings below while keeping this guard covered.
		t.Fatal("unsafe sub-second timing was accepted")
	}

	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	calls.Store(0)
	go func() {
		time.Sleep(1100 * time.Millisecond)
		cancel()
	}()
	err = runLoop(ctx, func(context.Context) (SyncSummary, error) {
		if calls.Add(1) == 1 {
			return SyncSummary{}, errors.New("synthetic detail")
		}
		return SyncSummary{Rounds: 1}, nil
	}, RunOptions{
		LockPath: privateTestPath(t, "agent.lock"), Interval: time.Second,
		MinBackoff: time.Second, MaxBackoff: time.Second,
		OnEvent: func(event RunEvent) { events <- event },
	})
	if err != nil {
		t.Fatal(err)
	}
	first := <-events
	if first.Code != "sync_failed" || first.FailureClass != localstore.SyncFailureLocalStore || first.Successful {
		t.Fatalf("unexpected redacted event %#v", first)
	}
}

func TestRunLoopDoesNotSyncAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	err := runLoop(ctx, func(context.Context) (SyncSummary, error) {
		calls.Add(1)
		return SyncSummary{}, nil
	}, RunOptions{
		LockPath: privateTestPath(t, "agent.lock"), Interval: time.Second,
		MinBackoff: time.Second, MaxBackoff: time.Second,
	})
	if err != nil || calls.Load() != 0 {
		t.Fatalf("cancelled runner called sync: calls=%d err=%v", calls.Load(), err)
	}
}

func TestRimeMaintenanceBusyIsDeferredWithoutGrowingFailureBackoff(t *testing.T) {
	options := RunOptions{Interval: time.Minute, MinBackoff: time.Second, MaxBackoff: 16 * time.Second}
	event, delay, nextBackoff := classifyRunResult(SyncSummary{},
		fmt.Errorf("wrapped host response: %w", ErrRimeMaintenanceBusy), options, 8*time.Second)
	if event.Code != "sync_deferred_busy" || event.Successful || delay != options.MinBackoff || nextBackoff != options.MinBackoff {
		t.Fatalf("busy host response polluted failure backoff: event=%#v delay=%v next=%v", event, delay, nextBackoff)
	}
	failure, failureDelay, failureBackoff := classifyRunResult(SyncSummary{}, errors.New("synthetic failure"), options, 8*time.Second)
	if failure.Code != "sync_failed" || failure.FailureClass != localstore.SyncFailureLocalStore ||
		failure.Successful || failureDelay != 8*time.Second || failureBackoff != options.MaxBackoff {
		t.Fatalf("ordinary failure no longer uses exponential backoff: event=%#v delay=%v next=%v", failure, failureDelay, failureBackoff)
	}
}

func TestSyncFailureClassificationUsesTypedRedactedBoundaries(t *testing.T) {
	checks := []struct {
		err  error
		want string
	}{
		{&syncclient.NetworkError{Err: errors.New("private endpoint detail")}, localstore.SyncFailureNetwork},
		{&syncclient.APIError{Status: 401, Code: "invalid_device_token"}, localstore.SyncFailureAuth},
		{&syncclient.APIError{Status: 503, Code: "temporarily_unavailable"}, localstore.SyncFailureRelayProtocol},
		{&syncclient.RelayProtocolError{Err: errors.New("private response detail")}, localstore.SyncFailureRelayProtocol},
		{&syncclient.LocalStoreError{Err: errors.New("private path detail")}, localstore.SyncFailureLocalStore},
		{errors.New("local snapshot detail"), localstore.SyncFailureLocalStore},
	}
	for _, check := range checks {
		if got := classifySyncFailure(fmt.Errorf("wrapped: %w", check.err)); got != check.want {
			t.Fatalf("classify %T=%q, want %q", check.err, got, check.want)
		}
	}
}

func TestRunLoopEmitsDeferredBusyEventWithoutReportingFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan RunEvent, 1)
	err := runLoop(ctx, func(context.Context) (SyncSummary, error) {
		cancel()
		return SyncSummary{}, fmt.Errorf("refresh wrapper: %w", ErrRimeMaintenanceBusy)
	}, RunOptions{
		LockPath: privateTestPath(t, "agent.lock"), Interval: time.Minute,
		MinBackoff: time.Second, MaxBackoff: 16 * time.Second,
		OnEvent: func(event RunEvent) { events <- event },
	})
	if err != nil {
		t.Fatal(err)
	}
	event := <-events
	if event.Code != "sync_deferred_busy" || event.Successful {
		t.Fatalf("busy maintenance was reported as a synchronization failure: %#v", event)
	}
}

func TestResidentRimeUserDBRequiresFixedMaintenanceRefresher(t *testing.T) {
	err := (Agent{RimeUserDBExportPath: privateTestPath(t, "rime-userdb.snapshot")}).Run(
		context.Background(), RunOptions{LockPath: privateTestPath(t, "agent.lock")},
	)
	if err == nil || !strings.Contains(err.Error(), "fixed platform maintenance refresher") {
		t.Fatalf("resident accepted a static cumulative snapshot: %v", err)
	}
}
