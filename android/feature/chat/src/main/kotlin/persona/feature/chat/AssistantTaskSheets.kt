package persona.feature.chat

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.AgentState
import persona.core.contracts.PlanTask
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JAction
import persona.core.designsystem.JActionSheet
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JChip
import persona.core.designsystem.JDialog
import persona.core.designsystem.JEmptyState
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JSheet
import persona.core.designsystem.JStatusDot
import persona.core.designsystem.JTextField
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.text
import persona.core.domain.answer.askLine
import persona.core.domain.answer.askingLine
import persona.core.domain.answer.waitingCard
import persona.core.domain.tasks.TaskNow
import persona.core.domain.tasks.TaskState
import persona.core.domain.tasks.isRemovable

// A task's detail, the dock's "All" and the plan's "Edit" (iOS TaskSheets.swift / PlanEditSheet; web TaskPanel): what a
// tap on a task opens instead of leaving the conversation for its agent's page.

/** What the assistant's chat has open over the conversation. */
internal sealed interface TaskSheet {
    data class Task(val todoId: String) : TaskSheet
    /** An agent no task stands for (the main conversation's dock, #1113). */
    data class Agent(val key: SessionKey) : TaskSheet
    /** "All": the conversation's tasks by where they are. */
    data object Board : TaskSheet
}

/** The sheet [host] has open, if any. */
@Composable
internal fun AssistantTaskSheets(host: AssistantTaskHost) {
    when (val open = host.sheet.value) {
        is TaskSheet.Task -> TaskDetailSheet(open.todoId, host) { host.sheet.value = null }
        is TaskSheet.Agent -> AgentDetailSheet(open.key, host) { host.sheet.value = null }
        TaskSheet.Board -> TaskBoardSheet(host) { host.sheet.value = null }
        null -> Unit
    }
}

/**
 * A task: its mark, title and state, "Remove" while nothing of it runs, and its agents (a switcher when there are several).
 * The agent shows where it runs and what it is on now, with "Open chat" for its whole page and "Say something" to talk to it from
 * here; a task whose agent is still starting, or did not start, says so (with "Retry" / "Remove task").
 */
@Composable
private fun TaskDetailSheet(todoId: String, host: AssistantTaskHost, onDismiss: () -> Unit) {
    val todos by host.tasks.todos.collectAsStateWithLifecycle()
    val busy by host.tasks.busy.collectAsStateWithLifecycle()
    androidx.compose.runtime.LaunchedEffect(todoId) { host.tasks.reloadTodos() }
    val todo = todos.firstOrNull { it.id == todoId }
    var picked by remember(todoId) { mutableStateOf<SessionKey?>(null) }
    var menu by remember(todoId) { mutableStateOf(false) }
    JSheet(onDismiss, scrollable = true) {
        if (todo == null) {
            JEmptyState(stringResource(R.string.chat_task_detail_gone), stringResource(R.string.chat_task_detail_gone_body), icon = PhIcons.ListChecks)
            return@JSheet
        }
        val state = host.tasks.stateOf(todo, host.sessions)
        val agents = host.tasks.agentsOf(todo, host.sessions)
        val lead = host.tasks.lead(todo, host.sessions)
        val shown = agents.firstOrNull { it.key == picked } ?: lead
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            TaskMark(state)
            Column(Modifier.weight(1f)) {
                Text(todo.title, style = PersonaTheme.type.title, color = PersonaTheme.palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis)
                Text(state.label.text(), style = PersonaTheme.type.caption, color = PersonaTheme.palette.status(state.status()).text, maxLines = 1)
            }
            if (state.isRemovable) JIconButton(PhIcons.DotsThree, stringResource(R.string.chat_task_detail_more), { menu = true })
            JIconButton(PhIcons.X, stringResource(R.string.chat_task_detail_close), onDismiss)
        }
        if (agents.size > 1) {
            Spacer(Modifier.height(Tokens.Space.m))
            Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                agents.forEach { s ->
                    JChip(s.displayTitle.ifBlank { s.agentLabel }, selected = s.key == shown?.key, onClick = { picked = s.key },
                        status = s.agentState.designStatus)
                }
            }
        }
        Spacer(Modifier.height(Tokens.Space.l))
        TaskOwnerDecision(todo, host)
        TaskLifecycleDetails(todo, host)
        val failure = todo.startFailure
        when {
            shown != null -> AgentSummary(shown, host, routeTo = todo.id, onDismiss = onDismiss)
            failure != null -> JEmptyState(stringResource(R.string.chat_task_detail_failed), failure.trim(), icon = PhIcons.WarningCircle, action = {
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    JButton(stringResource(R.string.chat_tasks_retry), { host.tasks.retryTask(todo, host.onError) }, kind = JButtonKind.Secondary,
                        size = JButtonSize.Medium, icon = PhIcons.ArrowClockwise, loading = persona.core.domain.tasks.TasksStore.retryKey(todo.id) in busy)
                    JButton(stringResource(R.string.chat_task_detail_remove), { menu = true },
                        kind = JButtonKind.Destructive, size = JButtonSize.Medium, icon = PhIcons.Trash)
                }
            })
            else -> JEmptyState(stringResource(R.string.chat_task_detail_starting), stringResource(R.string.chat_task_detail_starting_body), icon = PhIcons.GearSix)
        }
    }
    if (menu && todo != null) JActionSheet(
        listOf(JAction(stringResource(R.string.chat_task_detail_remove), PhIcons.Trash, destructive = true) {
            host.tasks.removeTask(todo, host.onError); onDismiss()
        }),
        { menu = false }, title = todo.title,
    )
}

/** An agent no task stands for, as a task's would show: it is an agent of its own, opened from the Agents page. */
@Composable
private fun AgentDetailSheet(key: SessionKey, host: AssistantTaskHost, onDismiss: () -> Unit) {
    val session = host.sessions.firstOrNull { it.key == key }
    JSheet(onDismiss, scrollable = true) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            session?.let { JStatusDot(it.agentState.designStatus) }
            Column(Modifier.weight(1f)) {
                Text(session?.displayTitle ?: stringResource(R.string.chat_task_detail_agent), style = PersonaTheme.type.title, color = PersonaTheme.palette.ink,
                    maxLines = 2, overflow = TextOverflow.Ellipsis)
                Text(stringResource(R.string.chat_task_detail_own_agent), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1)
            }
            JIconButton(PhIcons.X, stringResource(R.string.chat_task_detail_close), onDismiss)
        }
        Spacer(Modifier.height(Tokens.Space.l))
        if (session != null) AgentSummary(session, host, routeTo = null, onDismiss = onDismiss)
        else JEmptyState(stringResource(R.string.chat_task_detail_agent_gone), stringResource(R.string.chat_task_detail_agent_gone_body), icon = PhIcons.Robot)
    }
}

/** Where the agent runs, what it is on now, and the ways on: its whole page, or a word to it from the conversation. */
@Composable
private fun AgentSummary(session: Session, host: AssistantTaskHost, routeTo: String?, onDismiss: () -> Unit) {
    val now = when (session.agentState) {
        AgentState.NeedsYou -> session.askingLine(host.cards[session.key])?.text() ?: stringResource(R.string.chat_tasks_now_needs_you)
        else -> TaskNow.of(session, session.agentState == AgentState.Working)?.text ?: session.lastText?.takeIf(String::isNotBlank)
            ?: stringResource(R.string.chat_tasks_now_quiet)
    }
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        JGroupItem(GroupPosition.Single) {
            JRow(session.displayTitle.ifBlank { session.agentLabel },
                subtitle = listOf(listOfNotNull(host.machineName(session.machineId), session.folderName.takeIf(String::isNotBlank), session.agentLabel)
                    .joinToString(" · "), now).joinToString("\n"),
                subtitleLines = 6, leading = { JStatusDot(session.agentState.designStatus) })
        }
        val subagents = TaskNow.subagents(session)
        if (subagents.isNotEmpty()) Text(stringResource(R.string.chat_tasks_subagents, subagents.joinToString(stringResource(R.string.chat_tasks_list_separator))),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
        Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JButton(stringResource(R.string.chat_task_detail_open_chat), { onDismiss(); host.onOpen(session.key) }, Modifier.weight(1f),
                size = JButtonSize.Medium, icon = PhIcons.ChatCircle)
            if (routeTo != null) JButton(stringResource(R.string.chat_tasks_say), { host.tasks.setRoute(host.conversation, routeTo); onDismiss() },
                kind = JButtonKind.Secondary, size = JButtonSize.Medium, icon = PhIcons.PaperPlaneTilt)
        }
    }
}

/** "All" (iOS TaskBoardSheet): the conversation's tasks by where they are, then the agents no task stands for. */
@Composable
private fun TaskBoardSheet(host: AssistantTaskHost, onDismiss: () -> Unit) {
    val todos by host.tasks.todos.collectAsStateWithLifecycle()
    val rows = todos.filter { it.origin == host.conversation }.map { it to host.tasks.stateOf(it, host.sessions) }
    val agents = host.tasks.agentsOf(host.conversation, host.sessions)
    var removing by remember { mutableStateOf<Todo?>(null) }
    JSheet(onDismiss, title = stringResource(R.string.chat_task_board_title), scrollable = true) {
        if (rows.isEmpty() && agents.isEmpty()) {
            JEmptyState(stringResource(R.string.chat_task_board_empty), stringResource(R.string.chat_task_board_empty_body), icon = PhIcons.ListChecks)
            return@JSheet
        }
        @Composable fun section(title: Int, tasks: List<Pair<Todo, TaskState>>, sessions: List<Session> = emptyList()) {
            val count = tasks.size + sessions.size
            if (count == 0) return
            JSectionHeader(stringResource(title), count = count)
            tasks.forEachIndexed { index, (todo, state) ->
                JGroupItem(GroupPosition.of(index, count)) {
                    val lead = host.tasks.lead(todo, host.sessions)
                    val asks = if (state == TaskState.NeedsYou) todo.waitingCard(lead, host.cards)?.askLine()?.text() else null
                    JRow(todo.title, subtitle = boardSubtitle(todo, state, host, lead, asks).ifBlank { state.label.text() }, leading = { TaskMark(state) },
                        onClick = { host.sheet.value = TaskSheet.Task(todo.id) },
                        onLongClick = if (state.isRemovable) ({ removing = todo }) else null)
                }
            }
            sessions.forEachIndexed { index, s ->
                JGroupItem(GroupPosition.of(tasks.size + index, count)) {
                    JRow(s.displayTitle, subtitle = agentSubtitle(s, host).ifBlank { stringResource(R.string.chat_task_detail_own_agent) },
                        leading = { JStatusDot(s.agentState.designStatus) },
                        onClick = { host.sheet.value = TaskSheet.Agent(s.key) })
                }
            }
        }
        section(R.string.chat_task_board_waiting, rows.filter { it.second == TaskState.NeedsYou || it.second == TaskState.Interrupted },
            agents.filter { it.agentState == AgentState.NeedsYou })
        section(R.string.chat_task_board_working, rows.filter { it.second == TaskState.Working || it.second == TaskState.NotStarted },
            agents.filter { it.agentState != AgentState.NeedsYou })
        section(R.string.chat_task_board_unread, rows.filter { it.second == TaskState.Unread })
        section(R.string.chat_task_board_followup, rows.filter { it.second == TaskState.Followup })
        section(R.string.chat_task_board_done, rows.filter { it.second == TaskState.Done })
        section(R.string.chat_task_board_failed, rows.filter { it.second == TaskState.Failed })
    }
    removing?.let { todo ->
        JActionSheet(listOf(JAction(stringResource(R.string.chat_task_detail_remove), PhIcons.Trash, destructive = true) {
            host.tasks.removeTask(todo, host.onError)
        }), { removing = null }, title = todo.title)
    }
}

/** Why the task's agent did not start, if it did not: the task's own error, or its first agent's. */
private val Todo.startFailure: String?
    get() = startError?.takeIf(String::isNotBlank) ?: agents.firstOrNull { !it.startError.isNullOrBlank() }?.startError

/**
 * A task's row in "All" (iOS TaskBoardSheet.subtitle): where it runs, then what its card [asks] while it waits for you,
 * why it did not start, or what it is on now. A waiting task's card comes first: an agent that once failed to start
 * must not hide what the one now waiting asks (as the task card).
 */
private fun boardSubtitle(todo: Todo, state: TaskState, host: AssistantTaskHost, lead: Session?, asks: String? = null): String {
    val after = asks ?: todo.startFailure?.trim() ?: TaskNow.of(lead, state == TaskState.Working)?.text
    return listOfNotNull(todo.place(host, lead), after).filter(String::isNotBlank).joinToString(" · ")
}

/** An agent's row in "All" (iOS agentSubtitle): computer · folder · agent · its last words. */
private fun agentSubtitle(session: Session, host: AssistantTaskHost): String =
    listOfNotNull(host.machineName(session.machineId), session.folderName, session.agentLabel, session.lastText?.trim())
        .filter(String::isNotBlank).joinToString(" · ")

/** "Edit" (iOS PlanEditSheet): each task's title, computer, folder, model, permission and first message before it starts. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun PlanEditSheet(plan: TaskPlan, host: AssistantTaskHost, onDismiss: () -> Unit, starting: Boolean = false, onStart: (List<PlanTask>) -> Unit) {
    var tasks by remember(plan.id) { mutableStateOf(plan.tasks) }
    fun edit(index: Int, change: (PlanTask) -> PlanTask) { tasks = tasks.mapIndexed { i, t -> if (i == index) change(t) else t } }
    // Edits are asked about before a drag, the scrim or back drops them (iOS .sheetCancel(unsaved:)).
    val unsaved = tasks.isNotEmpty() && tasks != plan.tasks
    var askDiscard by remember(plan.id) { mutableStateOf(false) }
    if (askDiscard) JDialog(
        onDismiss = { askDiscard = false }, title = stringResource(R.string.chat_plan_edit_discard_title),
        text = stringResource(R.string.chat_plan_edit_discard_body), destructive = true,
        confirm = stringResource(R.string.chat_plan_edit_discard) to { askDiscard = false; onDismiss() },
        dismiss = stringResource(R.string.chat_plan_edit_keep_editing) to { askDiscard = false },
    )
    JSheet(onDismiss, title = stringResource(R.string.chat_plan_edit_title), dismissible = !unsaved, onBlockedDismiss = { askDiscard = true }) {
        Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            tasks.forEachIndexed { i, task ->
                if (tasks.size > 1) JSectionHeader(stringResource(R.string.chat_plan_edit_task_n, i + 1))
                JTextField(task.title, { v -> edit(i) { it.copy(title = v) } }, Modifier.fillMaxWidth(), label = stringResource(R.string.chat_plan_edit_name),
                    singleLine = true)
                Text(stringResource(R.string.chat_plan_edit_machine), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    (host.machines.map { it.id } + task.machine).filter(String::isNotBlank).distinct().forEach { id ->
                        JChip(host.machineName(id), selected = id == task.machine, onClick = { edit(i) { it.copy(machine = id) } }, icon = PhIcons.Laptop)
                    }
                }
                JTextField(task.cwd, { v -> edit(i) { it.copy(cwd = v) } }, Modifier.fillMaxWidth(), label = stringResource(R.string.chat_plan_edit_folder),
                    singleLine = true)
                JTextField(task.model.orEmpty(), { v -> edit(i) { it.copy(model = v) } }, Modifier.fillMaxWidth(),
                    label = stringResource(R.string.chat_plan_edit_model), placeholder = stringResource(R.string.chat_tasks_default_model), singleLine = true)
                Text(stringResource(R.string.chat_plan_edit_permission), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    PlanPermissions.forEach { mode ->
                        JChip(permissionLabel(mode), selected = (task.permission ?: "default") == mode, onClick = { edit(i) { it.copy(permission = mode) } })
                    }
                }
                JTextField(task.prompt, { v -> edit(i) { it.copy(prompt = v) } }, Modifier.fillMaxWidth(), label = stringResource(R.string.chat_plan_edit_prompt),
                    minLines = 3, maxLines = 8)
            }
        }
        Spacer(Modifier.height(Tokens.Space.m))
        JButton(stringResource(R.string.chat_tasks_plan_start), { onStart(tasks) }, Modifier.fillMaxWidth(),
            enabled = tasks.all { it.title.isNotBlank() && it.prompt.isNotBlank() }, loading = starting)
    }
}

/** The permission modes a planned task can start with (iOS PlanEditSheet). */
internal val PlanPermissions = listOf("default", "acceptEdits", "plan", "auto", "bypassPermissions")

/** An agent's state as the design system draws it. */
internal val AgentState.designStatus: Status get() = when (this) {
    AgentState.NeedsYou -> Status.NeedsYou
    AgentState.Unread -> Status.Unread
    AgentState.Working -> Status.Working
    AgentState.Idle -> Status.Idle
    AgentState.Offline -> Status.Offline
    AgentState.Unknown -> Status.Unknown
}
