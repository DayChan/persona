package persona.feature.agents

import persona.core.contracts.CoreText
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

class TodoHandOffStartTest {
    private class FakeCreation(override val scope: String = "hub", var answer: () -> AgentStartResult) : AgentCreationService {
        val starts = mutableListOf<String>()
        val checks = mutableListOf<String>()
        override suspend fun launchers(machineId: String) = emptyList<AgentLauncher>()
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path)
        override suspend fun start(request: AgentStartRequest, operationId: String): AgentStartResult {
            starts += operationId
            return answer()
        }
        override suspend fun operation(operationId: String): AgentStartResult {
            checks += operationId
            return answer()
        }
    }

    private class FakeSender : MessageSender {
        val sent = mutableListOf<Pair<SessionKey, String>>()
        override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            sent += session to clientMessageId
        }
    }

    private val request = AgentStartRequest("machine", "~", "claude", async = true, todoId = "todo")
    private val session = Session("machine", "agent")

    @Test fun rejectedStartIsShownAndAllowsANewOne() = runTest {
        val service = FakeCreation { AgentStartResult.Rejected(CoreText.plain("no launcher")) }
        val outcome = handOffToNewAgent(service, request, "do it", FakeSender(), {})
        assertEquals(HandOffOutcome.Failed(AgentText.raw("no launcher")), outcome)
    }

    @Test fun unconfirmedStartIsCheckedAgainNeverStartedTwice() = runTest {
        val service = FakeCreation { AgentStartResult.Unknown }
        val sender = FakeSender()
        val first = handOffToNewAgent(service, request, "do it", sender, {})
        assertTrue("Unknown must not be dropped: $first", first is HandOffOutcome.Unconfirmed)
        val pending = (first as HandOffOutcome.Unconfirmed).operation
        assertEquals(listOf(pending.id), service.starts)

        service.answer = { AgentStartResult.Pending }
        val second = handOffToNewAgent(service, request, "do it", sender, {}, pending)
        assertEquals(HandOffOutcome.Unconfirmed(pending, AgentText(R.string.agents_new_error_starting)), second)

        service.answer = { AgentStartResult.Created(session) }
        val adopted = mutableListOf<Session>()
        val third = handOffToNewAgent(service, request, "do it", sender, { adopted += it }, pending)
        assertEquals(HandOffOutcome.Opened(session), third)
        assertEquals("Only the first tap may post a start", listOf(pending.id), service.starts)
        assertEquals(listOf(pending.id, pending.id), service.checks)
        assertEquals(listOf(session), adopted)
        assertEquals(listOf(session.key to pending.messageId), sender.sent)
    }

    @Test fun failedRequestOrMissingServiceIsReported() = runTest {
        val service = FakeCreation { error("timeout") }
        val outcome = handOffToNewAgent(service, request, "do it", FakeSender(), {})
        assertTrue("A start whose answer was lost is unconfirmed: $outcome", outcome is HandOffOutcome.Unconfirmed)
        assertEquals(HandOffOutcome.Failed(AgentText(R.string.agents_new_session_unavailable)),
            handOffToNewAgent(null, request, "do it", FakeSender(), {}))
    }

    @Test fun pendingStartFromAnotherHubIsNotCheckedHere() = runTest {
        val service = FakeCreation("new-hub") { AgentStartResult.Created(session) }
        val stale = TodoHandOffOperation("old-hub", "old-op", "old-message")
        val outcome = handOffToNewAgent(service, request, "do it", FakeSender(), {}, stale)
        assertEquals(HandOffOutcome.Opened(session), outcome)
        assertEquals("The old hub's op id means nothing on this hub", emptyList<String>(), service.checks)
        assertEquals(1, service.starts.size)
        assertNotEquals("old-op", service.starts.single())
    }

    // "Give to a running one" (#1856): a refused link (a done to-do: 409, a gone one, an unreachable hub) is said in the sheet and
    // sends nothing; once linked, a send that fails is said too; cancelling is not a failure to show.
    @Test fun aRunningAgentsHandOffSaysWhyItDidNotGo() = runTest {
        val sent = mutableListOf<String>()
        val refused = handOffToRunningAgent(link = { throw java.io.IOException("todo is already completed") }, send = { sent += "prompt" })
        assertEquals(R.string.agents_todo_hand_off_failed, refused?.id)
        assertEquals(listOf("todo is already completed"), refused?.args?.toList())
        assertEquals("nothing is sent after a refused link", emptyList<String>(), sent)

        val unsent = handOffToRunningAgent(link = {}, send = { throw java.io.IOException("Unreachable") })
        assertEquals(listOf("Unreachable"), unsent?.args?.toList())

        assertNull(handOffToRunningAgent(link = {}, send = { sent += "prompt" }))
        assertEquals(listOf("prompt"), sent)

        try {
            handOffToRunningAgent(link = { throw kotlinx.coroutines.CancellationException("pairing changed") }, send = {})
            fail("cancellation is passed on")
        } catch (_: kotlinx.coroutines.CancellationException) {}
    }
}
