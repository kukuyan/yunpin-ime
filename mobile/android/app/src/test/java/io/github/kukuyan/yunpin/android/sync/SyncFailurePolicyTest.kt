// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import io.github.kukuyan.yunpin.android.status.FailureCategory
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test

class SyncFailurePolicyTest {
    @Test
    fun retriesOnlyTransientNetworkAndRemoteAvailabilityFailures() {
        val unavailable = SyncFailurePolicy.forRedactedCode("remote_unavailable")
        assertEquals(FailureCategory.NETWORK, unavailable.category)
        assertTrue(unavailable.retry)

        val rejected = SyncFailurePolicy.forRedactedCode("remote_rejected")
        assertEquals(FailureCategory.PROTOCOL, rejected.category)
        assertFalse(rejected.retry)
    }
}
