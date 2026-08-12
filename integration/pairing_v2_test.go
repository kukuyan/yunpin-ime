// SPDX-License-Identifier: Apache-2.0
package integration_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kukuyan/yunpin-ime/protocol"
	syncserver "github.com/kukuyan/yunpin-ime/sync/server"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

func deterministicBytes(length int, start byte) []byte {
	value := make([]byte, length)
	for index := range value {
		value[index] = start + byte(index)
	}
	return value
}

func TestPairingV2RejectsRelayTrustAndDeliversSignedRoster(t *testing.T) {
	ctx := context.Background()
	application, err := syncserver.New(ctx, filepath.Join(t.TempDir(), "pairing.db"), nil)
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
	client := syncclient.New(endpoint)

	creatorKeys, err := protocol.NewDeviceKeys(bytes.NewReader(deterministicBytes(96, 1)))
	if err != nil {
		t.Fatal(err)
	}
	creator, err := syncclient.GenerateAccountCredentials(bytes.NewReader(deterministicBytes(96, 101)))
	if err != nil {
		t.Fatal(err)
	}
	recoveryAuthentication := deterministicBytes(32, 31)
	creatorRegistration := syncclient.AccountRegistration{
		RecoveryAuthentication: recoveryAuthentication,
		DeviceRegistration: syncclient.DeviceRegistration{
			DeviceNameCiphertext: deterministicBytes(32, 41), Ed25519PublicKey: creatorKeys.Ed25519Public,
			X25519PublicKey: creatorKeys.X25519Public,
		},
	}
	creator, err = client.CreateAccount(ctx, creator, creatorRegistration)
	if err != nil {
		t.Fatal(err)
	}
	recoveryBox := protocol.SealedBox{Nonce: bytes.Repeat([]byte{0x51}, 24), Ciphertext: bytes.Repeat([]byte{0x52}, 16)}
	if err := client.PutKeyring(ctx, creator.DeviceToken, 1, recoveryBox); err != nil {
		t.Fatal(err)
	}
	if err := client.SealAccount(ctx, creator.AccountID, creator.DeviceToken); err != nil {
		t.Fatal(err)
	}

	invitation, err := syncclient.GeneratePairingInvitation(creator, creatorRegistration.DeviceRegistration,
		bytes.NewReader(deterministicBytes(48, 71)))
	if err != nil {
		t.Fatal(err)
	}
	invitation, err = client.CreatePairing(ctx, creator, invitation)
	if err != nil {
		t.Fatal(err)
	}

	joiningKeys, err := protocol.NewDeviceKeys(bytes.NewReader(deterministicBytes(96, 151)))
	if err != nil {
		t.Fatal(err)
	}
	joining, err := syncclient.GenerateDeviceCredentials(creator.AccountID,
		bytes.NewReader(deterministicBytes(80, 211)))
	if err != nil {
		t.Fatal(err)
	}
	joiningRegistration := syncclient.DeviceRegistration{
		DeviceNameCiphertext: deterministicBytes(32, 61), Ed25519PublicKey: joiningKeys.Ed25519Public,
		X25519PublicKey: joiningKeys.X25519Public,
	}
	transcript, err := client.JoinPairing(ctx, invitation, joining, joiningRegistration)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.GetPairing(ctx, invitation, creator)
	if err != nil || !bytes.Equal(status.DeviceID, joining.DeviceID) || !bytes.Equal(status.Ed25519PublicKey, joiningKeys.Ed25519Public) {
		t.Fatalf("authenticated pending device mismatch: status=%#v err=%v", status, err)
	}

	roster, err := protocol.SignPairingRoster(creator.AccountID, 1, []protocol.PairingRosterDevice{
		{DeviceID: creator.DeviceID, Ed25519PublicKey: creatorKeys.Ed25519Public, X25519PublicKey: creatorKeys.X25519Public},
		{DeviceID: joining.DeviceID, Ed25519PublicKey: joiningKeys.Ed25519Public, X25519PublicKey: joiningKeys.X25519Public},
	}, creator.DeviceID, creatorKeys.Ed25519Private)
	if err != nil {
		t.Fatal(err)
	}
	payload := protocol.PairingPackage{
		CurrentEpoch: 1, EpochKeys: []protocol.PairingEpochKey{{Epoch: 1, Key: deterministicBytes(32, 81)}},
		ObjectIDKey: deterministicBytes(32, 91), Roster: roster,
	}
	box, err := syncclient.SealPairingPackage(invitation, transcript, payload, creatorKeys.X25519Private,
		bytes.NewReader(deterministicBytes(24, 111)))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ApprovePairing(ctx, invitation.PairingID, creator.DeviceToken, box); err != nil {
		t.Fatal(err)
	}
	claim, err := client.ClaimPairing(ctx, invitation, joining, transcript, joiningKeys.Ed25519Private)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := syncclient.OpenPairingClaim(invitation, joining, transcript, joiningKeys.X25519Private, claim)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := client.ReadyPairing(ctx, invitation.PairingID, joining.DeviceToken); err != nil || state != "ready" {
		t.Fatalf("joining device readiness state=%q err=%v", state, err)
	}
	if err := client.FinalizePairing(ctx, invitation.PairingID, creator.DeviceToken); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opened, payload) {
		t.Fatalf("pairing package mismatch: got=%#v want=%#v", opened, payload)
	}

	// A relay-substituted joining X25519 key invalidates both the transcript
	// proof and the package; this check does not depend on relay honesty.
	tampered := transcript
	tampered.JoiningX25519PublicKey = creatorKeys.X25519Public
	if _, err := syncclient.OpenPairingClaim(invitation, joining, tampered, joiningKeys.X25519Private, claim); err == nil {
		t.Fatal("relay-substituted pairing transcript was accepted")
	}

	// The ordinary device bearer cannot invoke the dedicated rollback path.
	if err := client.DeleteCurrentDevice(ctx, creator.AccountID, joining.DeviceID, invitation.PairingID, joining.DeviceToken); err == nil {
		t.Fatal("normal device bearer was accepted as a rollback capability")
	}
}
