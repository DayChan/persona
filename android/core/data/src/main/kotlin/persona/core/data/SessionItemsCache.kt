package persona.core.data

import androidx.room.withTransaction
import persona.core.contracts.HubScope
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.domain.ContinuityReducer
import persona.core.domain.lines.chatTurns
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.encodeToString

class SessionItemsCache(
    private val database: ItemsDatabase,
    private val scope: HubScope,
    private val budget: Long = CacheBudgets().items,
) : ChatItemsCache {
    init { require(budget >= 0) }
    private val active = mutableMapOf<SessionKey, Int>()
    private val ledger = ItemsLedger()
    /** Held around every transaction that reads or changes [ledger], so a failed one drops it before the next uses it. */
    private val ledgerLock = Mutex()
    internal var onRelease: () -> Unit = {}
    internal fun activeSessions(): Set<SessionKey> = synchronized(active) { active.keys.toSet() }

    override suspend fun retain(session: SessionKey) = database.withTransaction {
        synchronized(active) { active[session] = (active[session] ?: 0) + 1 }
        database.items().touch(scope.path, session.machineId, session.sessionId, System.currentTimeMillis())
    }

    override suspend fun release(session: SessionKey) {
        // A group's data can close while a page still watches one of its chats (the group merged into another or was
        // disconnected, docs/android/android.md "Multiple hubs"): the cache is gone with it, there is nothing left to release.
        if (!database.isOpen) return
        try {
            ledgerTransaction {
                synchronized(active) {
                    val count = (active[session] ?: 1) - 1
                    if (count == 0) active.remove(session) else active[session] = count
                }
                trim()
            }
        } catch (closed: IllegalStateException) {
            if (database.isOpen) throw closed
            return
        }
        onRelease()
    }

    /** The full scan: every cached session's bytes, evicting over budget; leaves [ledger] holding what stays. */
    private suspend fun trim() {
        val dao = database.items()
        val entries = dao.sizes(scope.path).map { BudgetEntry(SessionKey(it.machine, it.session), it.bytes, it.accessed) }
        val pinned = activeSessions()
        val evicted = evictionCandidates(entries, budget) { it in pinned }.toSet()
        evicted.forEach {
            dao.clear(scope.path, it.machineId, it.sessionId)
            dao.clearCursor(scope.path, it.machineId, it.sessionId)
        }
        ledger.seed(entries.filter { it.key !in evicted })
    }

    /** A transaction that uses [ledger]; when it fails, nothing it recorded is trusted and the next save scans again. */
    private suspend fun <T> ledgerTransaction(block: suspend () -> T): T = ledgerLock.withLock {
        try { database.withTransaction { block() } }
        catch (failure: Throwable) { ledger.clear(); throw failure }
    }

    fun observe(session: SessionKey) = database.items().observe(scope.path, session.machineId, session.sessionId).map { rows ->
        rows.map { JsonCodec.decodeFromString<Item>(it.json) }
    }

    override suspend fun load(session: SessionKey): CachedChat = ledgerTransaction {
        database.items().touch(scope.path, session.machineId, session.sessionId, System.currentTimeMillis())
        trim()
        val cursor = database.items().cursor(scope.path, session.machineId, session.sessionId)
        CachedChat(observe(session).first(), cursor?.history, cursor?.covered ?: 0, cursor?.has_more)
    }

    /** Pass the reducer's authoritative merged view, not an isolated incremental page. */
    suspend fun save(session: SessionKey, reducer: ContinuityReducer) =
        save(session, CachedChat(reducer.items, reducer.history, reducer.covered, reducer.hasMore))

    suspend fun save(session: SessionKey, items: List<Item>) = save(session, CachedChat(items))

    override suspend fun save(session: SessionKey, snapshot: CachedChat) {
        val ordered = snapshot.items.distinctBy { it.id }.sortedWith(compareBy<Item> { it.seq }.thenBy { part(it) })
        val turns = chatTurns(ordered.map { it.lineItem() })
        val first = turns.takeLast(2).firstOrNull()?.users?.firstOrNull()?.item
        val start = first?.let { item -> ordered.indexOfFirst { it.id == item.id }.coerceAtLeast(0) } ?: 0
        val rows = ordered.drop(start).map { item ->
            // Inline originals belong to the display-sized image cache, not the items database.
            val cached = item.image?.let { item.copy(image = it.copy(data = "", lazy = true)) } ?: item
            ItemRow(scope.path, session.machineId, session.sessionId, item.seq, part(item), item.id, JsonCodec.encodeToString(cached))
        }
        ledgerTransaction {
            val dao = database.items()
            dao.clear(scope.path, session.machineId, session.sessionId)
            dao.insert(rows)
            if (rows.isEmpty()) dao.clearCursor(scope.path, session.machineId, session.sessionId)
            else dao.putCursor(SessionCursorRow(scope.path, session.machineId, session.sessionId,
                snapshot.history, snapshot.covered, snapshot.hasMore == true || start > 0,
                System.currentTimeMillis()))
            // Only this session's rows are read; the whole cache only when the total is over budget (where eviction
            // starts) or not known yet. Under budget the full scan evicted nothing either.
            if (ledger.needsScan(session, dao.size(scope.path, session.machineId, session.sessionId), budget)) trim()
        }
    }

    private fun part(item: Item): Int = item.id.substringAfterLast('#').toIntOrNull() ?: 0
}

/**
 * The JSON bytes each cached session of one scope holds (what [ItemsDao.sizes] sums), kept in step with every write of
 * its [SessionItemsCache]: the only writer of that scope's items database. Unknown until a full scan [seed]s it and
 * again after [clear]. Access times are not kept: eviction always reads them from the database.
 */
internal class ItemsLedger {
    private var sessions: MutableMap<SessionKey, Long>? = null

    /** Every session's bytes together; null while unknown. */
    var total: Long? = null
        private set

    fun seed(entries: List<BudgetEntry<SessionKey>>) {
        sessions = entries.filter { it.bytes > 0 }.associateTo(HashMap()) { it.key to it.bytes }
        total = entries.sumOf { it.bytes }
    }

    /**
     * [session] was just written and now holds [bytes]. Whether the full scan has to run: the total is unknown, or
     * it is over [budget], where eviction starts. At or under it the scan would evict nothing.
     */
    fun needsScan(session: SessionKey, bytes: Long, budget: Long): Boolean {
        val known = sessions ?: return true
        val before = if (bytes > 0) known.put(session, bytes) else known.remove(session)
        val now = total!! + bytes - (before ?: 0)
        total = now
        return now > budget
    }

    fun clear() { sessions = null; total = null }
}
