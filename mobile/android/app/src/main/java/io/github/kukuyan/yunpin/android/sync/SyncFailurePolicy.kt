// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import io.github.kukuyan.yunpin.android.status.FailureCategory

internal data class SyncFailureDisposition(
    val category: FailureCategory,
    val retry: Boolean,
)

internal object SyncFailurePolicy {
    fun forRedactedCode(code: String): SyncFailureDisposition = when (code) {
        "network_unavailable", "remote_unavailable", "deadline_exceeded", "cancelled" ->
            SyncFailureDisposition(FailureCategory.NETWORK, retry = true)
        "authorization_required" -> SyncFailureDisposition(FailureCategory.AUTHENTICATION, retry = false)
        "remote_conflict", "remote_rejected" -> SyncFailureDisposition(FailureCategory.PROTOCOL, retry = false)
        else -> SyncFailureDisposition(FailureCategory.STORAGE, retry = false)
    }
}
