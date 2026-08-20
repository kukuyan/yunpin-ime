// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

import android.content.Context
import android.util.AtomicFile
import io.github.kukuyan.yunpin.android.snapshot.SnapshotPaths
import java.io.File

/** Cross-process, atomic pointer containing one canonical profile UUID and no endpoint. */
internal class ActiveProfilePointer(context: Context) {
    private val file = AtomicFile(File(SnapshotPaths.activePointerDirectory(context), SnapshotPaths.ACTIVE_POINTER_NAME))

    @Synchronized
    fun write(profileId: String) {
        val canonical = requireNotNull(ProfileIdentity.canonical(profileId))
        file.baseFile.parentFile?.mkdirs()
        val output = file.startWrite()
        try {
            output.write("$canonical\n".toByteArray(Charsets.US_ASCII))
            file.finishWrite(output)
        } catch (error: Exception) {
            file.failWrite(output)
            throw error
        }
    }

    fun read(): String? {
        val bytes = try {
            file.readFully()
        } catch (_: Exception) {
            return null
        }
        if (bytes.size !in 37..40) return null
        return ProfileIdentity.canonical(bytes.toString(Charsets.US_ASCII).trim())
    }
}
