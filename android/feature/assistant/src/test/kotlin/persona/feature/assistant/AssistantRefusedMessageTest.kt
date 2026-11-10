package persona.feature.assistant

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.PendingMainConversation
import persona.core.designsystem.PersonaTheme

/** A refused message in the Persona chat's queue says why and offers Retry, whatever the hub's code (#2821). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp")
class AssistantRefusedMessageTest {
    @get:Rule val rule = createComposeRule()

    @Test fun aRefusedMessageSaysWhyAndCanBeRetried() {
        data class Case(val state: DeliveryState, val code: String?, val reason: String?, val retry: Boolean)
        val cases = listOf(
            Case(DeliveryState.Refused, "node_unreachable", "Your computer is offline", true),
            Case(DeliveryState.Refused, "assistant_offline", "The assistant is restarting", true),
            Case(DeliveryState.Refused, "no_machine", "none is connected yet", true),
            // No words of its own: the badge says it was not sent, never the raw code.
            Case(DeliveryState.Refused, "rpc_glitch", null, true),
            Case(DeliveryState.WaitingForNetwork, null, null, false),
        )
        var message by mutableStateOf(OutboxMessage("m", PendingMainConversation, "synthetic hello", DeliveryState.Queued, mainTarget = true))
        val retried = mutableListOf<String>()
        rule.setContent {
            PersonaTheme(reduceMotion = true) { AssistantPendingConversation(listOf(message), null, onRetry = { retried += it.clientMessageId }) }
        }
        fun shown(text: String) = rule.onAllNodes(hasText(text, substring = true)).fetchSemanticsNodes().isNotEmpty()
        for (c in cases) {
            message = message.copy(state = c.state, error = c.code)
            rule.waitForIdle()
            val label = "${c.state} ${c.code}"
            c.reason?.let { assertEquals(label, true, shown(it)) }
            c.code?.let { assertEquals(label, false, shown(it)) }
            assertEquals(label, c.retry, shown("Try again"))
        }
        message = message.copy(state = DeliveryState.Refused, error = "node_unreachable")
        rule.onNodeWithText("Try again").performClick()
        assertEquals(listOf("m"), retried)
    }
}
