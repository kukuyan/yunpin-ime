// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import org.junit.Test

class EndpointPolicyTest {
    // Every address in this file is a synthetic policy fixture.
    @Test
    fun acceptsUserSuppliedHttpsWithoutPath() {
        val parsed = EndpointPolicy.parse("https://sync.example.test/", allowPrivateHttp = false)
        assertEquals("https://sync.example.test", parsed.normalizedEndpoint)
    }

    @Test
    fun rejectsCredentialsPathQueryAndFragment() {
        listOf(
            "https://name@sync.example.test",
            "https://sync.example.test/v1",
            "https://sync.example.test?mode=test",
            "https://sync.example.test#part",
        ).forEach { value ->
            assertFailsWith<EndpointValidationException> { EndpointPolicy.parse(value, false) }
        }
    }

    @Test
    fun plaintextRequiresBothOptInAndLocality() {
        assertFailsWith<EndpointValidationException> { EndpointPolicy.parse("http://10.20.30.40", false) }
        assertEquals(
            "http://10.20.30.40",
            EndpointPolicy.parse("http://10.20.30.40", allowPrivateHttp = true).normalizedEndpoint,
        )
        assertFailsWith<EndpointValidationException> {
            EndpointPolicy.parse("http://sync.example.test", allowPrivateHttp = true)
        }
    }
}
