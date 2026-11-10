package persona.feature.inbox

import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.Navigator
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/**
 * "Hand to AI" (iOS `InboxHandOffSheet`, the old app's sheet): what to do with the item (three presets or the user's own
 * words), who does it — Persona's main conversation (2026-11-02: hand-offs go there) or one of the live Agents — and
 * whether to file the item as done. Persona receives the item through its hand-off route; older hubs and Agent
 * targets use the chat outbox. Once accepted, the sheet closes and the conversation it went to opens.
 *
 * [context] replaces the hub's text of the item (the mail page has the whole message); [onHandedOff] hears where it
 * went (null: Persona).
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun InboxHandOffSheet(
    repository: InboxRepository,
    record: InboxRecord,
    navigator: Navigator?,
    onDismiss: () -> Unit,
    context: String? = null,
    onHandedOff: (HandOffTarget?) -> Unit = {},
) {
    val snapshot by repository.snapshot.collectAsStateWithLifecycle()
    val feedback = LocalAppFeedback.current
    val scope = rememberCoroutineScope()
    val itemId = record.item.id
    var instruction by rememberSaveable(itemId) { mutableStateOf("") }
    var targetKey by rememberSaveable(itemId) { mutableStateOf<String?>(null) }
    var markDone by rememberSaveable(itemId) { mutableStateOf(false) }
    var doneChosen by rememberSaveable(itemId) { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    val attempt = remember(itemId) { HandOffAttempt() }
    val live = remember(snapshot.sessions, snapshot.machines) { handOffTargets(snapshot.sessions.map { it.session }, snapshot.machines, limit = Int.MAX_VALUE) }
    // Where this item's source went last time (#2121): preselected once the memory is read, and marked Last time.
    val memory by repository.handOffMemory.collectAsStateWithLifecycle(initialValue = null)
    val memoryKey = remember(record.source, record.mutes) { InboxHandOffMemory.key(record.source, record.mutes) }
    var preselected by rememberSaveable(itemId) { mutableStateOf(false) }
    var lastKey by rememberSaveable(itemId) { mutableStateOf<String?>(null) }
    var lastPersona by rememberSaveable(itemId) { mutableStateOf(false) }
    var lastGone by rememberSaveable(itemId) { mutableStateOf(false) }
    LaunchedEffect(memory, live) {
        val entries = memory ?: return@LaunchedEffect
        if (preselected) return@LaunchedEffect
        preselected = true
        val r = memoryKey?.let { InboxHandOffMemory.resolve(InboxHandOffMemory.recall(entries, it)) { path -> live.any { t -> t.key == path } } }
            ?: return@LaunchedEffect
        val agent = r.target.removePrefix("agent:").takeIf { r.target.startsWith("agent:") }
        targetKey = agent
        if (!doneChosen) markDone = agent != null
        lastKey = agent?.takeIf { r.lastTime }
        lastPersona = r.lastTime && agent == null
        lastGone = r.gone
    }
    // The 8 latest, and the one this source went to last time even when it is older than those.
    val targets = remember(live, lastKey) {
        val latest = live.take(8)
        val last = live.firstOrNull { it.key == lastKey }
        if (last != null && last !in latest) listOf(last) + latest else latest
    }
    val target = targets.firstOrNull { it.key == targetKey }
    val palette = PersonaTheme.palette
    val res = LocalResources.current
    val presets = handoffPresets(res)
    fun selectTarget(next: HandOffTarget?) {
        if (busy) return
        targetKey = next?.key
        lastGone = false
        if (!doneChosen) markDone = next != null
    }
    fun send() {
        if (busy) return
        busy = true
        error = null
        scope.launch {
            try {
                val text = handoffPrompt(res, handOffItem(record, context ?: repository.personaContext(itemId)), instruction)
                val operation = attempt.operationId(text, target)
                val session = repository.handOff(InboxHandOffRequest(itemId, text, target?.machineId, target?.sessionId, operation))
                attempt.done()
                memoryKey?.let { key ->
                    val entries = InboxHandOffMemory.remember(memory.orEmpty(), key, target?.let(InboxHandOffMemory::agent) ?: InboxHandOffMemory.MAIN)
                    try { repository.saveHandOffMemory(entries) } catch (e: CancellationException) { throw e } catch (_: Exception) {}
                }
                if (markDone && !record.item.isDone) {
                    try { repository.setDone(itemId, true) }
                    catch (e: CancellationException) { throw e }
                    catch (_: Exception) { feedback?.notice(res.getString(R.string.inbox_handoff_done_failed)) }
                }
                onDismiss()
                onHandedOff(target)
                if (target == null) {
                    if (session != null) navigator?.navigate(ChatRoute(session.machineId, session.sessionId, ConversationKind.Assistant))
                    else navigator?.selectTab(AppTab.Persona)
                } else navigator?.navigate(ChatRoute(target.machineId, target.sessionId, ConversationKind.Agent))
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { error = res.getString(R.string.inbox_handoff_failed, failureReason(e).resolve(res)) }
            finally { busy = false }
        }
    }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.inbox_hand_to_ai), dismissible = !busy, scrollable = true) {
        Column(Modifier.fillMaxWidth().imePadding()) {
            JSectionHeader(stringResource(R.string.inbox_handoff_what))
            JTextField(instruction, { instruction = it }, placeholder = presets[0], minLines = 2, maxLines = 5, enabled = !busy)
            Spacer(Modifier.height(Tokens.Space.s))
            FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                presets.forEach { preset ->
                    JChip(preset, selected = instruction.trim() == preset, onClick = { if (!busy) instruction = preset })
                }
            }
            JSectionHeader(stringResource(R.string.inbox_handoff_who))
            val rows = targets.size + 1
            JGroupItem(GroupPosition.of(0, rows)) {
                JRow("Persona", subtitle = stringResource(R.string.inbox_handoff_main_chat), leadingIcon = PhIcons.Sparkle, onClick = { selectTarget(null) }, chevron = false,
                    trailing = { LastTimeRadio(target == null, lastPersona) })
            }
            targets.forEachIndexed { index, agent ->
                JGroupItem(GroupPosition.of(index + 1, rows)) {
                    JRow(agent.title, subtitle = agent.machine, leadingIcon = PhIcons.Robot, onClick = { selectTarget(agent) },
                        chevron = false, trailing = { LastTimeRadio(target == agent, agent.key == lastKey) })
                }
            }
            if (targets.isEmpty()) {
                Spacer(Modifier.height(Tokens.Space.s))
                Text(stringResource(R.string.inbox_handoff_no_agents), style = PersonaTheme.type.caption, color = palette.ink3)
            }
            if (lastGone) {
                Spacer(Modifier.height(Tokens.Space.s))
                Text(stringResource(R.string.inbox_handoff_last_gone), style = PersonaTheme.type.caption, color = palette.status(Status.NeedsYou).text)
            }
            if (!record.item.isDone) {
                Spacer(Modifier.height(Tokens.Space.l))
                JGroupItem(GroupPosition.Single) { JSwitchRow(stringResource(R.string.inbox_handoff_mark_done), markDone, { if (!busy) { markDone = it; doneChosen = true } }) }
            }
            error?.let {
                Spacer(Modifier.height(Tokens.Space.m))
                JNotice(it, status = Status.NeedsYou)
            }
            Spacer(Modifier.height(Tokens.Space.xl))
            JButton(stringResource(R.string.inbox_send), ::send, Modifier.fillMaxWidth(), icon = PhIcons.PaperPlaneTilt, loading = busy, loadingText = stringResource(R.string.inbox_handoff_sending))
        }
    }
}

/** The row's radio, with "Last time" before it on the target this item's source went to last time (#2121). */
@Composable
private fun LastTimeRadio(selected: Boolean, last: Boolean) {
    Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        if (last) JTag(stringResource(R.string.inbox_handoff_last_time), Tone.Neutral)
        JRadio(selected, null)
    }
}
