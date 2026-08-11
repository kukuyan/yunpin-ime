// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestProcessLockRejectsConcurrentAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.lock")
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
		LockPath: filepath.Join(t.TempDir(), "agent.lock"), Interval: time.Second,
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
		LockPath: filepath.Join(t.TempDir(), "agent.lock"), Interval: time.Second,
		MinBackoff: time.Second, MaxBackoff: time.Second,
		OnEvent: func(event RunEvent) { events <- event },
	})
	if err != nil {
		t.Fatal(err)
	}
	first := <-events
	if first.Code != "sync_failed" || first.Successful {
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
		LockPath: filepath.Join(t.TempDir(), "agent.lock"), Interval: time.Second,
		MinBackoff: time.Second, MaxBackoff: time.Second,
	})
	if err != nil || calls.Load() != 0 {
		t.Fatalf("cancelled runner called sync: calls=%d err=%v", calls.Load(), err)
	}
}
