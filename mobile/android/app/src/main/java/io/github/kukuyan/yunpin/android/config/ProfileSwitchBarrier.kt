// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

internal interface ProfileSwitchBarrier {
    fun begin(): Boolean
    fun finish()
}

internal class ProfileSwitchBarrierUnavailable : IllegalStateException()

internal fun <T> withProfileSwitchBarrier(barrier: ProfileSwitchBarrier, action: () -> T): T {
    if (!barrier.begin()) throw ProfileSwitchBarrierUnavailable()
    return try {
        action()
    } finally {
        barrier.finish()
    }
}
