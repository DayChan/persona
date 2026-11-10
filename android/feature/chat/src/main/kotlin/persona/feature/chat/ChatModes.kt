package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.annotation.StringRes
import android.content.res.Resources
import androidx.compose.material3.Text
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.runtime.*
import persona.core.contracts.Session
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.modelName
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.sort.canChangeEffortOnly
import persona.core.domain.sort.canChangeModes
import persona.core.domain.sort.canTypeClaudeCommands
import persona.core.domain.sort.hasSource

/** Claude Code's permission modes, effort levels and /model aliases as the app shows them (iOS SessionModes, node/modes.go). */
internal object SessionModes {
    /** [id] is what the hub and the agent use; the words are resources, or [raw] for an id the app does not know (shown as is). */
    data class Option(val id: String, @StringRes val titleRes: Int, @StringRes val detailRes: Int, val raw: String? = null) {
        fun title(resources: Resources): String = raw ?: resources.getString(titleRes)
        fun detail(resources: Resources): String = if (detailRes == 0) "" else resources.getString(detailRes)
    }

    const val BYPASS = "bypassPermissions"

    val permissionModes = listOf(
        Option("default", R.string.chat_modes_default, R.string.chat_modes_default_detail),
        Option("acceptEdits", R.string.chat_modes_accept_edits, R.string.chat_modes_accept_edits_detail),
        Option("plan", R.string.chat_modes_plan, R.string.chat_modes_plan_detail),
        Option("auto", R.string.chat_modes_auto, R.string.chat_modes_auto_detail),
        Option(BYPASS, R.string.chat_modes_bypass, R.string.chat_modes_bypass_detail),
    )

    val effortLevels = listOf(
        Option("low", R.string.chat_modes_effort_low, R.string.chat_modes_effort_low_detail),
        Option("medium", R.string.chat_modes_effort_medium, R.string.chat_modes_effort_medium_detail),
        Option("high", R.string.chat_modes_effort_high, R.string.chat_modes_effort_high_detail),
        Option("xhigh", R.string.chat_modes_effort_xhigh, R.string.chat_modes_effort_xhigh_detail),
        Option("max", R.string.chat_modes_effort_max, R.string.chat_modes_effort_max_detail),
    )

    /** Codex's reasoning efforts (node/codex_effort.go, iOS SessionModes.codexEffortLevels); a thread offers its model's. */
    val codexEffortLevels = listOf(
        Option("minimal", R.string.chat_modes_effort_minimal, R.string.chat_modes_codex_minimal_detail),
        Option("low", R.string.chat_modes_effort_low, R.string.chat_modes_codex_low_detail),
        Option("medium", R.string.chat_modes_effort_medium, R.string.chat_modes_codex_medium_detail),
        Option("high", R.string.chat_modes_effort_high, R.string.chat_modes_codex_high_detail),
        Option("xhigh", R.string.chat_modes_effort_xhigh, R.string.chat_modes_effort_xhigh_detail),
        Option("max", R.string.chat_modes_effort_max, R.string.chat_modes_effort_max_detail),
        Option("ultra", R.string.chat_modes_effort_ultra, R.string.chat_modes_codex_ultra_detail),
    )

    /** A running session's effort choices: its own levels when the hub lists them (a Codex thread on its app-server), Claude Code's otherwise. */
    fun effortOptions(session: Session): List<Option> =
        if (session.efforts.isEmpty()) effortLevels
        else session.efforts.map { id -> codexEffortLevels.firstOrNull { it.id == id } ?: Option(id, 0, 0, raw = id) }

    /** Claude Code's `/model` aliases (iOS PromptItems.claudeModels): picking one sends `/model <alias>`. */
    val claudeModels = listOf(
        Option("default", R.string.chat_modes_model_default, R.string.chat_modes_model_default_detail),
        Option("opus", 0, R.string.chat_modes_model_opus_detail, raw = "Opus"),
        Option("sonnet", 0, R.string.chat_modes_model_sonnet_detail, raw = "Sonnet"),
        Option("haiku", 0, R.string.chat_modes_model_haiku_detail, raw = "Haiku"),
    )

    /**
     * What a running session can switch to: not bypassPermissions (only at start), plus the current mode when it is
     * none of these (bypassPermissions, or a newer Claude Code's mode, shown by its raw name), so it still shows as picked.
     */
    fun switchable(current: String?): List<Option> {
        val list = permissionModes.filter { it.id != BYPASS }
        if (current.isNullOrEmpty() || list.any { it.id == current }) return list
        return list + (permissionModes.firstOrNull { it.id == current } ?: Option(current, 0, R.string.chat_modes_unknown_detail, raw = current))
    }

    /** The alias a session's model reads as ("claude-opus-…" → opus); a gateway model or none is the default. */
    fun claudeModelId(model: String?): String {
        val name = model.orEmpty().lowercase()
        return listOf("opus", "sonnet", "haiku").firstOrNull { it in name } ?: "default"
    }

    fun modeLabel(id: String?, resources: Resources): String? =
        id?.let { mode -> permissionModes.firstOrNull { it.id == mode }?.title(resources) ?: mode }
    fun effortLabel(id: String?, resources: Resources): String? = id?.let { effort ->
        (effortLevels.firstOrNull { it.id == effort } ?: codexEffortLevels.firstOrNull { it.id == effort })?.title(resources) ?: effort
    }
}

/**
 * Claude Code's ultracode switch (#582; docs/protocol/protocol.md "Claude Code ultracode", the web's lib/ultracode.ts and
 * iOS `Ultracode`): every task runs as a multi-agent workflow. Its own switch under the thinking effort, for the
 * session's current process only, off by default and never remembered.
 */
internal object Ultracode {
    /** The hub takes the switch (hello `features`): an older hub never shows it. */
    const val FEATURE = "ultracode"
    /** A proper name, never translated (docs/protocol/i18n.md). */
    const val NAME = "ultracode"

    /** The chat offers it: the hub has it, a Claude Code agent of the user's (not a Persona conversation, no read-only source) that runs and takes messages. */
    fun offered(session: Session?, features: Set<String>): Boolean =
        session != null && FEATURE in features && !session.assistant && !session.hasSource && session.canChangeModes

    /** The node saw it on (unknown is not on) and the process still runs: a resume starts it off. */
    fun on(session: Session): Boolean = session.ultracode == true && !session.assistant && session.isClaude && session.isLive

    /**
     * A switch this phone asked for: the node answers by pushing the session, sometimes after the request returns, and a
     * queued change (`ultracode_pending`) says it waits, not what for. While either holds, the switch stands where it was put.
     * [answered]: the session as drawn when the request came back; the push with the result has not come while it still is.
     */
    data class Ask(val on: Boolean, val sending: Boolean = true, val answered: Session? = null)

    /** Where the switch stands: what was asked while it is on its way, otherwise what the node saw. */
    fun shownOn(session: Session, ask: Ask?): Boolean = when {
        ask == null -> session.ultracode == true
        ask.sending || session.ultracodePending -> ask.on
        ask.answered == session && session.ultracode != ask.on && session.ultracodeNote.isNullOrBlank() -> ask.on
        else -> session.ultracode == true
    }

    /** An ask that no longer says anything: the session moved past it. */
    fun spent(session: Session, ask: Ask): Boolean = !ask.sending && !session.ultracodePending && ask.answered != session

    /**
     * The line under the switch: that a change waits for the turn (one this phone asked for says "Applies next turn"), the
     * node's reason, or what it does. The reason does not gray the switch: many clear elsewhere, the rest the node refuses again at once.
     */
    fun line(session: Session, ask: Ask?, resources: Resources): String {
        val note = session.ultracodeNote?.trim().orEmpty()
        if (session.ultracodePending) {
            val queued = resources.getString(if (ask != null) R.string.chat_ultracode_pending else R.string.chat_ultracode_pending_other)
            return if (note.isEmpty()) queued else queued + resources.getString(R.string.chat_modes_hint_separator) + note
        }
        return note.ifEmpty { resources.getString(R.string.chat_ultracode_subtitle) }
    }
}

/**
 * The switch under the thinking effort, with [Ultracode.line] under it in full: its last part ("this session only") or
 * the node's reason is what the user must read, and Glass's narrower text column or a large font needs a third line.
 */
@Composable
internal fun UltracodeSwitchRow(session: Session, ask: Ultracode.Ask?, enabled: Boolean, onChange: (Boolean) -> Unit) =
    JSwitchRow(Ultracode.NAME, Ultracode.shownOn(session, ask), onChange, subtitle = Ultracode.line(session, ask, LocalResources.current),
        leadingIcon = WorkflowIcon, enabled = enabled, subtitleLines = Int.MAX_VALUE)

/** Non-default settings and the current GPT model in the existing hint above the composer. */
internal fun chatModeHint(session: Session?, resources: Resources, includeModes: Boolean = true): String? = listOfNotNull(
    session?.permissionMode?.takeIf { includeModes && it != "default" }?.let { SessionModes.modeLabel(it, resources) },
    session?.effort?.takeIf { includeModes && it != "high" }?.let {
        resources.getString(R.string.chat_modes_effort_hint, SessionModes.effortLabel(it, resources))
    },
    session?.takeIf { includeModes && Ultracode.on(it) }?.let { Ultracode.NAME },
    // Only GPT ids get a model hint: other models keep the composer's existing presentation.
    session?.model?.let { id -> modelName(id).takeUnless { it == id } },
).takeIf { it.isNotEmpty() }?.joinToString(resources.getString(R.string.chat_modes_hint_separator))

@Composable
internal fun chatModeHint(session: Session?, includeModes: Boolean = true): String? =
    chatModeHint(session, LocalResources.current, includeModes)

/**
 * The composer's "Mode" button (iOS SessionModeBar): permission mode and effort change on the running session; for
 * Claude in a terminal pane the model too, which goes out as `/model <alias>` ([onPickModel], the same path as the
 * quick messages). Claude's sessions get it (callers check [canChangeModes]), and a Codex thread on its app-server
 * with its effort alone, among its own levels ([canChangeEffortOnly]); all-allow, which a running session cannot pick,
 * stays listed and marked while it is on.
 */
@Composable
internal fun ChatModeBar(
    session: Session?, state: ChatChromeState, online: Boolean, onOverlay: (Boolean) -> Unit = {},
    onPickModel: ((String) -> Unit)? = null,
    /**
     * /model in the "/" list asked for the sheet, where the model is picked: opened, then [onOpenHandled] clears the
     * request, so the bar coming back (after a question card hid the composer) does not open it again.
     */
    openRequested: Boolean = false,
    onOpenHandled: () -> Unit = {},
) {
    var open by remember { mutableStateOf(false) }
    LaunchedEffect(openRequested) { if (openRequested) { open = true; onOpenHandled() } }
    val resources = LocalResources.current
    LaunchedEffect(open) { onOverlay(open) }
    DisposableEffect(Unit) { onDispose { onOverlay(false) } }
    val effortOnly = session?.canChangeEffortOnly == true
    val editable = session != null && state.available && online && (session.canChangeModes || effortOnly)
    val bypass = session?.permissionMode == SessionModes.BYPASS
    val ultracodeOffered = Ultracode.offered(session, LocalHubFeatures.current)
    var ultracodeAsk by remember(session?.key) { mutableStateOf<Ultracode.Ask?>(null) }
    val latest by rememberUpdatedState(session)
    LaunchedEffect(session, ultracodeAsk) { ultracodeAsk?.let { ask -> if (session != null && Ultracode.spent(session, ask)) ultracodeAsk = null } }
    JIconButton(PhIcons.SlidersHorizontal, stringResource(if (bypass) R.string.chat_modes_button_bypass else R.string.chat_modes_button), { open = true },
        tint = if (bypass) PersonaTheme.palette.cautionText else null)
    if (open) JSheet(onDismiss = { open = false }, title = stringResource(R.string.chat_modes_title), scrollable = true) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (!editable) Text(stringResource(if (session?.canChangeModes == false && !effortOnly) R.string.chat_modes_locked else R.string.chat_modes_offline),
                style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            if (session != null) {
                if (!effortOnly) {
                    JSectionHeader(stringResource(R.string.chat_modes_section_permissions))
                    SessionModes.switchable(session.permissionMode).forEach { option ->
                        val current = option.id == session.permissionMode
                        DecisionOption(option.title(resources), {
                            open = false
                            if (!current) state.run(session, ChatAction.Modes(permissionMode = option.id))
                        }, detail = option.detail(resources), selected = current, enabled = editable)
                    }
                }
                JSectionHeader(stringResource(R.string.chat_modes_section_effort))
                SessionModes.effortOptions(session).forEach { option ->
                    JRow(option.title(resources), subtitle = option.detail(resources), trailing = { JRadio(option.id == session.effort, null) }, enabled = editable,
                        onClick = { open = false; if (option.id != session.effort) state.run(session, ChatAction.Modes(effort = option.id)) })
                }
                // Applies at once, its own request (never with the effort); the sheet stays open to show what the node says.
                if (ultracodeOffered) UltracodeSwitchRow(session, ultracodeAsk, editable) { on ->
                    ultracodeAsk = Ultracode.Ask(on)
                    state.run(session, ChatAction.Modes(ultracode = on)) { ok ->
                        ultracodeAsk = if (ok) Ultracode.Ask(on, sending = false, answered = latest) else null
                    }
                }
                // /model types into Claude's pane: Claude in a terminal engine only (Codex and compatible variants pick in a menu the phone can't drive).
                if (onPickModel != null && session.canTypeClaudeCommands) {
                    val current = SessionModes.claudeModelId(session.model)
                    JSectionHeader(stringResource(R.string.chat_modes_section_model))
                    SessionModes.claudeModels.forEach { option ->
                        JRow(option.title(resources), subtitle = option.detail(resources), trailing = { JRadio(option.id == current, null) }, enabled = editable,
                            onClick = { open = false; if (option.id != current) onPickModel(option.id) })
                    }
                    Text(stringResource(R.string.chat_modes_model_note),
                        style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
                }
            }
        }
    }
}
