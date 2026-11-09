package persona.core.contracts

import kotlinx.serialization.KeepGeneratedSerializer
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * One call made on a to-do (#1618, docs/assistant/todos.md "Decision records"): written by hand, the user's answer to a
 * question card of an agent on it, or noted when its task finished. The current ones go along when the to-do is handed
 * to an agent; a revised one stays, with [supersededBy] set, for the history (iOS TodoDecision).
 */
@KeepGeneratedSerializer
@Serializable(with = TodoDecisionSerializer::class)
data class TodoDecision(
    val id: String,
    @SerialName("todo_id") val todoId: String = "",
    val question: String = "",
    val choice: String = "",
    val reason: String = "",
    /** manual, card, outcome, agent or thread; any other value reads as written by hand. */
    val source: String = "",
    @SerialName("source_machine") val sourceMachine: String = "",
    @SerialName("source_session") val sourceSession: String = "",
    /** The earlier decision this one revises. */
    @SerialName("supersedes_id") val supersedesId: String = "",
    /** The newest decision that revises this one; empty while it is current. */
    @SerialName("superseded_by") val supersededBy: String = "",
    @WireTime @SerialName("created_at") val createdAt: String? = null,
) {
    val isCurrent: Boolean get() = supersededBy.isEmpty()
    val created get() = parseTime(createdAt)
}

@KeepGeneratedSerializer
@Serializable(with = TodoDecisionsResponseSerializer::class)
data class TodoDecisionsResponse(val decisions: List<TodoDecision>? = null)

@KeepGeneratedSerializer
@Serializable(with = TodoDecisionResponseSerializer::class)
data class TodoDecisionResponse(val decision: TodoDecision)

object TodoDecisionSerializer : WireSerializer<TodoDecision>(TodoDecision.generatedSerializer())
object TodoDecisionsResponseSerializer : WireSerializer<TodoDecisionsResponse>(TodoDecisionsResponse.generatedSerializer())
object TodoDecisionResponseSerializer : WireSerializer<TodoDecisionResponse>(TodoDecisionResponse.generatedSerializer())
