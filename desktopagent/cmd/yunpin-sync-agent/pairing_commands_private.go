// SPDX-License-Identifier: Apache-2.0
//go:build yunpin_pairing_private

package main

import (
	"context"
	"errors"
	"flag"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

const privatePairingCommandsEnabled = true

func commandPairingCancel(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("pairing-cancel", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	confirm := set.Bool("confirm", false, "confirm cancellation of the protected creator pairing journal and relay reservation")
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("pairing-cancel requires --confirm")
	}
	secrets, client, err := pairingComponents(ctx, common)
	if err != nil {
		return err
	}
	var result desktopagent.PairingResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var cancelErr error
		result, cancelErr = desktopagent.CancelCreatorPairing(ctx, client, desktopagent.PairingOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database,
		})
		return cancelErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func commandPairingAbort(ctx context.Context, defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("pairing-abort", flag.ContinueOnError)
	common := addCommonFlags(set, defaults)
	confirm := set.Bool("confirm", false, "confirm rollback of the exact protected joining pairing journal")
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("pairing-abort requires --confirm")
	}
	secrets, client, err := pairingComponents(ctx, common)
	if err != nil {
		return err
	}
	var result desktopagent.PairingResult
	err = desktopagent.WithProcessLock(common.lock, func() error {
		var abortErr error
		result, abortErr = desktopagent.AbortJoiningPairing(ctx, client, desktopagent.PairingOptions{
			Secrets: secrets, Profile: common.profile, DatabasePath: common.database,
		})
		return abortErr
	})
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func runPrivatePairingCommand(ctx context.Context, defaults desktopagent.Paths, arguments []string) (bool, error) {
	if len(arguments) == 0 {
		return false, nil
	}
	switch arguments[0] {
	case "pairing-invite":
		return true, commandPairingInvite(ctx, defaults, arguments[1:])
	case "pairing-approve":
		return true, commandPairingApprove(ctx, defaults, arguments[1:])
	case "pairing-finalize":
		return true, commandPairingFinalize(ctx, defaults, arguments[1:])
	case "pairing-join":
		return true, commandPairingJoin(ctx, defaults, arguments[1:])
	case "pairing-claim":
		return true, commandPairingClaim(ctx, defaults, arguments[1:])
	case "pairing-cancel":
		return true, commandPairingCancel(ctx, defaults, arguments[1:])
	case "pairing-abort":
		return true, commandPairingAbort(ctx, defaults, arguments[1:])
	default:
		return false, nil
	}
}
