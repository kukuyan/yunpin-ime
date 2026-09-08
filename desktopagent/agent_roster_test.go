// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

func TestAgentRefreshesTrustBeforeAcceptingNewDeviceEnvelope(t *testing.T) {
	for _, scenario := range []string{"valid", "tampered_chain", "save_failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			bundle := pairedResidentCredential(t)
			defer bundle.Zero()
			secrets, agent := residentReadyFixture(t, bundle)
			// This fixture tests transport/trust, not a live platform reload.
			agent.BaselinePath, agent.SnapshotPath = "", ""
			before := append([]byte(nil), secrets.values["default"]...)
			third, err := protocol.NewDeviceKeys(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			thirdID := filled16(0x88)
			thirdHex := hex.EncodeToString(thirdID[:])
			next, err := protocol.SignPairingRosterAdvance(bundle.TrustedRoster,
				protocol.PairingRosterDevice{DeviceID: thirdID[:], Ed25519PublicKey: third.Ed25519Public, X25519PublicKey: third.X25519Public},
				bundle.DeviceID[:], ed25519.NewKeyFromSeed(bundle.SigningSeed[:]))
			if err != nil {
				t.Fatal(err)
			}
			digest, err := protocol.PairingRosterDigest(next)
			if err != nil {
				t.Fatal(err)
			}
			remote, err := localstore.OpenForDevice(ctx, filepath.Join(t.TempDir(), "synthetic.db"),
				bytes.Repeat([]byte{0xa1}, 32), bundle.ObjectIDKey[:], thirdHex)
			if err != nil {
				t.Fatal(err)
			}
			defer remote.Close()
			if err := remote.SaveExplicit(ctx, localstore.Phrase{Text: "合成新设备学习", Pinyin: "he cheng xin she bei xue xi", Pinned: true}); err != nil {
				t.Fatal(err)
			}
			events, err := remote.PendingEvents(ctx, 1)
			if err != nil || len(events) != 1 {
				t.Fatal("synthetic upload missing")
			}
			epoch := bundle.EpochKeys[bundle.CurrentEpoch]
			envelope, err := events[0].SealEnvelope(localstore.EnvelopeOptions{AccountID: bundle.AccountID[:], DeviceID: thirdID[:],
				KeyEpoch: bundle.CurrentEpoch, DeviceSeq: 1, EpochKey: epoch[:], SigningPrivate: third.Ed25519Private, Random: rand.Reader})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := envelope.ToWire()
			if err != nil {
				t.Fatal(err)
			}
			wire.DeviceID, wire.Cursor = thirdHex, 1
			if scenario == "tampered_chain" {
				next.PreviousHash[0] ^= 1
			}
			if scenario == "save_failure" {
				agent.Secrets = &failProfileSecretStore{memorySecretStore: secrets, profile: "default", failures: 1}
			}
			var syncCalls, refreshCalls atomic.Int32
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/v1/roster":
					refreshCalls.Add(1)
					updates := []protocol.PairingRoster{next}
					if r.URL.Query().Get("after_version") == "2" {
						updates = nil
					}
					_ = json.NewEncoder(w).Encode(syncclient.RosterUpdates{HeadVersion: 2, HeadHash: hex.EncodeToString(digest), Updates: updates})
				case strings.HasSuffix(r.URL.Path, "/seal"):
					w.WriteHeader(http.StatusNoContent)
				case r.URL.Path == "/v1/sync":
					syncCalls.Add(1)
					var request syncclient.SyncRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						http.Error(w, "invalid", 400)
						return
					}
					response := syncclient.SyncResponse{NextCursor: request.Cursor, CurrentKeyEpoch: bundle.CurrentEpoch}
					if request.Cursor == 0 {
						response.Envelopes, response.NextCursor = []protocol.WireEnvelope{wire}, 1
					}
					_ = json.NewEncoder(w).Encode(response)
				default:
					http.NotFound(w, r)
				}
			}))
			defer relay.Close()
			if err := ConfigureEndpoint(agent.EndpointConfigPath, relay.URL, true); err != nil {
				t.Fatal(err)
			}
			result, err := agent.syncOnceWithBundle(ctx, &bundle)
			if scenario == "valid" {
				if err != nil || result.Downloaded != 1 || result.Cursor != 1 || len(bundle.VerificationKeys) != 3 {
					t.Fatalf("new-device download failed: result=%+v err=%v", result, err)
				}
				// A second resident cycle uses the now-current in-memory checkpoint.
				if _, err := agent.syncOnceWithBundle(ctx, &bundle); err != nil {
					t.Fatal(err)
				}
				if refreshCalls.Load() != 2 {
					t.Fatal("unexpected refresh attempts")
				}
			} else {
				if err == nil || syncCalls.Load() != 0 || !bytes.Equal(before, secrets.values["default"]) || len(bundle.VerificationKeys) != 2 {
					t.Fatal("uncommitted trust allowed sync or changed the active checkpoint")
				}
				if scenario == "tampered_chain" && classifySyncFailure(err) != localstore.SyncFailureRelayProtocol {
					t.Fatal("invalid signature was classified as a database failure")
				}
			}
		})
	}
}
