// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

import java.util.UUID

/** Canonical non-secret local identity shared by profile storage, paths, and credential AAD. */
internal object ProfileIdentity {
    fun canonical(raw: String): String? {
        val parsed = try {
            UUID.fromString(raw)
        } catch (_: IllegalArgumentException) {
            return null
        }
        return parsed.toString().takeIf { it == raw }
    }
}
