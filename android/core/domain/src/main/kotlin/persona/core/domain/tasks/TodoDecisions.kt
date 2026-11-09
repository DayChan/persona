package persona.core.domain.tasks

import persona.core.contracts.TodoDecision

/**
 * Where a to-do's decisions are read from and recorded to (#1618, docs/assistant/todos.md "Decision records"): [TasksStore] on the
 * paired hub, a fake in tests.
 */
interface TodoDecisionsSource {
    /** Every decision of [todoId], oldest first, the revised ones included; null when the hub has no decisions route (an older hub). */
    suspend fun decisions(todoId: String): List<TodoDecision>?

    /** Records one by hand; with [supersedesId], one that revises that earlier decision. */
    suspend fun addDecision(todoId: String, question: String, choice: String, reason: String, supersedesId: String): TodoDecision
}
