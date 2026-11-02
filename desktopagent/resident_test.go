// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kukuyan/yunpin-ime/localstore"
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

func TestResidentLogsRedactedStartupFailure(t *testing.T) {
	state := privateTestPath(t, "state")
	makePrivateTestDirectory(t, state)
	var got []RunEvent
	err := RunResident(context.Background(), Paths{StateDirectory: state}, ResidentOptions{
		Interval: time.Nanosecond,
		Events:   func(event RunEvent) { got = append(got, event) },
	})
	if err == nil || len(got) != 1 || got[0].Code != "resident_start_failed" {
		t.Fatalf("startup failure was not reported: err=%v events=%+v", err, got)
	}
	content, err := os.ReadFile(EventLogPath(Paths{StateDirectory: state}))
	if err != nil || !strings.Contains(string(content), `"code":"resident_start_failed"`) {
		t.Fatalf("missing startup event: %s %v", content, err)
	}
}

func TestResidentLogsPrivateACLFailureAsSpecificRedactedEvent(t *testing.T) {
	state := privateTestPath(t, "state")
	makePrivateTestDirectory(t, state)
	var got RunEvent
	events, closeLog := residentEventSink(Paths{StateDirectory: state}, func(event RunEvent) { got = event })
	err := fmt.Errorf("secret path: %w", ErrPrivateACLInvalid)
	event, _, _ := classifyRunResult(SyncSummary{}, err, RunOptions{MaxBackoff: time.Minute}, time.Second)
	events(event)
	events(residentFailureEvent(err, false))
	closeLog()
	if got.Code != "resident_start_failed" || got.FailureClass != localstore.SyncFailurePrivateACL {
		t.Fatalf("wrong startup event: %+v", got)
	}
	content, readErr := os.ReadFile(EventLogPath(Paths{StateDirectory: state}))
	if readErr != nil || !strings.Contains(string(content), `"code":"private_acl_invalid"`) ||
		!strings.Contains(string(content), `"code":"resident_start_failed"`) || strings.Contains(string(content), "secret path") {
		t.Fatalf("incorrect or unredacted events: %s %v", content, readErr)
	}
}
