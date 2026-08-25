// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.security

import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertNull
import org.junit.Test

class ProfileCredentialIdentityTest {
    @Test
    fun acceptsOnlyCanonicalUuidAndProducesStableAadBytes() {
        val profileId = "123e4567-e89b-12d3-a456-426614174000"
        assertEquals(profileId, ProfileCredentialIdentity.canonical(profileId))
        assertContentEquals(profileId.toByteArray(), ProfileCredentialIdentity.aadBytes(profileId))
        assertNull(ProfileCredentialIdentity.canonical(profileId.uppercase()))
        assertNull(ProfileCredentialIdentity.canonical("profile-one"))
    }
}
