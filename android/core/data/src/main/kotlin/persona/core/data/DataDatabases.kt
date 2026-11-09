package persona.core.data

import androidx.room.AutoMigration
import androidx.room.ColumnInfo
import androidx.room.Dao
import androidx.room.Database
import androidx.room.Entity
import androidx.room.Index
import androidx.room.Insert
import androidx.room.OnConflictStrategy
import androidx.room.PrimaryKey
import androidx.room.Query
import androidx.room.RoomDatabase
import androidx.room.migration.Migration
import androidx.sqlite.db.SupportSQLiteDatabase
import androidx.room.withTransaction
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.DraftSnapshot
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map

@Entity(tableName = "outbox", indices = [Index(value = ["scope", "client_msg_id"], unique = true)])
data class OutboxRow(
    @PrimaryKey(autoGenerate = true) val position: Long = 0,
    val scope: String,
    val machine: String,
    val session: String,
    val client_msg_id: String,
    val text: String,
    val clear_draft: String?,
    val state: String,
    val error: String?,
    val created_at: Long,
    @ColumnInfo(defaultValue = "0") val main_target: Boolean = false,
    /** Null: the next attempt goes under client_msg_id. */
    val op_id: String? = null,
)

@Entity(tableName = "drafts", primaryKeys = ["scope", "machine", "session"])
data class DraftRow(
    val scope: String, val machine: String, val session: String, val text: String, val version: Long,
    @ColumnInfo(defaultValue = "'[]'") val attachments: String = "[]",
    val session_version: String? = null,
)

@Entity(tableName = "main_conversation")
data class MainConversationRow(
    @PrimaryKey val scope: String,
    val machine: String?,
    val session: String?,
)

@Dao
interface PersistentDao {
    @Query("SELECT * FROM outbox WHERE scope = :scope ORDER BY position")
    fun observeOutbox(scope: String): Flow<List<OutboxRow>>

    @Query("SELECT * FROM outbox WHERE scope = :scope AND client_msg_id = :id")
    suspend fun message(scope: String, id: String): OutboxRow?

    @Insert(onConflict = OnConflictStrategy.ABORT)
    suspend fun insertMessage(row: OutboxRow)

    @Query("UPDATE outbox SET state = :state, error = :error WHERE scope = :scope AND client_msg_id = :id AND state IN (:from)")
    suspend fun transition(scope: String, id: String, from: List<String>, state: String, error: String?): Int

    @Query("UPDATE outbox SET state = :state, error = :error, op_id = :opId WHERE scope = :scope AND client_msg_id = :id AND state IN (:from)")
    suspend fun transition(scope: String, id: String, from: List<String>, state: String, error: String?, opId: String): Int

    @Query("UPDATE outbox SET state = 'unknown' WHERE scope = :scope AND state = 'sending'")
    suspend fun recoverInterrupted(scope: String)

    @Query("SELECT * FROM drafts WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun draft(scope: String, machine: String, session: String): DraftRow?

    @Query("SELECT * FROM drafts WHERE scope = :scope AND machine = :machine AND session = :session")
    fun observeDraft(scope: String, machine: String, session: String): Flow<DraftRow?>

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun putDraft(row: DraftRow)

    @Query("SELECT * FROM main_conversation WHERE scope = :scope")
    suspend fun mainConversation(scope: String): MainConversationRow?

    @Insert(onConflict = OnConflictStrategy.IGNORE)
    suspend fun claimMainCreation(row: MainConversationRow): Long

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun putMainConversation(row: MainConversationRow)

    @Query("DELETE FROM main_conversation WHERE scope = :scope AND machine IS NULL AND session IS NULL")
    suspend fun releaseMainCreation(scope: String)

    @Query("DELETE FROM main_conversation WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun forgetMainConversation(scope: String, machine: String, session: String)

    @Query("UPDATE outbox SET machine = :machine, session = :session WHERE scope = :scope AND main_target = 1 AND machine = ''")
    suspend fun bindMainMessages(scope: String, machine: String, session: String)

    // Moving a merged group's data (GroupMerge.kt); queries only, the schema is unchanged.
    @Query("SELECT * FROM outbox WHERE scope = :scope ORDER BY position")
    suspend fun outbox(scope: String): List<OutboxRow>

    @Query("SELECT * FROM drafts WHERE scope = :scope")
    suspend fun drafts(scope: String): List<DraftRow>

    /** Keeps the row already there: a move retried after a crash adds nothing twice. */
    @Insert(onConflict = OnConflictStrategy.IGNORE)
    suspend fun adoptMessage(row: OutboxRow): Long
}

@Database(entities = [OutboxRow::class, DraftRow::class, MainConversationRow::class], version = 4,
    autoMigrations = [AutoMigration(from = 1, to = 2), AutoMigration(from = 2, to = 3), AutoMigration(from = 3, to = 4)],
    exportSchema = true)
abstract class PersistentDatabase : RoomDatabase() {
    abstract fun records(): PersistentDao
}

class RoomOutboxStore(private val database: PersistentDatabase, private val scope: HubScope) : OutboxStore {
    private val dao = database.records()
    override val entries = dao.observeOutbox(scope.path).map { rows -> rows.map { row ->
        OutboxEntry(row.client_msg_id, SessionKey(row.machine, row.session), row.text, row.clear_draft,
            OutboxState.entries.single { it.wire == row.state }, row.error, row.created_at, row.main_target, row.op_id)
    } }

    override suspend fun enqueue(entry: OutboxEntry) = enqueueDraft(entry, null)

    override suspend fun enqueueDraft(entry: OutboxEntry, draft: DraftSnapshot?) = database.withTransaction {
        require(draft == null || draft.scope == scope) { "Draft belongs to another pairing" }
        val existing = dao.message(scope.path, entry.clientMessageId)
        if (existing != null) {
            require(existing.main_target == entry.mainTarget &&
                (entry.mainTarget || (existing.machine == entry.session.machineId && existing.session == entry.session.sessionId)) &&
                existing.text == entry.text && existing.clear_draft == entry.clearDraft) { "Operation id reused with different parameters" }
        } else {
            if (draft != null) {
                val key = draft.key.storageKey()
                val old = dao.draft(scope.path, key.machineId, key.sessionId)
                check((old?.version ?: 0) == draft.version && old?.text.orEmpty() == draft.text) { "Draft version changed" }
                dao.putDraft(DraftRow(scope.path, key.machineId, key.sessionId, "", Math.addExact(draft.version, 1)))
            }
            val main = if (entry.mainTarget) mainConversation() else null
            val target = main ?: entry.session
            dao.insertMessage(OutboxRow(scope = scope.path, machine = target.machineId,
                session = target.sessionId, client_msg_id = entry.clientMessageId, text = entry.text,
                clear_draft = entry.clearDraft, state = OutboxState.Queued.wire, error = null,
                created_at = entry.createdAt, main_target = entry.mainTarget))
        }
    }

    override suspend fun mainConversation(): SessionKey? = dao.mainConversation(scope.path)?.let {
        if (it.machine != null && it.session != null) SessionKey(it.machine, it.session) else null
    }

    override suspend fun claimMainCreation(): Boolean =
        dao.claimMainCreation(MainConversationRow(scope.path, null, null)) != -1L

    override suspend fun releaseMainCreation() = dao.releaseMainCreation(scope.path)

    override suspend fun bindMainConversation(session: SessionKey) = database.withTransaction {
        require(session.machineId.isNotBlank() && session.sessionId.isNotBlank())
        dao.putMainConversation(MainConversationRow(scope.path, session.machineId, session.sessionId))
        dao.bindMainMessages(scope.path, session.machineId, session.sessionId)
    }

    override suspend fun forgetMainConversation(session: SessionKey) =
        dao.forgetMainConversation(scope.path, session.machineId, session.sessionId)

    override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?): Boolean =
        dao.transition(scope.path, id, from.map { it.wire }, to.wire, error) != 0

    override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String): Boolean =
        dao.transition(scope.path, id, from.map { it.wire }, to.wire, error, opId) != 0

    override suspend fun recoverInterrupted() = dao.recoverInterrupted(scope.path)
}

@Entity(tableName = "items", primaryKeys = ["scope", "machine", "session", "id"])
data class ItemRow(
    val scope: String, val machine: String, val session: String,
    val seq: Int, val part: Int, val id: String, val json: String,
)

@Entity(tableName = "cursors", primaryKeys = ["scope", "machine", "session"])
data class SessionCursorRow(
    val scope: String, val machine: String, val session: String,
    val history: String?, val covered: Int, val has_more: Boolean?,
    @ColumnInfo(defaultValue = "0") val accessed_at: Long = 0,
)

data class SessionCacheSize(val machine: String, val session: String, val bytes: Long, val accessed: Long)

@Dao
interface ItemsDao {
    @Query("SELECT i.machine, i.session, SUM(length(CAST(i.json AS BLOB))) AS bytes, " +
        "COALESCE(c.accessed_at, 0) AS accessed FROM items i LEFT JOIN cursors c ON " +
        "i.scope = c.scope AND i.machine = c.machine AND i.session = c.session " +
        "WHERE i.scope = :scope GROUP BY i.machine, i.session ORDER BY accessed, i.machine, i.session")
    suspend fun sizes(scope: String): List<SessionCacheSize>

    /** One session's share of [sizes], read along the primary key's prefix. */
    @Query("SELECT COALESCE(SUM(length(CAST(json AS BLOB))), 0) FROM items " +
        "WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun size(scope: String, machine: String, session: String): Long

    @Query("UPDATE cursors SET accessed_at = :accessed WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun touch(scope: String, machine: String, session: String, accessed: Long)

    @Query("DELETE FROM cursors WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun clearCursor(scope: String, machine: String, session: String)

    @Query("SELECT * FROM cursors WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun cursor(scope: String, machine: String, session: String): SessionCursorRow?

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun putCursor(cursor: SessionCursorRow)

    @Query("SELECT * FROM items WHERE scope = :scope AND machine = :machine AND session = :session ORDER BY seq, part, id")
    fun observe(scope: String, machine: String, session: String): Flow<List<ItemRow>>

    @Query("DELETE FROM items WHERE scope = :scope AND machine = :machine AND session = :session")
    suspend fun clear(scope: String, machine: String, session: String)

    @Insert(onConflict = OnConflictStrategy.REPLACE)
    suspend fun insert(items: List<ItemRow>)
}

@Database(entities = [ItemRow::class, SessionCursorRow::class], version = 3,
    autoMigrations = [AutoMigration(from = 2, to = 3)], exportSchema = true)
abstract class ItemsDatabase : RoomDatabase() {
    abstract fun items(): ItemsDao

    companion object {
        val MIGRATION_1_2 = object : Migration(1, 2) {
            override fun migrate(db: SupportSQLiteDatabase) {
                db.execSQL("CREATE TABLE IF NOT EXISTS `cursors` (`scope` TEXT NOT NULL, `machine` TEXT NOT NULL, " +
                    "`session` TEXT NOT NULL, `history` TEXT, `covered` INTEGER NOT NULL, `has_more` INTEGER, " +
                    "PRIMARY KEY(`scope`, `machine`, `session`))")
            }
        }
    }
}
