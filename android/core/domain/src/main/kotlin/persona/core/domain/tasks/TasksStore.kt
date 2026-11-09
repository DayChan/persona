package persona.core.domain.tasks

import java.time.Clock
import java.time.Duration
import java.time.Instant
import java.time.LocalDate
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import persona.core.contracts.AgentState
import persona.core.contracts.AssistantFeedEvent
import persona.core.contracts.AssistantViewActionEvent
import persona.core.contracts.AssistantViewBlockEvent
import persona.core.contracts.AssistantViewEvent
import persona.core.contracts.FeedItem
import persona.core.contracts.Session
import persona.core.contracts.PlanTask
import persona.core.contracts.TaskPlan
import persona.core.contracts.TaskPlanEvent
import persona.core.contracts.Todo
import persona.core.contracts.TodoSpace
import persona.core.contracts.TodosEvent
import persona.core.contracts.draftPlaceholder
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.domain.views.AssistantCardActions
import persona.core.domain.views.AssistantViewStore

/**
 * The to-dos last read from this pairing's hub, kept on the phone (iOS TodoStore's StateCache): a cold start or an open
 * without the hub shows them instead of "No to-dos" until the hub answers.
 */
interface TodoCache {
    suspend fun read(): List<Todo>?
    suspend fun write(todos: List<Todo>)
}

/** How long a Today card is held before an open asks the hub again whether its day has moved on (#5194). */
private val todayRecheck = Duration.ofMinutes(10)

class TasksStore(
    private val api: TasksApi,
    connection: HubConnection,
    private val scope: CoroutineScope,
    private val cache: TodoCache? = null,
    private val clock: Clock = Clock.systemDefaultZone(),
) : TodoDecisionsSource {
    private val _todos = MutableStateFlow<List<Todo>>(emptyList())
    val todos: StateFlow<List<Todo>> = _todos.asStateFlow()
    /** Guards [answered] with the list: the cached list never replaces one the hub has given. */
    private val todosLock = Any()
    /** The hub's last answer (a list, or an older hub's 404 as none); null until it answers, while the cache shows. */
    private var answered: List<Todo>? = null
    private val writing = Mutex()
    private val refreshingTodos = Mutex()
    private val agentLookup = TaskAgentLookup(api, scope)
    val agentSessions = agentLookup.sessions
    val agentLookupFailures = agentLookup.failures

    /** Called by the visible conversation; exact lookups never change the Agents list window. */
    fun watchAgents(keys: Set<persona.core.contracts.SessionKey>) = agentLookup.watch(keys)
    private var written: List<Todo>? = null
    private val _todosSupported = MutableStateFlow(true)
    val todosSupported: StateFlow<Boolean> = _todosSupported.asStateFlow()
    private val _plans = MutableStateFlow<Map<String, TaskPlan>>(emptyMap())
    val plans: StateFlow<Map<String, TaskPlan>> = _plans.asStateFlow()
    private val _feeds = MutableStateFlow<Map<String, List<FeedItem>>>(emptyMap())
    val feeds: StateFlow<Map<String, List<FeedItem>>> = _feeds.asStateFlow()
    /** Conversations whose whole feed has been read once: a live item alone does not make a later load incremental (#4689). */
    private val loaded = MutableStateFlow<Set<String>>(emptySet())
    /** Conversation → the task the next message goes straight to ("Say to 'xx'"). */
    val routes = MutableStateFlow<Map<String, String>>(emptyMap())
    val busy = MutableStateFlow<Set<String>>(emptySet())
    /** Message cards (inbox item ids) put off with "Later". */
    val later = MutableStateFlow<Set<String>>(emptySet())
    /** Conversation → when the Today card was last asked for, and the day of the card the hub gave (null while asking). */
    private val todayAsked = HashMap<String, Pair<Instant, LocalDate?>>()
    /** This hub's assistant cards (docs/assistant/assistant-views.md): live cards, answers and registered reply blocks. */
    val cards = AssistantViewStore()
    val cardActions = AssistantCardActions(api, cards, reload = { load(it, whole = true) })

    init {
        if (cache != null) scope.launch(start = CoroutineStart.UNDISPATCHED) {
            val cached = try { cache.read() } catch (e: CancellationException) { throw e } catch (_: Exception) { null }
            if (!cached.isNullOrEmpty()) synchronized(todosLock) {
                if (answered == null && _todos.value.isEmpty()) { written = cached; _todos.value = cached }
            }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.events.collect { e ->
                when (e) {
                    is TodosEvent -> reloadTodos()
                    is persona.core.contracts.SessionsListEvent -> agentLookup.refresh()
                    is AssistantFeedEvent -> received(listOf(e.item))
                    is TaskPlanEvent -> _plans.update { it + (e.plan.id to e.plan) }
                    is AssistantViewEvent -> cards.view(e.conversation, e.view)
                    is AssistantViewActionEvent -> cards.answer(e.conversation, e.action)
                    is AssistantViewBlockEvent -> cards.block(e.conversation, e.key)
                    else -> Unit
                }
            }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.state.collect { if (it == ConnectionState.Online) { reloadTodos(); agentLookup.refresh(); loaded.value.forEach(::load) } }
        }
    }

    fun reloadTodos() {
        scope.launch {
            refreshingTodos.withLock {
                try {
                    val list = api.todos()
                    _todosSupported.value = true
                    todosLoaded = true
                    synchronized(todosLock) { answered = list; if (list != _todos.value) _todos.value = list }
                    dropStaleRoutes()
                    remember()
                } catch (cancelled: CancellationException) {
                    throw cancelled
                } catch (error: Exception) {
                    val status = error::class.java.methods.firstOrNull { it.name == "getStatus" }?.invoke(error) as? Int
                    if (status == 404 || error.message?.contains("404") == true) {
                        _todosSupported.value = false
                        synchronized(todosLock) { answered = emptyList(); _todos.value = emptyList() }
                        remember()
                    }
                    // Failed reads retain the last confirmed binding; a reconnect or open retries the read.
                }
            }
        }
    }

    /**
     * Keeps the hub's latest answer for the next start, one write at a time (two reads can answer out of order); a failed
     * write only costs that start its first paint.
     */
    private suspend fun remember() {
        if (cache == null) return
        writing.withLock {
            val latest = synchronized(todosLock) { answered } ?: return
            if (latest == written) return
            try { cache.write(latest); written = latest }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { }
        }
    }

    suspend fun createTodo(title: String, notes: String = ""): Todo {
        val created = api.createTodo(title, notes)
        reloadTodos()
        return created
    }

    suspend fun setTodoDone(todo: Todo, done: Boolean): Todo {
        val before = _todos.value
        val now = Instant.now().toString()
        _todos.update { list -> list.map { if (it.id == todo.id) it.copy(doneAt = if (done) now else null, doneBy = if (done) "user" else null) else it } }
        return try {
            val updated = api.patchTodo(todo.id, done = done)
            _todos.update { list -> list.map { if (it.id == updated.id) updated else it } }
            updated
        } catch (t: Throwable) {
            _todos.value = before
            throw t
        }
    }

    suspend fun updateTodo(id: String, title: String? = null, notes: String? = null): Todo {
        val updated = api.patchTodo(id, title = title, notes = notes)
        _todos.update { list -> list.map { if (it.id == updated.id) updated else it } }
        return updated
    }

    suspend fun deleteTodo(id: String) {
        api.deleteTodo(id)
        _todos.update { list -> list.filterNot { it.id == id } }
        dropStaleRoutes()
    }

    /** Routes to to-dos no longer listed (deleted here or on another device) go: their chip is hidden already. */
    private fun dropStaleRoutes() {
        val ids = _todos.value.mapTo(HashSet()) { it.id }
        routes.update { r -> r.filterValues { it in ids }.takeIf { it.size != r.size } ?: r }
    }

    /** A hub's router answers 404 for a route it doesn't have (isLegacySpacesRoute): one from before decisions shows none. */
    override suspend fun decisions(todoId: String): List<persona.core.contracts.TodoDecision>? = try {
        api.todoDecisions(todoId)
    } catch (e: CancellationException) {
        throw e
    } catch (e: Exception) {
        if (isLegacySpacesRoute(e)) null else throw e
    }

    override suspend fun addDecision(todoId: String, question: String, choice: String, reason: String, supersedesId: String) =
        api.addTodoDecision(todoId, question, choice, reason, supersedesId)

    suspend fun linkTodoAgent(todoId: String, machineId: String, sessionId: String) {
        api.linkTodoAgent(todoId, machineId, sessionId)
        reloadTodos()
    }

    suspend fun unlinkTodoAgent(todoId: String, machineId: String, sessionId: String) {
        api.unlinkTodoAgent(todoId, machineId, sessionId)
        reloadTodos()
    }

    /** A read of the to-dos has answered: until then nothing tells whether the hub knows projects. */
    @Volatile private var todosLoaded = false
    /** The hub answered "Attach to to-do" with its router's 404: it has no such route (iOS TodosLogic.isLegacySpacesRoute). */
    @Volatile private var legacySpaces = false

    /**
     * Whether this hub knows projects on todos (#1616): an older one leaves `spaces` out altogether (iOS supportsSpaces).
     * False until the first read answers, and for good once the hub has no route for it.
     */
    val spacesSupported: Boolean
        get() = todosLoaded && !legacySpaces && _todos.value.let { list -> list.isEmpty() || list.any { it.spaces != null } }

    /**
     * "Attach to to-do" / "Remove from to-do" a whole project. A project is on one todo at a time: linking it takes it, and the agents it
     * brought, off any other first. The change shows at once; the hub then supplies the agents it attached.
     */
    suspend fun setTodoSpace(todoId: String, machineId: String, space: String, linked: Boolean) {
        val before = _todos.value
        val via = "$machineId/$space"
        _todos.update { list ->
            list.map { todo ->
                if (!linked && todo.id != todoId) return@map todo
                val others = todo.spaces?.filterNot { it.machineId == machineId && it.space == space }
                val spaces = if (linked && todo.id == todoId) others.orEmpty() + TodoSpace(machineId, space) else others
                val agents = if (!linked || todo.id != todoId) todo.agents.filterNot { it.source == "space" && it.via == via } else todo.agents
                todo.copy(spaces = spaces, agents = agents)
            }
        }
        try {
            if (linked) api.linkTodoSpace(todoId, machineId, space) else api.unlinkTodoSpace(todoId, machineId, space)
        } catch (t: Throwable) {
            _todos.value = before
            if (isLegacySpacesRoute(t)) legacySpaces = true
            throw t
        } finally {
            reloadTodos()
        }
    }

    /**
     * Pulls [conversation]'s feed: the items after the last one held (all of them with [whole] or before its first read; from the
     * oldest unsent message card on while it holds one, whose `filed_at` the hub works out on each read and sends no event for,
     * #5693 — not from the start, as the hub pages oldest first), and every time the full set of its cards, answers and registered blocks.
     */
    fun load(conversation: String, whole: Boolean = false) {
        scope.launch {
            val held = _feeds.value[conversation].orEmpty()
            val unsent = held.indexOfFirst { it.kind == "message" && it.sent == null }
            val since = when {
                whole || conversation !in loaded.value -> null
                unsent >= 0 -> held.take(unsent).lastOrNull { it.at != held[unsent].at }?.at // reads are after `since`: skip ties
                else -> held.lastOrNull()?.at
            }
            runCatching { api.feed(conversation, since) }.onSuccess { res ->
                if (res.plans.isNotEmpty()) _plans.update { it + res.plans }
                cards.snapshot(conversation, res.views, res.viewActions, res.viewBlocks)
                received(res.items, conversation)
                loaded.update { it + conversation }
            }
        }
    }

    private var main: String? = null
    /** The main assistant conversation's id once read: its dock also shows the agents no task stands for (#1113). */
    val mainConversation = MutableStateFlow<String?>(null)

    /**
     * The "Today" card, for the main conversation only (as iOS). A conversation that isn't the one held asks the hub again:
     * "New main chat", a move to another machine or another device replaces the main (#4690).
     */
    fun loadTodayIfMain(conversation: String) {
        scope.launch {
            val id = main.takeIf { it == conversation }
                ?: runCatching { api.mainConversation() }.getOrNull()?.also { main = it; mainConversation.value = it }
            if (id == conversation) loadToday(conversation)
        }
    }

    /** What [conversation]'s dock shows after its tasks (iOS `DockAgent.shown`); see [dockAgents]. */
    fun agentsOf(conversation: String, sessions: List<Session>): List<Session> =
        dockAgents(conversation, mainConversation.value, sessions, _todos.value)

    /**
     * The hub counts the day in the assistant's time zone, which need not be the phone's (#5194): the card is asked
     * again on the phone's new day, and on an open [todayRecheck] after the last ask unless the card's day is already
     * past the phone's (that hub cannot move on before the phone does).
     */
    fun loadToday(conversation: String) {
        val now = clock.instant()
        val today = LocalDate.now(clock)
        todayAsked[conversation]?.let { (at, day) ->
            if (LocalDate.ofInstant(at, clock.zone) == today &&
                (day == null || day > today || now < at + todayRecheck)) return
        }
        todayAsked[conversation] = now to null
        scope.launch {
            runCatching { api.today(conversation) }.onSuccess { card ->
                todayAsked[conversation] = now to (card.ref?.let { runCatching { LocalDate.parse(it) }.getOrNull() } ?: today)
                received(listOf(card))
            }.onFailure { todayAsked.remove(conversation) }
        }
    }

    private fun received(items: List<FeedItem>, conversation: String? = null) {
        // A live card's creation item carries the card as it was then: drawn even if its assistant_view was missed.
        items.filter { it.kind == "view" }.forEach { item ->
            ((item.body as? JsonObject)?.get("view"))?.let { cards.view(item.conversation, it) }
        }
        _feeds.update { feeds ->
            var next = conversation?.let { if (it in feeds) feeds else feeds + (it to emptyList()) } ?: feeds
            for ((c, list) in items.groupBy { it.conversation }) next = next + (c to AssistantLines.merge(next[c].orEmpty(), list))
            next
        }
        if (items.any { it.kind == "task" || it.kind == "receipt" }) reloadTodos()
    }

    fun tasksOf(conversation: String): List<Todo> = _todos.value.filter { it.origin == conversation }

    /** The task's agents among [sessions], and its state. */
    fun agentsOf(todo: Todo, sessions: List<Session>): List<Session> =
        todo.activeAgents.filter { it.dismissedAt == null && it.machineId.isNotBlank() && it.sessionId.isNotBlank() }.map { a ->
            val listed = sessions.firstOrNull { it.key == a.key }
            val exact = agentSessions.value[a.key]
            listOfNotNull(listed, exact).maxByOrNull { it.rev ?: 0L } ?: Session(
                machineId = a.machineId, sessionId = a.sessionId, title = a.title.orEmpty(), cwd = a.cwd.orEmpty(),
                agent = a.agent, state = "offline", status = "offline", startError = a.startError,
            )
        }

    fun stateOf(todo: Todo, sessions: List<Session>): TaskState =
        agentsOf(todo, sessions).let { a -> TaskState.of(todo, a.map { it.agentState }, a.any { it.interrupted }) }

    /** The agent to open first: the one waiting for the user, then one cut off (#1516), then working, then the latest. */
    fun lead(todo: Todo, sessions: List<Session>): Session? =
        agentsOf(todo, sessions).minWithOrNull(compareBy<Session>({ it.agentState != AgentState.NeedsYou }, { !it.interrupted },
            { it.agentState.priority }).thenByDescending { it.updatedAt.orEmpty() })

    /** [edits] is "Edit": the plan's tasks as the user changed them, by index (null: as planned). */
    fun startPlan(plan: TaskPlan, remember: Boolean, onError: (Throwable) -> Unit, edits: List<PlanTask>? = null) = act(plan.id, onError) {
        val p = if (edits == null) api.startPlan(plan.id, remember) else api.startPlan(plan.id, remember, edits)
        _plans.update { it + (p.id to p) }; reloadTodos()
    }

    fun dismissPlan(plan: TaskPlan, onError: (Throwable) -> Unit) = act(plan.id, onError) {
        val p = api.dismissPlan(plan.id); _plans.update { it + (p.id to p) }
    }

    /**
     * "Retry" on a task whose agent did not start (#1396): the hub starts the same task again in the same todo, which it
     * answers reopened — put in at once, so its card shows it starting where it was. Taps while one is on its way do
     * nothing ([busy] under [retryKey]); the hub starts one agent however many arrive. On a task the model service cut
     * off (#1516) it is "Continue": the hub tells its agent to go on, once.
     */
    fun retryTask(todo: Todo, onError: (Throwable) -> Unit) = act(retryKey(todo.id), onError) {
        val t = api.retryTask(todo.id)
        _todos.update { list -> list.map { if (it.id == t.id) t else it } }
        reloadTodos()
    }

    /** Removes a task that does not run (one whose agent never started): its todo goes, and any route to it. */
    fun removeTask(todo: Todo, onError: (Throwable) -> Unit) = act("remove:${todo.id}", onError) {
        api.removeTask(todo.id)
        _todos.update { list -> list.filterNot { it.id == todo.id } }
        routes.update { r -> r.filterValues { it != todo.id } }
        reloadTodos()
    }

    private fun act(id: String, onError: (Throwable) -> Unit, block: suspend () -> Unit) {
        if (id in busy.value) return
        busy.update { it + id }
        scope.launch {
            runCatching { block() }.onFailure(onError)
            busy.update { it - id }
        }
    }

    fun setRoute(conversation: String, todoId: String?) {
        routes.update { if (todoId == null) it - conversation else it + (conversation to todoId) }
    }

    /** Sends text straight to the task the conversation is routed to; false when there is none or its to-do is gone. */
    fun sendRouted(conversation: String, text: String, onError: (Throwable) -> Unit): Boolean {
        val todoId = routes.value[conversation] ?: return false
        setRoute(conversation, null)
        if (_todos.value.none { it.id == todoId }) return false // the hub would refuse it (409): Persona gets it instead
        scope.launch { runCatching { api.route(conversation, todoId, text) }.onSuccess { received(listOf(it)) }.onFailure(onError) }
        return true
    }

    /**
     * "Send" on a message card: the (edited) draft goes out through the inbox; the hub records it on the card, whose
     * `assistant_feed` event draws it sent (put in at once here too). Taps while one is on its way, and on a card
     * already sent or dealt with elsewhere (`filed_at`, #5095), do nothing ([busy] under the card's id); a draft with a
     * blank to fill in is not sent.
     */
    fun sendMessage(card: FeedItem, text: String, onError: (Throwable) -> Unit) {
        val itemId = card.ref ?: return
        val latest = { _feeds.value[card.conversation]?.firstOrNull { it.id == card.id } ?: card }
        if (!latest().canSend) return
        draftPlaceholder(text)?.let { onError(DraftPlaceholderLeft(it)); return }
        act(card.id, onError) {
            api.inboxReply(itemId, text, card.id)
            val now = latest()
            if (now.sent == null) {
                val body = JsonObject(((now.body as? JsonObject) ?: JsonObject(emptyMap())) +
                    mapOf("sent_at" to JsonPrimitive(Instant.now().toString()), "sent" to JsonPrimitive(text)))
                received(listOf(now.copy(body = body)))
            }
        }
    }

    fun putOff(itemId: String) = later.update { it + itemId }

    companion object {
        /** The [busy] key of a task's "Retry". */
        fun retryKey(todoId: String) = "retry:$todoId"
    }
}
