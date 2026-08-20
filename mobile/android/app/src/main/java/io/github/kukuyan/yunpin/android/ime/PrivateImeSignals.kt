// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

data class PrivateImeSignals(
    val privateMode: Boolean,
    val oneTimeInput: Boolean,
)

/** Explicit, namespaced signals cooperating editors may place in privateImeOptions. */
object PrivateImeSignalParser {
    const val PRIVATE_MODE = "io.github.kukuyan.yunpin.private_mode"
    const val ONE_TIME_INPUT = "io.github.kukuyan.yunpin.one_time_input"

    fun parse(raw: String?): PrivateImeSignals {
        val tokens = raw.orEmpty()
            .split(',', ';', ' ', '\t', '\r', '\n')
            .filterTo(mutableSetOf()) { it.isNotEmpty() }
        return PrivateImeSignals(
            privateMode = PRIVATE_MODE in tokens,
            oneTimeInput = ONE_TIME_INPUT in tokens,
        )
    }
}
