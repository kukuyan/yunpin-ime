// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import java.util.UUID
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test

class SnapshotRollbackTransactionTest {
    private val profileId = UUID.randomUUID().toString()
    private val expected = "a".repeat(64)

    @Test
    fun mismatchedRollbackFingerprintNeverCallsNativeRestore() {
        var restored = false
        val result = SnapshotRollbackTransaction.run(
            profileId,
            expected,
            activeProfileId = { profileId },
            rollbackFingerprint = { "b".repeat(64) },
            restore = { restored = true; true },
            currentFingerprint = { expected },
        )

        assertFalse(result)
        assertFalse(restored)
    }

    @Test
    fun profileSwitchAfterNativeRestoreFailsClosedBeforeCurrentAcceptance() {
        var active = profileId
        var currentRead = false
        val result = SnapshotRollbackTransaction.run(
            profileId,
            expected,
            activeProfileId = { active },
            rollbackFingerprint = { expected },
            restore = { active = UUID.randomUUID().toString(); true },
            currentFingerprint = { currentRead = true; expected },
        )

        assertFalse(result)
        assertFalse(currentRead)
    }

    @Test
    fun successfulRestoreRequiresMatchingCurrentFingerprintAndStableLease() {
        var restored = false
        val result = SnapshotRollbackTransaction.run(
            profileId,
            expected,
            activeProfileId = { profileId },
            rollbackFingerprint = { expected },
            restore = { restored = true; true },
            currentFingerprint = { expected },
        )

        assertTrue(result)
        assertTrue(restored)
    }

    @Test
    fun postRestoreCurrentMismatchFailsClosed() {
        val result = SnapshotRollbackTransaction.run(
            profileId,
            expected,
            activeProfileId = { profileId },
            rollbackFingerprint = { expected },
            restore = { true },
            currentFingerprint = { "b".repeat(64) },
        )

        assertFalse(result)
    }
}
