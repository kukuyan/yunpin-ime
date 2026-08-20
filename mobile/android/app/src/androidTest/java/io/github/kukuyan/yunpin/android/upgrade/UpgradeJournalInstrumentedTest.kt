// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.upgrade

import android.content.Context
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.util.UUID
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class UpgradeJournalInstrumentedTest {
    @Test
    fun onlyExplicitHealthGateClearsPendingUpgrade() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
        preferences.edit().clear().commit()
        val profileId = UUID.randomUUID().toString()
        try {
            val journal = UpgradeJournal(context)
            assertTrue(journal.recordLaunch(10, profileId).healthPending)
            assertFalse(journal.dataPlaneAllowed(10, profileId))
            assertTrue(journal.markHealthy(10, profileId, DIGEST_A))
            assertFalse(journal.read(profileId).healthPending)
            assertTrue(journal.dataPlaneAllowed(10, profileId))
            assertEquals(DIGEST_A, journal.lastKnownGoodSnapshotDigest(profileId))
            assertFalse(journal.dataPlaneAllowed(11, profileId))

            assertTrue(journal.recordLaunch(11, profileId).healthPending)
            assertFalse(journal.read(profileId).rollbackSuggested)
            assertTrue(journal.recordLaunch(11, profileId).rollbackSuggested)
            assertFalse(journal.markHealthy(10, profileId, DIGEST_A))
            assertTrue(journal.read(profileId).healthPending)
        } finally {
            preferences.edit().clear().commit()
        }
    }

    @Test
    fun freshEmptyProfileCanBecomeHealthyAcrossTwoLaunchesOnlyBeforeSnapshotLkg() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
        preferences.edit().clear().commit()
        val profileId = UUID.randomUUID().toString()
        try {
            val journal = UpgradeJournal(context)
            assertTrue(journal.recordLaunch(20, profileId).healthPending)
            assertTrue(journal.markHealthy(20, profileId, null))
            assertFalse(journal.recordLaunch(20, profileId).healthPending)
            assertFalse(journal.read(profileId).lastKnownGoodSnapshotPresent)
            assertEquals(0L, journal.read(profileId).lastKnownGoodSnapshotGeneration)

            assertTrue(journal.recordLaunch(21, profileId).healthPending)
            assertTrue(journal.markHealthy(21, profileId, null))
            assertFalse(journal.recordLaunch(21, profileId).healthPending)
        } finally {
            preferences.edit().clear().commit()
        }
    }

    @Test
    fun missingSnapshotAfterValidatedGenerationNeverClearsPending() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
        preferences.edit().clear().commit()
        val profileId = UUID.randomUUID().toString()
        try {
            val journal = UpgradeJournal(context)
            journal.recordLaunch(30, profileId)
            assertTrue(journal.markHealthy(30, profileId, DIGEST_A))
            assertTrue(journal.read(profileId).lastKnownGoodSnapshotPresent)
            assertEquals(1L, journal.read(profileId).lastKnownGoodSnapshotGeneration)

            journal.recordLaunch(31, profileId)
            assertFalse(journal.markHealthy(31, profileId, null))
            assertTrue(journal.read(profileId).healthPending)
            assertTrue(journal.recordLaunch(31, profileId).rollbackSuggested)
        } finally {
            preferences.edit().clear().commit()
        }
    }

    @Test
    fun snapshotHistoryAndPendingStateAreIsolatedPerCanonicalProfile() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
        preferences.edit().clear().commit()
        val first = UUID.randomUUID().toString()
        val second = UUID.randomUUID().toString()
        try {
            val journal = UpgradeJournal(context)
            journal.recordLaunch(40, first)
            assertTrue(journal.markHealthy(40, first, DIGEST_A))
            journal.recordLaunch(41, first)

            journal.recordLaunch(41, second)
            assertTrue(journal.markHealthy(41, second, null))

            assertTrue(journal.read(first).healthPending)
            assertTrue(journal.read(first).lastKnownGoodSnapshotPresent)
            assertFalse(journal.read(second).healthPending)
            assertFalse(journal.read(second).lastKnownGoodSnapshotPresent)
        } finally {
            preferences.edit().clear().commit()
        }
    }

    @Test
    fun digestChangesAdvanceGenerationAndStableDigestDoesNot() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)
        preferences.edit().clear().commit()
        val profileId = UUID.randomUUID().toString()
        try {
            val journal = UpgradeJournal(context)
            journal.recordLaunch(50, profileId)
            assertTrue(journal.markHealthy(50, profileId, DIGEST_A))
            assertEquals(1L, journal.read(profileId).lastKnownGoodSnapshotGeneration)
            assertTrue(journal.markHealthy(50, profileId, DIGEST_A))
            assertEquals(1L, journal.read(profileId).lastKnownGoodSnapshotGeneration)
            assertTrue(journal.markHealthy(50, profileId, DIGEST_B))
            assertEquals(2L, journal.read(profileId).lastKnownGoodSnapshotGeneration)
        } finally {
            preferences.edit().clear().commit()
        }
    }

    private companion object {
        const val PREFERENCES = "yunpin_upgrade_journal_v2"
        val DIGEST_A = "a".repeat(64)
        val DIGEST_B = "b".repeat(64)
    }
}
