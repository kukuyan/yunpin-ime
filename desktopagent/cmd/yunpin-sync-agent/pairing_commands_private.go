// SPDX-License-Identifier: Apache-2.0
//go:build yunpin_pairing_private

package main

import (
	"context"
	"errors"
	"flag"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

func commandE2EInitEmptyBaseline(defaults desktopagent.Paths, arguments []string) error {
	set := flag.NewFlagSet("e2e-init-empty-baseline", flag.ContinueOnError)
	confirm := set.Bool("confirm-create-empty-baseline", false, "confirm creation of the fixed immutable empty baseline on a clean E2E device")
	if err := parse(set, arguments); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("e2e-init-empty-baseline requires --confirm-create-empty-baseline")
	}
	result, err := desktopagent.InitializeEmptyBaseline(defaults)
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func runPrivatePairingCommand(_ context.Context, defaults desktopagent.Paths, arguments []string) (bool, error) {
	if len(arguments) > 0 && arguments[0] == "e2e-init-empty-baseline" {
		return true, commandE2EInitEmptyBaseline(defaults, arguments[1:])
	}
	return false, nil
}
