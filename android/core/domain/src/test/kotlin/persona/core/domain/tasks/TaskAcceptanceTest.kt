package persona.core.domain.tasks

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*

class TaskAcceptanceTest {
    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private class Api(var rows: List<Todo>) : TasksApi {
        var delayed: CompletableDeferred<List<Todo>>? = null
        val accepted = mutableListOf<Pair<String, String>>()
        val patches = mutableListOf<Pair<String, Boolean?>>()
        var patchGate: CompletableDeferred<Unit>? = null
        var patchFailure: Throwable? = null
        var readFailure: Throwable? = null
        override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo {
            patches += id to done
            patchGate?.await()
            patchFailure?.let { throw it }
            val updated = rows.first { it.id == id }.copy(doneAt = "2026-11-01T10:00:00Z", ask = null)
            rows = rows.map { if (it.id == id) updated else it }
            return updated
        }
        override suspend fun todos(): List<Todo> {
            readFailure?.let { throw it }
            return delayed?.let { held -> delayed = null; held.await() } ?: rows
        }
        override suspend fun acceptTask(todoId: String, askedAt: String) { accepted += todoId to askedAt }
        override suspend fun feed(conversation: String, since: String?) = FeedResponse()
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private val decision = Todo("task", origin = "main", supervision = Supervision(state = "needs_user"),
        ask = TodoAsk("decide", "Choose the release scope", "Defer the optional change", "2026-11-01T09:00:00Z"))

    @Test fun finishOnlyPatchesDoneAndWaitsForConfirmationWhileBlockingBothActions() = runTest {
        val api = Api(listOf(decision)).apply { patchGate = CompletableDeferred() }
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        store.setRoute("main", decision.id)
        store.finishTaskNow(decision, { throw it }) { fail("unchanged") }
        store.finishTaskNow(decision, { throw it }) { fail("duplicate") }
        store.acceptTask(decision, { throw it }) { fail("duplicate") }
        runCurrent()
        assertEquals(listOf(decision.id to true), api.patches)
        assertTrue(api.accepted.isEmpty())
        assertEquals(decision, store.todos.value.single())
        assertTrue("accept:${decision.id}" in store.busy.value)
        api.patchGate!!.complete(Unit); runCurrent()
        assertTrue(store.todos.value.single().isDone)
        assertNull(store.todos.value.single().doneBy)
        assertNull(store.todos.value.single().ask)
        assertTrue(store.busy.value.isEmpty())
        assertNull(store.routes.value["main"])
    }

    @Test fun failedFinishRetainsCardAndUnrelatedTasksAndCanRetry() = runTest {
        val other = Todo("other", "An independent task")
        val api = Api(listOf(decision, other)).apply { patchFailure = IllegalStateException("synthetic failure") }
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val errors = mutableListOf<Throwable>()
        store.finishTaskNow(decision, { errors += it }) { fail("unchanged") }; runCurrent()
        assertEquals(1, errors.size)
        assertEquals(listOf(decision, other), store.todos.value)
        assertTrue(store.busy.value.isEmpty())
        api.patchFailure = null
        store.finishTaskNow(decision, { throw it }) { fail("unchanged") }; runCurrent()
        assertTrue(store.todos.value.first().isDone)
        assertEquals(other, store.todos.value.last())
        assertEquals(2, api.patches.size)
    }

    @Test fun finishedTaskCannotResurrectFromPreWriteReadButCanReopenLater() = runTest {
        val api = Api(listOf(decision))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val held = CompletableDeferred<List<Todo>>()
        api.delayed = held
        store.reloadTodos(); runCurrent()
        store.finishTaskNow(decision, { throw it }) { fail("unchanged") }; runCurrent()
        val confirmed = store.todos.value.single()
        assertTrue(confirmed.isDone)
        held.complete(listOf(decision)); runCurrent()
        assertEquals(confirmed, store.todos.value.single())
        val reopened = decision.copy(ask = decision.ask!!.copy(askedAt = "2026-11-02T09:00:00Z"))
        api.rows = listOf(reopened)
        store.reloadTodos(); runCurrent()
        assertEquals(reopened, store.todos.value.single())
    }

    @Test fun aChangedDecisionIsNotCompletedAndMissingRecommendationStillAllowsFinish() = runTest {
        val latest = decision.copy(ask = decision.ask!!.copy(recommend = null, askedAt = "2026-11-02T09:00:00Z"))
        val api = Api(listOf(latest))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        var changed = 0
        store.finishTaskNow(decision, { throw it }) { changed++ }; runCurrent()
        assertEquals(1, changed)
        assertTrue(api.patches.isEmpty())
        store.finishTaskNow(latest, { throw it }) { fail("unchanged") }; runCurrent()
        assertTrue(store.todos.value.single().isDone)
    }

    @Test fun completionPersistsOnlyConfirmedRowsAndAFailedRefreshDoesNotUndoIt() = runTest {
        val other = Todo("other", "Independent task")
        val api = Api(listOf(decision, other))
        val otherGate = CompletableDeferred<Todo>()
        val routed = object : TasksApi by api {
            override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?) =
                if (id == other.id) otherGate.await() else api.patchTodo(id, title, notes, done)
        }
        var cached = listOf(decision, other)
        val cache = object : TodoCache {
            override suspend fun read() = cached
            override suspend fun write(todos: List<Todo>) { cached = todos }
        }
        val store = TasksStore(routed, Connection(), backgroundScope, cache)
        store.reloadTodos(); runCurrent()
        backgroundScope.launch { store.setTodoDone(other, true) }
        runCurrent()
        assertTrue(store.todos.value.last().isDone) // Unrelated completion has not been confirmed yet.
        api.readFailure = IllegalStateException("synthetic refresh failure")
        store.finishTaskNow(decision, { throw it }) { fail("unchanged") }; runCurrent()
        assertTrue(store.todos.value.first().isDone)
        assertTrue(cached.first().isDone)
        assertNull(cached.first().ask)
        assertEquals(other, cached.last())
        assertTrue(store.busy.value.isEmpty())
    }

    @Test fun unrelatedAcceptanceAndCompletedOrCancelledTasksCannotUseDirectFinish() = runTest {
        val api = Api(listOf(decision))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        for (todo in listOf(decision.copy(ask = decision.ask!!.copy(outcome = "done")),
            decision.copy(doneAt = "2026-11-01T10:00:00Z"), decision.copy(cancelledAt = "2026-11-01T10:00:00Z"))) {
            store.finishTaskNow(todo, { throw it }) { fail("not submitted") }
        }
        runCurrent()
        assertTrue(api.patches.isEmpty())
    }

    @Test fun acceptedRoundCannotResurrectFromDelayedReadButANewAskSurvives() = runTest {
        val original = Todo("task", origin = "main", ask = TodoAsk(verdict = "Check the deployed page", askedAt = "2026-11-01T09:00:00Z"))
        val api = Api(listOf(original))
        val store = TasksStore(api, Connection(), backgroundScope)
        store.reloadTodos(); runCurrent()
        val held = CompletableDeferred<List<Todo>>()
        api.delayed = held
        store.reloadTodos(); runCurrent()
        var changed = 0
        store.acceptTask(original, { throw it }) { changed++ }; runCurrent()
        assertEquals(listOf(original.id to original.ask!!.askedAt), api.accepted)
        assertNull(store.todos.value.single().ask)
        held.complete(listOf(original)); runCurrent()
        assertNull(store.todos.value.single().ask)
        val latest = original.copy(ask = original.ask!!.copy(askedAt = "2026-11-02T09:00:00Z", verdict = "Check the new release"))
        api.rows = listOf(latest)
        store.reloadTodos(); runCurrent()
        assertEquals(latest.ask, store.todos.value.single().ask)
        store.acceptTask(original, { throw it }) { changed++ }; runCurrent()
        assertEquals(1, changed)
        assertEquals(1, api.accepted.size)
    }
}
