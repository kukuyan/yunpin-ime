// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

import android.content.Context
import android.net.Uri

internal object ProfileSwitchBarrierProtocol {
    const val AUTHORITY_SUFFIX = ".ime-profile-barrier"
    const val BEGIN = "begin-profile-switch"
    const val FINISH = "finish-profile-switch"
    const val ACKNOWLEDGED = "acknowledged"
}

internal class ContentProviderProfileSwitchBarrier(context: Context) : ProfileSwitchBarrier {
    private val resolver = context.applicationContext.contentResolver
    private val uri = Uri.parse("content://${context.packageName}${ProfileSwitchBarrierProtocol.AUTHORITY_SUFFIX}")

    override fun begin(): Boolean = call(ProfileSwitchBarrierProtocol.BEGIN)

    override fun finish() {
        call(ProfileSwitchBarrierProtocol.FINISH)
    }

    private fun call(method: String): Boolean = try {
        resolver.call(uri, method, null, null)
            ?.getBoolean(ProfileSwitchBarrierProtocol.ACKNOWLEDGED, false) == true
    } catch (_: Exception) {
        false
    } catch (_: LinkageError) {
        false
    }
}
