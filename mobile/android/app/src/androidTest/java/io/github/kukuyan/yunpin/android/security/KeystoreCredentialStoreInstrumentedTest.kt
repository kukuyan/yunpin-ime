// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.security

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import android.util.Base64
import java.security.KeyStore
import java.util.UUID
import kotlin.test.assertContentEquals
import kotlin.test.assertNull
import kotlin.test.assertNotEquals
import kotlin.test.assertFailsWith
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class KeystoreCredentialStoreInstrumentedTest {
    @Test
    fun wrapsOpaqueSyntheticCredentialAndRejectsCrossProfileCopy() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val store = KeystoreCredentialStore(context)
        val profile = UUID.randomUUID().toString()
        val otherProfile = UUID.randomUUID().toString()
        val synthetic = ByteArray(96) { index -> ((index * 17 + 3) and 0xff).toByte() }
        store.store(profile, synthetic)
        assertContentEquals(synthetic, store.load(profile))
        val preferences = context.getSharedPreferences("yunpin_protected_credentials_v1", 0)
        val wrapped = preferences.getString("profile.$profile", null)
        assertNotEquals(Base64.encodeToString(synthetic, Base64.NO_WRAP), wrapped)
        preferences.edit().putString("profile.$otherProfile", wrapped).commit()
        assertFailsWith<CredentialUnavailableException> { store.load(otherProfile) }
        val keyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        assertNull(keyStore.getKey("YunPin.Mobile.CredentialWrap.v1", null).encoded)
    }
}
