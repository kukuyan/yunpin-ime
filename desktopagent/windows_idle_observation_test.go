// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"testing"
	"time"
)

func TestWindowsIdleRequiresContinuousKnownObservations(t *testing.T) {
	var tr windowsIdleTracker
	s := windowsIdleSample{valid: true, sessionID: 2, state: windowsIdleActive, lastInputTick: 100, uptimeMS: 1000}
	const min, gap = 15 * time.Minute, 2 * time.Minute
	if tr.eligible(s, min, gap) {
		t.Fatal("first observation qualified")
	}
	for i := 1; i <= 15; i++ {
		s.uptimeMS += 60000
		got := tr.eligible(s, min, gap)
		if got != (i == 15) {
			t.Fatalf("minute %d qualified=%v", i, got)
		}
	}
	s.lastInputTick = uint32(s.uptimeMS)
	if tr.eligible(s, min, gap) {
		t.Fatal("input failed to revoke eligibility")
	}
	s.valid = false
	if tr.eligible(s, min, gap) {
		t.Fatal("unknown session qualified")
	}
	s.valid = true
	s.uptimeMS += 60000
	if tr.eligible(s, min, gap) {
		t.Fatal("query failure failed to reset")
	}
}

func TestWindowsIdleSessionTransitionsAndOtherSessions(t *testing.T) {
	const min, gap = time.Minute, 2 * time.Minute
	var tr windowsIdleTracker
	s := windowsIdleSample{valid: true, sessionID: 2, state: windowsIdleActive, lastInputTick: 2, uptimeMS: 10}
	tr.eligible(s, min, gap)
	s.uptimeMS += 60000
	if !tr.eligible(s, min, gap) {
		t.Fatal("active idle never qualified")
	}
	for _, state := range []windowsIdleSessionState{windowsIdleLocked, windowsIdleDisconnected, windowsIdleActive} {
		s.state = state
		s.uptimeMS++
		if tr.eligible(s, min, gap) {
			t.Fatalf("transition to %d did not revoke", state)
		}
		s.uptimeMS += 60000
		if !tr.eligible(s, min, gap) {
			t.Fatalf("state %d never qualified", state)
		}
	}
	s.otherActive = true
	if tr.eligible(s, min, gap) {
		t.Fatal("another active session qualified")
	}
	s.otherActive = false
	if tr.eligible(s, min, gap) {
		t.Fatal("other session clearance failed to reset")
	}
	s.sessionID++
	if tr.eligible(s, min, gap) {
		t.Fatal("changed session qualified")
	}
}

func TestWindowsIdleTickWrapAnomalyAndGap(t *testing.T) {
	const min, gap = time.Minute, 2 * time.Minute
	var tr windowsIdleTracker
	s := windowsIdleSample{valid: true, sessionID: 1, state: windowsIdleActive, lastInputTick: 0xfffffff0, uptimeMS: 0xfffffff8}
	tr.eligible(s, min, gap)
	s.uptimeMS += 60000 // low 32-bit tick has wrapped; same input remains valid.
	if !tr.eligible(s, min, gap) {
		t.Fatal("tick wrap rejected valid unchanged input")
	}
	s.lastInputTick = uint32(s.uptimeMS) - 10 // new input after wrap
	if tr.eligible(s, min, gap) {
		t.Fatal("new input after wrap qualified")
	}
	s.lastInputTick = uint32(s.uptimeMS) + 100 // future/ambiguous tick
	if tr.eligible(s, min, gap) {
		t.Fatal("future tick qualified")
	}
	s.lastInputTick = uint32(s.uptimeMS)
	tr.eligible(s, min, gap)
	s.uptimeMS += 3 * 60000
	if tr.eligible(s, min, gap) {
		t.Fatal("observation gap qualified")
	}
	s.uptimeMS--
	if tr.eligible(s, min, gap) {
		t.Fatal("clock reversal qualified")
	}
}
