// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/kukuyan/yunpin-ime/protocol"
	"github.com/kukuyan/yunpin-ime/syncclient"
)

type RosterRelay interface {
	GetRosterUpdates(context.Context, string, uint64, []byte) (syncclient.RosterUpdates, error)
	PublishRosterAnchor(context.Context, string, protocol.PairingRoster) error
}

type chainedPairingRelay interface {
	RosterRelay
	CreatePairingFromRoster(context.Context, syncclient.Account, syncclient.PairingInvitation, protocol.PairingRoster) (syncclient.PairingInvitation, error)
	FinalizePairingWithRoster(context.Context, []byte, string, protocol.PairingRoster) error
}

// RefreshTrustedRoster accepts only a continuous add-only chain extending the
// caller's already trusted checkpoint. Call under the existing process lock,
// before constructing a worker session. Unchanged rounds do not touch secrets;
// changed trust enters memory only after the exact protected save succeeds.
func RefreshTrustedRoster(ctx context.Context, relay RosterRelay, secrets SecretStore, profile string, bundle *CredentialBundleV1) (bool, error) {
	if bundle == nil || relay == nil || secrets == nil {
		return false, errors.New("roster refresh dependencies are required")
	}
	if err := bundle.Validate(); err != nil {
		return false, err
	}
	if rosterIsEmpty(bundle.TrustedRoster) {
		return false, nil
	}
	trusted := clonePairingRoster(bundle.TrustedRoster)
	var totalBytes int
	for page := 0; page < 32; page++ {
		digest, err := protocol.PairingRosterDigest(trusted)
		if err != nil {
			return false, err
		}
		response, err := relay.GetRosterUpdates(ctx, string(bundle.DeviceToken), trusted.Version, digest)
		if err != nil {
			var api *syncclient.APIError
			if page == 0 && errors.As(err, &api) && api.Code == "roster_not_initialized" &&
				trusted.Version == 1 && len(trusted.PreviousHash) == 0 {
				if err := relay.PublishRosterAnchor(ctx, string(bundle.DeviceToken), trusted); err != nil {
					return false, err
				}
				continue
			}
			return false, err
		}
		encodedPage, err := json.Marshal(response)
		if err != nil {
			return false, err
		}
		totalBytes += len(encodedPage)
		if totalBytes > 8<<20 || len(response.Updates) > 16 || response.HeadVersion < trusted.Version || response.HeadVersion >= protocol.MaxChainedRosterDevices {
			return false, rosterProtocolError("roster response exceeds bounds or rolls back the trusted head")
		}
		for _, next := range response.Updates {
			if err := protocol.VerifyPairingRosterAdvance(trusted, next); err != nil {
				return false, &syncclient.RelayProtocolError{Err: err}
			}
			trusted = clonePairingRoster(next)
		}
		digest, err = protocol.PairingRosterDigest(trusted)
		if err != nil {
			return false, err
		}
		if trusted.Version > response.HeadVersion {
			return false, rosterProtocolError("roster page passes the advertised head")
		}
		if trusted.Version < response.HeadVersion {
			if len(response.Updates) == 0 {
				return false, rosterProtocolError("roster pagination made no progress")
			}
			continue
		}
		if hex.EncodeToString(digest) != response.HeadHash {
			return false, rosterProtocolError("roster head digest differs from the verified chain")
		}
		if trusted.Version == bundle.TrustedRoster.Version {
			return false, nil
		}
		for _, suffix := range []string{creatorPairingSuffix, joiningPairingSuffix} {
			journalProfile, err := pairingProfile(profile, suffix)
			if err != nil {
				return false, err
			}
			journal, present, err := loadOptionalSecret(ctx, secrets, journalProfile)
			zeroBytes(journal)
			if err != nil {
				return false, err
			}
			if present {
				return false, errors.New("finish the pending pairing before advancing local trust")
			}
		}
		previous, err := EncodeCredentialBundle(*bundle)
		if err != nil {
			return false, err
		}
		defer zeroBytes(previous)
		nextBundle, err := DecodeCredentialBundle(previous)
		if err != nil {
			return false, err
		}
		committed := false
		defer func() {
			if !committed {
				nextBundle.Zero()
			}
		}()
		if err := applyTrustedRoster(&nextBundle, trusted); err != nil {
			return false, err
		}
		next, err := EncodeCredentialBundle(nextBundle)
		if err != nil {
			return false, err
		}
		defer zeroBytes(next)
		if bytes.Equal(previous, next) {
			return false, errors.New("roster advancement did not change the credential checkpoint")
		}
		if err := replaceSecretExact(ctx, secrets, profile, previous, next); err != nil {
			return false, err
		}
		bundle.Zero()
		*bundle = nextBundle
		committed = true
		return true, nil
	}
	return false, rosterProtocolError("roster refresh exceeded the bounded page count")
}

func rosterProtocolError(message string) error {
	return &syncclient.RelayProtocolError{Err: errors.New(message)}
}
