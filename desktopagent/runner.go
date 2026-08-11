// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"time"
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
		delay := options.Interval
		event := RunEvent{Code: "sync_complete", Successful: true, Summary: summary}
		if syncErr != nil {
			delay = backoff
			event = RunEvent{Code: "sync_failed"}
			if backoff < options.MaxBackoff/2 {
				backoff *= 2
			} else {
				backoff = options.MaxBackoff
			}
		} else {
			backoff = options.MinBackoff
		}
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
	return runLoop(ctx, agent.SyncOnce, options)
}
