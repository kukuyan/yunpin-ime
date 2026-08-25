// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.snapshot

import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test

class SnapshotFormatTest {
    @Test
    fun acceptsOnlyKnownSnapshotHeaders() {
        val valid = (
            "phrase\tpinyin\tsource\tuse_count\tpinned\tlast_used_day\n" +
                "合成晨星\the cheng chen xing\tsynced_learning\t3\tfalse\t1\n"
            ).toByteArray()
        assertTrue(SnapshotFormat.validate(valid))
        assertFalse(SnapshotFormat.validate("text\tcode\n合成远山\the cheng yuan shan\n".toByteArray()))
        assertFalse(SnapshotFormat.validate(byteArrayOf()))
    }
}
