// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

internal object ImeProfileRuntime {
    internal fun interface Listener {
        fun invalidate(scheduleReload: Boolean)
    }

    private val lock = Any()
    private var switchPending = false
    private var listener: Listener? = null

    /** Returns true when service startup must wait for a matching finish. */
    fun register(value: Listener): Boolean = synchronized(lock) {
        listener = value
        switchPending
    }

    fun unregister(value: Listener) = synchronized(lock) {
        if (listener === value) listener = null
    }

    fun beginSwitch() {
        val target = synchronized(lock) {
            switchPending = true
            listener
        }
        target?.invalidate(false)
    }

    fun finishSwitch() {
        val target = synchronized(lock) {
            switchPending = false
            listener
        }
        target?.invalidate(true)
    }

    fun pointerChanged() {
        // A delayed FileObserver callback from an earlier write must not lift
        // an in-progress app-to-IME barrier and reload the old pointer. The
        // matching finish call is authoritative and schedules the new load.
        val target = synchronized(lock) { if (switchPending) null else listener }
        target?.invalidate(true)
    }
}
