package persona.feature.inbox

import androidx.compose.runtime.*
import androidx.compose.ui.res.stringResource
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.LocalDetailKey
import persona.core.navigation.Navigator

/** Grouped conversations keep the same directions and callbacks as individual inbox items. */
@Composable
internal fun InboxConversationItemRow(
    row: InboxConversationRow, navigator: Navigator, position: GroupPosition, onlineSince: Long?,
    done: Boolean, busy: Boolean, onDone: () -> Unit, onHandOff: () -> Unit,
) {
    val record = row.records.firstOrNull() ?: return
    val sender = record.item.sender?.takeIf { it.isNotBlank() }
    val group = row.records.firstNotNullOfOrNull { it.isGroup }
        ?: when (record.item.kind) { "dm" -> false; "group", "mention", "bot" -> true; else -> null }
    val conversationTitle = row.title
    val kindTag = group?.let { stringResource(if (it) R.string.inbox_kind_group else R.string.inbox_kind_dm) }
    val pages = LocalInboxItemPages.current
    var menu by remember(row.key) { mutableStateOf(false) }
    val doneLabel = stringResource(if (done) R.string.inbox_move_back else R.string.inbox_done)
    val handOffLabel = stringResource(R.string.inbox_hand_to_ai)
    if (menu) JActionSheet(title = row.title, onDismiss = { menu = false }, actions = listOf(
        JAction(doneLabel, PhIcons.Check, enabled = !busy, onClick = onDone),
        JAction(handOffLabel, PhIcons.Robot, enabled = !busy, onClick = onHandOff),
    ))
    JSwipeActions(
        start = if (busy) null else SwipeAction(doneLabel, PhIcons.Check, Status.Unread, onDone),
        end = if (busy) null else SwipeAction(handOffLabel, PhIcons.Robot, Status.Working, onHandOff),
    ) {
        MessageItem(sender = sender ?: conversationTitle, heading = conversationTitle.takeIf { sender != null },
            title = row.preview, position = position, tags = listOfNotNull(kindTag),
            source = MessageSource.Lark, sourceLabel = record.sourceLabel,
            unread = row.unreadCount ?: 0,
            status = when (row.section) { InboxConversationSection.Action -> Status.NeedsYou; InboxConversationSection.Fyi -> Status.Unread; else -> null },
            avatar = rememberLarkAvatar(record.avatar, onlineSince),
            selected = isInboxItemSelected(LocalDetailKey.current, row.itemId, row.key.thread),
            onClick = { if (!busy) navigator.navigate(pages.open(record)) }, onLongClick = { if (!busy) menu = true })
    }
}

internal val InboxConversationSection.groupId: String get() = when (this) {
    InboxConversationSection.Action -> "action"
    InboxConversationSection.Fyi -> "fyi"
    InboxConversationSection.ReadPending -> "read-pending"
    InboxConversationSection.Read -> "read"
    InboxConversationSection.Done -> "done"
}

