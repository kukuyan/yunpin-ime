// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import io.github.kukuyan.yunpin.android.config.ServerProfileStore
import io.github.kukuyan.yunpin.android.security.KeystoreCredentialStore
import io.github.kukuyan.yunpin.android.snapshot.CandidateSnapshotHealth
import io.github.kukuyan.yunpin.android.status.StatusStore
import io.github.kukuyan.yunpin.android.status.SyncPhase
import io.github.kukuyan.yunpin.android.upgrade.UpgradeJournal
import java.util.UUID
import java.util.concurrent.CountDownLatch
import java.util.concurrent.FutureTask
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class SyncCoordinatorInstrumentedTest {
    @Test
    fun schedulerBoundaryUsesOpaqueCredentialAndPersistsOnlyRedactedResult() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val profiles = ServerProfileStore(context)
        val credentials = KeystoreCredentialStore(context)
        val statuses = StatusStore(context)
        val profile = profiles.save(
            existingId = null,
            displayName = "Synthetic relay ${UUID.randomUUID()}",
            rawEndpoint = "https://sync.example.test",
            allowPrivateHttp = false,
        )
        val opaque = ByteArray(128) { index -> ((index * 29 + 5) and 0xff).toByte() }
        credentials.store(profile.id, opaque)
        val factory = RecordingCoreFactory()
        val coordinator = SyncCoordinator(
            context = context,
            profiles = profiles,
            credentials = credentials,
            coreFactory = factory,
            statuses = statuses,
            dataPlaneGate = DataPlaneGate { true },
            clock = { 1234L },
        )
        val result = coordinator.runBounded()
        assertFalse(result.retry)
        val health = coordinator.localStateHealth(profile.id)
        assertTrue(health.healthy)
        assertTrue(health.snapshotPresent)
        assertFalse(coordinator.localStateHealth(UUID.randomUUID().toString()).healthy)
        assertTrue(factory.openedCredential?.all { it == 0.toByte() } == true)
        val status = statuses.read()
        assertEquals(SyncPhase.SUCCEEDED, status.phase)
        assertEquals(12L, status.cursor)
        assertEquals(3, status.pendingEncryptedEnvelopes)
        assertTrue(status.snapshotPresent)
        assertFalse(status.render().contains("sync.example.test"))
    }

    @Test
    fun differentServerProfilesUseDifferentDatabaseAndSnapshotPaths() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val profiles = ServerProfileStore(context)
        val credentials = KeystoreCredentialStore(context)
        val factory = RecordingCoreFactory()
        val coordinator = SyncCoordinator(
            context = context,
            profiles = profiles,
            credentials = credentials,
            coreFactory = factory,
            statuses = StatusStore(context),
            dataPlaneGate = DataPlaneGate { true },
        )
        val first = profiles.save(null, "Synthetic one", "https://one.example.test", false)
        credentials.store(first.id, ByteArray(64) { 1 })
        coordinator.runBounded()
        val second = profiles.save(null, "Synthetic two", "https://two.example.test", false)
        credentials.store(second.id, ByteArray(64) { 2 })
        coordinator.runBounded()

        val paths = factory.openedConfigs.takeLast(2)
        assertEquals(2, paths.size)
        assertFalse(paths[0].databasePath == paths[1].databasePath)
        assertFalse(paths[0].snapshotPath == paths[1].snapshotPath)
    }

    @Test
    fun appOwnedMutationsReachCoreAndScheduleOnlyDurableChanges() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val profiles = ServerProfileStore(context)
        val credentials = KeystoreCredentialStore(context)
        val factory = RecordingCoreFactory()
        var scheduled = 0
        val profile = profiles.save(
            existingId = null,
            displayName = "Synthetic mutation relay ${UUID.randomUUID()}",
            rawEndpoint = "https://mutations.example.test",
            allowPrivateHttp = false,
        )
        credentials.store(profile.id, ByteArray(64) { 7 })
        val coordinator = SyncCoordinator(
            context = context,
            profiles = profiles,
            credentials = credentials,
            coreFactory = factory,
            statuses = StatusStore(context),
            dataPlaneGate = DataPlaneGate { true },
            scheduleImmediate = {
                scheduled += 1
                true
            },
        )

        val normal = coordinator.recordSelection(
            text = "合成离线候选",
            pinyin = "he cheng li xian hou xuan",
            noPersonalizedLearning = false,
        )
        val protected = coordinator.recordSelection(
            text = "合成保护上下文",
            pinyin = "he cheng bao hu shang xia wen",
            noPersonalizedLearning = true,
        )
        val saved = coordinator.saveExplicit("合成显式保存", "he cheng xian shi bao cun", 4, true)
        val deleted = coordinator.delete("合成删除", "he cheng shan chu")
        val published = coordinator.publishSnapshot()

        assertTrue(normal.result.recorded)
        assertTrue(normal.syncScheduled)
        assertFalse(protected.result.recorded)
        assertFalse(protected.syncScheduled)
        assertTrue(saved.syncScheduled)
        assertTrue(deleted.syncScheduled)
        assertTrue(published.result.changed)
        assertTrue(published.syncScheduled)
        assertEquals(listOf(false, true), factory.noPersonalizedLearning)
        assertEquals(1, factory.saveExplicitCalls)
        assertEquals(1, factory.deleteCalls)
        assertEquals(1, factory.publishSnapshotCalls)
        assertEquals(4, scheduled)
    }

    @Test
    fun pendingUpgradeDoesNotOpenCoreAndHealthyGateAllowsIt() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val profiles = ServerProfileStore(context)
        val credentials = KeystoreCredentialStore(context)
        val factory = RecordingCoreFactory()
        val profile = profiles.save(null, "Synthetic gated relay", "https://gated.example.test", false)
        credentials.store(profile.id, ByteArray(64) { 8 })
        var healthy = false
        val coordinator = SyncCoordinator(
            context = context,
            profiles = profiles,
            credentials = credentials,
            coreFactory = factory,
            statuses = StatusStore(context),
            dataPlaneGate = DataPlaneGate { healthy },
        )

        assertFalse(coordinator.runBounded().retry)
        assertTrue(factory.openedConfigs.isEmpty())

        healthy = true
        assertFalse(coordinator.runBounded().retry)
        assertEquals(1, factory.openedConfigs.size)
    }

    @Test
    fun queuedRollbackRejectsStaleJournalDigestBeforeOpeningCore() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val profiles = ServerProfileStore(context)
        val credentials = KeystoreCredentialStore(context)
        val journal = UpgradeJournal(context)
        val factory = RecordingCoreFactory()
        val profile = profiles.save(null, "Synthetic stale LKG relay", "https://stale-lkg.example.test", false)
        credentials.store(profile.id, ByteArray(64) { 9 })
        journal.recordLaunch(70, profile.id)
        assertTrue(journal.markHealthy(70, profile.id, DIGEST_A))
        val captured = journal.lastKnownGoodSnapshotDigest(profile.id)!!
        assertTrue(journal.markHealthy(70, profile.id, DIGEST_B))
        val coordinator = SyncCoordinator(
            context = context,
            profiles = profiles,
            credentials = credentials,
            coreFactory = factory,
            statuses = StatusStore(context),
            dataPlaneGate = DataPlaneGate { true },
            rollbackVerifier = FixedRollbackVerifier(DIGEST_A),
            rollbackLkgLease = journal,
        )

        assertFalse(coordinator.rollbackSnapshot(profile.id, captured))
        assertTrue(factory.openedConfigs.isEmpty())
        assertEquals(0, factory.rollbackCalls)
    }

    @Test
    fun matchingJournalAndFileFingerprintsPermitNativeRestore() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val profiles = ServerProfileStore(context)
        val credentials = KeystoreCredentialStore(context)
        val journal = UpgradeJournal(context)
        val factory = RecordingCoreFactory()
        val profile = profiles.save(null, "Synthetic matching LKG relay", "https://matching-lkg.example.test", false)
        credentials.store(profile.id, ByteArray(64) { 10 })
        journal.recordLaunch(71, profile.id)
        assertTrue(journal.markHealthy(71, profile.id, DIGEST_A))
        val coordinator = SyncCoordinator(
            context = context,
            profiles = profiles,
            credentials = credentials,
            coreFactory = factory,
            statuses = StatusStore(context),
            dataPlaneGate = DataPlaneGate { true },
            rollbackVerifier = FixedRollbackVerifier(DIGEST_A),
            rollbackLkgLease = journal,
        )

        assertTrue(coordinator.rollbackSnapshot(profile.id, DIGEST_A))
        assertEquals(1, factory.rollbackCalls)
    }

    @Test
    fun exactHealthCommitHoldsCoordinatorMonitorAgainstSnapshotPublication() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val profiles = ServerProfileStore(context)
        val credentials = KeystoreCredentialStore(context)
        val journal = UpgradeJournal(context)
        val currentDigest = AtomicReference(DIGEST_A)
        val commitEntered = CountDownLatch(1)
        val releaseCommit = CountDownLatch(1)
        val factory = RecordingCoreFactory(onPublishSnapshot = { currentDigest.set(DIGEST_C) })
        val profile = profiles.save(null, "Synthetic exact health relay", "https://exact-health.example.test", false)
        credentials.store(profile.id, ByteArray(64) { 11 })
        journal.recordLaunch(72, profile.id)
        val coordinator = SyncCoordinator(
            context = context,
            profiles = profiles,
            credentials = credentials,
            coreFactory = factory,
            statuses = StatusStore(context),
            // Model the same-version healthy refresh: the data plane is already
            // permitted, so only the coordinator monitor closes this window.
            dataPlaneGate = DataPlaneGate { true },
            upgradeSnapshotHealthReader = UpgradeSnapshotHealthReader {
                CandidateSnapshotHealth(present = true, sha256Hex = currentDigest.get())
            },
            upgradeHealthCommitter = UpgradeHealthCommitter { version, profileId, digest ->
                commitEntered.countDown()
                check(releaseCommit.await(5, TimeUnit.SECONDS))
                check(currentDigest.get() == DIGEST_A)
                journal.markHealthy(version, profileId, digest)
            },
            scheduleImmediate = { true },
        )
        val healthTask = FutureTask {
            coordinator.verifyAndMarkUpgradeHealthy(72, profile.id)
        }
        val healthThread = Thread(healthTask, "yunpin-health-test")
        val publishTask = FutureTask { coordinator.publishSnapshot() }
        val publishThread = Thread(publishTask, "yunpin-publish-test")

        try {
            healthThread.start()
            assertTrue(commitEntered.await(5, TimeUnit.SECONDS))
            publishThread.start()
            assertTrue(awaitBlocked(publishThread))
            assertFalse(publishTask.isDone)
            assertEquals(0, factory.publishSnapshotCalls)
            assertEquals(DIGEST_A, currentDigest.get())
            assertEquals(null, journal.lastKnownGoodSnapshotDigest(profile.id))
        } finally {
            releaseCommit.countDown()
        }

        assertTrue(healthTask.get(5, TimeUnit.SECONDS).marked)
        assertTrue(publishTask.get(5, TimeUnit.SECONDS).result.changed)
        assertEquals(DIGEST_A, journal.lastKnownGoodSnapshotDigest(profile.id))
        assertEquals(DIGEST_C, currentDigest.get())
    }

    private class RecordingCoreFactory(
        private val onPublishSnapshot: () -> Unit = {},
    ) : MobileSyncCoreFactory {
        var openedCredential: ByteArray? = null
        val openedConfigs = mutableListOf<MobileCoreOpenConfig>()
        val noPersonalizedLearning = mutableListOf<Boolean>()
        var saveExplicitCalls = 0
        var deleteCalls = 0
        var publishSnapshotCalls = 0
        var rollbackCalls = 0

        override fun open(config: MobileCoreOpenConfig): MobileSyncCoreSession {
            openedCredential = config.opaqueCredential
            openedConfigs += config
            return object : MobileSyncCoreSession {
                override fun sync(timeoutMillis: Long) = MobileCoreSyncReport(
                    rounds = 1,
                    uploaded = 1,
                    downloaded = 2,
                    cursor = 12,
                    pending = 3,
                    snapshotRows = 4,
                    snapshotChanged = true,
                )

                override fun status(timeoutMillis: Long) = MobileCoreStatus(
                    cursor = 12,
                    pending = 3,
                    prepared = false,
                    snapshotPresent = true,
                    rollbackPresent = true,
                    signedRosterChainRequired = true,
                )

                override fun recordSelection(
                    text: String,
                    pinyin: String,
                    passwordField: Boolean,
                    privateMode: Boolean,
                    oneTimeInput: Boolean,
                    noPersonalizedLearning: Boolean,
                    timeoutMillis: Long,
                ): MobileCoreLearnResult {
                    this@RecordingCoreFactory.noPersonalizedLearning += noPersonalizedLearning
                    return MobileCoreLearnResult(
                        recorded = !passwordField && !privateMode && !oneTimeInput && !noPersonalizedLearning,
                        useCount = if (noPersonalizedLearning) 0 else 1,
                        syncEligible = !passwordField && !privateMode && !oneTimeInput && !noPersonalizedLearning,
                    )
                }

                override fun saveExplicit(
                    text: String,
                    pinyin: String,
                    useCount: Long,
                    pinned: Boolean,
                    timeoutMillis: Long,
                ) {
                    saveExplicitCalls += 1
                }

                override fun delete(text: String, pinyin: String, timeoutMillis: Long) {
                    deleteCalls += 1
                }

                override fun publishSnapshot(timeoutMillis: Long): MobileCoreSnapshotReport {
                    onPublishSnapshot()
                    publishSnapshotCalls += 1
                    return MobileCoreSnapshotReport(
                        generation = 9,
                        rows = 4,
                        changed = true,
                        rollbackAvailable = true,
                    )
                }

                override fun rollbackSnapshot() {
                    rollbackCalls += 1
                }
                override fun cancelCurrentOperation() = Unit
                override fun close() = Unit
            }
        }
    }

    private class FixedRollbackVerifier(private val digest: String) : RollbackSnapshotVerifier {
        override fun rollbackFingerprint(profileId: String): String = digest
        override fun currentFingerprint(profileId: String): String = digest
    }

    private fun awaitBlocked(thread: Thread): Boolean {
        val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(5)
        while (System.nanoTime() < deadline) {
            if (thread.state == Thread.State.BLOCKED) return true
            if (!thread.isAlive) return false
            Thread.yield()
        }
        return false
    }

    private companion object {
        val DIGEST_A = "a".repeat(64)
        val DIGEST_B = "b".repeat(64)
        val DIGEST_C = "c".repeat(64)
    }
}
