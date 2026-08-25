// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

import android.content.Context
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.util.UUID
import kotlin.test.assertEquals
import kotlin.test.assertNull
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class ServerProfileStoreInstrumentedTest {
    @Test
    fun atomicPointerIsTheOnlyRuntimeActiveProfileAuthority() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val store = ServerProfileStore(context)
        val first = store.save(null, "Synthetic pointer one", "https://pointer-one.example.test", false)
        val second = store.save(null, "Synthetic pointer two", "https://pointer-two.example.test", false)
        try {
            store.select(first.id)
            context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
                .edit()
                .putString(ACTIVE_PROFILE, second.id)
                .commit()

            assertEquals(first.id, store.active()?.id)

            ActiveProfilePointer(context).write(UUID.randomUUID().toString())
            assertNull(store.active())
        } finally {
            ActiveProfilePointer(context).write(first.id)
        }
    }

    private companion object {
        const val PREFERENCES = "yunpin_server_profiles_v1"
        const val ACTIVE_PROFILE = "active_profile"
    }
}
