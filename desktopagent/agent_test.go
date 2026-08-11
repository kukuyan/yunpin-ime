// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

type fakeAccountRelay struct {
	createCalls  int
	putCalls     int
	registration syncclient.AccountRegistration
	box          protocol.SealedBox
}

func (relay *fakeAccountRelay) CreateAccount(_ context.Context, registration syncclient.AccountRegistration) (syncclient.Account, error) {
	relay.createCalls++
	relay.registration = registration
	return syncclient.Account{
		AccountID: bytes.Repeat([]byte{0xa1}, 16), DeviceID: bytes.Repeat([]byte{0xb2}, 16),
		DeviceToken: "synthetic_device_token_123456789",
	}, nil
}

func (relay *fakeAccountRelay) PutKeyring(_ context.Context, token string, epoch uint64, box protocol.SealedBox) error {
	if token != "synthetic_device_token_123456789" || epoch != 1 {
		return errors.New("unexpected keyring metadata")
	}
	relay.putCalls++
	relay.box = box
	return nil
}

type capturedInitializer struct {
	calls       int
	path        string
	dataKey     []byte
	objectIDKey []byte
	deviceID    string
}

func (capture *capturedInitializer) initialize(_ context.Context, path string, dataKey, objectIDKey []byte, deviceID string) error {
	capture.calls++
	capture.path = path
	capture.dataKey = append([]byte(nil), dataKey...)
	capture.objectIDKey = append([]byte(nil), objectIDKey...)
	capture.deviceID = deviceID
	return nil
}

func TestInitAccountCreatesRecoveryKeyringAndCommitsCredentialLast(t *testing.T) {
	relay := &fakeAccountRelay{}
	secrets := newMemorySecretStore()
	capture := &capturedInitializer{}
	result, err := initAccountForSyntheticProtocolTest(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: filepath.Join(t.TempDir(), "private.db"),
		Random: bytes.NewReader(bytes.Repeat([]byte{0x7c}, 2048)), InitializeStore: capture.initialize,
	})
	if err != nil {
		t.Fatal(err)
	}
	if relay.createCalls != 1 || relay.putCalls != 1 || capture.calls != 1 {
		t.Fatalf("unexpected call counts: create=%d keyring=%d initialize=%d", relay.createCalls, relay.putCalls, capture.calls)
	}
	if result.AccountIDHex != hex.EncodeToString(bytes.Repeat([]byte{0xa1}, 16)) {
		t.Fatalf("unexpected account ID %q", result.AccountIDHex)
	}
	recovery, err := protocol.DecodeRecoveryKey(result.RecoveryKey)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(recovery)
	keyring, err := protocol.OpenRecoveryPackage(recovery, relay.box)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := secrets.Load(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := DecodeCredentialBundle(encoded)
	zeroBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Zero()
	epochOne := bundle.EpochKeys[1]
	if !bytes.Equal(keyring.AccountID, bundle.AccountID[:]) || keyring.CurrentEpoch != bundle.CurrentEpoch ||
		!bytes.Equal(keyring.EpochKey, epochOne[:]) || !bytes.Equal(keyring.ObjectIDKey, bundle.ObjectIDKey[:]) {
		t.Fatal("stored credential and recovery keyring do not describe the same account keys")
	}
	if capture.deviceID != bundle.DeviceIDHex() || !bytes.Equal(capture.dataKey, bundle.LocalDataKey[:]) ||
		!bytes.Equal(capture.objectIDKey, bundle.ObjectIDKey[:]) {
		t.Fatal("local encrypted database was initialized with different keys")
	}
	self := bundle.VerificationKeys[bundle.DeviceID]
	wantSelf := ed25519.NewKeyFromSeed(bundle.SigningSeed[:]).Public().(ed25519.PublicKey)
	if !bytes.Equal(self[:], wantSelf) {
		t.Fatal("initial credential did not trust its own signing key")
	}
}

func TestInitAccountRefusesExistingProfileBeforeRelayCall(t *testing.T) {
	relay := &fakeAccountRelay{}
	secrets := newMemorySecretStore()
	if err := secrets.Save(context.Background(), "default", []byte("already-present")); err != nil {
		t.Fatal(err)
	}
	_, err := initAccountForSyntheticProtocolTest(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: filepath.Join(t.TempDir(), "private.db"),
		Random: bytes.NewReader(bytes.Repeat([]byte{0x7c}, 2048)),
	})
	if err == nil || relay.createCalls != 0 {
		t.Fatalf("existing profile reached relay: calls=%d err=%v", relay.createCalls, err)
	}
}

func TestInitAccountRefusesExistingDatabaseBeforeRelayCall(t *testing.T) {
	relay := &fakeAccountRelay{}
	database := filepath.Join(t.TempDir(), "private.db")
	if err := os.WriteFile(database, []byte("do-not-replace"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := initAccountForSyntheticProtocolTest(context.Background(), relay, InitAccountOptions{
		Secrets: newMemorySecretStore(), Profile: "default", DatabasePath: database,
		Random: bytes.NewReader(bytes.Repeat([]byte{0x7c}, 2048)),
	})
	if err == nil || relay.createCalls != 0 {
		t.Fatalf("existing database reached relay: calls=%d err=%v", relay.createCalls, err)
	}
	contents, readErr := os.ReadFile(database)
	if readErr != nil || string(contents) != "do-not-replace" {
		t.Fatalf("existing database changed: contents=%q err=%v", contents, readErr)
	}
}

func TestInitAccountIsFailClosedByDefault(t *testing.T) {
	relay := &fakeAccountRelay{}
	_, err := InitAccount(context.Background(), relay, InitAccountOptions{
		Secrets: newMemorySecretStore(), Profile: "default", DatabasePath: filepath.Join(t.TempDir(), "private.db"),
		Random: bytes.NewReader(bytes.Repeat([]byte{0x7c}, 2048)),
	})
	if err == nil || relay.createCalls != 0 {
		t.Fatalf("default account initialization wrote to relay: calls=%d err=%v", relay.createCalls, err)
	}
}

func TestStatusIsLocalOnlyAndReturnsNoIdentifiers(t *testing.T) {
	temporary := t.TempDir()
	endpoint := filepath.Join(temporary, "sync.json")
	database := filepath.Join(temporary, "private.db")
	if err := os.WriteFile(endpoint, []byte(`{"endpoint":"https://sync.invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database, []byte("synthetic-encrypted-database"), 0600); err != nil {
		t.Fatal(err)
	}
	secrets := newMemorySecretStore()
	encoded, err := EncodeCredentialBundle(testCredentials())
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Save(context.Background(), "default", encoded); err != nil {
		t.Fatal(err)
	}
	zeroBytes(encoded)
	status, err := (Agent{
		Secrets: secrets, Profile: "default", EndpointConfigPath: endpoint, DatabasePath: database,
	}).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || !status.EndpointConfigured || !status.DatabasePresent || status.CredentialVersion != 1 {
		t.Fatalf("unexpected status %#v", status)
	}
}
