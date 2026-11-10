package persona.core.designsystem

import persona.core.contracts.api.ConnectionState
import persona.core.domain.FailureAction
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class OperationFeedbackTest {
    @Test fun knowledgeCodesOverrideServerWordingAndKeepDiagnosticsOutOfFeedback() {
        for ((code, resource, action) in listOf(
            Triple("kb_disabled", R.string.designsystem_knowledge_disabled, FailureAction.None),
            Triple("kb_unavailable", R.string.designsystem_knowledge_unavailable, FailureAction.Retry),
            Triple("kb_read_only", R.string.designsystem_knowledge_read_only, FailureAction.None),
        )) {
            val error = HubFailure(HubError.Server, "Request failed: private diagnostic", status = 503, code = code,
                retryable = true, requestId = "request-1")
            val feedback = operationFeedback(error)
            assertEquals(UiText.Res(resource), feedback.text)
            assertEquals(feedback.text, failureReasonText(error))
            assertEquals(action, feedback.action)
            assertFalse(feedback.offline)
            assertNull(feedback.hubWords)
            assertTrue(failureDetail(error).contains("Request: request-1"))
        }
        val internal = HubFailure(HubError.Server, "private internal exception", status = 503, code = "internal", requestId = "request-1")
        assertEquals(operationFeedback(internal).text, failureReasonText(internal))
        assertFalse(failureDetail(internal).contains("private internal exception"))
        assertTrue(failureDetail(internal).contains("HTTP 503"))
    }

    @Test fun codeOverridesWordingAndOfflineNoticesAreLimitedPerKind() = runTest {
        val input = HubFailure(HubError.Server, "超时失败", code = "input_draft", delivered = "none", retryable = true)
        val changed = HubFailure(HubError.Server, "完全不同的文字", code = "input_draft", delivered = "none", retryable = true)
        assertEquals(operationFeedback(input), operationFeedback(changed))
        assertEquals(FailureAction.OpenTerminal, operationFeedback(input).action)
        assertEquals(FailureAction.None, operationFeedback(HubFailure(HubError.Server, retryable = false)).action)
        var now = 0L
        val gate = FeedbackGate({ ConnectionState.WaitingForNetwork }, { now }, localize = { it.toString() })
        val observed = mutableListOf<String>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { gate.messages.toList(observed) }
        val offline = HubFailure(HubError.NoNetwork)
        gate.failure(offline, true)
        runCurrent()
        gate.failure(input, true)
        runCurrent()
        gate.failure(offline, true)
        gate.failure(changed, true)
        runCurrent()
        assertEquals(2, observed.size)
        now = 4_000
        gate.failure(offline, true)
        runCurrent()
        assertEquals(3, observed.size)
        gate.failure(CancellationException(), true)
        gate.failure(input, false)
        runCurrent()
        assertEquals(3, observed.size)
    }

    /** A 502 node_unreachable is the computer, not the hub (docs/assistant/onboarding.md 4.4); the hub's own words are kept. */
    @Test fun codedRefusalsNameTheStep() {
        // The hub does not word node_unreachable: its error is transport detail, never shown.
        val offline = HubFailure(HubError.Server, "rpc: connection closed", status = 502, code = "node_unreachable", retryable = true)
        val feedback = operationFeedback(offline)
        assertEquals(FailureAction.WakeComputer, feedback.action)
        assertEquals(UiText.Res(R.string.designsystem_feedback_computer_offline), feedback.text)
        assertNull(feedback.hubWords)
        val restarting = operationFeedback(HubFailure(HubError.Server, "assistant offline", status = 503, code = "assistant_offline", retryable = true))
        assertEquals(UiText.Res(R.string.designsystem_feedback_assistant_restarting), restarting.text)
        assertNull(restarting.hubWords)
        // The ones it words (apierr.codeActions) keep its sentence, in the request's language.
        val missing = operationFeedback(HubFailure(HubError.Server, "Claude Code is not installed on Laptop.", status = 409, code = "claude_missing", retryable = false))
        assertEquals("Claude Code is not installed on Laptop.", missing.hubWords)
        assertEquals(UiText.Res(R.string.designsystem_feedback_claude_missing), missing.text)
        assertEquals(ReadinessActionId.WakeComputer, feedback.action.readinessAction())
        val noMachine = operationFeedback(HubFailure(HubError.Server, "HTTP 409", status = 409, code = "no_machine", retryable = false))
        assertEquals(FailureAction.AddComputer, noMachine.action)
        assertEquals(R.string.designsystem_readiness_action_add_computer, noMachine.actionLabelRes)
        assertEquals(UiText.Res(R.string.designsystem_feedback_no_machine), noMachine.text)
        assertNull(noMachine.hubWords)
    }
}
