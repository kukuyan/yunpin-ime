// SPDX-License-Identifier: Apache-2.0
//go:build windows

package desktopagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
)

func recordPlatformSyncStart(ctx context.Context, store *localstore.Store) error {
	if err := store.EnsureWindowsSyncSchedule(ctx); err != nil {
		return err
	}
	// A manual request may arrive before the resident has reconciled an
	// interrupted previous round. Preserve its unknown time boundary first.
	if err := store.RecoverWindowsSyncDelay(ctx, time.Now()); err != nil {
		return err
	}
	if err := store.BeginWindowsSync(ctx); err != nil {
		return err
	}
	if err := store.RecordSyncStarted(ctx, time.Now()); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		return errors.Join(err, store.FinishWindowsSync(cleanupCtx))
	}
	return nil
}
func recordPlatformNetworkCompleted(ctx context.Context, store *localstore.Store) error {
	return store.RecordWindowsNetworkCompleted(ctx, time.Now())
}
func finishPlatformSync(ctx context.Context, store *localstore.Store) error {
	return store.FinishWindowsSync(ctx)
}

// SQLite may create WAL sidecars with inherited DACLs even for a read-only
// connection. The private parent directory limits access while the connection
// is open; after it closes, protect and verify its files under the operation
// lock, including when the read failed. Existing files are checked beforehand.
func readPrivateWindowsScheduleHealth(ctx context.Context, path string) (localstore.SyncHealth, error) {
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return localstore.SyncHealth{}, err
	}
	if !privateDirectoryPermissionsOK(filepath.Dir(path), parent) {
		return localstore.SyncHealth{}, ErrPrivateACLInvalid
	}
	if err := verifyPrivateDatabaseFiles(path); err != nil {
		return localstore.SyncHealth{}, err
	}
	health, readErr := localstore.ReadSyncHealthForSchedule(ctx, path)
	return health, errors.Join(readErr, protectPrivateDatabaseFiles(path))
}

func (agent Agent) runPlatformResident(ctx context.Context, bundle *CredentialBundleV1, options RunOptions) (returnErr error) {
	session, err := currentWindowsSessionID()
	if err != nil {
		return err
	}
	// Initialize only the small scheduling checkpoint under the same lock as
	// manual sync, including on databases created by older installations.
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := WithProcessLock(options.LockPath, func() error {
			info, err := os.Lstat(agent.DatabasePath)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("sync database is unavailable")
			}
			if !privateFilePermissionsOK(agent.DatabasePath, info) {
				return ErrPrivateACLInvalid
			}
			if err := verifyPrivateDatabaseFiles(agent.DatabasePath); err != nil {
				return err
			}
			store, err := localstore.OpenForDevice(ctx, agent.DatabasePath, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex())
			if err != nil {
				return err
			}
			err = store.EnsureWindowsSyncSchedule(ctx)
			if err == nil {
				err = store.RecoverWindowsSyncDelay(ctx, time.Now())
			}
			return errors.Join(err, store.Close(), protectPrivateDatabaseFiles(agent.DatabasePath))
		})
		if !errors.Is(err, ErrAlreadyRunning) {
			if err != nil {
				return err
			}
			break
		}
		timer := time.NewTimer(options.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	events, err := startWindowsSessionEvents()
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, events.close()) }()
	if options.VerifyScheduleOnce {
		err := WithProcessLock(options.LockPath, func() error {
			readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_, err := readPrivateWindowsScheduleHealth(readCtx, agent.DatabasePath)
			return err
		})
		if err != nil {
			if options.OnEvent != nil {
				event, _, _ := classifyRunResult(SyncSummary{}, err, options, options.MinBackoff)
				options.OnEvent(event)
			}
			return err
		}
		if options.OnEvent != nil {
			options.OnEvent(RunEvent{Code: "schedule_check_ok", FailureClass: localstore.SyncFailureNone})
		}
		return nil
	}
	if options.OnStarted != nil {
		options.OnStarted()
	}
	return runWindowsIdleLoop(ctx, options, windowsIdleOperations{
		session: session,
		observe: func() (windowsIdleSample, error) { return events.observe(session) },
		load: func(readCtx context.Context) (localstore.SyncHealth, error) {
			if agent.DatabasePath == "" || !filepath.IsAbs(agent.DatabasePath) {
				return localstore.SyncHealth{}, errors.New("invalid sync database path")
			}
			info, err := os.Lstat(agent.DatabasePath)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return localstore.SyncHealth{}, errors.New("sync database is unavailable")
			}
			if !privateFilePermissionsOK(agent.DatabasePath, info) {
				return localstore.SyncHealth{}, ErrPrivateACLInvalid
			}
			return readPrivateWindowsScheduleHealth(readCtx, agent.DatabasePath)
		},
		locked: func(f func() error) error { return WithProcessLock(options.LockPath, f) },
		sync:   func(roundCtx context.Context) (SyncSummary, error) { return agent.syncOnceWithBundle(roundCtx, bundle) },
		now:    time.Now,
		wait: func(waitCtx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-waitCtx.Done():
				return waitCtx.Err()
			case <-timer.C:
				return nil
			}
		},
	})
}
