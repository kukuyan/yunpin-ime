// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"reflect"
	"testing"
)

func clock(ms int64, counter uint32, node string) HLC {
	return HLC{WallMillis: ms, Counter: counter, Node: node}
}

func mergeAll(t *testing.T, states ...PhraseState) PhraseState {
	t.Helper()
	result := states[0]
	for _, state := range states[1:] {
		var err error
		result, err = MergePhrase(result, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestCRDTConvergesAcrossOrderAndDuplicates(t *testing.T) {
	base := PhraseState{
		ObjectID: "synthetic-object",
		Counts:   map[string]uint64{"desktop-a": 2},
		Pinned:   LWWBool{Value: false, Clock: clock(10, 0, "desktop-a")},
		Presence: Presence{Present: true, Clock: clock(10, 0, "desktop-a"), Generation: 1},
	}
	second := PhraseState{
		ObjectID: "synthetic-object",
		Counts:   map[string]uint64{"desktop-a": 3, "desktop-b": 4},
		Pinned:   LWWBool{Value: true, Clock: clock(20, 0, "desktop-b")},
		Presence: Presence{Present: true, Clock: clock(10, 0, "desktop-a"), Generation: 1},
	}
	deleted := PhraseState{
		ObjectID: "synthetic-object",
		Counts:   map[string]uint64{"desktop-b": 5},
		Pinned:   LWWBool{Value: false, Clock: clock(15, 0, "desktop-a")},
		Presence: Presence{Present: false, Clock: clock(30, 0, "desktop-a"), Generation: 1},
	}

	forward := mergeAll(t, base, second, deleted, second)
	reverse := mergeAll(t, deleted, second, base, deleted)
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("merge did not converge:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
	if forward.Counts["desktop-a"] != 3 || forward.Counts["desktop-b"] != 5 {
		t.Fatalf("G-Counter merge incorrect: %#v", forward.Counts)
	}
	if !forward.Pinned.Value {
		t.Fatal("newest pin event did not win")
	}
	if forward.Presence.Present {
		t.Fatal("remove-wins tombstone was resurrected by a count")
	}
}

func TestRemoveWinsTieAndExplicitReAdd(t *testing.T) {
	tiedClock := clock(40, 1, "desktop-a")
	present := PhraseState{ObjectID: "x", Counts: map[string]uint64{}, Presence: Presence{Present: true, Clock: tiedClock, Generation: 1}}
	removed := PhraseState{ObjectID: "x", Counts: map[string]uint64{}, Presence: Presence{Present: false, Clock: tiedClock, Generation: 1}}
	merged := mergeAll(t, present, removed)
	if merged.Presence.Present {
		t.Fatal("remove must win an equal timestamp")
	}
	readded := PhraseState{ObjectID: "x", Counts: map[string]uint64{}, Presence: Presence{Present: true, Clock: clock(41, 0, "desktop-b"), Generation: 2}}
	merged = mergeAll(t, merged, readded)
	if !merged.Presence.Present {
		t.Fatal("newer explicit re-add should restore the object")
	}
}

func TestConcurrentRemoveWinsRegardlessOfHLCTieBreak(t *testing.T) {
	present := PhraseState{ObjectID: "x", Counts: map[string]uint64{"b": 9}, Presence: Presence{Present: true, Clock: clock(90, 0, "z-device"), Generation: 3}}
	removed := PhraseState{ObjectID: "x", Counts: map[string]uint64{}, Presence: Presence{Present: false, Clock: clock(80, 0, "a-device"), Generation: 3}}
	merged := mergeAll(t, present, removed)
	if merged.Presence.Present {
		t.Fatal("same-generation concurrent removal must dominate a later-HLC count state")
	}
}

func TestUnknownSettingUsesHLCLWW(t *testing.T) {
	older := SettingState{Key: "future.experimental.option", Value: []byte("opaque-a"), Clock: clock(1, 0, "a")}
	newer := SettingState{Key: "future.experimental.option", Value: []byte("opaque-b"), Clock: clock(2, 0, "b")}
	merged, err := MergeSetting(older, newer)
	if err != nil {
		t.Fatal(err)
	}
	if string(merged.Value) != "opaque-b" {
		t.Fatalf("newest unknown setting was not retained: %#v", merged)
	}
}
