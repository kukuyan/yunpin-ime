// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package desktopagent

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotReloadRequiresAppliedIdentity(t *testing.T) {
	for _, scenario := range []string{"applied", "busy", "failed", "wrong_nonce", "wrong_generation", "wrong_digest", "no_host", "lost_ack", "late", "bad_session", "bad_pid", "unknown_result", "public_ack", "oversized_ack"} {
		t.Run(scenario, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "private")
			makePrivateTestDirectory(t, directory)
			directory, _ = filepath.EvalSymlinks(directory)
			request := snapshotReloadRequest{Generation: 23, Digest: sha256.Sum256([]byte("public fixture"))}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			invoke := func(ctx context.Context, nonce string) error {
				if scenario == "no_host" || scenario == "lost_ack" {
					return nil
				}
				encoded, err := os.ReadFile(filepath.Join(directory, snapshotReloadRequestName))
				if err != nil {
					return err
				}
				fields := strings.Split(strings.TrimSpace(string(encoded)), "\t")
				if len(fields) != 4 || fields[1] != nonce {
					t.Fatal("request is not nonce-bound")
				}
				outcome := "applied"
				hostSession, pid := strings.Repeat("a", 32), "123"
				mode := os.FileMode(0600)
				switch scenario {
				case "busy":
					outcome = "busy"
				case "failed":
					outcome = "failed"
				case "wrong_nonce":
					fields[1] = strings.Repeat("f", 32)
				case "wrong_generation":
					fields[2] = "22"
				case "wrong_digest":
					fields[3] = strings.Repeat("0", 64)
				case "late":
					<-ctx.Done()
				case "bad_session":
					hostSession = "invalid"
				case "bad_pid":
					pid = "0"
				case "unknown_result":
					outcome = "delivered"
				case "public_ack":
					mode = 0644
				}
				ack := strings.Join(append(fields, hostSession, pid, outcome), "\t") + "\n"
				if scenario == "oversized_ack" {
					ack = strings.Repeat("x", 513)
				}
				return os.WriteFile(filepath.Join(directory, snapshotReloadAckName), []byte(ack), mode)
			}
			err := reloadSnapshotWithAck(ctx, directory, request, invoke)
			if scenario == "applied" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("notification or non-matching receipt was accepted as applied")
			}
			if scenario == "busy" && !errors.Is(err, ErrRimeMaintenanceBusy) {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(directory, snapshotReloadRequestName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("completed request was not removed")
			}
		})
	}
}

func TestSnapshotReloadLostAckRetriesSameDigest(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	makePrivateTestDirectory(t, directory)
	directory, _ = filepath.EvalSymlinks(directory)
	digest := sha256.Sum256([]byte("public fixture"))
	state := filepath.Join(directory, "snapshot.state")
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := reloadSnapshotWithAck(ctx, directory, snapshotReloadRequest{Generation: 1, Digest: digest},
		func(context.Context, string) error { return nil }); err == nil {
		t.Fatal("lost ACK accepted")
	}
	if pending, err := snapshotReloadPending(state, digest); err != nil || !pending {
		t.Fatal("unacknowledged digest was suppressed")
	}
}
