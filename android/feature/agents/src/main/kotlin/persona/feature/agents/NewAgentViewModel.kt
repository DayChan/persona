package persona.feature.agents

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.UiText
import persona.core.designsystem.failureReasonText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.launch
import java.util.UUID

/** A new tab in an existing herdr workspace: machine and directory are fixed. */
internal data class AgentSpacePreset(val machineId: String, val workspaceId: String, val cwd: String, val name: String)

/**
 * What a caller fills in ahead (the knowledge base's "Open Agent"): [machineId] and [cwd] are picked when that machine is
 * online, [prompt] starts the first message; all of it can still be changed.
 */
internal data class NewAgentPrefill(val machineId: String? = null, val cwd: String? = null, val prompt: String? = null)

/** The hub's "New Agent" model use (docs/assistant/model-config.md), read once per form. */
internal sealed interface NewAgentModelConfig {
    /** Still reading. */
    data object Unknown : NewAgentModelConfig
    /** An older hub: a launcher is picked here as before. */
    data object Unsupported : NewAgentModelConfig
    /** New agents follow [detail] unless a launcher is picked for this start. */
    data class Follow(val detail: ModelPurposeDetail) : NewAgentModelConfig
    /** The hub has it but it could not be read: a launcher must be picked, or the options reloaded. */
    data class Failed(val reason: UiText) : NewAgentModelConfig
}

/**
 * The model list of the launcher that will run ([launcher] on [machineId]), for the "Model" row. [list] stays while it
 * is asked again; null while the first answer is on its way, after a failure, and from hubs without the list.
 */
internal data class NewAgentModels(
    val machineId: String = "",
    val launcher: String = "",
    val list: LauncherModels? = null,
    val loading: Boolean = false,
    val error: UiText? = null,
)

internal data class NewAgentState(
    val initialized: Boolean = false,
    val space: AgentSpacePreset? = null,
    val machineId: String = "",
    val selection: AgentDefaults = AgentDefaults(),
    val launchers: List<AgentLauncher> = emptyList(),
    val recent: List<AgentRecentDirectory> = emptyList(),
    val browser: AgentDirectories? = null,
    val browsing: Boolean = false,
    val browsePath: String = "",
    val prompt: String = "",
    val loading: Boolean = false,
    val starting: Boolean = false,
    val pending: AgentStartOperation? = null,
    val created: Session? = null,
    val error: AgentText? = null,
    val modelConfig: NewAgentModelConfig = NewAgentModelConfig.Unknown,
    /** The prefilled machine was not online, so another one was picked (named here for the form to say so). */
    val prefillOffline: String? = null,
    val models: NewAgentModels = NewAgentModels(),
    /** The model picked for this start, an id of [modelList]; "": the default. Never saved, like a launcher override. */
    val model: String = "",
) {
    val locked get() = !initialized || starting || pending != null || created != null
    val follow: ModelPurposeDetail? get() = (modelConfig as? NewAgentModelConfig.Follow)?.detail
    /** An empty launcher follows the model configuration, when the hub has one. */
    val following: Boolean get() = follow != null && selection.launcher.isEmpty()
    /** The launcher the "New Agent" configuration resolves to on this machine, when it is one of its launchers. */
    val configuredLauncher: AgentLauncher? get() = follow?.purpose?.effective?.provider?.removePrefix("launcher.")
        ?.let { name -> launchers.firstOrNull { it.name == name } }
    val agentType: String get() = if (following) configuredLauncher?.agentType ?: "claude"
        else launchers.firstOrNull { it.name == selection.launcher }?.agentType ?: "claude"
    /** The launcher that will run, whose models the "Model" row lists: the one picked, or the configuration's. */
    val modelLauncher: String get() = if (following) configuredLauncher?.name.orEmpty() else selection.launcher
    /** [modelLauncher]'s models, once there are some to show. */
    val modelList: LauncherModels? get() = models.list?.takeIf {
        it.models.isNotEmpty() && models.machineId == machineId && models.launcher == modelLauncher
    }
    /** There is a list and a pick reaches the agent (the launcher does not pass its own --model). */
    val canPickModel: Boolean get() = modelList.let { it != null && it.locked == null }
    val pickedModel: AgentModel? get() = model.takeIf { it.isNotEmpty() && canPickModel }
        ?.let { id -> modelList?.models?.firstOrNull { it.id == id } }
    /** The `model` a start sends: only a picked one that is not the default (picking the default is picking none). */
    val sentModel: String? get() = pickedModel?.takeUnless { it.isDefault }?.id
    /** Model configuration › New Agent's model: what runs with no pick when its launcher is the one that runs. */
    val configuredModel: String get() = follow?.takeIf { configuredLauncher?.name == modelLauncher }
        ?.purpose?.effective?.model.orEmpty()
    /** The thinking efforts the model that will run takes, weakest first; null: not known, every level is offered. */
    val modelEfforts: List<String>? get() {
        val list = modelList ?: return null
        val running = pickedModel ?: if (configuredModel.isEmpty()) list.models.firstOrNull { it.isDefault }
            else list.models.firstOrNull { it.id == configuredModel }
        return running?.efforts
    }
}

/**
 * The form kept consistent with the model list: a pick the list no longer has goes back to the default, and an
 * effort the model that will run does not take back to "Follow launcher".
 */
private fun NewAgentState.settled(): NewAgentState {
    var next = this
    val list = modelList
    if (model.isNotEmpty() && list != null && list.models.none { it.id == model }) next = next.copy(model = "")
    val efforts = next.modelEfforts
    if (efforts != null && next.selection.effort.isNotEmpty() && next.selection.effort !in efforts) {
        next = next.copy(selection = next.selection.copy(effort = ""))
    }
    return next
}

internal class NewAgentViewModel(
    private val service: AgentCreationService,
    private val store: AgentCreationStore,
    private val machines: StateFlow<List<Machine>>,
    /** Sends the first message as an ordinary one once the hub answered an async start; null keeps it in `prompt`. */
    private val sender: MessageSender? = null,
    /** Lists a started session at once, so its chat has metadata before the next `sessions` push. */
    private val adopt: suspend (Session) -> Unit = {},
    private val prefill: NewAgentPrefill? = null,
) : ViewModel() {
    private val mutable = MutableStateFlow(NewAgentState())
    val state = mutable.asStateFlow()
    private var machineJob: Job? = null
    private var browseJob: Job? = null
    private var modelsJob: Job? = null
    init {
        viewModelScope.launch {
            try {
                val pending = store.pending(service.scope)
                if (pending != null) {
                    val r = pending.request
                    mutable.value = NewAgentState(initialized = true, machineId = r.machineId,
                        selection = AgentDefaults(r.cwd, r.launcher, r.permissionMode.orEmpty(), r.effort.orEmpty()), model = r.model.orEmpty(),
                        prompt = pending.firstMessage ?: r.prompt.orEmpty(), pending = pending, error = AgentText(R.string.agents_new_error_pending))
                } else {
                    val last = store.lastMachine(service.scope)
                    mutable.update { it.copy(initialized = true, prompt = prefill?.prompt.orEmpty()) }
                    machines.collect { list ->
                        if (state.value.machineId.isEmpty() && !state.value.locked) {
                            val space = state.value.space
                            val wanted = prefill?.machineId?.takeIf { it.isNotBlank() }
                            val pick = if (space != null) list.firstOrNull { it.id == space.machineId && it.online }
                                else (wanted?.let { id -> list.firstOrNull { it.id == id && it.online } }
                                    ?: list.firstOrNull { it.id == last && it.online } ?: list.firstOrNull { it.online })
                            if (pick != null) {
                                if (space == null && wanted != null && pick.id != wanted) {
                                    mutable.update { it.copy(prefillOffline = list.firstOrNull { m -> m.id == wanted }?.displayName ?: wanted) }
                                }
                                selectMachine(pick.id)
                            }
                        }
                    }
                }
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(error = AgentText(R.string.agents_new_error_record)) } }
        }
    }
    /** Opens the form for a new session, or for a new tab in [space]; an unconfirmed start keeps its own form. */
    fun startIn(space: AgentSpacePreset?) {
        val current = state.value
        if (current.pending != null || current.created != null || current.starting || current.space == space) return
        machineJob?.cancel()
        browseJob?.cancel()
        // The space's machine is the only choice; until it is online nothing is selected and nothing can start.
        mutable.update { it.copy(space = space, machineId = if (space != null) "" else it.machineId,
            browsing = false, browser = null, loading = false, error = null) }
        val machine = space?.machineId ?: current.machineId
        if (current.initialized && machine.isNotEmpty()) selectMachine(machine)
    }
    fun selectMachine(id: String) {
        if (state.value.locked || machines.value.none { it.id == id && it.online }) return
        state.value.space?.let { if (it.machineId != id) return }
        machineJob?.cancel()
        browseJob?.cancel()
        modelsJob?.cancel()
        mutable.update { it.copy(machineId = id, selection = AgentDefaults(), launchers = emptyList(), recent = emptyList(),
            browser = null, browsing = false, loading = true, error = null, models = NewAgentModels(), model = "") }
        machineJob = viewModelScope.launch {
            try {
                val defaults = store.defaults(service.scope, id)
                val known = state.value.modelConfig.takeIf { it is NewAgentModelConfig.Follow || it is NewAgentModelConfig.Unsupported }
                val (launchers, dirs, config) = coroutineScope {
                    val l = async { service.launchers(id) }
                    val d = async { service.directories(id) }
                    // The "New Agent" configuration is the hub's, not the machine's: read once per form.
                    val c = async { known ?: modelConfig() }
                    Triple(l.await(), d.await(), c.await())
                }
                val prefilledCwd = prefill?.cwd?.takeIf { prefill.machineId == id && it.isNotBlank() }
                mutable.update { it.copy(loading = false, launchers = launchers, recent = dirs.recent.take(6), modelConfig = config,
                    selection = defaults.copy(cwd = it.space?.cwd ?: prefilledCwd ?: defaults.cwd.ifEmpty { dirs.recent.firstOrNull()?.path ?: dirs.path },
                        launcher = defaultLauncher(config, defaults, launchers)),
                    error = (config as? NewAgentModelConfig.Failed)?.let { failed -> AgentText(R.string.agents_new_error_model_config, failed.reason) }) }
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(loading = false, error = AgentText(R.string.agents_new_error_load)) } }
            loadModels()
        }
    }
    /**
     * Reads the model list of the launcher that will run, unless it is already there (or on its way) for that
     * launcher; [refresh] asks the launcher's CLI again. It never holds up starting: the default stays possible.
     */
    private fun loadModels(refresh: Boolean = false) {
        val current = state.value
        val machine = current.machineId
        val launcher = current.modelLauncher
        val same = current.models.machineId == machine && current.models.launcher == launcher
        if (same && !refresh) return
        modelsJob?.cancel()
        if (machine.isEmpty() || launcher.isEmpty()) {
            mutable.update { it.copy(models = NewAgentModels(machine, launcher)) }
            return
        }
        mutable.update { it.copy(models = NewAgentModels(machine, launcher, list = it.models.list.takeIf { same }, loading = true)) }
        modelsJob = viewModelScope.launch {
            val next = try {
                NewAgentModels(machine, launcher, list = service.launcherModels(machine, launcher, refresh))
            } catch (e: CancellationException) { throw e }
            catch (error: Exception) {
                NewAgentModels(machine, launcher, list = state.value.models.list.takeIf { same }, error = failureReasonText(error))
            }
            mutable.update { if (it.machineId == machine && it.modelLauncher == launcher) it.copy(models = next).settled() else it }
        }
    }
    /** Asks the launcher's CLI for its models again (the node keeps the list a while). */
    fun reloadModels() {
        if (!state.value.locked) loadModels(refresh = true)
    }
    /** Picks the model for this start; the default entry, or "", is no pick. */
    fun pickModel(id: String) {
        val current = state.value
        if (current.locked || !current.canPickModel) return
        val entry = current.modelList?.models?.firstOrNull { it.id == id }
        mutable.update { it.copy(model = entry?.takeUnless { m -> m.isDefault }?.id.orEmpty(), error = null).settled() }
    }
    private suspend fun modelConfig(): NewAgentModelConfig = try {
        service.newAgentModel()?.let { NewAgentModelConfig.Follow(it) } ?: NewAgentModelConfig.Unsupported
    } catch (e: CancellationException) { throw e }
    catch (error: Exception) { NewAgentModelConfig.Failed(failureReasonText(error)) }
    fun selection(value: AgentDefaults) {
        if (state.value.locked || state.value.loading) return
        // Another launcher has its own models: back to its default, and its list.
        mutable.update { val next = it.copy(selection = value, error = null)
            (if (next.modelLauncher != it.modelLauncher) next.copy(model = "") else next).settled() }
        loadModels()
    }
    fun prompt(value: String) { if (!state.value.locked) mutable.update { it.copy(prompt = value) } }
    fun browse(path: String = state.value.selection.cwd) {
        if (state.value.locked || state.value.space != null) return
        browseJob?.cancel()
        val machine = state.value.machineId
        mutable.update { it.copy(browsing = true, browser = null, browsePath = path, loading = true, error = null) }
        browseJob = viewModelScope.launch {
            try {
                val dirs = service.directories(machine, path)
                mutable.update { it.copy(browser = dirs, loading = false) }
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(loading = false, error = AgentText(R.string.agents_new_error_browse)) } }
        }
    }
    fun closeBrowser(use: Boolean = false) {
        browseJob?.cancel()
        mutable.update { it.copy(browsing = false, browser = null, loading = false, error = null,
            selection = if (use && it.browser != null) it.selection.copy(cwd = it.browser.path) else it.selection) }
    }
    fun resume(session: Session) {
        val current = state.value
        if (!current.initialized || current.starting || current.created != null) return
        current.pending?.let {
            if (it.request.machineId == session.machineId && it.request.resumeSessionId == session.sessionId) submit()
            else mutable.update { it.copy(error = AgentText(R.string.agents_new_error_other_pending)) }
            return
        }
        if (session.cwd.isBlank() || machines.value.none { it.id == session.machineId && it.online }) {
            mutable.update { it.copy(error = AgentText(R.string.agents_new_error_resume_offline)) }
            return
        }
        machineJob?.cancel()
        browseJob?.cancel()
        mutable.update { it.copy(starting = true, loading = false, error = null) }
        viewModelScope.launch {
            try {
                val type = session.agent ?: "claude"
                val available = service.launchers(session.machineId)
                val launcher = session.launcher?.takeIf { name -> available.any { it.name == name && it.agentType == type } }
                    ?: available.firstOrNull { it.agentType == type && session.launcher.isNullOrBlank() }?.name
                if (launcher == null) {
                    mutable.update { it.copy(error = AgentText(R.string.agents_new_error_launcher_gone)) }
                    return@launch
                }
                execute(AgentStartOperation(UUID.randomUUID().toString(), AgentStartRequest(
                    session.machineId, session.cwd, launcher,
                    agentType = type.takeIf { it != "claude" }, resumeSessionId = session.sessionId,
                )), fresh = true)
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(error = if (it.pending == null) AgentText(R.string.agents_new_error_resume) else AgentText(R.string.agents_new_error_resume_pending)) } }
            finally { mutable.update { it.copy(starting = false) } }
        }
    }
    fun submit() {
        val current = state.value
        if (!current.initialized || current.starting || current.loading || current.created != null) return
        val previous = current.pending
        val agentType = current.agentType
        if (previous == null && (current.selection.cwd.isBlank() || (current.selection.launcher.isBlank() && !current.following) ||
                machines.value.none { it.id == current.machineId && it.online } ||
                (current.space != null && current.space.machineId != current.machineId))) return
        mutable.update { it.copy(starting = true, error = null) }
        viewModelScope.launch {
            try {
                val operation = previous ?: newOperation(current, agentType)
                execute(operation, fresh = previous == null)
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { mutable.update { it.copy(error = if (it.pending == null) AgentText(R.string.agents_new_error_save) else AgentText(R.string.agents_new_error_no_result)) } }
            finally { mutable.update { it.copy(starting = false) } }
        }
    }
    /**
     * Claude starts in the background (docs/protocol/protocol.md "New session"): the hub answers with a starting session, which
     * opens at once, and the first message goes as an ordinary message (on screen at once, sent once Claude is ready),
     * its id kept with the operation so it is sent once whatever happens to this process. Codex takes its first message
     * with the start, and so does any start without a sender.
     */
    private fun newOperation(current: NewAgentState, agentType: String): AgentStartOperation {
        val first = current.prompt.trim().takeIf { it.isNotEmpty() }
        val async = sender != null && agentType == "claude"
        val efforts = current.modelEfforts
        val request = AgentStartRequest(
            current.machineId, current.selection.cwd, current.selection.launcher,
            first.takeUnless { async }, current.selection.mode.takeIf { it.isNotEmpty() && agentType == "claude" },
            // Codex starts take no effort; Claude's only one the model that will run takes.
            current.selection.effort.takeIf { it.isNotEmpty() && agentType == "claude" && (efforts == null || it in efforts) },
            current.space?.workspaceId, agentType.takeIf { it != "claude" }, async = true.takeIf { async },
            nameHint = first.takeIf { async }, model = current.sentModel,
        )
        return AgentStartOperation(UUID.randomUUID().toString(), request,
            firstMessage = first.takeIf { async }, firstMessageId = UUID.randomUUID().toString().takeIf { async && first != null })
    }
    private suspend fun execute(operation: AgentStartOperation, fresh: Boolean) {
        if (fresh) {
            store.pending(service.scope, operation)
            mutable.update { it.copy(pending = operation) }
        }
        val result = if (fresh) service.start(operation.request, operation.id) else service.operation(operation.id, operation.request.machineId)
        when (result) {
            is AgentStartResult.Created -> {
                require(result.session.machineId == operation.request.machineId && result.session.sessionId.isNotBlank())
                val request = operation.request
                val session = result.session
                try { adopt(session) } catch (e: CancellationException) { throw e } catch (_: Exception) { }
                val first = operation.firstMessage
                val firstId = operation.firstMessageId
                if (first != null && firstId != null) {
                    val queued = try { checkNotNull(sender).send(session.key, first, firstId); true }
                        catch (e: CancellationException) { throw e } catch (_: Exception) { false }
                    if (!queued) {
                        // The record stays: checking again queues the same message under the same id, never a second one.
                        mutable.update { it.copy(error = AgentText(R.string.agents_new_error_first_message)) }
                        return
                    }
                }
                // A tab in an existing space must not replace the machine's remembered directory.
                if (request.workspaceId == null && request.resumeSessionId == null) {
                    // A Codex (or any non-Claude) start sends no mode or effort: the machine keeps the ones its next
                    // Claude Code starts with, as on iOS and the web (#1898).
                    val kept = if (request.agentType != null) store.defaults(service.scope, request.machineId) else null
                    store.saveDefaults(service.scope, request.machineId, AgentDefaults(request.cwd, request.launcher,
                        kept?.mode ?: request.permissionMode.orEmpty(), kept?.effort ?: request.effort.orEmpty()))
                }
                // Keep the receipt until navigation is acknowledged, including across process death.
                mutable.update { it.copy(created = session) }
            }
            is AgentStartResult.Rejected -> {
                store.pending(service.scope, null)
                mutable.update { it.copy(pending = null, error = AgentText.raw(result.message.shown)) }
            }
            AgentStartResult.Pending -> mutable.update { it.copy(error = AgentText(R.string.agents_new_error_starting)) }
            AgentStartResult.Unknown -> mutable.update { it.copy(error = AgentText(R.string.agents_new_error_unknown)) }
        }
    }
    fun opened() {
        viewModelScope.launch { acknowledge() }
    }
    /** The new chat is open: the start's record goes. For a page about to leave, which takes its scope with it. */
    suspend fun acknowledge() {
        try {
            store.pending(service.scope, null)
            mutable.update { it.copy(pending = null, created = null, prompt = "") }
        } catch (e: CancellationException) { throw e }
        catch (_: Exception) { mutable.update { it.copy(error = AgentText(R.string.agents_new_error_cleanup)) } }
    }
}

/**
 * The launcher a machine starts with: none (follow the "New Agent" configuration) when the hub has one, so a remembered
 * launcher never overrides it (docs/assistant/model-config.md); nothing when that configuration could not be read; otherwise the
 * machine's remembered launcher or its first.
 */
internal fun defaultLauncher(config: NewAgentModelConfig, defaults: AgentDefaults, launchers: List<AgentLauncher>): String = when (config) {
    is NewAgentModelConfig.Follow, is NewAgentModelConfig.Failed -> ""
    else -> defaults.launcher.takeIf { name -> launchers.any { it.name == name } } ?: launchers.firstOrNull()?.name.orEmpty()
}
