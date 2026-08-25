// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class MobileCoreJsonInstrumentedTest {
    @Test
    fun parsesOnlyRedactedSyncCounters() {
        val report = MobileCoreJson.parseSync(
            """{"ok":true,"result":{"Rounds":1,"Uploaded":1,"Downloaded":2,"Cursor":7,"Pending":0,"SnapshotRows":2,"SnapshotChanged":true}}""",
        )
        assertEquals(7L, report.cursor)
        assertEquals(2, report.snapshotRows)
        assertTrue(report.snapshotChanged)
    }

    @Test
    fun mapsFacadeFailureWithoutRetainingRawDetails() {
        val error = assertFailsWith<MobileCoreBindingException> {
            MobileCoreJson.parseSync("""{"ok":false,"error_code":"network_unavailable"}""")
        }
        assertEquals("network_unavailable", error.redactedCode)
    }

    @Test
    fun acceptsRedactedRemoteUnavailableFailure() {
        val error = assertFailsWith<MobileCoreBindingException> {
            MobileCoreJson.parseSync("""{"ok":false,"error_code":"remote_unavailable"}""")
        }
        assertEquals("remote_unavailable", error.redactedCode)
    }

    @Test
    fun rejectsUnknownControlPlaneGate() {
        val error = assertFailsWith<MobileCoreBindingException> {
            MobileCoreJson.parseStatus(
                """{"ok":true,"result":{"Cursor":0,"Pending":0,"Prepared":false,"SnapshotPresent":false,"RollbackPresent":false,"ControlPlaneGate":"untrusted"}}""",
            )
        }
        assertEquals("local_state_error", error.redactedCode)
    }

    @Test
    fun parsesLearningAndSnapshotResultsWithoutPhraseData() {
        val learn = MobileCoreJson.parseLearn(
            """{"ok":true,"result":{"Recorded":true,"UseCount":3,"SyncEligible":true}}""",
        )
        val snapshot = MobileCoreJson.parseSnapshot(
            """{"ok":true,"result":{"Generation":9,"Rows":4,"Changed":true,"RollbackAvailable":true}}""",
        )

        assertTrue(learn.recorded)
        assertEquals(3L, learn.useCount)
        assertEquals(9L, snapshot.generation)
        assertEquals(4, snapshot.rows)
    }

    @Test
    fun rejectsUnknownFieldsAndNonLongIntegralRepresentations() {
        listOf(
            """{"ok":true,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true,"RawError":"must-not-pass"}}""",
            """{"ok":true,"result":{"Recorded":true,"UseCount":1.0,"SyncEligible":true}}""",
            """{"ok":true,"result":{"Recorded":true,"UseCount":9223372036854775808,"SyncEligible":true}}""",
        ).forEach { encoded ->
            val error = assertFailsWith<MobileCoreBindingException> {
                MobileCoreJson.parseLearn(encoded)
            }
            assertEquals("local_state_error", error.redactedCode)
        }
    }

    @Test
    fun failureEnvelopeRejectsAdditionalRawDetails() {
        val error = assertFailsWith<MobileCoreBindingException> {
            MobileCoreJson.parseSync(
                """{"ok":false,"error_code":"network_unavailable","error":"private detail"}""",
            )
        }
        assertEquals("local_state_error", error.redactedCode)
    }

    @Test
    fun rejectsTrailingTokensAndSecondRootObject() {
        listOf(
            """{"ok":false,"error_code":"network_unavailable"}junk""",
            """{"ok":false,"error_code":"network_unavailable"}{"ok":true}""",
        ).forEach { encoded ->
            val error = assertFailsWith<MobileCoreBindingException> { MobileCoreJson.parseSync(encoded) }
            assertEquals("local_state_error", error.redactedCode)
        }
    }

    @Test
    fun rejectsDuplicateKeysAtRootAndResultDepth() {
        listOf(
            """{"ok":true,"ok":true,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true}}""",
            """{"ok":true,"result":{"Recorded":true,"UseCount":1,"UseCount":2,"SyncEligible":true}}""",
            """{"ok":true,"result":{"Recorded":true,"UseCount":1,"SyncEligible":true},"\u0072esult":{"Recorded":true,"UseCount":1,"SyncEligible":true}}""",
        ).forEach { encoded ->
            val error = assertFailsWith<MobileCoreBindingException> { MobileCoreJson.parseLearn(encoded) }
            assertEquals("local_state_error", error.redactedCode)
        }
    }
}
