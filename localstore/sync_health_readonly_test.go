// SPDX-License-Identifier: Apache-2.0
package localstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadSyncHealthForScheduleUsesExistingDatabaseAndNeverCreatesOne(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing.db")
	if _, err := ReadSyncHealthForSchedule(ctx, missing); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("read-only scheduling created a database")
	}
	store, path := openTestStore(t)
	if err := store.EnsureWindowsSyncSchedule(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := ReadSyncHealthForSchedule(ctx, path)
	if err != nil || h.LastSuccessAt != 0 || h.LastEventAt != 0 {
		t.Fatalf("empty health: %+v %v", h, err)
	}
	want := SyncHealth{LastSuccessAt: 123, LastEventAt: 456, LastEventCode: "sync_failed", LastFailureClass: SyncFailureNetwork}
	if err := store.RecordSyncHealth(ctx, want); err != nil {
		t.Fatal(err)
	}
	h, err = ReadSyncHealthForSchedule(ctx, path)
	if err != nil || h.LastSuccessAt != want.LastSuccessAt || h.LastEventAt != want.LastEventAt || h.LastEventCode != want.LastEventCode {
		t.Fatalf("read health: %+v %v", h, err)
	}
}

func TestRecordSyncStartedPreservesLastSuccessForReadOnlyScheduling(t *testing.T) {
	ctx := context.Background()
	store, path := openTestStore(t)
	if err := store.EnsureWindowsSyncSchedule(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSyncHealth(ctx, SyncHealth{LastSuccessAt: 123, LastEventAt: 123, LastEventCode: "sync_complete", LastFailureClass: SyncFailureNone}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSyncStarted(ctx, time.UnixMilli(456)); err != nil {
		t.Fatal(err)
	}
	h, err := ReadSyncHealthForSchedule(ctx, path)
	if err != nil || h.LastSuccessAt != 123 || h.LastEventAt != 456 || h.LastEventCode != "sync_started" {
		t.Fatalf("started health: %+v %v", h, err)
	}
}

func TestWindowsScheduleSurvivesNetworkSuccessAndLaterFailure(t *testing.T) {
	ctx := context.Background()
	store, path := openTestStore(t)
	if err := store.RecordSyncHealth(ctx, SyncHealth{LastSuccessAt: 123, LastEventAt: 123, LastEventCode: "sync_complete", LastFailureClass: SyncFailureNone}); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureWindowsSyncSchedule(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginWindowsSync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWindowsNetworkCompleted(ctx, time.UnixMilli(456)); err != nil {
		t.Fatal(err)
	}
	// A later /deploy failure must not discard the network completion time.
	if err := store.FinishWindowsSync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSyncHealth(ctx, SyncHealth{LastSuccessAt: 123, LastEventAt: 500, LastEventCode: "sync_failed", LastFailureClass: SyncFailureLocalStore}); err != nil {
		t.Fatal(err)
	}
	h, err := ReadSyncHealthForSchedule(ctx, path)
	if err != nil || h.LastSuccessAt != 123 || h.LastNetworkAt != 456 || h.SyncInFlight {
		t.Fatalf("checkpoint: %+v %v", h, err)
	}
	if err := store.BeginWindowsSync(ctx); err != nil {
		t.Fatal(err)
	}
	h, err = ReadSyncHealthForSchedule(ctx, path)
	if err != nil || !h.SyncInFlight {
		t.Fatalf("interrupted round: %+v %v", h, err)
	}
	if err := store.RecoverWindowsSyncDelay(ctx, time.UnixMilli(1000)); err != nil {
		t.Fatal(err)
	}
	h, err = ReadSyncHealthForSchedule(ctx, path)
	if err != nil || h.SyncInFlight || h.LastNetworkAt != 1000 || h.LastSuccessAt != 123 {
		t.Fatalf("recovered unknown outcome must wait without claiming success: %+v %v", h, err)
	}
	// An immediate manual round can fail before it reaches the network. It
	// must not erase the previous unknown round's conservative delay.
	if err := store.BeginWindowsSync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishWindowsSync(ctx); err != nil {
		t.Fatal(err)
	}
	h, err = ReadSyncHealthForSchedule(ctx, path)
	if err != nil || h.SyncInFlight || h.LastNetworkAt != 1000 {
		t.Fatalf("manual preflight lost prior unknown: %+v %v", h, err)
	}
}

func TestWindowsScheduleReconcilesLegacyHealthConservatively(t *testing.T) {
	for _, tc := range []struct {
		name       string
		checkpoint bool
		inFlight   bool
		health     SyncHealth
		want       int64
	}{
		{"first migration after unknown failure", false, false, SyncHealth{LastSuccessAt: 100, LastEventAt: 200, LastEventCode: "sync_failed", LastFailureClass: SyncFailureNetwork}, 200},
		{"existing checkpoint after legacy failure", true, false, SyncHealth{LastSuccessAt: 100, LastEventAt: 300, LastEventCode: "sync_failed", LastFailureClass: SyncFailureLocalStore}, 300},
		{"existing checkpoint after legacy success", true, false, SyncHealth{LastSuccessAt: 400, LastEventAt: 400, LastEventCode: "sync_complete", LastFailureClass: SyncFailureNone}, 400},
		{"legacy interrupted start retains marker", true, true, SyncHealth{LastSuccessAt: 100, LastEventAt: 500, LastEventCode: "sync_started", LastFailureClass: SyncFailureNone}, 500},
		{"old health cannot move checkpoint backwards", true, false, SyncHealth{LastSuccessAt: 10, LastEventAt: 20, LastEventCode: "sync_failed", LastFailureClass: SyncFailureNetwork}, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, path := openTestStore(t)
			if tc.checkpoint {
				if err := store.RecordSyncHealth(ctx, SyncHealth{LastSuccessAt: 100, LastEventAt: 100, LastEventCode: "sync_complete", LastFailureClass: SyncFailureNone}); err != nil {
					t.Fatal(err)
				}
				if err := store.EnsureWindowsSyncSchedule(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if tc.inFlight {
				if err := store.BeginWindowsSync(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.RecordSyncHealth(ctx, tc.health); err != nil {
				t.Fatal(err)
			}
			if err := store.EnsureWindowsSyncSchedule(ctx); err != nil {
				t.Fatal(err)
			}
			h, err := ReadSyncHealthForSchedule(ctx, path)
			if err != nil || h.LastNetworkAt != tc.want || h.SyncInFlight != tc.inFlight {
				t.Fatalf("checkpoint=%+v, want network=%d in-flight=%v; err=%v", h, tc.want, tc.inFlight, err)
			}
		})
	}
}
