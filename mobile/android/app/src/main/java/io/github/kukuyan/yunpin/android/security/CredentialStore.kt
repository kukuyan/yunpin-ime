// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.security

interface CredentialStore {
    fun contains(profileId: String): Boolean
    fun store(profileId: String, opaqueCredential: ByteArray)
    fun load(profileId: String): ByteArray?
}

class CredentialUnavailableException : IllegalStateException("Protected credentials are unavailable")
