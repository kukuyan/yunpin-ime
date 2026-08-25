// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.snapshot

import android.content.Context
import io.github.kukuyan.yunpin.android.ime.NativeCandidateEngine
import java.security.MessageDigest

data class CandidateSnapshotHealth(
    val present: Boolean,
    val sha256Hex: String?,
) {
    init {
        require(present == (sha256Hex != null))
    }
}

/** Network-free ABI and full snapshot-parse gate used after a foreground upgrade. */
object CandidateSnapshotHealthCheck {
    fun inspect(context: Context, profileId: String?): CandidateSnapshotHealth? {
        if (Thread.currentThread().isInterrupted) return null
        val snapshot = profileId?.let { SnapshotPaths.current(context, it) }
        return inspectPath(snapshot)
    }

    fun inspectCurrent(context: Context, profileId: String): CandidateSnapshotHealth? =
        inspectPath(SnapshotPaths.current(context, profileId))

    fun inspectRollback(context: Context, profileId: String): CandidateSnapshotHealth? =
        inspectPath(SnapshotPaths.rollback(context, profileId))

    private fun inspectPath(snapshot: java.io.File?): CandidateSnapshotHealth? {
        if (Thread.currentThread().isInterrupted) return null
        val bytes = if (snapshot?.exists() == true) {
            try {
                SnapshotReader.read(snapshot)
            } catch (_: Exception) {
                null
            } ?: return null
        } else {
            null
        }
        return try {
            NativeCandidateEngine().use { engine ->
                when {
                    bytes == null -> CandidateSnapshotHealth(present = false, sha256Hex = null)
                    !engine.loadSnapshot(bytes) -> null
                    else -> CandidateSnapshotHealth(
                        present = true,
                        sha256Hex = MessageDigest.getInstance("SHA-256").digest(bytes).toHex(),
                    )
                }
            }
        } catch (_: Exception) {
            null
        } catch (_: LinkageError) {
            null
        } finally {
            bytes?.fill(0)
        }
    }

    private fun ByteArray.toHex(): String = buildString(size * 2) {
        for (byte in this@toHex) {
            val value = byte.toInt() and 0xff
            append(HEX[value ushr 4])
            append(HEX[value and 0x0f])
        }
    }

    private const val HEX = "0123456789abcdef"
}
