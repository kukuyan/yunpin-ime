// SPDX-License-Identifier: Apache-2.0
package syncclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
)

// Session is supplied by a native OS-keychain adapter for one run. The worker
// never writes this secret material to disk. VerificationKeys must come from
// an authenticated pairing/recovery record, not an untrusted relay response.
type Session struct {
	AccountID        []byte
	DeviceID         []byte
	DeviceToken      string
	KeyEpoch         uint64
	EpochKeys        map[uint64][]byte
	SigningPrivate   ed25519.PrivateKey
	VerificationKeys map[string]ed25519.PublicKey
}

func (session Session) validate() error {
	if len(session.AccountID) != 16 || len(session.DeviceID) != 16 || session.DeviceToken == "" ||
		session.KeyEpoch < 1 || session.KeyEpoch > math.MaxInt64 || len(session.SigningPrivate) != ed25519.PrivateKeySize {
		return errors.New("invalid sync session")
	}
	if len(session.EpochKeys[session.KeyEpoch]) != 32 {
		return errors.New("current epoch key is unavailable")
	}
	self := hex.EncodeToString(session.DeviceID)
	selfVerification := session.VerificationKeys[self]
	if len(selfVerification) != ed25519.PublicKeySize ||
		!bytes.Equal(session.SigningPrivate.Public().(ed25519.PublicKey), selfVerification) {
		return errors.New("current device verification key is unavailable")
	}
	for deviceID, key := range session.VerificationKeys {
		decoded, err := hex.DecodeString(deviceID)
		if err != nil || len(decoded) != 16 || deviceID != hex.EncodeToString(decoded) || len(key) != ed25519.PublicKeySize {
			return errors.New("invalid trusted device verification key")
		}
	}
	return nil
}

type Worker struct {
	Client  *Client
	Store   *localstore.Store
	Session Session
	Random  io.Reader
}

type Result struct {
	Uploaded           bool
	AcknowledgedOutbox bool
	Downloaded         int
	Cursor             int64
	HasMore            bool
}

// UploadRejectionError is returned only for the relay's closed set of
// fail-closed sequence-chain rejection codes. Callers can classify the error
// without parsing or exposing transport text; the prepared upload remains
// checkpointed for explicit diagnosis or repair.
type UploadRejectionError struct {
	Code string
}

func (err *UploadRejectionError) Error() string {
	return "sync relay rejected the prepared upload"
}

func (worker *Worker) prepare(ctx context.Context, state localstore.SyncState) (localstore.SyncState, error) {
	if state.Prepared != nil {
		return state, nil
	}
	events, err := worker.Store.PendingEvents(ctx, 1)
	if err != nil {
		return state, localStoreError(err)
	}
	if len(events) == 0 {
		return state, nil
	}
	random := worker.Random
	if random == nil {
		random = rand.Reader
	}
	envelope, err := events[0].SealEnvelope(localstore.EnvelopeOptions{
		AccountID: worker.Session.AccountID, DeviceID: worker.Session.DeviceID,
		KeyEpoch: worker.Session.KeyEpoch, DeviceSeq: state.NextDeviceSequence,
		PreviousHash: state.PreviousHash, EpochKey: worker.Session.EpochKeys[worker.Session.KeyEpoch],
		SigningPrivate: worker.Session.SigningPrivate, Random: random,
	})
	if err != nil {
		return state, err
	}
	wire, err := envelope.ToWire()
	if err != nil {
		return state, err
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return state, err
	}
	hash, err := protocol.EnvelopeHash(envelope)
	if err != nil {
		return state, err
	}
	if err := worker.Store.SavePreparedUpload(ctx, localstore.PreparedUpload{
		EventID: events[0].ID, EventVersion: events[0].Version,
		DeviceSequence: state.NextDeviceSequence, Wire: encoded, EnvelopeHash: hash,
	}); err != nil {
		return state, localStoreError(err)
	}
	state, err = worker.Store.LoadSyncState(ctx)
	return state, localStoreError(err)
}

func (worker *Worker) decodeDownloads(response SyncResponse, cursor int64) ([]localstore.PhrasePayload, error) {
	payloads := make([]localstore.PhrasePayload, 0, len(response.Envelopes))
	lastCursor := cursor
	for _, wire := range response.Envelopes {
		if wire.Cursor > math.MaxInt64 || int64(wire.Cursor) <= lastCursor || int64(wire.Cursor) > response.NextCursor {
			return nil, relayProtocolError(errors.New("out-of-order download cursors"))
		}
		lastCursor = int64(wire.Cursor)
		envelope, err := protocol.EnvelopeFromDownload(worker.Session.AccountID, wire)
		if err != nil {
			return nil, relayProtocolError(err)
		}
		verificationKey := worker.Session.VerificationKeys[wire.DeviceID]
		if len(verificationKey) != ed25519.PublicKeySize {
			return nil, relayProtocolError(errors.New("download references an untrusted device"))
		}
		epochKey := worker.Session.EpochKeys[wire.KeyEpoch]
		if len(epochKey) != 32 {
			return nil, relayProtocolError(errors.New("download references an unavailable key epoch"))
		}
		var payload localstore.PhrasePayload
		if err := protocol.Open(epochKey, envelope, verificationKey, &payload); err != nil {
			return nil, relayProtocolError(err)
		}
		payloads = append(payloads, payload)
	}
	if len(response.Envelopes) == 0 && response.NextCursor != cursor {
		return nil, relayProtocolError(errors.New("cursor advanced without a downloaded envelope"))
	}
	if len(response.Envelopes) > 0 && lastCursor != response.NextCursor {
		return nil, relayProtocolError(errors.New("next cursor does not match the downloaded page"))
	}
	return payloads, nil
}

// SyncOnce prepares at most one local object and processes at most one relay
// page. Callers run this only in a background process; network or disk failure
// leaves the input engine and its current immutable snapshot untouched.
func (worker *Worker) SyncOnce(ctx context.Context) (Result, error) {
	if worker.Client == nil || worker.Store == nil {
		return Result{}, errors.New("sync worker requires a client and local store")
	}
	if err := worker.Session.validate(); err != nil {
		return Result{}, err
	}
	state, err := worker.Store.LoadSyncState(ctx)
	if err != nil {
		return Result{}, localStoreError(err)
	}
	if state.DeviceID != hex.EncodeToString(worker.Session.DeviceID) {
		return Result{}, errors.New("sync session device does not match the local store")
	}
	state, err = worker.prepare(ctx, state)
	if err != nil {
		return Result{}, err
	}
	request := SyncRequest{Cursor: state.Cursor, AckCursor: state.Cursor, Limit: 256}
	if state.Prepared != nil {
		var wire protocol.WireEnvelope
		if err := json.Unmarshal(state.Prepared.Wire, &wire); err != nil {
			return Result{}, localStoreError(errors.New("prepared upload is not valid wire JSON"))
		}
		if wire.DeviceSeq != state.Prepared.DeviceSequence || wire.DeviceID != "" || wire.Cursor != 0 {
			return Result{}, localStoreError(errors.New("prepared upload metadata does not match checkpoint"))
		}
		request.Envelopes = []protocol.WireEnvelope{wire}
	}
	response, err := worker.Client.Sync(ctx, worker.Session.DeviceToken, request)
	if err != nil {
		return Result{}, err
	}
	accepted := false
	if state.Prepared != nil {
		seenAccepted := false
		seenRejected := false
		for _, rejection := range response.RejectedSequences {
			if rejection.DeviceSequence != state.Prepared.DeviceSequence || seenRejected ||
				(rejection.Code != "sequence_conflict" && rejection.Code != "sequence_gap" && rejection.Code != "previous_hash_mismatch") {
				return Result{}, relayProtocolError(errors.New("unexpected upload rejection"))
			}
			seenRejected = true
		}
		for _, sequence := range response.AcceptedSequences {
			if sequence != state.Prepared.DeviceSequence || seenAccepted {
				return Result{}, relayProtocolError(errors.New("unexpected upload acknowledgement"))
			}
			seenAccepted = true
			accepted = true
		}
		if seenRejected && seenAccepted {
			return Result{}, relayProtocolError(errors.New("upload was both accepted and rejected"))
		}
		if seenRejected {
			return Result{}, &UploadRejectionError{Code: response.RejectedSequences[0].Code}
		}
		if !accepted {
			return Result{}, relayProtocolError(errors.New("prepared upload was not acknowledged"))
		}
	} else if len(response.AcceptedSequences) != 0 || len(response.RejectedSequences) != 0 {
		return Result{}, relayProtocolError(errors.New("upload results were returned for an empty upload"))
	}
	payloads, err := worker.decodeDownloads(response, state.Cursor)
	if err != nil {
		return Result{}, err
	}
	for _, payload := range payloads {
		if err := worker.Store.MergeRemotePayload(ctx, payload); err != nil {
			return Result{}, localStoreError(err)
		}
	}
	result := Result{Uploaded: state.Prepared != nil, Downloaded: len(payloads), Cursor: response.NextCursor, HasMore: response.HasMore}
	if state.Prepared != nil {
		result.AcknowledgedOutbox, err = worker.Store.CommitPreparedUpload(ctx)
		if err != nil {
			return Result{}, localStoreError(err)
		}
	}
	if err := worker.Store.AdvanceSyncCursor(ctx, state.Cursor, response.NextCursor); err != nil {
		return Result{}, localStoreError(err)
	}
	return result, nil
}

// SyncUntilIdle drains bounded pages and the coalescing local outbox without
// allowing a buggy relay to trap a desktop background process forever.
func (worker *Worker) SyncUntilIdle(ctx context.Context, maxRounds int) ([]Result, error) {
	if maxRounds < 1 || maxRounds > 1024 {
		return nil, errors.New("sync round limit must be between 1 and 1024")
	}
	results := make([]Result, 0)
	for round := 0; round < maxRounds; round++ {
		result, err := worker.SyncOnce(ctx)
		if err != nil {
			return results, err
		}
		results = append(results, result)
		pending, err := worker.Store.PendingEventCount(ctx)
		if err != nil {
			return results, localStoreError(err)
		}
		if !result.HasMore && pending == 0 {
			return results, nil
		}
	}
	return results, relayProtocolError(errors.New("sync did not become idle before the round limit"))
}
