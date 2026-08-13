// SPDX-License-Identifier: Apache-2.0
//go:build !yunpin_pairing_private

package main

import (
	"context"
	"testing"
)

func TestPairingCancelIsExactlyUnknownInDefaultBuild(t *testing.T) {
	for _, command := range []string{"pairing-cancel", "pairing-abort", "e2e-init-empty-baseline"} {
		err := run(context.Background(), []string{command})
		if err == nil || err.Error() != "unknown command" {
			t.Fatalf("default build exposed private pairing command %q: %v", command, err)
		}
	}
}
