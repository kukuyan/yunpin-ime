// SPDX-License-Identifier: Apache-2.0
package main

import (
	"io"
	"strings"
	"testing"
)

func TestUnknownCommandReturnsUsageBeforeOpeningStore(t *testing.T) {
	err := run(
		[]string{"help", "--root", t.TempDir()},
		strings.NewReader(""),
		io.Discard,
	)
	if err == nil || !strings.HasPrefix(err.Error(), "usage: yunpin-replay-lab") {
		t.Fatalf("unknown command did not return stable usage: %v", err)
	}
}
