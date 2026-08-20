// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test

class ExactCurrentUpgradeHealthTransactionTest {
    @Test
    fun statusSideSnapshotChangeCommitsOnlyTheSubsequentExactCurrentDigest() {
        val digestA = "a".repeat(64)
        val digestC = "c".repeat(64)
        var current = digestA
        var committed: String? = null

        val marked = ExactCurrentUpgradeHealthTransaction.run(
            readLocalSnapshotPresent = {
                current = digestC
                true
            },
            readExactCurrent = { ExactSnapshotFingerprint(true, current) },
            commit = { digest -> committed = digest; true },
        )

        assertTrue(marked)
        assertEquals(digestC, committed)
        assertFalse(committed == digestA)
    }

    @Test
    fun presenceMismatchNeverCommits() {
        var committed = false
        val marked = ExactCurrentUpgradeHealthTransaction.run(
            readLocalSnapshotPresent = { true },
            readExactCurrent = { ExactSnapshotFingerprint(false, null) },
            commit = { committed = true; true },
        )

        assertFalse(marked)
        assertFalse(committed)
    }
}
