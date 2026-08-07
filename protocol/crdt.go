// SPDX-License-Identifier: Apache-2.0
package protocol

import (
	"bytes"
	"errors"
	"maps"
)

type HLC struct {
	WallMillis int64  `json:"wall_ms" cbor:"1,keyasint"`
	Counter    uint32 `json:"counter" cbor:"2,keyasint"`
	Node       string `json:"node" cbor:"3,keyasint"`
}

func (clock HLC) Compare(other HLC) int {
	if clock.WallMillis < other.WallMillis {
		return -1
	}
	if clock.WallMillis > other.WallMillis {
		return 1
	}
	if clock.Counter < other.Counter {
		return -1
	}
	if clock.Counter > other.Counter {
		return 1
	}
	if clock.Node < other.Node {
		return -1
	}
	if clock.Node > other.Node {
		return 1
	}
	return 0
}

type LWWBool struct {
	Value bool `json:"value" cbor:"1,keyasint"`
	Clock HLC  `json:"clock" cbor:"2,keyasint"`
}

type Presence struct {
	Present    bool   `json:"present" cbor:"1,keyasint"`
	Clock      HLC    `json:"clock" cbor:"2,keyasint"`
	Generation uint64 `json:"generation" cbor:"3,keyasint"`
}

type PhraseState struct {
	ObjectID string            `json:"object_id" cbor:"1,keyasint"`
	Counts   map[string]uint64 `json:"counts" cbor:"2,keyasint"`
	Pinned   LWWBool           `json:"pinned" cbor:"3,keyasint"`
	Presence Presence          `json:"presence" cbor:"4,keyasint"`
}

func chooseBool(left, right LWWBool) LWWBool {
	comparison := left.Clock.Compare(right.Clock)
	if comparison < 0 {
		return right
	}
	if comparison > 0 {
		return left
	}
	if right.Value {
		return right
	}
	return left
}

func choosePresence(left, right Presence) Presence {
	if left.Generation < right.Generation {
		return right
	}
	if left.Generation > right.Generation {
		return left
	}
	// Within the same explicit-add generation a removal dominates every
	// concurrent or later ordinary update. Re-adding requires generation+1.
	if left.Present != right.Present {
		if !right.Present {
			return right
		}
		return left
	}
	comparison := left.Clock.Compare(right.Clock)
	if comparison < 0 {
		return right
	}
	if comparison > 0 {
		return left
	}
	return left
}

// MergePhrase is associative, commutative, and idempotent for a single opaque
// object. Explicit re-add is represented by a newer Presence clock.
func MergePhrase(left, right PhraseState) (PhraseState, error) {
	if left.ObjectID == "" || left.ObjectID != right.ObjectID {
		return PhraseState{}, errors.New("cannot merge different phrase objects")
	}
	result := PhraseState{
		ObjectID: left.ObjectID,
		Counts:   maps.Clone(left.Counts),
		Pinned:   chooseBool(left.Pinned, right.Pinned),
		Presence: choosePresence(left.Presence, right.Presence),
	}
	if result.Counts == nil {
		result.Counts = make(map[string]uint64)
	}
	for device, count := range right.Counts {
		if count > result.Counts[device] {
			result.Counts[device] = count
		}
	}
	return result, nil
}

type SettingState struct {
	Key   string `json:"key" cbor:"1,keyasint"`
	Value []byte `json:"value" cbor:"2,keyasint"`
	Clock HLC    `json:"clock" cbor:"3,keyasint"`
}

// MergeSetting applies HLC-LWW while retaining unknown setting keys as opaque
// client values. Byte comparison resolves only the impossible same-clock,
// different-value case deterministically.
func MergeSetting(left, right SettingState) (SettingState, error) {
	if left.Key == "" || left.Key != right.Key {
		return SettingState{}, errors.New("cannot merge different setting keys")
	}
	comparison := left.Clock.Compare(right.Clock)
	if comparison < 0 {
		return right, nil
	}
	if comparison > 0 {
		return left, nil
	}
	if bytes.Compare(left.Value, right.Value) < 0 {
		return right, nil
	}
	return left, nil
}
