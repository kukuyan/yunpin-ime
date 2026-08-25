// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import kotlin.test.assertFailsWith
import org.junit.Test

class StrictJsonDuplicateKeyScannerTest {
    @Test
    fun acceptsOneStrictEnvelope() {
        StrictJsonDuplicateKeyScanner(
            """{"ok":true,"result":{"UseCount":1,"SyncEligible":false}}""",
        ).validate()
    }

    @Test
    fun rejectsTrailingTokenAndSecondObject() {
        listOf(
            """{"ok":true}junk""",
            """{"ok":true}{"ok":false}""",
        ).forEach { assertFailsWith<IllegalArgumentException> { StrictJsonDuplicateKeyScanner(it).validate() } }
    }

    @Test
    fun rejectsLiteralAndEscapedDuplicateKeysAtEveryObjectDepth() {
        listOf(
            """{"ok":true,"ok":false}""",
            """{"ok":true,"\u006fk":false}""",
            """{"ok":true,"result":{"UseCount":1,"UseCount":2}}""",
        ).forEach { assertFailsWith<IllegalArgumentException> { StrictJsonDuplicateKeyScanner(it).validate() } }
    }

    @Test
    fun rejectsExcessiveNestingBeforeParserStackCanGrowUnbounded() {
        val encoded = "[".repeat(66) + "null" + "]".repeat(66)
        assertFailsWith<IllegalArgumentException> { StrictJsonDuplicateKeyScanner(encoded).validate() }
    }
}
