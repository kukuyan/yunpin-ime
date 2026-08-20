// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.upgrade

interface LkgDigestLease {
    fun <T> withCurrentLkgDigest(profileId: String, expectedDigest: String, block: () -> T): T?
}

object RejectingLkgDigestLease : LkgDigestLease {
    override fun <T> withCurrentLkgDigest(profileId: String, expectedDigest: String, block: () -> T): T? = null
}
