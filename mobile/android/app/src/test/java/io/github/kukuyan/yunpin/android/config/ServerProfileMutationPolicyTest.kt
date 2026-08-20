// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

import kotlin.test.assertFailsWith
import org.junit.Test

class ServerProfileMutationPolicyTest {
    @Test
    fun existingProfileCannotChangeEndpointOrPlaintextPolicy() {
        val existing = EndpointPolicy.parse("https://first.example.test", allowPrivateHttp = false)
        ServerProfileMutationPolicy.requireEndpointUnchanged(existing, existing)

        assertFailsWith<EndpointValidationException> {
            ServerProfileMutationPolicy.requireEndpointUnchanged(
                existing,
                EndpointPolicy.parse("https://second.example.test", allowPrivateHttp = false),
            )
        }
        assertFailsWith<EndpointValidationException> {
            ServerProfileMutationPolicy.requireEndpointUnchanged(
                existing,
                existing.copy(allowPrivateHttp = true),
            )
        }
    }
}
