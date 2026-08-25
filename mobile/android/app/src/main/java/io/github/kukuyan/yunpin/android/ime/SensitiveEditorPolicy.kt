// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import android.text.InputType
import android.view.inputmethod.EditorInfo

data class EditorPrivacy(val contextFlags: Int) {
    val privateCandidatesAllowed: Boolean get() = contextFlags == 0
}

object SensitiveEditorPolicy {
    const val CONTEXT_PASSWORD = 1 shl 0
    const val CONTEXT_PRIVATE_MODE = 1 shl 1
    const val CONTEXT_ONE_TIME_INPUT = 1 shl 2
    const val CONTEXT_NO_PERSONALIZED_LEARNING = 1 shl 3
    const val CONTEXT_SHARED_SNAPSHOT_UNAVAILABLE = 1 shl 4

    fun evaluate(
        inputType: Int,
        imeOptions: Int,
        privateMode: Boolean = false,
        oneTimeInput: Boolean = false,
        snapshotAvailable: Boolean = true,
    ): EditorPrivacy {
        var flags = 0
        val inputClass = inputType and InputType.TYPE_MASK_CLASS
        val variation = inputType and InputType.TYPE_MASK_VARIATION
        val password = when (inputClass) {
            InputType.TYPE_CLASS_TEXT -> variation == InputType.TYPE_TEXT_VARIATION_PASSWORD ||
                variation == InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD ||
                variation == InputType.TYPE_TEXT_VARIATION_WEB_PASSWORD
            InputType.TYPE_CLASS_NUMBER -> variation == InputType.TYPE_NUMBER_VARIATION_PASSWORD
            else -> false
        }
        val suggestionsDisabled = inputClass == InputType.TYPE_CLASS_TEXT &&
            (inputType and InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS) != 0
        if (password) flags = flags or CONTEXT_PASSWORD
        if (privateMode || suggestionsDisabled) flags = flags or CONTEXT_PRIVATE_MODE
        if (oneTimeInput) flags = flags or CONTEXT_ONE_TIME_INPUT
        if ((imeOptions and EditorInfo.IME_FLAG_NO_PERSONALIZED_LEARNING) != 0) {
            flags = flags or CONTEXT_NO_PERSONALIZED_LEARNING
        }
        if (!snapshotAvailable) flags = flags or CONTEXT_SHARED_SNAPSHOT_UNAVAILABLE
        return EditorPrivacy(flags)
    }
}
