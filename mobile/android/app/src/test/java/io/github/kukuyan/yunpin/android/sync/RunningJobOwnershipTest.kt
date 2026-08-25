// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test

class RunningJobOwnershipTest {
    @Test
    fun stoppingQueuedJobDoesNotClaimCancellationOwnership() {
        val active = RunningJobOwnership()
        val queued = RunningJobOwnership()
        assertTrue(active.claim())

        assertFalse(queued.stop())
        assertFalse(queued.claim())
        assertTrue(active.stop())
    }

    @Test
    fun stoppingActiveJobRequestsCoordinatorCancellation() {
        val ownership = RunningJobOwnership()
        assertTrue(ownership.claim())

        assertTrue(ownership.stop())
        assertTrue(ownership.isStopped())
        ownership.release()
        assertFalse(ownership.claim())
    }

    @Test
    fun stopBeforeWorkerStartPermanentlyRejectsClaim() {
        val ownership = RunningJobOwnership()

        assertFalse(ownership.stop())
        assertFalse(ownership.claim())
        assertTrue(ownership.isStopped())
    }
}
