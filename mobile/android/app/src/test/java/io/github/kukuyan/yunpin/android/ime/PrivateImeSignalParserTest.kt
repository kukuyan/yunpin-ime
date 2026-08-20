// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test

class PrivateImeSignalParserTest {
    @Test
    fun acceptsOnlyExactNamespacedPrivacyTokens() {
        val parsed = PrivateImeSignalParser.parse(
            "vendor.option,${PrivateImeSignalParser.PRIVATE_MODE};${PrivateImeSignalParser.ONE_TIME_INPUT}",
        )
        assertTrue(parsed.privateMode)
        assertTrue(parsed.oneTimeInput)

        val unrelated = PrivateImeSignalParser.parse("vendor.private,otp")
        assertFalse(unrelated.privateMode)
        assertFalse(unrelated.oneTimeInput)
    }
}
