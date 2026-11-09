package persona.feature.agents

import persona.core.contracts.Session
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.AgentStartRequest
import persona.core.contracts.api.AgentStartResult
import persona.core.contracts.api.MessageSender
import java.util.UUID
import kotlin.coroutines.cancellation.CancellationException

/**
 * A to-do's "Start a new one" start that isn't confirmed yet: the next tap checks it, it never posts a second start.
 * [scope] is the hub it was posted to; another hub doesn't know the id.
 */
internal data class TodoHandOffOperation(val scope: String, val id: String, val messageId: String)

internal sealed interface HandOffOutcome {
    data class Opened(val session: Session) : HandOffOutcome
    /** Nothing was started: the sheet can start again. */
    data class Failed(val message: AgentText) : HandOffOutcome
    /** The start may exist: keep [operation] for the next tap. */
    data class Unconfirmed(val operation: TodoHandOffOperation, val message: AgentText) : HandOffOutcome
}

/**
 * "Hand to Agent" → "Start a new one" (#4703): starts [request], or checks [pending] instead when the last tap on this hub wasn't confirmed, and
 * queues [prompt] as the first message under the operation's own id, so a retry never sends it twice.
 */
internal suspend fun handOffToNewAgent(
    service: AgentCreationService?,
    request: AgentStartRequest,
    prompt: String,
    sender: MessageSender,
    adopt: suspend (Session) -> Unit,
    pending: TodoHandOffOperation? = null,
): HandOffOutcome {
    service ?: return HandOffOutcome.Failed(AgentText(R.string.agents_new_session_unavailable))
    val check = pending?.takeIf { it.scope == service.scope }
    val operation = check ?: TodoHandOffOperation(service.scope, UUID.randomUUID().toString(), UUID.randomUUID().toString())
    val result = try {
        if (check == null) service.start(request, operation.id) else service.operation(operation.id)
    } catch (e: CancellationException) { throw e } catch (_: Exception) { AgentStartResult.Unknown }
    return when (result) {
        is AgentStartResult.Created -> {
            try { adopt(result.session) } catch (e: CancellationException) { throw e } catch (_: Exception) { }
            try { sender.send(result.session.key, prompt, operation.messageId) }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { return HandOffOutcome.Unconfirmed(operation, AgentText(R.string.agents_new_error_first_message)) }
            HandOffOutcome.Opened(result.session)
        }
        is AgentStartResult.Rejected -> HandOffOutcome.Failed(AgentText.raw(result.message.shown))
        AgentStartResult.Pending -> HandOffOutcome.Unconfirmed(operation, AgentText(R.string.agents_new_error_starting))
        AgentStartResult.Unknown -> HandOffOutcome.Unconfirmed(operation, AgentText(R.string.agents_new_error_unknown))
    }
}

/**
 * "Hand to Agent" → "Give to a running one": [link]s the agent to the to-do, then [send]s it the prompt. Null once both went; else
 * what to say in the sheet (#1856), as the new-agent branch does: a link the hub refuses (the to-do done or gone, the hub
 * unreachable) sends nothing, and cancelling (the pairing changed) is not a failure to show.
 */
internal suspend fun handOffToRunningAgent(link: suspend () -> Unit, send: suspend () -> Unit): AgentText? {
    try {
        link()
        send()
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        return AgentText(R.string.agents_todo_hand_off_failed, e.message?.takeIf { it.isNotBlank() } ?: e::class.java.simpleName)
    }
    return null
}
