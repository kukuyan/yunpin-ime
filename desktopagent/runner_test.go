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

type countingSecretStore struct {
	SecretStore
	interactiveLoads atomic.Int32
	backgroundLoads  atomic.Int32
}

type interactiveOnlyCountingStore struct {
	SecretStore
	loads atomic.Int32
}

func (store *interactiveOnlyCountingStore) Load(ctx context.Context, profile string) ([]byte, error) {
	store.loads.Add(1)
	return store.SecretStore.Load(ctx, profile)
}

func TestBackgroundCredentialLoadFailsClosedWithoutNoUICapability(t *testing.T) {
	store := &interactiveOnlyCountingStore{SecretStore: newMemorySecretStore()}
	_, err := (Agent{Secrets: store, Profile: "default"}).loadResidentBundle(context.Background())
	if err == nil {
		t.Fatal("background credential load accepted an interactive-only store")
	}
	if store.loads.Load() != 0 {
		t.Fatalf("background credential load invoked interactive Load %d times", store.loads.Load())
	}
}

func (store *countingSecretStore) Load(ctx context.Context, profile string) ([]byte, error) {
	store.interactiveLoads.Add(1)
	return store.SecretStore.Load(ctx, profile)
}

func (store *countingSecretStore) LoadWithoutUserInteraction(ctx context.Context, profile string) ([]byte, error) {
	store.backgroundLoads.Add(1)
	return store.SecretStore.Load(ctx, profile)
}

func TestResidentLoadsCredentialOnceAndZeroesItOnExit(t *testing.T) {
	memory := newMemorySecretStore()
	bundle := testCredentials()
	encoded, err := EncodeCredentialBundle(bundle)
	bundle.Zero()
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.Save(context.Background(), "default", encoded); err != nil {
		zeroBytes(encoded)
		t.Fatal(err)
	}
	zeroBytes(encoded)
	secrets := &countingSecretStore{SecretStore: memory}
	agent := Agent{Secrets: secrets, Profile: "default"}
	var retained *CredentialBundleV1
	err = agent.withResidentBundle(context.Background(), RunOptions{
		MinBackoff: time.Second, MaxBackoff: time.Second,
	}, func(loaded *CredentialBundleV1) error {
		retained = loaded
		if len(loaded.DeviceToken) == 0 || loaded.LocalDataKey == ([32]byte{}) {
			t.Fatal("resident did not retain its decoded credential during the run")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if secrets.backgroundLoads.Load() != 1 || secrets.interactiveLoads.Load() != 0 {
		t.Fatalf(
			"resident loads: background=%d interactive=%d, want background=1 interactive=0",
			secrets.backgroundLoads.Load(), secrets.interactiveLoads.Load(),
		)
	}
	if retained == nil || len(retained.DeviceToken) != 0 || retained.LocalDataKey != ([32]byte{}) ||
		retained.ObjectIDKey != ([32]byte{}) || retained.SigningSeed != ([32]byte{}) {
		t.Fatal("resident credential remained in memory after shutdown")
	}
}

type recoveringSecretStore struct {
	*countingSecretStore
	failures atomic.Int32
}

func (store *recoveringSecretStore) LoadWithoutUserInteraction(ctx context.Context, profile string) ([]byte, error) {
	store.backgroundLoads.Add(1)
	if store.failures.Add(-1) >= 0 {
		return nil, errors.New("synthetic interaction unavailable")
	}
	return store.SecretStore.Load(ctx, profile)
}

func TestResidentRetriesCredentialInSameLoopThenRetainsIt(t *testing.T) {
	memory := newMemorySecretStore()
	bundle := testCredentials()
	encoded, err := EncodeCredentialBundle(bundle)
	bundle.Zero()
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.Save(context.Background(), "default", encoded); err != nil {
		zeroBytes(encoded)
		t.Fatal(err)
	}
	zeroBytes(encoded)
	base := &countingSecretStore{SecretStore: memory}
	secrets := &recoveringSecretStore{countingSecretStore: base}
	secrets.failures.Store(1)
	agent := Agent{Secrets: secrets, Profile: "default"}
	var events atomic.Int32
	err = agent.withResidentBundle(context.Background(), RunOptions{
		MinBackoff: time.Second, MaxBackoff: time.Second,
		OnEvent: func(event RunEvent) {
			if event.Code != "sync_failed" {
				t.Errorf("credential retry event=%#v", event)
			}
			events.Add(1)
		},
	}, func(loaded *CredentialBundleV1) error {
		if len(loaded.DeviceToken) == 0 {
			t.Fatal("resident did not retain recovered credential")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if secrets.backgroundLoads.Load() != 2 || secrets.interactiveLoads.Load() != 0 || events.Load() != 1 {
		t.Fatalf(
			"recovery loads: background=%d interactive=%d events=%d, want 2/0/1",
			secrets.backgroundLoads.Load(), secrets.interactiveLoads.Load(), events.Load(),
		)
	}
}

func TestDuplicateResidentIsRejectedBeforeCredentialLoad(t *testing.T) {
	memory := newMemorySecretStore()
	secrets := &countingSecretStore{SecretStore: memory}
	operationLock := privateTestPath(t, "agent.lock")
	residentLock, err := acquireProcessLock(operationLock + ".resident")
	if err != nil {
		t.Fatal(err)
	}
	defer residentLock.Release()
	agent := Agent{Secrets: secrets, Profile: "default"}
	err = agent.Run(context.Background(), RunOptions{LockPath: operationLock})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("duplicate resident error=%v", err)
	}
	if secrets.backgroundLoads.Load() != 0 || secrets.interactiveLoads.Load() != 0 {
		t.Fatalf(
			"duplicate resident touched credentials: background=%d interactive=%d",
			secrets.backgroundLoads.Load(), secrets.interactiveLoads.Load(),
		)
	}
}

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

func TestRunLoopReleasesOperationLockBetweenRounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	operationLock := privateTestPath(t, "agent.lock")
	firstRound := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- runLoop(ctx, func(context.Context) (SyncSummary, error) {
			return SyncSummary{Rounds: 1}, nil
		}, RunOptions{
			LockPath: operationLock, Interval: time.Minute,
			MinBackoff: time.Second, MaxBackoff: time.Second,
			OnEvent: func(RunEvent) { firstRound <- struct{}{} },
		})
	}()
	select {
	case <-firstRound:
	case <-time.After(3 * time.Second):
		t.Fatal("resident did not complete its first round")
	}
	if err := WithProcessLock(operationLock, func() error { return nil }); err != nil {
		t.Fatalf("one-shot operation could not run while resident was waiting: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resident did not stop after cancellation")
	}
}

func TestRunLoopKeepsDistinctSingleResidentLock(t *testing.T) {
	operationLock := privateTestPath(t, "agent.lock")
	residentLock := operationLock + ".resident"
	first, err := acquireProcessLock(residentLock)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	err = runLoop(context.Background(), func(context.Context) (SyncSummary, error) {
		return SyncSummary{}, nil
	}, RunOptions{
		LockPath: operationLock, ResidentLockPath: residentLock,
		Interval: time.Minute, MinBackoff: time.Second, MaxBackoff: time.Second,
	})
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second resident error=%v", err)
	}
	if err := WithProcessLock(operationLock, func() error { return nil }); err != nil {
		t.Fatalf("resident instance lock incorrectly blocked a one-shot operation: %v", err)
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
	if event.Code != "sync_deferred_busy" || event.Successful || delay != options.Interval || nextBackoff != options.MinBackoff {
		t.Fatalf("busy host response polluted failure backoff: event=%#v delay=%v next=%v", event, delay, nextBackoff)
	}
	locked, lockedDelay, lockedBackoff := classifyRunResult(SyncSummary{},
		fmt.Errorf("wrapped operation lock: %w", ErrAlreadyRunning), options, 8*time.Second)
	if locked.Code != "sync_deferred_busy" || locked.Successful ||
		lockedDelay != options.MinBackoff || lockedBackoff != options.MinBackoff {
		t.Fatalf("concurrent one-shot polluted failure backoff: event=%#v delay=%v next=%v",
			locked, lockedDelay, lockedBackoff)
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
