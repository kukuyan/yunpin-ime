// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.snapshot

import android.content.Context
import java.io.File

/** Read-only entry point used by the isolated IME process. */
object SnapshotReader {
    fun readCurrent(context: Context): ByteArray? =
        SnapshotPaths.activeCurrent(context)?.let(SnapshotFormat::readBounded)

    fun read(file: File): ByteArray? = SnapshotFormat.readBounded(file)
}
