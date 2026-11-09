package persona.core.hub

import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.JsonCodec
import persona.core.contracts.PlanTask
import kotlinx.serialization.builtins.ListSerializer
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoDecision
import persona.core.contracts.TodoDecisionResponse
import persona.core.contracts.TodoDecisionsResponse
import persona.core.contracts.TodosResponse
import persona.core.contracts.api.AssistantViewAnswer
import persona.core.contracts.api.TasksApi
import persona.core.contracts.generated.ExtensionAssistantHTMLActionInput
import persona.core.contracts.generated.ExtensionAssistantHTMLOpenResult
import persona.core.contracts.generated.ExtensionViewAction
import persona.core.contracts.generated.ExtensionViewActionInput
import persona.core.contracts.generated.ExtensionViewChangedError

/** The unified entry's routes on the active pairing (docs/protocol/protocol.md "Tasks and conversation activity"). */
class HttpTasksApi internal constructor(
    private val request: suspend (path: String, method: String, body: JsonElement?) -> Response,
    override val features: kotlinx.coroutines.flow.StateFlow<Set<String>>? = null,
) : TasksApi {
    /** On [connection], whose hello's features say what its cards may do. */
    constructor(connection: OkHttpHubConnection) : this({ path, method, body ->
        connection.request(path, method, body?.toString()?.toRequestBody("application/json".toMediaType()))
    }, connection.serverFeatures)

    private suspend fun call(path: String, method: String = "GET", body: JsonElement? = null): JsonObject =
        request(path, method, body).use {
            if (!it.isSuccessful) throw HubFailure.from(it)
            val text = it.body?.string().orEmpty()
            if (text.isBlank()) JsonObject(emptyMap()) else JsonCodec.parseToJsonElement(text).jsonObject
        }

    private fun path(vararg segments: String, query: Map<String, String> = emptyMap()): String {
        val url = HttpUrl.Builder().scheme("https").host("example.com").addPathSegments("api/v1")
        segments.forEach { url.addPathSegment(it) }
        query.forEach { (k, v) -> url.addQueryParameter(k, v) }
        val built = url.build()
        return built.encodedPath + (built.encodedQuery?.let { "?$it" } ?: "")
    }

    override suspend fun taskAgent(key: persona.core.contracts.SessionKey): persona.core.contracts.Session? {
        if (features?.value?.contains("sessions_lookup") != true) return null
        val response = call(path("sessions", query = mapOf("summary" to "0", "limit" to "1",
            "machine_id" to key.machineId, "session_id" to key.sessionId)))
        return JsonCodec.decodeFromJsonElement<persona.core.contracts.SessionsResponse>(response)
            .sessions.firstOrNull { it.key == key }
    }

    override suspend fun todos(): List<Todo> =
        JsonCodec.decodeFromJsonElement<TodosResponse>(call(path("todos"))).todos

    override suspend fun createTodo(title: String, notes: String): Todo =
        JsonCodec.decodeFromJsonElement(call(path("todos"), "POST", buildJsonObject {
            put("title", title)
            put("notes", notes)
        })["todo"] ?: error("no todo in the answer"))

    override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo =
        JsonCodec.decodeFromJsonElement(call(path("todos", id), "PATCH", buildJsonObject {
            title?.let { put("title", it) }
            notes?.let { put("notes", it) }
            done?.let { put("done", it) }
        })["todo"] ?: error("no todo in the answer"))

    override suspend fun deleteTodo(id: String) {
        call(path("todos", id), "DELETE")
    }

    override suspend fun todoDecisions(todoId: String): List<TodoDecision> =
        JsonCodec.decodeFromJsonElement<TodoDecisionsResponse>(call(path("todos", todoId, "decisions", query = mapOf("all" to "1"))))
            .decisions.orEmpty()

    override suspend fun addTodoDecision(todoId: String, question: String, choice: String, reason: String, supersedesId: String): TodoDecision =
        JsonCodec.decodeFromJsonElement<TodoDecisionResponse>(call(path("todos", todoId, "decisions"), "POST", buildJsonObject {
            put("question", question.trim())
            put("choice", choice.trim())
            reason.trim().takeIf { it.isNotEmpty() }?.let { put("reason", it) }
            supersedesId.takeIf { it.isNotEmpty() }?.let { put("supersedes_id", it) }
        })).decision

    override suspend fun linkTodoAgent(todoId: String, machineId: String, sessionId: String) {
        call(path("todos", todoId, "agents", machineId, sessionId), "PUT", JsonObject(emptyMap()))
    }

    override suspend fun unlinkTodoAgent(todoId: String, machineId: String, sessionId: String) {
        call(path("todos", todoId, "agents", machineId, sessionId), "DELETE")
    }

    override suspend fun linkTodoSpace(todoId: String, machineId: String, space: String) {
        call(path("todos", todoId, "spaces", machineId, space), "PUT", JsonObject(emptyMap()))
    }

    override suspend fun unlinkTodoSpace(todoId: String, machineId: String, space: String) {
        call(path("todos", todoId, "spaces", machineId, space), "DELETE")
    }

    override suspend fun feed(conversation: String, since: String?): FeedResponse =
        JsonCodec.decodeFromJsonElement(call(path("assistant", "conversations", conversation, "feed",
            query = since?.let { mapOf("since" to it) } ?: emptyMap())))

    override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = startPlan(id, remember, null)

    override suspend fun startPlan(id: String, remember: Boolean, tasks: List<PlanTask>?): TaskPlan =
        plan(call(path("task-plans", id, "start"), "POST", buildJsonObject {
            tasks?.let { put("tasks", JsonCodec.encodeToJsonElement(ListSerializer(PlanTask.serializer()), it)) }
            put("remember", remember)
        }))

    override suspend fun dismissPlan(id: String): TaskPlan = plan(call(path("task-plans", id, "dismiss"), "POST", JsonObject(emptyMap())))

    override suspend fun retryTask(todoId: String): Todo =
        JsonCodec.decodeFromJsonElement(call(path("todos", todoId, "retry"), "POST", JsonObject(emptyMap()))["todo"]
            ?: error("no todo in the answer"))

    override suspend fun removeTask(todoId: String) {
        call(path("todos", todoId), "DELETE")
    }

    override suspend fun route(conversation: String, todoId: String, text: String): FeedItem =
        item(call(path("assistant", "conversations", conversation, "route"), "POST",
            buildJsonObject { put("todo_id", todoId); put("text", text) }))

    override suspend fun today(conversation: String): FeedItem =
        item(call(path("assistant", "conversations", conversation, "today"), "POST", JsonObject(emptyMap())))

    override suspend fun mainConversation(): String? =
        (call(path("assistant"))["main"] as? kotlinx.serialization.json.JsonPrimitive)?.content?.takeIf(String::isNotBlank)

    override suspend fun inboxReply(itemId: String, text: String, card: String?) {
        call(path("inbox", itemId, "reply"), "POST", buildJsonObject { put("text", text); card?.let { put("card", it) } })
    }

    override suspend fun viewAction(conversation: String, key: String, input: ExtensionViewActionInput): AssistantViewAnswer =
        cardCall(path("assistant", "conversations", conversation, "views", key, "actions"),
            JsonCodec.encodeToJsonElement(ExtensionViewActionInput.serializer(), input))

    override suspend fun viewHtmlAction(conversation: String, key: String, input: ExtensionAssistantHTMLActionInput): AssistantViewAnswer =
        cardCall(path("assistant", "conversations", conversation, "views", key, "html-actions"),
            JsonCodec.encodeToJsonElement(ExtensionAssistantHTMLActionInput.serializer(), input))

    /**
     * A card's action: the recorded answer, an HTML page's checked link, or one of the two 409s with what their body
     * carries (the recorded answer; the card's current version and view, `ViewChangedError`).
     */
    private suspend fun cardCall(path: String, body: JsonElement): AssistantViewAnswer {
        val o = try { call(path, "POST", body) } catch (failure: HubFailure) {
            if (failure.status != 409) throw failure
            val error = failure.body?.let { runCatching { JsonCodec.parseToJsonElement(it) as? JsonObject }.getOrNull() }
            return if (failure.code == "already_answered") AssistantViewAnswer.AlreadyAnswered(
                error?.get("action")?.let { runCatching { JsonCodec.decodeFromJsonElement(ExtensionViewAction.serializer(), it) }.getOrNull() })
            else {
                // view_changed, or an older hub's plain conflict for the same version check.
                val changed = error?.let { runCatching { JsonCodec.decodeFromJsonElement(ExtensionViewChangedError.serializer(), it) }.getOrNull() }
                AssistantViewAnswer.ViewChanged(changed?.version?.takeIf { it > 0 }, changed?.view)
            }
        }
        if (o["open"] != null) {
            val open = JsonCodec.decodeFromJsonElement(ExtensionAssistantHTMLOpenResult.serializer(), o)
            if (open.open.isEmpty() || open.version <= 0) error("no checked link in the answer")
            return AssistantViewAnswer.Open(open.open, open.version)
        }
        return AssistantViewAnswer.Recorded(JsonCodec.decodeFromJsonElement(ExtensionViewAction.serializer(),
            o["action"] ?: error("no action in the answer")))
    }

    private fun plan(o: JsonObject): TaskPlan = JsonCodec.decodeFromJsonElement(o["plan"] ?: error("no plan in the answer"))
    private fun item(o: JsonObject): FeedItem = JsonCodec.decodeFromJsonElement(o["item"] ?: error("no item in the answer"))
}
