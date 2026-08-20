// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlin.test.assertEquals
import kotlin.test.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class NativeCandidateEngineInstrumentedTest {
    @Test
    fun nativeCoreReturnsSyntheticCandidateAndFailsClosedForProtectedContext() {
        val snapshot = (
            "phrase\tpinyin\tsource\tuse_count\tpinned\tlast_used_day\n" +
                "合成晨星\the cheng chen xing\tsynced_learning\t4\ttrue\t1\n"
            ).toByteArray()
        NativeCandidateEngine().use { engine ->
            assertTrue(engine.loadSnapshot(snapshot))
            assertEquals(listOf("合成晨星"), engine.query("hechengchenxing", 2, 0))
            assertTrue(
                engine.query(
                    "hechengchenxing",
                    2,
                    SensitiveEditorPolicy.CONTEXT_PASSWORD,
                ).isEmpty(),
            )
        }
    }
}
