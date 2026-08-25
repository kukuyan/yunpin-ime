// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import android.content.Context
import java.util.concurrent.ThreadLocalRandom
import kotlin.math.pow
import kotlin.math.roundToLong

internal data class RetryBackoffPolicy(
    val baseDelayMillis: Long = 30_000L,
    val maximumDelayMillis: Long = 6L * 60L * 60L * 1000L,
    val jitterFraction: Double = 0.2,
) {
    init {
        require(baseDelayMillis > 0 && maximumDelayMillis >= baseDelayMillis)
        require(jitterFraction in 0.0..0.5)
    }

    fun delay(failureCount: Int, jitterUnit: Double): Long {
        val exponent = (failureCount - 1).coerceIn(0, 30)
        val exponential = baseDelayMillis.toDouble() * 2.0.pow(exponent)
        val capped = exponential.coerceAtMost(maximumDelayMillis.toDouble())
        val jittered = capped * (1.0 + jitterFraction * jitterUnit.coerceIn(-1.0, 1.0))
        return jittered.roundToLong().coerceIn(1L, maximumDelayMillis)
    }
}

/** Persists counters and the chosen delay only; no endpoint, identity, or error text. */
internal class SyncRetryController(
    context: Context,
    private val policy: RetryBackoffPolicy = RetryBackoffPolicy(),
    private val jitter: (Int) -> Double = { ThreadLocalRandom.current().nextDouble(-1.0, 1.0) },
) {
    private val preferences = context.getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE)

    @Synchronized
    fun recordFailure(): Long {
        val failures = (preferences.getInt(FAILURES, 0).coerceIn(0, 30) + 1).coerceAtMost(31)
        val delay = policy.delay(failures, jitter(failures))
        preferences.edit()
            .putInt(FAILURES, failures)
            .putLong(NEXT_DELAY, delay)
            .commit()
        return delay
    }

    @Synchronized
    fun reset() {
        preferences.edit()
            .putInt(FAILURES, 0)
            .putLong(NEXT_DELAY, policy.delay(0, 0.0))
            .commit()
    }

    private companion object {
        const val PREFERENCES = "yunpin_sync_retry_v1"
        const val FAILURES = "consecutive_failures"
        const val NEXT_DELAY = "next_delay_millis"
    }
}
