// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.snapshot

import android.content.Context
import io.github.kukuyan.yunpin.android.config.ActiveProfilePointer
import io.github.kukuyan.yunpin.android.config.ProfileIdentity
import java.io.File

object SnapshotPaths {
    const val ACTIVE_POINTER_NAME = "active-profile"

    fun activePointerDirectory(context: Context): File = File(context.filesDir, "sync-profiles")

    fun profileDirectory(context: Context, profileId: String): File {
        val canonical = requireNotNull(ProfileIdentity.canonical(profileId)) {
            "Server profile identity is invalid"
        }
        return File(activePointerDirectory(context), canonical)
    }

    fun database(context: Context, profileId: String): File =
        File(profileDirectory(context, profileId), "store.sqlite")

    fun current(context: Context, profileId: String): File =
        File(File(profileDirectory(context, profileId), "candidate-snapshots"), "current.tsv")

    fun rollback(context: Context, profileId: String): File =
        File(File(profileDirectory(context, profileId), "candidate-snapshots"), "current.tsv.rollback")

    fun activeCurrent(context: Context): File? =
        ActiveProfilePointer(context).read()?.let { current(context, it) }
}
