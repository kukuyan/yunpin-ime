// SPDX-License-Identifier: Apache-2.0
package desktopagent

import "time"

// windowsIdleSample is one observation of the current interactive session.
// Unknown states and failed queries are represented by valid=false, never idle.
type windowsIdleSessionState uint8

const (
	windowsIdleUnknown windowsIdleSessionState = iota
	windowsIdleActive
	windowsIdleLocked
	windowsIdleDisconnected
)

type windowsIdleSample struct {
	valid         bool
	sessionID     uint32
	state         windowsIdleSessionState
	lastInputTick uint32
	uptimeMS      uint64
	otherActive   bool
	// WTS and power notifications invalidate qualification even when two
	// opposite transitions occurred between lightweight samples.
	eventRevision uint64
}

// windowsIdleTracker requires continuous observations, not an assumed idle
// interval carried across a restart or failed query. It is an observation aid,
// not a maintenance authorization: the host must recheck under its input lock.
// Production samples include an epoch maintained by a continuously pumped WTS
// notification window. This is still a hint; the host must independently gate.
// Poll gaps above maxGap restart qualification (e.g. suspend/resume or an
// observer that stopped running). Use one instance per target session.
type windowsIdleTracker struct {
	initialized bool
	last        windowsIdleSample
	sinceMS     uint64
}

func (t *windowsIdleTracker) eligible(s windowsIdleSample, minimum, maxGap time.Duration) bool {
	if !s.valid || s.sessionID == 0 || s.otherActive || s.state < windowsIdleActive || s.state > windowsIdleDisconnected ||
		minimum <= 0 || maxGap <= 0 || minimum%time.Millisecond != 0 || maxGap%time.Millisecond != 0 ||
		uint32(uint32(s.uptimeMS)-s.lastInputTick) > 0x7fffffff {
		t.initialized = false
		return false
	}
	if !t.initialized || s.sessionID != t.last.sessionID || s.state != t.last.state ||
		s.eventRevision != t.last.eventRevision ||
		s.lastInputTick != t.last.lastInputTick || s.uptimeMS < t.last.uptimeMS ||
		s.uptimeMS-t.last.uptimeMS > uint64(maxGap/time.Millisecond) {
		t.initialized = true
		t.last = s
		t.sinceMS = s.uptimeMS
		return false
	}
	t.last = s
	return s.uptimeMS-t.sinceMS >= uint64(minimum/time.Millisecond)
}
