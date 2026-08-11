// SPDX-License-Identifier: Apache-2.0
package syncclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
)

func TestWorkerRejectsUnrelatedAcknowledgementAndKeepsExactRetry(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/sync" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"accepted_sequences":[999],"rejected_sequences":[],"envelopes":[],"next_cursor":0,"has_more":false,"current_key_epoch":0}`))
	}))
	defer relay.Close()
	endpoint, err := ParseEndpoint(relay.URL, EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	deviceID := bytes.Repeat([]byte{0x71}, 16)
	store, err := localstore.OpenForDevice(context.Background(), filepath.Join(t.TempDir(), "private.db"),
		bytes.Repeat([]byte{0x72}, 32), bytes.Repeat([]byte{0x73}, 32), hex.EncodeToString(deviceID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveExplicit(context.Background(), localstore.Phrase{
		Text: "合成响应校验", Pinyin: "he cheng xiang ying jiao yan", Pinned: true,
	}); err != nil {
		t.Fatal(err)
	}
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x74}, ed25519.SeedSize))
	worker := Worker{
		Client: New(endpoint), Store: store,
		Session: Session{
			AccountID: bytes.Repeat([]byte{0x75}, 16), DeviceID: deviceID, DeviceToken: "synthetic-token",
			KeyEpoch: 1, EpochKeys: map[uint64][]byte{1: bytes.Repeat([]byte{0x76}, 32)},
			SigningPrivate: private, VerificationKeys: map[string]ed25519.PublicKey{
				hex.EncodeToString(deviceID): private.Public().(ed25519.PublicKey),
			},
		},
	}
	if _, err := worker.SyncOnce(context.Background()); err == nil {
		t.Fatal("relay acknowledgement for an unrelated sequence was accepted")
	}
	state, err := store.LoadSyncState(context.Background())
	if err != nil || state.Prepared == nil || state.NextDeviceSequence != 1 {
		t.Fatalf("prepared retry was lost after invalid response: state=%#v err=%v", state, err)
	}
	if count, err := store.PendingEventCount(context.Background()); err != nil || count != 1 {
		t.Fatalf("outbox changed after invalid response: count=%d err=%v", count, err)
	}
}
