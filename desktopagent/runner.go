// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
)

type RunEvent struct {
	Code       string
	Successful bool
	Summary    SyncSummary
}

type RunOptions struct {
	LockPath   string
	Interval   time.Duration
	MinBackoff time.Duration
	MaxBackoff time.Duration
	OnEvent    func(RunEvent)
}

func (options *RunOptions) defaults() error {
	if options.Interval == 0 {
		options.Interval = time.Minute
	}
	if options.MinBackoff == 0 {
		options.MinBackoff = 5 * time.Second
	}
	if options.MaxBackoff == 0 {
		options.MaxBackoff = 15 * time.Minute
	}
	if options.Interval < time.Second || options.MinBackoff < time.Second || options.MaxBackoff < options.MinBackoff {
		return errors.New("agent timing configuration is invalid")
	}
	return nil
}

func jitter(duration time.Duration) time.Duration {
	// Apply 90-110% jitter without a pseudo-random global seed. Failure of the
	// random source safely falls back to the supplied duration.
	value, err := rand.Int(rand.Reader, big.NewInt(21))
	if err != nil {
		return duration
	}
	return duration * time.Duration(90+value.Int64()) / 100
}

func classifyRunResult(summary SyncSummary, syncErr error, options RunOptions, backoff time.Duration) (RunEvent, time.Duration, time.Duration) {
	if errors.Is(syncErr, ErrRimeMaintenanceBusy) {
		return RunEvent{Code: "sync_deferred_busy"}, options.MinBackoff, options.MinBackoff
	}
	if syncErr != nil {
		nextBackoff := options.MaxBackoff
		if backoff < options.MaxBackoff/2 {
			nextBackoff = backoff * 2
		}
		return RunEvent{Code: "sync_failed"}, backoff, nextBackoff
	}
	return RunEvent{Code: "sync_complete", Successful: true, Summary: summary}, options.Interval, options.MinBackoff
}

func runLoop(ctx context.Context, syncNow func(context.Context) (SyncSummary, error), options RunOptions) error {
	if syncNow == nil {
		return errors.New("sync operation is required")
	}
	if err := options.defaults(); err != nil {
		return err
	}
	lock, err := acquireProcessLock(options.LockPath)
	if err != nil {
		return err
	}
	defer lock.Release()
	backoff := options.MinBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}
		summary, syncErr := syncNow(ctx)
		event, delay, nextBackoff := classifyRunResult(summary, syncErr, options, backoff)
		backoff = nextBackoff
		if options.OnEvent != nil {
			options.OnEvent(event)
		}
		timer := time.NewTimer(jitter(delay))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func (agent Agent) Run(ctx context.Context, options RunOptions) error {
	if agent.RimeUserDBExportPath != "" && agent.RimeUserDBRefresh == nil {
		return errors.New("resident Rime userdb ingestion requires the fixed platform maintenance refresher")
	}
	// Persist the outcome of every round before handing the event on. Without
	// this the only externally visible fact about background synchronization is
	// that a process exists, which is precisely what gets mistaken for it
	// working.
	caller := options.OnEvent
	options.OnEvent = func(event RunEvent) {
		agent.recordSyncHealth(ctx, event)
		if caller != nil {
			caller(event)
		}
	}
	return runLoop(ctx, agent.SyncOnce, options)
}

// recordSyncHealth stores one round's outcome. It is best effort by design:
// health is diagnostics, and failing to write it must never stop or fail the
// synchronization it is describing.
func (agent Agent) recordSyncHealth(ctx context.Context, event RunEvent) {
	bundle, err := agent.loadBundle(ctx)
	if err != nil {
		return
	}
	defer bundle.Zero()
	store, err := localstore.OpenForDevice(
		ctx, agent.DatabasePath, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex(),
	)
	if err != nil {
		return
	}
	defer store.Close()

	health, err := store.LoadSyncHealth(ctx)
	if err != nil {
		return
	}
	now := time.Now().UnixMilli()
	health.LastEventAt = now
	health.LastEventCode = event.Code
	if event.Successful {
		// A failed round must not clear this: "when did this last work" is the
		// most useful thing to know once it stops working.
		health.LastSuccessAt = now
		health.Cursor = event.Summary.Cursor
	}
	if pending, err := store.PendingEventCount(ctx); err == nil {
		health.PendingUploads = int64(pending)
	}
	_ = store.RecordSyncHealth(ctx, health)
}
