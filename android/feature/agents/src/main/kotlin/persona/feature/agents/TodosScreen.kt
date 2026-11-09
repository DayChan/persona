package persona.feature.agents

import persona.core.domain.tasks.HoldStep
import persona.core.domain.tasks.HoldUnblock
import persona.core.domain.tasks.TodoHold
import persona.core.domain.tasks.hold
import persona.core.designsystem.text
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.launch
import persona.core.contracts.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.answer.askingLine
import persona.core.domain.answer.firstBySession
import persona.core.domain.plainMarkdown
import persona.core.domain.tasks.TasksStore
import persona.core.navigation.NewTodoRequests
import android.text.format.DateFormat
import androidx.compose.ui.platform.LocalConfiguration
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

data class TodoMostUrgent(
    val agent: TodoAgent,
    val session: Session?,
    val status: AgentState,
    val isFailed: Boolean,
)

fun mostUrgentAgent(todo: Todo, sessionLookup: (SessionKey) -> Session?): TodoMostUrgent? {
    val active = todo.currentAgents
    if (active.isEmpty()) return null

    fun score(st: AgentState, isFailed: Boolean): Int = when {
        isFailed -> 1
        st == AgentState.NeedsYou -> 4
        st == AgentState.Unread -> 3
        st == AgentState.Working -> 2
        else -> 0
    }

    var best: TodoMostUrgent? = null
    var bestScore = -1

    for (a in active) {
        val isFailed = !a.startError.isNullOrBlank()
        val s = sessionLookup(a.key)
        val st = if (isFailed) AgentState.Offline else (s?.agentState ?: AgentState.Offline)
        val sc = score(st, isFailed)
        if (sc > bestScore || best == null) {
            bestScore = sc
            best = TodoMostUrgent(a, s, st, isFailed)
        }
    }
    return best
}

@Composable
fun TodosScreen(
    todos: List<Todo>,
    sessions: List<Session>,
    tasks: TasksStore?,
    onBack: (() -> Unit)?,
    onOpenTodo: (String) -> Unit,
    now: Instant = Instant.now(),
    onError: (String) -> Unit = {},
    /** The cards waiting for an answer: a waiting agent's line says what its card asks (#3964). */
    pending: List<Interaction> = emptyList(),
    /** The launcher's "New to-do": each request brings the sheet up, also on a list kept from an earlier use (#5718). */
    newTodoRequests: NewTodoRequests? = null,
) {
    val cards = remember(pending) { pending.firstBySession() }
    var query by rememberSaveable { mutableStateOf("") }
    var creating by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(newTodoRequests) {
        val requests = newTodoRequests ?: return@LaunchedEffect
        requests.requests.collect { at -> if (at != null && requests.take(at)) creating = true }
    }
    var completedFolded by rememberSaveable { mutableStateOf(true) }
    var newTitle by remember { mutableStateOf("") }
    var newNotes by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    val res = agentResources()

    val sessionMap = remember(sessions) { sessions.associateBy { it.key } }
    val sessionLookup: (SessionKey) -> Session? = remember(sessionMap) { { key: SessionKey -> sessionMap[key] } }

    val filtered = remember(todos, query) {
        if (query.isBlank()) todos
        else {
            val q = query.trim()
            todos.filter { it.title.contains(q, ignoreCase = true) || it.notes.contains(q, ignoreCase = true) }
        }
    }

    val uncompleted = remember(filtered, sessions) {
        filtered.filter { !it.isDone }.ordered(sessionLookup)
    }
    val completed = remember(filtered) {
        filtered.filter { it.isDone }.sortedWith(
            compareByDescending<Todo> { it.done ?: it.created ?: Instant.EPOCH }.thenBy { it.id }
        )
    }

    if (creating) {
        JSheet(
            onDismiss = {
                creating = false
                newTitle = ""
                newNotes = ""
            },
            title = stringResource(R.string.agents_todo_new)
        ) {
            Column(
                Modifier
                    .fillMaxWidth()
                    .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.s),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)
            ) {
                JTextField(
                    newTitle,
                    { newTitle = it },
                    label = stringResource(R.string.agents_todo_title_label),
                    placeholder = stringResource(R.string.agents_todo_title_placeholder),
                    singleLine = true
                )
                JTextField(
                    newNotes,
                    { newNotes = it },
                    label = stringResource(R.string.agents_todo_notes_optional),
                    placeholder = stringResource(R.string.agents_todo_notes_placeholder),
                    minLines = 3,
                    maxLines = 6
                )
                Spacer(Modifier.height(Tokens.Space.s))
                JButton(
                    text = stringResource(R.string.agents_todo_create),
                    onClick = {
                        val title = newTitle.trim()
                        val notes = newNotes.trim()
                        if (title.isNotBlank()) {
                            creating = false
                            newTitle = ""
                            newNotes = ""
                            scope.launch {
                                try {
                                    tasks?.createTodo(title, notes)
                                } catch (e: Exception) {
                                    onError(res.getString(R.string.agents_todo_create_failed, e.message.orEmpty()))
                                }
                            }
                        }
                    },
                    enabled = newTitle.isNotBlank(),
                    modifier = Modifier.fillMaxWidth()
                )
            }
        }
    }

    JPage(
        title = stringResource(R.string.agents_todos_title),
        onBack = onBack,
        actions = {
            JIconButton(PhIcons.Plus, stringResource(R.string.agents_todo_new), { creating = true })
        }
    ) { padding ->
        LazyColumn(
            Modifier.fillMaxSize(),
            contentPadding = PaddingValues(
                top = padding.calculateTopPadding() + Tokens.Space.s,
                bottom = padding.calculateBottomPadding() + Tokens.Space.l,
                start = Tokens.Space.l,
                end = Tokens.Space.l
            ),
            verticalArrangement = Arrangement.spacedBy(ContentTokens.rowGap)
        ) {
            item(key = "search") {
                JSearchField(query, { query = it }, placeholder = stringResource(R.string.agents_todos_search))
            }

            if (uncompleted.isEmpty() && completed.isEmpty()) {
                item(key = "empty") {
                    JEmptyState(
                        title = stringResource(if (query.isBlank()) R.string.agents_todos_empty else R.string.agents_todos_no_match),
                        description = stringResource(if (query.isBlank()) R.string.agents_todos_empty_hint else R.string.agents_todos_no_match_hint),
                        icon = PhIcons.ListChecks,
                        action = if (query.isBlank()) ({
                            JButton(stringResource(R.string.agents_todo_new), { creating = true }, icon = PhIcons.Plus)
                        }) else null,
                    )
                }
            } else {
                if (uncompleted.isNotEmpty()) {
                    item(key = "uncompleted-header") {
                        JSectionHeader(stringResource(R.string.agents_todos_open), count = uncompleted.size)
                    }

                    itemsIndexed(uncompleted, key = { _, todo -> todo.id }) { index, todo ->
                        val state = todo.state(sessionLookup)
                        val urgent = mostUrgentAgent(todo, sessionLookup)
                        val status = when (state) {
                            TodoState.NeedsYou -> Status.NeedsYou
                            TodoState.Unread -> Status.Unread
                            TodoState.Working -> Status.Working
                            else -> Status.Idle
                        }

                        JSwipeActions(
                            start = SwipeAction(stringResource(R.string.agents_todo_done_action), PhIcons.Check, Status.Working, {
                                scope.launch {
                                    try {
                                        tasks?.setTodoDone(todo, true)
                                    } catch (e: Exception) {
                                        onError(res.getString(R.string.agents_todo_done_failed, e.message.orEmpty()))
                                    }
                                }
                            }),
                            end = SwipeAction(stringResource(R.string.agents_todo_delete_action), PhIcons.Trash, Status.NeedsYou, {
                                scope.launch {
                                    try {
                                        tasks?.deleteTodo(todo.id)
                                    } catch (e: Exception) {
                                        onError(res.getString(R.string.agents_todo_delete_failed, e.message.orEmpty()))
                                    }
                                }
                            }, destructive = true)
                        ) {
                            JCard(
                                modifier = Modifier.fillMaxWidth(),
                                onClick = { onOpenTodo(todo.id) }
                            ) {
                                Column(
                                    Modifier.padding(Tokens.Space.m),
                                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)
                                ) {
                                    Row(
                                        verticalAlignment = Alignment.CenterVertically,
                                        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)
                                    ) {
                                        Box(
                                            Modifier
                                                .size(Tokens.Space.xxl)
                                                .clip(CircleShape)
                                                .clickable {
                                                    scope.launch {
                                                        try {
                                                            tasks?.setTodoDone(todo, true)
                                                        } catch (e: Exception) {
                                                            onError(res.getString(R.string.agents_todo_done_failed, e.message.orEmpty()))
                                                        }
                                                    }
                                                },
                                            contentAlignment = Alignment.Center
                                        ) {
                                            Box(
                                                Modifier
                                                    .size(ContentTokens.statusIcon)
                                                    .border(ContentTokens.optionOutline, PersonaTheme.palette.ink3, CircleShape)
                                            )
                                        }
                                        Text(
                                            todo.title,
                                            style = PersonaTheme.type.headline,
                                            color = PersonaTheme.palette.ink,
                                            maxLines = 1,
                                            overflow = TextOverflow.Ellipsis,
                                            modifier = Modifier.weight(1f)
                                        )
                                        JTag(state.label)
                                    }

                                    // One line of what is going on (iOS TodoRow): why it finished on its own, the most urgent
                                    // agent's latest (red while it waits for you), the notes, or that no agent has it yet.
                                    TodoActivityLine(todo, urgent, urgent?.let { cards[it.agent.key] })
                                    // Done as asked but not live yet (#3306): where it is held, while its agents are not
                                    // at it again.
                                    todo.hold?.takeIf { state != TodoState.Working && state != TodoState.NeedsYou }?.let {
                                        TodoHoldLine(it, Modifier.padding(start = Tokens.Space.xxxl))
                                    }
                                    // Due date, high priority, a reminder still to push (#1683).
                                    TodoPlanLine(todo, Modifier.padding(start = Tokens.Space.xxxl))

                                    // Relative time and linked agent count at the bottom
                                    Row(
                                        Modifier.fillMaxWidth().padding(start = Tokens.Space.xxxl, top = Tokens.Space.xxs),
                                        verticalAlignment = Alignment.CenterVertically,
                                        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)
                                    ) {
                                        val timeStr = relativeTime(res, todo.updated ?: todo.created, now)
                                        if (timeStr.isNotBlank()) {
                                            Text(timeStr, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
                                        }
                                        Spacer(Modifier.weight(1f))
                                        if (todo.currentAgents.isNotEmpty()) {
                                            Text(pluralStringResource(R.plurals.agents_todo_agents, todo.currentAgents.size, todo.currentAgents.size), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
                                        }
                                    }
                                }
                            }
                        }
                    }
                }

                // Foldable finished group at the bottom
                if (completed.isNotEmpty()) {
                    item(key = "completed-header") {
                        Row(
                            Modifier.fillMaxWidth().clickable { completedFolded = !completedFolded }.padding(vertical = Tokens.Space.xs),
                            verticalAlignment = Alignment.CenterVertically
                        ) {
                            JSectionHeader(
                                stringResource(R.string.agents_todos_done),
                                count = completed.size,
                                modifier = Modifier.weight(1f)
                            )
                            JIcon(if (completedFolded) PhIcons.CaretDown else PhIcons.CaretUp, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink3)
                        }
                    }

                    if (!completedFolded) {
                        itemsIndexed(completed, key = { _, todo -> "done:${todo.id}" }) { _, todo ->
                            val reopen: () -> Unit = {
                                scope.launch {
                                    try {
                                        tasks?.setTodoDone(todo, false)
                                    } catch (e: Exception) {
                                        onError(res.getString(R.string.agents_todo_reopen_failed, e.message.orEmpty()))
                                    }
                                }
                            }
                            // iOS: swipe right to reopen, left to delete.
                            JSwipeActions(
                                start = SwipeAction(stringResource(R.string.agents_todo_reopen), PhIcons.ArrowUUpLeft, Status.Working, reopen),
                                end = SwipeAction(stringResource(R.string.agents_todo_delete_action), PhIcons.Trash, Status.NeedsYou, {
                                    scope.launch {
                                        try {
                                            tasks?.deleteTodo(todo.id)
                                        } catch (e: Exception) {
                                            onError(res.getString(R.string.agents_todo_delete_failed, e.message.orEmpty()))
                                        }
                                    }
                                }, destructive = true),
                            ) {
                                JCard(
                                    modifier = Modifier.fillMaxWidth(),
                                    onClick = { onOpenTodo(todo.id) }
                                ) {
                                    Column(Modifier.padding(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                                        Row(
                                            verticalAlignment = Alignment.CenterVertically,
                                            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)
                                        ) {
                                            Box(
                                                Modifier
                                                    .size(Tokens.Space.xxl)
                                                    .clip(CircleShape)
                                                    .clickable(onClick = reopen),
                                                contentAlignment = Alignment.Center
                                            ) {
                                                JIcon(PhIcons.CheckCircle, null, size = ContentTokens.statusIcon, selected = true, tint = PersonaTheme.palette.done.dot)
                                            }
                                            Text(
                                                todo.title,
                                                style = PersonaTheme.type.body.copy(textDecoration = TextDecoration.LineThrough),
                                                color = PersonaTheme.palette.ink3,
                                                maxLines = 1,
                                                overflow = TextOverflow.Ellipsis,
                                                modifier = Modifier.weight(1f)
                                            )
                                            if (todo.doneBy == "auto") {
                                                JTag(stringResource(R.string.agents_todo_auto_done))
                                            }
                                        }
                                        todo.autoDoneReason?.let { reason ->
                                            Text(
                                                reason,
                                                style = PersonaTheme.type.caption,
                                                color = PersonaTheme.palette.ink2,
                                                maxLines = 2,
                                                overflow = TextOverflow.Ellipsis,
                                                modifier = Modifier.padding(start = Tokens.Space.xxxl)
                                            )
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

/** Where a task's finished work is held short of live, in a line (iOS `TodoHold.line`): Held at PR · Blocked by"…" · Needs your approval. */
@Composable
internal fun TodoHoldLine(hold: TodoHold, modifier: Modifier = Modifier) {
    val step = stringResource(when (hold.step) {
        HoldStep.Pr -> R.string.agents_todo_hold_pr
        HoldStep.Merged -> R.string.agents_todo_hold_merged
        HoldStep.Off -> R.string.agents_todo_hold_off
    })
    val gate = hold.gate?.let { stringResource(R.string.agents_todo_hold_gate, it) }
    val who = when (hold.unblock) {
        HoldUnblock.User -> stringResource(R.string.agents_todo_hold_user)
        HoldUnblock.Agent -> stringResource(R.string.agents_todo_hold_agent)
        HoldUnblock.Other -> stringResource(R.string.agents_todo_hold_other)
        null -> null
    }
    Text(
        listOfNotNull(step, gate, who).joinToString(" · "),
        style = PersonaTheme.type.caption,
        color = PersonaTheme.palette.cautionText,
        maxLines = 2,
        overflow = TextOverflow.Ellipsis,
        modifier = modifier
    )
}

/** A to-do's due date ("Due Thu, Nov 12, 18:00", red once past), high priority and a reminder still to push, in a line
 * (iOS `TodoPlanLine`, #1683); nothing without them. */
@Composable
internal fun TodoPlanLine(todo: Todo, modifier: Modifier = Modifier) {
    val zone = ZoneId.systemDefault()
    val locale = LocalConfiguration.current.locales[0]
    val now = Instant.now()
    val sameYear = { i: Instant -> i.atZone(zone).year == now.atZone(zone).year }
    val dateOnly = { i: Instant ->
        DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, if (sameYear(i)) "EEEMMMd" else "yEEEMMMd"), locale)
    }
    val withTime = { i: Instant ->
        DateTimeFormatter.ofPattern(DateFormat.getBestDateTimePattern(locale, if (sameYear(i)) "EEEMMMdHHmm" else "yEEEMMMdHHmm"), locale)
    }
    val dueText = parseTime(todo.dueAt)?.let { withTime(it).format(it.atZone(zone)) }
        ?: todo.dueDay?.let { d -> dateOnly(d.atStartOfDay(zone).toInstant()).format(d) }
    val remind = todo.remind?.takeIf { todo.remindedAt == null && !todo.isDone }
    val high = todo.priority == "high" && !todo.isDone
    if (dueText == null && remind == null && !high) return
    val overdue = todo.isOverdue(now, zone)
    val parts = listOfNotNull(
        stringResource(R.string.agents_todo_priority_high).takeIf { high },
        dueText?.let { stringResource(if (overdue) R.string.agents_todo_was_due else R.string.agents_todo_due, it) },
        remind?.let { stringResource(R.string.agents_todo_remind_at, withTime(it).format(it.atZone(zone))) },
    )
    Text(
        parts.joinToString(" · "),
        style = PersonaTheme.type.caption,
        color = if (overdue) PersonaTheme.palette.danger.text else PersonaTheme.palette.ink3,
        maxLines = 1,
        overflow = TextOverflow.Ellipsis,
        modifier = modifier
    )
}

/** Why the to-do finished on its own, when the hub said; null otherwise (iOS `Todo.autoDoneReason`). */
internal val Todo.autoDoneReason: String?
    get() = doneReason?.trim()?.takeIf { doneBy == "auto" && it.isNotEmpty() }

/** What the most urgent agent of a to-do says on its row (iOS TodosView.mostUrgentSummary). */
internal sealed interface TodoUrgentSummary {
    data class StartFailed(val error: String) : TodoUrgentSummary
    data class Words(val text: String) : TodoUrgentSummary
    /** What the card it waits on asks (#3964). */
    data class Asks(val text: CoreText) : TodoUrgentSummary
}

/**
 * iOS mostUrgentSummary: a failed start with its error; else what its [card] asks while it waits for you; else the
 * session's last words as plain text, or its title; else the agent's own title. Nothing when none of them says
 * anything, so the notes or "Not handed to an agent yet" show instead.
 */
internal fun mostUrgentSummary(urgent: TodoMostUrgent?, card: Interaction? = null): TodoUrgentSummary? {
    urgent ?: return null
    val error = urgent.agent.startError?.trim().orEmpty()
    if (urgent.isFailed && error.isNotEmpty()) return TodoUrgentSummary.StartFailed(error)
    urgent.session?.let { session ->
        if (!urgent.isFailed) session.askingLine(card)?.let { return TodoUrgentSummary.Asks(it) }
        val last = session.lastText?.let(::plainMarkdown)?.takeIf(String::isNotBlank)
        return TodoUrgentSummary.Words(last ?: session.displayTitle)
    }
    return urgent.agent.title?.takeIf(String::isNotBlank)?.let(TodoUrgentSummary::Words)
}

/** The open row's line under the title: the auto-done reason, the most urgent agent, the notes, or "Not handed to an agent yet". */
@Composable
private fun TodoActivityLine(todo: Todo, urgent: TodoMostUrgent?, card: Interaction?) {
    val palette = PersonaTheme.palette
    val reason = todo.autoDoneReason
    val summary = when (val line = mostUrgentSummary(urgent, card)) {
        is TodoUrgentSummary.StartFailed -> stringResource(R.string.agents_todo_start_failed, line.error)
        is TodoUrgentSummary.Words -> line.text
        is TodoUrgentSummary.Asks -> line.text.text()
        null -> null
    }
    val notes = todo.notes.trim().takeIf { it.isNotEmpty() }
    val text = reason ?: summary ?: notes ?: stringResource(R.string.agents_todo_not_handed)
    val waiting = reason == null && summary != null && urgent?.status == AgentState.NeedsYou
    Row(
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        modifier = Modifier.padding(start = Tokens.Space.xxxl)
    ) {
        if (reason == null && urgent != null) {
            JStatusDot(when (urgent.status) {
                AgentState.NeedsYou -> Status.NeedsYou
                AgentState.Unread -> Status.Unread
                AgentState.Working -> Status.Working
                else -> Status.Idle
            })
        }
        Text(
            text,
            style = PersonaTheme.type.caption,
            color = if (waiting) palette.needsYou.text else palette.ink2,
            maxLines = if (notes != null && text == notes) 1 else 2,
            overflow = TextOverflow.Ellipsis,
            modifier = Modifier.weight(1f)
        )
    }
}

/** A to-do's state in the language on screen (core:contracts' `TodoState.title` is the Chinese). */
internal val TodoState.label: String @Composable get() = stringResource(when (this) {
    TodoState.Done -> R.string.agents_todos_done
    TodoState.NeedsYou -> R.string.agents_todo_state_needs_you
    TodoState.Unread -> R.string.agents_todo_state_unread
    TodoState.Working -> R.string.agents_todo_state_working
    TodoState.FollowUp -> R.string.agents_todo_state_follow_up
    TodoState.NotStarted -> R.string.agents_todo_state_not_started
})
