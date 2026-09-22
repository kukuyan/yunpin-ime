// SPDX-License-Identifier: Apache-2.0
package desktopagent

import "strings"

//go:generate python3 ../scripts/generate_snapshot_syllables.py

var nativeSnapshotSyllables = snapshotSyllableSet(nativeSnapshotSyllablesText)
var nativeSnapshotLegacySyllables = snapshotSyllableSet(nativeSnapshotLegacySyllablesText)

func snapshotSyllableSet(text string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, syllable := range strings.Fields(text) {
		set[syllable] = struct{}{}
	}
	return set
}

// nativeSnapshotCompatible receives canonical learned Pinyin only. The native
// snapshot parser splits existing boundaries; it does not segment a continuous
// code. Keep that rule distinct from protocol identity normalization and from
// public/fuzzy matching. The syllable sets are generated from the native source.
func nativeSnapshotCompatible(phrase, pinyin string, lastUsedDay int64) bool {
	if lastUsedDay < 0 || strings.ContainsFunc(phrase, func(r rune) bool {
		// validNativePhrase already rejects the remaining unsafe UTF-8/control
		// cases; the C++ parser additionally excludes C1 controls.
		return r >= 0x7f && r <= 0x9f
	}) {
		return false
	}
	syllables := strings.Fields(pinyin)
	if len(syllables) == 0 {
		return false
	}
	legacy := false
	codeLength := 0
	for _, syllable := range syllables {
		codeLength += len(syllable)
		if _, valid := nativeSnapshotSyllables[syllable]; valid {
			continue
		}
		_, finiteLegacy := nativeSnapshotLegacySyllables[syllable]
		if finiteLegacy || (len(syllable) == 1 && syllable[0] >= 'a' && syllable[0] <= 'z') {
			legacy = true
			continue
		}
		return false
	}
	return !legacy || codeLength >= 2
}
