// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android

import android.app.Activity
import android.os.Bundle
import android.text.InputType
import android.view.ViewGroup
import android.widget.ArrayAdapter
import android.widget.Button
import android.widget.CheckBox
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Spinner
import android.widget.TextView
import android.widget.Toast
import io.github.kukuyan.yunpin.android.config.ServerProfile
import io.github.kukuyan.yunpin.android.sync.SyncScheduler
import java.util.concurrent.Executors
import java.util.concurrent.Future
import java.util.concurrent.RejectedExecutionException

class MainActivity : Activity() {
    private lateinit var profileSpinner: Spinner
    private lateinit var profileName: EditText
    private lateinit var endpoint: EditText
    private lateinit var allowPrivateHttp: CheckBox
    private lateinit var status: TextView
    private var profiles: List<ServerProfile> = emptyList()
    private val appExecutor = Executors.newSingleThreadExecutor()
    private var healthTask: Future<*>? = null
    private var rollbackTask: Future<*>? = null
    @Volatile private var destroyed = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        title = getString(R.string.app_name)
        setContentView(buildContent())
        reloadProfiles()
        refreshStatus()
    }

    override fun onResume() {
        super.onResume()
        refreshStatus()
    }

    override fun onPostResume() {
        super.onPostResume()
        scheduleUpgradeHealthCheck()
    }

    override fun onDestroy() {
        destroyed = true
        healthTask?.cancel(true)
        rollbackTask?.cancel(true)
        appExecutor.shutdownNow()
        super.onDestroy()
    }

    private fun buildContent(): ScrollView {
        val padding = (20 * resources.displayMetrics.density).toInt()
        val column = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, padding, padding, padding)
        }
        column.addView(TextView(this).apply {
            text = "YunPin Android preview"
            textSize = 24f
        })
        column.addView(TextView(this).apply {
            text = "The app owns server selection, protected credentials, background sync, and atomic snapshots. The keyboard remains read-only and network-free."
        })
        column.addView(TextView(this).apply {
            text = "Device enrollment remains unavailable until the signed roster-chain migration is implemented and approved."
        })

        profileSpinner = Spinner(this)
        column.addView(profileSpinner, matchWidth())
        profileName = EditText(this).apply {
            hint = "Server profile name"
            inputType = InputType.TYPE_CLASS_TEXT
        }
        endpoint = EditText(this).apply {
            hint = "Absolute HTTPS endpoint"
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
        }
        allowPrivateHttp = CheckBox(this).apply {
            text = "Allow plaintext HTTP only for localhost/private IP literal"
        }
        column.addView(profileName, matchWidth())
        column.addView(endpoint, matchWidth())
        column.addView(allowPrivateHttp, matchWidth())

        column.addView(Button(this).apply {
            text = "Save and select server"
            setOnClickListener { saveProfile() }
        }, matchWidth())
        column.addView(Button(this).apply {
            text = "New server profile"
            setOnClickListener {
                profileSpinner.setSelection(0)
                profileName.text.clear()
                endpoint.text.clear()
                allowPrivateHttp.isChecked = false
            }
        }, matchWidth())
        column.addView(Button(this).apply {
            text = "Sync now"
            setOnClickListener {
                val graph = appGraph()
                val profileId = graph.profiles.active()?.id
                val scheduled = graph.upgrades.dataPlaneAllowed(
                    BuildConfig.VERSION_CODE.toLong(),
                    profileId,
                ) && SyncScheduler.enqueueImmediate(this@MainActivity)
                Toast.makeText(this@MainActivity, if (scheduled) "Sync scheduled" else "Unable to schedule sync", Toast.LENGTH_SHORT).show()
                refreshStatus()
            }
        }, matchWidth())
        column.addView(Button(this).apply {
            text = "Roll back candidate snapshot"
            setOnClickListener { scheduleSnapshotRollback(this) }
        }, matchWidth())

        status = TextView(this).apply {
            setPadding(0, padding, 0, 0)
            setTextIsSelectable(true)
        }
        column.addView(status, matchWidth())
        return ScrollView(this).apply { addView(column) }
    }

    private fun reloadProfiles(selectId: String? = appGraph().profiles.active()?.id) {
        profiles = appGraph().profiles.all()
        val labels = listOf("Select a server profile") + profiles.map { it.displayName }
        profileSpinner.adapter = ArrayAdapter(this, android.R.layout.simple_spinner_dropdown_item, labels)
        val selected = profiles.indexOfFirst { it.id == selectId }
        profileSpinner.setSelection(if (selected >= 0) selected + 1 else 0)
        if (selected >= 0) fillProfile(profiles[selected])
        profileSpinner.setOnItemSelectedListener(SimpleItemSelectedListener { position ->
            if (position > 0) {
                val profile = profiles[position - 1]
                if (appGraph().profiles.active()?.id == profile.id) {
                    fillProfile(profile)
                } else if (appGraph().profiles.select(profile.id)) {
                    fillProfile(profile)
                    refreshStatus()
                    restartUpgradeHealthCheck()
                } else {
                    Toast.makeText(this, PROFILE_ACTIVATION_FAILED, Toast.LENGTH_LONG).show()
                    reloadProfiles()
                    refreshStatus()
                }
            }
        })
    }

    private fun fillProfile(profile: ServerProfile) {
        profileName.setText(profile.displayName)
        endpoint.setText(profile.endpoint.normalizedEndpoint)
        allowPrivateHttp.isChecked = profile.endpoint.allowPrivateHttp
    }

    private fun saveProfile() {
        val existingId = profileSpinner.selectedItemPosition.takeIf { it > 0 }?.let { profiles[it - 1].id }
        try {
            appGraph().profiles.save(
                existingId = existingId,
                displayName = profileName.text.toString(),
                rawEndpoint = endpoint.text.toString(),
                allowPrivateHttp = allowPrivateHttp.isChecked,
            )
            reloadProfiles()
            refreshStatus()
            restartUpgradeHealthCheck()
        } catch (error: IllegalArgumentException) {
            Toast.makeText(this, error.message ?: "Invalid server profile", Toast.LENGTH_LONG).show()
            reloadProfiles()
        } catch (_: Exception) {
            Toast.makeText(this, PROFILE_ACTIVATION_FAILED, Toast.LENGTH_LONG).show()
            reloadProfiles()
            refreshStatus()
        }
    }

    private fun scheduleSnapshotRollback(button: Button) {
        if (destroyed || rollbackTask?.isDone == false) return
        val graph = appGraph()
        val profileId = graph.profiles.active()?.id
        val expectedLkgDigest = profileId?.let(graph.upgrades::lastKnownGoodSnapshotDigest)
        if (profileId == null || expectedLkgDigest == null) {
            Toast.makeText(this, "Snapshot rollback unavailable", Toast.LENGTH_SHORT).show()
            return
        }
        button.isEnabled = false
        try {
            rollbackTask = appExecutor.submit {
                val restored = if (Thread.currentThread().isInterrupted) {
                    false
                } else {
                    graph.coordinator.rollbackSnapshot(profileId, expectedLkgDigest)
                }
                if (destroyed || Thread.currentThread().isInterrupted) return@submit
                runOnUiThread {
                    if (destroyed) return@runOnUiThread
                    button.isEnabled = true
                    Toast.makeText(
                        this,
                        if (restored) "Previous snapshot restored" else "Snapshot rollback unavailable",
                        Toast.LENGTH_SHORT,
                    ).show()
                    refreshStatus()
                    if (restored) scheduleUpgradeHealthCheck()
                }
            }
        } catch (_: RejectedExecutionException) {
            button.isEnabled = true
        }
    }

    private fun refreshStatus() {
        val graph = appGraph()
        val upgrade = graph.upgrades.read(graph.profiles.active()?.id)
        status.text = buildString {
            appendLine(graph.statuses.read().render())
            append("Upgrade rollback suggested: ${upgrade.rollbackSuggested}")
        }
    }

    private fun scheduleUpgradeHealthCheck() {
        if (destroyed || healthTask?.isDone == false) return
        val graph = appGraph()
        val profileId = graph.profiles.active()?.id
        val version = BuildConfig.VERSION_CODE.toLong()
        try {
            healthTask = appExecutor.submit {
                if (destroyed || Thread.currentThread().isInterrupted) return@submit
                val result = graph.coordinator.verifyAndMarkUpgradeHealthy(version, profileId)
                if (!result.marked || destroyed || Thread.currentThread().isInterrupted) return@submit
                if (result.enableDataPlane) {
                    SyncScheduler.ensurePeriodic(applicationContext)
                    SyncScheduler.enqueueImmediate(applicationContext)
                }
                runOnUiThread {
                    if (!destroyed) refreshStatus()
                }
            }
        } catch (_: RejectedExecutionException) {
            // Preserve healthPending; a later foreground launch may retry.
        }
    }

    private fun restartUpgradeHealthCheck() {
        healthTask?.cancel(true)
        healthTask = null
        val graph = appGraph()
        graph.coordinator.cancelActive()
        SyncScheduler.cancelDataPlane(this)
        try {
            val version = BuildConfig.VERSION_CODE.toLong()
            val profileId = graph.profiles.active()?.id
            graph.upgrades.recordLaunch(version, profileId)
            if (graph.upgrades.dataPlaneAllowed(version, profileId)) {
                SyncScheduler.ensurePeriodic(this)
                SyncScheduler.enqueueImmediate(this)
            }
        } catch (_: Exception) {
            return
        }
        scheduleUpgradeHealthCheck()
    }

    private fun matchWidth() = LinearLayout.LayoutParams(
        ViewGroup.LayoutParams.MATCH_PARENT,
        ViewGroup.LayoutParams.WRAP_CONTENT,
    )

    private companion object {
        const val PROFILE_ACTIVATION_FAILED = "Unable to activate the selected server profile"
    }
}
