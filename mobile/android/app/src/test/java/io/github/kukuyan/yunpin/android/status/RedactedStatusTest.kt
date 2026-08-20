// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.status

import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test

class RedactedStatusTest {
    @Test
    fun renderedStatusContainsOnlyCategoriesAndCounters() {
        val rendered = RedactedStatus(
            phase = SyncPhase.SUCCEEDED,
            endpointConfigured = true,
            credentialsPresent = true,
            cursor = 8,
            pendingEncryptedEnvelopes = 2,
            snapshotPresent = true,
            controlPlaneGate = ControlPlaneGate.SIGNED_ROSTER_CHAIN_REQUIRED,
        ).render()
        assertTrue(rendered.contains("Committed cursor: 8"))
        assertFalse(rendered.contains("sync.example.test"))
        assertFalse(rendered.contains("Bearer"))
        assertFalse(rendered.contains("合成晨星"))
    }
}
