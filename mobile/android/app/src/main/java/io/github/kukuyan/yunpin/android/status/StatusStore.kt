// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.status

import android.content.Context

/** Stores only bounded counters, categories, and timestamps; never error text. */
class StatusStore(context: Context) {
    private val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)

    fun read(): RedactedStatus = RedactedStatus(
        phase = enumValue(preferences.getString("phase", null), SyncPhase.NOT_CONFIGURED),
        failure = enumValue(preferences.getString("failure", null), FailureCategory.NONE),
        endpointConfigured = preferences.getBoolean("endpoint_configured", false),
        credentialsPresent = preferences.getBoolean("credentials_present", false),
        cursor = preferences.getLong("cursor", 0).coerceAtLeast(0),
        pendingEncryptedEnvelopes = preferences.getInt("pending", 0).coerceAtLeast(0),
        snapshotGeneration = preferences.getLong("snapshot_generation", 0).coerceAtLeast(0),
        snapshotPresent = preferences.getBoolean("snapshot_present", false),
        rollbackAvailable = preferences.getBoolean("rollback_available", false),
        controlPlaneGate = enumValue(preferences.getString("control_plane_gate", null), ControlPlaneGate.NONE),
        lastAttemptEpochMillis = preferences.getLong("last_attempt", 0).coerceAtLeast(0),
        lastSuccessEpochMillis = preferences.getLong("last_success", 0).coerceAtLeast(0),
    )

    fun write(status: RedactedStatus) {
        check(
            preferences.edit()
                .putString("phase", status.phase.name)
                .putString("failure", status.failure.name)
                .putBoolean("endpoint_configured", status.endpointConfigured)
                .putBoolean("credentials_present", status.credentialsPresent)
                .putLong("cursor", status.cursor)
                .putInt("pending", status.pendingEncryptedEnvelopes)
                .putLong("snapshot_generation", status.snapshotGeneration)
                .putBoolean("snapshot_present", status.snapshotPresent)
                .putBoolean("rollback_available", status.rollbackAvailable)
                .putString("control_plane_gate", status.controlPlaneGate.name)
                .putLong("last_attempt", status.lastAttemptEpochMillis)
                .putLong("last_success", status.lastSuccessEpochMillis)
                .commit(),
        )
    }

    private inline fun <reified T : Enum<T>> enumValue(raw: String?, fallback: T): T =
        enumValues<T>().firstOrNull { it.name == raw } ?: fallback

    private companion object {
        const val PREFERENCES = "yunpin_redacted_status_v1"
    }
}
