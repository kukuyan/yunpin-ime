// SPDX-License-Identifier: Apache-2.0
package io.github.kukuyan.yunpin.android.ime

import android.text.InputType
import android.view.inputmethod.EditorInfo
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import org.junit.Test

class SensitiveEditorPolicyTest {
    @Test
    fun allRequiredPasswordVariationsHidePrivateCandidates() {
        val protectedTypes = listOf(
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD,
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_VISIBLE_PASSWORD,
            InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_WEB_PASSWORD,
            InputType.TYPE_CLASS_NUMBER or InputType.TYPE_NUMBER_VARIATION_PASSWORD,
        )
        protectedTypes.forEach { inputType ->
            assertFalse(SensitiveEditorPolicy.evaluate(inputType, 0).privateCandidatesAllowed)
        }
    }

    @Test
    fun noPersonalizedLearningFlagFailsClosed() {
        val result = SensitiveEditorPolicy.evaluate(
            InputType.TYPE_CLASS_TEXT,
            EditorInfo.IME_FLAG_NO_PERSONALIZED_LEARNING,
        )
        assertFalse(result.privateCandidatesAllowed)
        assertTrue((result.contextFlags and SensitiveEditorPolicy.CONTEXT_NO_PERSONALIZED_LEARNING) != 0)
    }

    @Test
    fun ordinaryTextNeedsAnAvailableSnapshot() {
        assertTrue(SensitiveEditorPolicy.evaluate(InputType.TYPE_CLASS_TEXT, 0).privateCandidatesAllowed)
        assertFalse(
            SensitiveEditorPolicy.evaluate(
                InputType.TYPE_CLASS_TEXT,
                0,
                snapshotAvailable = false,
            ).privateCandidatesAllowed,
        )
    }

    @Test
    fun explicitPrivateOneTimeAndNoSuggestionSignalsFailClosed() {
        assertFalse(
            SensitiveEditorPolicy.evaluate(
                InputType.TYPE_CLASS_TEXT,
                0,
                privateMode = true,
            ).privateCandidatesAllowed,
        )
        assertFalse(
            SensitiveEditorPolicy.evaluate(
                InputType.TYPE_CLASS_TEXT,
                0,
                oneTimeInput = true,
            ).privateCandidatesAllowed,
        )
        assertFalse(
            SensitiveEditorPolicy.evaluate(
                InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS,
                0,
            ).privateCandidatesAllowed,
        )
    }
}
