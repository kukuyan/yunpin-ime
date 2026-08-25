// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"path/filepath"
	"testing"
)

func TestResidentEventSinkForwardsEventsWhenLogIsUnavailable(t *testing.T) {
	state := privateTestPath(t, "state")
	makePrivateTestDirectory(t, state)
	// A regular file where the log directory belongs makes OpenEventLog fail
	// without relying on platform-specific permission behavior.
	writePrivateTestFile(t, filepath.Join(state, eventLogDirectory), []byte("blocked"))
	called := false
	events, closeLog := residentEventSink(Paths{StateDirectory: state}, func(event RunEvent) {
		called = event.Code == "sync_failed"
	})
	defer closeLog()
	events(RunEvent{Code: "sync_failed", FailureClass: "local_store"})
	if !called {
		t.Fatal("an unavailable diagnostic log stopped resident event delivery")
	}
	if EventLogAvailable(Paths{StateDirectory: state}) {
		t.Fatal("blocked log path was reported available")
	}
}
