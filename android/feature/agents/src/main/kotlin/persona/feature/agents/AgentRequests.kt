package persona.feature.agents

import persona.core.designsystem.text
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import persona.core.contracts.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.answer.*
import persona.feature.chat.SecretRunActions
import persona.feature.chat.SecretRunSummary
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import java.time.Instant

internal typealias AnswerRequest = (Interaction, AnswerAction, AnswerDraft, Boolean) -> Unit

@Composable
internal fun RequestCard(
    request: Interaction, machine: String?, metadata: AgentsMetadata, now: Instant, online: Boolean,
    delivery: RequestDelivery?, onOpen: () -> Unit, onAnswer: AnswerRequest,
    readOnly: Boolean = false, space: String? = null,
    /** False when the session's source does not let the phone approve (a Codex thread not joined live). */
    approvable: Boolean = true,
) {
    val enabled = online && !readOnly && approvable && !request.expired(now) && !RequestDeliveries.locks(delivery)
    val question = request.quickQuestion()
    val source = listOfNotNull(machine, space ?: request.cwd?.trimEnd('/')?.substringAfterLast('/')?.takeIf { it.isNotBlank() }).joinToString(" / ")
    val answer: (AnswerAction) -> Unit = { onAnswer(request, it, AnswerDraft(), false) }
    val res = agentResources()
    val cardSummary = request.cardSummary(res)
    DecisionCard(
        kind = request.decisionKind, headline = request.cardTitle(res), title = request.sessionTitle ?: stringResource(R.string.agents_request_session),
        modifier = Modifier.padding(bottom = 12.dp), source = source.takeIf { it.isNotBlank() },
        expires = if (readOnly) stringResource(R.string.agents_request_handled) else countdown(request.expiresAt, now),
        summary = markdownPreview(cardSummary).lineSequence().firstOrNull(), onOpen = onOpen,
        expandedByDefault = false,
        body = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                when (request.kind) {
                    // A password card (#1137), as in the chat: why, and every command exactly as it will run.
                    "permission" -> if (request.isSecretRun) SecretRunSummary(request) else {
                        val summary = request.permissionSummary()
                        val input = request.toolInput as? JsonObject
                        val command = (input?.get("command") as? JsonPrimitive)?.takeIf { it.isString }?.content
                        if (request.toolName == "Bash" && !command.isNullOrBlank()) {
                            JCodeBlock(command, maxLines = 6)
                            val purpose = (input?.get("description") as? JsonPrimitive)?.takeIf { it.isString }?.content
                            purpose?.takeIf { it.isNotBlank() }?.let { Text(it, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2) }
                            val text = summary.text.text()
                            if (text != command && text != purpose) Text(text, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
                        } else {
                            Text(summary.text.text(), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink, maxLines = 4, overflow = TextOverflow.Ellipsis)
                            summary.description?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
                        }
                    }
                    "question" -> {
                        Text(markdownPreview(cardSummary), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink, maxLines = 4, overflow = TextOverflow.Ellipsis)
                        question?.options?.forEachIndexed { index, option ->
                            val label = AnswerBuilder.display(option.label)
                            DecisionOption(label.text, { onAnswer(request, AnswerAction.PickOption, AnswerDraft(optionIndex = index), false) },
                                detail = option.description, recommended = label.recommended, enabled = enabled,
                                position = index + 1 to question.options.size)
                        }
                    }
                    else -> Text(markdownPreview(cardSummary), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink,
                        maxLines = 4, overflow = TextOverflow.Ellipsis)
                }
                request.sourceLabel(res, metadata)?.let { Text(it.replaceFirst("来自子 Agent · ", "来自子 Agent，"), // i18n-ignore: rewrites core:domain's source line
                    style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
                if (!approvable && !readOnly) Text(stringResource(R.string.agents_approve_blocked),
                    style = PersonaTheme.type.caption, color = PersonaTheme.palette.cautionText)
                if (request.needsFieldValues && !request.isSecretRun && approvable && !readOnly) Text(stringResource(R.string.agents_request_fields_elsewhere),
                    style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                delivery?.let { RequestDeliveryStatus(it) }
            }
        },
        actions = if (readOnly) null else ({
            when (request.kind) {
                // Only the iPhone fills in a password card (#1137): here it can only be declined.
                "permission" -> if (request.isSecretRun) SecretRunActions(enabled) { answer(AnswerAction.Deny) }
                else DecisionButtons(
                    // A card with fields to fill in cannot be allowed here yet (#4987).
                    primary = JAction(stringResource(R.string.agents_request_allow), enabled = enabled && !request.needsFieldValues, onClick = { answer(AnswerAction.Allow) }),
                    secondary = JAction(stringResource(R.string.agents_request_deny), enabled = enabled, onClick = { answer(AnswerAction.Deny) }),
                    more = if (request.offersAlways) listOf(JAction(request.alwaysActionLabel.text(),
                        enabled = enabled && request.sessionId.isNotEmpty() && !request.needsFieldValues, onClick = { answer(AnswerAction.Always) })) else emptyList())
                // The full editor owns feedback, draft persistence and the plan-read gate.
                "plan" -> DecisionButtons(JAction(stringResource(R.string.agents_request_review_plan), enabled = enabled, onClick = onOpen),
                    secondary = JAction(stringResource(R.string.agents_request_send_back), enabled = enabled, onClick = onOpen))
                "question" -> if (question == null) JButton(stringResource(R.string.agents_request_answer), onOpen, enabled = enabled)
                else -> JButton(stringResource(R.string.agents_request_view), onOpen)
            }
        }),
        quickActions = {
            if (request.kind == "permission" && !readOnly && !request.needsFieldValues) {
                JButton(stringResource(R.string.agents_request_deny), { answer(AnswerAction.Deny) }, Modifier.weight(1f), kind = JButtonKind.Secondary, size = JButtonSize.Small, enabled = enabled)
                JButton(stringResource(R.string.agents_request_allow), { answer(AnswerAction.Allow) }, Modifier.weight(1f), size = JButtonSize.Small, enabled = enabled)
            } else if (question != null && !readOnly) {
                question.options.forEachIndexed { index, option ->
                    val label = AnswerBuilder.display(option.label)
                    JButton(label.text, { onAnswer(request, AnswerAction.PickOption, AnswerDraft(optionIndex = index), false) }, Modifier.weight(1f),
                        kind = if (label.recommended) JButtonKind.Primary else JButtonKind.Secondary, size = JButtonSize.Small, enabled = enabled)
                }
            } else JButton(stringResource(R.string.agents_request_view), onOpen, kind = JButtonKind.Secondary, size = JButtonSize.Small)
            delivery?.let { RequestDeliveryStatus(it) }
        },
    )
}

@Composable
internal fun RequestsDialog(
    requests: List<Interaction>, names: Map<String, String>, metadata: AgentsMetadata, now: Instant,
    delivery: Map<String, RequestDelivery>, onDismiss: () -> Unit,
    onOpen: (Interaction) -> Unit, onAnswer: AnswerRequest,
    history: List<Interaction> = emptyList(),
) {
    var showHistory by remember { mutableStateOf(false) }
    val shown = if (showHistory) history else requests
    JSheet(onDismiss, title = stringResource(R.string.agents_requests_sheet_title)) {
        JSegmented(listOf(JSegment(stringResource(R.string.agents_requests_pending), requests.size), JSegment(stringResource(R.string.agents_requests_history_tab), history.size)), if (showHistory) 1 else 0, { showHistory = it == 1 }, Modifier.fillMaxWidth())
        LazyColumn(Modifier.weight(1f, fill = false), contentPadding = PaddingValues(bottom = 16.dp)) {
            if (showHistory) item("history-note") {
                Text(stringResource(R.string.agents_requests_history_note), Modifier.padding(vertical = 12.dp), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
            }
            itemsIndexed(shown, key = { _, request -> request.id }) { index, request ->
                JGroupItem(GroupPosition.of(index, shown.size)) {
                    JRow(request.cardTitle(agentResources()), subtitle = request.sessionTitle ?: stringResource(R.string.agents_request_session), leadingIcon = request.decisionKind.icon,
                        value = if (showHistory) stringResource(R.string.agents_request_handled) else countdown(request.expiresAt, now), onClick = { onOpen(request) })
                    names[request.machineId]?.let { Text(it, Modifier.padding(horizontal = 14.dp, vertical = 4.dp), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
                    delivery[request.id]?.let { RequestDeliveryStatus(it) }
                }
            }
            if (shown.isEmpty()) item("empty") {
                JEmptyState(stringResource(if (showHistory) R.string.agents_requests_history_empty else R.string.agents_requests_empty),
                    stringResource(R.string.agents_requests_empty_hint), icon = PhIcons.Check)
            }
        }
    }
}

@Composable
internal fun RequestDetail(
    request: Interaction, metadata: AgentsMetadata, now: Instant, online: Boolean, delivery: RequestDelivery?,
    onDismiss: () -> Unit, onOpenChat: (Interaction) -> Unit, onAnswer: AnswerRequest,
    readOnly: Boolean = false, onTerminal: (() -> Unit)?, machine: Machine? = null,
    /** Why this session's requests cannot be answered from the phone (its source does not approve). */
    blockedReason: String? = null,
    /** False for a session without a terminal: no "handle in terminal" note in the request. */
    terminalHelp: Boolean = true,
) {
    var linkVisible by remember(request) { mutableStateOf(false) }
    val feedback = LocalAppFeedback.current
    val openForContext = stringResource(R.string.agents_request_open_for_context)
    val openForEdit = stringResource(R.string.agents_request_open_for_edit)
    JSheet(onDismiss, title = stringResource(if (readOnly) R.string.agents_request_history_title else R.string.agents_request_handle_title)) {
        key(request) {
            persona.feature.chat.ChatLinkHost(Modifier.weight(1f),
                onContext = { feedback?.notice(openForContext) },
                onEdit = { _, _ -> feedback?.notice(openForEdit) },
                onVisibilityChanged = { linkVisible = it }) { inset ->
                Column(Modifier.fillMaxSize().padding(bottom = inset).imePadding(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    JRow(request.headline.text(), subtitle = request.sessionTitle, leadingIcon = request.decisionKind.icon)
                    // The editor below announces "Sending" itself; announcing it here too would say it twice.
                    delivery?.let { RequestDeliveryStatus(it, live = false) }
                    if (!online && !readOnly) Text(stringResource(R.string.agents_request_offline), style = PersonaTheme.type.caption, color = PersonaTheme.palette.cautionText)
                    Box(Modifier.weight(1f)) {
                        persona.feature.chat.RequestEditor(request, machine, busy = RequestDeliveries.locks(delivery), uncertain = delivery == RequestDelivery.Unknown,
                            online = online, readOnly = readOnly, onTerminal = onTerminal, linkVisible = linkVisible,
                            blockedReason = blockedReason, terminalHelp = terminalHelp,
                            onAnswer = { action, draft, read -> if (!readOnly) onAnswer(request, action, draft, read) })
                    }
                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        JButton(stringResource(R.string.agents_action_open_session), { onOpenChat(request) }, Modifier.weight(1f), kind = JButtonKind.Secondary)
                        // A session without a terminal (a Codex thread, an agent runtime's) has none to open; nor has the
                        // assistant's own password card (#1137).
                        onTerminal?.takeUnless { request.isSecretRun }?.let { JButton(stringResource(R.string.agents_request_open_terminal), it, Modifier.weight(1f), kind = JButtonKind.Secondary) }
                    }
                }
            }
        }
    }
}

@Composable
private fun RequestDeliveryStatus(delivery: RequestDelivery, live: Boolean = true) {
    // A live region, so a screen reader hears "Sending" / "Delivered" after an answer without looking for it.
    Box(if (live) Modifier.semantics(mergeDescendants = true) { liveRegion = LiveRegionMode.Polite } else Modifier) {
        when (delivery) {
            RequestDelivery.Sending -> PendingBadge(PendingStatus.Sending)
            RequestDelivery.Unknown -> PendingBadge(PendingStatus.Unconfirmed)
            RequestDelivery.Sent -> JTag(stringResource(R.string.agents_request_sent), status = Status.Unread)
            RequestDelivery.Retry -> JTag(stringResource(R.string.agents_request_retry), tone = Tone.Caution)
        }
    }
}

private val Interaction.decisionKind: DecisionKind get() = when (kind) {
    "permission" -> if (toolName == "Bash" || isSecretRun) DecisionKind.Command else DecisionKind.Tool
    "plan" -> DecisionKind.Plan
    "question" -> DecisionKind.Question
    "elicitation" -> DecisionKind.Form
    "screen" -> DecisionKind.Screen
    else -> DecisionKind.Other
}
