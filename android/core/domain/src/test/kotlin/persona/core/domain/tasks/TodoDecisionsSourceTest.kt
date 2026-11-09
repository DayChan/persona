package persona.core.domain.tasks

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoDecision
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi

/**
 * The to-do page's decisions come from the hub through [TasksStore] (#1853): a hub from before decisions (its router's 404) has
 * none to show, while any other failure is said; a recorded one goes to the hub as typed.
 */
class TodoDecisionsSourceTest {
    private class Api : TasksApi {
        var failure: Exception? = null
        val added = mutableListOf<List<String>>()
        override suspend fun todos(): List<Todo> = emptyList()
        override suspend fun todoDecisions(todoId: String): List<TodoDecision> {
            failure?.let { throw it }
            return listOf(TodoDecision("dec_1", todoId, "Merge now?", "Wait"))
        }
        override suspend fun addTodoDecision(todoId: String, question: String, choice: String, reason: String, supersedesId: String): TodoDecision {
            added += listOf(todoId, question, choice, reason, supersedesId)
            return TodoDecision("dec_2", todoId, question, choice, reason)
        }
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    /** The hub's error as the HTTP client throws it (HubFailure): read by its getters. */
    private class HubError(val status: Int, val code: String?, message: String) : Exception(message)

    @Test fun anOlderHubHasNoDecisionsAndOtherFailuresAreSaid() = runBlocking {
        val api = Api()
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        assertEquals(listOf("dec_1"), store.decisions("todo_ask_1")?.map { it.id })

        api.failure = HubError(404, null, "404 page not found")
        assertNull("no decisions route", store.decisions("todo_ask_1"))

        api.failure = HubError(404, "not_found", "todo not found")
        try {
            store.decisions("todo_gone")
            fail("a to-do that is gone is not an older hub")
        } catch (e: HubError) {
            assertEquals("todo not found", e.message)
        }
        api.failure = HubError(503, null, "decisions need the hub's database")
        try {
            store.decisions("todo_ask_1")
            fail("an unavailable store is said")
        } catch (e: HubError) {
            assertTrue(e.message.orEmpty().contains("database"))
        }
    }

    @Test fun aRecordedDecisionGoesToTheHub() = runBlocking {
        val api = Api()
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        val saved = store.addDecision("todo_ask_1", "Merge now?", "Merge", "CI is green", "")
        assertEquals("dec_2", saved.id)
        assertEquals(listOf(listOf("todo_ask_1", "Merge now?", "Merge", "CI is green", "")), api.added)
    }
}
