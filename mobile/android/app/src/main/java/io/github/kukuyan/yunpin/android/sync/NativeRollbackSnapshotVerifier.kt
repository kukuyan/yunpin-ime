// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import android.content.Context
import io.github.kukuyan.yunpin.android.snapshot.CandidateSnapshotHealthCheck

class NativeRollbackSnapshotVerifier(context: Context) : RollbackSnapshotVerifier {
    private val appContext = context.applicationContext

    override fun rollbackFingerprint(profileId: String): String? =
        CandidateSnapshotHealthCheck.inspectRollback(appContext, profileId)
            ?.takeIf { it.present }
            ?.sha256Hex

    override fun currentFingerprint(profileId: String): String? =
        CandidateSnapshotHealthCheck.inspectCurrent(appContext, profileId)
            ?.takeIf { it.present }
            ?.sha256Hex
}
