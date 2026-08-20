// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.snapshot

import java.io.File
import java.io.FileInputStream

object SnapshotFormat {
    const val MAX_BYTES: Long = 64L * 1024L * 1024L
    private val acceptedHeaders = setOf(
        "phrase\tpinyin\tsource\tuse_count",
        "phrase\tpinyin\tsource\tuse_count\tpinned",
        "phrase\tpinyin\tsource\tuse_count\tpinned\tlast_used_day",
    )

    fun validate(bytes: ByteArray): Boolean {
        if (bytes.isEmpty() || bytes.size.toLong() > MAX_BYTES || bytes.any { it == 0.toByte() }) return false
        val newline = bytes.indexOf('\n'.code.toByte()).let { if (it < 0) bytes.size else it }
        if (newline > 128) return false
        val rawHeader = bytes.copyOfRange(0, newline).toString(Charsets.UTF_8).removeSuffix("\r")
        return rawHeader in acceptedHeaders
    }

    fun readBounded(file: File): ByteArray? {
        if (!file.isFile || file.length() !in 1L..MAX_BYTES) return null
        return FileInputStream(file).use { input ->
            val result = ByteArray(file.length().toInt())
            var offset = 0
            while (offset < result.size) {
                if (Thread.currentThread().isInterrupted) return null
                val count = input.read(result, offset, result.size - offset)
                if (count < 0) return null
                offset += count
            }
            if (input.read() != -1 || !validate(result)) null else result
        }
    }
}
