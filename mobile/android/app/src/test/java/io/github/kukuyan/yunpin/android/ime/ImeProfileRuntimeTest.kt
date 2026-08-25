// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import kotlin.test.assertEquals
import kotlin.test.assertTrue
import org.junit.Test

class ImeProfileRuntimeTest {
    @Test
    fun serviceStartingBetweenBarrierPhasesCannotLoadOldProfile() {
        ImeProfileRuntime.beginSwitch()
        val events = mutableListOf<Boolean>()
        val listener = ImeProfileRuntime.Listener { events += it }
        try {
            assertTrue(ImeProfileRuntime.register(listener))
            assertEquals(emptyList(), events)

            ImeProfileRuntime.finishSwitch()

            assertEquals(listOf(true), events)
        } finally {
            ImeProfileRuntime.unregister(listener)
            ImeProfileRuntime.finishSwitch()
        }
    }

    @Test
    fun activeServiceIsSynchronouslyClearedBeforePointerWrite() {
        val events = mutableListOf<Boolean>()
        val listener = ImeProfileRuntime.Listener { events += it }
        ImeProfileRuntime.finishSwitch()
        ImeProfileRuntime.register(listener)
        try {
            ImeProfileRuntime.beginSwitch()
            assertEquals(listOf(false), events)
            ImeProfileRuntime.finishSwitch()
            assertEquals(listOf(false, true), events)
        } finally {
            ImeProfileRuntime.unregister(listener)
            ImeProfileRuntime.finishSwitch()
        }
    }

    @Test
    fun delayedObserverEventCannotLiftAnActiveBarrier() {
        val events = mutableListOf<Boolean>()
        val listener = ImeProfileRuntime.Listener { events += it }
        ImeProfileRuntime.finishSwitch()
        ImeProfileRuntime.register(listener)
        try {
            ImeProfileRuntime.beginSwitch()
            ImeProfileRuntime.pointerChanged()

            assertEquals(listOf(false), events)
            assertTrue(ImeProfileRuntime.register(listener))

            ImeProfileRuntime.finishSwitch()
            assertEquals(listOf(false, true), events)
        } finally {
            ImeProfileRuntime.unregister(listener)
            ImeProfileRuntime.finishSwitch()
        }
    }
}
