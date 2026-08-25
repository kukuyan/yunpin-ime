// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.status

enum class SyncPhase {
    NOT_CONFIGURED,
    CREDENTIALS_REQUIRED,
    PROTOCOL_CORE_REQUIRED,
    UPGRADE_HEALTH_REQUIRED,
    IDLE,
    RUNNING,
    RETRY_SCHEDULED,
    FAILED,
    SUCCEEDED,
}

enum class FailureCategory {
    NONE,
    CONFIGURATION,
    KEYSTORE,
    NETWORK,
    AUTHENTICATION,
    PROTOCOL,
    STORAGE,
    INTERNAL,
}

enum class ControlPlaneGate {
    NONE,
    SIGNED_ROSTER_CHAIN_REQUIRED,
}

data class RedactedStatus(
    val phase: SyncPhase = SyncPhase.NOT_CONFIGURED,
    val failure: FailureCategory = FailureCategory.NONE,
    val endpointConfigured: Boolean = false,
    val credentialsPresent: Boolean = false,
    val cursor: Long = 0,
    val pendingEncryptedEnvelopes: Int = 0,
    val snapshotGeneration: Long = 0,
    val snapshotPresent: Boolean = false,
    val rollbackAvailable: Boolean = false,
    val controlPlaneGate: ControlPlaneGate = ControlPlaneGate.NONE,
    val lastAttemptEpochMillis: Long = 0,
    val lastSuccessEpochMillis: Long = 0,
) {
    init {
        require(cursor >= 0)
        require(pendingEncryptedEnvelopes >= 0)
        require(snapshotGeneration >= 0)
        require(lastAttemptEpochMillis >= 0)
        require(lastSuccessEpochMillis >= 0)
    }

    fun render(): String = buildString {
        appendLine("State: ${phase.name.lowercase()}")
        appendLine("Failure category: ${failure.name.lowercase()}")
        appendLine("Server selected: $endpointConfigured")
        appendLine("Protected credential present: $credentialsPresent")
        appendLine("Committed cursor: $cursor")
        appendLine("Queued encrypted envelopes: $pendingEncryptedEnvelopes")
        appendLine("Snapshot generation: $snapshotGeneration")
        appendLine("Snapshot present: $snapshotPresent")
        appendLine("Snapshot rollback available: $rollbackAvailable")
        appendLine("Enrollment gate: ${controlPlaneGate.name.lowercase()}")
        appendLine("Last attempt (epoch ms): $lastAttemptEpochMillis")
        append("Last success (epoch ms): $lastSuccessEpochMillis")
    }
}
