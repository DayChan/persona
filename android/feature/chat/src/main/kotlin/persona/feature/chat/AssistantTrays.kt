package persona.feature.chat

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.semantics.ProgressBarRangeInfo
import androidx.compose.ui.semantics.progressBarRangeInfo
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.Dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.*
import persona.core.contracts.api.AssistantInboxDraft
import persona.core.contracts.api.ScheduledTask
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.answer.*
import persona.core.domain.tasks.TaskState
import persona.core.domain.sort.canApprove

internal enum class AssistantTray { Pending, Progress, Agents }

@Stable
internal class AssistantTrayState {
    var expanded by mutableStateOf<AssistantTray?>(null)
    fun toggle(tray: AssistantTray) { expanded = tray.takeUnless { expanded == tray } }
}

/** Only policy-valid one-shot approvals are eligible; drafts and field-bearing permissions never are. */
internal fun trayApproval(request: Interaction, session: Session?): Resolution? {
    if (session?.canApprove != true || request.needsFieldValues) return null
    val action = when (request.kind) {
        "permission" -> AnswerAction.Allow
        "plan" -> AnswerAction.ApproveEdits
        else -> return null
    }
    return AnswerBuilder.build(request, action, AnswerDraft(), AnswerContext()).resolution
}

internal data class TrayProgress(val id: String, val title: String, val todo: Todo? = null,
    val plan: TaskPlan? = null, val done: Boolean = false, val blocked: Boolean = false, val cancelled: Boolean = false,
    val text: String? = null, val completed: Int? = null, val total: Int? = null, val at: String? = null, val pr: String? = null)

/** Prefer an explicit supervisor report, then the agent's counted plan; do not estimate progress from status. */
internal fun trayProgress(todos: List<Todo>, plans: List<TaskPlan>, host: AssistantTaskHost,
    feed: List<FeedItem> = emptyList(), projected: List<AssistantTaskCard> = emptyList()): List<TrayProgress> {
    val bound = plans.flatMap { plan -> plan.tasks.mapIndexedNotNull { index, task ->
        task.todoId?.takeIf { id -> todos.firstOrNull { it.id == id }?.planId?.let { it == plan.id } != false }
            ?.let { it to "plan:${plan.id}:$index" }
    } }.toMap()
    val rows = todos.filter { it.origin == host.conversation }.map { todo ->
        val lead = host.tasks.lead(todo, host.sessions)
        val state = host.tasks.stateOf(todo, host.sessions)
        val report = todo.supervision?.progress
        val plan = lead?.plan
        val reportAt = parseTime(report?.at)
        val planAt = parseTime(plan?.at)
        val reportIsCurrent = planAt == null || (reportAt != null && !reportAt.isBefore(planAt))
        val countedReport = report?.takeIf { reportIsCurrent && it.steps > 0 && it.step in 0..it.steps }
        val countedPlan = plan?.takeIf { it.total > 0 && it.done in 0..it.total }
        val cancelled = todo.isDone && (todo.cancelledAt != null || todo.doneBy == "cancelled" || todo.supervision?.state == "cancelled")
        val done = todo.ask == null && todo.isDone && !cancelled && state != TaskState.Failed
        val completedText = todo.supervision?.reviews?.lastOrNull()?.note?.takeIf(String::isNotBlank)
            ?: todo.doneReason?.takeIf(String::isNotBlank) ?: lead?.lastText?.takeIf(String::isNotBlank)
        TrayProgress(bound[todo.id] ?: "todo:${todo.id}", todo.title, todo = todo,
            done = done, cancelled = cancelled,
            blocked = !cancelled && (todo.ask != null || state in setOf(TaskState.NeedsYou, TaskState.Interrupted, TaskState.Failed) || todo.supervision?.state == "needs_user"),
            text = if (done || cancelled) completedText else todo.ask?.verdict ?: report?.takeIf { reportIsCurrent }?.text?.takeIf(String::isNotBlank) ?: plan?.current?.takeIf(String::isNotBlank)
                ?: todo.startError?.takeIf(String::isNotBlank) ?: lead?.attentionReason?.takeIf(String::isNotBlank)
                ?: lead?.activity?.takeIf(String::isNotBlank) ?: lead?.lastText?.takeIf(String::isNotBlank),
            completed = countedReport?.step ?: countedPlan?.done, total = countedReport?.steps ?: countedPlan?.total,
            at = report?.at?.takeIf { reportIsCurrent } ?: plan?.at ?: todo.updatedAt,
            pr = todo.prUrl?.takeIf(String::isNotBlank))
    }
    val pending = plans.filter { it.conversation == host.conversation && it.state == "open" }.flatMap { plan ->
        plan.tasks.mapIndexedNotNull { index, task ->
            if (task.todoId != null && todos.any { it.id == task.todoId }) null
            else TrayProgress("plan:${plan.id}:$index", task.title, plan = plan, text = task.why, blocked = task.error != null)
        }
    }
    val fallback = (pending + rows).distinctBy { it.id }
    val merged = persona.core.domain.tasks.AssistantTaskCards.merge(feed,
        plans.filter { it.conversation == host.conversation }.associateBy { it.id },
        todos.filter { it.origin == host.conversation }, projected)
    val byTodo = rows.associateBy { it.todo?.id }
    val byId = fallback.associateBy { it.id }
    val authoritative = merged.filter { card ->
        card.todo?.planId?.let { it == card.identity.planId } != false
    }.groupBy { it.identity.todoId?.let { id -> "todo:$id" } ?: it.identity.id }.values.map { cards ->
        cards.maxBy { parseTime(it.identity.updatedAt) ?: java.time.Instant.MIN }
    }.map { card ->
        val row = byTodo[card.todo?.id] ?: byId[card.identity.id]
            ?: TrayProgress(card.identity.id, card.plan?.tasks?.getOrNull(card.identity.index ?: -1)?.title.orEmpty(),
                todo = card.todo, plan = card.plan)
        val receipt = card.receipt
        val report = card.report
        val latestSummary = report?.field("summary") ?: receipt?.field("summary")
        val pr = report?.field("pr") ?: receipt?.field("pr")
        val acceptedText = card.todo?.supervision?.reviews?.lastOrNull { it.verdict == "accepted" }?.note?.takeIf(String::isNotBlank)
            ?: card.todo?.doneReason?.takeIf(String::isNotBlank)
        val completed = card.todo?.ask == null && (row.done || (card.todo == null && card.identity.state == "accepted"))
        val cancelled = row.cancelled || (card.todo == null && card.identity.state == "cancelled")
        row.copy(id = card.identity.id,
            done = completed && !cancelled, cancelled = cancelled,
            blocked = !cancelled && (row.blocked || (card.todo == null && card.identity.state in setOf("needs_user", "needs_you", "blocked", "failed", "interrupted"))),
            text = if (completed || cancelled) latestSummary ?: (if (row.done || row.cancelled) row.text else acceptedText) else row.text ?: latestSummary,
            pr = row.pr ?: pr,
            at = listOfNotNull(card.identity.updatedAt, row.at).maxByOrNull { parseTime(it) ?: java.time.Instant.MIN })
    }
    val known = authoritative.map { it.todo?.id }.filterNotNull().toSet()
    val identities = authoritative.map { it.id }.toSet()
    return (authoritative + fallback.filter { it.id !in identities && (it.todo == null || it.todo.id !in known) })
        .distinctBy { it.id }.sortedBy { it.done || it.cancelled }
}

internal sealed interface TrayDraft {
    val id: String
    val title: String
    val text: String
    data class Inbox(val draft: AssistantInboxDraft) : TrayDraft {
        override val id get() = draft.itemId
        override val title get() = draft.title
        override val text get() = draft.suggestion.text
    }
    data class Feed(val item: FeedItem) : TrayDraft {
        override val id get() = item.field("item_id") ?: item.ref ?: item.id
        override val title get() = item.field("chat") ?: item.field("title").orEmpty()
        override val text get() = item.field("draft").orEmpty()
    }
}

internal fun trayDrafts(inbox: List<AssistantInboxDraft>, feed: List<FeedItem>): List<TrayDraft> =
    (inbox.map { TrayDraft.Inbox(it) } + feed.filter { it.kind == "message" && it.canSend && it.field("draft")?.isNotBlank() == true }
        .map { TrayDraft.Feed(it) }).distinctBy { it.id }

/** Real task/session/interaction projections, with one bounded panel above its permanent three-entry bar. */
@Composable
internal fun AssistantTrays(host: AssistantTaskHost, state: AssistantTrayState,
    requests: List<Interaction>, inbox: List<AssistantInboxDraft>, schedules: List<ScheduledTask>,
    maximumHeight: Dp, busy: Boolean, enabled: Boolean,
    onApprove: (List<Pair<Interaction, Resolution>>) -> Unit, onReview: (Interaction) -> Unit,
    onSend: (TrayDraft) -> Unit, onSchedule: (ScheduledTask) -> Unit,
    backgroundRecords: Boolean = false, onBackground: (() -> Unit)? = null,
    onPlan: (TaskPlan) -> Unit = {}) {
    val todos by host.tasks.todos.collectAsStateWithLifecycle()
    val plans by host.tasks.plans.collectAsStateWithLifecycle()
    val feeds by host.tasks.feeds.collectAsStateWithLifecycle()
    val main by host.tasks.mainConversation.collectAsStateWithLifecycle()
    val taskBusy by host.tasks.busy.collectAsStateWithLifecycle()
    val taskCards by host.tasks.taskCards.collectAsStateWithLifecycle()
    val drafts = trayDrafts(inbox, feeds[host.conversation].orEmpty())
    val progress = trayProgress(todos, plans.values.toList(), host, feeds[host.conversation].orEmpty(), taskCards[host.conversation].orEmpty())
    val bySession = host.sessions.associateBy { it.key }
    val ownedTaskAgents = todos.filter { it.origin == host.conversation }.flatMap { it.activeAgents.map { a -> a.key } }.toSet()
    val standalone = persona.core.domain.tasks.dockAgents(host.conversation, main, host.sessions, todos)
    val agents = (standalone + host.sessions.filter { it.key in ownedTaskAgents })
        .distinctBy { it.key }.filter { it.agentState in setOf(AgentState.Working, AgentState.NeedsYou, AgentState.Unread) }
    val pendingPlans = plans.values.filter { it.conversation == host.conversation && it.state == "open" }
    val needsReview = progress.filter { row ->
        val todo = row.todo
        val ask = todo?.ask
        (ask != null || todo?.supervision?.state in setOf("needs_user", "pending_review")) &&
            !(todo?.isDone == true && (ask == null || ask.outcome == "decide"))
    }
    val pendingCount = requests.size + drafts.size + pendingPlans.size + needsReview.size
    val approvals = requests.mapNotNull { r -> trayApproval(r, bySession[GroupKeys.rawKey(r.sessionKey)])?.let { r to it } }
    val pendingEnabled = pendingCount > 0
    LaunchedEffect(pendingCount) { if (!pendingEnabled && state.expanded == AssistantTray.Pending) state.expanded = null }
    BackHandler(state.expanded != null) { state.expanded = null }
    Column(Modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        state.expanded?.let { tray ->
            JCard(Modifier.fillMaxWidth().testTag("assistant-tray-${tray.name.lowercase()}"), padding = Tokens.Space.xs) {
                LazyColumn(Modifier.heightIn(max = minOf(maximumHeight, AssistantTrayTokens.maximumHeight)),
                    contentPadding = PaddingValues(bottom = Tokens.Space.s)) {
                    item {
                        JSectionHeader(stringResource(when (tray) {
                            AssistantTray.Pending -> R.string.chat_tray_pending
                            AssistantTray.Progress -> R.string.chat_tray_progress
                            AssistantTray.Agents -> R.string.chat_tray_agents
                        }), trailing = stringResource(R.string.chat_tray_items, when (tray) {
                            AssistantTray.Pending -> pendingCount
                            AssistantTray.Progress -> progress.size
                            AssistantTray.Agents -> agents.size
                        }), action = if (tray == AssistantTray.Pending && approvals.isNotEmpty()) ({
                            JTextButton(stringResource(R.string.chat_tray_approve_all), { onApprove(approvals) },
                                enabled = enabled && !busy, modifier = Modifier.testTag("assistant-approve-all"))
                        }) else if (tray == AssistantTray.Agents) ({
                            JTextButton(stringResource(R.string.chat_tasks_dock_all, progress.size + standalone.size),
                                { host.sheet.value = TaskSheet.Board }, modifier = Modifier.testTag("assistant-task-board"))
                        }) else null)
                    }
                    when (tray) {
                        AssistantTray.Pending -> {
                            if (pendingCount == 0) item { JEmptyState(stringResource(R.string.chat_tray_caught_up), "", icon = PhIcons.CheckCircle) }
                            items(requests, key = { "request:${it.id}" }) { request ->
                                val approval = trayApproval(request, bySession[GroupKeys.rawKey(request.sessionKey)])
                                TrayDecisionRow(request.sessionTitle ?: bySession[GroupKeys.rawKey(request.sessionKey)]?.displayTitle ?: request.headline.shown,
                                    request.headline.shown, if (approval != null) R.string.chat_tray_approve else R.string.chat_tray_review,
                                    "assistant-decision-${request.id}", enabled && !busy) {
                                    if (approval != null) onApprove(listOf(request to approval)) else onReview(request)
                                }
                            }
                            items(pendingPlans, key = { "plan:${it.id}" }) { plan ->
                                TrayDecisionRow(plan.tasks.joinToString(stringResource(R.string.chat_tasks_list_separator)) { it.title },
                                    stringResource(R.string.chat_tray_plan_review), R.string.chat_tray_review, "assistant-plan-${plan.id}", enabled && plan.id !in taskBusy) { onPlan(plan) }
                            }
                            items(needsReview, key = { "review:${it.id}" }) { row ->
                                val todo = row.todo
                                if (todo?.ask?.outcome == "decide") TaskDecisionCard(todo, host, enabled) { openTask(host, todo) }
                                else TrayDecisionRow(row.title, todo?.ask?.verdict ?: row.text.orEmpty(), R.string.chat_tray_review, "assistant-review-${row.id}", enabled) {
                                    todo?.let { openTask(host, it) }
                                }
                            }
                            if (drafts.isNotEmpty()) item { JSectionHeader(stringResource(R.string.chat_tray_drafts)) }
                            items(drafts, key = { "draft:${it.id}" }) { draft ->
                                TrayDecisionRow(draft.title, draft.text, R.string.chat_tray_send, "assistant-draft-${draft.id}",
                                    enabled && !busy && draftPlaceholder(draft.text) == null) { onSend(draft) }
                            }
                        }
                        AssistantTray.Progress -> {
                            items(progress, key = { it.id }) { row ->
                                TrayProgressRow(row, onClick = { row.todo?.let { openTask(host, it) } ?: row.plan?.let(onPlan) })
                            }
                            if (schedules.isNotEmpty() || backgroundRecords) item { JSectionHeader(stringResource(R.string.chat_tray_quiet)) }
                            items(schedules, key = { "schedule:${it.id}" }) { schedule ->
                                JRow(schedule.title, subtitle = schedule.lastError?.takeIf(String::isNotBlank)
                                    ?: schedule.nextRun?.let { stringResource(R.string.chat_tray_next_run, it) }
                                    ?: schedule.lastStatus, subtitleLines = 1, onClick = { onSchedule(schedule) },
                                    leading = { AssistantTrayMark(null) }, trailing = {
                                        Text(when (schedule.repeats) {
                                            ScheduledTask.INTERVAL -> stringResource(R.string.chat_tray_interval, schedule.everyMinutes)
                                            ScheduledTask.DAILY -> stringResource(R.string.chat_tray_daily)
                                            else -> stringResource(R.string.chat_tray_once)
                                        }, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
                                    })
                            }
                            if (backgroundRecords && onBackground != null) item {
                                JRow(stringResource(R.string.chat_tray_background), onClick = onBackground, leadingIcon = PhIcons.Clock)
                            }
                            if (progress.isEmpty() && schedules.isEmpty() && !backgroundRecords) item {
                                JEmptyState(stringResource(R.string.chat_tray_no_progress), "", icon = PhIcons.ListChecks)
                            }
                        }
                        AssistantTray.Agents -> {
                            items(agents, key = { it.key.path }) { agent ->
                                val tone = when (agent.agentState) {
                                    AgentState.Working -> AssistantTrayTone.Agents
                                    AgentState.NeedsYou -> AssistantTrayTone.Pending
                                    else -> AssistantTrayTone.Progress
                                }
                                JRow(agent.displayTitle, subtitle = stringResource(R.string.chat_tray_agent_detail, host.machineName(agent.machineId),
                                    agent.attentionReason?.takeIf(String::isNotBlank) ?: agent.activity?.takeIf(String::isNotBlank) ?: agent.lastText.orEmpty()),
                                    subtitleLines = 1, onClick = { host.onOpen(agent.key) }, modifier = Modifier.testTag("assistant-agent-${agent.sessionId}"),
                                    leading = { AssistantTrayMark(tone) }, trailing = {
                                        Text(stringResource(when (agent.agentState) {
                                            AgentState.Working -> R.string.chat_tray_working
                                            AgentState.NeedsYou -> R.string.chat_tray_needs_you
                                            else -> R.string.chat_tray_unread
                                        }), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
                                    })
                            }
                            item {
                                val routes by host.tasks.routes.collectAsStateWithLifecycle()
                                val routed = routes[host.conversation]?.let { id -> todos.firstOrNull { it.id == id } }
                                if (routed != null) JRow(stringResource(R.string.chat_tasks_route_chip, routed.title),
                                    onClick = { host.tasks.setRoute(host.conversation, null) }, leadingIcon = PhIcons.PaperPlaneTilt)
                            }
                        }
                    }
                }
            }
        }
        // Content-width chips at the leading edge, like the old All / task chips; a large font scrolls rather than wraps.
        Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).testTag("assistant-tray-bar"),
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            for (tray in AssistantTray.entries) AssistantTrayEntry(stringResource(when (tray) {
                AssistantTray.Pending -> R.string.chat_tray_pending
                AssistantTray.Progress -> R.string.chat_tray_progress
                AssistantTray.Agents -> R.string.chat_tray_agents
            }), when (tray) {
                AssistantTray.Pending -> pendingCount
                AssistantTray.Progress -> progress.count { !it.done && !it.cancelled }
                AssistantTray.Agents -> agents.size
            }, when (tray) {
                AssistantTray.Pending -> AssistantTrayTone.Pending
                AssistantTray.Progress -> AssistantTrayTone.Progress
                AssistantTray.Agents -> AssistantTrayTone.Agents
            }, state.expanded == tray,
                stringResource(if (state.expanded == tray) R.string.chat_tray_expanded else R.string.chat_tray_collapsed),
                { state.toggle(tray) }, Modifier.testTag("assistant-entry-${tray.name.lowercase()}"),
                enabled = true)
        }
    }
}

@Composable
private fun TrayDecisionRow(title: String, subtitle: String, action: Int, tag: String, enabled: Boolean, onClick: () -> Unit) {
    JRow(title, subtitle = subtitle, subtitleLines = 1, chevron = false,
        leading = { AssistantTrayMark(AssistantTrayTone.Pending) }, trailing = {
            JButton(stringResource(action), onClick, Modifier.testTag(tag), kind = JButtonKind.Secondary,
                size = JButtonSize.Small, enabled = enabled)
        })
}

internal fun trayPullRequestNumber(reference: String): String? =
    Regex("/(?:pull|merge_requests)/(\\d+)(?:$|[/?#])").find(reference)?.groupValues?.get(1)

@Composable
private fun TrayProgressRow(row: TrayProgress, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    JRow(onClick = onClick, modifier = Modifier.testTag("assistant-progress-${row.id}"), chevron = false, trailing = {
        if (row.done) JIcon(PhIcons.Check, stringResource(if (row.todo?.supervision?.state == "accepted")
            R.string.chat_tray_accepted else R.string.chat_tray_done), tint = palette.positive)
        else Text(stringResource(if (row.cancelled) R.string.chat_tray_cancelled else if (row.blocked) R.string.chat_tray_blocked else if (row.plan != null) R.string.chat_tray_planned else R.string.chat_tray_in_progress),
            style = PersonaTheme.type.micro, color = if (row.blocked) palette.cautionText else palette.ink3, maxLines = 1)
    }) {
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Text(row.title, style = PersonaTheme.type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            row.text?.let { Text(it, style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis) }
            row.pr?.let { reference ->
                val number = trayPullRequestNumber(reference)
                Text(if (number == null) reference else stringResource(R.string.chat_tray_pr_number, number),
                    style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            val done = row.completed; val total = row.total
            if (!row.done && !row.cancelled) {
                val counted = done != null && total != null && total > 0
                if (counted) Text(stringResource(R.string.chat_tray_steps, done, total),
                    style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1)
                val fraction = if (counted) done!!.toFloat() / total!! else if (row.plan != null) 0f else null
                JLinearProgress(fraction, Modifier.semantics {
                    progressBarRangeInfo = if (counted) ProgressBarRangeInfo(done!!.toFloat(), 0f..total!!.toFloat())
                        else if (row.plan != null) ProgressBarRangeInfo(0f, 0f..1f) else ProgressBarRangeInfo.Indeterminate
                }, tone = if (row.blocked) Tone.Caution else null,
                    color = if (row.blocked) null else assistantTrayColor(AssistantTrayTone.Progress))
            }
        }
    }
}
