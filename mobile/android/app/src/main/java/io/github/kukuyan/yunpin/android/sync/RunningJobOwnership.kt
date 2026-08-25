// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

/**
 * Small, Android-free ownership gate for JobService queue/cancellation races.
 * A queued job that is stopped never becomes the coordinator owner and must
 * not cancel a different job that is currently inside the shared coordinator.
 */
internal class RunningJobOwnership {
    private val lock = Any()
    private var stopped = false
    private var ownsCoordinator = false

    fun claim(): Boolean = synchronized(lock) {
        if (stopped || ownsCoordinator) {
            false
        } else {
            ownsCoordinator = true
            true
        }
    }

    /** Returns true only when this job currently owns the coordinator. */
    fun stop(): Boolean = synchronized(lock) {
        stopped = true
        ownsCoordinator
    }

    fun release() = synchronized(lock) {
        ownsCoordinator = false
    }

    fun isStopped(): Boolean = synchronized(lock) { stopped }
}
