package persona.feature.inbox

import persona.core.domain.inbox.InboxInteraction
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentPreferences
import persona.core.domain.sort.AgentSession
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import java.util.UUID
import persona.core.contracts.SessionKey
import persona.core.contracts.LarkReaction
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.MailTransport
import persona.core.contracts.api.OutboxMessage
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf

private val emptyReplyComposers: StateFlow<ReplyComposers?> = MutableStateFlow(null)

private val disconnectedMail: StateFlow<MailTransport?> = MutableStateFlow(null)
private val emptyPersonaOutbox: StateFlow<List<OutboxMessage>> = MutableStateFlow(emptyList())
private val emptyConversationInbox: StateFlow<InboxConversations?> = MutableStateFlow(null)
private val emptyMessageReads: StateFlow<Set<InboxMessageRead>> = MutableStateFlow(emptySet())

/** Feature capabilities until the shared repository contract exposes inbox details. */
interface InboxRepository : persona.core.contracts.api.InboxRepository {
    val conversationInbox: StateFlow<InboxConversations?> get() = emptyConversationInbox
    val messageReads: StateFlow<Set<InboxMessageRead>> get() = emptyMessageReads
    val supportsThreadOutbox: Boolean get() = false
    val replyComposers: StateFlow<ReplyComposers?> get() = emptyReplyComposers
    suspend fun readingStore(): LarkReadingStore? = null
    val snapshot: StateFlow<InboxSnapshot>
    val mailTransport: StateFlow<MailTransport?> get() = disconnectedMail
    val personaOutbox: StateFlow<List<OutboxMessage>> get() = emptyPersonaOutbox
    val connectionState: Flow<ConnectionState> get() = flowOf(ConnectionState.Connecting)
    val preferences: Flow<InboxPreferences> get() = flowOf(InboxPreferences())
    suspend fun savePreferences(value: InboxPreferences) = Unit
    suspend fun refreshNow() = refresh()
    suspend fun askPersona(itemId: String, action: InboxAiAction) {
        error("Persona entry is not connected")
    }
    suspend fun askPersona(request: InboxPersonaRequest) = askPersona(request.itemId, request.action)
    suspend fun personaContext(itemId: String): String? = null

    /**
     * A reply in the user's voice drafted by the hub (`POST /api/v1/inbox/{id}/draft`); [instruction] may be empty.
     * The text only fills the reply for the user to edit: sending it is a separate, confirmed step.
     */
    suspend fun draftReply(itemId: String, instruction: String): String = error("AI 草拟尚未接入") // i18n-ignore: developer error for a repository without the hub

    /**
     * The user's answer to an item's suggestion (inbox phase 1, `POST /api/v1/inbox/{id}/suggestion`). The snapshot
     * takes the item the hub answers with, also when the suggestion was stale ([SuggestionOutcome.Stale]).
     */
    suspend fun actOnSuggestion(request: SuggestionRequest): SuggestionOutcome =
        error("建议尚未接入") // i18n-ignore: developer error for a repository without the hub

    /** "Hand to AI": the item as context with what to do, queued for Persona's main conversation or a running Agent. */
    suspend fun handOff(request: InboxHandOffRequest): SessionKey? = error("交给 AI 尚未接入") // i18n-ignore: developer error for a repository without the hub
    fun replyUpdates(target: ReplyTarget): Flow<Unit> = kotlinx.coroutines.flow.emptyFlow()
    suspend fun replyStatus(target: ReplyTarget, clientId: String): ReplyDelivery = ReplyDelivery.Ready
    suspend fun verifyReply(target: ReplyTarget, clientId: String): ReplyDelivery = ReplyDelivery.Unknown
    fun conversation(feedId: String, startPosition: Long?): Flow<LarkConversation>
    suspend fun loadOlder(feedId: String)
    suspend fun refreshConversation(feedId: String)
    suspend fun loadNewer(feedId: String) = refreshConversation(feedId)
    suspend fun latestConversation(feedId: String) = refreshConversation(feedId)
    suspend fun markVisible(feedId: String, messages: List<LarkMessage>): Boolean = false
    /**
     * 全部已读 / 下一条 on a conversation not loaded here: acknowledge its newest message from someone else, from one raw
     * page (no window, no card parsing). False when there is no Feishu transport or nothing to acknowledge.
     */
    suspend fun markLatestRead(feedId: String): Boolean = false
    suspend fun setDone(itemId: String, done: Boolean)
    /**
     * Mark all as seen / Clear all and their Undo: [items] done (or back) in one request, each as listed, so one
     * that took new messages since stays open ([InboxBulkResult.changedSince]).
     */
    suspend fun setDone(items: List<persona.core.contracts.api.InboxDoneTarget>, done: Boolean): InboxBulkResult =
        bulk(items, if (done) persona.core.contracts.api.InboxBulkAction.Done else persona.core.contracts.api.InboxBulkAction.Reopen)
    /** Clean up all (dismiss) / Mark all as seen / Clear all (done) / Move back (reopen) over [items] in one request ([setDone] is its done / reopen). */
    suspend fun bulk(items: List<persona.core.contracts.api.InboxDoneTarget>, action: persona.core.contracts.api.InboxBulkAction): InboxBulkResult =
        error("批量处理尚未接入") // i18n-ignore: developer error for a repository without the hub
    suspend fun readDraft(target: ReplyTarget): ReplyDraft?
    suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft)

    /** Persist to the scoped outbox before transport; encode clientId as client_id.
     * Retry with the same ID, reconcile only by client ID or server message ID, and
     * publish pending/confirmed bubbles through conversation. Inbox replies mark done.
     * Clear the stored draft only if its ID still matches this request after acceptance.
     */
    suspend fun reply(target: ReplyTarget, request: ReplyRequest)

    /** Download authenticated media and return a content URI suitable for a read grant. */
    suspend fun downloadFile(file: LarkPart.File): String

    suspend fun momentsPost(postId: String): persona.core.contracts.LarkMomentsPost = error("Moments post unavailable")
    suspend fun momentsComments(postId: String): List<persona.core.contracts.LarkMomentsComment> = emptyList()
    suspend fun urlPreview(messageId: String, url: String): persona.core.contracts.LarkUrlPreview? = null
    suspend fun react(messageId: String, key: String, remove: Boolean) = Unit
    suspend fun cardAction(messageId: String, actionId: String) = Unit
    suspend fun userProfile(userId: String, feedId: String? = null): kotlinx.serialization.json.JsonObject? = null
    suspend fun quickReactions(): List<String> = emptyList()
    suspend fun emojis(): kotlinx.serialization.json.JsonObject? = null

    /** "Muted" (docs/assistant/inbox.md "Mute"): what the user muted, in every source, newest first. */
    suspend fun inboxMutes(): List<InboxMute> = error("静音尚未接入") // i18n-ignore: developer error for a repository without the hub
    /** "Mute this repository / chat / sender": the item and the open ones of its source it covers are done at once; a failure puts them back. */
    suspend fun mute(record: InboxRecord, option: InboxMuteOption): Unit = error("静音尚未接入") // i18n-ignore: developer error
    suspend fun unmute(id: String): Unit = error("静音尚未接入") // i18n-ignore: developer error

    /** A chat's "Pinned messages", a page at a time ([accountId] null: the hub's current account, which the page names). */
    suspend fun larkChatPins(feedId: String, accountId: String?, cursor: String?): LarkChatPinsPage = error("Chat pins unavailable")
    /** Where a pin's message is: the message itself, and the thread's root when it is a reply in one ([topic]: a topic group). */
    suspend fun larkPinTarget(feedId: String, messageId: String, accountId: String, topic: Boolean): LarkPinTarget = error("Chat pins unavailable")
    /** "Add pin" ([pinId] null) or "Remove pin"; throws [LarkPinFailure] when the hub answers otherwise. */
    suspend fun larkChangePin(feedId: String, messageId: String, pinId: String?, accountId: String?): Unit = error("Chat pins unavailable")
    /** Read receipts of my messages; ones the Lark gateway has none for are left out. */
    suspend fun larkReadStates(feedId: String, messageIds: List<String>): List<LarkReadState> = emptyList()

    suspend fun larkMessageDisplay(): LarkMessageDisplay? = null
}

data class InboxSnapshot(
    val records: List<InboxRecord> = emptyList(),
    val interactions: List<InboxInteraction> = emptyList(),
    val sessions: List<AgentSession> = emptyList(),
    val mutes: AgentPreferences = AgentPreferences(),
    val loading: Boolean = false,
    val error: String? = null,
    val contentSource: LarkContentSource? = null,
    /** Machine names by id, for saying where an Agent runs. */
    val machines: Map<String, String> = emptyMap(),
    /** How triage stands: new items waiting outside the list (#1773); null from an older hub. */
    val triage: InboxTriage? = null,
    /** How many refreshes have succeeded. A new count says the records are the hub's whole list, even when it is empty. */
    val refreshes: Int = 0,
)

data class InboxRecord(
    val item: InboxItem,
    val sourceLabel: String = "",
    val feedId: String? = null,
    val startPosition: Long? = null,
    val count: Int = 0,
    val agent: InboxAgent? = null,
    val localStatus: String? = null,
    val localDelivery: persona.core.contracts.api.DeliveryState? = null,
    /** lark | mail (also lark for documents and posts without a chat). */
    val source: String = "",
    /** A Feishu mention of everyone (@all) rather than of the user. */
    val atAll: Boolean = false,
    val mail: InboxMailRef? = null,
    /** A saved reply that AI wrote ("AI draft"): its row shows it as a draft card, not as "Draft · …". */
    val aiDraft: Boolean = false,
    /** A Feishu chat's picture (the group's, or the other person's), at the thumbnail size. */
    val avatar: AuthenticatedLarkImage? = null,
    val read: Boolean = false,
    val moment: InboxMomentRef? = null,
    /** The hub's translations of a mail item, by target language, done as it came in (Settings › Translation). */
    val translations: Map<String, persona.core.contracts.api.ItemTranslation> = emptyMap(),
    /** A Feishu document's notice (`doc`): no chat behind it, its own page (InboxDocDetail.kt). */
    val doc: InboxDocRef? = null,
    /** A hub connector's item (GitHub, Telegram, an extension's): its thread opens as a conversation (ConnectorThread.kt). */
    val connector: InboxConnectorRef? = null,
    /** What it can be muted by (the row's menu: "Mute this repository / chat / sender"); an older hub sends none. */
    val mutes: List<InboxMuteOption> = emptyList(),
    /** A Feishu chat's kind (`lark.is_group` / `is_topic`); null for one listed or cached before the hub said. */
    val isGroup: Boolean? = null,
    /** The source conversation name, never the AI event title. */
    val groupName: String? = null,
    val isTopic: Boolean? = null,
    /** Inbox phase 1: Recap, the processing conversation, the suggestion, the Handling chain; none from an older hub. */
    val event: persona.core.contracts.InboxEventFields = persona.core.contracts.InboxEventFields.None,
    /** The manager whose inbox it is, in the hub's merged list (docs/protocol/protocol.md「Per-manager routing」); null: the default. */
    val manager: String? = null,
) {
    /** A hub connector's item (not Feishu's, not mail): it opens as its connector's thread. */
    val isConnector: Boolean get() = connector != null && !isLark && !isMail && moment == null

    val isLark: Boolean get() = feedId != null || source == "lark"
    /** A mail item: only these open the mail page; the kind says it too in records cached without their source. */
    val isMail: Boolean get() = mail != null || source == "mail" || (source.isEmpty() && item.kind == "mail")

    /** The item's id on the hub (its requests name it there; [InboxItemKey]). */
    val hubItemId: String get() = InboxItemKey.hubId(item.id)
}

/**
 * An inbox item's id in the app ([InboxItem.id]): the hub's for the hub's own assistant's items (unstamped, or stamped
 * `assistant`), and the hub's id with its manager for any other manager's (docs/assistant/managers.md "Clients"), so two managers
 * reading the same source have two items, and an id never moves to the other one's copy when the lists change. Rows,
 * records and local state (drafts, queued replies) go by it; requests take the hub's id back ([hubId]) and name the
 * manager.
 */
object InboxItemKey {
    private const val SEPARATOR = '\u001F'

    fun of(hubId: String, manager: String): String = "$hubId$SEPARATOR$manager"

    fun hubId(key: String): String = key.substringBefore(SEPARATOR)

    /** The app's id of the item [hubId] of [manager] (null: unstamped). */
    fun appId(hubId: String, manager: String?): String =
        if (manager.isNullOrBlank() || manager == persona.core.contracts.api.ManagerRoster.ASSISTANT_MANAGER) hubId else of(hubId, manager)
}

/**
 * The record with the app's id [id]; else, for an id that came from outside the lists (a notification, a link, an
 * extension's `persona://inbox/<id>`), the first with that id on the hub.
 */
fun List<InboxRecord>.inboxRecord(id: String?): InboxRecord? =
    if (id == null) null else firstOrNull { it.item.id == id } ?: firstOrNull { it.hubItemId == id }

/**
 * An item's `connector` block (docs/protocol/protocol.md "Inbox"): the [thread] its connector reads it as, the [label] the
 * connector gave it (the row's tag, its words: "Direct message", "@me", "Transfer"…), and whether and where a reply goes.
 */
@Serializable
data class InboxConnectorRef(
    val thread: String,
    val label: String? = null,
    val canReply: Boolean = false,
    val replyTo: String? = null,
)

/** A Moments post reference on an inbox item. */
data class InboxMomentRef(
    val postId: String,
    val notice: String? = null,
    val replyTo: String? = null,
    val myPost: Boolean = false,
    val postText: String? = null,
)

/** Where a mail item's messages are: what "Hand to AI" tells the agent so it can read them with its mail tools. */
data class InboxMailRef(
    val accountId: String,
    val mailbox: String = "INBOX",
    val uids: List<Long> = emptyList(),
    /** False when the user was only copied. */
    val direct: Boolean = true,
)

@Serializable
data class InboxAgent(
    val state: String,
    val draft: String = "",
    val basis: String = "",
    val task: String = "",
    val machineId: String? = null,
    val sessionId: String? = null,
)

@Serializable
sealed interface ReplyTarget {
    @Serializable data class Inbox(val itemId: String) : ReplyTarget
    @Serializable data class Lark(val feedId: String) : ReplyTarget
}

@Serializable
data class ReplyRequest(val text: String, @SerialName("client_id") val clientId: String,
    val mailTarget: persona.core.contracts.api.MailReplyTarget? = null)

/** [ai]: the text came from AI (the AI twin, or "AI draft"); sending it asks first, sending the user's own words does not. */
@Serializable
data class ReplyDraft(val text: String, val clientId: String = UUID.randomUUID().toString(), val ai: Boolean = false)

data class LarkConversation(
    val title: String = "",
    val messages: List<LarkMessage> = emptyList(),
    val loading: Boolean = false,
    val hasOlder: Boolean = false,
    val loadingOlder: Boolean = false,
    val error: String? = null,
    /** A failure the Lark window itself ran into, worded by the page. */
    val failure: LarkFailure? = null,
    val hasNewer: Boolean = false,
    val loadingNewer: Boolean = false,
    val verified: Boolean = false,
    val windowRevision: Long = 0,
    /** The chat's kind, from its inbox row; null when the inbox does not list it (read receipts go by the counts then). */
    val isGroup: Boolean? = null,
    /** A topic group: "Pinned messages" resolves threads with it. */
    val isTopic: Boolean = false,
)

enum class LarkFailure(@androidx.annotation.StringRes val text: Int) {
    NotPaired(R.string.inbox_lark_not_paired),
    Incomplete(R.string.inbox_lark_incomplete),
    LoadFailed(R.string.inbox_lark_messages_failed),
}

enum class LarkDelivery { Sent, Sending, Failed, WaitingForNetwork }

data class LarkMessage(
    val id: String,
    val senderId: String,
    val senderName: String,
    val position: Long? = null,
    val time: String = "",
    val mine: Boolean = false,
    val parts: List<LarkPart>,
    val clientId: String? = null,
    val delivery: LarkDelivery = LarkDelivery.Sent,
    val pendingDelivery: persona.core.contracts.api.DeliveryState? = null,
    val avatar: AuthenticatedLarkImage? = null,
    val bot: Boolean = false,
    val system: Boolean = false,
    val edited: Boolean = false,
    val quote: LarkQuote? = null,
    val badgeCount: Long? = null,
    val isRead: Boolean = false,
    val replyCount: Long = 0,
    val thread: LarkThreadSource? = null,
    val reactions: List<LarkReaction> = emptyList(),
    /** False for what is not the sender's own text (recalled, encrypted): Settings › Translation leaves it alone. */
    val translatable: Boolean = true,
    /** Recalled by its sender: it reads "Message recalled", unless "Keep recalled messages" shows what the Lark gateway kept ([retained], its parts). */
    val recalled: Boolean = false,
    /** A recalled message whose words the Lark gateway kept (shown only with message display › "Keep recalled messages" on). */
    val retained: Boolean = false,
    /** Its earlier versions, oldest first (shown only with message display › "Edit history" on). */
    val editHistory: List<LarkEditVersion> = emptyList(),
    /** The chat can pin it ("Add pin"), and unpin it. */
    val canPin: Boolean = false,
    val canUnpin: Boolean = false,
)

/** One earlier version of an edited message. */
data class LarkEditVersion(val editedAt: String, val text: String)

data class LarkQuote(val id: String, val sender: String, val text: String, val recalled: Boolean = false)

data class LarkForwardedMessage(
    val id: String = "",
    val senderName: String = "",
    val senderAvatar: AuthenticatedLarkImage? = null,
    val time: String = "",
    val text: String = "",
    val parts: List<LarkPart> = emptyList(),
)

sealed interface LarkPart {
    data class Text(val text: String, val source: LarkContentSource? = null) : LarkPart
    data class Card(val content: LarkCardContent, val source: LarkContentSource, val messageId: String? = null) : LarkPart
    // Models may carry an authenticated Coil ImageRequest supplied by the data layer.
    data class Image(val model: Any, val description: String = "", val width: Int = 1, val height: Int = 1) : LarkPart
    data class File(val ref: String, val name: String, val mimeType: String = "application/octet-stream") : LarkPart
    data class External(val title: String, val larkUrl: String, val kind: String, val duration: String = "", val cover: Image? = null) : LarkPart
    data class Code(val code: String, val language: String = "") : LarkPart
    data class Audio(val ref: String, val durationMs: Long, val source: LarkContentSource? = null) : LarkPart
    data class Video(val ref: String, val durationMs: Long, val cover: Image? = null, val source: LarkContentSource? = null) : LarkPart
    data class Sticker(val model: Any, val description: String = "", val width: Int = 1, val height: Int = 1) : LarkPart
    data class MergeForward(val title: String, val messages: List<LarkForwardedMessage>, val source: LarkContentSource? = null) : LarkPart
}

internal fun startsInboxItem(message: LarkMessage, previous: LarkMessage?, start: Long?): Boolean =
    start != null && message.position != null && message.position >= start &&
        (previous?.position == null || previous.position < start)

internal fun showsSender(message: LarkMessage, previous: LarkMessage?, boundary: Boolean): Boolean =
    boundary || previous == null || previous.senderId != message.senderId || previous.mine != message.mine

@Serializable
data class InboxPreferences(
    val source: String = "",
    val kind: String = "",
    val query: String = "",
    val section: String = "",
    val expanded: List<String> = listOf("action", "fyi"),
)

enum class InboxAiAction { Summarize, DraftReply, Handoff }
@Serializable
data class InboxPersonaRequest(
    val itemId: String,
    val action: InboxAiAction,
    val operationId: String = UUID.randomUUID().toString(),
    val context: String? = null,
)
@Serializable
enum class ReplyDelivery { Ready, Queued, WaitingForNetwork, Sending, Unknown, Verifying, Refused, NeedsConfirmation, Sent }

/**
 * One "Hand to AI". No [machineId] / [sessionId]: Persona's main conversation. [operationId] stays the same while the same
 * text goes to the same place, so a retry after an unclear failure is not queued twice.
 */
@Serializable
data class InboxHandOffRequest(
    val itemId: String,
    val text: String,
    val machineId: String? = null,
    val sessionId: String? = null,
    val operationId: String = UUID.randomUUID().toString(),
)

/**
 * One answer to a suggestion: [action] confirm (with [text] when the user changed it), reject, or instruct ([text] the
 * words). [clientId] stays the same while the same answer is retried, so the hub runs it once.
 */
@Serializable
data class SuggestionRequest(
    val itemId: String,
    val version: Long,
    val action: SuggestionAction,
    val text: String? = null,
    val clientId: String = UUID.randomUUID().toString(),
)

@Serializable
enum class SuggestionAction(val wire: String) { Confirm("confirm"), Reject("reject"), Instruct("instruct") }

enum class SuggestionOutcome { Done, Stale }
