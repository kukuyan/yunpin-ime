// SPDX-License-Identifier: Apache-2.0
package mobilecore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/desktopagent"
	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
	"golang.org/x/crypto/curve25519"
)

func repeatedArray16(value byte) [16]byte {
	var result [16]byte
	for index := range result {
		result[index] = value
	}
	return result
}

func repeatedArray32(value byte) [32]byte {
	var result [32]byte
	for index := range result {
		result[index] = value
	}
	return result
}

func syntheticCredential(t *testing.T) []byte {
	t.Helper()
	accountID := repeatedArray16(0x11)
	deviceID := repeatedArray16(0x21)
	peerID := repeatedArray16(0x22)
	signingSeed := repeatedArray32(0x31)
	peerSeed := repeatedArray32(0x32)
	xPrivate := repeatedArray32(0x41)
	peerXPrivate := repeatedArray32(0x42)
	private := ed25519.NewKeyFromSeed(signingSeed[:])
	peerPrivate := ed25519.NewKeyFromSeed(peerSeed[:])
	xPublic, err := curve25519.X25519(xPrivate[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	peerXPublic, err := curve25519.X25519(peerXPrivate[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	roster, err := protocol.SignPairingRoster(accountID[:], 1, []protocol.PairingRosterDevice{
		{DeviceID: deviceID[:], Ed25519PublicKey: private.Public().(ed25519.PublicKey), X25519PublicKey: xPublic},
		{DeviceID: peerID[:], Ed25519PublicKey: peerPrivate.Public().(ed25519.PublicKey), X25519PublicKey: peerXPublic},
	}, deviceID[:], private)
	if err != nil {
		t.Fatal(err)
	}
	var selfEd, peerEd [ed25519.PublicKeySize]byte
	var selfX, peerX [32]byte
	copy(selfEd[:], private.Public().(ed25519.PublicKey))
	copy(peerEd[:], peerPrivate.Public().(ed25519.PublicKey))
	copy(selfX[:], xPublic)
	copy(peerX[:], peerXPublic)
	bundle := desktopagent.CredentialBundleV1{
		Version:   desktopagent.CredentialBundleVersion,
		AccountID: accountID, DeviceID: deviceID, DeviceToken: bytes.Repeat([]byte{'A'}, 32),
		SigningSeed: signingSeed, X25519Private: xPrivate,
		LocalDataKey: repeatedArray32(0x51), ObjectIDKey: repeatedArray32(0x61),
		CurrentEpoch: 1, EpochKeys: map[uint64][32]byte{1: repeatedArray32(0x71)},
		VerificationKeys: map[[16]byte][ed25519.PublicKeySize]byte{deviceID: selfEd, peerID: peerEd},
		X25519PublicKeys: map[[16]byte][32]byte{deviceID: selfX, peerID: peerX},
		TrustedRoster:    roster,
	}
	encoded, err := desktopagent.EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	bundle.Zero()
	return encoded
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func acceptingTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/sync" || request.URL.Host != "relay.invalid" {
			t.Fatalf("unexpected request target: %s %s", request.Method, request.URL.Redacted())
		}
		var input syncclient.SyncRequest
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			t.Fatal(err)
		}
		accepted := make([]uint64, 0, len(input.Envelopes))
		for _, envelope := range input.Envelopes {
			if envelope.DeviceID != "" || envelope.Cursor != 0 {
				t.Fatal("upload leaked server-owned envelope fields")
			}
			accepted = append(accepted, envelope.DeviceSeq)
		}
		encoded, err := json.Marshal(syncclient.SyncResponse{
			AcceptedSequences: accepted,
			RejectedSequences: []syncclient.SyncRejection{},
			Envelopes:         []protocol.WireEnvelope{},
			NextCursor:        input.Cursor,
			HasMore:           false,
			CurrentKeyEpoch:   1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(encoded)),
			Request:    request,
		}, nil
	})
}

func invalidCursorTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/sync" {
			t.Fatalf("unexpected request path: %s", request.URL.Path)
		}
		encoded, err := json.Marshal(syncclient.SyncResponse{
			AcceptedSequences: []uint64{}, RejectedSequences: []syncclient.SyncRejection{},
			Envelopes: []protocol.WireEnvelope{}, NextCursor: 1, CurrentKeyEpoch: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(encoded)), Request: request}, nil
	})
}

func backlogTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var input syncclient.SyncRequest
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		accepted := make([]uint64, 0, len(input.Envelopes))
		for _, envelope := range input.Envelopes {
			accepted = append(accepted, envelope.DeviceSeq)
		}
		encoded, err := json.Marshal(syncclient.SyncResponse{
			AcceptedSequences: accepted,
			RejectedSequences: []syncclient.SyncRejection{},
			Envelopes:         []protocol.WireEnvelope{},
			NextCursor:        input.Cursor,
			HasMore:           true,
			CurrentKeyEpoch:   1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(encoded)),
			Request:    request,
		}, nil
	})
}

func openSyntheticCore(t *testing.T, transport http.RoundTripper) *Core {
	t.Helper()
	directory := t.TempDir()
	credential := syntheticCredential(t)
	defer zeroBytes(credential)
	core, err := Open(context.Background(), Options{
		DatabasePath: directory + "/state/store.sqlite",
		SnapshotPath: directory + "/shared/private.tsv",
		Endpoint:     "https://relay.invalid",
		Credential:   credential,
		Transport:    transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := core.Close(); err != nil {
			t.Error(err)
		}
	})
	return core
}

func TestOfflineQueueProtectedContextAndBoundedSync(t *testing.T) {
	core := openSyntheticCore(t, acceptingTransport(t))
	ctx := context.Background()
	protected, err := core.RecordSelection(ctx, "合成隐私样例", "he cheng yin si yang li", localstore.LearningContext{PasswordField: true})
	if err != nil || protected.Recorded {
		t.Fatalf("protected selection was not rejected locally: result=%+v err=%v", protected, err)
	}
	protected, err = core.RecordSelection(ctx, "合成隐私样例", "he cheng yin si yang li", localstore.LearningContext{NoPersonalizedLearning: true})
	if err != nil || protected.Recorded {
		t.Fatalf("no-personalized-learning selection was not rejected locally: result=%+v err=%v", protected, err)
	}
	status, err := core.Status(ctx)
	if err != nil || status.Pending != 0 {
		t.Fatalf("protected context changed queue: status=%+v err=%v", status, err)
	}
	learned, err := core.RecordSelection(ctx, "合成同步样例", "he cheng tong bu yang li", localstore.LearningContext{})
	if err != nil || !learned.Recorded || !learned.SyncEligible {
		t.Fatalf("ordinary selection was not queued: result=%+v err=%v", learned, err)
	}
	status, err = core.Status(ctx)
	if err != nil || status.Pending != 1 || status.ControlPlaneGate != "signed_roster_chain_required" {
		t.Fatalf("unexpected redacted status: status=%+v err=%v", status, err)
	}
	report, err := core.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Rounds != 1 || report.Uploaded != 1 || report.Pending != 0 || report.SnapshotRows != 1 || !report.SnapshotChanged {
		t.Fatalf("unexpected bounded sync report: %+v", report)
	}
}

func TestSnapshotPublicationRetainsOneRollback(t *testing.T) {
	core := openSyntheticCore(t, acceptingTransport(t))
	ctx := context.Background()
	if err := core.SaveExplicit(ctx, "合成甲词", "he cheng jia ci", 3, false); err != nil {
		t.Fatal(err)
	}
	first, err := core.PublishSnapshot(ctx)
	if err != nil || !first.Changed || first.Rows != 1 || first.RollbackAvailable {
		t.Fatalf("unexpected first publication: report=%+v err=%v", first, err)
	}
	before, err := readPrivateRegular(core.snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(before, []byte("phrase\tpinyin\tsource\tuse_count\tpinned\n")) ||
		bytes.HasPrefix(before, []byte("phrase\tpinyin\tsource\tuse_count\tpinned\tlast_used_day\n")) {
		t.Fatalf("mobile writer diverged from the frozen five-column snapshot profile: %q", before)
	}
	if err := core.SaveExplicit(ctx, "合成乙词", "he cheng yi ci", 4, true); err != nil {
		t.Fatal(err)
	}
	second, err := core.PublishSnapshot(ctx)
	if err != nil || !second.Changed || second.Rows != 2 || !second.RollbackAvailable {
		t.Fatalf("unexpected second publication: report=%+v err=%v", second, err)
	}
	if err := core.RollbackSnapshot(); err != nil {
		t.Fatal(err)
	}
	after, err := readPrivateRegular(core.snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || strings.Contains(string(after), "合成乙词") {
		t.Fatal("snapshot rollback did not restore the exact previous generation")
	}
}

func TestCorruptCurrentSnapshotNeverPoisonsRollback(t *testing.T) {
	core := openSyntheticCore(t, acceptingTransport(t))
	ctx := context.Background()
	if err := core.SaveExplicit(ctx, "合成旧版本", "he cheng jiu ban ben", 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := core.PublishSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := core.SaveExplicit(ctx, "合成中版本", "he cheng zhong ban ben", 2, false); err != nil {
		t.Fatal(err)
	}
	if _, err := core.PublishSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	lastGoodRollback, err := readPrivateRegular(snapshotRollbackPath(core.snapshotPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(core.snapshotPath, []byte("synthetic corruption\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := core.Status(ctx)
	if err != nil || status.SnapshotPresent || !status.RollbackPresent {
		t.Fatalf("redacted status reported corrupt snapshot state as usable: status=%+v err=%v", status, err)
	}
	if err := core.SaveExplicit(ctx, "合成新版本", "he cheng xin ban ben", 3, true); err != nil {
		t.Fatal(err)
	}
	report, err := core.PublishSnapshot(ctx)
	if err != nil || !report.Changed || !report.RollbackAvailable {
		t.Fatalf("valid publication did not heal a corrupt current snapshot: report=%+v err=%v", report, err)
	}
	retained, err := readPrivateRegular(snapshotRollbackPath(core.snapshotPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(retained, lastGoodRollback) {
		t.Fatal("corrupt current snapshot replaced the last-known-good rollback")
	}
	if err := core.RollbackSnapshot(); err != nil {
		t.Fatal(err)
	}
	restored, err := readPrivateRegular(core.snapshotPath)
	if err != nil || !bytes.Equal(restored, lastGoodRollback) {
		t.Fatal("validated rollback was not restored")
	}

	currentBeforeRejectedRollback := append([]byte(nil), restored...)
	if err := os.WriteFile(snapshotRollbackPath(core.snapshotPath), []byte("synthetic corruption\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := core.RollbackSnapshot(); err == nil {
		t.Fatal("corrupt rollback snapshot was accepted")
	}
	currentAfterRejectedRollback, err := readPrivateRegular(core.snapshotPath)
	if err != nil || !bytes.Equal(currentAfterRejectedRollback, currentBeforeRejectedRollback) {
		t.Fatal("rejected rollback changed the current snapshot")
	}
}

func TestSnapshotValidatorRejectsZeroDaySuffix(t *testing.T) {
	contents := []byte(mobileSnapshotHeader + "合成日期\the cheng ri qi\tsynced_learning@0\t1\tfalse\n")
	if err := validateMobileSnapshot(contents); err == nil {
		t.Fatal("zero-day learning-source suffix was accepted")
	}
}

func TestSnapshotValidatorRejectsC1ControlText(t *testing.T) {
	contents := []byte(mobileSnapshotHeader + "合成\u0085控制\the cheng kong zhi\tsynced_learning\t1\tfalse\n")
	if err := validateMobileSnapshot(contents); err == nil {
		t.Fatal("C1 control character was accepted in a private snapshot")
	}
}

func TestSnapshotSourceRejectsInvalidAndDuplicateActiveRows(t *testing.T) {
	valid := localstore.Phrase{
		Text: "合成快照源", Pinyin: "he cheng kuai zhao yuan", UseCount: 1,
	}
	invalid := valid
	invalid.Text = "合成\n坏行"
	if _, err := snapshotRows(localstore.Snapshot{Phrases: []localstore.Phrase{valid, invalid}}); err == nil {
		t.Fatal("invalid active source row was silently omitted")
	}
	duplicate := valid
	duplicate.Text = " 合成快照源 "
	if _, err := snapshotRows(localstore.Snapshot{Phrases: []localstore.Phrase{valid, duplicate}}); err == nil {
		t.Fatal("duplicate active source identity was silently omitted")
	}
	deletedInvalid := invalid
	deletedInvalid.Deleted = true
	rows, err := snapshotRows(localstore.Snapshot{Phrases: []localstore.Phrase{valid, deletedInvalid}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("tombstone visibility filtering failed: rows=%d err=%v", len(rows), err)
	}
}

func TestDeleteWinsUntilExplicitReadd(t *testing.T) {
	core := openSyntheticCore(t, acceptingTransport(t))
	ctx := context.Background()
	if err := core.SaveExplicit(ctx, "合成删除样例", "he cheng shan chu yang li", 2, false); err != nil {
		t.Fatal(err)
	}
	if err := core.Delete(ctx, "合成删除样例", "he cheng shan chu yang li"); err != nil {
		t.Fatal(err)
	}
	deleted, err := core.PublishSnapshot(ctx)
	if err != nil || deleted.Rows != 0 {
		t.Fatalf("tombstone remained visible: report=%+v err=%v", deleted, err)
	}
	status, err := core.Status(ctx)
	if err != nil || status.Pending != 1 {
		t.Fatalf("coalesced delete outbox is invalid: status=%+v err=%v", status, err)
	}
	if err := core.SaveExplicit(ctx, "合成删除样例", "he cheng shan chu yang li", 2, false); err != nil {
		t.Fatal(err)
	}
	readded, err := core.PublishSnapshot(ctx)
	if err != nil || readded.Rows != 1 {
		t.Fatalf("explicit re-add did not advance presence: report=%+v err=%v", readded, err)
	}
}

func TestInvalidRemoteCursorDoesNotAdvanceCheckpoint(t *testing.T) {
	core := openSyntheticCore(t, invalidCursorTransport(t))
	if _, err := core.Sync(context.Background()); err == nil {
		t.Fatal("relay advanced an empty page without an envelope")
	}
	status, err := core.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Cursor != 0 || status.Prepared {
		t.Fatalf("invalid remote cursor changed checkpoint: %+v", status)
	}
}

func TestBoundedBacklogPublishesDurablePartialProgress(t *testing.T) {
	directory := t.TempDir()
	credential := syntheticCredential(t)
	defer zeroBytes(credential)
	core, err := Open(context.Background(), Options{
		DatabasePath: directory + "/state/store.sqlite",
		SnapshotPath: directory + "/shared/private.tsv",
		Endpoint:     "https://relay.invalid",
		Credential:   credential,
		Transport:    backlogTransport(t),
		MaxRounds:    1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	if err := core.SaveExplicit(context.Background(), "合成积压样例", "he cheng ji ya yang li", 2, false); err != nil {
		t.Fatal(err)
	}
	report, err := core.Sync(context.Background())
	if err == nil {
		t.Fatal("bounded backlog unexpectedly reported idle")
	}
	if report.Rounds != 1 || report.Uploaded != 1 || report.Pending != 0 ||
		report.SnapshotRows != 1 || !report.SnapshotChanged {
		t.Fatalf("durable partial progress was not published: %+v", report)
	}
}

func TestInvalidMobilePhraseCannotEnterEncryptedOutbox(t *testing.T) {
	core := openSyntheticCore(t, acceptingTransport(t))
	ctx := context.Background()
	invalid := []struct {
		text   string
		pinyin string
	}{
		{text: strings.Repeat("合", 513), pinyin: "he"},
		{text: "合成控制字符", pinyin: "he cheng\nkong zhi zi fu"},
		{text: "\u202e合成方向控制", pinyin: "he cheng fang xiang kong zhi"},
	}
	for _, phrase := range invalid {
		if _, err := core.RecordSelection(ctx, phrase.text, phrase.pinyin, localstore.LearningContext{}); err == nil {
			t.Fatal("invalid selection entered the mobile store")
		}
		if err := core.SaveExplicit(ctx, phrase.text, phrase.pinyin, 1, false); err == nil {
			t.Fatal("invalid explicit phrase entered the mobile store")
		}
		if err := core.Delete(ctx, phrase.text, phrase.pinyin); err == nil {
			t.Fatal("invalid tombstone entered the mobile store")
		}
	}
	status, err := core.Status(ctx)
	if err != nil || status.Pending != 0 {
		t.Fatalf("invalid mobile phrases changed the outbox: status=%+v err=%v", status, err)
	}
}

func TestFacadeStatusContainsNoIdentityOrEndpoint(t *testing.T) {
	core := openSyntheticCore(t, acceptingTransport(t))
	facade := &Facade{core: core}
	encoded, err := facade.Status(5000)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"relay.invalid", strings.Repeat("11", 16), strings.Repeat("21", 16), strings.Repeat("A", 32)} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("redacted status leaked forbidden material: %s", forbidden)
		}
	}
}

func TestFacadeMapsSyncFailureToStableRedactedCode(t *testing.T) {
	core := openSyntheticCore(t, invalidCursorTransport(t))
	facade := &Facade{core: core}
	encoded, err := facade.Sync(5000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, `"ok":false`) || !strings.Contains(encoded, `"error_code":"local_state_error"`) {
		t.Fatalf("facade did not return a stable redacted failure: %s", encoded)
	}
	if strings.Contains(encoded, "relay.invalid") || strings.Contains(encoded, "cursor without") {
		t.Fatalf("facade returned raw transport details: %s", encoded)
	}
}

func TestFacadeSeparatesRetryableRemoteFailures(t *testing.T) {
	for _, status := range []int{408, 429, 500, 503, 599} {
		if code := redactedErrorCode(&syncclient.APIError{Status: status}); code != "remote_unavailable" {
			t.Fatalf("HTTP %d mapped to %q", status, code)
		}
	}
	for _, status := range []int{400, 404, 422} {
		if code := redactedErrorCode(&syncclient.APIError{Status: status}); code != "remote_rejected" {
			t.Fatalf("HTTP %d mapped to %q", status, code)
		}
	}
	for _, rejection := range []string{"sequence_conflict", "sequence_gap", "previous_hash_mismatch"} {
		if code := redactedErrorCode(&syncclient.UploadRejectionError{Code: rejection}); code != "remote_conflict" {
			t.Fatalf("typed sequence rejection %q mapped to %q", rejection, code)
		}
	}
	if code := redactedErrorCode(&syncclient.UploadRejectionError{Code: "synthetic_unknown"}); code != "local_state_error" {
		t.Fatalf("unknown typed rejection mapped to %q", code)
	}
}

func TestFacadeCloseIsSafeDuringConcurrentStatus(t *testing.T) {
	core := openSyntheticCore(t, acceptingTransport(t))
	facade := &Facade{core: core}
	var workers sync.WaitGroup
	for index := 0; index < 8; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for attempt := 0; attempt < 20; attempt++ {
				_, _ = facade.Status(5000)
			}
		}()
	}
	if err := facade.Close(); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if _, err := facade.Status(5000); err == nil {
		t.Fatal("closed facade accepted a status call")
	}
}

func TestFacadeCancellationReachesActiveSyncTransport(t *testing.T) {
	started := make(chan struct{}, 1)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	core := openSyntheticCore(t, transport)
	facade := &Facade{core: core}
	type outcome struct {
		encoded string
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		encoded, err := facade.Sync(300_000)
		done <- outcome{encoded: encoded, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("synthetic transport did not receive the sync request")
	}
	facade.CancelCurrentOperation()
	select {
	case result := <-done:
		if result.err != nil || !strings.Contains(result.encoded, `"error_code":"cancelled"`) {
			t.Fatalf("active sync did not return a redacted cancellation: encoded=%s err=%v", result.encoded, result.err)
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(result.encoded), &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope) != 2 || envelope["result"] != nil {
			t.Fatalf("failure envelope carried an ambiguous partial result: %s", result.encoded)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active sync ignored native cancellation")
	}
}

func TestPublicPlaintextEndpointIsRejected(t *testing.T) {
	directory := t.TempDir()
	credential := syntheticCredential(t)
	defer zeroBytes(credential)
	_, err := Open(context.Background(), Options{
		DatabasePath: directory + "/state.sqlite",
		SnapshotPath: directory + "/private.tsv",
		Endpoint:     "http://example.invalid",
		Credential:   credential,
	})
	if err == nil {
		t.Fatal("public plaintext relay endpoint was accepted")
	}
}

func TestOpenRejectsAliasedPrivateStatePaths(t *testing.T) {
	directory := t.TempDir()
	credential := syntheticCredential(t)
	defer zeroBytes(credential)
	shared := directory + "/state"
	_, err := Open(context.Background(), Options{
		DatabasePath: shared,
		SnapshotPath: shared,
		Endpoint:     "https://relay.invalid",
		Credential:   credential,
	})
	if err == nil {
		t.Fatal("database and snapshot path alias was accepted")
	}
	_, err = Open(context.Background(), Options{
		DatabasePath: directory + "/same/store.sqlite",
		SnapshotPath: directory + "/same/private.tsv",
		Endpoint:     "https://relay.invalid",
		Credential:   credential,
	})
	if err == nil {
		t.Fatal("database and snapshot in one sidecar-capable directory were accepted")
	}
	_, err = Open(context.Background(), Options{
		DatabasePath: directory + "/state/store.sqlite?mode=memory",
		SnapshotPath: directory + "/shared/private.tsv",
		Endpoint:     "https://relay.invalid",
		Credential:   credential,
	})
	if err == nil {
		t.Fatal("SQLite URI metacharacters were accepted in a validated path")
	}

	first := directory + "/first"
	second := directory + "/second"
	if err := os.WriteFile(first, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Fatal(err)
	}
	if err := validateDistinctPrivatePaths(first, second); err == nil {
		t.Fatal("hard-linked private state paths were accepted")
	}
}
