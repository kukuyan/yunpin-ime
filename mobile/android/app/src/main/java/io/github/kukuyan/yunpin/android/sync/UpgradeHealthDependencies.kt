// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import android.content.Context
import io.github.kukuyan.yunpin.android.snapshot.CandidateSnapshotHealth
import io.github.kukuyan.yunpin.android.snapshot.CandidateSnapshotHealthCheck

fun interface UpgradeSnapshotHealthReader {
    fun inspect(profileId: String?): CandidateSnapshotHealth?
}

class NativeUpgradeSnapshotHealthReader(context: Context) : UpgradeSnapshotHealthReader {
    private val appContext = context.applicationContext
    override fun inspect(profileId: String?): CandidateSnapshotHealth? =
        CandidateSnapshotHealthCheck.inspect(appContext, profileId)
}

fun interface UpgradeHealthCommitter {
    fun markHealthy(versionCode: Long, profileId: String?, snapshotSha256Hex: String?): Boolean
}

object RejectingUpgradeHealthCommitter : UpgradeHealthCommitter {
    override fun markHealthy(versionCode: Long, profileId: String?, snapshotSha256Hex: String?): Boolean = false
}
