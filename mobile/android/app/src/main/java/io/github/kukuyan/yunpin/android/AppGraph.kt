// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android

import android.content.Context
import io.github.kukuyan.yunpin.android.config.ServerProfileStore
import io.github.kukuyan.yunpin.android.security.KeystoreCredentialStore
import io.github.kukuyan.yunpin.android.status.StatusStore
import io.github.kukuyan.yunpin.android.sync.GeneratedGoMobileCoreFactory
import io.github.kukuyan.yunpin.android.sync.DataPlaneGate
import io.github.kukuyan.yunpin.android.sync.SyncCoordinator
import io.github.kukuyan.yunpin.android.sync.UpgradeHealthCommitter
import io.github.kukuyan.yunpin.android.upgrade.UpgradeJournal

class AppGraph(context: Context) {
    private val appContext = context.applicationContext
    val profiles = ServerProfileStore(appContext)
    val credentials = KeystoreCredentialStore(appContext)
    val statuses = StatusStore(appContext)
    val upgrades = UpgradeJournal(appContext)
    val coordinator = SyncCoordinator(
        context = appContext,
        profiles = profiles,
        credentials = credentials,
        coreFactory = GeneratedGoMobileCoreFactory(),
        statuses = statuses,
        dataPlaneGate = DataPlaneGate { profileId ->
            upgrades.dataPlaneAllowed(BuildConfig.VERSION_CODE.toLong(), profileId)
        },
        rollbackLkgLease = upgrades,
        upgradeHealthCommitter = UpgradeHealthCommitter { versionCode, profileId, digest ->
            upgrades.markHealthy(versionCode, profileId, digest)
        },
    )
}
