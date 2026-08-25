// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

type fakeAccountRelay struct {
	createCalls  int
	putCalls     int
	deleteCalls  int
	sealCalls    int
	registration syncclient.AccountRegistration
	credentials  syncclient.Account
	box          protocol.SealedBox
	createErr    error
	deleteErr    error
	sealErr      error
	onCreate     func()
	onPut        func()
	onSeal       func()
}

func (relay *fakeAccountRelay) DeleteAccount(_ context.Context, accountID []byte, token string) error {
	if !bytes.Equal(accountID, relay.credentials.AccountID) || token != relay.credentials.RollbackToken {
		return errors.New("unexpected rollback credentials")
	}
	relay.deleteCalls++
	return relay.deleteErr
}

func (relay *fakeAccountRelay) CreateAccount(_ context.Context, credentials syncclient.Account, registration syncclient.AccountRegistration) (syncclient.Account, error) {
	relay.createCalls++
	if relay.createCalls > 1 && (!bytes.Equal(relay.credentials.AccountID, credentials.AccountID) ||
		!bytes.Equal(relay.credentials.DeviceID, credentials.DeviceID) || relay.credentials.DeviceToken != credentials.DeviceToken ||
		relay.credentials.RollbackToken != credentials.RollbackToken) {
		return syncclient.Account{}, errors.New("provisioning identity changed across retry")
	}
	relay.registration = registration
	relay.credentials = syncclient.Account{
		AccountID: append([]byte(nil), credentials.AccountID...), DeviceID: append([]byte(nil), credentials.DeviceID...),
		DeviceToken: credentials.DeviceToken, RollbackToken: credentials.RollbackToken,
	}
	if relay.onCreate != nil {
		relay.onCreate()
	}
	if relay.createErr != nil {
		return syncclient.Account{}, relay.createErr
	}
	return credentials, nil
}

func (relay *fakeAccountRelay) PutKeyring(_ context.Context, token string, epoch uint64, box protocol.SealedBox) error {
	if token != relay.credentials.DeviceToken || epoch != 1 {
		return errors.New("unexpected keyring metadata")
	}
	relay.putCalls++
	relay.box = protocol.SealedBox{
		Nonce: append([]byte(nil), box.Nonce...), Ciphertext: append([]byte(nil), box.Ciphertext...),
	}
	if relay.onPut != nil {
		relay.onPut()
	}
	return nil
}

func (relay *fakeAccountRelay) SealAccount(_ context.Context, accountID []byte, token string) error {
	if !bytes.Equal(accountID, relay.credentials.AccountID) || token != relay.credentials.DeviceToken {
		return errors.New("unexpected seal credentials")
	}
	relay.sealCalls++
	if relay.onSeal != nil {
		relay.onSeal()
	}
	return relay.sealErr
}

func deterministicAccountRandom() *bytes.Reader {
	value := make([]byte, 8192)
	for index := range value {
		value[index] = byte((index % 251) + 1)
	}
	return bytes.NewReader(value)
}

func prepareSyntheticAccount(t *testing.T, secrets SecretStore, database string) PrepareAccountResult {
	t.Helper()
	var delivered PrepareAccountResult
	result, err := PrepareAccount(context.Background(), InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database, Random: deterministicAccountRandom(),
		ConfirmRecoverySaved: func(_ context.Context, result PrepareAccountResult) error {
			delivered = result
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RecoveryKey != "" || delivered.RecoveryKey == "" || result.AccountIDHex != delivered.AccountIDHex {
		t.Fatalf("recovery key delivery contract mismatch: returned=%#v delivered=%#v", result, delivered)
	}
	return delivered
}

func TestPrepareAccountIsNetworkFreeAndJournalExcludesRecoveryRoot(t *testing.T) {
	secrets := newMemorySecretStore()
	database := privateTestPath(t, "private.db")
	result := prepareSyntheticAccount(t, secrets, database)
	if result.AccountIDHex == "" || result.RecoveryKey == "" {
		t.Fatalf("prepare result is incomplete: %#v", result)
	}
	if _, err := secrets.Load(context.Background(), "default"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("prepare wrote active credential: %v", err)
	}
	pending, err := secrets.Load(context.Background(), "default.provisioning")
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(pending)
	recovery, err := protocol.DecodeRecoveryKey(result.RecoveryKey)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(recovery)
	if bytes.Contains(pending, recovery) || bytes.Contains(pending, []byte(result.RecoveryKey)) {
		t.Fatal("pending provisioning journal contains recovery root")
	}
	journal, err := decodeProvisioningJournal(pending)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Zero()
	if result.AccountIDHex != bytesToHex(journal.Credentials.AccountID) {
		t.Fatal("displayed account ID differs from protected journal")
	}
	if _, err := os.Stat(database); !os.IsNotExist(err) {
		t.Fatalf("prepare touched encrypted database: %v", err)
	}
}

func TestPrepareDeliversAndConfirmsBeforePersistingPendingJournal(t *testing.T) {
	secrets := newMemorySecretStore()
	database := privateTestPath(t, "private.db")
	deliveryCalled := false
	_, err := PrepareAccount(context.Background(), InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database, Random: deterministicAccountRandom(),
		ConfirmRecoverySaved: func(ctx context.Context, result PrepareAccountResult) error {
			deliveryCalled = true
			if result.RecoveryKey == "" || result.AccountIDHex == "" {
				return errors.New("recovery delivery was empty")
			}
			if _, err := secrets.Load(ctx, "default.provisioning"); !errors.Is(err, ErrSecretNotFound) {
				return errors.New("pending journal existed before saved-key confirmation")
			}
			return errors.New("synthetic user did not confirm saved key")
		},
	})
	if err == nil || !deliveryCalled {
		t.Fatalf("failed delivery confirmation was accepted: called=%t err=%v", deliveryCalled, err)
	}
	if _, loadErr := secrets.Load(context.Background(), "default.provisioning"); !errors.Is(loadErr, ErrSecretNotFound) {
		t.Fatalf("failed confirmation persisted pending journal: %v", loadErr)
	}
	if _, statErr := os.Stat(database); !os.IsNotExist(statErr) {
		t.Fatalf("failed confirmation touched database: %v", statErr)
	}
}

func bytesToHex(value []byte) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2] = digits[item>>4]
		encoded[index*2+1] = digits[item&15]
	}
	return string(encoded)
}

func TestInitPreparedAccountCommitsAndSealsThenDeletesJournal(t *testing.T) {
	secrets := newMemorySecretStore()
	database := privateTestPath(t, "private.db")
	prepared := prepareSyntheticAccount(t, secrets, database)
	relay := &fakeAccountRelay{}
	result, err := InitAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	})
	if err != nil || result.State != "ready" || result.AccountIDHex != prepared.AccountIDHex {
		t.Fatalf("init result=%#v err=%v", result, err)
	}
	if relay.createCalls != 1 || relay.putCalls != 1 || relay.sealCalls != 1 || relay.deleteCalls != 0 {
		t.Fatalf("unexpected calls: create=%d put=%d seal=%d delete=%d", relay.createCalls, relay.putCalls, relay.sealCalls, relay.deleteCalls)
	}
	if _, err := secrets.Load(context.Background(), "default.provisioning"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("completed journal remains: %v", err)
	}
	active, err := secrets.Load(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := DecodeCredentialBundle(active)
	zeroBytes(active)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Zero()
	recovery, err := protocol.DecodeRecoveryKey(prepared.RecoveryKey)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(recovery)
	opened, err := protocol.OpenRecoveryPackage(recovery, relay.box)
	if err != nil || !bytes.Equal(opened.AccountID, bundle.AccountID[:]) || !bytes.Equal(opened.ObjectIDKey, bundle.ObjectIDKey[:]) {
		t.Fatalf("recovery package differs from active credential: package=%#v err=%v", opened, err)
	}
	if _, err := os.Stat(database); err != nil {
		t.Fatalf("encrypted database missing: %v", err)
	}
}

func TestInitPreparedAccountResumesLostCreateResponseWithSameIdentity(t *testing.T) {
	secrets := newMemorySecretStore()
	database := privateTestPath(t, "private.db")
	prepareSyntheticAccount(t, secrets, database)
	relay := &fakeAccountRelay{createErr: errors.New("synthetic response lost after commit")}
	if _, err := InitAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	}); err == nil {
		t.Fatal("lost create response unexpectedly succeeded")
	}
	firstAccount := append([]byte(nil), relay.credentials.AccountID...)
	relay.createErr = nil
	result, err := InitAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	})
	if err != nil || result.State != "ready" || relay.createCalls != 2 || !bytes.Equal(firstAccount, relay.credentials.AccountID) {
		t.Fatalf("create retry mismatch: result=%#v calls=%d err=%v", result, relay.createCalls, err)
	}
}

func TestInitPreparedAccountResumesAfterSealFailureWithoutRecreating(t *testing.T) {
	secrets := newMemorySecretStore()
	database := privateTestPath(t, "private.db")
	prepareSyntheticAccount(t, secrets, database)
	relay := &fakeAccountRelay{sealErr: errors.New("synthetic seal outage")}
	result, err := InitAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	})
	if !errors.Is(err, ErrAccountSealPending) || result.State != "seal_pending" {
		t.Fatalf("seal failure mismatch: result=%#v err=%v", result, err)
	}
	if _, err := secrets.Load(context.Background(), "default"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("seal failure promoted an active credential before remote sealing: %v", err)
	}
	if _, err := os.Lstat(database); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("seal failure promoted an encrypted database before remote sealing: %v", err)
	}
	if _, err := secrets.Load(context.Background(), "default.provisioning"); err != nil {
		t.Fatalf("seal failure removed pending journal: %v", err)
	}
	relay.sealErr = nil
	result, err = InitAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	})
	if err != nil || result.State != "ready" || relay.createCalls != 1 || relay.putCalls != 1 || relay.sealCalls != 2 {
		t.Fatalf("seal retry recreated remote state: result=%#v create=%d put=%d seal=%d err=%v",
			result, relay.createCalls, relay.putCalls, relay.sealCalls, err)
	}
}

func TestExpiredRemoteReadyProvisioningRequiresIdempotentAbort(t *testing.T) {
	secrets := newMemorySecretStore()
	database := privateTestPath(t, "private.db")
	prepareSyntheticAccount(t, secrets, database)
	relay := &fakeAccountRelay{sealErr: &syncclient.APIError{Status: http.StatusNotFound, Code: "account_not_found"}}
	result, err := InitAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	})
	if !errors.Is(err, ErrAccountProvisioningExpired) || result.State != "expired_abort_required" ||
		relay.createCalls != 1 || relay.putCalls != 1 || relay.sealCalls != 1 {
		t.Fatalf("expired provisioning mismatch: result=%#v create=%d put=%d seal=%d err=%v",
			result, relay.createCalls, relay.putCalls, relay.sealCalls, err)
	}
	if _, err := secrets.Load(context.Background(), "default"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expired unsealed identity promoted an active credential: %v", err)
	}
	if _, err := secrets.Load(context.Background(), "default.provisioning"); err != nil {
		t.Fatalf("expired identity lost its protected rollback capability: %v", err)
	}
	// The relay's GC has already placed the provisioning rollback hash in its
	// idempotent tombstone. A confirmed 204 lets the client safely remove only
	// its still-pending journal and exact-key local artifacts.
	result, err = AbortAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	})
	if err != nil || result.State != "aborted" || relay.deleteCalls != 1 {
		t.Fatalf("expired provisioning abort mismatch: result=%#v delete=%d err=%v", result, relay.deleteCalls, err)
	}
}

func TestRetiredProvisioningIdentityDoesNotRegenerateOrLoopCreate(t *testing.T) {
	secrets := newMemorySecretStore()
	database := privateTestPath(t, "private.db")
	prepareSyntheticAccount(t, secrets, database)
	relay := &fakeAccountRelay{createErr: &syncclient.APIError{Status: http.StatusConflict, Code: "provisioning_identity_retired"}}
	result, err := InitAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	})
	if !errors.Is(err, ErrAccountProvisioningExpired) || result.State != "expired_abort_required" || relay.createCalls != 1 {
		t.Fatalf("retired provisioning identity was not made explicit: result=%#v calls=%d err=%v", result, relay.createCalls, err)
	}
	if _, err := secrets.Load(context.Background(), "default.provisioning"); err != nil {
		t.Fatalf("retired identity lost protected abort capability: %v", err)
	}
}

func TestInitPreparedAccountResumesAfterEachPersistedKillBoundary(t *testing.T) {
	for _, boundary := range []string{"after-create", "after-keyring"} {
		t.Run(boundary, func(t *testing.T) {
			secrets := newMemorySecretStore()
			database := privateTestPath(t, "private.db")
			prepareSyntheticAccount(t, secrets, database)
			killed := false
			relay := &fakeAccountRelay{}
			if boundary == "after-create" {
				relay.onCreate = func() { killed = true }
			}
			if boundary == "after-keyring" {
				relay.onPut = func() { killed = true }
			}
			// Model a hard process stop by making the next boundary return without
			// changing any protected journal material. The relay fakes are
			// idempotent, so the second process must reuse the same credentials.
			if boundary == "after-create" {
				relay.createErr = errors.New("response lost at kill boundary")
			}
			if boundary == "after-keyring" {
				relay.sealErr = errors.New("stop after keyring and local commit")
			}
			_, firstErr := InitAccount(context.Background(), relay, InitAccountOptions{
				Secrets: secrets, Profile: "default", DatabasePath: database,
			})
			if firstErr == nil || !killed {
				t.Fatalf("boundary was not reached: killed=%t err=%v", killed, firstErr)
			}
			relay.createErr = nil
			relay.sealErr = nil
			result, err := InitAccount(context.Background(), relay, InitAccountOptions{
				Secrets: secrets, Profile: "default", DatabasePath: database,
			})
			if err != nil || result.State != "ready" {
				t.Fatalf("resume failed: result=%#v err=%v", result, err)
			}
		})
	}
}

func TestAbortAccountRetainsJournalUntilIdempotentRemoteRollbackConfirms(t *testing.T) {
	secrets := newMemorySecretStore()
	database := privateTestPath(t, "private.db")
	prepareSyntheticAccount(t, secrets, database)
	encoded, err := secrets.Load(context.Background(), "default.provisioning")
	if err != nil {
		t.Fatal(err)
	}
	journal, err := decodeProvisioningJournal(encoded)
	zeroBytes(encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Zero()
	relay := &fakeAccountRelay{credentials: journal.Credentials, deleteErr: errors.New("synthetic lost rollback response")}
	result, err := AbortAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	})
	if err == nil || result.State != "abort_pending" || relay.deleteCalls != 1 {
		t.Fatalf("lost rollback response mismatch: result=%#v calls=%d err=%v", result, relay.deleteCalls, err)
	}
	if _, err := secrets.Load(context.Background(), "default.provisioning"); err != nil {
		t.Fatalf("ambiguous rollback removed protected journal: %v", err)
	}
	relay.deleteErr = nil
	result, err = AbortAccount(context.Background(), relay, InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database,
	})
	if err != nil || result.State != "aborted" || relay.deleteCalls != 2 {
		t.Fatalf("idempotent rollback retry failed: result=%#v calls=%d err=%v", result, relay.deleteCalls, err)
	}
	if _, err := secrets.Load(context.Background(), "default.provisioning"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("confirmed rollback retained journal: %v", err)
	}
}

func TestPrepareRefusesExistingProfileOrDatabaseBeforeGenerating(t *testing.T) {
	secrets := newMemorySecretStore()
	if err := secrets.Save(context.Background(), "default", []byte("already-present")); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareAccount(context.Background(), InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: privateTestPath(t, "private.db"), Random: deterministicAccountRandom(),
		ConfirmRecoverySaved: func(context.Context, PrepareAccountResult) error { return nil },
	}); err == nil {
		t.Fatal("prepare accepted existing active profile")
	}
	secrets = newMemorySecretStore()
	database := privateTestPath(t, "private.db")
	if err := os.WriteFile(database, []byte("do-not-replace"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareAccount(context.Background(), InitAccountOptions{
		Secrets: secrets, Profile: "default", DatabasePath: database, Random: deterministicAccountRandom(),
		ConfirmRecoverySaved: func(context.Context, PrepareAccountResult) error { return nil },
	}); err == nil {
		t.Fatal("prepare accepted existing database")
	}
	contents, _ := os.ReadFile(database)
	if string(contents) != "do-not-replace" {
		t.Fatal("prepare changed existing database")
	}
}

func TestStatusIsLocalOnlyAndReturnsNoIdentifiers(t *testing.T) {
	database := privateTestPath(t, "private.db")
	temporary := filepath.Dir(database)
	endpoint := filepath.Join(temporary, "sync.json")
	if err := os.WriteFile(endpoint, []byte(`{"endpoint":"https://sync.invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database, []byte("synthetic-encrypted-database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := protectPrivateDatabaseFiles(database); err != nil {
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
	if !status.Ready || !status.EndpointConfigured || !status.DatabasePresent || status.CredentialVersion != CredentialBundleVersion {
		t.Fatalf("unexpected status %#v", status)
	}
	if status.HealthAvailable {
		t.Fatal("an unreadable health database was presented as an available zero-value record")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(database, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := (Agent{
			Secrets: secrets, Profile: "default", EndpointConfigPath: endpoint, DatabasePath: database,
		}).Status(context.Background()); err == nil {
			t.Fatal("status accepted a non-private encrypted database")
		}
	}
}

func TestSyncOnceRecordsNetworkFailureWithoutOverwritingLastSuccess(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasSuffix(request.URL.Path, "/seal"):
			response.WriteHeader(http.StatusNoContent)
		case request.URL.Path == "/v1/sync":
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"accepted_sequences":[],"rejected_sequences":[],"envelopes":[],"next_cursor":0,"has_more":false,"current_key_epoch":1}`))
		default:
			http.NotFound(response, request)
		}
	}))

	root := privateTestPath(t, "state")
	makePrivateTestDirectory(t, root)
	endpoint := filepath.Join(root, "sync.json")
	if err := ConfigureEndpoint(endpoint, relay.URL, true); err != nil {
		relay.Close()
		t.Fatal(err)
	}
	bundle := testCredentials()
	defer bundle.Zero()
	database := filepath.Join(root, "private.db")
	if err := ensureEncryptedStore(context.Background(), database, bundle, nil); err != nil {
		relay.Close()
		t.Fatal(err)
	}
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		relay.Close()
		t.Fatal(err)
	}
	secrets := newMemorySecretStore()
	if err := secrets.Save(context.Background(), "default", encoded); err != nil {
		zeroBytes(encoded)
		relay.Close()
		t.Fatal(err)
	}
	zeroBytes(encoded)
	agent := Agent{
		Secrets: secrets, Profile: "default", StateDirectory: root,
		EndpointConfigPath: endpoint, DatabasePath: database,
	}
	if _, err := agent.SyncOnce(context.Background()); err != nil {
		relay.Close()
		t.Fatalf("successful baseline round: %v", err)
	}
	readHealth := func() localstore.SyncHealth {
		store, err := localstore.OpenForDevice(context.Background(), database,
			bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex())
		if err != nil {
			t.Fatal(err)
		}
		health, loadErr := store.LoadSyncHealth(context.Background())
		closeErr := store.Close()
		if loadErr != nil || closeErr != nil {
			t.Fatalf("load health: load=%v close=%v", loadErr, closeErr)
		}
		return health
	}
	success := readHealth()
	if success.LastSuccessAt == 0 || success.LastEventCode != "sync_complete" ||
		success.LastFailureClass != localstore.SyncFailureNone {
		t.Fatalf("successful round health=%+v", success)
	}
	relay.Close()
	if _, err := agent.SyncOnce(context.Background()); err == nil {
		t.Fatal("network outage unexpectedly synchronized")
	}
	failure := readHealth()
	if failure.LastEventCode != "sync_failed" || failure.LastFailureClass != localstore.SyncFailureNetwork {
		t.Fatalf("network outage was not classified: %+v", failure)
	}
	if failure.LastSuccessAt != success.LastSuccessAt {
		t.Fatalf("network failure overwrote last success: before=%d after=%d", success.LastSuccessAt, failure.LastSuccessAt)
	}
	status, err := agent.Status(context.Background())
	if err != nil || !status.HealthAvailable || status.Health.LastFailureClass != localstore.SyncFailureNetwork {
		t.Fatalf("status did not expose classified health: status=%+v err=%v", status, err)
	}
}

func TestSyncOnceFailsClosedBeforeNetworkWhenBaselineAndSnapshotAreMissing(t *testing.T) {
	requests := 0
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		http.Error(response, "unexpected", http.StatusInternalServerError)
	}))
	defer relay.Close()
	root := t.TempDir()
	endpoint := filepath.Join(root, "sync", "sync.json")
	if err := ConfigureEndpoint(endpoint, relay.URL, true); err != nil {
		t.Fatal(err)
	}
	bundle := testCredentials()
	defer bundle.Zero()
	database := filepath.Join(root, "sync", "private.db")
	if err := ensureEncryptedStore(context.Background(), database, bundle, nil); err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	secrets := newMemorySecretStore()
	if err := secrets.Save(context.Background(), "default", encoded); err != nil {
		t.Fatal(err)
	}
	zeroBytes(encoded)
	nativeEvents := filepath.Join(root, "sync", "native-events", "incoming")
	if err := os.MkdirAll(nativeEvents, 0700); err != nil {
		t.Fatal(err)
	}
	agent := Agent{
		Secrets: secrets, Profile: "default", EndpointConfigPath: endpoint, DatabasePath: database,
		NativeEventsPath: nativeEvents, BaselinePath: filepath.Join(root, "rime", "baseline.tsv"),
		SnapshotPath: filepath.Join(root, "rime", "private.tsv"), SnapshotStatePath: filepath.Join(root, "sync", "snapshot-state"),
	}
	if _, err := agent.SyncOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "both baseline and private snapshot are missing") {
		t.Fatalf("missing baseline did not fail closed: %v", err)
	}
	if requests != 0 {
		t.Fatalf("missing baseline reached network: requests=%d", requests)
	}
}
