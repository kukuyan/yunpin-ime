// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import org.junit.Test

class ProfileSwitchBarrierTest {
    @Test
    fun clearsImeBeforePointerWriteAndFinishesAfterward() {
        val events = mutableListOf<String>()
        val barrier = RecordingBarrier(events, begins = true)

        withProfileSwitchBarrier(barrier) { events += "write-pointer" }

        assertEquals(listOf("clear-ime", "write-pointer", "allow-load"), events)
    }

    @Test
    fun pointerWriteNeverRunsWhenImeBarrierCannotAcknowledge() {
        val events = mutableListOf<String>()
        val barrier = RecordingBarrier(events, begins = false)

        assertFailsWith<ProfileSwitchBarrierUnavailable> {
            withProfileSwitchBarrier(barrier) { events += "write-pointer" }
        }

        assertEquals(listOf("clear-ime-failed"), events)
    }

    @Test
    fun failedPointerWriteStillReleasesFailClosedReload() {
        val events = mutableListOf<String>()
        val barrier = RecordingBarrier(events, begins = true)

        assertFailsWith<IllegalStateException> {
            withProfileSwitchBarrier(barrier) {
                events += "write-pointer"
                throw IllegalStateException("synthetic")
            }
        }

        assertEquals(listOf("clear-ime", "write-pointer", "allow-load"), events)
    }

    private class RecordingBarrier(
        private val events: MutableList<String>,
        private val begins: Boolean,
    ) : ProfileSwitchBarrier {
        override fun begin(): Boolean {
            events += if (begins) "clear-ime" else "clear-ime-failed"
            return begins
        }

        override fun finish() {
            events += "allow-load"
        }
    }
}
