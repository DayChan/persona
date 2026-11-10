package persona.feature.settings

import android.content.ClipData
import android.content.ClipboardManager
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.api.HubSent
import persona.core.contracts.api.SentItem
import persona.core.contracts.api.SentWire
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.launch
import java.time.Instant

// Settings › Sent as you (#5278, docs/extensions/connectors.md "Sent as the user"): what Persona wrote in the user's name through the
// connectors, newest first. A row has the connector, where it went, how long ago, the first lines of what it said, how the gate
// let it through (orange where the user did not decide) and the chat that asked, which opens. A row opens a sheet with every
// field and the whole text, and Recall (asked once in a dialog) when the hub can take the message back. Load earlier follows `next`; the chips (Lark and the connectors the loaded rows show) filter by
// connector. The logic and the words are in SentState.kt; the web page is SentSettings.tsx, iOS's SentView.swift.

/**
 * The page over [sent] (null in demo mode). [onChat] opens the chat a row names (machine id, session id); without it a row has
 * no link to one.
 */
@Composable
internal fun SentAsYouPage(sent: HubSent?, onBack: () -> Unit, onChat: ((machineId: String, sessionId: String) -> Unit)?) {
    if (sent == null) {
        JListPage(title = stringResource(R.string.settings_sent_title), onBack = onBack) { item { JNotice(stringResource(R.string.settings_sent_demo)) } }
        return
    }
    val model = remember(sent) { SentModel(sent) }
    val state by model.state.collectAsState()
    val scope = rememberCoroutineScope()
    var refreshing by remember(model) { mutableStateOf(false) }
    var detail by remember(model) { mutableStateOf<SentItem?>(null) }
    LaunchedEffect(model) { model.load() }
    // How long ago is read when the rows come in or the page is drawn again, not every minute.
    val now = remember(state.items) { Instant.now() }
    SentContent(state, now, onBack, refreshing,
        onRefresh = { scope.launch { refreshing = true; try { model.load() } finally { refreshing = false } } },
        onRetry = { scope.launch { model.retry() } },
        onChoose = { scope.launch { model.choose(it) } },
        onMore = { scope.launch { model.loadMore() } },
        onOpen = { detail = it }, onChat = onChat)
    // The open row as the list has it now: a recall or a refresh changes it under the sheet.
    detail?.let { open -> state.items?.firstOrNull { it.id == open.id } ?: open }?.let { item ->
        JSheet(onDismiss = { detail = null }, title = SentText.connectorName(LocalResources.current, item.connector).ifBlank { null }, scrollable = true) {
            // A chat opens under the sheet's page: the sheet goes first.
            SentDetailBody(item, onChat = onChat?.let { go -> { machine, session -> detail = null; go(machine, session) } }, onDone = { detail = null },
                recalling = state.recalling == item.id, recallError = state.recallError?.takeIf { it.id == item.id }?.text,
                onRecall = { scope.launch { model.recall(item.id) } })
        }
    }
}

/** The page as drawn for [state] at [now] (the pictures draw this with fixed data). */
@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun SentContent(
    state: SentState, now: Instant, onBack: () -> Unit, refreshing: Boolean = false, onRefresh: () -> Unit, onRetry: () -> Unit,
    onChoose: (String?) -> Unit, onMore: () -> Unit, onOpen: (SentItem) -> Unit, onChat: ((String, String) -> Unit)?,
) {
    val res = LocalResources.current
    JListPage(title = stringResource(R.string.settings_sent_title), onBack = onBack, refreshing = refreshing, onRefresh = onRefresh) {
        if (state.showsChips) item("chips") {
            val label = stringResource(R.string.settings_sent_filter_label)
            FlowRow(Modifier.fillMaxWidth().padding(vertical = Tokens.Space.s).semantics { contentDescription = label },
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JChip(stringResource(R.string.settings_sent_all), selected = state.filter == null, onClick = { onChoose(null) })
                state.choices.forEach { id -> JChip(SentText.connectorName(res, id), selected = state.filter == id, onClick = { onChoose(id) }) }
            }
        }
        val problem = state.problem
        val rows = state.items
        when {
            problem != null -> item("problem") { SentProblemNotice(problem, onRetry) }
            rows == null -> item("loading") { JLoading() }
            rows.isEmpty() -> item("empty") { SentEmpty(state.filter, onShowAll = { onChoose(null) }) }
            else -> {
                itemsIndexed(rows, key = { _, it -> "sent-${it.id}" }) { index, row ->
                    JGroupItem(GroupPosition.of(index, rows.size), if (index == 0) Modifier.padding(top = Tokens.Space.s) else Modifier) {
                        SentRow(row, now, onOpen = { onOpen(row) }, onChat = onChat)
                    }
                }
                item("intro") { SettingsCaption(stringResource(R.string.settings_sent_intro), Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s)) }
                if (state.next != null) item("more") {
                    Column(Modifier.fillMaxWidth().padding(top = Tokens.Space.s), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        JButton(stringResource(R.string.settings_sent_load_earlier), onMore, kind = JButtonKind.Secondary, size = JButtonSize.Medium,
                            loading = state.loadingMore)
                        state.moreError?.let { JInlineStatus(it.text(), tone = InlineTone.Failed) }
                    }
                }
            }
        }
    }
}

/**
 * The hub's three answers that say what is wrong in a line (no ledger, no full access, an older hub) read plainly; only a
 * failure is red, with Retry (as web and iOS).
 */
@Composable
private fun SentProblemNotice(problem: SentProblem, onRetry: () -> Unit) {
    val palette = PersonaTheme.palette
    val failed = problem.kind == SentProblem.Kind.Error
    JCard(Modifier.padding(top = Tokens.Space.s)) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Text(problem.text.text(), style = PersonaTheme.type.callout, color = if (failed) palette.danger.text else palette.ink2)
            if (failed) JButton(stringResource(R.string.settings_src_retry), onRetry, kind = JButtonKind.Secondary, size = JButtonSize.Small, icon = PhIcons.ArrowsClockwise)
        }
    }
}

/** Nothing recorded: what is and is not recorded, or under a chip, that connector has nothing (Show all). */
@Composable
private fun SentEmpty(filter: String?, onShowAll: () -> Unit) {
    val palette = PersonaTheme.palette
    JCard(Modifier.padding(top = Tokens.Space.s)) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (filter != null) {
                Text(stringResource(R.string.settings_sent_empty_filtered, SentText.connectorName(LocalResources.current, filter)), style = PersonaTheme.type.callout, color = palette.ink2)
                JButton(stringResource(R.string.settings_sent_show_all), onShowAll, kind = JButtonKind.Secondary, size = JButtonSize.Small)
            } else {
                Text(stringResource(R.string.settings_sent_empty_title), style = PersonaTheme.type.headline, color = palette.ink)
                Text(stringResource(R.string.settings_sent_empty_recorded), style = PersonaTheme.type.callout, color = palette.ink2)
                Text(stringResource(R.string.settings_sent_empty_not_recorded), style = PersonaTheme.type.callout, color = palette.ink2)
                Text(stringResource(R.string.settings_sent_empty_days), style = PersonaTheme.type.callout, color = palette.ink2)
            }
        }
    }
}

private fun SentTone.tone(): Tone = if (this == SentTone.Orange) Tone.Caution else Tone.Neutral

/** A send in the list: the connector, where it went, when, its first lines, how it was let through and who asked. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun SentRow(item: SentItem, now: Instant, onOpen: () -> Unit, onChat: ((String, String) -> Unit)?) {
    val res = LocalResources.current
    val palette = PersonaTheme.palette
    val name = SentText.connectorName(res, item.connector)
    val target = SentText.targetText(item)
    val lines = SentText.previewLines(item)
    val allowed = SentText.allowedBy(res, item.letThrough)
    val origin = SentText.originInfo(item.origin)
    // What names the chat link for a screen reader (every row has the same words): the connector and where it went.
    val what = if (target == "—") name else "$name $target"
    Column(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Column(Modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onOpen), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Row(verticalAlignment = Alignment.Bottom, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Text(name, Modifier.widthIn(max = Tokens.Space.xxxl * 6), style = PersonaTheme.type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(target, Modifier.weight(1f), style = PersonaTheme.type.callout, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(SentText.ago(res, item.at, now), style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1)
            }
            if (lines.isEmpty()) Text(stringResource(R.string.settings_sent_no_text), style = PersonaTheme.type.callout, color = palette.ink3)
            else lines.forEach { Text(it, style = PersonaTheme.type.callout, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis) }
        }
        FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JTag(allowed.label, allowed.tone.tone(), Modifier.align(Alignment.CenterVertically))
            if (item.recalledAt.isNotEmpty()) JTag(stringResource(R.string.settings_sent_recalled), Modifier.align(Alignment.CenterVertically))
            if (origin.extensionId.isNotEmpty()) {
                JTag(stringResource(R.string.settings_sent_origin_extension), Modifier.align(Alignment.CenterVertically))
                Text(origin.extensionId, Modifier.align(Alignment.CenterVertically), style = PersonaTheme.type.codeSmall, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            if (origin.hasChat) {
                if (onChat != null) {
                    val description = stringResource(R.string.settings_sent_open_chat_row, what)
                    JButton(stringResource(R.string.settings_sent_open_chat), { onChat(origin.machine, origin.session) },
                        Modifier.align(Alignment.CenterVertically).semantics { contentDescription = description },
                        kind = JButtonKind.Quiet, size = JButtonSize.Small)
                }
            } else if (origin.extensionId.isEmpty()) {
                Text(stringResource(R.string.settings_sent_origin_none), Modifier.align(Alignment.CenterVertically), style = PersonaTheme.type.caption, color = palette.ink3)
            }
        }
    }
}

/** A label over its value in the details. */
@Composable
private fun SentFact(label: String, content: @Composable ColumnScope.() -> Unit) {
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
        Text(label, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
        content()
    }
}

/**
 * One send in full: when, the action, every field of the target, how it was let through (the rule, the card), the chat, the
 * message id when there is one, when it was recalled, and the whole text, which can be copied; Recall when the hub can take it
 * back ([onRecall], after a dialog), then Done.
 */
@Composable
internal fun ColumnScope.SentDetailBody(
    item: SentItem, onChat: ((String, String) -> Unit)?, onDone: () -> Unit,
    recalling: Boolean = false, recallError: ResText? = null, onRecall: (() -> Unit)? = null,
) {
    val res = LocalResources.current
    val palette = PersonaTheme.palette
    val fields = SentText.targetFields(item.target)
    val allowed = SentText.allowedBy(res, item.letThrough)
    val origin = SentText.originInfo(item.origin)
    val value = PersonaTheme.type.body
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.l)) {
        // The order the web's details have: when, the action, where it went, how it was let through, who asked, the text.
        SentFact(stringResource(R.string.settings_sent_d_sent)) { Text(SentText.full(res, item.at), style = value, color = palette.ink) }
        SentFact(stringResource(R.string.settings_sent_d_action)) {
            JSelectableText(item.action.ifEmpty { "—" }, PersonaTheme.type.code, palette.ink)
        }
        SentFact(stringResource(R.string.settings_sent_d_target)) {
            if (fields.isEmpty()) Text("—", style = value, color = palette.ink3)
            else Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                fields.forEach { field ->
                    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                        Text(field.key, Modifier.width(Tokens.Space.xxxl * 3), style = PersonaTheme.type.codeSmall, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        JSelectableText(field.value, value, palette.ink, Modifier.weight(1f))
                    }
                }
            }
        }
        SentFact(stringResource(R.string.settings_sent_d_allowed)) {
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                JTag(allowed.label, allowed.tone.tone())
                if (allowed.detail.isNotEmpty()) Text(allowed.detail, style = PersonaTheme.type.callout, color = palette.ink2)
            }
        }
        if (item.letThrough.interactionId.isNotEmpty()) SentFact(stringResource(R.string.settings_sent_d_card)) {
            JSelectableText(item.letThrough.interactionId, PersonaTheme.type.code, palette.ink)
        }
        if (origin.extensionId.isNotEmpty()) SentFact(stringResource(R.string.settings_sent_d_extension)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Text(origin.extensionId, Modifier.weight(1f, fill = false), style = value, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                JTag(stringResource(R.string.settings_sent_origin_extension))
            }
        }
        // A chat that can't be opened from here (no navigation host) is not shown as a link that does nothing.
        if (!origin.hasChat || onChat != null) SentFact(stringResource(R.string.settings_sent_d_chat)) {
            if (origin.hasChat && onChat != null) JButton(stringResource(R.string.settings_sent_open_chat), { onChat(origin.machine, origin.session) },
                kind = JButtonKind.Secondary, size = JButtonSize.Small)
            else Text(stringResource(R.string.settings_sent_origin_none), style = value, color = palette.ink3)
        }
        if (item.messageRef.isNotEmpty()) SentFact(stringResource(R.string.settings_sent_d_message)) {
            JSelectableText(item.messageRef, PersonaTheme.type.code, palette.ink)
        }
        if (item.recalledAt.isNotEmpty()) SentFact(stringResource(R.string.settings_sent_recalled)) {
            Text(SentText.full(res, item.recalledAt), style = value, color = palette.ink)
        }
        SentTextSection(item.text)
        if (onRecall != null && item.recalledAt.isEmpty()) SentRecall(item, recalling, recallError, onRecall)
        JButton(stringResource(R.string.settings_sent_done), onDone, Modifier.fillMaxWidth().padding(top = Tokens.Space.s))
    }
}

/** Recall, only when the hub says the message can be taken back (or a recall just failed, to keep its reason), asked once in a dialog. */
@Composable
private fun SentRecall(item: SentItem, recalling: Boolean, error: ResText?, onRecall: () -> Unit) {
    if (!item.canRecall && error == null) return
    var asking by remember(item.id) { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        if (item.canRecall) JButton(stringResource(R.string.settings_sent_recall), { asking = true }, Modifier.fillMaxWidth(),
            kind = JButtonKind.Destructive, size = JButtonSize.Medium, icon = PhIcons.ArrowUUpLeft, loading = recalling)
        error?.let { JInlineStatus(it.text(), tone = InlineTone.Failed) }
    }
    if (asking) JDialog(onDismiss = { asking = false }, title = stringResource(R.string.settings_sent_recall_title),
        text = stringResource(R.string.settings_sent_recall_confirm, SentText.connectorName(LocalResources.current, item.connector)),
        confirm = stringResource(R.string.settings_sent_recall_do) to { asking = false; onRecall() }, destructive = true)
}

/** The whole text with Copy text, and a note when the ledger cut it. */
@Composable
private fun SentTextSection(text: String) {
    val context = LocalContext.current
    val res = LocalResources.current
    val feedback = LocalAppFeedback.current
    val palette = PersonaTheme.palette
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(stringResource(R.string.settings_sent_d_text), Modifier.weight(1f), style = PersonaTheme.type.caption, color = palette.ink3)
            if (text.isNotEmpty()) JButton(stringResource(R.string.settings_sent_copy_text), {
                context.getSystemService(ClipboardManager::class.java)?.setPrimaryClip(ClipData.newPlainText(res.getString(R.string.settings_sent_d_text), text))
                feedback?.notice(res.getString(R.string.settings_src_copied))
            }, kind = JButtonKind.Quiet, size = JButtonSize.Small, icon = PhIcons.Copy)
        }
        if (text.isEmpty()) Text(stringResource(R.string.settings_sent_no_text), style = PersonaTheme.type.callout, color = palette.ink3)
        else {
            JSelectableText(text, PersonaTheme.type.callout, palette.ink,
                Modifier.fillMaxWidth().clip(PersonaTheme.shapes.controlShape).background(palette.sunken).padding(Tokens.Space.m))
            if (SentText.textCut(text)) Text(stringResource(R.string.settings_sent_text_cut, SentText.textMost(res)), style = PersonaTheme.type.caption, color = palette.ink3)
        }
    }
}
