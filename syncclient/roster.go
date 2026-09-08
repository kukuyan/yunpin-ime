// SPDX-License-Identifier: Apache-2.0
package syncclient

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"

	"github.com/kukuyan/yunpin-ime/protocol"
)

// RosterUpdates is only transport data. The caller must validate every signed
// transition from its own persisted checkpoint before accepting any trust.
type RosterUpdates struct {
	HeadVersion uint64                   `json:"head_version"`
	HeadHash    string                   `json:"head_hash"`
	Updates     []protocol.PairingRoster `json:"updates"`
}

func (client *Client) GetRosterUpdates(ctx context.Context, token string, version uint64, hash []byte) (RosterUpdates, error) {
	if token == "" || version == 0 || len(hash) != 32 {
		return RosterUpdates{}, errors.New("trusted roster checkpoint is required")
	}
	var result RosterUpdates
	path := fmt.Sprintf("/v1/roster?after_version=%d&after_hash=%s", version, hex.EncodeToString(hash))
	err := client.doJSON(ctx, http.MethodGet, path, token, nil, &result, http.StatusOK)
	return result, err
}

func (client *Client) PublishRosterAnchor(ctx context.Context, token string, roster protocol.PairingRoster) error {
	if token == "" || roster.Version != 1 || len(roster.PreviousHash) != 0 || len(roster.Devices) != 2 {
		return errors.New("existing two-device trust anchor is required")
	}
	if err := protocol.VerifyPairingRoster(roster); err != nil {
		return err
	}
	return client.doJSON(ctx, http.MethodPut, "/v1/roster/anchor", token, roster, nil, http.StatusNoContent)
}

func (client *Client) CreatePairingFromRoster(ctx context.Context, creator Account, invitation PairingInvitation, roster protocol.PairingRoster) (PairingInvitation, error) {
	digest, err := protocol.PairingRosterDigest(roster)
	if err != nil {
		return PairingInvitation{}, err
	}
	return client.createPairing(ctx, creator, invitation, roster.Version, digest)
}

func (client *Client) FinalizePairingWithRoster(ctx context.Context, pairingID []byte, token string, roster protocol.PairingRoster) error {
	if len(pairingID) != 16 || token == "" {
		return errors.New("pairing ID and creator token are required")
	}
	if err := protocol.VerifyPairingRoster(roster); err != nil {
		return err
	}
	var result struct {
		State string `json:"state"`
	}
	path := "/v1/pairings/" + hex.EncodeToString(pairingID) + "/finalize"
	if err := client.doJSON(ctx, http.MethodPost, path, token, map[string]any{"roster": roster}, &result, http.StatusOK); err != nil {
		return err
	}
	if result.State != "finalized" {
		return errors.New("sync relay returned invalid pairing finalization")
	}
	return nil
}
