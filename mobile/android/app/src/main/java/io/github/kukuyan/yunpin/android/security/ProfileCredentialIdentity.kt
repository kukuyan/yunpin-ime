// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.security

import io.github.kukuyan.yunpin.android.config.ProfileIdentity

/** Canonical bytes used for both credential preference keys and AES-GCM AAD. */
internal object ProfileCredentialIdentity {
    fun canonical(raw: String): String? {
        return ProfileIdentity.canonical(raw)
    }

    fun aadBytes(canonicalId: String): ByteArray {
        require(canonical(canonicalId) == canonicalId)
        return canonicalId.toByteArray(Charsets.UTF_8)
    }
}
