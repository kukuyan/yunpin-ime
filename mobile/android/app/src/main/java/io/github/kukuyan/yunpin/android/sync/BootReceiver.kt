// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import io.github.kukuyan.yunpin.android.BuildConfig
import io.github.kukuyan.yunpin.android.appGraph

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == Intent.ACTION_BOOT_COMPLETED || intent.action == Intent.ACTION_MY_PACKAGE_REPLACED) {
            val graph = context.appGraph()
            val profileId = graph.profiles.active()?.id
            if (graph.upgrades.dataPlaneAllowed(BuildConfig.VERSION_CODE.toLong(), profileId)) {
                SyncScheduler.ensurePeriodic(context)
            } else {
                SyncScheduler.cancelDataPlane(context)
            }
        }
    }
}
