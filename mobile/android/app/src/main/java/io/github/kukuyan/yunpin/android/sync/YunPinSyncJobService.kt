// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import android.app.job.JobParameters
import android.app.job.JobService
import io.github.kukuyan.yunpin.android.BuildConfig
import io.github.kukuyan.yunpin.android.appGraph
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.FutureTask
import java.util.concurrent.RejectedExecutionException

class YunPinSyncJobService : JobService() {
    private val executor = Executors.newSingleThreadExecutor()
    private val running = ConcurrentHashMap<Int, RunningJob>()
    private val retryController by lazy(LazyThreadSafetyMode.SYNCHRONIZED) { SyncRetryController(this) }

    override fun onStartJob(params: JobParameters): Boolean {
        if (!dataPlaneAllowed()) return false
        val run = RunningJob()
        val task = FutureTask<Unit>({ executeJob(params, run) }, Unit)
        run.future = task
        if (running.putIfAbsent(params.jobId, run) != null) return false
        return try {
            executor.execute(task)
            true
        } catch (_: RejectedExecutionException) {
            running.remove(params.jobId, run)
            task.cancel(true)
            false
        }
    }

    override fun onStopJob(params: JobParameters): Boolean {
        val run = running.remove(params.jobId) ?: return false
        if (run.ownership.stop()) appGraph().coordinator.cancelActive()
        run.future.cancel(true)
        if (!dataPlaneAllowed()) return false
        val retryScheduled = SyncScheduler.enqueueRetry(this, retryController.recordFailure())
        return !retryScheduled
    }

    override fun onDestroy() {
        var cancelOwnedOperation = false
        running.values.forEach { run ->
            if (run.ownership.stop()) cancelOwnedOperation = true
            run.future.cancel(true)
        }
        if (cancelOwnedOperation) appGraph().coordinator.cancelActive()
        running.clear()
        executor.shutdownNow()
        super.onDestroy()
    }

    private fun executeJob(params: JobParameters, run: RunningJob) {
        if (Thread.currentThread().isInterrupted || !run.ownership.claim()) {
            running.remove(params.jobId, run)
            return
        }
        val retry = try {
            if (Thread.currentThread().isInterrupted || run.ownership.isStopped()) {
                true
            } else if (!dataPlaneAllowed()) {
                false
            } else {
                try {
                    appGraph().coordinator.runBounded().retry
                } catch (_: Exception) {
                    true
                } catch (_: LinkageError) {
                    true
                }
            }
        } finally {
            run.ownership.release()
        }
        if (running.remove(params.jobId, run) && !run.ownership.isStopped()) {
            val usePlatformFallback = if (retry && dataPlaneAllowed()) {
                !SyncScheduler.enqueueRetry(this, retryController.recordFailure())
            } else {
                retryController.reset()
                false
            }
            jobFinished(params, usePlatformFallback)
        }
    }

    private fun dataPlaneAllowed(): Boolean {
        val graph = appGraph()
        return graph.upgrades.dataPlaneAllowed(
            BuildConfig.VERSION_CODE.toLong(),
            graph.profiles.active()?.id,
        )
    }

    private class RunningJob {
        val ownership = RunningJobOwnership()
        lateinit var future: FutureTask<Unit>
    }
}
