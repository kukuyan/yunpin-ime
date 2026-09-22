// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

func TestOnboardingExpiredLoginDoesNotUnpairDevice(t *testing.T) {
	bundle, _, secrets := rosterRefreshFixture(t)
	defer bundle.Zero()
	before := append([]byte(nil), secrets.values["owner"]...)
	secrets.values["owner.user-session"] = []byte(`{"version":1,"endpoint":"https://sync.test","username":"alice","token":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","expires_at_unix_ms":1}`)
	state, err := ReadOnboardingLocalStatus(context.Background(), secrets, "owner", "https://sync.test")
	if err != nil || !state.CredentialPresent || !state.Paired || state.SessionValid || state.PairingPending || state.PairingState != "ready" {
		t.Fatalf("expired account session affected device trust: state=%+v err=%v", state, err)
	}
	encoded, _ := json.Marshal(state)
	for _, forbidden := range []string{"AAAAAAAA", "account_id", "device_id", "invitation", "recovery"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("status exposed protected material: %s", forbidden)
		}
	}
	if !bytes.Equal(before, secrets.values["owner"]) {
		t.Fatal("status changed active credentials")
	}
}

func TestOnboardingStatusFollowsExistingSixStepJournal(t *testing.T) {
	ctx := context.Background()
	creator, joining := newMemorySecretStore(), newMemorySecretStore()
	bundle := saveTestCreator(t, creator, "creator")
	defer bundle.Zero()
	relay := &fakePairingRelay{}
	creatorOptions := PairingOptions{Secrets: creator, Profile: "creator", DatabasePath: privateTestPath(t, "creator.db"), Random: pairingRandom(3)}
	joiningOptions := PairingOptions{Secrets: joining, Profile: "joining", DatabasePath: privateTestPath(t, "joining.db"), Random: pairingRandom(91)}
	check := func(secrets SecretStore, profile, role, phase string, paired, pending bool) {
		t.Helper()
		state, err := ReadOnboardingLocalStatus(ctx, secrets, profile, "")
		if err != nil || state.PairingRole != role || state.PairingState != phase || state.Paired != paired || state.PairingPending != pending {
			t.Fatalf("role/phase=%s/%s state=%+v err=%v", role, phase, state, err)
		}
	}
	invite, err := StartPairing(ctx, relay, creatorOptions)
	if err != nil {
		t.Fatal(err)
	}
	check(creator, "creator", "creator", "invited", false, true)
	remote, expired, err := ReadCreatorOnboardingPairingState(ctx, relay, creatorOptions)
	if err != nil || remote != "created" || expired {
		t.Fatalf("pending creator read: %s %v %v", remote, expired, err)
	}
	if _, err := JoinPairing(ctx, relay, joiningOptions, invite.Invitation); err != nil {
		t.Fatal(err)
	}
	check(joining, "joining", "joiner", "joined", false, true)
	if _, err := ApprovePairing(ctx, relay, creatorOptions); err != nil {
		t.Fatal(err)
	}
	check(creator, "creator", "creator", "awaiting_claim", false, true)
	if _, err := ClaimPairing(ctx, relay, joiningOptions); err != nil {
		t.Fatal(err)
	}
	// A future signed roster is already durable, but it is not active trust
	// for onboarding until creator finalization and joining journal cleanup.
	check(joining, "joining", "joiner", "finalize_pending", false, true)
	if _, err := FinalizePairing(ctx, relay, creatorOptions); err != nil {
		t.Fatal(err)
	}
	check(creator, "creator", "", "ready", true, false)
	if _, err := ClaimPairing(ctx, relay, joiningOptions); err != nil {
		t.Fatal(err)
	}
	check(joining, "joining", "", "ready", true, false)
	if relay.claimCalls != 1 {
		t.Fatal("journal completion repeated the initial claim")
	}
}

type onboardingServerFixture struct {
	rosterReply
	devices []syncclient.Device
}

func (relay *onboardingServerFixture) ListDevices(context.Context, string) ([]syncclient.Device, error) {
	return relay.devices, nil
}

func TestOnboardingServerAddressVerificationIsReadOnlyAndTrustBound(t *testing.T) {
	for _, scenario := range []string{"same_checkpoint", "valid_advance", "wrong_device", "wrong_key", "bad_chain", "bad_head", "missing_current"} {
		t.Run(scenario, func(t *testing.T) {
			bundle, next, secrets := rosterRefreshFixture(t)
			defer bundle.Zero()
			before := append([]byte(nil), secrets.values["owner"]...)
			ed, x := bundle.VerificationKeys[bundle.DeviceID], bundle.X25519PublicKeys[bundle.DeviceID]
			relay := &onboardingServerFixture{devices: []syncclient.Device{{ID: append([]byte(nil), bundle.DeviceID[:]...), Ed25519PublicKey: append([]byte(nil), ed[:]...), X25519PublicKey: append([]byte(nil), x[:]...), Current: true}}}
			head := bundle.TrustedRoster
			if scenario == "valid_advance" || scenario == "bad_chain" {
				head = next
				relay.value.Updates = []protocol.PairingRoster{next}
			}
			digest, _ := protocol.PairingRosterDigest(head)
			relay.value.HeadVersion, relay.value.HeadHash = head.Version, hex.EncodeToString(digest)
			switch scenario {
			case "wrong_device":
				relay.devices[0].ID[0] ^= 1
			case "wrong_key":
				relay.devices[0].Ed25519PublicKey[0] ^= 1
			case "missing_current":
				relay.devices[0].Current = false
			case "bad_chain":
				relay.value.Updates[0].PreviousHash[0] ^= 1
			case "bad_head":
				relay.value.HeadHash = strings.Repeat("0", 64)
			}
			err := VerifyOnboardingServer(context.Background(), relay, secrets, "owner")
			wantOK := scenario == "same_checkpoint" || scenario == "valid_advance"
			if (err == nil) != wantOK {
				t.Fatalf("verification err=%v wantOK=%v", err, wantOK)
			}
			if len(secrets.values) != 1 || !bytes.Equal(before, secrets.values["owner"]) {
				t.Fatal("address verification persisted a credential/checkpoint or journal")
			}
		})
	}
}

func TestOnboardingMalformedPendingJournalFailsClosed(t *testing.T) {
	secrets := newMemorySecretStore()
	secrets.values["owner.pairing-join"] = []byte(`{"version":1,"invitation":"private-malformed"}`)
	if _, err := ReadOnboardingLocalStatus(context.Background(), secrets, "owner", ""); err == nil {
		t.Fatal("malformed pending state was treated as an empty installation")
	}
}
