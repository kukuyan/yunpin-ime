// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import android.content.Context
import io.github.kukuyan.yunpin.android.config.ServerProfile
import io.github.kukuyan.yunpin.android.config.ServerProfileStore
import io.github.kukuyan.yunpin.android.security.CredentialStore
import io.github.kukuyan.yunpin.android.security.CredentialUnavailableException
import io.github.kukuyan.yunpin.android.snapshot.SnapshotPaths
import io.github.kukuyan.yunpin.android.status.ControlPlaneGate
import io.github.kukuyan.yunpin.android.status.FailureCategory
import io.github.kukuyan.yunpin.android.status.RedactedStatus
import io.github.kukuyan.yunpin.android.status.StatusStore
import io.github.kukuyan.yunpin.android.status.SyncPhase
import io.github.kukuyan.yunpin.android.upgrade.LkgDigestLease
import io.github.kukuyan.yunpin.android.upgrade.RejectingLkgDigestLease
import java.util.concurrent.atomic.AtomicReference

data class CoordinatorResult(val retry: Boolean)
data class ScheduledCoreResult<T>(val result: T, val syncScheduled: Boolean)
data class LocalStateHealth(val healthy: Boolean, val snapshotPresent: Boolean)
data class UpgradeHealthCommitResult(val marked: Boolean, val enableDataPlane: Boolean)

class SyncCoordinator(
    context: Context,
    private val profiles: ServerProfileStore,
    private val credentials: CredentialStore,
    private val coreFactory: MobileSyncCoreFactory,
    private val statuses: StatusStore,
    private val dataPlaneGate: DataPlaneGate,
    private val rollbackVerifier: RollbackSnapshotVerifier = NativeRollbackSnapshotVerifier(context),
    private val rollbackLkgLease: LkgDigestLease = RejectingLkgDigestLease,
    private val upgradeSnapshotHealthReader: UpgradeSnapshotHealthReader = NativeUpgradeSnapshotHealthReader(context),
    private val upgradeHealthCommitter: UpgradeHealthCommitter = RejectingUpgradeHealthCommitter,
    private val clock: () -> Long = System::currentTimeMillis,
    private val scheduleImmediate: (Context) -> Boolean = { SyncScheduler.enqueueImmediate(it) },
) {
    private val appContext = context.applicationContext
    private val activeCore = AtomicReference<MobileSyncCoreSession?>(null)

    /** Safe for JobService.onStopJob while runBounded owns this coordinator's monitor. */
    fun cancelActive() {
        try {
            activeCore.get()?.cancelCurrentOperation()
        } catch (_: Exception) {
            // The interrupted worker still owns Close and redacted failure handling.
        } catch (_: LinkageError) {
            // A broken optional generated archive must not crash JobService.onStopJob.
        }
    }

    @Synchronized
    fun runBounded(): CoordinatorResult {
        val now = clock().coerceAtLeast(0)
        val profile = profiles.active()
        if (!dataPlaneGate.allows(profile?.id)) {
            val previous = statuses.read()
            statuses.write(
                previous.copy(
                    phase = SyncPhase.UPGRADE_HEALTH_REQUIRED,
                    failure = FailureCategory.NONE,
                    endpointConfigured = profile != null,
                    lastAttemptEpochMillis = now,
                ),
            )
            return CoordinatorResult(retry = false)
        }
        if (profile == null) {
            statuses.write(RedactedStatus(phase = SyncPhase.NOT_CONFIGURED, lastAttemptEpochMillis = now))
            return CoordinatorResult(retry = false)
        }
        val previous = statuses.read()
        val credential = loadCredential(profile.id, now) ?: return CoordinatorResult(false)
        statuses.write(
            previous.copy(
                phase = SyncPhase.RUNNING,
                failure = FailureCategory.NONE,
                endpointConfigured = true,
                credentialsPresent = true,
                lastAttemptEpochMillis = now,
            ),
        )
        try {
            coreFactory.open(openConfig(profile, credential)).use { core ->
                check(activeCore.compareAndSet(null, core)) { "mobile core operation already active" }
                try {
                    if (Thread.currentThread().isInterrupted) {
                        core.cancelCurrentOperation()
                        throw InterruptedException("background job stopped")
                    }
                    val report = core.sync(SYNC_TIMEOUT_MILLIS)
                    val state = core.status(STATUS_TIMEOUT_MILLIS)
                    val success = clock().coerceAtLeast(now)
                    statuses.write(
                        RedactedStatus(
                            phase = SyncPhase.SUCCEEDED,
                            endpointConfigured = true,
                            credentialsPresent = true,
                            cursor = state.cursor,
                            pendingEncryptedEnvelopes = state.pending.coerceAtMost(Int.MAX_VALUE.toLong()).toInt(),
                            snapshotGeneration = previous.snapshotGeneration + if (report.snapshotChanged) 1 else 0,
                            snapshotPresent = state.snapshotPresent,
                            rollbackAvailable = state.rollbackPresent,
                            controlPlaneGate = if (state.signedRosterChainRequired) {
                                ControlPlaneGate.SIGNED_ROSTER_CHAIN_REQUIRED
                            } else {
                                ControlPlaneGate.NONE
                            },
                            lastAttemptEpochMillis = now,
                            lastSuccessEpochMillis = success,
                        ),
                    )
                    return CoordinatorResult(retry = false)
                } finally {
                    activeCore.compareAndSet(core, null)
                }
            }
        } catch (_: InterruptedException) {
            Thread.currentThread().interrupt()
            return failed(previous, now, FailureCategory.NETWORK, SyncPhase.RETRY_SCHEDULED, true)
        } catch (_: MobileCoreUnavailableException) {
            return failed(previous, now, FailureCategory.PROTOCOL, SyncPhase.PROTOCOL_CORE_REQUIRED, false)
        } catch (error: MobileCoreBindingException) {
            val disposition = SyncFailurePolicy.forRedactedCode(error.redactedCode)
            return failed(
                previous,
                now,
                disposition.category,
                if (disposition.retry) SyncPhase.RETRY_SCHEDULED else SyncPhase.FAILED,
                disposition.retry,
            )
        } catch (_: Exception) {
            return failed(previous, now, FailureCategory.INTERNAL, SyncPhase.FAILED, false)
        } finally {
            credential.fill(0)
        }
    }

    @Synchronized
    fun rollbackSnapshot(expectedProfileId: String, expectedLkgDigest: String): Boolean {
        if (Thread.currentThread().isInterrupted) return false
        return try {
            profiles.withActiveSelection(expectedProfileId) {
                rollbackLkgLease.withCurrentLkgDigest(expectedProfileId, expectedLkgDigest) {
                    var restoredState: MobileCoreStatus? = null
                    val restored = SnapshotRollbackTransaction.run(
                        expectedProfileId = expectedProfileId,
                        expectedLkgDigest = expectedLkgDigest,
                        activeProfileId = { profiles.active()?.id },
                        rollbackFingerprint = { rollbackVerifier.rollbackFingerprint(expectedProfileId) },
                        restore = {
                            restoredState = restoreSnapshotThroughCore(expectedProfileId)?.takeIf { it.snapshotPresent }
                            restoredState != null
                        },
                        currentFingerprint = { rollbackVerifier.currentFingerprint(expectedProfileId) },
                    )
                    if (!restored) {
                        false
                    } else {
                        val state = restoredState ?: return@withCurrentLkgDigest false
                        val previous = statuses.read()
                        statuses.write(
                            previous.copy(
                                snapshotGeneration = if (previous.snapshotGeneration == Long.MAX_VALUE) {
                                    Long.MAX_VALUE
                                } else {
                                    previous.snapshotGeneration + 1
                                },
                                snapshotPresent = state.snapshotPresent,
                                rollbackAvailable = state.rollbackPresent,
                            ),
                        )
                        true
                    }
                } ?: false
            } ?: false
        } catch (_: Exception) {
            false
        } catch (_: LinkageError) {
            false
        }
    }

    /**
     * Containing-app-only mutation surface. The IME package has no coordinator
     * dependency and never calls any learning or outbox method.
     */
    @Synchronized
    fun recordSelection(
        text: String,
        pinyin: String,
        passwordField: Boolean = false,
        privateMode: Boolean = false,
        oneTimeInput: Boolean = false,
        noPersonalizedLearning: Boolean,
    ): ScheduledCoreResult<MobileCoreLearnResult> {
        val result = withActiveProfileCore { core ->
            core.recordSelection(
                text = text,
                pinyin = pinyin,
                passwordField = passwordField,
                privateMode = privateMode,
                oneTimeInput = oneTimeInput,
                noPersonalizedLearning = noPersonalizedLearning,
                timeoutMillis = LOCAL_OPERATION_TIMEOUT_MILLIS,
            )
        }
        val scheduled = result.recorded && result.syncEligible && scheduleImmediateSafely()
        return ScheduledCoreResult(result, scheduled)
    }

    @Synchronized
    fun saveExplicit(
        text: String,
        pinyin: String,
        useCount: Long,
        pinned: Boolean,
    ): ScheduledCoreResult<Unit> {
        val result = withActiveProfileCore { core ->
            core.saveExplicit(text, pinyin, useCount, pinned, LOCAL_OPERATION_TIMEOUT_MILLIS)
        }
        return ScheduledCoreResult(result, scheduleImmediateSafely())
    }

    @Synchronized
    fun delete(text: String, pinyin: String): ScheduledCoreResult<Unit> {
        val result = withActiveProfileCore { core ->
            core.delete(text, pinyin, LOCAL_OPERATION_TIMEOUT_MILLIS)
        }
        return ScheduledCoreResult(result, scheduleImmediateSafely())
    }

    @Synchronized
    fun publishSnapshot(): ScheduledCoreResult<MobileCoreSnapshotReport> {
        val report = withActiveProfileCore { core ->
            core.publishSnapshot(SNAPSHOT_OPERATION_TIMEOUT_MILLIS)
        }
        val previous = statuses.read()
        statuses.write(
            previous.copy(
                snapshotGeneration = report.generation,
                snapshotPresent = true,
                rollbackAvailable = report.rollbackAvailable,
            ),
        )
        return ScheduledCoreResult(report, scheduleImmediateSafely())
    }

    /**
     * Performs a bounded, network-free upgrade health gate. Configured state
     * must open through the shared core and read its redacted local status.
     * Candidate ABI/snapshot validation is a separate foreground upgrade gate.
     */
    @Synchronized
    fun localStateHealth(expectedProfileId: String?): LocalStateHealth {
        return readLocalStateHealth(expectedProfileId)
    }

    /**
     * Freezes every containing-app data-plane operation while status, the exact
     * current fingerprint, and the journal commit are bound under one profile
     * lease. No caller-provided or earlier fingerprint can be committed.
     */
    @Synchronized
    fun verifyAndMarkUpgradeHealthy(versionCode: Long, expectedProfileId: String?): UpgradeHealthCommitResult {
        if (versionCode <= 0 || Thread.currentThread().isInterrupted) {
            return UpgradeHealthCommitResult(false, false)
        }
        return profiles.withActiveSelection(expectedProfileId) {
            val wasBlocked = !dataPlaneGate.allows(expectedProfileId)
            val marked = ExactCurrentUpgradeHealthTransaction.run(
                readLocalSnapshotPresent = {
                    readLocalStateHealth(expectedProfileId).takeIf { it.healthy }?.snapshotPresent
                },
                readExactCurrent = {
                    upgradeSnapshotHealthReader.inspect(expectedProfileId)?.let {
                        ExactSnapshotFingerprint(it.present, it.sha256Hex)
                    }
                },
                commit = { digest ->
                    upgradeHealthCommitter.markHealthy(versionCode, expectedProfileId, digest)
                },
            )
            UpgradeHealthCommitResult(
                marked = marked,
                enableDataPlane = marked && wasBlocked && dataPlaneGate.allows(expectedProfileId),
            )
        } ?: UpgradeHealthCommitResult(false, false)
    }

    private fun readLocalStateHealth(expectedProfileId: String?): LocalStateHealth {
        if (Thread.currentThread().isInterrupted) return LocalStateHealth(false, false)
        val profile = profiles.active()
        if (profile?.id != expectedProfileId) return LocalStateHealth(false, false)
        if (profile == null) return LocalStateHealth(true, false)
        if (!credentials.contains(profile.id)) return LocalStateHealth(false, false)
        val credential = try {
            credentials.load(profile.id)
        } catch (_: CredentialUnavailableException) {
            null
        } ?: return LocalStateHealth(false, false)
        return try {
            val state = coreFactory.open(
                openConfig(profile, credential),
            ).use { core ->
                core.status(STATUS_TIMEOUT_MILLIS)
            }
            LocalStateHealth(
                healthy = !Thread.currentThread().isInterrupted && profiles.active()?.id == expectedProfileId,
                snapshotPresent = state.snapshotPresent,
            )
        } catch (_: Exception) {
            LocalStateHealth(false, false)
        } catch (_: LinkageError) {
            LocalStateHealth(false, false)
        } finally {
            credential.fill(0)
        }
    }

    private fun openConfig(profile: ServerProfile, credential: ByteArray) =
        MobileCoreOpenConfig(
            databasePath = SnapshotPaths.database(appContext, profile.id).absolutePath,
            snapshotPath = SnapshotPaths.current(appContext, profile.id).absolutePath,
            endpoint = profile.endpoint.normalizedEndpoint,
            allowPrivateHttp = profile.endpoint.allowPrivateHttp,
            opaqueCredential = credential,
        )

    private inline fun <T> withActiveProfileCore(block: (MobileSyncCoreSession) -> T): T {
        if (Thread.currentThread().isInterrupted) throw MobileCoreBindingException("cancelled")
        val profile = profiles.active() ?: throw MobileCoreBindingException("authorization_required")
        if (!dataPlaneGate.allows(profile.id)) throw MobileCoreBindingException("local_state_error")
        if (!credentials.contains(profile.id)) throw MobileCoreBindingException("authorization_required")
        val credential = try {
            credentials.load(profile.id)
        } catch (_: CredentialUnavailableException) {
            null
        } ?: throw MobileCoreBindingException("local_state_error")
        return try {
            if (Thread.currentThread().isInterrupted) throw MobileCoreBindingException("cancelled")
            coreFactory.open(openConfig(profile, credential)).use { core ->
                if (Thread.currentThread().isInterrupted) {
                    core.cancelCurrentOperation()
                    throw MobileCoreBindingException("cancelled")
                }
                block(core)
            }
        } catch (error: MobileCoreUnavailableException) {
            throw error
        } catch (error: MobileCoreBindingException) {
            throw error
        } catch (_: Exception) {
            throw MobileCoreBindingException("local_state_error")
        } catch (_: LinkageError) {
            throw MobileCoreBindingException("local_state_error")
        } finally {
            credential.fill(0)
        }
    }

    private fun scheduleImmediateSafely(): Boolean = try {
        scheduleImmediate(appContext)
    } catch (_: Exception) {
        false
    } catch (_: LinkageError) {
        false
    }

    private fun restoreSnapshotThroughCore(expectedProfileId: String): MobileCoreStatus? {
        if (Thread.currentThread().isInterrupted || profiles.active()?.id != expectedProfileId) return null
        val profile = profiles.active()?.takeIf { it.id == expectedProfileId } ?: return null
        val credential = try {
            credentials.load(profile.id)
        } catch (_: CredentialUnavailableException) {
            null
        } ?: return null
        return try {
            coreFactory.open(openConfig(profile, credential)).use { core ->
                if (Thread.currentThread().isInterrupted || profiles.active()?.id != expectedProfileId) return null
                core.rollbackSnapshot()
                if (Thread.currentThread().isInterrupted || profiles.active()?.id != expectedProfileId) return null
                core.status(STATUS_TIMEOUT_MILLIS)
            }
        } catch (_: Exception) {
            null
        } catch (_: LinkageError) {
            null
        } finally {
            credential.fill(0)
        }
    }

    private fun loadCredential(profileId: String, now: Long): ByteArray? {
        if (!credentials.contains(profileId)) {
            statuses.write(
                RedactedStatus(
                    phase = SyncPhase.CREDENTIALS_REQUIRED,
                    endpointConfigured = true,
                    lastAttemptEpochMillis = now,
                ),
            )
            return null
        }
        return try {
            credentials.load(profileId)
        } catch (_: CredentialUnavailableException) {
            statuses.write(
                RedactedStatus(
                    phase = SyncPhase.FAILED,
                    failure = FailureCategory.KEYSTORE,
                    endpointConfigured = true,
                    credentialsPresent = true,
                    lastAttemptEpochMillis = now,
                ),
            )
            null
        }
    }

    private fun failed(
        previous: RedactedStatus,
        now: Long,
        failure: FailureCategory,
        phase: SyncPhase,
        retry: Boolean,
    ): CoordinatorResult {
        statuses.write(
            previous.copy(
                phase = phase,
                failure = failure,
                endpointConfigured = true,
                credentialsPresent = true,
                lastAttemptEpochMillis = now,
            ),
        )
        return CoordinatorResult(retry = retry)
    }

    private companion object {
        const val SYNC_TIMEOUT_MILLIS = 60_000L
        const val STATUS_TIMEOUT_MILLIS = 5_000L
        const val LOCAL_OPERATION_TIMEOUT_MILLIS = 5_000L
        const val SNAPSHOT_OPERATION_TIMEOUT_MILLIS = 10_000L
    }
}
