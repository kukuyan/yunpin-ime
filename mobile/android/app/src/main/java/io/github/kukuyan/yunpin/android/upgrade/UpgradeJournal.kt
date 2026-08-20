// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.upgrade

import android.content.Context
import io.github.kukuyan.yunpin.android.config.ProfileIdentity

data class UpgradeState(
    val observedVersion: Long,
    val previousVersion: Long,
    val healthPending: Boolean,
    val rollbackSuggested: Boolean,
    val lastKnownGoodSnapshotPresent: Boolean,
    val lastKnownGoodSnapshotGeneration: Long,
)

/**
 * Records an upgrade health checkpoint without attempting APK installation or
 * rollback. Snapshot rollback remains an explicit local action in the app.
 */
class UpgradeJournal(context: Context) : LkgDigestLease {
    private val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)

    @Synchronized
    fun recordLaunch(versionCode: Long, profileId: String?): UpgradeState {
        require(versionCode > 0)
        val scope = scope(profileId)
        val observed = preferences.getLong(scope.key(OBSERVED_VERSION), 0)
        val previous = preferences.getLong(scope.key(PREVIOUS_VERSION), 0)
        val wasPending = preferences.getBoolean(scope.key(HEALTH_PENDING), false)
        val rollbackSuggested = wasPending && previous != 0L
        if (observed != versionCode) {
            check(
                preferences.edit()
                    .putLong(scope.key(PREVIOUS_VERSION), observed)
                    .putLong(scope.key(OBSERVED_VERSION), versionCode)
                    .putBoolean(scope.key(HEALTH_PENDING), true)
                    .putBoolean(scope.key(ROLLBACK_SUGGESTED), rollbackSuggested)
                    .commit(),
            )
        } else if (rollbackSuggested) {
            preferences.edit().putBoolean(scope.key(ROLLBACK_SUGGESTED), true).commit()
        }
        return read(profileId)
    }

    @Synchronized
    fun markHealthy(versionCode: Long, profileId: String?, snapshotSha256Hex: String?): Boolean {
        val scope = scope(profileId)
        if (preferences.getLong(scope.key(OBSERVED_VERSION), 0) != versionCode) return false
        val previousPresent = preferences.getBoolean(scope.key(SNAPSHOT_PRESENT), false)
        val previousGeneration = preferences.getLong(scope.key(SNAPSHOT_GENERATION), 0)
        val previousDigest = preferences.getString(scope.key(SNAPSHOT_SHA256), null)
        val previousBindingValid = if (previousPresent) {
            previousGeneration > 0 && previousDigest != null && SHA256.matches(previousDigest)
        } else {
            previousGeneration == 0L && previousDigest == null
        }
        if (!previousBindingValid) {
            return false
        }
        if (snapshotSha256Hex == null && previousPresent) return false
        if (snapshotSha256Hex != null && !SHA256.matches(snapshotSha256Hex)) return false

        val editor = preferences.edit()
            .putBoolean(scope.key(HEALTH_PENDING), false)
            .putBoolean(scope.key(ROLLBACK_SUGGESTED), false)
        if (snapshotSha256Hex == null) {
            // Legitimate only before this profile has ever established a
            // validated snapshot generation.
            editor.putBoolean(scope.key(SNAPSHOT_PRESENT), false)
                .putLong(scope.key(SNAPSHOT_GENERATION), 0)
                .remove(scope.key(SNAPSHOT_SHA256))
        } else {
            val generation = when {
                previousPresent && previousDigest == snapshotSha256Hex -> previousGeneration
                previousGeneration == Long.MAX_VALUE -> return false
                else -> previousGeneration + 1
            }
            editor.putBoolean(scope.key(SNAPSHOT_PRESENT), true)
                .putLong(scope.key(SNAPSHOT_GENERATION), generation)
                .putString(scope.key(SNAPSHOT_SHA256), snapshotSha256Hex)
        }
        return editor.commit()
    }

    @Synchronized
    fun read(profileId: String?): UpgradeState {
        val scope = scope(profileId)
        return UpgradeState(
            observedVersion = preferences.getLong(scope.key(OBSERVED_VERSION), 0),
            previousVersion = preferences.getLong(scope.key(PREVIOUS_VERSION), 0),
            healthPending = preferences.getBoolean(scope.key(HEALTH_PENDING), false),
            rollbackSuggested = preferences.getBoolean(scope.key(ROLLBACK_SUGGESTED), false),
            lastKnownGoodSnapshotPresent = preferences.getBoolean(scope.key(SNAPSHOT_PRESENT), false),
            lastKnownGoodSnapshotGeneration = preferences.getLong(scope.key(SNAPSHOT_GENERATION), 0),
        )
    }

    @Synchronized
    fun dataPlaneAllowed(versionCode: Long, profileId: String?): Boolean {
        if (versionCode <= 0) return false
        val scope = scope(profileId)
        return preferences.getLong(scope.key(OBSERVED_VERSION), 0) == versionCode &&
            !preferences.getBoolean(scope.key(HEALTH_PENDING), true) &&
            !preferences.getBoolean(scope.key(ROLLBACK_SUGGESTED), true)
    }

    /** Returns a digest only when the complete per-profile LKG binding is valid. */
    @Synchronized
    fun lastKnownGoodSnapshotDigest(profileId: String): String? {
        val scope = scope(profileId)
        if (!preferences.getBoolean(scope.key(SNAPSHOT_PRESENT), false)) return null
        if (preferences.getLong(scope.key(SNAPSHOT_GENERATION), 0) <= 0) return null
        return preferences.getString(scope.key(SNAPSHOT_SHA256), null)?.takeIf(SHA256::matches)
    }

    /** Holds the journal monitor across an entire fingerprint-bound restore. */
    @Synchronized
    override fun <T> withCurrentLkgDigest(
        profileId: String,
        expectedDigest: String,
        block: () -> T,
    ): T? {
        if (lastKnownGoodSnapshotDigest(profileId) != expectedDigest) return null
        val result = block()
        if (lastKnownGoodSnapshotDigest(profileId) != expectedDigest) return null
        return result
    }

    private fun scope(profileId: String?): String = profileId?.let {
        requireNotNull(ProfileIdentity.canonical(it)) { "Server profile identity is invalid" }
    } ?: APP_SHELL

    private fun String.key(suffix: String) = "$this.$suffix"

    private companion object {
        const val PREFERENCES = "yunpin_upgrade_journal_v2"
        const val APP_SHELL = "app-shell"
        const val OBSERVED_VERSION = "observed_version"
        const val PREVIOUS_VERSION = "previous_version"
        const val HEALTH_PENDING = "health_pending"
        const val ROLLBACK_SUGGESTED = "rollback_suggested"
        const val SNAPSHOT_PRESENT = "snapshot_present"
        const val SNAPSHOT_GENERATION = "snapshot_generation"
        const val SNAPSHOT_SHA256 = "snapshot_sha256"
        val SHA256 = Regex("[0-9a-f]{64}")
    }
}
