// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android

import android.app.Application
import io.github.kukuyan.yunpin.android.sync.SyncScheduler

class YunPinApplication : Application() {
    val graph: AppGraph by lazy(LazyThreadSafetyMode.SYNCHRONIZED) { AppGraph(this) }

    override fun onCreate() {
        super.onCreate()
        // Application.onCreate also runs in :ime. Keep that process free of
        // scheduling, credential access, status writes, and network setup.
        if (Application.getProcessName() != packageName) return
        val version = BuildConfig.VERSION_CODE.toLong()
        val profileId = graph.profiles.active()?.id
        graph.upgrades.recordLaunch(version, profileId)
        if (graph.upgrades.dataPlaneAllowed(version, profileId)) {
            SyncScheduler.ensurePeriodic(this)
        } else {
            SyncScheduler.cancelDataPlane(this)
        }
    }
}

fun android.content.Context.appGraph(): AppGraph =
    (applicationContext as YunPinApplication).graph
