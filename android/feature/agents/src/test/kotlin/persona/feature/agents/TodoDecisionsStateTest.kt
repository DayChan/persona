package persona.feature.agents

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.async
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.TodoDecision
import persona.core.domain.tasks.TodoDecisionsSource

/** The Decisions section's state (#1853, iOS TodoDecisionsModel): what it reads, shows and records for a to-do. */
class TodoDecisionsStateTest {
    private class Source : TodoDecisionsSource {
        var list: List<TodoDecision>? = emptyList()
        var failure: Exception? = null
        var gate: CompletableDeferred<Unit>? = null
        val added = mutableListOf<List<String>>()
        override suspend fun decisions(todoId: String): List<TodoDecision>? {
            val read = list // what the hub had when it was asked
            gate?.await()
            failure?.let { throw it }
            return read
        }
        override suspend fun addDecision(todoId: String, question: String, choice: String, reason: String, supersedesId: String): TodoDecision {
            failure?.let { throw it }
            added += listOf(todoId, question, choice, reason, supersedesId)
            val d = TodoDecision("dec_${added.size + 10}", todoId, question, choice, reason, supersedesId = supersedesId)
            list = list.orEmpty().map { if (it.id == supersedesId) it.copy(supersededBy = d.id) else it } + d
            return d
        }
    }

    private val wait = TodoDecision("dec_1", "todo_ask_1", "Merge the release PR now?", "Wait", supersededBy = "dec_2")
    private val merge = TodoDecision("dec_2", "todo_ask_1", "Merge the release PR now?", "Merge", "CI is green", supersedesId = "dec_1")

    @Test fun theCurrentOnesShowAndTheRevisedOnesOnRequest() = runBlocking {
        val source = Source().apply { list = listOf(wait, merge) }
        val state = TodoDecisionsState(source)
        state.select("todo_ask_1")
        assertNull("nothing until the first read", state.all)
        state.load()
        assertEquals(listOf("dec_2"), state.current.map { it.id })
        assertEquals(1, state.revisedCount)
        assertEquals(listOf("dec_2"), state.shown.map { it.id })
        state.showAll = true
        assertEquals(listOf("dec_1", "dec_2"), state.shown.map { it.id })
        // Another to-do starts over.
        state.select("todo_2")
        assertNull(state.all)
        assertFalse(state.showAll)
    }

    @Test fun anOlderHubHidesTheSectionAndAFailureCanBeRetried() = runBlocking {
        val source = Source().apply { list = null }
        val state = TodoDecisionsState(source)
        state.select("todo_ask_1")
        state.load()
        assertTrue("no decisions route", state.unsupported)

        val failing = Source().apply { failure = IllegalStateException("hub unreachable") }
        val again = TodoDecisionsState(failing)
        again.select("todo_ask_1")
        again.load()
        assertEquals("hub unreachable", again.failed)
        assertFalse(again.unsupported)
        failing.failure = null
        again.load()
        assertNull(again.failed)
        assertEquals(emptyList<TodoDecision>(), again.all)

        assertTrue("no source: nothing to draw", TodoDecisionsState(null).unsupported)
    }

    @Test fun onlyTheNewestReadLands() = runBlocking {
        val source = Source().apply { list = listOf(wait) }
        val state = TodoDecisionsState(source)
        state.select("todo_ask_1")
        val gate = CompletableDeferred<Unit>()
        source.gate = gate
        val slow = async(start = CoroutineStart.UNDISPATCHED) { state.load() } // asked first, with one decision
        source.gate = null
        source.list = listOf(wait, merge)
        state.load()
        assertEquals(2, state.all?.size)
        gate.complete(Unit)
        slow.await() // answers last, with the older list: it does not land
        assertEquals(2, state.all?.size)
    }

    @Test fun recordingSendsWhatWasTypedAndReadsAgain() = runBlocking {
        val source = Source().apply { list = listOf(merge.copy(supersedesId = "")) }
        val state = TodoDecisionsState(source)
        state.select("todo_ask_1")
        state.load()
        var error: Exception? = null
        assertFalse("a blank choice is not sent", state.add("Merge now?", "  ", "", null) { error = it })
        assertTrue(state.add("  Ship on Friday? ", " Yes ", " the release train ", null) { error = it })
        assertEquals(listOf("todo_ask_1", "Ship on Friday?", "Yes", "the release train", ""), source.added.last())
        assertTrue(state.add("Merge the release PR now?", "Wait", "", state.decision("dec_2")) { error = it })
        assertEquals("dec_2", source.added.last()[4])
        assertEquals(listOf("Yes", "Wait"), state.current.map { it.choice })
        assertNull(error)

        source.failure = IllegalStateException("question and choice cannot be empty")
        assertFalse(state.add("Merge now?", "Merge", "", null) { error = it })
        assertEquals("question and choice cannot be empty", error?.message)
    }
}
