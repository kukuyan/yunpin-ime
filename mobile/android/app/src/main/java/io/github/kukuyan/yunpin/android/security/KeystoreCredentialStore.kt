// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.security

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Wraps an opaque protocol credential bundle with a non-exportable Android
 * Keystore AES-GCM key. This class never creates or interprets a recovery key.
 */
class KeystoreCredentialStore(context: Context) : CredentialStore {
    private val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)

    override fun contains(profileId: String): Boolean {
        val canonicalId = ProfileCredentialIdentity.canonical(profileId) ?: return false
        return preferences.contains(preferenceKey(canonicalId))
    }

    @Synchronized
    override fun store(profileId: String, opaqueCredential: ByteArray) {
        val canonicalId = requireNotNull(ProfileCredentialIdentity.canonical(profileId))
        require(opaqueCredential.isNotEmpty() && opaqueCredential.size <= MAX_CREDENTIAL_BYTES)
        try {
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.ENCRYPT_MODE, wrappingKey())
            cipher.updateAAD(aad(canonicalId))
            val ciphertext = cipher.doFinal(opaqueCredential)
            val envelope = encodeEnvelope(cipher.iv, ciphertext)
            check(
                preferences.edit()
                    .putString(preferenceKey(canonicalId), Base64.encodeToString(envelope, Base64.NO_WRAP))
                    .commit(),
            )
        } catch (_: Exception) {
            throw CredentialUnavailableException()
        }
    }

    @Synchronized
    override fun load(profileId: String): ByteArray? {
        val canonicalId = ProfileCredentialIdentity.canonical(profileId) ?: return null
        val encoded = preferences.getString(preferenceKey(canonicalId), null) ?: return null
        return try {
            val (iv, ciphertext) = decodeEnvelope(Base64.decode(encoded, Base64.NO_WRAP))
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, wrappingKey(), GCMParameterSpec(TAG_BITS, iv))
            cipher.updateAAD(aad(canonicalId))
            cipher.doFinal(ciphertext).also {
                if (it.isEmpty() || it.size > MAX_CREDENTIAL_BYTES) throw CredentialUnavailableException()
            }
        } catch (_: CredentialUnavailableException) {
            throw CredentialUnavailableException()
        } catch (_: Exception) {
            throw CredentialUnavailableException()
        }
    }

    private fun wrappingKey(): SecretKey {
        val keyStore = KeyStore.getInstance(KEYSTORE_PROVIDER).apply { load(null) }
        (keyStore.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE_PROVIDER)
        generator.init(
            KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .setUserAuthenticationRequired(false)
                .build(),
        )
        return generator.generateKey()
    }

    private fun encodeEnvelope(iv: ByteArray, ciphertext: ByteArray): ByteArray {
        require(iv.size in 12..32 && ciphertext.size <= MAX_CREDENTIAL_BYTES + 32)
        return ByteArrayOutputStream().use { output ->
            DataOutputStream(output).use { data ->
                data.write(MAGIC)
                data.writeByte(iv.size)
                data.writeInt(ciphertext.size)
                data.write(iv)
                data.write(ciphertext)
            }
            output.toByteArray()
        }
    }

    private fun decodeEnvelope(envelope: ByteArray): Pair<ByteArray, ByteArray> {
        if (envelope.size > MAX_CREDENTIAL_BYTES + 64) throw CredentialUnavailableException()
        DataInputStream(ByteArrayInputStream(envelope)).use { data ->
            val magic = ByteArray(MAGIC.size).also { data.readFully(it) }
            if (!magic.contentEquals(MAGIC)) throw CredentialUnavailableException()
            val ivSize = data.readUnsignedByte()
            val ciphertextSize = data.readInt()
            if (ivSize !in 12..32 || ciphertextSize !in 17..MAX_CREDENTIAL_BYTES + 32) {
                throw CredentialUnavailableException()
            }
            if (envelope.size != MAGIC.size + 1 + 4 + ivSize + ciphertextSize) {
                throw CredentialUnavailableException()
            }
            val iv = ByteArray(ivSize).also { data.readFully(it) }
            val ciphertext = ByteArray(ciphertextSize).also { data.readFully(it) }
            return iv to ciphertext
        }
    }

    private fun preferenceKey(profileId: String) = "profile.$profileId"
    private fun aad(profileId: String) = AAD_PREFIX + ProfileCredentialIdentity.aadBytes(profileId)

    private companion object {
        const val PREFERENCES = "yunpin_protected_credentials_v1"
        const val KEYSTORE_PROVIDER = "AndroidKeyStore"
        const val KEY_ALIAS = "YunPin.Mobile.CredentialWrap.v1"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
        const val TAG_BITS = 128
        const val MAX_CREDENTIAL_BYTES = 1024 * 1024
        val MAGIC = byteArrayOf('Y'.code.toByte(), 'P'.code.toByte(), 'C'.code.toByte(), 1)
        val AAD_PREFIX = "io.github.kukuyan.yunpin.android/credential/v1\u0000".toByteArray(Charsets.UTF_8)
    }
}
