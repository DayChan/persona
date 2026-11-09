package persona.feature.inbox

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.flow.channelFlow
import kotlinx.coroutines.flow.conflate
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.mapNotNull
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import persona.core.contracts.api.InboxDoneTarget
import persona.core.domain.inbox.InboxTier
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.intOrNull

/** Conversation identity includes pairing and assistant ownership, even when they share the same source thread. */
data class InboxConversationKey(val scope: String, val source: String, val account: String, val thread: String, val manager: String? = null)

/** One validated conversation, retaining the repository and draft owner that supplied it. */
class InboxConversationBinding internal constructor(
    val key: InboxConversationKey,
    val window: Flow<LarkConversation>,
    val composer: ReplyComposer,
)

enum class InboxConversationSection { Action, Fyi, ReadPending, Read, Done }

data class InboxMessageRead(val scope: String, val feedId: String, val id: String, val position: Long)

data class InboxConversationRow(
    val key: InboxConversationKey,
    val title: String,
    val records: List<InboxRecord>,
    val section: InboxConversationSection,
    val version: String,
    /** Null until a verified message window supplies the count; item counts are event counts. */
    val unreadCount: Int? = null,
    val messagePreview: String? = null,
    val latestMessageId: String? = null,
) {
    val itemId: String get() = records.first().item.id
    val preview: String get() = messagePreview ?: records.first().item.preview
    val unread: Boolean get() = section == InboxConversationSection.Action || section == InboxConversationSection.Fyi
}

data class InboxConversationList(
    val rows: List<InboxConversationRow> = emptyList(),
    val originalRecords: List<InboxRecord> = emptyList(),
)

data class InboxReadVersion(val itemId: String, val version: String?)

fun inboxConversationKey(scope: String, record: InboxRecord): InboxConversationKey? =
    record.feedId?.takeIf { record.doc == null && record.moment == null && !record.isMail }?.let {
        InboxConversationKey(scope, record.source.ifBlank { "lark" }, record.item.account, it, record.manager)
    }

private fun itemsVersion(records: List<InboxRecord>) = records.sortedBy { it.item.id }.joinToString("") {
    "${it.item.id.length}:${it.item.id}${it.item.version?.length ?: -1}:${it.item.version.orEmpty()}"
}

private fun messageVersion(version: String, window: LarkConversation?): String {
    val last = window?.messages?.lastOrNull { !it.mine && !it.system && it.position != null && it.delivery == LarkDelivery.Sent }
    return version + last?.let { "|${it.id.length}:${it.id}:${it.position}" }.orEmpty()
}

/** Group only conversations this adapter can read and reply to; documents keep their original routes. */
fun buildInboxConversations(scope: String, snapshot: InboxSnapshot, read: Set<InboxReadVersion> = emptySet()): InboxConversationList {
    val originals = snapshot.records.filter { inboxConversationKey(scope, it) == null }
    val rows = snapshot.records.mapNotNull { record -> inboxConversationKey(scope, record)?.let { it to record } }
        .groupBy({ it.first }, { it.second }).map { (key, records) ->
            val ordered = records.sortedWith(compareByDescending<InboxRecord> { it.item.updatedAt }.thenBy { it.item.id })
            val unread = ordered.filter { !it.item.isDone && !it.read && InboxReadVersion(it.item.id, it.item.version) !in read }
            val pending = ordered.filterNot { it.item.isDone }
            val section = when {
                pending.isEmpty() -> InboxConversationSection.Done
                unread.any { it.item.triage == InboxTier.Action } -> InboxConversationSection.Action
                unread.isNotEmpty() -> InboxConversationSection.Fyi
                pending.any { it.item.triage == InboxTier.Action } -> InboxConversationSection.ReadPending
                else -> InboxConversationSection.Read
            }
            val version = itemsVersion(ordered)
            InboxConversationRow(key, ordered.first().item.title, ordered, section, version)
        }.sortedWith(compareBy<InboxConversationRow> { it.section.ordinal }.thenByDescending { it.records.first().item.updatedAt }.thenBy { it.key.thread })
    return InboxConversationList(rows, originals)
}

/** A read operates on the exact messages and item versions captured before it started. */
data class InboxReadSnapshot(
    val key: InboxConversationKey,
    val version: String,
    val items: List<InboxDoneTarget>,
    val messages: List<LarkMessage>,
    val complete: Boolean = false,
)

data class InboxReadResult(
    val succeeded: List<InboxConversationKey> = emptyList(),
    val failed: List<InboxReadSnapshot> = emptyList(),
    val skipped: List<InboxConversationKey> = emptyList(),
)

/**
 * Unread conversations whose message windows load ahead of being opened. Each window fetches and parses whole
 * message pages (cards through jsoup); 24 of them at once ran a real Feishu inbox out of a 192 MB heap within
 * seconds of launch (2026-11-06). Others load when the app or the capsule opens them.
 */
internal const val PREFETCHED_WINDOWS = 3

/** 全部已读: conversations acknowledged at once. */
private const val BULK_READS = 4

/** One paired inbox shared by the full app and the capsule. Reads never complete its items. */
class InboxConversations(
    private val repository: InboxRepository,
    val scope: String,
    private val owner: CoroutineScope,
    private val composers: ReplyComposers,
) {
    private val mutable = MutableStateFlow(InboxConversationList())
    val state: StateFlow<InboxConversationList> = mutable.asStateFlow()
    private val windows = MutableStateFlow<Map<InboxConversationKey, LarkConversation>>(emptyMap())
    private val windowVersions = MutableStateFlow<Map<InboxConversationKey, String>>(emptyMap())
    private val acceptedReads = MutableStateFlow<List<InboxReadSnapshot>>(emptyList())
    private val jobs = mutableMapOf<InboxReadSnapshot, Deferred<Boolean>>()
    /** [readPosition]: Feishu's own read position for the conversation; what is at or before it was read anywhere. */
    private data class Metadata(val title: String, val preview: String?, val unread: Int?, val version: String, val messageEnd: String?,
        val readPosition: Long? = null)
    private val metadata = MutableStateFlow<Map<InboxConversationKey, Metadata>>(emptyMap())
    private data class InboxInputs(val snapshot: InboxSnapshot, val opened: Map<InboxConversationKey, LarkConversation>,
        val accepted: List<InboxReadSnapshot>, val receipts: Set<InboxMessageRead>, val info: Map<InboxConversationKey, Metadata>)
    private val metadataRequests = Channel<Unit>(Channel.CONFLATED)
    private val active = mutableMapOf<InboxConversationKey, Int>()
    private var prefetched = emptySet<InboxConversationKey>()

    init {
        owner.launch {
            repository.snapshot.map { snapshot -> buildInboxConversations(scope, snapshot).rows
                .filter { it.section != InboxConversationSection.Done && it.section != InboxConversationSection.Read }
                .take(PREFETCHED_WINDOWS).map { it.key }.toSet() }
                .distinctUntilChanged().collectLatest { keys ->
                    synchronized(active) { prefetched = keys }
                    windows.update { it.filterKeys { key -> key in keys || synchronized(active) { key in active } } }
                    coroutineScope { keys.forEach { key -> launch {
                        try { repository.conversation(key.thread, null).collect { window ->
                            acceptWindow(key, window)
                        } } catch (e: CancellationException) { throw e }
                        catch (_: Exception) { windows.update { it + (key to LarkConversation(failure = LarkFailure.LoadFailed)) } }
                    } } }
                }
        }
        owner.launch {
            repository.snapshot.map { buildInboxConversations(scope, it).rows.map { row -> row.key to row.version } }
                .distinctUntilChanged().collect { metadataRequests.trySend(Unit) }
        }
        owner.launch {
            // Pages through every Feishu conversation: at most every couple of seconds however often rows change.
            for (request in metadataRequests) { delay(METADATA_MILLIS); metadataRequests.tryReceive(); refreshMetadata() }
        }
        owner.launch {
            // The (whole-inbox) rebuild only ever works on the latest inputs: every message, receipt and metadata page
            // used to rebuild every row several times over while the previous rebuilds were still queued.
            combine(repository.snapshot, windows, acceptedReads, repository.messageReads, metadata) { snapshot, opened, accepted, receipts, info ->
                InboxInputs(snapshot, opened, accepted, receipts, info)
            }.conflate().map { (snapshot, opened, accepted, receipts, info) ->
                fun confirmed(key: InboxConversationKey, message: LarkMessage): Boolean = message.isRead ||
                    // Read on any device (or before this process): Feishu's read position covers it. Without this a
                    // loaded window kept its conversation unread after a restart though Feishu had it read.
                    message.position?.let { position -> info[key]?.readPosition?.let { position <= it } == true } == true ||
                    message.position?.let { position -> InboxMessageRead(scope, key.thread, message.id, position) in receipts ||
                        accepted.any { it.key == key && it.messages.any { captured -> (captured.position ?: -1) >= position } } } == true
                val read = accepted.filter { captured ->
                    val latest = opened[captured.key]?.messages?.lastOrNull { !it.mine && !it.system && it.position != null }
                    captured.complete && latest != null && captured.messages.any { it.id == latest.id && it.position == latest.position }
                }.flatMap { captured -> captured.items.map { InboxReadVersion(it.id, it.version) } }.toSet()
                val initial = buildInboxConversations(scope, snapshot, read)
                val observedRead = initial.rows.filter { row ->
                    val window = opened[row.key]
                    val last = window?.messages?.lastOrNull { !it.mine && !it.system && it.position != null }
                    val knownUnread = window?.takeIf { it.verified }?.messages.orEmpty().any {
                        !it.mine && !it.system && it.position != null && !confirmed(row.key, it)
                    }
                    val sourceRead = !knownUnread && info[row.key]?.takeIf { it.version == row.version && it.messageEnd == window?.messages?.lastOrNull()?.id }?.unread == 0
                    sourceRead || windowVersions.value[row.key] == row.version && window?.verified == true && !window.hasNewer &&
                        last != null && (last.isRead || InboxMessageRead(scope, row.key.thread, last.id, last.position!!) in receipts)
                }.flatMap { row -> row.records.map { InboxReadVersion(it.item.id, it.item.version) } }.toSet()
                val grouped = buildInboxConversations(scope, snapshot, read + observedRead)
                grouped.copy(rows = grouped.rows.map { row ->
                    val window = opened[row.key]
                    val messages = window?.messages.orEmpty().filter { !it.mine && !it.system && it.position != null && it.delivery == LarkDelivery.Sent }
                    val known = messages.filterNot { confirmed(row.key, it) }
                    val exact = window?.verified == true && !window.hasNewer && !window.hasOlder && windowVersions.value[row.key] == row.version
                    val section = if (window?.verified == true && known.isNotEmpty() && !row.unread && row.records.any { !it.item.isDone }) {
                        if (row.records.any { it.item.triage == InboxTier.Action && !it.item.isDone }) InboxConversationSection.Action else InboxConversationSection.Fyi
                    } else row.section
                    val unread = section == InboxConversationSection.Action || section == InboxConversationSection.Fyi
                    val reported = info[row.key]?.takeIf { it.version == row.version && it.messageEnd == window?.messages?.lastOrNull()?.id }?.unread
                    row.copy(title = info[row.key]?.title?.takeIf { it.isNotBlank() } ?: window?.title?.takeIf { it.isNotBlank() } ?: row.title,
                        section = section,
                        version = messageVersion(row.version, window), messagePreview = info[row.key]?.preview,
                        latestMessageId = messages.lastOrNull()?.id?.takeIf { window?.verified == true },
                        unreadCount = if (!unread) 0 else if (reported != null && reported >= known.size) reported else if (exact) known.size else null)
                })
            }.distinctUntilChanged().collect { mutable.value = it }
        }
    }

    private fun acceptWindow(key: InboxConversationKey, window: LarkConversation) {
        val before = windows.value[key]
        if (window.verified && (before?.verified != true || before.windowRevision != window.windowRevision ||
                before.messages.map { it.id to it.parts } != window.messages.map { it.id to it.parts })) {
            buildInboxConversations(scope, repository.snapshot.value).rows.firstOrNull { it.key == key }?.let { row ->
                windowVersions.update { versions -> versions + (key to row.version) }
            }
        }
        windows.update { it + (key to window) }
        if (window.verified && before?.messages?.lastOrNull()?.id != window.messages.lastOrNull()?.id) metadataRequests.trySend(Unit)
    }

    private suspend fun refreshMetadata() {
        val source = repository.snapshot.value.contentSource?.takeIf { it.scope == scope } ?: return
        val captured = buildInboxConversations(scope, repository.snapshot.value).rows.associateBy { it.key.thread }
        val ends = windows.value.mapValues { it.value.messages.lastOrNull()?.id }
        try {
            val found = mutableMapOf<InboxConversationKey, Metadata>()
            var cursor: String? = null
            val cursors = mutableSetOf<String>()
            do {
                val page = source.transport.larkConversations(cursor)
                (page["conversations"] as? JsonArray).orEmpty().filterIsInstance<JsonObject>().forEach { value ->
                    val id = (value["id"] as? JsonPrimitive)?.contentOrNull
                    val row = captured[id] ?: return@forEach
                    found[row.key] = Metadata((value["name"] as? JsonPrimitive)?.contentOrNull.orEmpty(),
                        (value["preview"] as? JsonPrimitive)?.contentOrNull,
                        (value["unread"] as? JsonPrimitive)?.intOrNull, row.version, ends[row.key],
                        (value["read_position"] as? JsonPrimitive)?.contentOrNull?.toLongOrNull())
                }
                cursor = (page["next_cursor"] as? JsonPrimitive)?.contentOrNull?.takeIf { it.isNotBlank() }
                check(cursor == null || cursors.add(cursor!!)) { "Conversation cursor did not advance" }
            } while (cursor != null)
            if (repository.snapshot.value.contentSource === source) metadata.value = found
        } catch (e: CancellationException) { throw e }
        catch (_: Exception) { /* Missing source metadata leaves counts unknown and the inbox versions unread. */ }
    }

    private fun owns(key: InboxConversationKey) = key.scope == scope && state.value.rows.any { it.key == key }

    private fun requireOwned(key: InboxConversationKey) = check(owns(key)) { "Conversation is no longer in this inbox" }

    fun bind(key: InboxConversationKey): InboxConversationBinding? {
        if (!owns(key)) return null
        return InboxConversationBinding(key, observeOwned(key), composerOwned(key))
    }

    fun observe(key: InboxConversationKey): Flow<LarkConversation> {
        requireOwned(key)
        return observeOwned(key)
    }

    private fun observeOwned(key: InboxConversationKey): Flow<LarkConversation> = channelFlow {
            synchronized(active) { active[key] = (active[key] ?: 0) + 1 }
            try {
                combine(repository.conversation(key.thread, null), repository.messageReads) { window, reads ->
                    acceptWindow(key, window)
                    window.copy(messages = window.messages.map { message ->
                        if (message.position != null && InboxMessageRead(scope, key.thread, message.id, message.position) in reads) message.copy(isRead = true) else message
                    })
                }.distinctUntilChanged().collect { send(it) }
            } finally {
                val retained = synchronized(active) {
                    val count = (active[key] ?: 1) - 1
                    if (count == 0) active.remove(key) else active[key] = count
                    key in prefetched || key in active
                }
                if (!retained) windows.update { it - key }
            }
    }
    fun composer(key: InboxConversationKey): ReplyComposer {
        requireOwned(key)
        return composerOwned(key)
    }

    private fun composerOwned(key: InboxConversationKey) = composers.get(ReplyTarget.Lark(key.thread))

    fun capture(key: InboxConversationKey, messages: List<LarkMessage>? = null): InboxReadSnapshot? {
        val row = state.value.rows.firstOrNull { it.key == key } ?: return null
        return capture(row, windows.value[key], windowVersions.value[key], messages)
    }

    private fun capture(row: InboxConversationRow, opened: LarkConversation?, verifiedVersion: String?, messages: List<LarkMessage>?): InboxReadSnapshot? {
        val itemVersion = itemsVersion(row.records)
        if (verifiedVersion != itemVersion || messageVersion(itemVersion, opened) != row.version) return null
        val window = opened?.takeIf { it.verified && !it.loading && it.failure == null && it.error == null } ?: return null
        val wanted = messages ?: window.messages
        val captured = wanted.filter { candidate -> !candidate.mine && !candidate.system && candidate.position != null && candidate.delivery == LarkDelivery.Sent }
        if (captured.isEmpty() || captured.any { candidate -> window.messages.none { it.id == candidate.id && it.position == candidate.position } }) return null
        val last = window.messages.lastOrNull { !it.mine && !it.system && it.position != null }
        val complete = !window.hasNewer && last != null && captured.any { it.id == last.id && it.position == last.position }
        return InboxReadSnapshot(row.key, row.version, row.records.map { InboxDoneTarget(it.item.id, it.item.version) }, captured, complete)
    }
    fun captureFyi(): List<InboxReadSnapshot> {
        val rows = state.value.rows
        val opened = windows.value
        val versions = windowVersions.value
        return rows.filter { it.section == InboxConversationSection.Fyi }
            .mapNotNull { capture(it, opened[it.key], versions[it.key], null)?.takeIf { captured -> captured.complete } }
    }

    suspend fun read(snapshot: InboxReadSnapshot): Boolean {
        if (snapshot.key.scope != scope || state.value.rows.none { it.key == snapshot.key }) return false
        val job = synchronized(jobs) { jobs.getOrPut(snapshot) { owner.async {
            try {
                val accepted = repository.markVisible(snapshot.key.thread, snapshot.messages)
                if (accepted) acceptedReads.update { old -> old.filterNot { it.key == snapshot.key && it.messages.maxOfOrNull { message -> message.position ?: -1 }
                    ?.let { it <= (snapshot.messages.maxOfOrNull { message -> message.position ?: -1 } ?: -1) } == true } + snapshot }
                if (accepted) refreshMetadata()
                accepted
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { false }
        } } }
        try { return job.await() }
        finally { synchronized(jobs) { if (job.isCompleted) jobs.remove(snapshot, job) } }
    }
    /**
     * 全部已读 for every 仅需知悉 conversation, not only the windows already open: a receipt names the messages shown,
     * so each unopened one is loaded, read and released in turn (one window at a time keeps the heap small, see
     * [PREFETCHED_WINDOWS]). A conversation that changed or fails to load is reported, not guessed.
     */
    suspend fun readAllFyi(): InboxReadResult = readAll(setOf(InboxConversationSection.Fyi))

    /**
     * [readAllFyi] for any unread sections (the capsule's 飞书消息 group reads 需要你处理 too; read is not done).
     * Each conversation's newest page is fetched raw and its last message from someone else acknowledged, a few at a
     * time: building a full window per conversation (every page parsed, cards through jsoup) made it slow. The rows
     * turn read through the conversations' reported unread counts, refreshed once at the end.
     */
    suspend fun readAll(sections: Set<InboxConversationSection>, keys: Set<InboxConversationKey>? = null): InboxReadResult {
        val rows = state.value.rows.filter { it.section in sections && (keys == null || it.key in keys) }
        val gate = Semaphore(BULK_READS)
        val outcomes = coroutineScope {
            rows.map { row -> async { gate.withPermit {
                val current = state.value.rows.any { it.key == row.key && it.version == row.version && it.section in sections }
                row.key to (current && acknowledge(row))
            } } }.awaitAll()
        }
        refreshMetadata()
        return InboxReadResult(succeeded = outcomes.filter { it.second }.map { it.first },
            skipped = outcomes.filterNot { it.second }.map { it.first })
    }

    /** 下一条 on a conversation whose messages are not shown yet: it is read all the same, as [readAll] reads one. */
    suspend fun readConversation(key: InboxConversationKey): Boolean {
        val row = state.value.rows.firstOrNull { it.key == key } ?: return false
        return acknowledge(row).also { if (it) refreshMetadata() }
    }

    private suspend fun acknowledge(row: InboxConversationRow): Boolean {
        // A conversation already loaded here reads through its window, so its messages turn read locally too.
        capture(row, windows.value[row.key], windowVersions.value[row.key], null)?.takeIf { it.complete }?.let { captured ->
            return runCatching { read(captured) }.getOrDefault(false)
        }
        return try { repository.markLatestRead(row.key.thread) }
        catch (e: CancellationException) { throw e }
        catch (_: Exception) { false }
    }

    suspend fun readFyi(snapshots: List<InboxReadSnapshot>): InboxReadResult {
        val succeeded = mutableListOf<InboxConversationKey>()
        val failed = mutableListOf<InboxReadSnapshot>()
        val skipped = mutableListOf<InboxConversationKey>()
        for (snapshot in snapshots) {
            val current = state.value.rows.firstOrNull { it.key == snapshot.key }
            if (current == null || current.version != snapshot.version || current.section != InboxConversationSection.Fyi) {
                skipped += snapshot.key
                continue
            }
            try { if (read(snapshot)) succeeded += snapshot.key else failed += snapshot }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { failed += snapshot }
        }
        return InboxReadResult(succeeded, failed, skipped)
    }
}

/** Feishu metadata (every conversation, paged) refreshes at most this often. */
private const val METADATA_MILLIS = 2_000L
