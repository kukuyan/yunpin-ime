// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import java.io.Closeable

class NativeCandidateEngine : Closeable {
    private var handle: Long

    init {
        if (!libraryLoaded || nativeAbiVersion() != EXPECTED_ABI_VERSION) {
            throw IllegalStateException("Candidate core unavailable")
        }
        handle = nativeCreate()
        check(handle != 0L) { "Candidate core unavailable" }
    }

    @Synchronized
    fun loadSnapshot(bytes: ByteArray): Boolean =
        handle != 0L && bytes.isNotEmpty() && nativeLoadSnapshot(handle, bytes) == STATUS_OK

    @Synchronized
    fun query(input: String, limit: Int, contextFlags: Int): List<String> {
        if (handle == 0L || input.isEmpty() || limit <= 0) return emptyList()
        return nativeQueryUtf8(
            handle,
            input.toByteArray(Charsets.UTF_8),
            limit.coerceAtMost(8),
            contextFlags,
        )?.map { it.toString(Charsets.UTF_8) }.orEmpty()
    }

    @Synchronized
    override fun close() {
        if (handle != 0L) {
            nativeDestroy(handle)
            handle = 0
        }
    }

    private external fun nativeAbiVersion(): Int
    private external fun nativeCreate(): Long
    private external fun nativeDestroy(handle: Long)
    private external fun nativeLoadSnapshot(handle: Long, bytes: ByteArray): Int
    private external fun nativeQueryUtf8(handle: Long, input: ByteArray, limit: Int, contextFlags: Int): Array<ByteArray>?

    private companion object {
        const val EXPECTED_ABI_VERSION = 1
        const val STATUS_OK = 0
        val libraryLoaded = try {
            System.loadLibrary("yunpin_mobile_jni")
            true
        } catch (_: UnsatisfiedLinkError) {
            false
        }
    }
}
