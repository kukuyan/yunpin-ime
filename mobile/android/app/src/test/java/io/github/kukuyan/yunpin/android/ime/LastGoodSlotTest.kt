// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import java.io.Closeable
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue
import org.junit.Test

class LastGoodSlotTest {
    @Test
    fun publishesOnlyCompleteReplacementAndClosesPreviousValue() {
        val slot = LastGoodSlot<FakeEngine>()
        slot.observeActiveProfile("profile-a")
        val first = FakeEngine("first")
        val replacement = FakeEngine("replacement")
        val firstLease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        assertTrue(slot.publishIfCurrent(first, firstLease, "profile-a", ready = true))
        assertEquals("first", slot.withCurrent { it.value })
        val replacementLease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        assertTrue(slot.publishIfCurrent(replacement, replacementLease, "profile-a", ready = true))
        assertTrue(first.closed)
        assertEquals("replacement", slot.withCurrent { it.value })
        slot.close()
        assertTrue(replacement.closed)
        assertFalse(slot.isAvailable())
        assertNull(slot.withCurrent { it.value })
    }

    @Test
    fun closedSlotRejectsAndClosesLateLoaderResult() {
        val slot = LastGoodSlot<FakeEngine>()
        val lease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        slot.close()
        val late = FakeEngine("late")
        assertFalse(slot.publishIfCurrent(late, lease, "profile-a", ready = true))
        assertEquals(1, late.closeCount)
    }

    @Test
    fun failedReplacementIsClosedWithoutClearingLastGoodValue() {
        val slot = LastGoodSlot<FakeEngine>()
        slot.observeActiveProfile("profile-a")
        val lastGood = FakeEngine("last-good")
        val failed = FakeEngine("failed")
        val lastGoodLease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        assertTrue(slot.publishIfCurrent(lastGood, lastGoodLease, "profile-a", ready = true))

        val failedLease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        assertFalse(slot.publishIfCurrent(failed, failedLease, "profile-a", ready = false))

        assertEquals(1, failed.closeCount)
        assertEquals(0, lastGood.closeCount)
        assertEquals("last-good", slot.withCurrent { it.value })
    }

    @Test
    fun explicitProfileSwitchClearClosesThePreviousValue() {
        val slot = LastGoodSlot<FakeEngine>()
        val previousProfile = FakeEngine("previous-profile")
        val lease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        assertTrue(slot.publishIfCurrent(previousProfile, lease, "profile-a", ready = true))

        slot.observeActiveProfile("profile-b")

        assertTrue(previousProfile.closed)
        assertFalse(slot.isAvailable())
    }

    @Test
    fun profileSwitchAndLatePublicationAreOneAtomicDecision() {
        val slot = LastGoodSlot<FakeEngine>()
        val firstLease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        val first = FakeEngine("profile-a")
        assertTrue(slot.publishIfCurrent(first, firstLease, "profile-a", ready = true))

        val lateLease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        val late = FakeEngine("late-profile-a")
        assertFalse(slot.publishIfCurrent(late, lateLease, "profile-b", ready = true))

        assertTrue(first.closed)
        assertTrue(late.closed)
        assertEquals("profile-b", slot.activeProfilePath())
        assertFalse(slot.isAvailable())
        assertNull(slot.withCurrent { it.value })
    }

    @Test
    fun newerInputThreadObservationInvalidatesAnOlderLoaderLease() {
        val slot = LastGoodSlot<FakeEngine>()
        val oldLease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        slot.observeActiveProfile("profile-b")
        val late = FakeEngine("late-profile-a")

        assertFalse(slot.publishIfCurrent(late, oldLease, "profile-a", ready = true))
        assertTrue(late.closed)
        assertEquals("profile-b", slot.activeProfilePath())
        assertFalse(slot.isAvailable())
    }

    @Test
    fun candidateUseReconciliationImmediatelyDetachesOldProfileEngine() {
        val slot = LastGoodSlot<FakeEngine>()
        val oldEngine = FakeEngine("profile-a")
        val lease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        assertTrue(slot.publishIfCurrent(oldEngine, lease, "profile-a", ready = true))

        val state = slot.reconcileActiveProfile("profile-b")

        assertTrue(state.changed)
        assertFalse(state.available)
        assertTrue(oldEngine.closed)
        assertNull(slot.withCurrent { it.value })
        assertEquals("profile-b", slot.activeProfilePath())
    }

    @Test
    fun fileObserverInvalidationRejectsEveryInFlightLease() {
        val slot = LastGoodSlot<FakeEngine>()
        val oldEngine = FakeEngine("profile-a")
        val published = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        assertTrue(slot.publishIfCurrent(oldEngine, published, "profile-a", ready = true))
        val inFlight = slot.beginLoad(slot.captureObservation(), "profile-a")!!

        slot.invalidate()
        val late = FakeEngine("late-profile-a")

        assertTrue(oldEngine.closed)
        assertFalse(slot.publishIfCurrent(late, inFlight, "profile-a", ready = true))
        assertTrue(late.closed)
        assertFalse(slot.isAvailable())
    }

    @Test
    fun candidateObservationCannotCrossProfileOrSameProfileReplacement() {
        val slot = LastGoodSlot<FakeEngine>()
        val first = FakeEngine("first")
        val firstLease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        assertTrue(slot.publishIfCurrent(first, firstLease, "profile-a", ready = true))
        val candidateObservation = slot.withCurrentObserved { _, observation -> observation }!!

        val replacement = FakeEngine("replacement")
        val replacementLease = slot.beginLoad(slot.captureObservation(), "profile-a")!!
        assertTrue(slot.publishIfCurrent(replacement, replacementLease, "profile-a", ready = true))
        assertNull(slot.withCurrentIf(candidateObservation) { it.value })

        val currentObservation = slot.withCurrentObserved { _, observation -> observation }!!
        slot.invalidate()
        assertNull(slot.withCurrentIf(currentObservation) { it.value })
    }

    private class FakeEngine(val value: String) : Closeable {
        var closeCount = 0
        val closed: Boolean get() = closeCount > 0
        override fun close() {
            closeCount += 1
        }
    }
}
