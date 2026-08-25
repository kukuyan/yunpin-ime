// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import android.graphics.Typeface
import android.inputmethodservice.InputMethodService
import android.os.Handler
import android.os.FileObserver
import android.os.Looper
import android.system.Os
import android.view.Gravity
import android.view.KeyEvent
import android.view.View
import android.view.ViewGroup
import android.view.inputmethod.EditorInfo
import android.widget.Button
import android.widget.HorizontalScrollView
import android.widget.LinearLayout
import io.github.kukuyan.yunpin.android.snapshot.SnapshotPaths
import io.github.kukuyan.yunpin.android.snapshot.SnapshotReader
import java.util.concurrent.Executors
import java.util.concurrent.Future
import java.util.concurrent.RejectedExecutionException
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Read-only keyboard surface. It has no sync, credential, queue, or network
 * dependency and never emits learning events.
 */
class YunPinInputMethodService : InputMethodService() {
    private val engines = LastGoodSlot<NativeCandidateEngine>()
    private val loader = Executors.newSingleThreadExecutor()
    private val refreshScheduled = AtomicBoolean(false)
    private val refreshRequested = AtomicBoolean(false)
    private val mainHandler = Handler(Looper.getMainLooper())
    private val observerLock = Any()
    private val profileRuntimeListener = ImeProfileRuntime.Listener { scheduleReload ->
        handleProfileInvalidation(scheduleReload)
    }
    @Volatile private var snapshotStamp: SnapshotStamp? = null
    @Volatile private var snapshotProfilePath: String? = null
    @Volatile private var destroyed = false
    private var loadTask: Future<*>? = null
    private var rootObserver: FileObserver? = null
    private var profileObserver: FileObserver? = null
    private var inputActive = false
    private var privacy = EditorPrivacy(SensitiveEditorPolicy.CONTEXT_SHARED_SNAPSHOT_UNAVAILABLE)
    private val composition = StringBuilder()
    private var candidates = CandidateBatch.EMPTY
    private lateinit var candidateRow: LinearLayout

    override fun onCreate() {
        super.onCreate()
        val switchPending = ImeProfileRuntime.register(profileRuntimeListener)
        startProfileObservers()
        if (!switchPending) scheduleSnapshotRefresh()
    }

    override fun onCreateInputView(): View {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(4), dp(4), dp(4), dp(6))
        }
        candidateRow = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        root.addView(HorizontalScrollView(this).apply { addView(candidateRow) }, matchWidth(dp(48)))
        listOf("qwertyuiop", "asdfghjkl", "zxcvbnm").forEach { row ->
            root.addView(letterRow(row), matchWidth(dp(48)))
        }
        root.addView(actionRow(), matchWidth(dp(52)))
        return root
    }

    override fun onStartInput(attribute: EditorInfo?, restarting: Boolean) {
        super.onStartInput(attribute, restarting)
        inputActive = true
        clearComposition(finish = false)
        val explicitSignals = PrivateImeSignalParser.parse(attribute?.privateImeOptions)
        privacy = SensitiveEditorPolicy.evaluate(
            inputType = attribute?.inputType ?: 0,
            imeOptions = attribute?.imeOptions ?: 0,
            privateMode = explicitSignals.privateMode,
            oneTimeInput = explicitSignals.oneTimeInput,
            snapshotAvailable = engines.isAvailable(),
        )
        scheduleSnapshotRefresh()
        updateCandidates()
    }

    override fun onFinishInput() {
        inputActive = false
        clearComposition(finish = true)
        privacy = EditorPrivacy(SensitiveEditorPolicy.CONTEXT_SHARED_SNAPSHOT_UNAVAILABLE)
        super.onFinishInput()
    }

    override fun onUpdateSelection(
        oldSelStart: Int,
        oldSelEnd: Int,
        newSelStart: Int,
        newSelEnd: Int,
        candidatesStart: Int,
        candidatesEnd: Int,
    ) {
        super.onUpdateSelection(oldSelStart, oldSelEnd, newSelStart, newSelEnd, candidatesStart, candidatesEnd)
        if (composition.isNotEmpty() && (newSelStart != candidatesEnd || newSelEnd != candidatesEnd)) {
            clearComposition(finish = true)
        }
    }

    override fun onDestroy() {
        destroyed = true
        ImeProfileRuntime.unregister(profileRuntimeListener)
        synchronized(observerLock) {
            rootObserver?.stopWatching()
            profileObserver?.stopWatching()
            rootObserver = null
            profileObserver = null
        }
        loadTask?.cancel(true)
        loader.shutdownNow()
        mainHandler.removeCallbacksAndMessages(null)
        engines.close()
        super.onDestroy()
    }

    /** Enqueues work only; no snapshot payload read or parse occurs here. */
    private fun scheduleSnapshotRefresh() {
        if (destroyed) return
        refreshRequested.set(true)
        if (!refreshScheduled.compareAndSet(false, true)) return
        try {
            loadTask = loader.submit {
                try {
                    while (!destroyed && refreshRequested.getAndSet(false)) {
                        refreshSnapshotOffInputThread()
                    }
                } finally {
                    refreshScheduled.set(false)
                    if (!destroyed && refreshRequested.get()) scheduleSnapshotRefresh()
                }
            }
        } catch (_: RejectedExecutionException) {
            refreshScheduled.set(false)
        }
    }

    private fun refreshSnapshotOffInputThread() {
        val observation = engines.captureObservation()
        val file = SnapshotPaths.activeCurrent(this)
        val profilePath = file?.absolutePath
        val lease = engines.beginLoad(observation, profilePath)
        if (snapshotProfilePath != engines.activeProfilePath()) {
            snapshotStamp = null
            snapshotProfilePath = engines.activeProfilePath()
            postSnapshotAvailability()
        }
        if (file == null || lease == null) {
            if (file != null) refreshRequested.set(true)
            return
        }
        val stat = try {
            Os.stat(file.absolutePath)
        } catch (_: Exception) {
            return
        }
        val stamp = SnapshotStamp(file.lastModified(), stat.st_size, stat.st_ino)
        if (snapshotStamp == stamp && engines.isAvailable()) return
        val bytes = SnapshotReader.read(file) ?: return
        val replacement = try {
            NativeCandidateEngine()
        } catch (_: Exception) {
            bytes.fill(0)
            return
        } catch (_: LinkageError) {
            bytes.fill(0)
            return
        }
        val loaded = try {
            !destroyed && replacement.loadSnapshot(bytes)
        } catch (_: Exception) {
            false
        } catch (_: LinkageError) {
            false
        } finally {
            bytes.fill(0)
        }
        val finalProfilePath = SnapshotPaths.activeCurrent(this)?.absolutePath
        if (!engines.publishIfCurrent(replacement, lease, finalProfilePath, loaded && !destroyed)) {
            if (finalProfilePath != profilePath) refreshRequested.set(true)
            snapshotProfilePath = engines.activeProfilePath()
            snapshotStamp = null
            postSnapshotAvailability()
            return
        }
        snapshotProfilePath = profilePath
        snapshotStamp = stamp
        postSnapshotAvailability()
    }

    private fun postSnapshotAvailability() {
        mainHandler.post {
            if (!destroyed && inputActive) {
                val unavailable = SensitiveEditorPolicy.CONTEXT_SHARED_SNAPSHOT_UNAVAILABLE
                privacy = if (engines.isAvailable()) {
                    EditorPrivacy(privacy.contextFlags and unavailable.inv())
                } else {
                    EditorPrivacy(privacy.contextFlags or unavailable)
                }
                updateCandidates()
            }
        }
    }

    private fun startProfileObservers() {
        val root = object : FileObserver(
            filesDir.absolutePath,
            FileObserver.CREATE or FileObserver.MOVED_TO or FileObserver.DELETE or FileObserver.MOVED_FROM,
        ) {
            override fun onEvent(event: Int, path: String?) {
                if (path == "sync-profiles") {
                    ImeProfileRuntime.pointerChanged()
                    startProfileDirectoryObserver()
                }
            }
        }
        synchronized(observerLock) {
            if (destroyed) return
            rootObserver = root
            root.startWatching()
        }
        startProfileDirectoryObserver()
    }

    private fun startProfileDirectoryObserver() {
        val directory = SnapshotPaths.activePointerDirectory(this)
        if (!directory.isDirectory) return
        val observer = object : FileObserver(
            directory.absolutePath,
            FileObserver.CLOSE_WRITE or FileObserver.CREATE or FileObserver.DELETE or
                FileObserver.MOVED_FROM or FileObserver.MOVED_TO or FileObserver.DELETE_SELF or FileObserver.MOVE_SELF,
        ) {
            override fun onEvent(event: Int, path: String?) {
                val pointerEvent = path == null || path == SnapshotPaths.ACTIVE_POINTER_NAME
                if (pointerEvent) ImeProfileRuntime.pointerChanged()
                if (event and (FileObserver.DELETE_SELF or FileObserver.MOVE_SELF) != 0) {
                    synchronized(observerLock) {
                        if (profileObserver === this) profileObserver = null
                    }
                }
            }
        }
        synchronized(observerLock) {
            if (destroyed || profileObserver != null) return
            profileObserver = observer
            observer.startWatching()
        }
    }

    private fun handleProfileInvalidation(scheduleReload: Boolean) {
        if (destroyed) return
        engines.invalidate()
        snapshotProfilePath = null
        snapshotStamp = null
        mainHandler.post {
            if (!destroyed && inputActive) {
                clearComposition(finish = true)
                val unavailable = SensitiveEditorPolicy.CONTEXT_SHARED_SNAPSHOT_UNAVAILABLE
                privacy = EditorPrivacy(privacy.contextFlags or unavailable)
            }
        }
        if (scheduleReload) scheduleSnapshotRefresh()
    }

    private fun letterRow(letters: String): LinearLayout = LinearLayout(this).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER
        letters.forEach { letter ->
            addView(key(letter.toString()) { appendLetter(letter) }, weightedKey())
        }
    }

    private fun actionRow(): LinearLayout = LinearLayout(this).apply {
        orientation = LinearLayout.HORIZONTAL
        addView(key("⌫") { backspace() }, weightedKey(1f))
        addView(key("Space") { commitSpace() }, weightedKey(3f))
        addView(key("Enter") { commitEnter() }, weightedKey(1f))
    }

    private fun key(label: String, action: () -> Unit): Button = Button(this).apply {
        text = label
        isAllCaps = false
        setOnClickListener { action() }
    }

    private fun appendLetter(letter: Char) {
        composition.append(letter)
        currentInputConnection?.setComposingText(composition, 1)
        updateCandidates()
    }

    private fun backspace() {
        if (composition.isNotEmpty()) {
            composition.deleteCharAt(composition.lastIndex)
            if (composition.isEmpty()) currentInputConnection?.finishComposingText()
            else currentInputConnection?.setComposingText(composition, 1)
            updateCandidates()
        } else {
            currentInputConnection?.deleteSurroundingText(1, 0)
        }
    }

    private fun commitSpace() {
        if (composition.isEmpty()) {
            currentInputConnection?.commitText(" ", 1)
            return
        }
        val batch = candidates
        val observation = batch.observation ?: return
        val committed = engines.withCurrentIf(observation) {
            val value = batch.values.firstOrNull() ?: composition.toString()
            if (value.isNotEmpty()) currentInputConnection?.commitText(value, 1)
            currentInputConnection?.commitText(" ", 1)
            true
        } ?: false
        if (!committed) return
        clearComposition(finish = true)
    }

    private fun commitEnter() {
        if (composition.isNotEmpty()) currentInputConnection?.commitText(composition.toString(), 1)
        clearComposition(finish = true)
        currentInputConnection?.sendKeyEvent(KeyEvent(KeyEvent.ACTION_DOWN, KeyEvent.KEYCODE_ENTER))
        currentInputConnection?.sendKeyEvent(KeyEvent(KeyEvent.ACTION_UP, KeyEvent.KEYCODE_ENTER))
    }

    private fun commitCandidate(candidate: String, observation: LastGoodSlot.Observation) {
        if (!privacy.privateCandidatesAllowed) return
        val committed = engines.withCurrentIf(observation) {
            currentInputConnection?.commitText(candidate, 1)
            true
        } ?: false
        if (!committed) return
        clearComposition(finish = true)
    }

    private fun clearComposition(finish: Boolean) {
        composition.clear()
        candidates = CandidateBatch.EMPTY
        if (::candidateRow.isInitialized) candidateRow.removeAllViews()
        if (finish) currentInputConnection?.finishComposingText()
    }

    private fun updateCandidates() {
        candidates = if (composition.isNotEmpty()) {
            try {
                engines.withCurrentObserved { engine, observation ->
                    val values = if (privacy.privateCandidatesAllowed) {
                        engine.query(composition.toString(), 2, privacy.contextFlags)
                    } else {
                        emptyList()
                    }
                    CandidateBatch(values, observation)
                } ?: CandidateBatch.EMPTY
            } catch (_: Exception) {
                CandidateBatch.EMPTY
            } catch (_: LinkageError) {
                CandidateBatch.EMPTY
            }
        } else {
            CandidateBatch.EMPTY
        }
        if (!::candidateRow.isInitialized) return
        candidateRow.removeAllViews()
        val rendered = candidates
        rendered.values.forEach { candidate ->
            val observation = rendered.observation ?: return@forEach
            candidateRow.addView(Button(this).apply {
                text = candidate
                isAllCaps = false
                typeface = Typeface.DEFAULT
                setOnClickListener { commitCandidate(candidate, observation) }
            })
        }
    }

    private fun weightedKey(weight: Float = 1f) = LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.MATCH_PARENT, weight)
    private fun matchWidth(height: Int) = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, height)
    private fun dp(value: Int) = (value * resources.displayMetrics.density).toInt()

    private data class SnapshotStamp(val modifiedMillis: Long, val byteCount: Long, val inode: Long)

    private data class CandidateBatch(
        val values: List<String>,
        val observation: LastGoodSlot.Observation?,
    ) {
        companion object {
            val EMPTY = CandidateBatch(emptyList(), null)
        }
    }
}
