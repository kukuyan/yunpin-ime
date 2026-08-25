// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"path/filepath"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

type RunEvent struct {
	Code         string
	FailureClass string
	Successful   bool
	Summary      SyncSummary
}

func validateRunEvent(event RunEvent) error {
	switch event.Code {
	case "sync_complete":
		if !event.Successful || event.FailureClass != localstore.SyncFailureNone {
			return errors.New("successful run event is inconsistent")
		}
	case "sync_deferred_busy":
		if event.Successful || event.FailureClass != localstore.SyncFailureNone {
			return errors.New("deferred run event is inconsistent")
		}
	case "sync_failed":
		if event.Successful || (event.FailureClass != localstore.SyncFailureNetwork &&
			event.FailureClass != localstore.SyncFailureAuth &&
			event.FailureClass != localstore.SyncFailureRelayProtocol &&
			event.FailureClass != localstore.SyncFailureLocalStore) {
			return errors.New("failed run event is inconsistent")
		}
	default:
		return errors.New("unknown run event code")
	}
	return nil
}

type RunOptions struct {
	// LockPath serializes one state-changing operation. It is held only for a
	// synchronization round, so an explicit sync-once can run while the
	// resident is waiting for its next interval.
	LockPath string
	// ResidentLockPath keeps exactly one background loop alive. When omitted it
	// is derived from LockPath, preserving the fixed platform state root without
	// adding a caller-controlled location.
	ResidentLockPath string
	Interval         time.Duration
	MinBackoff       time.Duration
	MaxBackoff       time.Duration
	OnEvent          func(RunEvent)
}

func (options *RunOptions) defaults() error {
	if options.LockPath == "" || !filepath.IsAbs(options.LockPath) {
		return errors.New("agent operation lock path must be absolute")
	}
	if options.ResidentLockPath == "" {
		options.ResidentLockPath = options.LockPath + ".resident"
	}
	if !filepath.IsAbs(options.ResidentLockPath) ||
		filepath.Clean(options.ResidentLockPath) == filepath.Clean(options.LockPath) {
		return errors.New("resident instance lock must be a distinct absolute path")
	}
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
	if errors.Is(syncErr, ErrRimeMaintenanceBusy) || errors.Is(syncErr, ErrAlreadyRunning) {
		return RunEvent{Code: "sync_deferred_busy", FailureClass: localstore.SyncFailureNone}, options.MinBackoff, options.MinBackoff
	}
	if syncErr != nil {
		nextBackoff := options.MaxBackoff
		if backoff < options.MaxBackoff/2 {
			nextBackoff = backoff * 2
		}
		return RunEvent{Code: "sync_failed", FailureClass: classifySyncFailure(syncErr)}, backoff, nextBackoff
	}
	return RunEvent{Code: "sync_complete", FailureClass: localstore.SyncFailureNone, Successful: true, Summary: summary}, options.Interval, options.MinBackoff
}

// classifySyncFailure maps errors to a closed, redacted boundary. It never
// serializes the underlying message, endpoint, token, account or device.
func classifySyncFailure(syncErr error) string {
	var apiError *syncclient.APIError
	if errors.As(syncErr, &apiError) {
		if apiError.Status == 401 || apiError.Status == 403 {
			return localstore.SyncFailureAuth
		}
		return localstore.SyncFailureRelayProtocol
	}
	var networkError *syncclient.NetworkError
	if errors.As(syncErr, &networkError) {
		return localstore.SyncFailureNetwork
	}
	var relayError *syncclient.RelayProtocolError
	if errors.As(syncErr, &relayError) {
		return localstore.SyncFailureRelayProtocol
	}
	var rejection *syncclient.UploadRejectionError
	if errors.As(syncErr, &rejection) {
		return localstore.SyncFailureRelayProtocol
	}
	var storeError *syncclient.LocalStoreError
	if errors.As(syncErr, &storeError) {
		return localstore.SyncFailureLocalStore
	}
	// Configuration, local ingestion, snapshot and platform reload failures all
	// occur on the device side. New remote boundaries must introduce a typed
	// error above rather than teaching this function to parse text.
	return localstore.SyncFailureLocalStore
}

func runLoop(ctx context.Context, syncNow func(context.Context) (SyncSummary, error), options RunOptions) error {
	if syncNow == nil {
		return errors.New("sync operation is required")
	}
	if err := options.defaults(); err != nil {
		return err
	}
	residentLock, err := acquireProcessLock(options.ResidentLockPath)
	if err != nil {
		return err
	}
	defer residentLock.Release()
	backoff := options.MinBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}
		var summary SyncSummary
		syncErr := WithProcessLock(options.LockPath, func() error {
			var operationErr error
			summary, operationErr = syncNow(ctx)
			return operationErr
		})
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
	return runLoop(ctx, agent.SyncOnce, options)
}

// recordSyncHealth stores a round's outcome through the same open Store used
// by the synchronization operation. It is best effort by design: diagnostics
// can never turn a completed synchronization into a failed one.
func recordSyncHealth(ctx context.Context, store *localstore.Store, summary SyncSummary, syncErr error) {
	healthContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	health, err := store.LoadSyncHealth(healthContext)
	if err != nil {
		return
	}
	event, _, _ := classifyRunResult(summary, syncErr, RunOptions{
		Interval: time.Minute, MinBackoff: time.Second, MaxBackoff: time.Second,
	}, time.Second)
	now := time.Now().UnixMilli()
	health.LastEventAt = now
	health.LastEventCode = event.Code
	health.LastFailureClass = event.FailureClass
	if event.Successful {
		// A failed round must not clear this: "when did this last work" is the
		// most useful thing to know once it stops working.
		health.LastSuccessAt = now
		health.Cursor = summary.Cursor
	}
	if pending, err := store.PendingEventCount(healthContext); err == nil {
		health.PendingUploads = int64(pending)
	}
	_ = store.RecordSyncHealth(healthContext, health)
}
