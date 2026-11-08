package persona.feature.inbox

import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.material3.Text
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.*
import persona.core.navigation.LocalDetailKey
import persona.core.navigation.Navigator

@Composable
internal fun InboxItemRow(record: InboxRecord, repository: InboxRepository, navigator: Navigator,
    composers: ReplyComposers, position: GroupPosition, done: Boolean, busy: Boolean, onlineSince: Long?, onDone: () -> Unit,
    onHandOff: () -> Unit,
    onMute: (InboxMuteOption) -> Unit = {},
) {
    val item = record.item
    val groupEvent = record.feedId != null && record.isGroup != false && item.kind in setOf("group", "mention", "bot")
    val pages = LocalInboxItemPages.current
    val translated = rememberItemTranslation(record)
    var menu by remember { mutableStateOf(false) }
    val res = LocalResources.current
    val doneLabel = stringResource(if (done) R.string.inbox_move_back else R.string.inbox_done)
    val handOffLabel = stringResource(R.string.inbox_hand_to_ai)
    if (menu) JActionSheet(title = item.title, onDismiss = { menu = false }, actions = buildList {
        add(JAction(doneLabel, PhIcons.Check, enabled = !busy, onClick = onDone))
        add(JAction(handOffLabel, PhIcons.Robot, onClick = onHandOff))
        // What the item can be muted by, the name with each (iOS `rowMenu`): its repository, chat or sender.
        record.mutes.forEachIndexed { i, option ->
            add(JAction(res.getString(R.string.inbox_mute_option, res.getString(option.title), option.shortName), PhIcons.Prohibit,
                separatorBefore = i == 0) { onMute(option) })
        }
    })
    JSwipeActions(
        start = if (busy) null else SwipeAction(doneLabel, PhIcons.Check, Status.Unread, onDone),
        end = SwipeAction(handOffLabel, PhIcons.Robot, Status.Working, onHandOff),
    ) {
        MessageItem(sender = item.sender.orEmpty().ifBlank { item.title }, title = if (groupEvent) record.groupName ?: stringResource(R.string.inbox_kind_group)
                else translated?.subject?.ifBlank { null } ?: item.title,
            heading = item.title.takeIf { groupEvent },
            titleIcon = translated?.let { PhIcons.Translate }, titleIconLabel = translated?.let { stringResource(R.string.inbox_translated) },
            selected = isInboxItemSelected(LocalDetailKey.current, item.id, record.feedId),
            position = position, avatar = rememberLarkAvatar(record.avatar.takeIf { record.doc == null }, onlineSince), avatarIcon = record.doc?.let { docIcon(it.type) },
            // A hub connector's item is any connector's (#2726): a neutral mark and the hub's name for it, never a brand.
            source = when { record.isConnector -> MessageSource.Other; record.feedId != null || record.source == "lark" -> MessageSource.Lark; else -> MessageSource.Mail },
            sourceLabel = if (record.isConnector) record.sourceLabel
                else stringResource(if (record.feedId != null || record.source == "lark") R.string.inbox_source_lark else R.string.inbox_source_mail),
            group = record.groupName.takeIf { groupEvent },
            time = item.updatedAt?.let { displayTime(it, currentLocale) },
            preview = item.summary?.takeIf { it.isNotBlank() } ?: translated?.preview?.ifBlank { null }
                ?: record.doc?.let { listOf(docTypeName(res, it.type), item.preview).filter { s -> s.isNotBlank() }.joinToString(" · ") } ?: item.preview,
            status = when { done -> null; item.isWorking || record.agent?.state in setOf("running", "queued") -> Status.Working
                item.section == InboxTier.Action.wire -> Status.NeedsYou; else -> null },
            // The hub names its own sources Lark and Mail; the row already shows those as its source.
            tags = listOfNotNull(kindLabel(res, record).takeIf { it.isNotBlank() },
                record.sourceLabel.takeIf { !record.isConnector && it.isNotBlank() && it !in setOf("飞书", "邮件") }, // i18n-ignore: hub source labels it matches
                pluralStringResource(R.plurals.inbox_messages_count, record.count, record.count).takeIf { record.count > 0 }),
            // An extension may draw the page of its connector's items (inbox_item); the rest open their own page.
            // An event (inbox phase 1) opens its own page; an item from an older hub its source, as always.
            onClick = { navigator.navigate(inboxRowPage(record, pages)) }, onLongClick = { menu = true }, extra = {
                record.localDelivery?.pendingStatus()?.let { PendingBadge(it) }
                    ?: record.localStatus?.let { InboxLocalStatus(it) }
                if (!done) eventMark(record)?.let { EventRowMark(it) }
                if (!done) AgentDraftCard(record, composers, navigator, repository = repository)
            })
    }
}

@Composable
private fun InboxLocalStatus(label: String) {
    // A pending reply shows as its badge from [InboxRecord.localDelivery]; the real repository words the rest in the
    // shown language already, and the demo one writes the tokens above, which turn into the shown language here.
    val draft = label.removePrefix(LOCAL_DRAFT).takeIf { label.startsWith(LOCAL_DRAFT) }
    val shown = when {
        draft != null -> stringResource(R.string.inbox_local_draft, draft)
        label == LOCAL_UNCONFIRMED -> stringResource(R.string.inbox_local_unconfirmed)
        else -> label
    }
    Text(shown, style = PersonaTheme.type.micro,
        color = if (draft != null) PersonaTheme.palette.cautionText else PersonaTheme.palette.ink3)
}


/** A row's time in the language the app is in: "Nov 5, 2:30 PM" in English, the same in each language's own form. */
internal fun displayTime(value: String, locale: java.util.Locale, zone: java.time.ZoneId = java.time.ZoneId.systemDefault()): String =
    runCatching { LocalizedFormats.format(java.time.Instant.parse(value), LocalizedFormats.MONTH_DAY_TIME, locale, zone) }.getOrDefault("")


@Composable
internal fun ReplyStatus(state: ReplyComposerState) {
    state.delivery.pendingStatus()?.let { PendingBadge(it) }
        ?: if (state.sending) PendingBadge(PendingStatus.Sending) else Unit
    state.error?.let { JNotice(androidx.compose.ui.res.stringResource(it.text), status = Status.NeedsYou) }
}

/** As iOS InboxStore: filing an item as "Done" buzzes success at once, putting it back a light tap. */
internal fun doneHaptic(done: Boolean): Haptic = if (done) Haptic.Success else Haptic.Tap
