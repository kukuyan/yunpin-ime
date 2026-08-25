// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.sync

import org.json.JSONObject
import org.json.JSONTokener
import java.io.Closeable
import java.lang.reflect.Modifier

data class MobileCoreOpenConfig(
    val databasePath: String,
    val snapshotPath: String,
    val endpoint: String,
    val allowPrivateHttp: Boolean,
    val opaqueCredential: ByteArray,
)

data class MobileCoreSyncReport(
    val rounds: Int,
    val uploaded: Int,
    val downloaded: Int,
    val cursor: Long,
    val pending: Long,
    val snapshotRows: Int,
    val snapshotChanged: Boolean,
)

data class MobileCoreStatus(
    val cursor: Long,
    val pending: Long,
    val prepared: Boolean,
    val snapshotPresent: Boolean,
    val rollbackPresent: Boolean,
    val signedRosterChainRequired: Boolean,
)

data class MobileCoreLearnResult(
    val recorded: Boolean,
    val useCount: Long,
    val syncEligible: Boolean,
)

data class MobileCoreSnapshotReport(
    val generation: Long,
    val rows: Int,
    val changed: Boolean,
    val rollbackAvailable: Boolean,
)

class MobileCoreBindingException(val redactedCode: String) : IllegalStateException()
class MobileCoreUnavailableException : IllegalStateException()

interface MobileSyncCoreSession : Closeable {
    fun sync(timeoutMillis: Long): MobileCoreSyncReport
    fun status(timeoutMillis: Long): MobileCoreStatus
    fun recordSelection(
        text: String,
        pinyin: String,
        passwordField: Boolean,
        privateMode: Boolean,
        oneTimeInput: Boolean,
        noPersonalizedLearning: Boolean,
        timeoutMillis: Long,
    ): MobileCoreLearnResult
    fun saveExplicit(text: String, pinyin: String, useCount: Long, pinned: Boolean, timeoutMillis: Long)
    fun delete(text: String, pinyin: String, timeoutMillis: Long)
    fun publishSnapshot(timeoutMillis: Long): MobileCoreSnapshotReport
    fun rollbackSnapshot()
    fun cancelCurrentOperation()
}

interface MobileSyncCoreFactory {
    fun open(config: MobileCoreOpenConfig): MobileSyncCoreSession
}

/**
 * Narrow adapter for an optional gomobile AAR generated from mobile/synccore.
 * The IME process never references this class. Reflection keeps a source
 * checkout buildable until that generated artifact passes the toolchain gate.
 */
class GeneratedGoMobileCoreFactory : MobileSyncCoreFactory {
    override fun open(config: MobileCoreOpenConfig): MobileSyncCoreSession {
        val generatedClass = try {
            Class.forName(GENERATED_CLASS)
        } catch (_: ClassNotFoundException) {
            throw MobileCoreUnavailableException()
        } catch (_: LinkageError) {
            throw MobileCoreUnavailableException()
        }
        val arguments = arrayOf<Any>(
            config.databasePath,
            config.snapshotPath,
            config.endpoint,
            config.allowPrivateHttp,
            config.opaqueCredential,
        )
        val method = try {
            generatedClass.methods.filter {
                Modifier.isStatic(it.modifiers) &&
                    it.name.equals("openFacade", ignoreCase = true) &&
                    it.accepts(arguments)
            }.singleOrNull()
        } catch (_: Exception) {
            null
        } catch (_: LinkageError) {
            null
        } ?: throw MobileCoreUnavailableException()
        val facade = try {
            method.invoke(null, *arguments)
        } catch (_: Exception) {
            throw MobileCoreBindingException("local_state_error")
        } catch (_: LinkageError) {
            throw MobileCoreBindingException("local_state_error")
        } ?: throw MobileCoreBindingException("local_state_error")
        return ReflectiveMobileSyncCoreSession(facade)
    }

    private companion object {
        const val GENERATED_CLASS = "go.mobilecore.Mobilecore"
    }
}

private class ReflectiveMobileSyncCoreSession(private val facade: Any) : MobileSyncCoreSession {
    override fun sync(timeoutMillis: Long): MobileCoreSyncReport =
        MobileCoreJson.parseSync(invokeString("sync", timeoutMillis))

    override fun status(timeoutMillis: Long): MobileCoreStatus =
        MobileCoreJson.parseStatus(invokeString("status", timeoutMillis))

    override fun recordSelection(
        text: String,
        pinyin: String,
        passwordField: Boolean,
        privateMode: Boolean,
        oneTimeInput: Boolean,
        noPersonalizedLearning: Boolean,
        timeoutMillis: Long,
    ): MobileCoreLearnResult = MobileCoreJson.parseLearn(
        invokeString(
            "recordSelection",
            text,
            pinyin,
            passwordField,
            privateMode,
            oneTimeInput,
            noPersonalizedLearning,
            timeoutMillis,
        ),
    )

    override fun saveExplicit(text: String, pinyin: String, useCount: Long, pinned: Boolean, timeoutMillis: Long) {
        invoke("saveExplicit", text, pinyin, useCount, pinned, timeoutMillis)
    }

    override fun delete(text: String, pinyin: String, timeoutMillis: Long) {
        invoke("delete", text, pinyin, timeoutMillis)
    }

    override fun publishSnapshot(timeoutMillis: Long): MobileCoreSnapshotReport =
        MobileCoreJson.parseSnapshot(invokeString("publishSnapshot", timeoutMillis))

    override fun rollbackSnapshot() {
        invoke("rollbackSnapshot")
    }

    override fun cancelCurrentOperation() {
        invoke("cancelCurrentOperation")
    }

    override fun close() {
        invoke("close")
    }

    private fun invokeString(name: String, vararg arguments: Any): String =
        invoke(name, *arguments) as? String ?: throw MobileCoreBindingException("local_state_error")

    private fun invoke(name: String, vararg arguments: Any): Any? {
        val method = try {
            facade.javaClass.methods.filter {
                !Modifier.isStatic(it.modifiers) &&
                    it.name.equals(name, ignoreCase = true) &&
                    it.accepts(arguments)
            }.singleOrNull()
        } catch (_: Exception) {
            null
        } catch (_: LinkageError) {
            null
        } ?: throw MobileCoreBindingException("local_state_error")
        return try {
            method.invoke(facade, *arguments)
        } catch (_: Exception) {
            throw MobileCoreBindingException("local_state_error")
        } catch (_: LinkageError) {
            throw MobileCoreBindingException("local_state_error")
        }
    }
}

private fun java.lang.reflect.Method.accepts(arguments: Array<out Any>): Boolean =
    parameterTypes.size == arguments.size && parameterTypes.indices.all { index ->
        val argument = arguments[index]
        when (parameterTypes[index]) {
            java.lang.Boolean.TYPE -> argument is Boolean
            java.lang.Byte.TYPE -> argument is Byte
            java.lang.Short.TYPE -> argument is Short
            java.lang.Integer.TYPE -> argument is Int
            java.lang.Long.TYPE -> argument is Long
            java.lang.Float.TYPE -> argument is Float
            java.lang.Double.TYPE -> argument is Double
            java.lang.Character.TYPE -> argument is Char
            else -> parameterTypes[index].isAssignableFrom(argument.javaClass)
        }
    }

internal object MobileCoreJson {
    fun parseSync(encoded: String): MobileCoreSyncReport = failClosed {
        val result = parseFacadeResult(encoded, SYNC_FIELDS)
        MobileCoreSyncReport(
            rounds = result.strictInt("Rounds"),
            uploaded = result.strictInt("Uploaded"),
            downloaded = result.strictInt("Downloaded"),
            cursor = result.strictLong("Cursor"),
            pending = result.strictLong("Pending"),
            snapshotRows = result.strictInt("SnapshotRows"),
            snapshotChanged = result.strictBoolean("SnapshotChanged"),
        ).also { report ->
            requireBinding(
                report.rounds >= 0 && report.uploaded >= 0 && report.downloaded >= 0 &&
                    report.cursor >= 0 && report.pending >= 0 && report.snapshotRows >= 0,
            )
        }
    }

    fun parseStatus(encoded: String): MobileCoreStatus = failClosed {
        val result = parseFacadeResult(encoded, STATUS_FIELDS)
        val gate = result.strictString("ControlPlaneGate")
        requireBinding(gate.isEmpty() || gate == "signed_roster_chain_required")
        MobileCoreStatus(
            cursor = result.strictLong("Cursor"),
            pending = result.strictLong("Pending"),
            prepared = result.strictBoolean("Prepared"),
            snapshotPresent = result.strictBoolean("SnapshotPresent"),
            rollbackPresent = result.strictBoolean("RollbackPresent"),
            signedRosterChainRequired = gate == "signed_roster_chain_required",
        ).also { status -> requireBinding(status.cursor >= 0 && status.pending >= 0) }
    }

    fun parseLearn(encoded: String): MobileCoreLearnResult = failClosed {
        val result = parseFacadeResult(encoded, LEARN_FIELDS)
        MobileCoreLearnResult(
            recorded = result.strictBoolean("Recorded"),
            useCount = result.strictLong("UseCount"),
            syncEligible = result.strictBoolean("SyncEligible"),
        ).also { learn -> requireBinding(learn.useCount >= 0) }
    }

    fun parseSnapshot(encoded: String): MobileCoreSnapshotReport = failClosed {
        val result = parseFacadeResult(encoded, SNAPSHOT_FIELDS)
        MobileCoreSnapshotReport(
            generation = result.strictLong("Generation"),
            rows = result.strictInt("Rows"),
            changed = result.strictBoolean("Changed"),
            rollbackAvailable = result.strictBoolean("RollbackAvailable"),
        ).also { snapshot -> requireBinding(snapshot.generation >= 0 && snapshot.rows >= 0) }
    }

    private fun parseFacadeResult(encoded: String, resultFields: Set<String>): JSONObject {
        if (encoded.toByteArray(Charsets.UTF_8).size !in 2..MAX_RESULT_BYTES) {
            throw MobileCoreBindingException("local_state_error")
        }
        val root = try {
            StrictJsonDuplicateKeyScanner(encoded).validate()
            val tokener = JSONTokener(encoded)
            val value = tokener.nextValue()
            if (value !is JSONObject || tokener.nextClean() != '\u0000') {
                throw MobileCoreBindingException("local_state_error")
            }
            value
        } catch (_: Exception) {
            throw MobileCoreBindingException("local_state_error")
        }
        val ok = root.opt("ok") as? Boolean ?: throw MobileCoreBindingException("local_state_error")
        if (!ok) {
            root.requireExactKeys(FAILURE_FIELDS)
            val code = root.opt("error_code") as? String ?: "local_state_error"
            throw MobileCoreBindingException(code.takeIf { it in REDACTED_CODES } ?: "local_state_error")
        }
        root.requireExactKeys(SUCCESS_FIELDS)
        return (root.optJSONObject("result") ?: throw MobileCoreBindingException("local_state_error"))
            .also { it.requireExactKeys(resultFields) }
    }

    private fun JSONObject.strictLong(name: String): Long {
        return when (val raw = get(name)) {
            is Byte -> raw.toLong()
            is Short -> raw.toLong()
            is Int -> raw.toLong()
            is Long -> raw
            else -> throw MobileCoreBindingException("local_state_error")
        }
    }

    private fun JSONObject.strictInt(name: String): Int {
        val value = strictLong(name)
        requireBinding(value in 0L..Int.MAX_VALUE.toLong())
        return value.toInt()
    }

    private fun JSONObject.strictBoolean(name: String): Boolean =
        (get(name) as? Boolean) ?: throw MobileCoreBindingException("local_state_error")

    private fun JSONObject.strictString(name: String): String =
        (get(name) as? String) ?: throw MobileCoreBindingException("local_state_error")

    private fun JSONObject.requireExactKeys(expected: Set<String>) {
        val actual = mutableSetOf<String>()
        val iterator = keys()
        while (iterator.hasNext()) actual += iterator.next()
        requireBinding(actual == expected)
    }

    private inline fun <T> failClosed(block: () -> T): T = try {
        block()
    } catch (error: MobileCoreBindingException) {
        throw error
    } catch (_: Exception) {
        throw MobileCoreBindingException("local_state_error")
    } catch (_: LinkageError) {
        throw MobileCoreBindingException("local_state_error")
    }

    private fun requireBinding(condition: Boolean) {
        if (!condition) throw MobileCoreBindingException("local_state_error")
    }

    private const val MAX_RESULT_BYTES = 16 * 1024
    private val SUCCESS_FIELDS = setOf("ok", "result")
    private val FAILURE_FIELDS = setOf("ok", "error_code")
    private val SYNC_FIELDS = setOf(
        "Rounds", "Uploaded", "Downloaded", "Cursor", "Pending", "SnapshotRows", "SnapshotChanged",
    )
    private val STATUS_FIELDS = setOf(
        "Cursor", "Pending", "Prepared", "SnapshotPresent", "RollbackPresent", "ControlPlaneGate",
    )
    private val LEARN_FIELDS = setOf("Recorded", "UseCount", "SyncEligible")
    private val SNAPSHOT_FIELDS = setOf("Generation", "Rows", "Changed", "RollbackAvailable")
    private val REDACTED_CODES = setOf(
        "cancelled",
        "deadline_exceeded",
        "authorization_required",
        "remote_conflict",
        "remote_rejected",
        "remote_unavailable",
        "network_unavailable",
        "local_state_error",
    )
}
