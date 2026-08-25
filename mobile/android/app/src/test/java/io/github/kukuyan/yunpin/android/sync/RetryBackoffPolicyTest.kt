// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import kotlin.test.assertEquals
import org.junit.Test

class RetryBackoffPolicyTest {
    @Test
    fun delayGrowsCapsAndAppliesBoundedJitter() {
        val policy = RetryBackoffPolicy(baseDelayMillis = 100, maximumDelayMillis = 400, jitterFraction = 0.2)
        assertEquals(100, policy.delay(1, 0.0))
        assertEquals(200, policy.delay(2, 0.0))
        assertEquals(400, policy.delay(3, 0.0))
        assertEquals(400, policy.delay(9, 1.0))
        assertEquals(320, policy.delay(9, -1.0))
    }
}
