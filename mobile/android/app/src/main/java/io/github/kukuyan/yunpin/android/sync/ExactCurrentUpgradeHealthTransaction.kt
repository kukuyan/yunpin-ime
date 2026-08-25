// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

internal data class ExactSnapshotFingerprint(
    val present: Boolean,
    val sha256Hex: String?,
)

/** Orders status, exact-current fingerprinting, then the journal commit. */
internal object ExactCurrentUpgradeHealthTransaction {
    fun run(
        readLocalSnapshotPresent: () -> Boolean?,
        readExactCurrent: () -> ExactSnapshotFingerprint?,
        commit: (String?) -> Boolean,
    ): Boolean {
        val localPresent = readLocalSnapshotPresent() ?: return false
        val current = readExactCurrent() ?: return false
        if (current.present != (current.sha256Hex != null)) return false
        if (current.sha256Hex != null && !SHA256.matches(current.sha256Hex)) return false
        if (localPresent != current.present) return false
        return commit(current.sha256Hex)
    }

    private val SHA256 = Regex("[0-9a-f]{64}")
}
