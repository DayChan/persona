package persona.feature.inbox

import persona.core.contracts.CoreText
import persona.core.domain.inbox.*

internal data class InboxConversationView(val list: InboxListResult, val conversations: List<InboxConversationRow>)

/** Group only items admitted by the current view. Actions must never acquire hidden events in the same feed. */
internal fun conversationInboxView(
    original: InboxListResult,
    conversations: InboxConversationList?,
    settings: InboxListSettings,
    pending: Map<String, Boolean>,
    fallbackTitle: CoreText,
): InboxConversationView {
    if (conversations == null) return InboxConversationView(original, emptyList())
    val matching = original.groups.flatMap { it.rows }.filter { it.type == InboxRowType.Item }.map { it.id }.toSet()
    val native = conversations.rows.mapNotNull { row ->
        val records = row.records.filter { it.item.id in matching }.map { record ->
            pending[record.item.id]?.let { done -> record.copy(item = record.item.copy(doneAt = if (done) java.time.Instant.EPOCH.toString() else null)) } ?: record
        }
        if (records.isEmpty()) return@mapNotNull null
        // Completion and source read receipts are independent. Keep unread conversations unread; reconcile the
        // already-read sections immediately, including while a write is pending and when it rolls back.
        val section = if (row.unread) row.section else when {
            records.all { it.item.isDone } -> InboxConversationSection.Done
            records.any { !it.item.isDone && it.item.triage == InboxTier.Action } -> InboxConversationSection.ReadPending
            else -> InboxConversationSection.Read
        }
        row.copy(records = records, section = section,
            messagePreview = row.messagePreview.takeIf { records.size == row.records.size })
    }
    val nativeIds = native.flatMap { it.records }.map { it.item.id }.toSet()
    val groups = listOf("action", "fyi", "read-pending", "noise", "done").mapNotNull { id ->
        val base = original.groups.firstOrNull { it.id == id }
        val added = native.filter { it.section != InboxConversationSection.Read && it.section.groupId == id }
            .map { InboxRow(InboxRowType.Item, it.itemId) }
        val rows = added + base?.rows.orEmpty().filterNot { it.type == InboxRowType.Item && it.id in nativeIds }
        if (rows.isEmpty()) null else InboxGroup(id, base?.title ?: fallbackTitle, rows,
            if (id in settings.expanded) rows else emptyList(), id !in settings.expanded)
    }
    return InboxConversationView(original.copy(groups = groups), native)
}
