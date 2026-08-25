// SPDX-License-Identifier: Apache-2.0
package syncclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
)

type workerRoundTripFunc func(*http.Request) (*http.Response, error)

func (function workerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func workerJSONResponse(request *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestWorkerRejectsUnrelatedAcknowledgementAndKeepsExactRetry(t *testing.T) {
	transport := workerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/sync" {
			t.Fatalf("unexpected request path: %s", request.URL.Path)
		}
		return workerJSONResponse(request, `{"accepted_sequences":[999],"rejected_sequences":[],"envelopes":[],"next_cursor":0,"has_more":false,"current_key_epoch":0}`), nil
	})
	endpoint, err := ParseEndpoint("https://relay.invalid", EndpointPolicy{})
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
		Client: New(endpoint, WithTransport(transport)), Store: store,
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
	} else {
		var protocolError *RelayProtocolError
		if !errors.As(err, &protocolError) {
			t.Fatalf("invalid relay response lost its typed boundary: %T %v", err, err)
		}
	}
	state, err := store.LoadSyncState(context.Background())
	if err != nil || state.Prepared == nil || state.NextDeviceSequence != 1 {
		t.Fatalf("prepared retry was lost after invalid response: state=%#v err=%v", state, err)
	}
	if count, err := store.PendingEventCount(context.Background()); err != nil || count != 1 {
		t.Fatalf("outbox changed after invalid response: count=%d err=%v", count, err)
	}
}

func TestWorkerReturnsTypedSequenceRejectionAndKeepsPreparedUpload(t *testing.T) {
	transport := workerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/sync" {
			t.Fatalf("unexpected request path: %s", request.URL.Path)
		}
		return workerJSONResponse(request, `{"accepted_sequences":[],"rejected_sequences":[{"device_seq":1,"code":"previous_hash_mismatch"}],"envelopes":[],"next_cursor":0,"has_more":false,"current_key_epoch":1}`), nil
	})
	endpoint, err := ParseEndpoint("https://relay.invalid", EndpointPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	deviceID := bytes.Repeat([]byte{0x51}, 16)
	store, err := localstore.OpenForDevice(context.Background(), filepath.Join(t.TempDir(), "private.db"),
		bytes.Repeat([]byte{0x52}, 32), bytes.Repeat([]byte{0x53}, 32), hex.EncodeToString(deviceID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveExplicit(context.Background(), localstore.Phrase{
		Text: "合成链冲突", Pinyin: "he cheng lian chong tu", Pinned: true,
	}); err != nil {
		t.Fatal(err)
	}
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x54}, ed25519.SeedSize))
	worker := Worker{
		Client: New(endpoint, WithTransport(transport)), Store: store,
		Session: Session{
			AccountID: bytes.Repeat([]byte{0x55}, 16), DeviceID: deviceID, DeviceToken: "synthetic-token",
			KeyEpoch: 1, EpochKeys: map[uint64][]byte{1: bytes.Repeat([]byte{0x56}, 32)},
			SigningPrivate: private, VerificationKeys: map[string]ed25519.PublicKey{
				hex.EncodeToString(deviceID): private.Public().(ed25519.PublicKey),
			},
		},
	}
	_, err = worker.SyncOnce(context.Background())
	var rejection *UploadRejectionError
	if !errors.As(err, &rejection) || rejection.Code != "previous_hash_mismatch" {
		t.Fatalf("sequence-chain rejection lost its typed classification: %T %v", err, err)
	}
	state, err := store.LoadSyncState(context.Background())
	if err != nil || state.Prepared == nil || state.NextDeviceSequence != 1 {
		t.Fatalf("prepared upload was lost after sequence rejection: state=%#v err=%v", state, err)
	}
	if count, err := store.PendingEventCount(context.Background()); err != nil || count != 1 {
		t.Fatalf("outbox changed after sequence rejection: count=%d err=%v", count, err)
	}
}
