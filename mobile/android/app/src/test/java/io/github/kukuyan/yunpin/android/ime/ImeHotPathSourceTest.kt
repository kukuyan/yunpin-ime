// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import java.io.File
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test

class ImeHotPathSourceTest {
    @Test
    fun candidateHotPathsContainNoFilesystemReads() {
        val source = locateSource().readText()
        listOf("commitSpace", "commitCandidate", "updateCandidates").forEach { method ->
            val body = functionBody(source, method)
            assertFalse(body.contains("SnapshotPaths"), "$method must not read the active-profile file")
            assertFalse(body.contains("SnapshotReader"), "$method must not read snapshot bytes")
            assertFalse(body.contains("java.io"), "$method must not perform filesystem I/O")
            assertTrue(body.contains("engines."), "$method must consult only in-memory engine state")
        }
    }

    private fun functionBody(source: String, name: String): String {
        val start = source.indexOf("private fun $name")
        require(start >= 0)
        var depth = 0
        var opened = false
        for (index in start until source.length) {
            when (source[index]) {
                '{' -> {
                    opened = true
                    depth += 1
                }
                '}' -> if (opened && --depth == 0) return source.substring(start, index + 1)
            }
        }
        throw AssertionError("$name body was not closed")
    }

    private fun locateSource(): File {
        var directory = File(System.getProperty("user.dir")).canonicalFile
        repeat(8) {
            listOf(
                File(directory, "app/src/main/java/io/github/kukuyan/yunpin/android/ime/YunPinInputMethodService.kt"),
                File(directory, "mobile/android/app/src/main/java/io/github/kukuyan/yunpin/android/ime/YunPinInputMethodService.kt"),
            ).firstOrNull(File::isFile)?.let { return it }
            directory = directory.parentFile ?: return@repeat
        }
        throw AssertionError("IME source file was not found")
    }
}
