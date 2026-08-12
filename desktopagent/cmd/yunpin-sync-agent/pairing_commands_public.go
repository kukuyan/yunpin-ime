// SPDX-License-Identifier: Apache-2.0
//go:build !yunpin_pairing_private

package main

import (
	"context"

	"github.com/kukuyan/yunpin-ime/desktopagent"
)

const privatePairingCommandsEnabled = false

func runPrivatePairingCommand(context.Context, desktopagent.Paths, []string) (bool, error) {
	return false, nil
}
