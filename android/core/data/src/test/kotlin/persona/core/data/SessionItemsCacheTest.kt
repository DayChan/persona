package persona.core.data

import androidx.room.Room
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.encodeToString
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.HubScope
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class SessionItemsCacheTest {
    private val scope = HubScope("hub", "items-test")
    private lateinit var database: ItemsDatabase

    @Before fun open() {
        database = Room.inMemoryDatabaseBuilder(RuntimeEnvironment.getApplication(), ItemsDatabase::class.java).build()
    }
    @After fun close() = database.close()

    @Test fun ledgerAsksForTheFullScanOnlyWhenUnknownOrOverBudget() {
        val a = SessionKey("m", "a"); val b = SessionKey("m", "b"); val c = SessionKey("m", "c")
        // (session, bytes after its save) -> (full scan, total); budget 30, seeded with a = 10, b = 10.
        val steps = listOf(
            Triple(a, 15L, false to 25L),
            Triple(c, 5L, false to 30L), // at the budget: the scan would evict nothing
            Triple(c, 6L, true to 31L),
            Triple(b, 0L, false to 21L), // emptied: its share goes
            Triple(b, 0L, false to 21L),
        )
        val ledger = ItemsLedger()
        assertEquals(true, ledger.needsScan(a, 1, Long.MAX_VALUE))
        ledger.seed(listOf(BudgetEntry(a, 10, 1), BudgetEntry(b, 10, 2)))
        steps.forEachIndexed { i, (session, bytes, expected) ->
            assertEquals("step $i", expected, ledger.needsScan(session, bytes, 30) to ledger.total)
        }
        ledger.clear()
        assertEquals(true to null, ledger.needsScan(a, 1, Long.MAX_VALUE) to ledger.total)
    }

    @Test fun overBudgetSavesEvictTheOldestUnpinnedSessionsAsTheFullScanDid() = runBlocking {
        val chat = listOf(Item("1#0", 1, role = "user", kind = "text", text = "q".repeat(400)),
            Item("2#0", 2, role = "assistant", kind = "text", text = "a".repeat(400)))
        val bytes = chat.sumOf { JsonCodec.encodeToString(it).toByteArray().size }.toLong()
        val cache = SessionItemsCache(database, scope, budget = bytes * 5 / 2)
        val (a, b, c, d) = listOf("a", "b", "c", "d").map { SessionKey("m", it) }
        suspend fun cached() = database.items().sizes(scope.path).map { it.session to it.bytes }

        cache.retain(a)
        listOf(a, b).forEach { cache.save(it, chat) }
        assertEquals(listOf("a" to bytes, "b" to bytes), cached())
        // Over budget: a is the oldest but on screen, so b goes, with its cursor.
        cache.save(c, chat)
        assertEquals(listOf("a" to bytes, "c" to bytes), cached())
        assertEquals(null, database.items().cursor(scope.path, "m", "b"))
        cache.release(a)
        cache.save(d, chat)
        assertEquals(listOf("c" to bytes, "d" to bytes), cached())
        // A session shrinking under budget evicts nothing.
        cache.save(c, chat.take(1))
        assertEquals(listOf("c", "d"), cached().map { it.first }.sorted())
    }
}
