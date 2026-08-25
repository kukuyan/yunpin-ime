// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import java.io.Closeable

/**
 * Publishes a fully built, profile-bound replacement without exposing either a
 * half-loaded value or a late result for a profile that is no longer active.
 *
 * Snapshot I/O happens outside this lock. A loader captures an observation,
 * reconciles the active profile, then presents the final active-profile
 * observation together with its lease. Any intervening profile observation
 * invalidates the lease before publication.
 */
internal class LastGoodSlot<T : Closeable> : Closeable {
    internal data class Observation(val revision: Long)
    internal data class LoadLease(val profilePath: String, val revision: Long)
    internal data class ActiveProfileState(val changed: Boolean, val available: Boolean)

    private val lock = Any()
    private var current: T? = null
    private var activeProfilePath: String? = null
    private var revision = 0L
    private var closed = false

    fun isAvailable(): Boolean = synchronized(lock) { current != null && !closed }

    fun <R> withCurrent(block: (T) -> R): R? = synchronized(lock) {
        if (closed) null else current?.let(block)
    }

    /** Returns a value together with the exact publication revision that made it. */
    fun <R> withCurrentObserved(block: (T, Observation) -> R): R? = synchronized(lock) {
        if (closed) null else current?.let { block(it, Observation(revision)) }
    }

    /** Runs only while [observation] still names the same published engine. */
    fun <R> withCurrentIf(observation: Observation, block: (T) -> R): R? = synchronized(lock) {
        if (closed || revision != observation.revision) null else current?.let(block)
    }

    fun captureObservation(): Observation = synchronized(lock) { Observation(revision) }

    /** Immediately removes a value owned by a different active profile. */
    fun reconcileActiveProfile(profilePath: String?): ActiveProfileState {
        var previous: T? = null
        val state = synchronized(lock) {
            if (closed || activeProfilePath == profilePath) {
                ActiveProfileState(changed = false, available = current != null && !closed)
            } else {
                activeProfilePath = profilePath
                revision += 1
                previous = current
                current = null
                ActiveProfileState(changed = true, available = false)
            }
        }
        previous?.close()
        return state
    }

    fun observeActiveProfile(profilePath: String?): Boolean = reconcileActiveProfile(profilePath).changed

    /** Invalidates both the published value and every in-flight loader lease. */
    fun invalidate() {
        var previous: T? = null
        synchronized(lock) {
            if (closed) return
            activeProfilePath = null
            revision += 1
            previous = current
            current = null
        }
        previous?.close()
    }

    /**
     * Reconciles the profile observed after [observation] was captured. A newer
     * observation wins, so a loader that read an old pointer cannot switch the
     * slot back to that old profile.
     */
    fun beginLoad(observation: Observation, observedProfilePath: String?): LoadLease? {
        var previous: T? = null
        val lease = synchronized(lock) {
            if (closed || revision != observation.revision) {
                null
            } else {
                if (activeProfilePath != observedProfilePath) {
                    activeProfilePath = observedProfilePath
                    revision += 1
                    previous = current
                    current = null
                }
                observedProfilePath?.let { LoadLease(it, revision) }
            }
        }
        previous?.close()
        return lease
    }

    /**
     * The final active profile reconciliation and last-good replacement happen
     * under one lock. A late loader can therefore only be rejected; it can never
     * become visible after another profile observation.
     */
    fun publishIfCurrent(
        replacement: T,
        lease: LoadLease,
        observedActiveProfilePath: String?,
        ready: Boolean,
    ): Boolean {
        var previous: T? = null
        val accepted = synchronized(lock) {
            if (closed || revision != lease.revision || activeProfilePath != lease.profilePath) {
                false
            } else if (observedActiveProfilePath != lease.profilePath) {
                activeProfilePath = observedActiveProfilePath
                revision += 1
                previous = current
                current = null
                false
            } else if (!ready) {
                false
            } else {
                previous = current
                current = replacement
                // Candidate batches are tied to one concrete publication, not
                // just a profile path. Same-profile replacement invalidates
                // stale candidate buttons as well.
                revision += 1
                true
            }
        }
        previous?.close()
        if (!accepted) replacement.close()
        return accepted
    }

    fun activeProfilePath(): String? = synchronized(lock) { activeProfilePath }

    override fun close() {
        val previous = synchronized(lock) {
            if (closed) return
            closed = true
            revision += 1
            current.also { current = null }
        }
        previous?.close()
    }
}
