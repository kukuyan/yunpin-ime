// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import io.github.kukuyan.yunpin.android.config.ProfileIdentity

fun interface DataPlaneGate {
    fun allows(profileId: String?): Boolean
}

interface RollbackSnapshotVerifier {
    fun rollbackFingerprint(profileId: String): String?
    fun currentFingerprint(profileId: String): String?
}

/** Pure ordering gate around the native, atomic snapshot restore operation. */
internal object SnapshotRollbackTransaction {
    fun run(
        expectedProfileId: String,
        expectedLkgDigest: String,
        activeProfileId: () -> String?,
        rollbackFingerprint: () -> String?,
        restore: () -> Boolean,
        currentFingerprint: () -> String?,
    ): Boolean {
        if (ProfileIdentity.canonical(expectedProfileId) != expectedProfileId) return false
        if (!SHA256.matches(expectedLkgDigest)) return false
        if (activeProfileId() != expectedProfileId) return false
        if (rollbackFingerprint() != expectedLkgDigest) return false
        if (activeProfileId() != expectedProfileId) return false
        if (!restore()) return false
        if (activeProfileId() != expectedProfileId) return false
        if (currentFingerprint() != expectedLkgDigest) return false
        return activeProfileId() == expectedProfileId
    }

    private val SHA256 = Regex("[0-9a-f]{64}")
}
