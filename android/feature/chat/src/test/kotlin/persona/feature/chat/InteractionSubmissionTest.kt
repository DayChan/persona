package persona.feature.chat

import persona.core.contracts.Interaction
import persona.core.contracts.api.InteractionGoneException
import persona.core.contracts.api.InteractionPromptChangedException
import persona.core.contracts.Resolution
import persona.core.contracts.api.HubCodedFailure
import persona.core.contracts.api.ResolveOutcome
import persona.core.domain.answer.AnswerAction
import persona.core.domain.answer.AnswerBuilder
import persona.core.domain.answer.AnswerDraft
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

class InteractionSubmissionTest {
    @Test fun terminalOutcomesWithdrawStaleCardsAndOnlyLostReceiptsStayUncertain() = runTest {
        assertEquals(InteractionSubmission.Answered, submitInteraction { ResolveOutcome() }.submission)
        val gone = submitInteraction { throw InteractionGoneException() }
        assertEquals(R.string.chat_interaction_resolved_elsewhere, gone.submission.notice)
        assertTrue(gone.withdraw)
        val expired = submitInteraction { throw InteractionGoneException("expired") }
        assertEquals(InteractionReply(InteractionSubmission.ResolvedElsewhere, endedStatus = "expired"), expired)
        val already = submitInteraction { ResolveOutcome(already = true, resolution = Resolution(allow = false)) }
        assertEquals(InteractionReply(InteractionSubmission.AlreadyAnswered, effective = Resolution(allow = false)), already)
        assertTrue(already.withdraw)
        val changed = submitInteraction { throw InteractionPromptChangedException() }
        assertEquals(InteractionSubmission.PromptChanged, changed.submission)
        assertTrue(changed.withdraw)
        assertFalse(submitInteraction { throw IllegalStateException() }.withdraw)
        try {
            submitInteraction { throw CancellationException() }
            fail("Cancellation must propagate")
        } catch (_: CancellationException) { }
    }

    // As iOS AppModel.resolve: answered succeeds, a changed dialog nudges, gone plays nothing, unconfirmed is an error.
    @Test fun eachOutcomePlaysWhatIosPlays() {
        assertEquals(mapOf(
            InteractionSubmission.Answered to persona.core.designsystem.Haptic.Success,
            // iOS plays success for a card the hub says was answered already, too.
            InteractionSubmission.AlreadyAnswered to persona.core.designsystem.Haptic.Success,
            InteractionSubmission.ResolvedElsewhere to null,
            InteractionSubmission.PromptChanged to persona.core.designsystem.Haptic.Attention,
            InteractionSubmission.Refused to persona.core.designsystem.Haptic.Error,
            InteractionSubmission.Unknown to persona.core.designsystem.Haptic.Error,
        ), InteractionSubmission.entries.associateWith { it.haptic })
    }

    /** What the hub's transport throws for an answer it refused: the hub's words, its status and its code. */
    private class HubAnswer(override val status: Int?, override val code: String? = null, message: String = "HTTP $status") :
        java.io.IOException(message), HubCodedFailure

    // #1881: a refusal the hub answered leaves the card answerable with its words; only a lost receipt stays unconfirmed.
    @Test fun aRefusalTheHubAnsweredIsNotUnconfirmed() = runTest {
        val codex = submitInteraction { throw HubAnswer(409, "input_draft", "The terminal has a draft in its input box") }
        assertEquals(InteractionReply(InteractionSubmission.Refused, reason = "The terminal has a draft in its input box"), codex)
        assertFalse(codex.withdraw)
        assertEquals(InteractionReply(InteractionSubmission.Refused, reason = "Only the card's own choices"),
            submitInteraction { throw HubAnswer(403, "answer_only", "Only the card's own choices") })
        // the hub answered 503 itself before passing it on: nothing was done, after the transport's retries
        assertEquals(InteractionReply(InteractionSubmission.Refused, reason = "The assistant isn't connected"),
            submitInteraction { throw HubAnswer(503, "assistant_offline", "The assistant isn't connected") })
        // a status alone gives no words to show
        assertEquals(InteractionReply(InteractionSubmission.Refused), submitInteraction { throw HubAnswer(400) })
        // may have gone through: a proxy's 503, a 500, a 502, no answer at all, wrapped or not
        for (lost in listOf(HubAnswer(503), HubAnswer(500, "internal", "boom"), HubAnswer(502, "node_unreachable", "gone"),
                java.io.IOException("reset"), IllegalStateException(HubAnswer(504))))
            assertEquals(InteractionSubmission.Unknown, submitInteraction { throw lost }.submission)
        assertEquals(InteractionSubmission.Refused, submitInteraction { throw IllegalStateException(HubAnswer(409, "x", "no")) }.submission)
    }

    @Test fun passwordPreservesWhitespaceOnTheWireButNeverEntersSavedDrafts() {
        val request = Interaction("request", kind = "screen", secret = true)
        val draft = AnswerDraft(text = "  sample input  ", other = mapOf("question" to "sample input"))
        val result = AnswerBuilder.build(request, AnswerAction.TerminalText, draft)
        assertTrue(result.allowed)
        assertEquals(draft.text, result.resolution?.text)
        assertEquals(listOf("enter"), result.resolution?.keys)
        assertEquals(AnswerDraft(), result.draft.forStorage(request))
        assertEquals(draft, draft.forStorage(request.copy(secret = false)))
    }
}
