// SPDX-License-Identifier: Apache-2.0
package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	syncserver "github.com/kukuyan/yunpin-ime/sync/server"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

type dropFirstSyncResponse struct {
	base http.RoundTripper
	once sync.Once
}

func (transport *dropFirstSyncResponse) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	dropped := false
	if request.URL.Path == "/v1/sync" {
		transport.once.Do(func() { dropped = true })
	}
	if !dropped {
		return response, nil
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	return nil, errors.New("synthetic lost response after relay commit")
}

func syntheticRegistration(seed byte, recovery []byte) (syncclient.AccountRegistration, ed25519.PrivateKey) {
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	return syncclient.AccountRegistration{
		RecoveryAuthentication: recovery,
		DeviceRegistration: syncclient.DeviceRegistration{
			DeviceNameCiphertext: bytes.Repeat([]byte{seed + 0x10}, 32),
			Ed25519PublicKey:     private.Public().(ed25519.PublicKey),
			X25519PublicKey:      bytes.Repeat([]byte{seed + 0x20}, 32),
		},
	}, private
}

func openSynchronizedStore(t *testing.T, deviceID []byte, dataKey, idKey []byte) *localstore.Store {
	t.Helper()
	store, err := localstore.OpenForDevice(context.Background(), filepath.Join(t.TempDir(), "private.db"), dataKey, idKey, hex.EncodeToString(deviceID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestTwoHeadlessDesktopWorkersConvergeAndRetryLostResponse(t *testing.T) {
	ctx := context.Background()
	application, err := syncserver.New(ctx, filepath.Join(t.TempDir(), "sync.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	relay := httptest.NewServer(application)
	defer relay.Close()
	endpoint, err := syncclient.ParseEndpoint(relay.URL, syncclient.EndpointPolicy{AllowPrivateHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	normalClient := syncclient.New(endpoint)
	recoveryKey := bytes.Repeat([]byte{0x30}, 32)
	recoveryAuthenticationWire, err := protocol.RecoveryAuthentication(recoveryKey)
	if err != nil {
		t.Fatal(err)
	}
	recoveryAuthentication, err := base64.RawURLEncoding.DecodeString(recoveryAuthenticationWire)
	if err != nil {
		t.Fatal(err)
	}
	registrationA, privateA := syntheticRegistration(0x41, recoveryAuthentication)
	accountA, err := normalClient.CreateAccount(ctx, registrationA)
	if err != nil {
		t.Fatal(err)
	}
	objectIDKey := bytes.Repeat([]byte{0x52}, 32)
	epochKey := bytes.Repeat([]byte{0x53}, 32)
	recoveryBox, err := protocol.SealRecoveryPackage(recoveryKey, protocol.RecoveryPackage{
		AccountID: accountA.AccountID, CurrentEpoch: 1, EpochKey: epochKey, ObjectIDKey: objectIDKey,
	}, bytes.NewReader(bytes.Repeat([]byte{0x54}, 24)))
	if err != nil {
		t.Fatal(err)
	}
	if err := normalClient.PutKeyring(ctx, accountA.DeviceToken, 1, recoveryBox); err != nil {
		t.Fatal(err)
	}
	registrationB, privateB := syntheticRegistration(0x42, recoveryAuthentication)
	accountB, err := normalClient.RecoverAccount(ctx, accountA.AccountID, registrationB)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(accountA.AccountID, accountB.AccountID) || bytes.Equal(accountA.DeviceID, accountB.DeviceID) {
		t.Fatal("account recovery did not create a distinct device in the same account")
	}
	keyrings, err := normalClient.GetKeyrings(ctx, accountB.DeviceToken)
	if err != nil || len(keyrings) != 1 || keyrings[0].Epoch != 1 || keyrings[0].WriterDeviceID != hex.EncodeToString(accountA.DeviceID) {
		t.Fatalf("recovered device did not receive the strict keyring: keyrings=%#v err=%v", keyrings, err)
	}
	recovered, err := protocol.OpenRecoveryPackage(recoveryKey, keyrings[0].Box)
	if err != nil || !bytes.Equal(recovered.AccountID, accountA.AccountID) ||
		!bytes.Equal(recovered.ObjectIDKey, objectIDKey) || !bytes.Equal(recovered.EpochKey, epochKey) || recovered.CurrentEpoch != 1 {
		t.Fatalf("recovery keyring did not restore client keys: recovered=%#v err=%v", recovered, err)
	}

	storeA := openSynchronizedStore(t, accountA.DeviceID, bytes.Repeat([]byte{0x51}, 32), objectIDKey)
	storeB := openSynchronizedStore(t, accountB.DeviceID, bytes.Repeat([]byte{0x61}, 32), recovered.ObjectIDKey)
	phrase := localstore.Phrase{Text: "合成桌面同步测试", Pinyin: "he cheng zhuo mian tong bu ce shi", Pinned: true}
	if err := storeA.SaveExplicit(ctx, phrase); err != nil {
		t.Fatal(err)
	}
	verificationKeys := map[string]ed25519.PublicKey{
		hex.EncodeToString(accountA.DeviceID): privateA.Public().(ed25519.PublicKey),
		hex.EncodeToString(accountB.DeviceID): privateB.Public().(ed25519.PublicKey),
	}
	sessionA := syncclient.Session{
		AccountID: accountA.AccountID, DeviceID: accountA.DeviceID, DeviceToken: accountA.DeviceToken,
		KeyEpoch: 1, EpochKeys: map[uint64][]byte{1: epochKey}, SigningPrivate: privateA,
		VerificationKeys: verificationKeys,
	}
	sessionB := syncclient.Session{
		AccountID: accountB.AccountID, DeviceID: accountB.DeviceID, DeviceToken: accountB.DeviceToken,
		KeyEpoch: recovered.CurrentEpoch, EpochKeys: map[uint64][]byte{recovered.CurrentEpoch: recovered.EpochKey}, SigningPrivate: privateB,
		VerificationKeys: verificationKeys,
	}
	droppingClient := syncclient.New(endpoint, syncclient.WithTransport(&dropFirstSyncResponse{base: http.DefaultTransport}))
	workerA := syncclient.Worker{Client: droppingClient, Store: storeA, Session: sessionA}
	if _, err := workerA.SyncOnce(ctx); err == nil {
		t.Fatal("synthetic lost response did not reach the client")
	}
	stateAfterLoss, err := storeA.LoadSyncState(ctx)
	if err != nil || stateAfterLoss.Prepared == nil || stateAfterLoss.NextDeviceSequence != 1 {
		t.Fatalf("exact retry state was not retained: state=%#v err=%v", stateAfterLoss, err)
	}
	workerA.Client = normalClient
	results, err := workerA.SyncUntilIdle(ctx, 4)
	if err != nil || len(results) == 0 || !results[0].Uploaded {
		t.Fatalf("idempotent upload retry failed: results=%#v err=%v", results, err)
	}
	stateA, err := storeA.LoadSyncState(ctx)
	if err != nil || stateA.Prepared != nil || stateA.NextDeviceSequence != 2 || len(stateA.PreviousHash) != 32 {
		t.Fatalf("device A chain did not advance exactly once: state=%#v err=%v", stateA, err)
	}

	workerB := syncclient.Worker{Client: normalClient, Store: storeB, Session: sessionB}
	if _, err := workerB.SyncUntilIdle(ctx, 4); err != nil {
		t.Fatal(err)
	}
	snapshotB, err := storeB.Snapshot(ctx)
	if err != nil || len(snapshotB.Phrases) != 1 || snapshotB.Phrases[0].Text != phrase.Text || !snapshotB.Phrases[0].Pinned {
		t.Fatalf("device B did not merge device A phrase: snapshot=%#v err=%v", snapshotB, err)
	}
	if _, err := storeB.RecordSelection(ctx, phrase, localstore.LearningContext{}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.RecordSelection(ctx, phrase, localstore.LearningContext{}); err != nil {
		t.Fatal(err)
	}
	if _, err := workerB.SyncUntilIdle(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := workerA.SyncUntilIdle(ctx, 4); err != nil {
		t.Fatal(err)
	}
	snapshotA, err := storeA.Snapshot(ctx)
	if err != nil || len(snapshotA.Phrases) != 1 || snapshotA.Phrases[0].UseCount != 2 {
		t.Fatalf("device A did not merge device B count: snapshot=%#v err=%v", snapshotA, err)
	}
}
