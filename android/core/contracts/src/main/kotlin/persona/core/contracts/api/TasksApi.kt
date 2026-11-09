package persona.core.contracts.api

import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo

/** Tasks dispatched from an assistant conversation (docs/assistant/unified-entry.md; docs/protocol/protocol.md "Tasks and conversation activity"). */
interface TasksApi {
    /** Exact metadata for a bound agent, independent of the Agents list filter and page. */
    suspend fun taskAgent(key: persona.core.contracts.SessionKey): persona.core.contracts.Session? = null
    suspend fun todos(): List<Todo>
    suspend fun createTodo(title: String, notes: String = ""): Todo = error("createTodo not implemented")
    suspend fun patchTodo(id: String, title: String? = null, notes: String? = null, done: Boolean? = null): Todo = error("patchTodo not implemented")
    suspend fun deleteTodo(id: String) = Unit
    suspend fun linkTodoAgent(todoId: String, machineId: String, sessionId: String) = Unit
    suspend fun unlinkTodoAgent(todoId: String, machineId: String, sessionId: String) = Unit
    /** "Attach to to-do" a whole project (PUT /api/v1/todos/{id}/spaces/{mid}/{space}): its agents follow the todo. */
    suspend fun linkTodoSpace(todoId: String, machineId: String, space: String): Unit = error("linkTodoSpace not implemented")
    /** "Remove from to-do": the project and the agents it brought leave the todo. */
    suspend fun unlinkTodoSpace(todoId: String, machineId: String, space: String): Unit = error("unlinkTodoSpace not implemented")
    /** A to-do's decisions (#1618), oldest first, the revised ones included (`GET /api/v1/todos/{id}/decisions?all=1`). */
    suspend fun todoDecisions(todoId: String): List<persona.core.contracts.TodoDecision> = error("todoDecisions not implemented")
    /** Records a decision by hand; with [supersedesId], one that revises that earlier decision. A blank reason is not sent. */
    suspend fun addTodoDecision(todoId: String, question: String, choice: String, reason: String = "", supersedesId: String = ""):
        persona.core.contracts.TodoDecision = error("addTodoDecision not implemented")

    /** A conversation's feed after [since] (an ISO time; null: all of it), with the plans its `plan` items name. */
    suspend fun feed(conversation: String, since: String?): FeedResponse
    suspend fun startPlan(id: String, remember: Boolean): TaskPlan
    /** "Start" after "Edit": [tasks] replaces the plan's tasks by index with what the user changed (null: as planned). */
    suspend fun startPlan(id: String, remember: Boolean, tasks: List<persona.core.contracts.PlanTask>?): TaskPlan = startPlan(id, remember)
    suspend fun dismissPlan(id: String): TaskPlan
    /**
     * "Retry" on a task whose agent did not start (#1396): the hub starts the same task again in the same todo and answers
     * it reopened, without its error. Refused (409) when the machine is offline or the task did not fail to start.
     */
    suspend fun retryTask(todoId: String): Todo
    /** Removes a task: its todo goes; its agents and their chats stay. */
    suspend fun removeTask(todoId: String)
    /** Sends text straight to a task's agent; the answer is the conversation's new `routed` item. */
    suspend fun route(conversation: String, todoId: String, text: String): FeedItem
    /** The conversation's "Today" card for the user's day (made on the first call of the day). */
    suspend fun today(conversation: String): FeedItem
    /** The main assistant conversation's id (GET /api/v1/assistant `main`), null when there is none. */
    suspend fun mainConversation(): String?
    /**
     * "Send" on a message card: the draft goes out through the inbox, which files the item as done and records it on
     * the card ([card], the card's id: sent once per card however many taps or devices).
     */
    suspend fun inboxReply(itemId: String, text: String, card: String? = null)

    /**
     * The features the hub these requests go to said in its last hello (`assistant_views`, `assistant_html_bridge`);
     * null when this API cannot tell, which counts as none: a card answers only on a hub that says it can.
     */
    val features: kotlinx.coroutines.flow.StateFlow<Set<String>>? get() = null

    /** A card's action (docs/assistant/assistant-views.md "Persisted submissions"): [key] is the card's, sent as one escaped path segment. */
    suspend fun viewAction(conversation: String, key: String, input: persona.core.contracts.generated.ExtensionViewActionInput): AssistantViewAnswer =
        error("viewAction not implemented")

    /** What an assistant's HTML page asked for through its bridge, after the user confirmed it. */
    suspend fun viewHtmlAction(conversation: String, key: String, input: persona.core.contracts.generated.ExtensionAssistantHTMLActionInput): AssistantViewAnswer =
        error("viewHtmlAction not implemented")
}
