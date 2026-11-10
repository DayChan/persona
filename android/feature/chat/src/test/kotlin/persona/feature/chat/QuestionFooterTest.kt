package persona.feature.chat

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.QuestionOption
import persona.core.contracts.Resolution
import persona.core.designsystem.PersonaTheme

/**
 * The question footer as on iOS (needsFooter, #2821): pages and Submit for several questions or a multiple choice; a
 * single-choice question is answered by tapping an option, with a Submit only for its "Other answer" text.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-xhdpi", application = QuestionTestApplication::class)
class QuestionFooterTest {
    @get:Rule val rule = createComposeRule()
    private val format = Question("Choose a format", options = listOf(QuestionOption("JSON"), QuestionOption("CSV")))
    private val tests = Question("Which tests?", multiSelect = true, options = listOf(QuestionOption("Unit"), QuestionOption("UI")))

    @Test fun theFooterIsDrawnOnlyWhenThereIsSomethingToTurnOrSubmit() {
        data class Case(val label: String, val questions: List<Question>, val shown: List<String>, val hidden: List<String>)
        val cases = listOf(
            Case("one single choice", listOf(format), emptyList(), listOf("Submit", "Previous", "Question 1 of 1")),
            Case("two questions", listOf(format, tests), listOf("Next", "Previous", "Question 1 of 2"), listOf("Submit")),
            Case("one multiple choice", listOf(tests), listOf("Submit", "Question 1 of 1"), listOf("Next")),
        )
        var requests by mutableStateOf(emptyList<Interaction>())
        val sent = mutableListOf<Resolution>()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                InteractionPanel(requests, null, 600.dp, null, emptySet(), { _, resolution -> sent += resolution }, null, {})
            }
        }
        fun exists(text: String) = rule.onAllNodes(hasText(text) or hasContentDescription(text)).fetchSemanticsNodes().isNotEmpty()
        cases.forEachIndexed { i, c ->
            requests = listOf(Interaction("q$i", kind = "question", questions = c.questions))
            rule.waitForIdle()
            c.shown.forEach { assertEquals("${c.label}: $it", true, exists(it)) }
            c.hidden.forEach { assertEquals("${c.label}: $it", false, exists(it)) }
        }

        // One single choice with words of its own: one Submit, which sends them.
        requests = listOf(Interaction("other", kind = "question", questions = listOf(format)))
        rule.onNodeWithText("Other answer…").performClick()
        rule.onNode(hasSetTextAction()).performTextReplacement("YAML")
        rule.onNodeWithContentDescription("Question 1 of 1").assertDoesNotExist()
        rule.onNodeWithText("Submit").assertIsEnabled().performClick()
        assertEquals(Resolution(answers = mapOf("Choose a format" to "YAML")), sent.single())
    }
}
