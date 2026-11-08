package persona.feature.assistant

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.material3.Text
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.Session
import persona.core.contracts.Machine
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantRefused
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantReadinessFeature
import persona.core.contracts.api.ReadinessSuggestion
import persona.core.contracts.api.ReadinessStep
import persona.core.contracts.api.ChannelSources
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.PendingMainConversation
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.DraftRepository
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.ChatComposerService
import persona.feature.chat.ChatComposer
import persona.feature.chat.ChatComposerState
import persona.feature.chat.ChatDraftEditor
import persona.feature.chat.rememberChatComposerState
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ModelPurposeRoute
import persona.core.navigation.Navigator
import persona.feature.assistant.proactive.AssistantProactiveScreen
import persona.feature.assistant.settings.AssistantSettingsScreen
import persona.feature.assistant.settings.StoredTimezoneChoice
import persona.feature.assistant.settings.followPhoneZone
import persona.feature.assistant.settings.settingsApi
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import java.time.LocalTime
import java.util.UUID

/** One assistant's in-flight operation and retry target survive switching away and back. */
private class AssistantSendState {
    val sending = mutableStateOf(false)
    val clientId = mutableStateOf<String?>(null)
    val text = mutableStateOf<String?>(null)
    val launcher = mutableStateOf<String?>(null)
    val started = mutableStateOf<StarterStart?>(null)
}

/** Supply the shared chat content when available, or use the shell's ChatRoute entry. */
@Composable
fun AssistantScreen(
    navigator: Navigator,
    repository: AssistantRepository,
    messageSender: MessageSender,
    startConversation: suspend () -> SessionKey,
    pinnedConversations: Set<SessionKey> = emptySet(),
    connectionLabel: String? = null,
    hubOnline: Boolean = true,
    chatContent: (@Composable (SessionKey) -> Unit)? = null,
    demo: Boolean = false,
    machines: List<Machine> = LocalAssistantHost.current?.machines.orEmpty(),
    phoneToolsAvailable: Boolean? = LocalAssistantHost.current?.phoneToolsAvailable,
    management: AssistantManagement? = LocalAssistantHost.current?.management,
    assistantChatContent: (@Composable (SessionKey, AssistantChatOptions) -> Unit)? = LocalAssistantHost.current?.chatContent,
    requestedSession: SessionKey? = null,
    mainConversationSender: MainConversationSender? = LocalAssistantHost.current?.mainSender ?: messageSender as? MainConversationSender,
    onKnowledge: (() -> Unit)? = null,
    draftRepository: DraftRepository? = LocalAssistantHost.current?.drafts,
    draftScope: CoroutineScope? = LocalAssistantHost.current?.coroutineScope,
    composerService: ChatComposerService? = assistantComposerService(),
    /** The launcher's "Ask Persona": put the cursor in the composer and bring up the keyboard. */
    composerFocus: ComposerFocusRequests = AssistantComposerFocus,
    /** "Needs you now" over the home's suggestions; [onOpenNeed] opens a line where it lives (none: not shown). */
    needsYou: NeedsYou = NeedsYou(),
    onOpenNeed: ((NeedsYouLine) -> Unit)? = null,
    /** "N more in the inbox": the inbox tab when not given. */
    onOpenInbox: (() -> Unit)? = null,
    /** Pins or unpins a conversation (the hub's pins); none: the history offers no pinning. */
    onTogglePin: (suspend (SessionKey) -> Unit)? = null,
    /** "Voice chat" in the title menu, when the hub runs a voice extension. */
    onVoice: (() -> Unit)? = null,
    onTodos: (() -> Unit)? = null,
    todoCount: Int = 0,
    showTodos: Boolean = true,
    /** A new conversation on a launcher picked for it alone (the home composer's "Launcher" chip); null: no chip. */
    startConversationWith: (suspend (String) -> SessionKey)? =
        (repository as? persona.core.contracts.api.AssistantConversations)?.let { conversations -> { launcher -> conversations.startConversation(launcher) } },
) = PersonaTheme {
    val receivedSnapshot by repository.assistant.collectAsStateWithLifecycle()
    val conversationsOf = repository as? persona.core.contracts.api.AssistantConversations
    // Several managers (docs/assistant/managers.md step 4): the page shows one, the default to begin with.
    val reportedRoster = LocalManagerRoster.current
    val managerRoster = if (hubOnline) reportedRoster else reportedRoster?.copy(
        managers = reportedRoster.managers.map { it.copy(online = false) },
    )
    val managerChoice by persona.core.contracts.api.AssistantManagerChoice.current.collectAsStateWithLifecycle()
    // A late repository emission is never shown under another assistant's selected chip.
    val snapshot = receivedSnapshot.takeIf { it.managerId == managerChoice.shown }
        ?: persona.core.contracts.api.AssistantSnapshot(manager = managerChoice.manager, managerId = managerChoice.shown)
    val pageManagement = remember(management, managerChoice.hubId, managerChoice.shown) {
        management?.forManager(managerChoice.shown)
    }
    val managers = assistantManagers(managerRoster, managerChoice, androidx.compose.ui.platform.LocalResources.current)
    val outbox by (mainConversationSender?.outbox ?: messageSender.outbox).collectAsStateWithLifecycle()
    val mainMessages = if (managerChoice.manager == null) outbox.filter { (it.mainTarget || it.session == PendingMainConversation) && (it.session == PendingMainConversation || it.session == snapshot.mainSession) } else emptyList()
    val scope = rememberCoroutineScope()
    var drawerOpen by rememberSaveable { mutableStateOf(false) }
    var sidebarVisible by rememberSaveable { mutableStateOf(true) }
    val snackbar = remember { SnackbarHostState() }
    val feedback = persona.core.designsystem.LocalAppFeedback.current
    var query by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf("") }
    val transientComposer = key(managerChoice.hubId, managerChoice.shown) { rememberChatComposerState() }
    // Keep an in-flight editor alive when its assistant is hidden, so its successful send can clear only that draft.
    val draftEditors = remember(draftRepository, draftScope) { mutableMapOf<Pair<String?, String>, ChatDraftEditor>() }
    val draftEditor = if (draftRepository != null && draftScope != null) {
        draftEditors.getOrPut(managerChoice.hubId to managerChoice.shown) {
            ChatDraftEditor(draftRepository, draftScope, DraftKey.Assistant(managerChoice.shown.takeUnless { it == persona.core.contracts.api.ManagerRoster.ASSISTANT_MANAGER }?.let { "main@$it" } ?: "main"))
        }
    } else null
    DisposableEffect(draftEditors) { onDispose { draftEditors.values.forEach { it.close() } } }
    val savedComposer by key(draftEditor) {
        (draftEditor?.composer ?: remember { MutableStateFlow<ChatComposerState?>(null) }).collectAsStateWithLifecycle()
    }
    val draftError by key(draftEditor) {
        (draftEditor?.error ?: remember { MutableStateFlow<Int?>(null) }).collectAsStateWithLifecycle()
    }
    val composer = savedComposer ?: transientComposer
    val draftReady = draftEditor == null || savedComposer != null
    LaunchedEffect(draftEditor, savedComposer) {
        savedComposer?.let { state -> snapshotFlow { state.draft }.collect { draftEditor?.changed(it) } }
    }
    val sends = remember(draftRepository, draftScope) { mutableMapOf<Pair<String?, String>, AssistantSendState>() }
    val sendState = sends.getOrPut(managerChoice.hubId to managerChoice.shown) { AssistantSendState() }
    var sending by sendState.sending
    var activeMachine by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf<String?>(null) }
    var activeSession by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf<String?>(null) }
    var viewingMainQueue by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf(false) }
    var pendingClientId by sendState.clientId
    var pendingText by sendState.text
    var openedMain by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf(false) }
    var proactiveOpen by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf(false) }
    var settingsOpen by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf(false) }
    val context = androidx.compose.ui.platform.LocalContext.current
    var ending by remember { mutableStateOf<Session?>(null) }
    var endingBusy by remember { mutableStateOf(false) }
    var ended by remember(pageManagement) { mutableStateOf(emptySet<SessionKey>()) }
    var archived by remember(pageManagement) { mutableStateOf(emptySet<SessionKey>()) }
    var assistantMachine by remember(pageManagement) { mutableStateOf("") }
    // The launcher the next new conversation starts with, for it alone (iOS StarterComposer); null: the default.
    // Both belong to this hub: another one has other launchers and conversations.
    var starterLauncher by sendState.launcher
    // The conversation that launcher started for a message that then failed to send: its retry goes there.
    var starterStarted by sendState.started
    var launchers by remember(pageManagement, assistantMachine) { mutableStateOf<List<persona.core.contracts.api.AgentLauncher>?>(null) }
    var launchersFailed by remember(pageManagement, assistantMachine) { mutableStateOf(false) }
    var defaultLauncher by remember(pageManagement) { mutableStateOf<String?>(null) }
    val hasChat = chatContent != null || assistantChatContent != null
    var menu by remember { mutableStateOf(false) }
    var info by rememberSaveable { mutableStateOf<AssistantInfo?>(null) }
    var showAllRecent by rememberSaveable { mutableStateOf(false) }
    var autoOpen by rememberAutoOpen() // "Automatic chats", folded until opened (kept on this phone)
    var pinning by remember { mutableStateOf(false) }
    val clipboard = androidx.compose.ui.platform.LocalClipboardManager.current
    val resources = androidx.compose.ui.platform.LocalResources.current
    val locale = currentLocale
    val words = conversationWords()
    fun say(message: String) {
        if (feedback?.notice(message) != true) scope.launch { snackbar.showSnackbar(message) }
    }
    fun copyId(key: SessionKey) {
        clipboard.setText(androidx.compose.ui.text.AnnotatedString(key.sessionId))
        say(resources.getString(R.string.assistant_copied_session_id))
    }
    // Pins live on the hub (the Agents list shares them); one change at a time, a failure goes to the usual place.
    fun togglePins(keys: List<SessionKey>) {
        val toggle = onTogglePin ?: return
        if (pinning || keys.isEmpty()) return
        pinning = true
        scope.launch {
            try { keys.forEach { toggle(it) } }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                if (feedback?.failure(error) != true) snackbar.showSnackbar(persona.core.designsystem.operationFeedback(error).text.resolve(resources))
            }
            finally { pinning = false }
        }
    }
    val active = activeSession?.let { SessionKey(activeMachine.orEmpty(), it) }
    // "Compact context" and "New main chat" (#1727): the main conversation's menu, on hubs that offer them.
    val mainActions = if (pageManagement != null && !demo) MainActions.of(LocalHubFeatures.current) else MainActions(false, false)
    // What the main conversation still needs (docs/assistant/onboarding.md 4.4), on hubs that work it out: asked when the tab
    // shows, when a computer comes or goes and after the hub refused to start it.
    val readinessOn = pageManagement != null && !demo && AssistantReadinessFeature in LocalHubFeatures.current
    val mainReadiness = remember(pageManagement) { MainReadiness { probe -> requireNotNull(pageManagement).readiness(probe) } }
    var readinessHelp by remember { mutableStateOf<ReadinessHelp?>(null) }
    val machinesSeen = machines.joinToString(",") { "${it.id}:${it.online}" }
    LaunchedEffect(mainReadiness, readinessOn, machinesSeen) { if (readinessOn) mainReadiness.refresh() }
    androidx.lifecycle.compose.LifecycleEventEffect(androidx.lifecycle.Lifecycle.Event.ON_RESUME) {
        if (readinessOn) scope.launch { mainReadiness.refresh() }
    }
    val refusedMain = mainMessages.filter { it.setupRefusal() != null }.map { it.clientMessageId }
    LaunchedEffect(mainReadiness, readinessOn, refusedMain) { if (readinessOn && refusedMain.isNotEmpty()) mainReadiness.refresh() }
    val readiness = mainReadiness.readiness.takeIf { readinessOn }
    val blocking = mainReadiness.blocking.takeIf { readinessOn }
    fun runReadinessAction(action: ReadinessActionId, launcher: String? = blocking?.launcher ?: readiness?.launcher) {
        action.help?.let { readinessHelp = it; return }
        if (action == ReadinessActionId.TestAssistant) { scope.launch { mainReadiness.refresh(probe = true) }; return }
        val machine = readiness?.steps?.firstOrNull { it.id == ReadinessStep.COMPUTER }?.machine
        action.destination(launcher, machine)?.let(navigator::navigate)
    }
    val onMain = active != null && active == snapshot.mainSession
    val shownCompaction = if (onMain) snapshot.conversations.firstOrNull { it.key == active }?.compaction else null
    var compaction by remember(active) { mutableStateOf(CompactionStatus()) }
    var compacting by remember { mutableStateOf(false) }
    var newMainOpen by remember { mutableStateOf(false) }
    var newMainBusy by remember { mutableStateOf(false) }
    var carrySummary by rememberSaveable { mutableStateOf(true) }
    LaunchedEffect(active, shownCompaction) { compaction = compaction.observe(shownCompaction) }
    LaunchedEffect(compaction.line) {
        if (compaction.line == CompactionLine.Done) {
            kotlinx.coroutines.delay(CompactionDoneMillis)
            compaction = compaction.dismissed()
        }
    }
    val groups = remember(snapshot, query, pinnedConversations, ended, archived, words) {
        conversationGroups(snapshot.copy(conversations = snapshot.conversations.filter { it.key !in archived }.map {
            if (it.key in ended) it.copy(status = "offline", state = "offline") else it
        }), query, pinnedConversations, words)
    }
    // Persona's face (top bar and the conversation list) shows the conversation on screen, as on iOS: none on the
    // new-conversation page.
    val faceState = personaState(active?.let { key -> snapshot.conversations.firstOrNull { it.key == key } }, hubOnline,
        machines, assistantMachine)
    fun openConversation(key: SessionKey) {
        if (key == PendingMainConversation) {
            viewingMainQueue = true
            return
        }
        openedMain = true
        viewingMainQueue = false
        ended = ended - key
        activeMachine = key.machineId
        activeSession = key.sessionId
        if (!hasChat) {
            navigator.selectTab(AppTab.Persona)
            navigator.navigate(ChatRoute(key.machineId, key.sessionId))
        }
    }
    fun clearConversation() {
        openedMain = true
        viewingMainQueue = false
        activeMachine = null
        activeSession = null
        pendingClientId = null
        pendingText = null
    }
    fun newConversation() {
        if (sending) return
        clearConversation()
        scope.launch { drawerOpen = false }
    }
    // Another manager: its own conversations, so none of this one's stays open.
    fun chooseManager(id: String) {
        if (id == managerChoice.shown) return
        persona.core.contracts.api.AssistantManagerChoice.pick(id)
    }
    fun refusal(error: Exception): String =
        if (error is AssistantRefused) error.message.orEmpty() else persona.core.designsystem.operationFeedback(error).text.resolve(resources)
    fun compactMain() {
        val key = active ?: return
        val api = pageManagement ?: return
        if (compacting) return
        compacting = true
        compaction = compaction.asked(shownCompaction)
        scope.launch {
            try { api.compactConversation(key) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                compaction = compaction.refused(snapshot.conversations.firstOrNull { it.key == key }?.compaction)
                say(refusal(error))
            }
            finally { compacting = false }
        }
    }
    fun newMain() {
        val api = pageManagement ?: return
        val previous = snapshot.mainSession ?: return
        if (newMainBusy) return
        newMainBusy = true
        val carry = carrySummary
        scope.launch {
            try {
                val result = api.newMainConversation(carry, previous.sessionId)
                newMainOpen = false
                // The next message to the main conversation starts the new one, here and from every other entry.
                assistantAttempt { mainConversationSender?.mainReplaced(previous) }
                assistantAttempt { repository.refresh() }
                newConversation()
                say(resources.getString(newMainNotice(result.carried, carry)))
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                newMainOpen = false
                if ((error as? AssistantRefused)?.code == "main_changed") {
                    // Another device put it away first: show the main conversation the hub has now.
                    assistantAttempt { mainConversationSender?.mainReplaced(previous) }
                    assistantAttempt { repository.refresh() }
                }
                say(refusal(error))
            } finally {
                newMainBusy = false
            }
        }
    }
    // Hidden at once; after the hub's list is refreshed (it leaves archived ones out) the hub's word is the one kept,
    // so a later message that brings the conversation back shows it again.
    fun archive(session: Session) {
        val target = pageManagement ?: return
        archived = archived + session.key
        scope.launch {
            try {
                target.archiveConversation(session.key)
                // Clear only the archived selection; keep the list open for the next archive.
                if (activeMachine == session.machineId && activeSession == session.sessionId) clearConversation()
                assistantAttempt { repository.refresh() }.onSuccess { archived = archived - session.key }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                archived = archived - session.key
                if (feedback?.failure(error) != true) snackbar.showSnackbar(persona.core.designsystem.operationFeedback(error).text.resolve(resources))
            }
        }
    }
    fun startNewConversation() {
        if (sending) return
        if (snapshot.mainSession == null) {
            newConversation()
            return
        }
        sending = true
        scope.launch {
            try {
                // Explicit branches are separate from all home and external main-message entry points.
                openConversation(startConversation())
                drawerOpen = false
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                // A coded refusal (no computer, Claude Code missing…) says what is missing instead of "Can't start a chat right now".
                val coded = persona.core.designsystem.operationFeedback(error).takeIf { it.action.readinessAction() != null }
                scope.launch { snackbar.showSnackbar(coded?.text?.resolve(context) ?: resources.getString(R.string.assistant_new_conversation_failed)) }
                if (coded != null && readinessOn) mainReadiness.refresh()
            } finally {
                sending = false
            }
        }
    }
    fun submit() {
        // Not ready (setup): the step opens instead and the draft stays; nothing is queued (docs/assistant/onboarding.md 4.4).
        if (active == null && blocking != null) { blocking.primaryAction()?.let { runReadinessAction(it) }; return }
        // An offline manager takes nothing: its page is history only until it is back.
        if (managers.offline != null) return
        if (sending || !draftReady || !composer.hasContent || !composer.ready) return
        val sentDraft = composer.draft
        val text = composer.message()
        // A launcher picked on the new-conversation page: a conversation of its own on it, then the message to it.
        val launcher = starterLauncher.takeIf { active == null }
        val startWith = startConversationWith
        // Capture the concrete owner before any lookup; a later default change cannot retarget this message.
        val forManager = managerChoice.manager
        val submittedManager = managerChoice.shown
        val shownSnapshot = snapshot
        sending = true
        val submitted = text.trim()
        openedMain = true
        if (pendingClientId == null || pendingText != submitted) {
            pendingClientId = UUID.randomUUID().toString()
            pendingText = submitted
        }
        val operationId = requireNotNull(pendingClientId)
        val submittedChoice = managerChoice
        scope.launch {
            try {
                // Started once: a retry after the message failed sends to the same new conversation, while the
                // launcher is still the one that started it.
                val target = if (launcher != null && startWith != null)
                    starterTarget(launcher, starterStarted, startWith).also { starterStarted = it }.session
                    // Resolve this owner explicitly; legacy repositories retain the default main queue.
                    else active ?: if (conversationsOf != null) {
                        managerMainTarget(submittedManager, shownSnapshot, conversationsOf::mainConversationOf)
                    } else forManager?.let { error("managers unavailable") }
                sendAssistantMessage(mainConversationSender, messageSender, target, submitted, operationId)
                // Clear the submitted assistant's editor after commit, even while another assistant is shown.
                composer.clearIfUnchanged(sentDraft)
                draftEditor?.changed(composer.draft)
                pendingClientId = null
                pendingText = null
                if (persona.core.contracts.api.AssistantManagerChoice.current.value != submittedChoice) return@launch
                if (launcher != null && target != null) {
                    starterLauncher = null
                    starterStarted = null
                    assistantAttempt { repository.refresh() }
                    openConversation(target)
                } else if (target != null) {
                    openConversation(target)
                } else if (active == null) viewingMainQueue = true
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                if (feedback?.failure(error) != true) scope.launch {
                    snackbar.showSnackbar(persona.core.designsystem.operationFeedback(error).text.resolve(resources))
                }
            } finally {
                sending = false
            }
        }
    }
    LaunchedEffect(mainMessages, viewingMainQueue, managerChoice) {
        if (managerChoice.manager == null && (viewingMainQueue || (!openedMain && mainMessages.isNotEmpty() && requestedSession == null))) {
            openedMain = true
            viewingMainQueue = true
            mainMessages.firstOrNull { it.session != PendingMainConversation }?.session?.let { target ->
                openConversation(target)
                assistantAttempt { repository.refresh() }
            }
        }
    }
    LaunchedEffect(repository) {
        // Another device may have moved or replaced the main conversation while this one stayed connected.
        assistantAttempt { mainConversationSender?.mainChanged() }
        try { repository.refresh() }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* Cached conversations remain available while offline. */ }
    }
    LaunchedEffect(pageManagement) {
        if (pageManagement != null) {
            assistantAttempt { pageManagement.warm() }
            assistantAttempt { assistantMachine = pageManagement.notes().machineId }
            // Routines and the assistant's "today" follow the phone's time zone unless the user picked one.
            assistantAttempt { followPhoneZone(pageManagement.settingsApi(), StoredTimezoneChoice(context)) }
        }
    }
    // The chip's default and list: read when the page shows and again on each open (the default may have changed in
    // Settings; a list that could not be read is tried again), as the web reads them each time its menu opens.
    suspend fun loadStarterLaunchers() {
        val api = pageManagement ?: return
        if (startConversationWith == null || demo || assistantMachine.isBlank()) return
        val machine = assistantMachine
        assistantAttempt { api.config().launcher }.onSuccess { defaultLauncher = it.takeIf(String::isNotBlank) }
        if (launchers != null) return
        launchersFailed = false
        assistantAttempt { api.launchers(machine).filter(::runsAssistant) }
            .onSuccess { if (machine == assistantMachine) launchers = it }
            .onFailure { if (machine == assistantMachine) launchersFailed = true }
    }
    LaunchedEffect(pageManagement, assistantMachine, startConversationWith) { loadStarterLaunchers() }
    LaunchedEffect(requestedSession) {
        requestedSession?.let { openedMain = true; proactiveOpen = false; settingsOpen = false; openConversation(it) }
    }
    LaunchedEffect(snapshot.mainSession, snapshot.conversations, managerChoice) {
        if (!demo && !openedMain && requestedSession == null && mainMessages.isEmpty()) {
            val initial = snapshot.mainSession
            initial?.let(::openConversation)
        }
    }
    val keyboard = androidx.compose.ui.platform.LocalSoftwareKeyboardController.current
    val focusTarget by rememberUpdatedState(composer)
    LaunchedEffect(composerFocus) {
        composerFocus.requests.collect { at ->
            if (at == null || !composerFocus.take(at)) return@collect
            // The home's composer (an embedded conversation keeps its composer inside the shared chat page); what is
            // sent there goes to the main conversation, as from any home entry.
            proactiveOpen = false; settingsOpen = false; drawerOpen = false
            if (activeSession != null && hasChat && !demo) newConversation()
            // After the tab's first layout and the saved draft: the text field has to be attached (the old app waited 300 ms).
            kotlinx.coroutines.withTimeoutOrNull(3_000) { snapshotFlow { draftEditor == null || savedComposer != null }.first { it } }
            kotlinx.coroutines.delay(300)
            focusTarget.focusRequest++
            kotlinx.coroutines.delay(100)
            keyboard?.show()
        }
    }
    BackHandler(drawerOpen || active != null || viewingMainQueue) {
        if (drawerOpen) scope.launch { drawerOpen = false } else newConversation()
    }
    val sidebar: @Composable () -> Unit = {
        AssistantDrawer(query, { query = it }, groups, active, snapshot.mainSession != null,
            onEnd = if (pageManagement != null) ({ ending = it }) else null,
            onMain = {
                openedMain = true
                snapshot.mainSession?.let(::openConversation) ?: run { newConversation(); viewingMainQueue = true }
                scope.launch { drawerOpen = false }
            },
            onOpen = { openConversation(it); scope.launch { drawerOpen = false } },
            onInfo = {
                if (it == AssistantInfo.Knowledge && onKnowledge != null) onKnowledge()
                else if (pageManagement != null && it == AssistantInfo.Launcher) settingsOpen = true
                else if (pageManagement != null && it == AssistantInfo.Schedule) proactiveOpen = true
                else info = it
                scope.launch { drawerOpen = false }
            },
            onNew = ::startNewConversation,
            onHome = { newConversation() },
            onSettings = { scope.launch { drawerOpen = false }; navigator.selectTab(AppTab.Settings) },
            onPin = if (onTogglePin != null) ({ session -> togglePins(listOf(session.key)) }) else null,
            // The hub's pins also hold the Agents list's sessions: only Persona's own conversations are unpinned here.
            onUnpinAll = if (onTogglePin != null) ({
                togglePins(snapshot.conversations.map { it.key }.distinct().filter { it in pinnedConversations && it != snapshot.mainSession })
            }) else null,
            onCopy = { copyId(it.key) },
            onArchive = if (pageManagement != null && !demo) ::archive else null,
            showAllRecent = showAllRecent,
            onShowAllRecent = { showAllRecent = true },
            autoOpen = autoOpen, onAutoOpen = { autoOpen = it },
            onTodos = onTodos?.let { action -> { scope.launch { drawerOpen = false }; action() } },
            todoCount = todoCount,
            showTodos = showTodos,
        )
    }
    val body: @Composable (Boolean) -> Unit = { wide ->
        val hasComposer = active == null || !hasChat || demo
        JPage(
            title = LocalPersonaName.current,
            modifier = Modifier.fillMaxSize().imePadding(),
            topBar = {
                CompositionLocalProvider(LocalRootLeading provides {
                    JPersonaButton(faceState, stringResource(R.string.assistant_conversation_list), {
                        if (wide) sidebarVisible = !sidebarVisible else drawerOpen = true
                    })
                }) {
                    Column {
                        JTopBar(
                            title = LocalPersonaName.current, subtitle = connectionLabel,
                            titleBusy = !connectionLabel.isNullOrBlank(), onTitleClick = { menu = true },
                            actions = {
                                JIconButton(PhIcons.NotePencil, stringResource(R.string.assistant_new_conversation), ::startNewConversation,
                                    enabled = !sending && managers.offline == null)
                                JIconButton(PhIcons.UserCircle, stringResource(R.string.assistant_profile), {
                                    if (pageManagement != null) settingsOpen = true else info = AssistantInfo.Settings
                                })
                            },
                        )
                        if (!demo) JManagerSwitcher(managerRoster, managerChoice.shown, ::chooseManager)
                    }
                }
            },
            bottomBar = if (hasComposer || snackbar.currentSnackbarData != null) ({
                Column {
                    SnackbarHost(snackbar) { data ->
                        JNotice(data.visuals.message, Modifier.padding(horizontal = pagePadding),
                            actionLabel = stringResource(R.string.assistant_got_it), onAction = { data.dismiss() })
                    }
                    if (hasComposer && active == null && !viewingMainQueue) {
                        val cardModifier = Modifier.padding(horizontal = pagePadding).padding(bottom = Tokens.Space.s)
                        if (blocking != null) ReadinessStepCard(blocking, { runReadinessAction(it) }, cardModifier,
                            busy = ReadinessActionId.TestAssistant.takeIf { mainReadiness.probing },
                            note = readiness?.probe?.let { probe ->
                                if (probe.ok) stringResource(persona.core.designsystem.R.string.designsystem_readiness_probe_ok, probe.answer.orEmpty())
                                else stringResource(persona.core.designsystem.R.string.designsystem_readiness_probe_failed, probe.error.orEmpty())
                            })
                        else if (mainReadiness.chatOnly && readinessOn) JNotice(
                            stringResource(persona.core.designsystem.R.string.designsystem_readiness_chat_only), cardModifier,
                            actionLabel = stringResource(ReadinessActionId.AddComputer.label),
                            onAction = { runReadinessAction(ReadinessActionId.AddComputer) })
                    }
                    if (hasComposer) {
                        draftError?.let { JNotice(androidx.compose.ui.res.stringResource(it), Modifier.padding(horizontal = pagePadding), status = Status.NeedsYou) }
                        val uploadTarget = active ?: snapshot.mainSession
                            ?: SessionKey(assistantMachine, PendingMainConversation.sessionId)
                        ChatComposer(uploadTarget, composer, working = false, sending = sending || !draftReady,
                            canClear = false, service = composerService?.takeIf { uploadTarget.machineId.isNotBlank() },
                            onSend = ::submit, onCommand = { composer.insert(it, composer.focused) },
                            onNotice = { message ->
                                if (feedback?.notice(message) != true) scope.launch { snackbar.showSnackbar(message) }
                            }, enabled = draftReady && !sending && managers.offline == null,
                            placeholder = managers.offline ?: stringResource(R.string.assistant_composer_placeholder, LocalPersonaName.current),
                            blockedLabel = blocking?.takeIf { active == null }?.let { stringResource(blockedSendLabel(it)) },
                            onBlocked = { blocking?.primaryAction()?.let { runReadinessAction(it) } },
                            // A new conversation's launcher, for it alone (iOS StarterComposer, web StarterLauncherChip).
                            accessory = if (active == null && pageManagement != null && startConversationWith != null && !demo) ({
                                StarterLauncherChip(starterLauncher, defaultLauncher, launchers, launchersFailed, enabled = !sending,
                                    onOpen = { scope.launch { loadStarterLaunchers() } }) { picked ->
                                    // Another launcher (or the default) does not reuse what the last one started.
                                    if (picked != starterLauncher) starterStarted = null
                                    starterLauncher = picked
                                }
                            }) else null)
                    }
                }
            }) else null,
            // An embedded conversation is a page of its own and softens the edges under these bars itself.
            edges = !(active != null && hasChat),
        ) { padding ->
            if (active != null && hasChat) {
                // The conversation fills the page and scrolls under the bar above it and the dock below, like any
                // page, instead of being cut off at a line under the bar; the chrome insets give it their room.
                // (The demo notice sits under the bar, so there the conversation starts below it.)
                val host = ChromeInsets(bottom = padding.calculateBottomPadding(), handlesNavigationBar = true)
                Column(Modifier.fillMaxSize().then(if (demo) Modifier.padding(top = padding.calculateTopPadding()) else Modifier)) {
                    if (demo) AssistantDemoNotice()
                    Box(Modifier.weight(1f)) {
                        CompositionLocalProvider(LocalChromeInsets provides if (demo) host else host.copy(top = padding.calculateTopPadding())) {
                            if (assistantChatContent != null) assistantChatContent(active, AssistantChatOptions(readOnlyNote = managers.offline))
                            else chatContent?.invoke(active)
                        }
                        // A compaction of the main conversation: under the bar, over the conversation.
                        if (onMain) compaction.line?.let { line ->
                            CompactionStatusLine(line, onDismiss = { compaction = compaction.dismissed() },
                                modifier = Modifier.align(Alignment.TopCenter)
                                    .then(if (demo) Modifier else Modifier.padding(top = padding.calculateTopPadding())))
                        }
                    }
                }
            } else if (viewingMainQueue || (active != null && !hasChat)) {
                val visible = if (active != null) outbox.filter { it.session == active } else mainMessages
                AssistantPendingConversation(visible, snapshot.conversations.firstOrNull { it.key == active },
                    Modifier.fillMaxSize(), contentPadding = padding, demo = demo, emptyDetail = managers.offlineEmpty,
                    onRefusal = { action -> runReadinessAction(action) },
                    onRetry = { message -> scope.launch {
                        // Asked again first: a step still missing opens instead of sending into the same refusal.
                        if (readinessOn) mainReadiness.refresh()
                        val missing = mainReadiness.blocking.takeIf { readinessOn }
                        if (missing != null) missing.primaryAction()?.let { runReadinessAction(it) }
                        else assistantAttempt { (mainConversationSender as? MessageSender ?: messageSender).retry(message.clientMessageId) }
                    } })
            } else {
                AssistantHome(enabled = !sending && draftReady && composer.ready, machines = machines,
                    phoneToolsAvailable = phoneToolsAvailable, contentPadding = padding, demo = demo,
                    setup = blocking != null, hubSuggestions = readiness?.suggestions?.takeIf { it.isNotEmpty() },
                    onLocalSuggestion = { readinessHelp = ReadinessHelp.ofSuggestion(it) },
                    onSuggestion = {
                        composer.value = androidx.compose.ui.text.input.TextFieldValue(it, androidx.compose.ui.text.TextRange(it.length))
                        submit()
                    }, needsYou = needsYou, onNeed = onOpenNeed,
                    onMore = onOpenInbox ?: { navigator.selectTab(AppTab.Inbox) })
            }
        }
    }
    // A run or a follow-up opened from "Proactive" (here or inside the settings page) shows its conversation in place.
    val openFromSchedule: (SessionKey) -> Unit = {
        proactiveOpen = false; settingsOpen = false; openedMain = true; openConversation(it)
        scope.launch { assistantAttempt { repository.refresh() } }
    }
    // A task's model is set on Settings › Models (iOS ModelConfigurationLink); back from it returns here.
    val openModelConfiguration: (String, String) -> Unit = { purpose, owner -> navigator.navigate(ModelPurposeRoute(purpose, owner)) }
    if (settingsOpen && pageManagement != null) {
        AssistantSettingsScreen(pageManagement, machines, initialManager = managerChoice.shown, onBack = { settingsOpen = false }, inPlace = true, onMoved = {
            // The conversations and the upload target now belong to the other machine.
            assistantAttempt { repository.refresh() }
            assistantAttempt { assistantMachine = pageManagement.notes().machineId }
            // The old main conversation is an ordinary one on the old machine now: back here, start the new one.
            newConversation()
        }, onOpenConversation = openFromSchedule, onModelConfiguration = openModelConfiguration)
        return@PersonaTheme
    }
    if (proactiveOpen && pageManagement != null) {
        // Shown in place of the conversation: back on its first page closes it here (its own pages come first).
        BackHandler { proactiveOpen = false }
        AssistantProactiveScreen(onBack = { proactiveOpen = false }, onOpenConversation = openFromSchedule,
            onModelConfiguration = { purpose -> openModelConfiguration(purpose, managerChoice.shown) }, management = pageManagement, machines = machines)
        return@PersonaTheme
    }
    BoxWithConstraints(Modifier.fillMaxSize()) {
        if (maxWidth >= 840.dp) {
            Row(Modifier.fillMaxSize()) {
                // The list of conversations sits on the same background as the page beside it, not a panel of its own.
                if (sidebarVisible) Box(Modifier.width(300.dp).fillMaxHeight()) {
                    PageBackdrop()
                    Box(Modifier.fillMaxSize().statusBarsPadding().navigationBarsPadding()) { sidebar() }
                }
                Box(Modifier.weight(1f)) { body(true) }
            }
        } else {
            body(false)
            if (drawerOpen) JSheet(onDismiss = { drawerOpen = false }, title = stringResource(R.string.assistant_conversation_list)) {
                Box(Modifier.fillMaxWidth().fillMaxHeight(0.9f)) { sidebar() }
            }
        }
    }
    if (menu) JActionSheet(
        title = LocalPersonaName.current, onDismiss = { menu = false },
        actions = buildList {
            listOf(AssistantInfo.Settings, AssistantInfo.Knowledge).forEach { topic ->
                add(JAction(resources.getString(topic.title), onClick = {
                    if (topic == AssistantInfo.Knowledge && onKnowledge != null) onKnowledge()
                    else if (topic == AssistantInfo.Settings && pageManagement != null) settingsOpen = true
                    else info = topic
                }))
            }
            onVoice?.let { open -> add(JAction(resources.getString(R.string.assistant_voice_chat), onClick = open)) }
            add(JAction(resources.getString(R.string.assistant_info_session), onClick = { info = AssistantInfo.Session }))
            if (onMain && mainActions.compact) add(JAction(resources.getString(R.string.assistant_compact), separatorBefore = true,
                enabled = !compacting && compaction.line != CompactionLine.Running, onClick = ::compactMain))
            if (onMain && mainActions.newMain) add(JAction(resources.getString(R.string.assistant_new_main), separatorBefore = !mainActions.compact,
                enabled = !newMainBusy, onClick = { newMainOpen = true }))
            active?.let { key -> add(JAction(resources.getString(R.string.assistant_copy_session_id), separatorBefore = true, onClick = { copyId(key) })) }
        },
    )
    if (newMainOpen) {
        JDialog(onDismiss = { if (!newMainBusy) newMainOpen = false }, title = stringResource(R.string.assistant_new_main_title),
            text = newMainText(resources, java.time.LocalDate.now(), locale), confirmEnabled = !newMainBusy,
            confirm = stringResource(R.string.assistant_create) to ::newMain,
            dismiss = stringResource(R.string.assistant_cancel) to { if (!newMainBusy) newMainOpen = false }) {
            JSwitchRow(stringResource(R.string.assistant_carry_summary), carrySummary, { carrySummary = it },
                subtitle = stringResource(R.string.assistant_carry_summary_hint),
                enabled = !newMainBusy)
        }
    }
    ending?.let { session ->
        JDialog(onDismiss = { if (!endingBusy) ending = null }, title = stringResource(R.string.assistant_end_title),
            text = stringResource(R.string.assistant_end_text, session.conversationTitle(words)),
            destructive = true, confirmEnabled = !endingBusy,
            confirm = stringResource(R.string.assistant_end) to {
                endingBusy = true
                scope.launch {
                    try {
                        requireNotNull(pageManagement).endConversation(session.key)
                        ended = ended + session.key
                        ending = null
                        if (active == session.key) newConversation()
                        assistantAttempt { repository.refresh() }
                    } catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { snackbar.showSnackbar(resources.getString(R.string.assistant_end_failed)) }
                    finally { endingBusy = false }
                }
            }, dismiss = stringResource(R.string.assistant_cancel) to { if (!endingBusy) ending = null })
    }
    readinessHelp?.let { help ->
        ReadinessHelpDialog(help, readiness?.machine?.name, onDismiss = { readinessHelp = null }, onAction = { runReadinessAction(it) })
    }
    // An open conversation's "Session info" also lists what it was always allowed (#890); the rest explain in a dialog.
    val infoSession = active?.takeIf { info == AssistantInfo.Session && !demo }
    if (infoSession != null && pageManagement != null) AssistantSessionInfoSheet(infoSession, pageManagement, onDismiss = { info = null })
    else info?.let { topic ->
        JDialog(onDismiss = { info = null }, title = stringResource(topic.title),
            text = when (topic) {
                AssistantInfo.Schedule -> stringResource(R.string.assistant_info_schedule_text)
                AssistantInfo.Knowledge -> stringResource(R.string.assistant_info_knowledge_text)
                AssistantInfo.Settings -> stringResource(R.string.assistant_info_settings_text)
                AssistantInfo.Session -> if (active == null) stringResource(R.string.assistant_info_session_none)
                    else stringResource(R.string.assistant_info_session_text, active.sessionId)
                AssistantInfo.Launcher -> stringResource(R.string.assistant_info_launcher_text)
            }, confirm = stringResource(R.string.assistant_got_it) to { info = null }, dismiss = null)
    }
}

/** What the drawer's "More" rows and the title menu explain when there is nothing to open (the demo, a missing hub). */
internal enum class AssistantInfo(@androidx.annotation.StringRes val title: Int) {
    Schedule(R.string.assistant_info_schedule),
    Knowledge(R.string.assistant_info_knowledge),
    Settings(R.string.assistant_info_settings),
    Session(R.string.assistant_info_session),
    Launcher(R.string.assistant_info_launcher),
}

/** A suggestion card; [prompt] is sent as the user's own message, so it is in the user's language too. */
private data class Suggestion(val icon: PhIcon, @androidx.annotation.StringRes val title: Int,
    @androidx.annotation.StringRes val detail: Int, @androidx.annotation.StringRes val prompt: Int)
private val suggestions = listOf(
    Suggestion(PhIcons.Tray, R.string.assistant_suggest_inbox, R.string.assistant_suggest_inbox_detail, R.string.assistant_suggest_inbox_prompt),
    Suggestion(PhIcons.Terminal, R.string.assistant_suggest_agents, R.string.assistant_suggest_agents_detail, R.string.assistant_suggest_agents_prompt),
    Suggestion(PhIcons.CalendarBlank, R.string.assistant_suggest_calendar, R.string.assistant_suggest_calendar_detail, R.string.assistant_suggest_calendar_prompt),
    Suggestion(PhIcons.Clock, R.string.assistant_suggest_remind, R.string.assistant_suggest_remind_detail, R.string.assistant_suggest_remind_prompt),
    Suggestion(PhIcons.BookOpen, R.string.assistant_suggest_knowledge, R.string.assistant_suggest_knowledge_detail, R.string.assistant_suggest_knowledge_prompt),
    Suggestion(PhIcons.FileText, R.string.assistant_suggest_doc, R.string.assistant_suggest_doc_detail, R.string.assistant_suggest_doc_prompt),
)

@Composable
@OptIn(ExperimentalLayoutApi::class)
private fun AssistantHome(
    enabled: Boolean, machines: List<Machine>, phoneToolsAvailable: Boolean?, contentPadding: PaddingValues,
    demo: Boolean, onSuggestion: (String) -> Unit,
    needsYou: NeedsYou = NeedsYou(), onNeed: ((NeedsYouLine) -> Unit)? = null, onMore: () -> Unit = {},
    /** The main conversation still misses a step (docs/assistant/onboarding.md 4.4): the lead says so. */
    setup: Boolean = false,
    /** The hub's example questions (readiness `suggestions`), in place of the app's own; null: the app's. */
    hubSuggestions: List<ReadinessSuggestion>? = null,
    onLocalSuggestion: (String) -> Unit = {},
) {
    LazyVerticalGrid(
        columns = GridCells.Fixed(2), modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(start = pagePadding, end = pagePadding,
            top = contentPadding.calculateTopPadding() + 16.dp, bottom = contentPadding.calculateBottomPadding() + 24.dp),
        horizontalArrangement = Arrangement.spacedBy(12.dp), verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        if (demo) item(span = { GridItemSpan(2) }) { AssistantDemoNotice() }
        item(span = { GridItemSpan(2) }) {
            Column(Modifier.padding(vertical = 20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text(stringResource(greeting(LocalTime.now().hour)), style = PersonaTheme.type.largeTitle, color = PersonaTheme.palette.ink)
                Text(stringResource(if (setup) R.string.assistant_home_setup_lead else R.string.assistant_home_lead),
                    style = PersonaTheme.type.voice, color = PersonaTheme.palette.ink2)
                if (!setup) Text(stringResource(R.string.assistant_home_note), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
            }
        }
        // What waits on the user comes before what Persona could do: the suggestions stay under it.
        if (onNeed != null && !needsYou.isEmpty) item(key = "needs-you", span = { GridItemSpan(2) }) {
            NeedsYouSection(needsYou, onNeed, onMore)
        }
        if (hubSuggestions != null) items(hubSuggestions, key = { "hub-${it.id}" }) { suggestion ->
            HubSuggestionCard(suggestion, enabled, onOpen = {
                val prompt = suggestion.prompt?.takeIf(String::isNotBlank)
                if (suggestion.local || prompt == null) onLocalSuggestion(suggestion.id) else onSuggestion(prompt)
            })
        } else items(suggestions) { suggestion ->
            val prompt = stringResource(suggestion.prompt)
            SuggestionCard(suggestion.icon, stringResource(suggestion.title), stringResource(suggestion.detail),
                onClick = if (enabled) ({ onSuggestion(prompt) }) else null)
        }
        item(span = { GridItemSpan(2) }) {
            Column {
                JSectionHeader(stringResource(R.string.assistant_online_devices))
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    machines.filter { it.online }.distinctBy { it.id }.forEach { machine ->
                        JChip(machine.displayName, icon = PhIcons.Desktop)
                    }
                    JChip(when (phoneToolsAvailable) {
                        true -> stringResource(R.string.assistant_this_phone)
                        false -> stringResource(R.string.assistant_phone_tools_unavailable)
                        null -> stringResource(R.string.assistant_phone_permissions_pending)
                    }, icon = PhIcons.DeviceMobile)
                }
            }
        }
    }
}

/** One of the hub's example questions: a local one opens help here, never sent to the model. */
@Composable
private fun HubSuggestionCard(suggestion: ReadinessSuggestion, enabled: Boolean, onOpen: () -> Unit) {
    val detail = when {
        suggestion.local -> stringResource(R.string.assistant_suggest_local_detail)
        "phone_calendar" in suggestion.needs -> stringResource(R.string.assistant_suggest_needs_calendar)
        else -> null
    }
    SuggestionCard(if (suggestion.local) PhIcons.Question else PhIcons.Sparkle, suggestion.title, detail,
        onClick = if (enabled || suggestion.local) onOpen else null)
}

/** A suggestion card on the home: an icon, a one-line title and a line under it. */
@Composable
private fun SuggestionCard(icon: PhIcon, title: String, detail: String?, onClick: (() -> Unit)?) {
    JCard(Modifier.fillMaxWidth().heightIn(min = 144.dp), padding = 16.dp, onClick = onClick) {
        JIcon(icon, null, tint = PersonaTheme.palette.accent)
        Spacer(Modifier.height(12.dp))
        Text(title, maxLines = 1, overflow = TextOverflow.Ellipsis,
            style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
        Spacer(Modifier.height(4.dp))
        Text(detail.orEmpty(), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

@Composable
internal fun AssistantDrawer(
    query: String, onQuery: (String) -> Unit, groups: ConversationGroups, active: SessionKey?, hasMain: Boolean,
    onEnd: ((Session) -> Unit)?,
    onMain: () -> Unit, onOpen: (SessionKey) -> Unit, onInfo: (AssistantInfo) -> Unit,
    onNew: () -> Unit, onHome: () -> Unit, onSettings: () -> Unit,
    onPin: ((Session) -> Unit)? = null, onUnpinAll: (() -> Unit)? = null, onCopy: (Session) -> Unit = {},
    onArchive: ((Session) -> Unit)? = null,
    showAllRecent: Boolean = false, onShowAllRecent: () -> Unit = {},
    autoOpen: Boolean = false, onAutoOpen: (Boolean) -> Unit = {},
    onTodos: (() -> Unit)? = null, todoCount: Int = 0, showTodos: Boolean = true,
) {
    // The origin "Automatic chats" lists when open (the first one there is, until another is picked).
    var autoPicked by rememberSaveable { mutableStateOf<String?>(null) }
    Column(Modifier.fillMaxHeight().padding(horizontal = 12.dp)) {
        LazyColumn(Modifier.weight(1f), contentPadding = PaddingValues(bottom = 12.dp)) {
            // The first row is the main conversation, as "Persona" with the app's mark, then a line (#2004, the same on
            // every platform): it is listed nowhere else, has no pin of its own, and is on show whatever the search.
            // Its session keeps its id and title; only the row reads differently.
            item(key = "persona") {
                PersonaRow(groups.main, selected = groups.main != null && groups.main.key == active, hasMain = hasMain,
                    onMain = onMain, onEnd = groups.main?.let { m -> onEnd?.let { action -> { action(m) } } },
                    onCopy = { groups.main?.let(onCopy) })
                JDivider(Modifier.padding(vertical = Tokens.Space.s))
            }
            item { JSearchField(query, onQuery, stringResource(R.string.assistant_search_chats)) }
            if (groups.pinned.isNotEmpty()) item(key = "pinned-header") {
                var pinMenu by remember { mutableStateOf(false) }
                JSectionHeader(stringResource(R.string.assistant_pinned), count = groups.pinned.size, action = if (onUnpinAll != null) ({
                    JIconButton(PhIcons.DotsThree, stringResource(R.string.assistant_pinned_actions), { pinMenu = true })
                }) else null)
                if (pinMenu && onUnpinAll != null) JActionSheet(
                    listOf(JAction(stringResource(R.string.assistant_unpin_all), PhIcons.PushPin, onClick = onUnpinAll)), { pinMenu = false },
                    title = stringResource(R.string.assistant_pinned),
                )
            }
            itemsIndexed(groups.pinned, key = { _, session -> session.key.path }) { index, session ->
                ConversationRow(session, selected = session.key == active,
                    position = GroupPosition.of(index, groups.pinned.size),
                    onOpen = { onOpen(session.key) }, onEnd = onEnd?.let { action -> { action(session) } },
                    pinned = true, onPin = onPin?.let { pin -> { pin(session) } }, onCopy = { onCopy(session) },
                    onArchive = onArchive?.let { action -> { action(session) } })
            }
            // The most recent 30, then one row for the rest (a search shows everything it finds).
            val recent = recentRows(groups.recent, query, showAllRecent)
            val recentCount = recent.shown.size + if (recent.hidden > 0) 1 else 0
            if (groups.recent.isNotEmpty()) item(key = "recent-header") { JSectionHeader(stringResource(R.string.assistant_recent), count = groups.recent.size) }
            itemsIndexed(recent.shown, key = { _, session -> session.key.path }) { index, session ->
                ConversationRow(session, selected = session.key == active,
                    position = GroupPosition.of(index, recentCount),
                    onOpen = { onOpen(session.key) }, onEnd = onEnd?.let { action -> { action(session) } },
                    pinned = false, onPin = onPin?.let { pin -> { pin(session) } }, onCopy = { onCopy(session) },
                    onArchive = onArchive?.let { action -> { action(session) } })
            }
            if (recent.hidden > 0) item(key = "recent-all") {
                JGroupItem(GroupPosition.of(recentCount - 1, recentCount)) {
                    DrawerItem(pluralStringResource(R.plurals.assistant_show_all_recent, groups.recent.size, groups.recent.size), PhIcons.ChatsCircle, onShowAllRecent)
                }
            }
            // "Automatic chats" (#3264): what the hub opened by itself, one row folded by default; open, a chip per origin and that
            // origin's conversations. Folded, the row still shows what waits for the user and what runs.
            if (groups.auto.isNotEmpty()) {
                val origins = autoGroups(groups.auto)
                val picked = origins.firstOrNull { it.origin?.wire == autoPicked } ?: origins.first()
                val shown = groups.auto.filter { it.autoOrigin() == picked.origin }
                val autoCount = if (autoOpen) 2 + shown.size else 1
                item(key = "auto-header") {
                    JGroupItem(GroupPosition.of(0, autoCount), Modifier.padding(top = Tokens.Space.m)) {
                        AutoConversationsRow(tally(null, groups.auto), autoOpen) { onAutoOpen(!autoOpen) }
                    }
                }
                if (autoOpen) {
                    item(key = "auto-origins") {
                        JGroupItem(GroupPosition.of(1, autoCount)) { AutoOriginChips(origins, picked.origin) { autoPicked = it.wire } }
                    }
                    itemsIndexed(shown, key = { _, session -> "auto/" + session.key.path }) { index, session ->
                        ConversationRow(session, selected = session.key == active,
                            position = GroupPosition.of(index + 2, autoCount),
                            onOpen = { onOpen(session.key) }, onEnd = onEnd?.let { action -> { action(session) } },
                            pinned = false, onPin = onPin?.let { pin -> { pin(session) } }, onCopy = { onCopy(session) },
                            onArchive = onArchive?.let { action -> { action(session) } })
                    }
                }
            }
            if (groups.pinned.isEmpty() && groups.recent.isEmpty() && groups.auto.isEmpty() && (groups.main == null || query.isNotBlank())) {
                item { JEmptyState(stringResource(if (query.isBlank()) R.string.assistant_empty_title else R.string.assistant_no_match),
                    stringResource(if (query.isBlank()) R.string.assistant_empty_detail else R.string.assistant_no_match_detail)) }
            }
            item { JSectionHeader(stringResource(R.string.assistant_more)) }
            // Assistant details remain available from the Persona tab's toolbar, not as a drawer row.
            val totalMore = if (showTodos && onTodos != null) 6 else 5
            // Each row's place in the group is taken while the list is built: counted inside an item's own content, it
            // was counted again whenever only some rows recomposed, and the group's corners landed on the wrong rows.
            var moreIndex = 0
            fun more(content: @Composable () -> Unit) {
                val position = GroupPosition.of(moreIndex++, totalMore)
                item { JGroupItem(position) { content() } }
            }
            more { DrawerItem(stringResource(R.string.assistant_home), PhIcons.House, onHome) }
            if (showTodos && onTodos != null) more { DrawerItem(stringResource(R.string.assistant_todos), PhIcons.ListChecks, badge = todoCount, onClick = onTodos) }
            more { DrawerItem(stringResource(AssistantInfo.Schedule.title), PhIcons.Clock) { onInfo(AssistantInfo.Schedule) } }
            more { DrawerItem(stringResource(AssistantInfo.Knowledge.title), PhIcons.BookOpen) { onInfo(AssistantInfo.Knowledge) } }
            more { DrawerItem(stringResource(AssistantInfo.Session.title), PhIcons.Info) { onInfo(AssistantInfo.Session) } }
            more { DrawerItem(stringResource(AssistantInfo.Launcher.title), PhIcons.RocketLaunch) { onInfo(AssistantInfo.Launcher) } }
        }
        Row(Modifier.padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            JButton(stringResource(R.string.assistant_new_conversation), onNew, Modifier.weight(1f), icon = PhIcons.NotePencil)
            JIconButton(PhIcons.Gear, stringResource(R.string.assistant_open_settings), onSettings)
        }
    }
}

/**
 * One conversation of the history. Its menu (⋯, or a long press) pins or unpins it ([onPin]; the main conversation
 * has none), copies its session id, archives it ([onArchive]; not the main one either), and ends it while it runs ([onEnd]).
 */
@Composable
private fun ConversationRow(
    session: Session, selected: Boolean, onOpen: () -> Unit, onEnd: (() -> Unit)?,
    modifier: Modifier = Modifier, position: GroupPosition = GroupPosition.Single,
    pinned: Boolean = false, onPin: (() -> Unit)? = null, onCopy: () -> Unit = {}, onArchive: (() -> Unit)? = null,
) {
    var menu by remember { mutableStateOf(false) }
    // The channel its latest message came in from (WeChat, the Feishu bot…), as a small mark after the title.
    val channels = LocalChannels.current
    val channel = remember(session.channels, channels) { ChannelSources.latest(session.channels, channels) }
    val title = session.conversationTitle(conversationWords())
    JGroupItem(position, modifier) {
        // The time goes on the line under the title, with the state: beside the title it left the title two or three
        // characters in the wide window's 300dp list (2026-11-02), and a narrow drawer cut it short too.
        JRow(title, onClick = onOpen, onLongClick = { menu = true },
            titleAccessory = channel?.let { c -> { JChannelMark(c, size = MarkSize.Inline) } },
            subtitle = session.lastText?.takeIf { it.isNotBlank() },
            leading = { JIcon(if (selected) PhIcons.CheckCircle else PhIcons.ChatCircle, null,
                selected = selected, tint = if (selected) PersonaTheme.palette.accent else PersonaTheme.palette.ink2) },
            trailing = { JIconButton(PhIcons.DotsThree, stringResource(R.string.assistant_conversation_actions), { menu = true }) }, chevron = false)
        Row(Modifier.padding(start = 16.dp, end = 16.dp, bottom = 10.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
            StatusLabel(session.conversationStatus())
            conversationTime(session.updated, currentLocale)?.let { time ->
                Text(time, Modifier.weight(1f), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3,
                    maxLines = 1, overflow = TextOverflow.Ellipsis, textAlign = TextAlign.End)
            }
        }
    }
    if (menu) JActionSheet(
        title = title, onDismiss = { menu = false },
        actions = conversationActions(androidx.compose.ui.platform.LocalResources.current, pinned, session.isLive, onPin, onCopy, onEnd, onArchive),
    )
}

/** The history row's menu: "Pin" / "Unpin" (when it can be pinned), "Copy session ID", "Archive" (when it can be), then "End chat" while it runs. */
internal fun conversationActions(
    resources: android.content.res.Resources, pinned: Boolean, live: Boolean, onPin: (() -> Unit)?, onCopy: () -> Unit,
    onEnd: (() -> Unit)?, onArchive: (() -> Unit)? = null,
): List<JAction> =
    buildList {
        onPin?.let { add(JAction(resources.getString(if (pinned) R.string.assistant_unpin else R.string.assistant_pin), PhIcons.PushPin, onClick = it)) }
        add(JAction(resources.getString(R.string.assistant_copy_session_id), PhIcons.Copy, onClick = onCopy))
        onArchive?.let { add(JAction(resources.getString(R.string.assistant_archive), PhIcons.Archive, onClick = it)) }
        if (onEnd != null && live) add(JAction(resources.getString(R.string.assistant_end), PhIcons.Stop, destructive = true, separatorBefore = true, onClick = onEnd))
    }

private val AutoOrigin.icon: PhIcon get() = when (this) {
    AutoOrigin.Inbox -> PhIcons.Tray
    AutoOrigin.Task -> PhIcons.Clock
    AutoOrigin.LarkAgent -> PhIcons.At
    AutoOrigin.Spawned -> PhIcons.GitBranch
    AutoOrigin.Extension -> PhIcons.PuzzlePiece
    AutoOrigin.Other -> PhIcons.DotsThree
}

@Composable
private fun AutoOrigin.label(): String = stringResource(when (this) {
    AutoOrigin.Inbox -> R.string.assistant_origin_inbox
    AutoOrigin.Task -> R.string.assistant_origin_task
    AutoOrigin.LarkAgent -> R.string.assistant_origin_lark_agent
    AutoOrigin.Spawned -> R.string.assistant_origin_spawned
    AutoOrigin.Extension -> R.string.assistant_origin_extension
    AutoOrigin.Other -> R.string.assistant_origin_other
})

/** The state a tally shows: someone waiting for the user first, else running, else none. */
private fun OriginTally.status(): Status? = when {
    needsYou > 0 -> Status.NeedsYou
    working > 0 -> Status.Working
    else -> null
}

/** "Automatic chats"'s row: its count, and while folded what waits for the user (their count) or a dot while some run. */
@Composable
private fun AutoConversationsRow(total: OriginTally, open: Boolean, onToggle: () -> Unit) {
    val needs = pluralStringResource(R.plurals.assistant_auto_needs_you, total.needsYou, total.needsYou)
    val running = pluralStringResource(R.plurals.assistant_auto_working, total.working, total.working)
    JRow(stringResource(R.string.assistant_auto), leadingIcon = PhIcons.Lightning, onClick = onToggle, chevron = false,
        subtitle = if (open) null else listOfNotNull(needs.takeIf { total.needsYou > 0 }, running.takeIf { total.working > 0 })
            .joinToString(" · ").ifBlank { null },
        trailing = {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                if (!open && total.needsYou > 0) JBadge(total.needsYou)
                else if (!open && total.working > 0) JStatusDot(Status.Working)
                Text("${total.count}", style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1)
                JIcon(if (open) PhIcons.CaretDown else PhIcons.CaretRight, null, size = Tokens.Space.l, tint = PersonaTheme.palette.ink3)
            }
        })
}

/** The origins "Automatic chats" has, one chip each with its count and state; [onPick] lists that origin's conversations. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun AutoOriginChips(origins: List<OriginTally>, picked: AutoOrigin?, onPick: (AutoOrigin) -> Unit) {
    FlowRow(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.m),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        for (o in origins) {
            val origin = o.origin ?: continue
            JChip(origin.label(), selected = origin == picked, onClick = { onPick(origin) }, icon = origin.icon, status = o.status(), count = o.count)
        }
    }
}

/**
 * The drawer's first row: the main conversation ([main]; null before it has started, or not yet loaded), read as
 * Persona's name after the app's mark. Beside it what it waits on (its pending count, else working / needs you /
 * unread); its menu (⋯ or a long press) copies its session id and ends it while it runs. It cannot be pinned or archived.
 */
@Composable
private fun PersonaRow(main: Session?, selected: Boolean, hasMain: Boolean, onMain: () -> Unit, onEnd: (() -> Unit)?, onCopy: () -> Unit) {
    var menu by remember { mutableStateOf(false) }
    val name = LocalPersonaName.current
    val status = main?.conversationStatus()
    val pending = main?.pendingInteractions ?: 0
    JRow(name, onClick = onMain, onLongClick = main?.let { { menu = true } }, selected = selected,
        // "Main chat" under the name keeps the row readable as a conversation, not a brand heading (#6327, as the web, #6293).
        subtitle = stringResource(if (main == null && !hasMain) R.string.assistant_start_main else R.string.assistant_main_chat),
        leading = { JAppMark(Tokens.Space.xxl) }, chevron = false,
        trailing = if (main == null) null else ({
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                if (pending > 0) JBadge(pending)
                else if (status == Status.Working || status == Status.NeedsYou || status == Status.Unread) StatusLabel(status)
                JIconButton(PhIcons.DotsThree, stringResource(R.string.assistant_conversation_actions), { menu = true })
            }
        }))
    if (menu && main != null) JActionSheet(
        title = name, onDismiss = { menu = false },
        actions = conversationActions(androidx.compose.ui.platform.LocalResources.current, pinned = false, main.isLive,
            onPin = null, onCopy = onCopy, onEnd = onEnd),
    )
}

@Composable
private fun DrawerItem(title: String, icon: PhIcon, onClick: () -> Unit) {
    JRow(title, leadingIcon = icon, onClick = onClick)
}

@Composable
private fun DrawerItem(title: String, icon: PhIcon, badge: Int, onClick: () -> Unit) {
    JRow(
        title,
        leadingIcon = icon,
        onClick = onClick,
        trailing = if (badge > 0) ({ JBadge(badge) }) else null
    )
}

@Composable
internal fun AssistantDemoNotice() = JNotice(stringResource(R.string.assistant_demo_notice), title = stringResource(R.string.assistant_demo_title), icon = PhIcons.Info)

@Composable
internal fun AssistantDemo(navigator: Navigator) {
    val words = demoWords()
    val repository = remember { DemoAssistantRepository(words) }
    val messages by repository.outbox.collectAsStateWithLifecycle()
    AssistantScreen(navigator, repository, repository, repository::start, repository.pinned, demo = true,
        chatContent = { session ->
            LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                items(messages.filter { it.session == session }, key = { it.clientMessageId }) { message ->
                    UserBubble(message.text)
                }
                item { Text(stringResource(R.string.assistant_demo_reply), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2) }
            }
        })
}
