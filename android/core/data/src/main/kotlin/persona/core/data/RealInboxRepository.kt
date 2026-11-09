package persona.core.data

import persona.core.contracts.CoreText
import persona.core.contracts.CoreText.Key
import persona.core.contracts.InboxEvent
import persona.core.contracts.LarkEvent
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.domain.inbox.InboxInteraction
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentPreferences
import persona.core.domain.sort.AgentSession
import persona.feature.inbox.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*
import java.time.Instant
import java.io.IOException

enum class LarkWindowSyncState { Cached, Syncing, Complete, Incomplete, Failed }

@OptIn(ExperimentalCoroutinesApi::class, FlowPreview::class)
class RealInboxRepository(
    private val connection: InboxAssistantConnection,
    private val journal: InboxReplyJournal,
    private val sessions: SessionsRepository,
    private val interactions: InteractionCenter,
    private val scope: CoroutineScope,
    private val snapshots: ListSnapshotStore? = null,
    private val images: ImageBytesCache = ImageBytesCache(),
    private val hubPreferences: HubPreferences? = null,
    private val hubConnection: HubConnection? = connection as? HubConnection,
    private val mainConversationSender: MainConversationSender? = null,
    private val actions: InboxActionsTransport? = null,
    private val lookupReply: suspend (ReplyTarget, String) -> InboxReplyReceipt? = { _, _ -> null },
    private val classifyReplyFailure: (Exception) -> OutboxState = { OutboxState.Unknown },
    private val sendThread: (suspend (String, ReplyRequest) -> JsonObject)? = null,
    /** The ordinary sessions' outbox (the one Agents uses): "Hand to AI" to an Agent queues its message there. */
    private val messageSender: MessageSender? = null,
    /**
     * Manager's main conversation, in [messageSender]'s ids (docs/assistant/managers.md "Clients"): older hubs without the
     * hand-off route send another manager's item there; null: the default's main queue for every item.
     */
    private val mainConversationOf: (suspend (String) -> persona.core.contracts.SessionKey)? = null,
    /** How long a burst of hub inbox events is collected before the one refresh it causes. */
    private val refreshWindowMillis: Long = 1_000,
    // Last, so existing callers that pass it as a trailing lambda keep compiling.
    private val download: suspend (InboxAssistantTransport, LarkPart.File) -> String,
) : persona.feature.inbox.InboxRepository {
    override val supportsThreadOutbox: Boolean get() = sendThread != null
    override val personaOutbox = mainConversationSender?.outbox ?: MutableStateFlow(emptyList())
    override val connectionState: Flow<ConnectionState> = hubConnection?.state ?: connection.current.map {
        if (it == null) ConnectionState.Connecting else ConnectionState.Online
    }
    private val current = MutableStateFlow(InboxSnapshot())
    override val snapshot = current.asStateFlow()
    override val entries = snapshot.map { state -> state.records.map {
        InboxEntry(it.item.id, it.item.title, it.item.preview, if (it.feedId != null) "lark" else "mail")
    } }.stateIn(scope, SharingStarted.Eagerly, emptyList())
    private val sync = MutableStateFlow<Map<String, LarkWindowSyncState>>(emptyMap())
    val syncStates: StateFlow<Map<String, LarkWindowSyncState>> = sync.asStateFlow()
    private val outbox = MutableStateFlow<List<InboxPendingReply>>(emptyList())
    private val versions = mutableMapOf<String, String>()
    private val refreshLock = Mutex()
    private val groupNames = InboxGroupNames()
    // The items last written to the list snapshot, so an unchanged list is not written again.
    private var savedItems: Pair<String, JsonArray>? = null
    private val sendLock = Mutex()
    private val personaLock = Mutex()
    private val personaRequests = mutableMapOf<String, String>()
    // Access order is maintained when a window is opened.
    private val windows = LinkedHashMap<String, Window>(16, 0.75f, true)
    private var restoredScope: String? = null

    private suspend fun restore(transport: InboxAssistantTransport) {
        if (restoredScope == transport.scope) return
        quietly { snapshots.restore<JsonObject>("${transport.scope}:inbox")?.let { data ->
            savedItems = transport.scope to JsonArray(data.array("items"))
            val records = listed(data.array("items").map { it.jsonObject }, transport)
            if (owns(transport)) current.update { it.copy(records = records, loading = false) }
        } }
        restoredScope = transport.scope
    }
    private val windowsLock = Any()

    private class Window(val transport: InboxAssistantTransport, val feedId: String, val around: Long?) {
        val state = MutableStateFlow(LarkConversation(loading = true))
        val lock = Mutex()
        var older: String? = null
        var newer: String? = null
        var tail = false
        var initialized = false
        var restored = false
        var users = 0
        var raw = emptyList<JsonObject>()
        var generation = 0L
    }

    init {
        (hubConnection as? OpenWindowReconciliationOwner)?.openWindowReconciliation
            ?.register(scope, ::reconcileOpened)
        if (hubPreferences != null) scope.launch {
            hubPreferences.preferences.collect { state ->
                current.update { it.copy(mutes = AgentPreferences(state.mutes.spaces, state.mutes.sessions, state.mutes.allowed)) }
            }
        }
        scope.launch {
            connection.current.collectLatest { transport ->
                refreshLock.withLock {
                    current.update { it.copy(records = emptyList(), loading = false, error = null,
                        contentSource = transport?.let(::LarkContentSource),
                        mutes = hubPreferences?.preferences?.value?.mutes?.let { m -> AgentPreferences(m.spaces, m.sessions, m.allowed) }
                            ?: AgentPreferences()) }
                    outbox.value = emptyList()
                    sync.value = emptyMap()
                    synchronized(windowsLock) { windows.entries.removeAll { it.value.transport !== transport } }
                    versions.clear()
                    restoredScope = null
                    if (transport != null) restore(transport)
                }
                if (transport != null) {
                    sendLock.withLock {
                        val saved = journal.update(transport.scope) { document ->
                            var restored = document.copy(outbox = document.outbox.map {
                                if (it.state == "sending") it.copy(state = "unknown") else it
                            })
                            document.drafts.filterKeys { it.startsWith("lark:thread-pending:") }.forEach { (key, draft) ->
                                if (draft.text.isNotBlank() && restored.outbox.none { it.request.clientId == draft.clientId }) {
                                    restored = restored.enqueue(InboxPendingReply(
                                        ReplyTarget.Lark("thread:${key.removePrefix("lark:thread-pending:")}"),
                                        ReplyRequest(draft.text, draft.clientId), state = "unknown"))
                                }
                            }
                            restored
                        }
                        if (owns(transport)) publishLocal(saved)
                    }
                    // One collector owns startup and reconnect recovery for this transport.
                    (hubConnection?.state ?: flowOf(ConnectionState.Online)).collectLatest { state ->
                        if (state == ConnectionState.Online) recoverReplies(transport)
                    }
                }
            }
        }
        scope.launch {
            combine(sessions.sessions, interactions.pending) { agents, waiting ->
                agents.map { AgentSession(it) } to waiting.map {
                    InboxInteraction(it.id, it.machineId, it.sessionId, it.createdAt,
                        agents.firstOrNull { agent -> agent.key == it.sessionKey }?.displayTitle.orEmpty(), it.summary, it.headline.shown,
                        manager = it.manager)
                }
            }.collect { (agents, waiting) -> current.update { it.copy(sessions = agents, interactions = waiting) } }
        }
        scope.launch {
            sessions.machines.collect { machines -> current.update { it.copy(machines = machines.associate { machine -> machine.id to machine.displayName }) } }
        }
        hubConnection?.let { hub ->
            scope.launch(start = CoroutineStart.UNDISPATCHED) {
                hub.state.collect { state ->
                    if (state == ConnectionState.Online) coroutineScope {
                        launch { quietly { refresh() } }
                        val active = synchronized(windowsLock) {
                            windows.values.filter { it.users > 0 && !it.state.value.verified }
                        }
                        active.forEach { window -> launch { quietly { load(window, older = false) } } }
                    } else invalidateWindows()
                }
            }
            scope.launch(start = CoroutineStart.UNDISPATCHED) {
                hub.events.collect { event ->
                    val feeds = when (event) {
                        is LarkEvent -> event.feedIds.toSet()
                        is InboxEvent -> null
                        else -> return@collect
                    }
                    val active = invalidateWindows(feeds)
                    // A Feishu event is about chats: it reloads the open windows only. The hub tells {"type":"inbox"}
                    // whenever the list itself changes (as the iOS app relies on); an unchanged chat can send Feishu
                    // events every few seconds, and a full inbox refresh for each one kept the list moving.
                    coroutineScope {
                        active.forEach { window -> launch { quietly { load(window, older = false) } } }
                    }
                }
            }
            // A burst of inbox events is one refresh: sample passes at most one event per window, and an event that comes
            // in while a refresh runs is kept for one more refresh after it.
            scope.launch(start = CoroutineStart.UNDISPATCHED) {
                hub.events.filterIsInstance<InboxEvent>().sample(refreshWindowMillis).collect { quietly { refresh() } }
            }
        }
        // Polling remains a fallback for hubs without invalidation events.
        scope.launch {
            current.subscriptionCount.map { it > 1 }.distinctUntilChanged().collectLatest { visible ->
                if (visible) while (isActive) {
                    try { if (online()) refresh() } catch (e: CancellationException) { throw e } catch (_: Exception) { }
                    delay(15_000)
                }
            }
        }
    }

    override suspend fun refresh() = refreshLock.withLock {
        val transport = requireTransport()
        if (!online()) return@withLock
        restore(transport)
        // Only an empty list shows that it is loading: a background refresh of a shown list must not change its layout.
        current.update { if (it.records.isEmpty()) it.copy(loading = true, error = null) else it }
        try {
            val body = transport.inbox()
            val data = body.array("items").map { it.jsonObject }
            // New items waiting for triage (#1773); an older hub sends no `triage`.
            val triage = InboxTriage.decode(body["triage"])
            val mute = if (hubPreferences == null) transport.mutes() else null
            if (!owns(transport)) return@withLock
            groupNames.refresh(data, transport)
            if (!owns(transport)) return@withLock
            val records = listed(data, transport)
            val newVersions = records.zip(data).associate { (record, item) -> record.item.id to (item.obj("agent")?.toString().orEmpty()) }
            val saved = journal.update(transport.scope) { document ->
                document.copy(drafts = document.drafts.filter { (key, draft) ->
                    !key.startsWith("inbox:") || draft.version == null || newVersions[key.removePrefix("inbox:")] == draft.version
                })
            }
            if (!owns(transport)) return@withLock
            versions.clear(); versions.putAll(newVersions)
            outbox.value = saved.outbox
            current.update { it.copy(records = records.map { item -> withLocalStatus(item, saved) },
                mutes = mute?.let { m -> AgentPreferences(m.strings("spaces"), m.strings("sessions"), m.strings("allowed").toSet()) } ?: it.mutes,
                loading = false, error = null, triage = triage, refreshes = it.refreshes + 1) }
            val items = JsonArray(data)
            if (savedItems != transport.scope to items) {
                snapshots.save("${transport.scope}:inbox", buildJsonObject { put("items", items) })
                savedItems = transport.scope to items
            }
        } catch (e: CancellationException) { throw e }
        catch (e: Exception) {
            if (owns(transport)) current.update { it.copy(loading = false, error = DataText.of(Key.InboxRefreshFailed)) }
            throw e
        }
    }

    override suspend fun refreshNow() { actions?.collectNow(); refresh() }

    override suspend fun personaContext(itemId: String): String? {
        val transport = requireTransport()
        val key = "${transport.scope}:inbox-context:$itemId"
        val cached = snapshots.restore<JsonObject>(key)?.optional("context")
        if (!online() || actions == null) return cached
        return try {
            val record = recordOf(itemId)
            asItem(record) { actions.context(record?.hubItemId ?: itemId) }.takeIf { it.isNotBlank() }?.also { context ->
                if (!owns(transport)) throw CancellationException("Pairing changed")
                snapshots.save(key, buildJsonObject { put("context", context) })
            } ?: cached
        } catch (e: CancellationException) { throw e }
        catch (_: Exception) { cached }
    }

    override suspend fun setDone(itemId: String, done: Boolean) = refreshLock.withLock {
        val transport = requireTransport()
        val old = recordOf(itemId)
        val shown = old?.item?.id ?: itemId
        current.update { it.copy(records = it.records.map { record ->
            if (record.item.id == shown) record.copy(item = record.item.copy(doneAt = if (done) Instant.now().toString() else null)) else record
        }) }
        try {
            val result = asItem(old) { transport.setDone(old?.hubItemId ?: itemId, done) }
            if (owns(transport)) replaceRecord(record(result.obj("item") ?: result, transport, old))
        } catch (e: Exception) {
            if (owns(transport) && old != null) replaceRecord(old)
            throw e
        }
    }

    override suspend fun inboxMutes(): List<persona.feature.inbox.InboxMute> =
        persona.feature.inbox.inboxMutes(requireTransport().inboxMutes())

    // "Mute this repository / chat / sender": the items it covers are done at once (the hub files them too), back if it fails.
    override suspend fun mute(record: InboxRecord, option: persona.feature.inbox.InboxMuteOption) {
        val transport = requireTransport()
        val covered = refreshLock.withLock {
            val ids = persona.feature.inbox.mutedBy(current.value.records, record, option)
            val old = current.value.records.filter { it.item.id in ids }
            val doneAt = Instant.now().toString()
            current.update { it.copy(records = it.records.map { r -> if (r.item.id in ids) r.copy(item = r.item.copy(doneAt = doneAt)) else r }) }
            old
        }
        try { asItem(record) { transport.muteInbox(record.hubItemId, option.tag) } }
        catch (e: Exception) {
            if (owns(transport)) refreshLock.withLock { covered.forEach(::replaceRecord) }
            throw e
        }
        quietly { refresh() }
    }

    override suspend fun unmute(id: String) = requireTransport().unmuteInbox(id)

    override suspend fun setDone(items: List<InboxDoneTarget>, done: Boolean): InboxBulkResult =
        bulk(items, if (done) InboxBulkAction.Done else InboxBulkAction.Reopen)

    override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
        val done = action != InboxBulkAction.Reopen
        val result = refreshLock.withLock {
            val transport = requireTransport()
            val ids = items.map { it.id }.toSet()
            val old = current.value.records.filter { it.item.id in ids }
            val doneAt = if (done) Instant.now().toString() else null
            current.update { it.copy(records = it.records.map { record ->
                if (record.item.id in ids) record.copy(item = record.item.copy(doneAt = doneAt)) else record
            }) }
            try {
                val outcome = bulkByManager(transport, items, action, old)
                val result = outcome.result
                if (owns(transport)) {
                    outcome.changed.forEach(::replaceRecord)
                    // Already so (another device filed it) keeps what was just shown; gone from the hub leaves the list;
                    // one that took new messages since, or that the hub failed to write, goes back to how it was, until the refresh.
                    val gone = result.missing.toSet()
                    if (gone.isNotEmpty()) current.update { it.copy(records = it.records.filter { r -> r.item.id !in gone }) }
                    old.filter { it.item.id in result.changedSince || it.item.id in result.failed }.forEach(::replaceRecord)
                }
                result
            } catch (e: Exception) {
                if (owns(transport)) old.forEach(::replaceRecord)
                throw e
            }
        }
        // The hub's own state (when, by whom) for what this request did not move itself.
        if (result.changedSince.isNotEmpty() || result.unchanged.isNotEmpty() || result.missing.isNotEmpty() || result.failed.isNotEmpty()) quietly { refresh() }
        return result
    }

    /** What the managers answered to a bulk action, in the app's ids, and the items it changed as the hub has them now. */
    private class BulkOutcome(val result: InboxBulkResult, val changed: List<InboxRecord>)

    /**
     * Each manager files its own items (the merged list stamps them; the hub sends `/inbox/bulk` to the manager the
     * request names, docs/protocol/protocol.md "Per-manager routing"): one request per manager, naming it on the request, the
     * answers put together. A manager that fails (offline) leaves the others' done: its own items are `failed`, put
     * back and said so; only when every manager fails does the whole action fail.
     */
    private suspend fun bulkByManager(
        transport: InboxAssistantTransport, items: List<InboxDoneTarget>, action: InboxBulkAction, old: List<InboxRecord>,
    ): BulkOutcome {
        val byId = old.associateBy { it.item.id }
        val default = AssistantManagerChoice.current.value.defaultId
        val groups = items.groupBy { byId[it.id]?.manager?.takeIf { m -> m.isNotBlank() && m != default } }
        val answers = mutableListOf<InboxBulkResult>()
        val changed = mutableListOf<InboxRecord>()
        val failed = mutableListOf<String>()
        var problem: Exception? = null
        for ((manager, list) in groups) {
            // within one manager each hub id is one item: the app's id back from it
            val keyOf = list.associate { InboxItemKey.hubId(it.id) to it.id }
            val key = { id: String -> keyOf[id] ?: id }
            try {
                val data = withContext(persona.core.contracts.api.RequestManager(manager.orEmpty())) {
                    transport.bulk(list.map { it.copy(id = InboxItemKey.hubId(it.id)) }, action)
                }
                val r = InboxBulkResult.from(data)
                answers += InboxBulkResult(r.changed.map(key), r.unchanged.map(key), r.changedSince.map(key), r.missing.map(key), r.failed.map(key))
                data.array("changed").forEach { e ->
                    (e as? JsonObject)?.let { obj -> changed += record(obj, transport, byId[key(obj.string("id"))]) }
                }
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                problem = problem ?: e
                failed += list.map { it.id }
            }
        }
        if (answers.isEmpty()) throw problem ?: IllegalStateException("nothing asked")
        val result = InboxBulkResult(answers.flatMap { it.changed }, answers.flatMap { it.unchanged }, answers.flatMap { it.changedSince },
            answers.flatMap { it.missing }, answers.flatMap { it.failed } + failed)
        return BulkOutcome(result, changed)
    }

    override suspend fun readDraft(target: ReplyTarget): ReplyDraft? {
        val transport = requireTransport()
        val document = journal.read(transport.scope)
        // A reply refused because its mail target changed must be confirmed again under a new identifier.
        fun restored(text: String, clientId: String) =
            if (document.outbox.any { it.request.clientId == clientId && it.state == "target_changed" }) ReplyDraft(text)
            else ReplyDraft(text, clientId)
        return document.drafts[target.storageKey()]?.let { restored(it.text, it.clientId).copy(ai = it.ai) }
            ?: document.outbox.lastOrNull { it.target == target && it.state != "sent" }?.let {
                restored(it.request.text, it.request.clientId)
            }
    }
    override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) {
        val transport = requireTransport()
        val version = refreshLock.withLock { (target as? ReplyTarget.Inbox)?.let { versions[it.itemId] } }
        val saved = journal.update(transport.scope) {
            it.copy(drafts = it.drafts + (target.storageKey() to InboxStoredDraft(draft.text, draft.clientId, version, draft.ai)))
        }
        if (owns(transport)) publishLocal(saved)
    }

    override suspend fun draftReply(itemId: String, instruction: String): String {
        val transport = requireTransport()
        val actions = checkNotNull(actions) { CoreText.of(Key.InboxCannotDraft).shown }
        val record = recordOf(itemId)
        val text = asItem(record) { actions.draft(record?.hubItemId ?: itemId, instruction) }
        if (!owns(transport)) throw CancellationException("Pairing changed")
        return text
    }

    override suspend fun actOnSuggestion(request: SuggestionRequest): SuggestionOutcome {
        val transport = requireTransport()
        val old = recordOf(request.itemId)
        val (item, outcome) = try {
            asItem(old) { transport.suggestion(old?.hubItemId ?: request.itemId, request.version, request.action.wire, request.text, request.clientId) } to
                SuggestionOutcome.Done
        } catch (e: InboxSuggestionStale) {
            e.item to SuggestionOutcome.Stale
        }
        if (!owns(transport)) throw CancellationException("Pairing changed")
        // The hub answers with the item as it now is (a new suggestion, Waiting for reply, or filed): the page redraws from it.
        (item?.obj("item") ?: item)?.takeIf { it.optional("id") == (old?.hubItemId ?: request.itemId) }?.let { data ->
            refreshLock.withLock {
                versions[request.itemId] = data.obj("agent")?.toString().orEmpty()
                replaceRecord(record(data, transport, old))
            }
        }
        return outcome
    }

    override suspend fun handOff(request: InboxHandOffRequest): SessionKey? {
        require(request.itemId.isNotBlank() && request.text.isNotBlank() && request.operationId.isNotBlank())
        scope.coroutineContext.ensureActive()
        val machine = request.machineId
        val session = request.sessionId
        if (!machine.isNullOrBlank() && !session.isNullOrBlank()) {
            val key = SessionKey(machine, session)
            checkNotNull(messageSender) { "agent message sender not wired" }.send(key, request.text, request.operationId)
            return key
        }
        // Main-chat hand-offs need a receipt from the item's manager, not merely a local queue entry.
        if (!online()) throw IOException("network offline")
        val transport = requireTransport()
        val old = recordOf(request.itemId)
        val result = try {
            asItem(old) { transport.handOff(old?.hubItemId ?: request.itemId, request.text, request.operationId) }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            if (e !is UnsupportedOperationException && hubCodedFailure(e)?.status !in setOf(404, 405)) throw e
            // Older hubs have no hand-off route. Keep the original manager-aware outbox path and operation ID.
            val owner = old?.manager?.takeIf { it.isNotBlank() && it != AssistantManagerChoice.current.value.defaultId }
            val main = owner?.let { checkNotNull(mainConversationOf) { "managers unavailable" }(it) }
            if (main == null) checkNotNull(mainConversationSender) { "main chat sender not wired" }.sendToMain(request.text, request.operationId)
            else checkNotNull(messageSender) { "agent message sender not wired" }.send(main, request.text, request.operationId)
            return null
        }
        if (!owns(transport)) throw CancellationException("Pairing changed")
        val destination = result.obj("session")
        val machineId = destination?.optional("machine_id")?.takeIf { it.isNotBlank() }
        val sessionId = destination?.optional("session_id")?.takeIf { it.isNotBlank() }
        if (machineId == null || sessionId == null) throw IOException("hand-off response has no session")
        result.obj("item")?.takeIf { it.optional("id") == (old?.hubItemId ?: request.itemId) }?.let { data ->
            refreshLock.withLock { replaceRecord(record(data, transport, old)) }
        }
        return SessionKey(machineId, sessionId)
    }

    override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = sendLock.withLock {
        require(request.text.isNotBlank() && request.clientId.isNotBlank())
        val transport = requireTransport()
        val previous = journal.read(transport.scope).outbox.firstOrNull { it.request.clientId == request.clientId }
        require(previous == null || previous.request == request && (previous.target == target ||
            target is ReplyTarget.Lark && previous.feedId == target.feedId)) { "reply id already used" }
        val actualTarget = previous?.target ?: target
        if (previous?.state == "sent") return@withLock
        val feed = previous?.feedId ?: when (actualTarget) {
            is ReplyTarget.Lark -> actualTarget.feedId.takeUnless { it.startsWith("thread:") }
            is ReplyTarget.Inbox -> recordOf(actualTarget.itemId)?.feedId
        }
        // Reconcile before retrying, under the same lock as sending. Text is never evidence of delivery.
        if (previous != null && online()) {
            if (previous.feedId == null && feed != null) {
                val linked = journal.update(transport.scope) { it.enqueue(previous.copy(feedId = feed)) }
                if (owns(transport)) publishLocal(linked)
            }
            reconcileReplyEchoes(transport, feed)
            if (journal.read(transport.scope).outbox.any { it.request.clientId == request.clientId && it.state == "sent" })
                return@withLock
        }
        if (previous != null && previous.outboxState !in setOf(OutboxState.Queued, OutboxState.WaitingForNetwork, OutboxState.Refused)) {
            if (online()) reconcilePending(transport, previous)
            if (replyStatus(actualTarget, request.clientId) == ReplyDelivery.Sent) return@withLock
            throw ReplyUnconfirmed()
        }
        val pending = InboxPendingReply(actualTarget, request, feed, state = if (online()) "sending" else "waitingForNetwork",
            serverId = previous?.serverId)
        val saved = journal.update(transport.scope) {
            it.enqueue(pending)
        }
        if (!owns(transport)) throw CancellationException("Pairing changed")
        publishLocal(saved)
        if (saved.outbox.any { it.request.clientId == request.clientId && it.state == "sent" }) return@withLock
        if (!online()) throw IOException("sends once connected")
        try {
            val mailTarget = request.mailTarget
            // the item replied to; a chat's send, the one record of that chat (two managers' copies of one chat: the
            // page's pin decides)
            val replied = when (actualTarget) {
                is ReplyTarget.Inbox -> recordOf(actualTarget.itemId)
                is ReplyTarget.Lark -> current.value.records.filter { it.feedId == actualTarget.feedId }.singleOrNull()
            }
            val response = if (actualTarget is ReplyTarget.Lark && actualTarget.feedId.startsWith("thread:"))
                checkNotNull(sendThread)(actualTarget.feedId.removePrefix("thread:"), request)
            else if (mailTarget != null && actualTarget is ReplyTarget.Inbox)
                asItem(replied) { transport.replyMail(InboxItemKey.hubId(actualTarget.itemId), request.text, request.clientId, mailTarget) }
            else asItem(replied) { transport.reply((actualTarget as? ReplyTarget.Inbox)?.itemId?.let(InboxItemKey::hubId),
                (actualTarget as? ReplyTarget.Lark)?.feedId, request.text, request.clientId) }
            val result = response.obj("message") ?: response
            if (response.string("state") in setOf("pending", "unknown") || result.string("state") in setOf("pending", "unknown")) {
                val uncertain = journal.update(transport.scope) { it.transition(request.clientId, "unknown") }
                if (owns(transport)) publishLocal(uncertain)
                throw ReplyUnconfirmed()
            }
            val accepted = journal.update(transport.scope) { it.confirm(request.clientId,
                // A Feishu send answers {"ok":true,"message_id":…} (hub lark.go); older replies said "id".
                if (feed != null) result.optional("message_id") ?: result.optional("id") else null) }
            if (owns(transport)) {
                publishLocal(accepted)
                if (actualTarget is ReplyTarget.Inbox) {
                    val item = response.obj("item") ?: response
                    if (item.optional("id") != null) replaceRecord(record(item, transport, replied))
                    else current.update { it.copy(records = it.records.map { record ->
                        if (record.item.id == (replied?.item?.id ?: actualTarget.itemId)) record.copy(item = record.item.copy(doneAt = Instant.now().toString())) else record
                    }) }
                }
            }
        } catch (e: Exception) {
            withContext(NonCancellable) {
                if (owns(transport)) {
                    val state = when (e) {
                        is MailReplyTargetChanged -> "target_changed"
                        is CancellationException -> OutboxState.Unknown.wire
                        else -> classifyReplyFailure(e).wire
                    }
                    val failed = journal.update(transport.scope) { it.transition(request.clientId, state) }
                    publishLocal(failed)
                }
            }
            throw e
        }
    }

    fun larkOutbox(feedId: String): Flow<List<LarkMessage>> = outbox.map { pending ->
        // Once Feishu's copy has been seen it is the message; a window without it must not show the reply again.
        pending.filter { it.feedId == feedId && !it.seen }.map { local ->
            LarkMessage(local.serverId ?: "local:${local.request.clientId}", "", DataText.of(Key.Me), mine = true,
                parts = listOf(LarkPart.Text(local.request.text)), clientId = local.request.clientId,
                delivery = when (local.outboxState) {
                    OutboxState.Sent, OutboxState.Shown -> LarkDelivery.Sent
                    OutboxState.Sending -> LarkDelivery.Sending
                    OutboxState.Queued, OutboxState.WaitingForNetwork -> LarkDelivery.WaitingForNetwork
                    else -> LarkDelivery.Failed
                }, pendingDelivery = local.outboxState.delivery)
        }
    }

    suspend fun acceptLarkWindow(feedId: String, raw: List<JsonObject>) {
        val transport = requireTransport()
        val window = synchronized(windowsLock) {
            windows[feedId] ?: Window(transport, feedId, null).also { windows[feedId] = it }
        }
        window.raw = raw
        window.state.value = window.state.value.copy(messages = raw.map { message(it, window) })
        reconcileReplies(window, raw)
    }

    override fun conversation(feedId: String, startPosition: Long?): Flow<LarkConversation> =
        connection.current.flatMapLatest { transport ->
            if (transport == null) flowOf(LarkConversation(error = DataText.of(Key.PairHubFirst)))
            else {
                val window = synchronized(windowsLock) {
                    windows[feedId]?.takeIf { it.transport === transport && it.around == startPosition }
                        ?: Window(transport, feedId, startPosition).also { trimWindows(7); windows[feedId] = it }
                }
                channelFlow {
                    synchronized(windowsLock) { window.users++ }
                    try {
                    restoreWindow(window)
                    send(window.state.value)
                    launch {
                        while (isActive) {
                            try { if (online()) load(window, older = false) }
                            catch (e: CancellationException) { throw e }
                            catch (_: Exception) { }
                            delay(15_000)
                        }
                    }
                    combine(window.state, outbox) { state, pending ->
                        val locals = if (window.tail) pending.filter { it.feedId == feedId && !it.seen }.filterNot { local ->
                            state.messages.any { it.clientId == local.request.clientId || local.serverId != null && it.id == local.serverId }
                        }.map { local ->
                            LarkMessage(local.serverId ?: "local:${local.request.clientId}", "", DataText.of(Key.Me), mine = true,
                                parts = listOf(LarkPart.Text(local.request.text)), clientId = local.request.clientId,
                                delivery = when (local.outboxState) { OutboxState.Sent -> LarkDelivery.Sent; OutboxState.Sending -> LarkDelivery.Sending; OutboxState.WaitingForNetwork -> LarkDelivery.WaitingForNetwork; else -> LarkDelivery.Failed }, pendingDelivery = local.outboxState.delivery)
                        } else emptyList()
                        state.copy(messages = state.messages + locals)
                    }.collect { send(it) }
                    } finally { synchronized(windowsLock) { window.users--; trimWindows() } }
                }
            }
        }

    /** Retain at most eight idle windows, evicting least recently opened first; policy, unmeasured. */
    private fun trimWindows(limit: Int = 8) {
        val idle = windows.entries.filter { it.value.users == 0 }
        idle.take((idle.size - limit).coerceAtLeast(0)).forEach { windows.remove(it.key) }
    }

    private fun windowKey(window: Window) = "${window.transport.scope}:lark:${window.feedId}:${window.around}"
    private suspend fun restoreWindow(window: Window) = window.lock.withLock {
        if (window.restored) return@withLock
        snapshots.restore<JsonObject>(windowKey(window))?.let { data ->
            val raw = data.array("messages").map { it.jsonObject }
            val positions = raw.mapNotNull { it.number("position") }
            if (window.around == null || positions.isNotEmpty() && window.around in positions.min()..positions.max()) {
                window.raw = raw
                window.older = data.optional("older_cursor")
                window.newer = data.optional("newer_cursor")
                window.tail = !data.flag("has_newer")
                window.initialized = true
                sync.update { it + (window.feedId to LarkWindowSyncState.Cached) }
                window.state.value = LarkConversation(messages = raw.map { message(it, window) }, loading = false,
                    hasOlder = data.flag("has_older"), hasNewer = !window.tail, verified = false)
            }
        }
        window.restored = true
    }

    /** Updating permits read-only reconciliation, while user writes remain gated by online(). */
    suspend fun reconcileOpened() = coroutineScope {
        val active = invalidateWindows()
        active.map { window -> async {
            restoreWindow(window)
            load(window, older = false, reconciling = true)
            window.state.first { it.verified }
        } }.awaitAll()
        Unit
    }

    private fun online() = hubConnection?.state?.value?.let { it == ConnectionState.Online } ?: true

    private fun invalidateWindows(feeds: Set<String>? = null): List<Window> = synchronized(windowsLock) {
        windows.values.filter { feeds == null || it.feedId in feeds }.onEach {
            it.generation++
            it.state.update { state -> state.copy(verified = false) }
            sync.update { states -> states + (it.feedId to LarkWindowSyncState.Cached) }
        }.filter { it.users > 0 }
    }

    override fun replyUpdates(target: ReplyTarget): Flow<Unit> = outbox.map { replies ->
        replies.filter { it.target == target || target is ReplyTarget.Lark && it.feedId == target.feedId }
    }.distinctUntilChanged().map { Unit }

    override suspend fun replyStatus(target: ReplyTarget, clientId: String): ReplyDelivery {
        val reply = journal.read(requireTransport().scope).outbox.firstOrNull {
            it.request.clientId == clientId && (it.target == target || target is ReplyTarget.Lark && it.feedId == target.feedId)
        } ?: return ReplyDelivery.Ready
        if (reply.state == "target_changed") return ReplyDelivery.Ready
        return when (reply.outboxState) {
            OutboxState.Sent, OutboxState.Shown -> ReplyDelivery.Sent
            OutboxState.Sending -> ReplyDelivery.Sending
            OutboxState.Queued -> ReplyDelivery.Queued
            OutboxState.WaitingForNetwork -> ReplyDelivery.WaitingForNetwork
            OutboxState.Refused -> ReplyDelivery.Refused
            OutboxState.Verifying -> ReplyDelivery.Verifying
            OutboxState.NeedsConfirmation -> ReplyDelivery.NeedsConfirmation
            else -> ReplyDelivery.Unknown
        }
    }

    override suspend fun verifyReply(target: ReplyTarget, clientId: String): ReplyDelivery = sendLock.withLock {
        val transport = requireTransport()
        val pending = journal.read(transport.scope).outbox.firstOrNull { it.request.clientId == clientId }
            ?: return@withLock ReplyDelivery.Ready
        require(pending.target == target || target is ReplyTarget.Lark && pending.feedId == target.feedId)
        if (pending.state !in setOf("sent", "target_changed") && online()) reconcilePending(transport, pending)
        if (journal.read(transport.scope).outbox.any { it.request.clientId == clientId && it.state == "target_changed" })
            throw MailReplyTargetChanged()
        replyStatus(target, clientId)
    }

    /** The public outbox calls this before opening its sending gate. Only GETs are allowed here. */
    suspend fun reconcilePendingReplies() = sendLock.withLock {
        val transport = requireTransport()
        quietly {
            for (pending in journal.read(transport.scope).outbox.filter { it.outboxState in uncertainReplyStates }) {
                if (!owns(transport)) break
                reconcilePending(transport, pending)
            }
        }
    }

    private suspend fun reconcilePending(transport: InboxAssistantTransport, pending: InboxPendingReply) {
        if (pending.state == "sent" || !owns(transport)) return
        val id = pending.request.clientId
        if (pending.outboxState !in uncertainReplyStates) return
        publishLocal(journal.update(transport.scope) { it.transition(id, OutboxState.Verifying.wire) })
        try {
            val receipt = lookupReply(pending.target, id)
            if (!owns(transport)) throw CancellationException("Pairing changed")
            if (receipt?.state == OutboxState.Sent) {
                publishLocal(journal.update(transport.scope) { it.confirm(id, receipt.serverId) })
                return
            }
            if (receipt?.state == OutboxState.Refused) {
                publishLocal(journal.update(transport.scope) { it.transition(id, receipt.state.wire, receipt.error) })
                return
            }
            reconcileReplyEchoes(transport, pending.feedId)
        } finally {
            withContext(NonCancellable) {
                if (owns(transport)) publishLocal(journal.update(transport.scope) { document ->
                    if (document.outbox.any { it.request.clientId == id && it.state == "verifying" })
                        document.transition(id, OutboxState.Unknown.wire) else document
                })
            }
        }
    }

    private val uncertainReplyStates = setOf(OutboxState.Sending, OutboxState.Unknown,
        OutboxState.Verifying, OutboxState.NeedsConfirmation)

    private suspend fun recoverReplies(transport: InboxAssistantTransport) {
        reconcilePendingReplies()
        quietly {
            val pending = journal.read(transport.scope).outbox.filter { it.outboxState in setOf(OutboxState.Queued, OutboxState.WaitingForNetwork) }
            if (pending.any { it.target is ReplyTarget.Inbox && it.feedId == null }) quietly { refresh() }
            for (pendingReply in pending) {
                if (!owns(transport) || !online()) break
                quietly { reply(pendingReply.target, pendingReply.request) }
            }
        }
    }

    private suspend fun reconcileReplyEchoes(transport: InboxAssistantTransport, feed: String?) {
        if (feed == null) return
        val window = synchronized(windowsLock) { windows[feed] } ?: Window(transport, feed, null)
        var page = transport.messages(feed)
        val seen = mutableSetOf<String>()
        var before: String? = null
        repeat(100) {
            if (!owns(transport)) throw CancellationException("Pairing changed")
            reconcileReplies(window, rows(page))
            if (!complete(page)) throw IncompleteWindow()
            if (!page.flag("has_older") || journal.read(transport.scope).outbox.none { it.feedId == feed && it.state != "sent" }) return
            before = checkedCursor(page, true, before, seen)
            page = transport.messages(feed, before = before)
        }
        throw IncompleteWindow()
    }

    override suspend fun loadOlder(feedId: String) { load(window(feedId), older = true) }
    override suspend fun refreshConversation(feedId: String) { load(window(feedId), older = false) }
    override suspend fun latestConversation(feedId: String) {
        val window = window(feedId)
        window.lock.withLock { if (owns(window.transport) && online()) replaceLatest(window) }
    }
    private fun window(feedId: String) = synchronized(windowsLock) { requireNotNull(windows[feedId]) }

    private class WindowGap : Exception()
    private class ReplyUnconfirmed : IOException()
    private class IncompleteWindow : Exception()
    private fun complete(page: JsonObject) = (page.optional("sync_state") ?: page.optional("syncState")) == "complete"
    private fun rows(page: JsonObject) = page.array("messages").map { it.jsonObject }
    private fun cursor(page: JsonObject, older: Boolean) = page.optional(if (older) "older_cursor" else "newer_cursor")
    private fun checkedCursor(page: JsonObject, older: Boolean, used: String?, seen: MutableSet<String>): String {
        val next = cursor(page, older) ?: throw WindowGap()
        if (next == used || !seen.add(next)) throw WindowGap()
        val number = next.toLongOrNull()
        val prior = used?.toLongOrNull()
        if (number != null && prior != null && if (older) number >= prior else number <= prior) throw WindowGap()
        return next
    }

    private suspend fun fetch(window: Window, around: Long? = null, before: String? = null, after: String? = null): JsonObject {
        val page = window.transport.messages(window.feedId, around, before, after)
        if (!owns(window.transport)) throw CancellationException("Pairing changed")
        // Positions are sparse. Only authoritative pages and their cursor chain establish coverage.
        if (!complete(page)) {
            reconcileReplies(window, rows(page))
            if ((page.optional("sync_state") ?: page.optional("syncState")) == "gap") throw WindowGap()
            if (window.raw.isEmpty() && before == null && after == null) {
                window.raw = rows(page)
                window.older = cursor(page, true)
                window.newer = cursor(page, false)
                window.tail = !page.flag("has_newer")
                window.initialized = true
                window.state.update { it.copy(messages = window.raw.map { row -> message(row, window) },
                    hasOlder = page.flag("has_older"), hasNewer = page.flag("has_newer"), verified = false) }
            }
            throw IncompleteWindow()
        }
        val afterPosition = after?.toLongOrNull()
        val beforePosition = before?.toLongOrNull()
        val positions = rows(page).mapNotNull { it.number("position") }
        if (afterPosition != null && positions.isNotEmpty() && positions.max() < afterPosition) throw WindowGap()
        if (beforePosition != null && positions.isNotEmpty() && positions.min() > beforePosition) throw WindowGap()
        return page
    }

    private suspend fun load(window: Window, older: Boolean, reconciling: Boolean = false) = window.lock.withLock {
        if (!owns(window.transport) || !(online() || reconciling && hubConnection?.state?.value == ConnectionState.Updating)) return@withLock
        if (older && (!window.state.value.verified || !window.state.value.hasOlder)) return@withLock
        val generation = window.generation
        sync.update { it + (window.feedId to LarkWindowSyncState.Syncing) }
        window.state.update { it.copy(loading = !window.initialized, loadingOlder = older,
            loadingNewer = !older && window.initialized, verified = false, error = null) }
        try {
            if (older) {
                val before = window.older ?: throw WindowGap()
                val page = fetch(window, before = before)
                if (page.flag("has_older")) checkedCursor(page, true, before, mutableSetOf(before))
                val incoming = rows(page)
                val edge = window.raw.firstOrNull()?.number("position")
                if (page.flag("has_older") && incoming.none { (it.number("position") ?: Long.MAX_VALUE) < (edge ?: Long.MAX_VALUE) }) throw WindowGap()
                install(window, window.raw + incoming, cursor(page, true), window.newer,
                    page.flag("has_older"), !window.tail, generation)
            } else {
                val firstPosition = window.raw.firstOrNull()?.number("position")
                val initial = fetch(window, around = firstPosition ?: window.around)
                var first = initial
                var last = initial
                val incoming = rows(initial).toMutableList()
                val beforeSeen = mutableSetOf<String>()
                val afterSeen = mutableSetOf<String>()
                var budget = 100
                var before: String? = null
                while (firstPosition != null && first.flag("has_older") &&
                    (incoming.mapNotNull { it.number("position") }.minOrNull() ?: Long.MAX_VALUE) > firstPosition) {
                    if (--budget == 0) throw WindowGap()
                    before = checkedCursor(first, true, before, beforeSeen)
                    first = fetch(window, before = before)
                    incoming.addAll(0, rows(first))
                }
                var after: String? = null
                while (last.flag("has_newer")) {
                    if (--budget == 0) throw WindowGap()
                    after = checkedCursor(last, false, after, afterSeen)
                    last = fetch(window, after = after)
                    incoming += rows(last)
                }
                val preserved = if (firstPosition == null) incoming else incoming.filter { (it.number("position") ?: Long.MAX_VALUE) >= firstPosition }
                install(window, preserved, if (firstPosition != null) window.older ?: cursor(first, true) else cursor(first, true),
                    cursor(last, false), if (firstPosition != null) window.state.value.hasOlder else first.flag("has_older"),
                    false, generation, reconciling = reconciling)
            }
        } catch (e: CancellationException) { throw e }
        catch (_: WindowGap) {
            try { replaceLatest(window, reconciling) }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) {
                sync.update { it + (window.feedId to LarkWindowSyncState.Incomplete) }
                window.state.update { it.copy(loading = false, loadingOlder = false,
                loadingNewer = false, verified = false, error = DataText.of(Key.LarkIncomplete)) } }
        } catch (_: IncompleteWindow) {
            sync.update { it + (window.feedId to LarkWindowSyncState.Incomplete) }
            window.state.update { it.copy(loading = false, loadingOlder = false, loadingNewer = false,
                verified = false, error = DataText.of(Key.LarkIncomplete)) }
        } catch (e: Exception) {
            sync.update { it + (window.feedId to LarkWindowSyncState.Failed) }
            window.state.update { it.copy(loading = false, loadingOlder = false, loadingNewer = false,
                verified = false, error = DataText.of(Key.LarkLoadFailed)) }
            throw e
        }
    }

    private suspend fun replaceLatest(window: Window, reconciling: Boolean = false) {
        val generation = window.generation
        val page = fetch(window)
        if (page.flag("has_newer")) throw WindowGap()
        install(window, rows(page), cursor(page, true), cursor(page, false), page.flag("has_older"), false, generation,
            replaced = true, reconciling = reconciling)
    }

    private suspend fun reconcileReplies(window: Window, raw: List<JsonObject>) {
        val saved = journal.update(window.transport.scope) { document ->
            var confirmed = document
            for (local in document.outbox.filter { it.feedId == window.feedId }) {
                val remote = raw.firstOrNull { it.optional("client_id") == local.request.clientId ||
                    it.optional("clientId") == local.request.clientId || local.serverId != null && it.string("id") == local.serverId }
                if (remote != null && delivered(remote))
                    confirmed = confirmed.confirm(local.request.clientId, remote.string("id"), seen = true)
            }
            confirmed
        }
        if (owns(window.transport)) publishLocal(saved)
        // A window opened past the reply never holds its copy: ask the hub for it by id, as iOS verifyAcknowledged does.
        for (local in saved.outbox.filter { it.feedId == window.feedId && it.state == "sent" && !it.seen && it.serverId != null }) {
            if (!owns(window.transport)) return
            val id = local.serverId!!
            quietly {
                val copy = window.transport.larkMessage(id).obj("message")
                if (copy != null && copy.string("id") == id && delivered(copy)) publishLocal(journal.update(window.transport.scope) {
                    if (it.outbox.any { reply -> reply.request.clientId == local.request.clientId && reply.state == "sent" })
                        it.confirm(local.request.clientId, id, seen = true) else it
                })
            }
        }
    }

    private fun delivered(remote: JsonObject) = remote.optional("id") != null &&
        (remote.optional("local_state") ?: remote.optional("localState")) !in setOf("sending", "pending", "failed", "unknown", "verifying", "queued", "waiting", "waitingForNetwork")

    private suspend fun install(window: Window, incoming: List<JsonObject>, older: String?, newer: String?,
        hasOlder: Boolean, hasNewer: Boolean, generation: Long, replaced: Boolean = false, reconciling: Boolean = false) {
        reconcileReplies(window, incoming)
        if (!owns(window.transport)) return
        window.raw = incoming.associateBy { it.string("id") }.values.sortedBy { it.number("position") ?: Long.MAX_VALUE }
        window.older = older
        window.newer = newer
        window.tail = !hasNewer
        window.initialized = true
        window.state.update { it.copy(
            title = current.value.records.firstOrNull { record -> record.feedId == window.feedId }?.item?.title ?: DataText.of(Key.LarkChat),
            messages = window.raw.map { row -> message(row, window) }, loading = false, loadingOlder = false, loadingNewer = false,
            hasOlder = hasOlder, hasNewer = hasNewer, verified = generation == window.generation &&
                (online() || reconciling && hubConnection?.state?.value == ConnectionState.Updating),
            windowRevision = it.windowRevision + if (replaced) 1 else 0,
            error = if (replaced) DataText.of(Key.LarkJumpedToLatest) else null) }
        sync.update { it + (window.feedId to if (window.state.value.verified) LarkWindowSyncState.Complete else LarkWindowSyncState.Cached) }
        if (!window.state.value.verified) return
        val start = window.around?.let { anchor -> window.raw.indexOfFirst { (it.number("position") ?: Long.MAX_VALUE) >= anchor } }
        val offset = if (start != null && start >= 0) (start - 40).coerceIn(0, (window.raw.size - 80).coerceAtLeast(0))
            else (window.raw.size - 80).coerceAtLeast(0)
        val cached = window.raw.drop(offset).take(80)
        snapshots.save(windowKey(window), buildJsonObject {
            put("messages", JsonArray(cached))
            put("older_cursor", if (offset > 0) cached.firstOrNull()?.optional("position") else older)
            put("newer_cursor", if (offset + cached.size < window.raw.size) cached.lastOrNull()?.optional("position") else newer)
            put("has_older", hasOlder || offset > 0)
            put("has_newer", hasNewer || offset + cached.size < window.raw.size)
        })
    }

    override suspend fun downloadFile(file: LarkPart.File): String = download(requireTransport(), file)
    override suspend fun askPersona(itemId: String, action: InboxAiAction) = askPersona(InboxPersonaRequest(itemId, action))

    override suspend fun askPersona(request: InboxPersonaRequest) = personaLock.withLock {
        scope.coroutineContext.ensureActive()
        require(request.itemId.isNotBlank() && request.operationId.isNotBlank())
        val sender = checkNotNull(mainConversationSender) { "main chat sender not wired" }
        val record = recordOf(request.itemId)
        // Another manager's item is its Persona's to look at, as Hand to AI does (handOff): its main conversation.
        val owner = record?.manager?.takeIf { it.isNotBlank() && it != AssistantManagerChoice.current.value.defaultId }
        val text = sender.outbox.value.firstOrNull { it.clientMessageId == request.operationId }?.text
            ?: personaRequests.getOrPut(request.operationId) {
                require(record != null || !request.context.isNullOrBlank()) { CoreText.of(Key.InboxItemNotLoaded).shown }
                val messages = synchronized(windowsLock) { record?.feedId?.let { windows[it]?.state?.value?.messages } }.orEmpty()
                val loaded = messages.joinToString("\n") { message ->
                    val body = message.parts.joinToString("\n") { part -> when (part) {
                        is LarkPart.Text -> part.text
                        is LarkPart.Card -> part.content.summary
                        is LarkPart.File -> "附件：${part.name}（${part.mimeType}）" // i18n-ignore: the chat handed to the model
                        is LarkPart.Image -> part.description
                        is LarkPart.External -> "${part.title}\n${part.larkUrl}"
                        is LarkPart.Code -> "代码：\n${part.code}" // i18n-ignore: the chat handed to the model
                        is LarkPart.Audio -> "语音（${part.durationMs / 1000} 秒）" // i18n-ignore: the chat handed to the model
                        is LarkPart.Video -> "视频（${part.durationMs / 1000} 秒）" // i18n-ignore: the chat handed to the model
                        is LarkPart.Sticker -> part.description
                        is LarkPart.MergeForward -> "聊天记录：${part.title}（${part.messages.size} 条）" // i18n-ignore: the chat handed to the model
                    } }
                    "${message.senderName} ${message.time}\n$body"
                }
                inboxPersonaText(request, record, loaded.ifBlank { record?.item?.preview.orEmpty() })
            }
        if (owner == null) sender.sendToMain(text, request.operationId)
        else checkNotNull(messageSender) { "agent message sender not wired" }
            .send(checkNotNull(mainConversationOf) { "managers unavailable" }(owner), text, request.operationId)
    }
    private fun requireTransport() = requireNotNull(connection.current.value) { CoreText.of(Key.PairHubFirst).shown }
    private fun owns(transport: InboxAssistantTransport) = connection.current.value === transport
    private fun replaceRecord(record: InboxRecord) {
        current.update { it.copy(records = it.records.map { old -> if (old.item.id == record.item.id) record else old }) }
    }
    private fun publishLocal(saved: InboxReplyDocument) {
        outbox.value = saved.outbox
        current.update { it.copy(records = it.records.map { record -> withLocalStatus(record, saved) }) }
    }
    private fun withLocalStatus(record: InboxRecord, saved: InboxReplyDocument): InboxRecord {
        val target = ReplyTarget.Inbox(record.item.id)
        val pending = saved.outbox.lastOrNull {
            (it.target == target || record.feedId != null && it.feedId == record.feedId) && it.state !in setOf("sent", "target_changed")
        }
        val draft = saved.drafts[target.storageKey()]?.takeIf { it.text.isNotBlank() }
        val status = when {
            pending?.state == "sending" -> DataText.of(Key.ReplySending)
            pending?.outboxState == OutboxState.WaitingForNetwork -> DataText.of(Key.ReplyWaiting)
            pending?.state == "unknown" -> DataText.of(Key.ReplyUnknown)
            pending?.state == "verifying" -> DataText.of(Key.ReplyVerifying)
            pending?.state == "refused" -> DataText.of(Key.ReplyRefused)
            pending != null -> DataText.of(Key.ReplyFailed)
            // An AI draft shows as its card; only the user's own words read "Draft · …".
            draft != null && !draft.ai -> DataText.of(Key.ReplyDraft, draft.text)
            else -> null
        }
        return record.copy(localStatus = status, localDelivery = pending?.outboxState?.delivery, aiDraft = draft?.ai == true)
    }
    /**
     * The hub's merged list as records: every one an id of its own (InboxItemKey), and the manager each is stamped with
     * noted for the requests made for it elsewhere (its mail page; docs/protocol/protocol.md "Per-manager routing"). An id two
     * managers have is not noted: the repository names the manager on each of its requests ([asItem]).
     */
    private fun listed(data: List<JsonObject>, transport: InboxAssistantTransport): List<InboxRecord> {
        val records = data.map { record(it, transport) }.map { it.copy(item = it.item.copy(id = InboxItemKey.appId(it.item.id, it.manager))) }
        val repeated = records.groupBy { it.hubItemId }.filterValues { it.size > 1 }.keys
        records.filter { it.hubItemId !in repeated }.forEach { AssistantManagerChoice.noteItem(listOf("inbox", it.hubItemId), it.manager) }
        return records
    }

    /** The record shown with [itemId] (the app's id, InboxItemKey; or the hub's, from outside the lists). */
    private fun recordOf(itemId: String): InboxRecord? = current.value.records.inboxRecord(itemId)

    /**
     * Runs requests for [record]'s item, naming its manager on each of them where they are made (RequestManager): the
     * default's names none, and so overrides what a list noted for an id another manager has too. With no record known
     * nothing is named: what the page pinned (its chat's manager) still decides.
     */
    private suspend fun <T> asItem(record: InboxRecord?, block: suspend () -> T): T {
        if (record == null) return block()
        val default = AssistantManagerChoice.current.value.defaultId
        val manager = record.manager?.takeIf { it.isNotBlank() && it != default } ?: ""
        return withContext(persona.core.contracts.api.RequestManager(manager)) { block() }
    }

    /**
     * One item as the hub has it. An answer about one item comes from its manager alone, unstamped (only the merged
     * list stamps): it keeps the manager and the id [previous] had, so its group and its next requests stay its own.
     */
    private fun record(data: JsonObject, transport: InboxAssistantTransport, previous: InboxRecord? = null): InboxRecord {
        val lark = data.obj("lark")
        val agent = data.obj("agent")
        val mail = data.obj("mail")
        val moment = data.obj("moment")
        val event = persona.core.contracts.inboxEventFields(data)
        val fresh = InboxRecord(
            InboxItem(data.string("id"), data.string("account"), data.string("kind"), data.string("title"),
                data.optional("sender"), data.string("preview"), data.optional("summary"), data.optional("tier"),
                data.optional("updated_at"), data.optional("done_at"), data.optional("version"),
                notice = moment?.optional("notice"), status = event.status, urgent = event.urgent),
            sourceLabel = data.optional("account_name") ?: DataText.of(if (data.string("source") == "lark") Key.SourceLark else Key.SourceMail),
            feedId = lark?.optional("feed_id"), startPosition = lark?.number("start_position"), count = data.number("count")?.toInt() ?: 0,
            agent = agent?.let { InboxAgent(it.string("state"), it.string("draft"), it.optional("note") ?: it.string("reason"),
                it.string("task"), it.optional("machine_id"), it.optional("session_id")) },
            source = data.string("source"),
            atAll = lark?.flag("at_all") == true,
            isGroup = lark?.let { it.flag("is_group") },
            groupName = groupNames.name(data, transport.scope),
            isTopic = lark?.let { it.flag("is_topic") },
            read = data.flag("read"),
            mail = mail?.let {
                InboxMailRef(it.string("account_id"), it.optional("mailbox") ?: "INBOX",
                    it.array("uids").mapNotNull { uid -> (uid as? JsonPrimitive)?.longOrNull },
                    // The hub omits direct when false: a mail the user was only copied on (or got through a list).
                    direct = it.flag("direct"))
            },
            // Only a Feishu chat has a picture on the hub; a mail row keeps its initial, as on iOS.
            avatar = if (data.string("source") == "lark") avatar(data.optional("avatar"), transport) else null,
            moment = moment?.let {
                InboxMomentRef(
                    postId = it.string("post_id"),
                    notice = it.optional("notice"),
                    replyTo = it.optional("reply_to"),
                    myPost = it.flag("my_post"),
                    postText = it.optional("post_text"),
                )
            },
            translations = data.obj("translations")?.mapNotNull { (target, value) ->
                (value as? JsonObject)?.let { t ->
                    target to persona.core.contracts.api.ItemTranslation(t.string("subject"), t.string("preview"))
                }
            }?.toMap().orEmpty(),
            doc = persona.feature.inbox.inboxDocRef(data.obj("doc")),
            connector = data.obj("connector")?.let { c ->
                c.optional("thread")?.let { thread ->
                    persona.feature.inbox.InboxConnectorRef(thread, c.optional("label"), c.flag("can_reply"), c.optional("reply_to"))
                }
            },
            mutes = persona.feature.inbox.inboxMuteOptions(data),
            event = event,
            manager = data.optional("manager"),
        )
        if (previous == null || data.string("id") != previous.hubItemId) return fresh
        return fresh.copy(item = fresh.item.copy(id = previous.item.id), manager = fresh.manager ?: previous.manager)
    }

    override suspend fun momentsPost(postId: String): persona.core.contracts.LarkMomentsPost {
        val transport = requireTransport()
        val json = transport.momentsPost(postId)
        return persona.core.contracts.JsonCodec.decodeFromJsonElement(persona.core.contracts.LarkMomentsPost.serializer(), json)
    }

    override suspend fun momentsComments(postId: String): List<persona.core.contracts.LarkMomentsComment> {
        val transport = requireTransport()
        val json = transport.momentsComments(postId)
        return persona.core.contracts.JsonCodec.decodeFromJsonElement(persona.core.contracts.LarkMomentsCommentsResponse.serializer(), json).comments
    }

    override suspend fun urlPreview(messageId: String, url: String): persona.core.contracts.LarkUrlPreview? {
        val transport = requireTransport()
        return runCatching {
            val json = transport.urlPreview(messageId, url)
            persona.core.contracts.JsonCodec.decodeFromJsonElement(persona.core.contracts.LarkUrlPreview.serializer(), json)
        }.getOrNull()
    }
    private fun avatar(ref: String?, transport: InboxAssistantTransport) =
        larkAvatar(ref, transport.scope) { sized -> images.get("${transport.scope}:$sized") { transport.avatar(sized) } }
    private fun message(data: JsonObject, window: Window): LarkMessage {
        val source = LarkContentSource(window.transport)
        val id = data.string("id")
        val external = "https://applink.feishu.cn/client/chat/open?chatId=${java.net.URLEncoder.encode(window.feedId, "UTF-8")}&messageId=${java.net.URLEncoder.encode(id, "UTF-8")}"
        val parts = if (data.flag("recalled")) listOf(LarkPart.Text(DataText.of(Key.LarkRecalled))) else data.array("parts").map { element ->
            val part = element.jsonObject
            val metadata = part.obj("metadata") ?: JsonObject(emptyMap())
            val text = part.optional("text") ?: data.string("text")
            when (part.string("kind")) {
                "image", "emotion" -> {
                    val ref = part.string("url")
                    LarkPart.Image(AuthenticatedLarkImage("${window.transport.scope}:$ref") { images.get("${window.transport.scope}:$ref") { window.transport.media(ref) } },
                        width = metadata.number("width")?.toInt()?.coerceAtLeast(1) ?: 1,
                        height = metadata.number("height")?.toInt()?.coerceAtLeast(1) ?: 1)
                }
                "file" -> LarkPart.File(part.string("url"), metadata.optional("name") ?: text.ifBlank { DataText.of(Key.LarkFile) },
                    metadata.optional("mimeType") ?: "application/octet-stream")
                "text", "at", "code" -> LarkPart.Text(text, source)
                "card", "interactive" -> larkInteractivePart(metadata, text, window.transport)
                "link" -> LarkPart.Text(listOf(text, part.string("url")).filter { it.isNotBlank() }.distinct().joinToString("\n"), source)
                else -> LarkPart.External(text, external, if (part.string("kind") == "media") "video" else "card",
                    metadata.string("duration"))
            }
        }.ifEmpty { listOf(LarkPart.Text(data.string("text"), source)) }
        return LarkMessage(id, data.string("sender_id"), data.string("sender_name"), data.number("position"), data.string("created_at"),
            data.flag("is_mine"), parts, data.optional("client_id") ?: data.optional("clientId"),
            delivery = when (data.string("local_state")) {
                "failed" -> LarkDelivery.Failed
                "sending", "pending" -> LarkDelivery.Sending
                else -> LarkDelivery.Sent
            }, avatar = avatar(data.optional("sender_avatar"), window.transport),
            replyCount = maxOf(0L, data.number("reply_count") ?: 0L, data.number("thread_reply_count") ?: 0L),
            thread = larkThreadSource(data, window.transport, window.feedId),
            // A recalled message shows a placeholder, an encrypted one its ciphertext: neither is the sender's text.
            translatable = !data.flag("recalled") && (data["olk"] == null || data["olk"] is JsonNull))
    }
}

private fun JsonObject.strings(key: String): List<String> = array(key).mapNotNull { (it as? JsonPrimitive)?.contentOrNull }
