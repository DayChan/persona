package persona.android.capsule

import persona.android.AppText
import persona.android.R

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.launch
import persona.android.ui.ShellSession
import persona.core.contracts.contextBody
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InboxDoneTarget
import persona.feature.inbox.*
import persona.platform.capsule.*
import persona.core.contracts.SessionKey

/** A projection of the app-owned inbox; it never substitutes task completion for a read receipt. */
@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
internal class CapsuleInboxSource(
    private val session: ShellSession,
    private val scope: CoroutineScope,
    private val openDetail: (String?) -> Unit,
    private val noticeMemory: CapsuleNoticeMemory = CapsuleNoticeMemory(),
) : CapsuleContentSource {
    private data class SelectionRequest(val controller: InboxConversations, val key: InboxConversationKey)
    private data class Selection(val controller: InboxConversations, val binding: InboxConversationBinding) {
        val key get() = binding.key
    }
    private val requested = MutableStateFlow<SelectionRequest?>(null)
    private val expanded = MutableStateFlow(false)
    private val targets = MutableStateFlow<Map<InboxConversationKey, String>>(emptyMap())
    private val handOffAttempts = mutableMapOf<InboxConversationKey, HandOffAttempt>()
    private val handedOffSessions = MutableStateFlow<Map<InboxConversationKey, SessionKey>>(emptyMap())
    private val personaDrafts = MutableStateFlow<Map<InboxConversationKey, String>>(emptyMap())
    private val personaRequests = MutableStateFlow<Map<InboxConversationKey, Map<String, String>>>(emptyMap())
    private val context = MutableStateFlow<String?>(null)
    private val failure = MutableStateFlow<String?>(null)
    private val busy = MutableStateFlow(false)
    private var retryFyi: List<InboxReadSnapshot>? = null
    private val pendingNotice = MutableStateFlow(noticeMemory.pending)
    private val events = MutableSharedFlow<CapsuleCompletion>(extraBufferCapacity = 16)
    override val completions = events.asSharedFlow()
    private val controllers = session.inbox.conversationInbox
    private val rows = controllers.flatMapLatest { it?.state ?: flowOf(InboxConversationList()) }
        .stateIn(scope, SharingStarted.Eagerly, InboxConversationList())
    private val selection = controllers.flatMapLatest { controller ->
        if (controller == null) {
            requested.value?.let(::invalidate)
            flowOf(null)
        } else combine(controller.state.map { it.rows.map { row -> row.key }.toSet() }.distinctUntilChanged(), requested) { members, request ->
            if (request != null && (request.controller !== controller || request.key !in members)) {
                invalidate(request)
                null
            } else request
        }.distinctUntilChanged().map { request ->
            request?.let { selected ->
                controller.bind(selected.key)?.let { Selection(controller, it) } ?: run { invalidate(selected); null }
            }
        }
    }.stateIn(scope, SharingStarted.Eagerly, null)
    private val window = combine(selection, expanded) { selected, shown ->
        selected.takeIf { shown }
    }.distinctUntilChanged().flatMapLatest { target ->
        if (target == null) flowOf(null to LarkConversation())
        else target.binding.window.map { target to it }
    }.stateIn(scope, SharingStarted.Eagerly, null to LarkConversation())
    private val composer = selection.flatMapLatest { target ->
        if (target == null) flowOf(null to ReplyComposerState())
        else target.binding.composer.state.map { target to it }
    }.stateIn(scope, SharingStarted.Eagerly, null to ReplyComposerState())
    private val privateChat = combine(session.assistant.assistant, selection, targets, expanded, handedOffSessions) { assistant, selected, targets, shown, handedOff ->
        val key = selected?.key
        (handedOff[key] ?: assistant.mainSession)?.takeIf { shown && key != null && targets[key] == "persona" }
    }.distinctUntilChanged().flatMapLatest { key ->
        if (key == null) flowOf(ChatSnapshot(loading = false)) else session.chats.observe(key)
    }.stateIn(scope, SharingStarted.Eagerly, ChatSnapshot(loading = false))

    private fun entryId(key: InboxConversationKey) = "inbox:conversation:${key.source.length}:${key.source}${key.account.length}:${key.account}${key.thread}"
    private fun entry(row: InboxConversationRow): CapsuleEntry = CapsuleEntry(entryId(row.key), row.title, row.preview,
        if (row.section in setOf(InboxConversationSection.Done, InboxConversationSection.Read)) CapsuleState.Idle else CapsuleState.NeedsYou,
        actions = (if (row.section in setOf(InboxConversationSection.Fyi, InboxConversationSection.Read)) emptyList() else listOf(
            CapsuleAction(if (row.section == InboxConversationSection.Done) "undo" else "complete",
                if (row.section == InboxConversationSection.Done) AppText.get(R.string.app_capsule_undo_complete) else AppText.get(R.string.app_capsule_complete)))) + listOf(CapsuleAction("open", AppText.get(R.string.app_capsule_open_and_handle))),
        unread = row.unread, unreadCount = row.unreadCount,
        // The items' versions alone: the row's own version also folds in whichever window is loaded.
        version = row.records.sortedBy { it.item.id }.joinToString(";") { "${it.item.id}:${it.item.version.orEmpty()}" },
        group = when (row.section) {
            InboxConversationSection.Action -> "action"
            InboxConversationSection.Fyi -> "fyi"
            InboxConversationSection.ReadPending -> "read-pending"
            InboxConversationSection.Done -> "completed"
            InboxConversationSection.Read -> "read"
        })
    private fun messageText(message: LarkMessage): String = message.parts.joinToString("\n") { part -> when (part) {
        is LarkPart.Text -> part.text
        is LarkPart.Code -> part.code
        is LarkPart.File -> AppText.get(R.string.app_capsule_attachment, part.name)
        is LarkPart.Image -> part.description.ifBlank { AppText.get(R.string.app_capsule_image_full_chat) }
        is LarkPart.Card -> part.content.summary
        is LarkPart.External -> "${part.title}\n${part.larkUrl}"
        is LarkPart.Audio -> AppText.get(R.string.app_capsule_audio_full_chat)
        is LarkPart.Video -> AppText.get(R.string.app_capsule_video_full_chat)
        is LarkPart.Sticker -> part.description.ifBlank { AppText.get(R.string.app_capsule_sticker) }
        is LarkPart.MergeForward -> AppText.get(R.string.app_capsule_forwarded_history, part.title)
    } }
    private fun version(key: InboxConversationKey, row: InboxConversationRow, value: LarkConversation): String =
        "${entryId(key)}|${row.version}|${value.windowRevision}|" + value.messages.joinToString(";") { "${it.id.length}:${it.id}:${it.position}" }

    private data class View(val list: InboxConversationList, val selection: Selection?, val window: Pair<Selection?, LarkConversation>,
        val targets: Map<InboxConversationKey, String>, val composer: Pair<Selection?, ReplyComposerState>)
    private val view = combine(rows, selection, window, targets, composer) { list, selected, window, targets, composer ->
        View(list, selected, window, targets, composer)
    }
    override val content = combine(view, privateChat, personaRequests, personaDrafts, context) { view, chat, requests, drafts, context ->
        val key = view.selection?.key
        val row = view.list.rows.firstOrNull { it.key == key }
        val source = view.window.second.takeIf { view.window.first === view.selection } ?: LarkConversation()
        val replyComposer = view.composer.second.takeIf { view.composer.first === view.selection } ?: ReplyComposerState()
        val target = key?.let { view.targets[it] } ?: "source"
        val entries = view.list.rows.map(::entry) + view.list.originalRecords.map { record ->
            // Documents, moments and doc comments open in the app and have no read receipt here, so they stay out of
            // 未读: listed there, 全部已读 could never clear them.
            CapsuleEntry("inbox:item:${record.item.id}", record.item.title, record.item.preview, CapsuleState.NeedsYou,
                detailOnly = true)
        }
        val messages = if (target == "persona" && key != null) {
            val owned = requests[key].orEmpty()
            var include = false
            chat.items.mapNotNull { item ->
                if (item.role == "user") include = item.clientMsgId in owned
                if (include && item.kind == "text" && !item.text.isNullOrBlank())
                    CapsuleMessage(item.id, owned[item.clientMsgId] ?: item.text.orEmpty(), item.role == "user", sender = if (item.role == "user") null else "Persona")
                else null
            }
        } else source.messages.filterNot { it.system }.map { message -> CapsuleMessage(message.id, messageText(message), message.mine,
            unread = !message.mine && !message.isRead && row?.unread == true, sender = message.senderName.takeUnless { message.mine }) }
        CapsuleContent(entries = entries, selectedId = key?.let(::entryId), messages = messages,
            canSend = row != null && if (target == "source") !replyComposer.loading && replyComposer.canEdit else true,
            available = true, sectionId = InboxSection, sections = listOf(CapsuleSection(InboxSection, AppText.get(R.string.app_capsule_inbox))),
            unreadEntries = entries.filter { it.unread }, badgeCount = entries.count { it.unread },
            hasActivity = view.list.rows.any { it.section !in setOf(InboxConversationSection.Done, InboxConversationSection.Read) },
            readVersion = if (key != null && row != null && row.unread && target == "source" && source.verified && !source.loading && source.error == null && source.failure == null && !source.hasNewer)
                version(key, row, source) else null,
            recipient = row?.title, confirmSend = target == "source", replyTargetId = target,
            replyTargets = if (row != null) listOf(CapsuleReplyTarget("source", AppText.get(R.string.app_capsule_original_chat)), CapsuleReplyTarget("persona", "Persona")) else emptyList(),
            draftText = if (key == null) null else if (target == "persona") drafts[key].orEmpty() else replyComposer.draft.text,
            context = context,
            canLoadOlder = if (target == "persona") chat.hasOlder else source.hasOlder,
            error = source.error ?: replyComposer.error?.let { AppText.get(R.string.app_capsule_reply_pending_check) },
            noticeTargetIds = entries.filter { it.unread }.map { it.id }.toSet())
    }.combine(pendingNotice) { value, notice ->
        val target = value.entries.firstOrNull { it.id == notice?.targetId && it.unread }
        value.copy(pendingCompletion = if (notice != null && target != null)
            CapsuleCompletion(notice.id, target.id, target.title, notice.kind) else null)
    }.combine(busy) { value, busy -> value.copy(busy = busy) }
        .combine(failure) { value, failure -> value.copy(error = failure ?: value.error) }
        .stateIn(scope, SharingStarted.Eagerly, CapsuleContent(available = true, sectionId = InboxSection))

    init {
        scope.launch {
            val notices = CapsuleInboxNotices()
            combine(rows, session.inbox.snapshot) { list, snapshot -> list to snapshot }.collect { (list, snapshot) ->
                val expected = snapshot.records.map { it.item.id }.toSet()
                val grouped = (list.rows.flatMap { it.records } + list.originalRecords).map { it.item.id }.toSet()
                val ready = !snapshot.loading && snapshot.refreshes > 0 && expected == grouped
                notices.update(list.rows, ready, ::entryId).forEach {
                    val pending = CapsulePendingNotice(it.id, it.targetId, it.kind, "inbox")
                    noticeMemory.pending = pending
                    pendingNotice.value = pending
                    events.emit(it)
                }
            }
        }
    }
    private fun invalidate(request: SelectionRequest) {
        if (!requested.compareAndSet(request, null)) return
        targets.update { it - request.key }
        context.value = null
        failure.value = null
        retryFyi = null
    }
    private fun currentSelection(): Selection? {
        val selected = selection.value ?: return null
        val request = requested.value ?: return null
        if (controllers.value !== selected.controller || request.controller !== selected.controller || request.key != selected.key ||
            selected.controller.state.value.rows.none { it.key == selected.key }) return null
        return selected
    }
    override fun shown(expanded: Boolean) { this.expanded.value = expanded }
    override fun selectSection(id: String) { require(id == InboxSection); select(null) }
    override fun select(id: String?) {
        requested.value = id?.let { id ->
            val controller = controllers.value ?: error(AppText.get(R.string.app_capsule_connect_first))
            val key = controller.state.value.rows.firstOrNull { entryId(it.key) == id }?.key
                ?: error(AppText.get(R.string.app_capsule_changed_return_list))
            SelectionRequest(controller, key)
        }
        context.value = null
        failure.value = null
    }
    override fun open(id: String?) {
        val item = rows.value.rows.firstOrNull { entryId(it.key) == id }?.itemId
            ?: rows.value.originalRecords.firstOrNull { "inbox:item:${it.item.id}" == id }?.item?.id
        check(id == null || item != null) { AppText.get(R.string.app_capsule_item_changed_return_list) }
        openDetail(item)
    }
    override fun openCompletion(completion: CapsuleCompletion) {
        check(completion.targetId in content.value.noticeTargetIds.orEmpty()) { AppText.get(R.string.app_capsule_message_notice_gone) }
        select(completion.targetId)
    }
    override fun noticeShown(id: String) {
        if (pendingNotice.value?.id == id) clearPending()
    }
    fun clearPending() { noticeMemory.clearPending(); pendingNotice.value = null }
    override fun updateDraft(id: String?, text: String) {
        val selected = currentSelection() ?: return
        val key = selected.key
        if (entryId(key) != id) return
        if (targets.value[key] == "persona") personaDrafts.update { it + (key to text) }
        else selected.binding.composer.update(text)
    }
    override suspend fun viewed(id: String?, messageId: String, version: String): Boolean {
        val selected = currentSelection() ?: return false
        val key = selected.key
        if (!expanded.value || entryId(key) != id || content.value.readVersion != version) return false
        val value = window.value.takeIf { it.first === selected }?.second ?: return false
        if (value.messages.lastOrNull { !it.mine && !it.system }?.id != messageId) return false
        val controller = selected.controller
        val captured = controller.capture(key, value.messages) ?: return false
        return controller.read(captured).also { accepted ->
            if (accepted && pendingNotice.value?.targetId == id) clearPending()
        }
    }
    override suspend fun send(id: String?, text: String) {
        val selected = currentSelection() ?: error(AppText.get(R.string.app_capsule_choose_chat))
        val key = selected.key
        check(entryId(key) == id && text.isNotBlank()) { AppText.get(R.string.app_capsule_changed) }
        val row = selected.controller.state.value.rows.firstOrNull { it.key == key } ?: error(AppText.get(R.string.app_capsule_closed))
        if (targets.value[key] == "persona") {
            val loaded = window.value.takeIf { it.first === selected }?.second
            check(loaded?.verified == true) { AppText.get(R.string.app_capsule_body_unsynced) }
            val quoted = loaded.messages.joinToString("\n") { "${it.senderName}: ${messageText(it)}" }
            val request = "<persona-context>\n${contextBody(row.title + "\n" + quoted)}\n</persona-context>\n$text"
            val attempt = handOffAttempts.getOrPut(key) { HandOffAttempt() }
            val operation = attempt.operationId(request, null)
            personaRequests.update { it + (key to (it[key].orEmpty() + (operation to text))) }
            val destination = session.inbox.handOff(InboxHandOffRequest(row.itemId, request, operationId = operation))
            attempt.done()
            handedOffSessions.update { if (destination == null) it - key else it + (key to destination) }
            personaDrafts.update { if (it[key] == text) it - key else it }
        } else {
            val composer = selected.binding.composer
            composer.state.first { !it.loading }
            currentCoroutineContext().ensureActive()
            check(currentSelection() === selected) { AppText.get(R.string.app_error_switched) }
            check(composer.state.value.canEdit) { AppText.get(R.string.app_capsule_prior_reply_check) }
            composer.update(text)
            composer.send()
            val result = composer.state.first { !it.sending }
            check(result.sent || result.delivery in setOf(ReplyDelivery.Queued, ReplyDelivery.WaitingForNetwork)) {
                AppText.get(R.string.app_capsule_send_unconfirmed_draft)
            }
        }
    }
    override suspend fun perform(id: String?, actionId: String) {
        if (actionId.startsWith("target:")) {
            val key = currentSelection()?.key ?: return
            check(entryId(key) == id)
            val target = actionId.removePrefix("target:")
            require(target == "source" || target == "persona")
            targets.update { it + (key to target) }
            context.value = null
            return
        }
        if (actionId == "read-unread") {
            val controller = controllers.value ?: error(AppText.get(R.string.app_capsule_connect_first))
            if (busy.value) return
            busy.value = true
            try {
                val result = controller.readAll(setOf(InboxConversationSection.Action, InboxConversationSection.Fyi))
                failure.value = if (result.failed.isNotEmpty()) AppText.get(R.string.app_capsule_read_partial_failed)
                    else if (result.skipped.isNotEmpty()) AppText.get(R.string.app_capsule_read_partial_skipped)
                    else null
            } finally { busy.value = false }
            return
        }
        if (actionId == "seen") {
            val selected = currentSelection()?.takeIf { entryId(it.key) == id } ?: return
            if (!selected.controller.readConversation(selected.key)) error(AppText.get(R.string.app_capsule_read_sync_failed))
            return
        }
        if (actionId == "read-fyi") {
            val controller = controllers.value ?: error(AppText.get(R.string.app_capsule_connect_first))
            if (busy.value) return
            busy.value = true
            try {
                // A retry resends only what failed; otherwise every 仅需知悉 conversation is loaded and read in turn.
                val result = retryFyi?.let { controller.readFyi(it) } ?: controller.readAllFyi()
                retryFyi = result.failed.takeIf { it.isNotEmpty() }
                failure.value = if (result.failed.isNotEmpty()) AppText.get(R.string.app_capsule_read_partial_failed)
                    else if (result.skipped.isNotEmpty()) AppText.get(R.string.app_capsule_read_partial_skipped)
                    else null
            } finally { busy.value = false }
            return
        }
        val selected = currentSelection() ?: return
        val key = selected.key
        check(entryId(key) == id)
        when (actionId) {
            "complete", "undo" -> {
                val row = rows.value.rows.firstOrNull { it.key == key } ?: error(AppText.get(R.string.app_capsule_changed))
                val done = actionId == "complete"
                val result = session.inbox.setDone(row.records.map { InboxDoneTarget(it.item.id, it.item.version) }, done)
                if (result.changedSince.isNotEmpty()) error(AppText.get(R.string.app_capsule_item_updated_check))
            }
            "context" -> {
                if (targets.value[key] == "persona") (handedOffSessions.value[key] ?: session.assistant.assistant.value.mainSession)?.let { session.chats.loadOlder(it) }
                else session.inbox.loadOlder(key.thread)
                currentCoroutineContext().ensureActive()
                if (currentSelection() !== selected) return
                val loaded = window.value.takeIf { it.first === selected }?.second ?: return
                context.value = loaded.messages.joinToString("\n\n") { "${it.senderName}\n${messageText(it)}" }
            }
            "open" -> open(id)
            else -> error(AppText.get(R.string.app_capsule_action_unavailable))
        }
    }
}
