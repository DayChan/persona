package persona.feature.inbox

import persona.core.contracts.api.InboxBulkAction
import persona.core.contracts.api.InboxDoneTarget
import persona.core.contracts.api.ManagerPins
import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.withContext
import kotlin.coroutines.CoroutineContext
import kotlin.coroutines.EmptyCoroutineContext

/** This repository with [pins] on every request made through it (an item page's own); null pins: this one. */
fun InboxRepository.pinned(pins: ManagerPins?): InboxRepository =
    if (pins == null) this else PinnedInboxRepository((this as? PinnedInboxRepository)?.base ?: this, pins)

/**
 * The pins of the page this repository was made for (none: empty), for work it starts elsewhere: a chat's composer is
 * shared by every page of that chat and sends in the pairing's scope.
 */
internal val InboxRepository.pagePins: CoroutineContext get() = (this as? PinnedInboxRepository)?.pins ?: EmptyCoroutineContext

internal class PinnedInboxRepository(val base: InboxRepository, val pins: ManagerPins) : InboxRepository by base {
    override fun conversation(feedId: String, startPosition: Long?) = base.conversation(feedId, startPosition).flowOn(pins)

    override suspend fun refresh() = withContext(pins) { base.refresh() }
    override suspend fun readingStore() = withContext(pins) { base.readingStore() }
    override suspend fun savePreferences(value: InboxPreferences) = withContext(pins) { base.savePreferences(value) }
    override suspend fun refreshNow() = withContext(pins) { base.refreshNow() }
    override suspend fun askPersona(itemId: String, action: InboxAiAction) = withContext(pins) { base.askPersona(itemId, action) }
    override suspend fun askPersona(request: InboxPersonaRequest) = withContext(pins) { base.askPersona(request) }
    override suspend fun personaContext(itemId: String) = withContext(pins) { base.personaContext(itemId) }
    override suspend fun draftReply(itemId: String, instruction: String) = withContext(pins) { base.draftReply(itemId, instruction) }
    override suspend fun actOnSuggestion(request: SuggestionRequest) = withContext(pins) { base.actOnSuggestion(request) }
    override suspend fun handOff(request: InboxHandOffRequest) = withContext(pins) { base.handOff(request) }
    override suspend fun replyStatus(target: ReplyTarget, clientId: String) = withContext(pins) { base.replyStatus(target, clientId) }
    override suspend fun verifyReply(target: ReplyTarget, clientId: String) = withContext(pins) { base.verifyReply(target, clientId) }
    override suspend fun loadOlder(feedId: String) = withContext(pins) { base.loadOlder(feedId) }
    override suspend fun refreshConversation(feedId: String) = withContext(pins) { base.refreshConversation(feedId) }
    override suspend fun loadNewer(feedId: String) = withContext(pins) { base.loadNewer(feedId) }
    override suspend fun latestConversation(feedId: String) = withContext(pins) { base.latestConversation(feedId) }
    override suspend fun markVisible(feedId: String, messages: List<LarkMessage>) = withContext(pins) { base.markVisible(feedId, messages) }
    override suspend fun markLatestRead(feedId: String) = withContext(pins) { base.markLatestRead(feedId) }
    override suspend fun setDone(itemId: String, done: Boolean) = withContext(pins) { base.setDone(itemId, done) }
    override suspend fun setDone(items: List<InboxDoneTarget>, done: Boolean) = withContext(pins) { base.setDone(items, done) }
    override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction) = withContext(pins) { base.bulk(items, action) }
    override suspend fun doneHistory(manager: String?, cursor: InboxDoneCursor?) = withContext(pins) { base.doneHistory(manager, cursor) }
    override suspend fun doneDays(manager: String?, zone: java.time.ZoneId) = withContext(pins) { base.doneDays(manager, zone) }
    override suspend fun readDraft(target: ReplyTarget) = withContext(pins) { base.readDraft(target) }
    override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = withContext(pins) { base.saveDraft(target, draft) }
    override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = withContext(pins) { base.reply(target, request) }
    override suspend fun downloadFile(file: LarkPart.File) = withContext(pins) { base.downloadFile(file) }
    override suspend fun momentsPost(postId: String, manager: String?) = withContext(pins) { base.momentsPost(postId, manager) }
    override suspend fun momentsComments(postId: String, manager: String?) = withContext(pins) { base.momentsComments(postId, manager) }
    override suspend fun urlPreview(messageId: String, url: String) = withContext(pins) { base.urlPreview(messageId, url) }
    override suspend fun react(messageId: String, key: String, remove: Boolean) = withContext(pins) { base.react(messageId, key, remove) }
    override suspend fun cardAction(messageId: String, actionId: String) = withContext(pins) { base.cardAction(messageId, actionId) }
    override suspend fun userProfile(userId: String, feedId: String?) = withContext(pins) { base.userProfile(userId, feedId) }
    override suspend fun quickReactions() = withContext(pins) { base.quickReactions() }
    override suspend fun emojis() = withContext(pins) { base.emojis() }
    override suspend fun inboxMutes(manager: String?) = withContext(pins) { base.inboxMutes(manager) }
    override suspend fun mute(record: InboxRecord, option: InboxMuteOption) = withContext(pins) { base.mute(record, option) }
    override suspend fun unmute(id: String, manager: String?) = withContext(pins) { base.unmute(id, manager) }
    override suspend fun inboxPolicies(record: InboxRecord?, manager: String?) = withContext(pins) { base.inboxPolicies(record, manager) }
    override suspend fun savePolicy(target: InboxPolicyTarget, tier: InboxPolicyTier, twin: InboxPolicyTwin) =
        withContext(pins) { base.savePolicy(target, tier, twin) }
    override suspend fun removePolicy(id: String, manager: String?) = withContext(pins) { base.removePolicy(id, manager) }
    override suspend fun larkChatPins(feedId: String, accountId: String?, cursor: String?) =
        withContext(pins) { base.larkChatPins(feedId, accountId, cursor) }
    override suspend fun larkPinTarget(feedId: String, messageId: String, accountId: String, topic: Boolean) =
        withContext(pins) { base.larkPinTarget(feedId, messageId, accountId, topic) }
    override suspend fun larkChangePin(feedId: String, messageId: String, pinId: String?, accountId: String?) =
        withContext(pins) { base.larkChangePin(feedId, messageId, pinId, accountId) }
    override suspend fun larkReadStates(feedId: String, messageIds: List<String>) = withContext(pins) { base.larkReadStates(feedId, messageIds) }
    override suspend fun larkMessageDisplay() = withContext(pins) { base.larkMessageDisplay() }
}
