package persona.feature.agents

import persona.core.domain.tasks.hold
import persona.feature.chat.ReviewChecklist
import persona.core.designsystem.text
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.launch
import persona.core.contracts.AgentState
import persona.core.contracts.CoreText
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoState
import persona.core.contracts.state
import persona.core.designsystem.*
import persona.core.domain.answer.askingLine
import persona.core.domain.answer.firstBySession
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.sort.slotLabel
import persona.core.domain.tasks.TasksStore
import persona.core.domain.tasks.TodoDecisionsSource
import java.time.Instant

/** Where an agent on a to-do came from (iOS TodosLogic.agentSource): with a project, sent by another agent, the same chat. */
@Composable
internal fun agentSource(agent: TodoAgent, lookup: (SessionKey) -> Session?): String? = when (agent.source) {
    "space" -> agent.via.orEmpty().substringAfter('/').let { name ->
        if (name.isBlank()) stringResource(R.string.agents_todo_source_space) else stringResource(R.string.agents_todo_source_space_named, name)
    }
    "spawned" -> agent.via?.takeIf { '/' in it }?.let { via -> lookup(SessionKey(via.substringBefore('/'), via.substringAfter('/'))) }
        ?.displayTitle?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.agents_todo_source_spawned_by, it) }
        ?: stringResource(R.string.agents_todo_source_spawned)
    "chat" -> stringResource(R.string.agents_todo_source_chat)
    else -> null
}

/** An agent row's last line on a to-do's page: what its [card] asks while it waits for you, else its last words. */
internal fun todoAgentPreview(session: Session?, card: Interaction?): CoreText? =
    session?.askingLine(card) ?: session?.lastText?.takeIf { it.isNotBlank() }?.let(CoreText::plain)

@Composable
fun TodoDetailScreen(
    todoId: String,
    todos: List<Todo>,
    machines: List<Machine>,
    sessions: List<Session>,
    tasks: TasksStore?,
    onBack: () -> Unit,
    onOpenSession: (SessionKey) -> Unit,
    onHandOff: (Todo) -> Unit,
    onError: (String) -> Unit = {},
    now: Instant = Instant.now(),
    /** The cards waiting for an answer: a waiting agent's row says what its card asks (#3964). */
    pending: List<Interaction> = emptyList(),
    /** Where the Decisions section reads and records (#1618); the hub's store unless a test passes its own. */
    decisions: TodoDecisionsSource? = tasks,
) {
    val cards = remember(pending) { pending.firstBySession() }
    val todo = todos.firstOrNull { it.id == todoId }
    val scope = rememberCoroutineScope()
    val res = agentResources()
    val decisionsState = remember(decisions) { TodoDecisionsState(decisions) }
    // The hub moves the to-do's updated_at when a decision is written, here or anywhere (an answered card too).
    LaunchedEffect(decisionsState, todoId, todo?.updatedAt) {
        decisionsState.select(todoId)
        decisionsState.load()
    }

    if (todo == null) {
        JPage(title = stringResource(R.string.agents_todo_detail), onBack = onBack) {
            Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                JEmptyState(stringResource(R.string.agents_todo_gone), stringResource(R.string.agents_todo_gone_hint))
            }
        }
        return
    }

    var menuOpen by remember { mutableStateOf(false) }
    var confirmDelete by remember { mutableStateOf(false) }
    var editingTitle by remember { mutableStateOf(false) }
    var editingNotes by remember { mutableStateOf(false) }
    var titleText by remember(todo.title) { mutableStateOf(todo.title) }
    var notesText by remember(todo.notes) { mutableStateOf(todo.notes) }
    var pastFolded by rememberSaveable { mutableStateOf(true) }

    val sessionMap = remember(sessions) { sessions.associateBy { it.key } }
    val sessionLookup: (SessionKey) -> Session? = remember(sessionMap) { { key: SessionKey -> sessionMap[key] } }
    val state = todo.state(sessionLookup)

    if (confirmDelete) {
        JDialog(
            onDismiss = { confirmDelete = false },
            title = stringResource(R.string.agents_todo_delete_title),
            text = stringResource(R.string.agents_todo_delete_text, todo.title),
            confirm = stringResource(R.string.agents_todo_delete_action) to {
                confirmDelete = false
                scope.launch {
                    try {
                        tasks?.deleteTodo(todo.id)
                        onBack()
                    } catch (e: Exception) {
                        onError(res.getString(R.string.agents_todo_delete_error, e.message.orEmpty()))
                    }
                }
            },
            destructive = true
        )
    }

    decisionsState.form?.let { form ->
        TodoDecisionSheet(form.supersedes, onDismiss = { decisionsState.form = null }) { question, choice, reason ->
            decisionsState.add(question, choice, reason, form.supersedes) { e ->
                onError(res.getString(R.string.agents_todo_decision_failed, e.message.orEmpty()))
            }
        }
    }

    if (menuOpen) {
        JActionSheet(
            actions = listOf(
                JAction(
                    label = stringResource(R.string.agents_todo_delete_title),
                    icon = PhIcons.Trash,
                    destructive = true,
                    onClick = { confirmDelete = true }
                )
            ),
            onDismiss = { menuOpen = false },
            title = stringResource(R.string.agents_todo_actions)
        )
    }

    JPage(
        title = stringResource(R.string.agents_todo_detail),
        onBack = onBack,
        actions = {
            JIconButton(PhIcons.DotsThree, stringResource(R.string.agents_todo_more), { menuOpen = true })
        },
        bottomBar = {
            JBottomBar {
                Row(
                    Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.xs, vertical = Tokens.Space.xxs),
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)
                ) {
                    JButton(
                        text = stringResource(if (todo.isDone) R.string.agents_todo_reopen else R.string.agents_todo_complete),
                        kind = JButtonKind.Secondary,
                        icon = if (todo.isDone) PhIcons.ArrowClockwise else PhIcons.Check,
                        onClick = {
                            scope.launch {
                                try {
                                    tasks?.setTodoDone(todo, !todo.isDone)
                                } catch (e: Exception) {
                                    onError(res.getString(R.string.agents_todo_state_failed, e.message.orEmpty()))
                                }
                            }
                        },
                        modifier = Modifier.weight(1f)
                    )
                    JButton(
                        text = stringResource(R.string.agents_todo_hand_to_agent),
                        icon = PhIcons.Robot,
                        onClick = { onHandOff(todo) },
                        modifier = Modifier.weight(1f)
                    )
                }
            }
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
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)
        ) {
            // To-do title and notes card
            item(key = "title-notes") {
                JCard(Modifier.fillMaxWidth()) {
                    Column(
                        Modifier.padding(Tokens.Space.m),
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)
                    ) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            val status = when (state) {
                                TodoState.NeedsYou -> Status.NeedsYou
                                TodoState.Unread -> Status.Unread
                                TodoState.Working -> Status.Working
                                else -> Status.Idle
                            }
                            JStatusDot(status)
                            Spacer(Modifier.width(Tokens.Space.s))
                            if (todo.isDone) {
                                if (todo.doneBy == "auto") {
                                    JTag(stringResource(R.string.agents_todo_auto_done))
                                } else {
                                    JTag(stringResource(R.string.agents_todos_done))
                                }
                            } else {
                                JTag(state.label)
                            }
                            // iOS: "Status · N agents · created X".
                            Text(
                                listOfNotNull(
                                    pluralStringResource(R.plurals.agents_todo_agents, todo.currentAgents.size, todo.currentAgents.size),
                                    relativeTime(res, todo.created, now).takeIf { it.isNotBlank() }?.let { stringResource(R.string.agents_todo_created_at, it) },
                                ).joinToString(" · ", prefix = " · "),
                                style = PersonaTheme.type.caption,
                                color = PersonaTheme.palette.ink2,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                                modifier = Modifier.weight(1f).padding(start = Tokens.Space.xs),
                            )
                            if (!editingTitle && !editingNotes) {
                                JIconButton(PhIcons.NotePencil, stringResource(R.string.agents_todo_edit), {
                                    editingTitle = true
                                    editingNotes = true
                                })
                            }
                        }

                        if (editingTitle) {
                            JTextField(
                                titleText,
                                { titleText = it },
                                label = stringResource(R.string.agents_todo_title_label),
                                singleLine = true
                            )
                        } else {
                            Text(
                                todo.title,
                                style = PersonaTheme.type.title,
                                color = PersonaTheme.palette.ink
                            )
                        }

                        if (editingNotes) {
                            JTextField(
                                notesText,
                                { notesText = it },
                                label = stringResource(R.string.agents_todo_notes_label),
                                placeholder = stringResource(R.string.agents_todo_notes_edit_placeholder),
                                minLines = 3,
                                maxLines = 8
                            )
                            Row(
                                Modifier.fillMaxWidth(),
                                horizontalArrangement = Arrangement.End
                            ) {
                                JTextButton(stringResource(R.string.agents_todo_cancel), onClick = {
                                    titleText = todo.title
                                    notesText = todo.notes
                                    editingTitle = false
                                    editingNotes = false
                                })
                                Spacer(Modifier.width(Tokens.Space.s))
                                JButton(stringResource(R.string.agents_todo_save), onClick = {
                                    editingTitle = false
                                    editingNotes = false
                                    scope.launch {
                                        try {
                                            tasks?.updateTodo(todo.id, title = titleText.trim(), notes = notesText.trim())
                                        } catch (e: Exception) {
                                            onError(res.getString(R.string.agents_todo_save_failed, e.message.orEmpty()))
                                        }
                                    }
                                })
                            }
                        } else if (todo.notes.isNotBlank()) {
                            Text(
                                todo.notes,
                                style = PersonaTheme.type.body,
                                color = PersonaTheme.palette.ink2
                            )
                        } else {
                            Text(
                                stringResource(R.string.agents_todo_notes_empty),
                                style = PersonaTheme.type.caption,
                                color = PersonaTheme.palette.ink3,
                                modifier = Modifier.clickable {
                                    editingTitle = true
                                    editingNotes = true
                                }
                            )
                        }

                        if (todo.isDone && todo.doneBy == "auto" && !todo.doneReason.isNullOrBlank()) {
                            Text(
                                stringResource(R.string.agents_todo_auto_reason, todo.doneReason.orEmpty()),
                                style = PersonaTheme.type.caption,
                                color = PersonaTheme.palette.ink3
                            )
                        }
                        // Done as asked but not live yet: where it is held and who can unhold it (#3306).
                        todo.hold?.let { TodoHoldLine(it) }
                        // What done means for a task the main conversation supervises, and where its review stands (#3124).
                        ReviewChecklist(todo)
                    }
                }
            }

            // Decisions made on it, under the notes as on iOS and the web (#1618)
            todoDecisionsSection(decisionsState, todo, sessionLookup, now)

            // Projects (workspaces linked as a whole, iOS TodoSpacesSection)
            val spaces = todo.spaces.orEmpty().takeIf { tasks?.spacesSupported != false }.orEmpty()
            if (spaces.isNotEmpty()) {
                item(key = "spaces-header") {
                    JSectionHeader(stringResource(R.string.agents_todo_spaces), count = spaces.size)
                }
                itemsIndexed(spaces, key = { _, sp -> "space:${sp.machineId}/${sp.space}" }) { index, sp ->
                    val machineName = machines.firstOrNull { it.id == sp.machineId }?.displayName ?: sp.machineId
                    val live = machines.firstOrNull { it.id == sp.machineId }?.online != false && sessions.any { session ->
                        session.machineId == sp.machineId && session.agentState != AgentState.Offline &&
                            (session.spaceName?.takeIf { it.isNotBlank() } ?: session.spaceId) == sp.space
                    }
                    JGroupItem(GroupPosition.of(index, spaces.size)) {
                        JRow(
                            title = stringResource(R.string.agents_todo_location, sp.space, machineName),
                            subtitle = if (live) null else stringResource(R.string.agents_todo_space_no_agent),
                            leadingIcon = PhIcons.SquaresFour,
                            trailing = {
                                JTextButton(stringResource(R.string.agents_todo_space_remove), onClick = {
                                    scope.launch {
                                        try {
                                            tasks?.setTodoSpace(todo.id, sp.machineId, sp.space, linked = false)
                                        } catch (e: Exception) {
                                            onError(res.getString(R.string.agents_todo_remove_failed, e.message.orEmpty()))
                                        }
                                    }
                                })
                            },
                            chevron = false,
                        )
                    }
                }
            }

            // Agents working on it
            item(key = "active-header") {
                JSectionHeader(stringResource(R.string.agents_todo_active_agents), count = todo.currentAgents.size)
            }

            if (todo.currentAgents.isEmpty()) {
                item(key = "active-empty") {
                    JEmptyState(stringResource(R.string.agents_todo_no_agents), stringResource(R.string.agents_todo_no_agents_hint))
                }
            } else {
                itemsIndexed(todo.currentAgents, key = { _, a -> a.key.path }) { index, agent ->
                    val session = sessionLookup(agent.key)
                    val machineName = machines.firstOrNull { it.id == agent.machineId }?.displayName ?: agent.machineId
                    val title = session?.displayTitle ?: agent.title?.takeIf { it.isNotBlank() } ?: stringResource(R.string.agents_todo_session_short, agent.sessionId.take(8))
                    // iOS: computer · space · tab, where it came from, then what it says now.
                    val place = listOf(machineName, session?.spaceName, session?.slotLabel).filterNot { it.isNullOrBlank() }.joinToString(" · ")
                        .ifBlank { stringResource(R.string.agents_todo_location, machineName, session?.folderName ?: agent.cwd?.substringAfterLast('/').orEmpty()) }
                    val source = agentSource(agent, sessionLookup)
                    val preview = todoAgentPreview(session, cards[agent.key])?.text() ?: if (session == null) stringResource(R.string.agents_todo_not_listed) else null
                    val subtitle = listOfNotNull(place, source, preview).joinToString("\n")
                    val status = session?.let {
                        Status.entries.firstOrNull { s -> s.name == it.agentState.name }
                    } ?: Status.Idle

                    JGroupItem(GroupPosition.of(index, todo.currentAgents.size)) {
                        Column {
                            JRow(
                                title = title,
                                subtitle = subtitle,
                                subtitleLines = 4,
                                leading = { JStatusDot(status) },
                                trailing = {
                                    JIconButton(PhIcons.X, stringResource(R.string.agents_todo_remove_agent), {
                                        scope.launch {
                                            try {
                                                tasks?.unlinkTodoAgent(todo.id, agent.machineId, agent.sessionId)
                                            } catch (e: Exception) {
                                                onError(res.getString(R.string.agents_todo_remove_failed, e.message.orEmpty()))
                                            }
                                        }
                                    })
                                },
                                chevron = session != null,
                                onClick = { onOpenSession(agent.key) }
                            )
                            if (!agent.startError.isNullOrBlank()) {
                                Row(
                                    Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.l, vertical = ContentTokens.tightGap),
                                    verticalAlignment = Alignment.CenterVertically
                                ) {
                                    Text(
                                        stringResource(R.string.agents_todo_start_failed, agent.startError.orEmpty()),
                                        style = PersonaTheme.type.caption,
                                        color = PersonaTheme.palette.needsYou.text,
                                        modifier = Modifier.weight(1f)
                                    )
                                    JTextButton(stringResource(R.string.agents_todo_remove), onClick = {
                                        scope.launch {
                                            try {
                                                tasks?.unlinkTodoAgent(todo.id, agent.machineId, agent.sessionId)
                                            } catch (e: Exception) {
                                                onError(res.getString(R.string.agents_todo_remove_error, e.message.orEmpty()))
                                            }
                                        }
                                    })
                                }
                            }
                        }
                    }
                }
            }

            // Folded section of agents that took part
            if (todo.releasedAgents.isNotEmpty()) {
                item(key = "past-header") {
                    Row(
                        Modifier.fillMaxWidth().clickable { pastFolded = !pastFolded }.padding(vertical = Tokens.Space.xs),
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        JSectionHeader(
                            stringResource(R.string.agents_todo_past_agents),
                            count = todo.releasedAgents.size,
                            modifier = Modifier.weight(1f)
                        )
                        JIcon(if (pastFolded) PhIcons.CaretDown else PhIcons.CaretRight, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink3)
                    }
                }

                if (!pastFolded) {
                    itemsIndexed(todo.releasedAgents, key = { _, a -> "rel:${a.key.path}" }) { index, agent ->
                        val session = sessionLookup(agent.key)
                        val title = session?.displayTitle ?: agent.title?.takeIf { it.isNotBlank() } ?: stringResource(R.string.agents_todo_session_short, agent.sessionId.take(8))
                        val subtitle = stringResource(R.string.agents_todo_unlinked, agent.machineId)
                        JGroupItem(GroupPosition.of(index, todo.releasedAgents.size)) {
                            JRow(
                                title = title,
                                subtitle = subtitle,
                                leading = { JStatusDot(Status.Idle) },
                                chevron = session != null,
                                onClick = { onOpenSession(agent.key) }
                            )
                        }
                    }
                }
            }
        }
    }
}
