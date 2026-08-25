// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import android.app.job.JobInfo
import android.app.job.JobScheduler
import android.content.ComponentName
import android.content.Context

object SyncScheduler {
    private const val PERIODIC_JOB_ID = 0x595001
    private const val IMMEDIATE_JOB_ID = 0x595002
    private const val RETRY_JOB_ID = 0x595003
    private const val PERIOD_MILLIS = 15L * 60L * 1000L
    private const val RETRY_MILLIS = 30L * 1000L
    private const val MAXIMUM_RETRY_MILLIS = 6L * 60L * 60L * 1000L

    fun ensurePeriodic(context: Context): Boolean {
        val scheduler = context.getSystemService(JobScheduler::class.java)
        if (scheduler.allPendingJobs.any { it.id == PERIODIC_JOB_ID }) return true
        val job = baseBuilder(context, PERIODIC_JOB_ID)
            .setPeriodic(PERIOD_MILLIS)
            .setPersisted(true)
            .build()
        return scheduler.schedule(job) == JobScheduler.RESULT_SUCCESS
    }

    /** Used by explicit Sync Now and after an app-owned local commit. */
    fun enqueueImmediate(context: Context): Boolean {
        val job = baseBuilder(context, IMMEDIATE_JOB_ID)
            .setMinimumLatency(0)
            .setOverrideDeadline(RETRY_MILLIS)
            .build()
        return context.getSystemService(JobScheduler::class.java).schedule(job) == JobScheduler.RESULT_SUCCESS
    }

    fun enqueueRetry(context: Context, requestedDelayMillis: Long): Boolean {
        val delay = requestedDelayMillis.coerceIn(RETRY_MILLIS, MAXIMUM_RETRY_MILLIS)
        val deadline = (delay + (delay / 4).coerceAtMost(5L * 60L * 1000L))
            .coerceAtMost(MAXIMUM_RETRY_MILLIS)
        val job = baseBuilder(context, RETRY_JOB_ID)
            .setMinimumLatency(delay)
            .setOverrideDeadline(deadline)
            .setPersisted(true)
            .build()
        return context.getSystemService(JobScheduler::class.java).schedule(job) == JobScheduler.RESULT_SUCCESS
    }

    fun cancelDataPlane(context: Context) {
        val scheduler = context.getSystemService(JobScheduler::class.java)
        scheduler.cancel(PERIODIC_JOB_ID)
        scheduler.cancel(IMMEDIATE_JOB_ID)
        scheduler.cancel(RETRY_JOB_ID)
    }

    private fun baseBuilder(context: Context, id: Int): JobInfo.Builder =
        JobInfo.Builder(id, ComponentName(context, YunPinSyncJobService::class.java))
            .setRequiredNetworkType(JobInfo.NETWORK_TYPE_ANY)
            .setBackoffCriteria(RETRY_MILLIS, JobInfo.BACKOFF_POLICY_EXPONENTIAL)
}
