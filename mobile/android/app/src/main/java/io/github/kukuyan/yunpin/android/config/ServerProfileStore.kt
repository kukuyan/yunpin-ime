// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.config

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject
import java.util.UUID

data class ServerProfile(
    val id: String,
    val displayName: String,
    val endpoint: EndpointConfig,
)

/** Non-secret server choices. Credentials are deliberately stored elsewhere. */
class ServerProfileStore(context: Context) {
    private val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
    private val activePointer = ActiveProfilePointer(context)
    private val profileSwitchBarrier: ProfileSwitchBarrier = ContentProviderProfileSwitchBarrier(context)

    @Synchronized
    fun all(): List<ServerProfile> {
        val encoded = preferences.getString(PROFILES, null) ?: return emptyList()
        return try {
            val array = JSONArray(encoded)
            buildList {
                for (index in 0 until array.length()) {
                    val item = array.getJSONObject(index)
                    val id = item.getString("id")
                    val displayName = item.getString("display_name")
                    val allowPrivateHttp = item.getBoolean("allow_private_http")
                    val endpoint = EndpointPolicy.parse(item.getString("endpoint"), allowPrivateHttp)
                    if (ProfileIdentity.canonical(id) == id && displayName.isNotBlank()) {
                        add(ServerProfile(id, displayName, endpoint))
                    }
                }
            }
        } catch (_: Exception) {
            emptyList()
        }
    }

    @Synchronized
    fun active(): ServerProfile? {
        // This AtomicFile is also the only selector visible to the IME process.
        // The preference is retained as non-authoritative metadata so a failed
        // pointer write can never split the app and keyboard data planes.
        val activeId = activePointer.read() ?: return null
        return all().firstOrNull { it.id == activeId }
    }

    /** Holds the selection lock across a final lease check and small commit. */
    @Synchronized
    fun <T> withActiveSelection(expectedProfileId: String?, block: () -> T): T? {
        if (expectedProfileId != null && ProfileIdentity.canonical(expectedProfileId) != expectedProfileId) return null
        if (active()?.id != expectedProfileId) return null
        return block()
    }

    @Synchronized
    fun save(
        existingId: String?,
        displayName: String,
        rawEndpoint: String,
        allowPrivateHttp: Boolean,
    ): ServerProfile {
        val safeName = displayName.trim()
        require(safeName.isNotEmpty() && safeName.length <= 80) { "A short profile name is required" }
        val profiles = all().toMutableList()
        val existing = existingId?.let { id ->
            profiles.firstOrNull { it.id == id }
                ?: throw IllegalArgumentException("The selected server profile no longer exists")
        }
        val requestedEndpoint = EndpointPolicy.parse(rawEndpoint, allowPrivateHttp)
        ServerProfileMutationPolicy.requireEndpointUnchanged(existing?.endpoint, requestedEndpoint)
        val profile = ServerProfile(
            id = existing?.id ?: UUID.randomUUID().toString(),
            displayName = safeName,
            endpoint = requestedEndpoint,
        )
        val position = profiles.indexOfFirst { it.id == profile.id }
        if (position >= 0) profiles[position] = profile else profiles.add(profile)
        persist(profiles, profile.id)
        return profile
    }

    @Synchronized
    fun select(id: String): Boolean {
        if (all().none { it.id == id }) return false
        if (!preferences.edit().putString(ACTIVE_PROFILE, id).commit()) return false
        return try {
            withProfileSwitchBarrier(profileSwitchBarrier) { activePointer.write(id) }
            true
        } catch (_: Exception) {
            false
        }
    }

    private fun persist(profiles: List<ServerProfile>, activeId: String) {
        val array = JSONArray()
        profiles.forEach { profile ->
            array.put(
                JSONObject()
                    .put("id", profile.id)
                    .put("display_name", profile.displayName)
                    .put("endpoint", profile.endpoint.normalizedEndpoint)
                    .put("allow_private_http", profile.endpoint.allowPrivateHttp),
            )
        }
        check(
            preferences.edit()
                .putString(PROFILES, array.toString())
                .putString(ACTIVE_PROFILE, activeId)
                .commit(),
        ) { "Unable to persist the selected sync server" }
        try {
            withProfileSwitchBarrier(profileSwitchBarrier) { activePointer.write(activeId) }
        } catch (_: Exception) {
            throw IllegalStateException("Unable to activate the selected sync server")
        }
    }

    private companion object {
        const val PREFERENCES = "yunpin_server_profiles_v1"
        const val PROFILES = "profiles"
        const val ACTIVE_PROFILE = "active_profile"
    }
}
