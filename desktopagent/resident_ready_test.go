// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/localstore"
	"github.com/kukuyan/yunpin-ime/protocol"
)

type residentJournalErrorStore struct {
	SecretStore
	profile string
}

func (store residentJournalErrorStore) Load(ctx context.Context, profile string) ([]byte, error) {
	if profile == store.profile {
		return nil, errors.New("synthetic backend failure at /sensitive/store/location")
	}
	return store.SecretStore.Load(ctx, profile)
}

func pairedResidentCredential(t *testing.T) CredentialBundleV1 {
	t.Helper()
	bundle := testCredentials()
	secondID := filled16(0x77)
	secondEd := filled32(0x78)
	secondX := filled32(0x79)
	selfEd := bundle.VerificationKeys[bundle.DeviceID]
	selfX := bundle.X25519PublicKeys[bundle.DeviceID]
	roster, err := protocol.SignPairingRoster(bundle.AccountID[:], 1, []protocol.PairingRosterDevice{
		{DeviceID: bundle.DeviceID[:], Ed25519PublicKey: selfEd[:], X25519PublicKey: selfX[:]},
		{DeviceID: secondID[:], Ed25519PublicKey: secondEd[:], X25519PublicKey: secondX[:]},
	}, bundle.DeviceID[:], ed25519.NewKeyFromSeed(bundle.SigningSeed[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := applyTrustedRoster(&bundle, roster); err != nil {
		t.Fatal(err)
	}
	return bundle
}

func residentReadyFixture(t *testing.T, bundle CredentialBundleV1) (*memorySecretStore, Agent) {
	t.Helper()
	paths := testRimeBridgePaths(t)
	writeTestRimeInstallation(t, paths, "installation_id: "+testRimeInstallationID+"\n")
	if err := ConfigureRimeBridge(paths); err != nil {
		t.Fatal(err)
	}
	rimeRoot := filepath.Dir(paths.InstallationPath)
	baseline := filepath.Join(rimeRoot, "yunpin", "baseline.tsv")
	if err := makePrivateDirectory(filepath.Dir(baseline)); err != nil {
		t.Fatal(err)
	}
	writePrivateTestFile(t, baseline, []byte(privateSnapshotHeader))
	state := filepath.Dir(paths.SyncDirectory)
	endpoint := filepath.Join(state, "sync.json")
	writePrivateTestFile(t, endpoint, []byte(`{"endpoint":"https://sync.invalid"}`))
	database := filepath.Join(state, "private.db")
	store, err := localstore.OpenForDevice(
		context.Background(), database, bundle.LocalDataKey[:], bundle.ObjectIDKey[:], bundle.DeviceIDHex(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := protectPrivateDatabaseFiles(database); err != nil {
		t.Fatal(err)
	}
	secrets := newMemorySecretStore()
	encoded, err := EncodeCredentialBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.Save(context.Background(), "default", encoded); err != nil {
		zeroBytes(encoded)
		t.Fatal(err)
	}
	zeroBytes(encoded)
	return secrets, Agent{
		Secrets: secrets, Profile: "default", StateDirectory: state,
		EndpointConfigPath: endpoint, DatabasePath: database,
		BaselinePath: baseline, SnapshotPath: filepath.Join(filepath.Dir(baseline), "private.tsv"),
	}
}

func TestResidentReadyRejectsPermissionCorrectNonDatabaseFile(t *testing.T) {
	bundle := pairedResidentCredential(t)
	defer bundle.Zero()
	_, agent := residentReadyFixture(t, bundle)
	writePrivateTestFile(t, agent.DatabasePath, []byte("not-a-database"))
	if _, err := agent.ResidentReady(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "valid private local database") {
		t.Fatalf("opaque private file crossed resident database gate: %v", err)
	}
}

func TestResidentReadyRejectsFirstDeviceBootstrapCredential(t *testing.T) {
	bundle := testCredentials()
	defer bundle.Zero()
	_, agent := residentReadyFixture(t, bundle)
	if _, err := agent.ResidentReady(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "two-device") {
		t.Fatalf("bootstrap credential crossed resident gate: %v", err)
	}
}

func TestResidentReadyAllowsFinalizedTwoDeviceStateWithoutJournals(t *testing.T) {
	bundle := pairedResidentCredential(t)
	defer bundle.Zero()
	_, agent := residentReadyFixture(t, bundle)
	ready, err := agent.ResidentReady(context.Background())
	if err != nil || !ready.Ready {
		t.Fatalf("finalized resident state was rejected: ready=%#v err=%v", ready, err)
	}
}

func TestResidentRosterMembershipRequiresActiveDevice(t *testing.T) {
	bundle := pairedResidentCredential(t)
	defer bundle.Zero()
	missing := filled16(0xee)
	if rosterContainsDevice(bundle.TrustedRoster, missing[:]) {
		t.Fatal("self-omitting roster was accepted by the resident membership predicate")
	}
	if !rosterContainsDevice(bundle.TrustedRoster, bundle.DeviceID[:]) {
		t.Fatal("active device was not found in its finalized roster fixture")
	}
}

func TestResidentReadyRejectsEachProtectedSetupJournal(t *testing.T) {
	for _, suffix := range []string{provisioningProfileSuffix, creatorPairingSuffix, joiningPairingSuffix} {
		t.Run(suffix, func(t *testing.T) {
			bundle := pairedResidentCredential(t)
			defer bundle.Zero()
			secrets, agent := residentReadyFixture(t, bundle)
			if err := secrets.Save(context.Background(), agent.Profile+suffix, []byte("synthetic-pending-state")); err != nil {
				t.Fatal(err)
			}
			if _, err := agent.ResidentReady(context.Background()); err == nil ||
				!strings.Contains(err.Error(), "pending protected setup state") {
				t.Fatalf("protected journal %q crossed resident gate: %v", suffix, err)
			}
		})
	}
}

func TestResidentReadyFailsClosedOnJournalSecretStoreError(t *testing.T) {
	bundle := pairedResidentCredential(t)
	defer bundle.Zero()
	secrets, agent := residentReadyFixture(t, bundle)
	agent.Secrets = residentJournalErrorStore{
		SecretStore: secrets,
		profile:     agent.Profile + creatorPairingSuffix,
	}
	_, err := agent.ResidentReady(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not exclude") {
		t.Fatalf("journal secret-store failure crossed resident gate: %v", err)
	}
	for _, forbidden := range []string{"/sensitive/", agent.DatabasePath, bundle.DeviceIDHex(), string(bundle.DeviceToken)} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("resident gate error exposed protected context %q: %v", forbidden, err)
		}
	}
}

func TestResidentReadyRequiresReadOnlyRimeBridgeMetadata(t *testing.T) {
	bundle := pairedResidentCredential(t)
	defer bundle.Zero()
	_, agent := residentReadyFixture(t, bundle)
	before, err := os.ReadFile(agent.BaselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(agent.StateDirectory, "rime-installation.pre-bridge.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ResidentReady(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "private Rime bridge state") {
		t.Fatalf("incomplete Rime bridge crossed resident gate: %v", err)
	}
	after, err := os.ReadFile(agent.BaselinePath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("readiness gate changed or consumed vocabulary state: err=%v", err)
	}
}

func TestResidentReadyRejectsUnexpectedRimeDeviceMetadataWithoutChangingIt(t *testing.T) {
	bundle := pairedResidentCredential(t)
	defer bundle.Zero()
	_, agent := residentReadyFixture(t, bundle)
	unexpected := filepath.Join(agent.StateDirectory, "rime-sync", "unexpected-device")
	if err := makePrivateDirectory(unexpected); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(unexpected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.ResidentReady(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "private Rime bridge state") {
		t.Fatalf("unexpected Rime device metadata crossed resident gate: %v", err)
	}
	after, err := os.Stat(unexpected)
	if err != nil || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("readiness gate changed unexpected Rime metadata: err=%v", err)
	}
}
