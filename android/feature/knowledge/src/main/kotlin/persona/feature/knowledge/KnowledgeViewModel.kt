@file:OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)

package persona.feature.knowledge

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import persona.core.contracts.HubScope
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.operationFeedback
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import androidx.annotation.StringRes
import java.io.IOException
import java.util.UUID

internal data class KnowledgeFilter(
    val kind: KnowledgeKind = KnowledgeKind.Projects,
    val tag: String? = null,
    val query: String = "",
    val revision: Int = 0,
) {
    fun sameView(other: KnowledgeFilter) = kind == other.kind && tag == other.tag && query == other.query
}

internal data class KnowledgePage(
    val scope: HubScope? = null,
    val filter: KnowledgeFilter = KnowledgeFilter(),
    val loading: Boolean = false,
    val paired: Boolean = true,
    val entries: List<KnowledgeEntry> = emptyList(),
    val tags: List<String> = emptyList(),
    val groups: List<KnowledgeSearchGroup> = emptyList(),
    @StringRes val message: Int? = null,
    /** The list or search shown is the answer to [filter] (not what an earlier one left while this one loads). */
    val loaded: Boolean = false,
)

/** Tagging saved on an entry's page; the list (another view model) reloads to show it. */
internal object KnowledgeEdits {
    val revision = MutableStateFlow(0)
    fun changed() = revision.update { it + 1 }
}

internal class KnowledgeViewModel(
    connection: KnowledgeConnection,
    private val dispatcher: CoroutineDispatcher = Dispatchers.Default,
    edits: StateFlow<Int> = KnowledgeEdits.revision,
) : ViewModel() {
    private val filter = MutableStateFlow(KnowledgeFilter())
    /** Names of the entries lists have shown, per pairing: search hits carry little more than an id. */
    private val names = mutableMapOf<String, KnowledgeEntry>()
    private var namesScope: HubScope? = null
    private var last: KnowledgePage? = null

    val page = combine(connection.current, filter, edits) { repository, selection, _ -> repository to selection }
        .flatMapLatest { (repository, selection) -> flow {
            val initial = KnowledgePage(scope = repository?.scope, filter = selection, paired = repository != null)
            if (repository == null) { emit(initial); return@flow }
            // Reloading the same view keeps what it showed until the answer comes (a refresh, a save on an entry's page),
            // and after a failed answer too: the page says it could not reload over what it still shows (#3991).
            val shown = last?.takeIf { it.scope == repository.scope && it.filter.sameView(selection) }
            emit(initial.copy(loading = true, entries = shown?.entries.orEmpty(), tags = shown?.tags.orEmpty(), groups = shown?.groups.orEmpty()))
            try {
                if (selection.query.isBlank()) {
                    val result = repository.list(selection.kind, selection.tag)
                    val entries = withContext(dispatcher) { result.items.filter { it.inScope() }.distinctBy { it.key } }
                    remember(repository.scope, entries)
                    emit(initial.copy(entries = entries, tags = result.tags.orEmpty().distinct(), loaded = true))
                } else {
                    delay(300)
                    val hits = repository.search(selection.query.trim())
                    val missing = hits.mapNotNull { it.reference()?.first }.distinct().filter { kind -> !known(repository.scope, kind) }
                    val listed = coroutineScope { missing.map { kind -> async { runCatching { repository.list(kind).items }.getOrNull() } }.awaitAll() }
                    listed.filterNotNull().forEach { remember(repository.scope, it) }
                    val index = synchronized(names) { names.toMap() }
                    val groups = withContext(dispatcher) { searchGroups(hits, index) }
                    emit(initial.copy(groups = groups, loaded = true))
                }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                emit(initial.copy(entries = shown?.entries.orEmpty(), tags = shown?.tags.orEmpty(), groups = shown?.groups.orEmpty(),
                    message = loadMessage(error), loaded = shown?.loaded == true))
            }
        } }
        .onEach { if (!it.loading) last = it }
        .stateIn(viewModelScope, SharingStarted.WhileSubscribed(0, 0), KnowledgePage(loading = true))

    private fun remember(scope: HubScope, entries: List<KnowledgeEntry>) = synchronized(names) {
        if (namesScope != scope) { names.clear(); namesScope = scope }
        entries.forEach { names[it.key] = it }
    }

    private fun known(scope: HubScope, kind: KnowledgeKind) = synchronized(names) {
        namesScope == scope && names.keys.any { it.startsWith("${kind.wire}/") }
    }

    fun kind(kind: KnowledgeKind) { filter.update { it.copy(kind = kind, tag = null) } }
    fun tag(tag: String?) { filter.update { it.copy(tag = tag) } }
    fun query(query: String) { filter.update { it.copy(query = query) } }
    fun refresh() { filter.update { it.copy(revision = it.revision + 1) } }
}

internal data class KnowledgeDetailPage(
    val scope: HubScope? = null,
    val loading: Boolean = true,
    val paired: Boolean = true,
    val detail: KnowledgeDetail? = null,
    val agents: List<Session> = emptyList(),
    @StringRes val message: Int? = null,
)
internal data class KnowledgeSending(val busy: Boolean = false, @StringRes val message: Int? = null)

/** The tag editor's save: [saved] counts the saves that went through, so the sheet closes on each; [error] says why one did not. */
internal data class KnowledgeTagging(val busy: Boolean = false, val error: persona.core.designsystem.UiText? = null, val saved: Int = 0)

internal class KnowledgeDetailViewModel(
    private val kind: KnowledgeKind,
    private val id: String,
    private val connection: KnowledgeConnection,
    private val sender: MainConversationSender,
) : ViewModel() {
    private val revision = MutableStateFlow(0)
    private var sendJob: Job? = null
    private var attempt: Pair<String, String>? = null
    private val _sending = MutableStateFlow(KnowledgeSending())
    val sending = _sending.asStateFlow()
    private val _tagging = MutableStateFlow(KnowledgeTagging())
    val tagging = _tagging.asStateFlow()
    private val opened = Channel<Unit>(Channel.BUFFERED)
    val handoffs = opened.receiveAsFlow()
    private var last: KnowledgeDetailPage? = null
    val page = combine(connection.current, revision) { repository, _ -> repository }
        .flatMapLatest { repository -> flow {
            val initial = KnowledgeDetailPage(scope = repository?.scope, paired = repository != null, loading = repository != null)
            // A reload (pull, a saved tag) keeps the page on screen until the new one comes.
            val shown = last?.takeIf { repository != null && it.scope == repository.scope && it.detail != null }
            emit(initial.copy(detail = shown?.detail, agents = shown?.agents.orEmpty()))
            if (repository != null) try {
                val response = repository.entry(kind, id)
                emit(initial.copy(loading = false, detail = response.detail, agents = projectAgents(response.agents)))
            } catch (cancelled: CancellationException) { throw cancelled }
            // A failed reload keeps the entry on screen; the page says it could not reload (#3991).
            catch (error: Exception) { emit(initial.copy(loading = false, detail = shown?.detail, agents = shown?.agents.orEmpty(), message = loadMessage(error))) }
        } }
        .onEach { if (!it.loading) last = it }
        .stateIn(viewModelScope, SharingStarted.WhileSubscribed(0, 0), KnowledgeDetailPage())

    init {
        viewModelScope.launch {
            connection.current.collect {
                sendJob?.cancel()
                attempt = null
                _sending.value = KnowledgeSending()
                _tagging.value = KnowledgeTagging()
                while (opened.tryReceive().isSuccess) { }
            }
        }
    }

    fun refresh() { revision.update { it + 1 } }

    /** Hands [question] about the entry to the main chat; [kindName] is the entry's kind in the app's language. */
    fun ask(question: String, kindName: String) {
        if (_sending.value.busy || question.isBlank()) return
        val repository = connection.current.value ?: return
        val currentPage = page.value
        val detail = currentPage.detail ?: return
        if (currentPage.scope != repository.scope) return
        val text = knowledgeContext(detail, kindName) + "\n\n" + question.trim()
        val messageId = attempt?.takeIf { it.first == text }?.second ?: UUID.randomUUID().toString()
        attempt = text to messageId
        _sending.value = KnowledgeSending(busy = true)
        sendJob = viewModelScope.launch {
            try {
                checkCurrent(repository)
                // The data layer owns durable delivery, offline queueing and deduplication.
                sender.sendToMain(text, messageId)
                checkCurrent(repository)
                attempt = null
                _sending.value = KnowledgeSending(message = R.string.knowledge_handed)
                opened.send(Unit)
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) {
                _sending.value = KnowledgeSending(message = R.string.knowledge_hand_failed)
            }
        }
    }

    /** Saves hand-made tagging, then reads the entry again and has the list reload. */
    fun saveTags(update: KnowledgeUpdate) {
        if (_tagging.value.busy) return
        val repository = connection.current.value ?: return
        val saved = _tagging.value.saved
        _tagging.value = KnowledgeTagging(busy = true, saved = saved)
        viewModelScope.launch {
            try {
                checkCurrent(repository)
                repository.update(kind, id, update)
                checkCurrent(repository)
                _tagging.value = KnowledgeTagging(saved = saved + 1)
                refresh()
                KnowledgeEdits.changed()
            } catch (cancelled: CancellationException) {
                _tagging.value = KnowledgeTagging(saved = saved)
                throw cancelled
            } catch (error: Exception) {
                _tagging.value = KnowledgeTagging(error = operationFeedback(error).text, saved = saved)
            }
        }
    }

    fun dismissTagError() { _tagging.update { it.copy(error = null) } }

    private fun checkCurrent(repository: KnowledgeRepository) {
        if (connection.current.value !== repository) throw CancellationException("Pairing changed")
    }
}

@StringRes
internal fun loadMessage(error: Exception): Int {
    val failure = hubCodedFailure(error)
    return when {
        failure?.code == "kb_disabled" -> R.string.knowledge_disabled_text
        failure?.code == "kb_unavailable" -> R.string.knowledge_opening
        failure?.code == "kb_read_only" -> persona.core.designsystem.R.string.designsystem_knowledge_read_only
        failure?.code == "assistant_offline" -> persona.core.designsystem.R.string.designsystem_feedback_assistant_restarting
        failure?.code == "node_unreachable" -> persona.core.designsystem.R.string.designsystem_feedback_computer_offline
        failure?.code == "timeout" || failure?.status == 504 -> R.string.knowledge_timeout
        failure?.status == 401 -> persona.core.designsystem.R.string.designsystem_feedback_pairing_expired
        failure?.status == 403 -> R.string.knowledge_forbidden
        failure?.status == 404 -> R.string.knowledge_missing
        failure?.status != null -> R.string.knowledge_unreadable
        error is IOException -> R.string.knowledge_unreachable
        else -> R.string.knowledge_unreadable
    }
}
