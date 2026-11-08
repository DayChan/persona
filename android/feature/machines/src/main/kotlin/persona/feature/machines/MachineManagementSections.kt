package persona.feature.machines

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.Machine
import persona.core.contracts.api.AgentLauncher
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.engines.Engines

/** What the machine card says about its personad build, with "Redeploy" while it differs from the hub's (iOS `DeployRow`). */
@Composable
internal fun MachineDeployLine(machine: Machine, starting: Boolean, onDeploy: (() -> Unit)?) {
    val busy = machine.deploying || starting
    val text = when {
        busy -> stringResource(R.string.machines_deploying)
        !machine.deployError.isNullOrBlank() -> stringResource(R.string.machines_deploy_failed_last, machine.deployError.orEmpty())
        else -> stringResource(R.string.machines_node_mismatch)
    }
    JInlineStatus(
        text, Modifier.padding(top = Tokens.Space.s),
        tone = when { busy -> InlineTone.Busy; machine.deployError.isNullOrBlank() -> InlineTone.Caution; else -> InlineTone.Failed },
        action = if (busy || onDeploy == null || !machine.online) null else ({
            JButton(stringResource(R.string.machines_redeploy), onDeploy, kind = JButtonKind.Secondary, size = JButtonSize.Small)
        }),
    )
}

/** Whether the card shows the deploy line: a node that is not the hub's build, or one being replaced. */
internal fun showsDeploy(machine: Machine?): Boolean = machine != null && (machine.nodeMismatch == true || machine.deploying)

/**
 * The machine page's controls: its engine (opens the choice), "Also show botmux sessions" right under it when the hub has the
 * switch ([botmux] non-null; flipped in place, like the web page's header pill, #2842) and "Reconnect", with how the hub
 * reaches it.
 */
internal fun LazyListScope.machineControls(
    machine: Machine,
    reconnecting: Boolean,
    /** False while the manager it goes through is offline (several managers): no Reconnect row then, as on the web. */
    canReconnect: Boolean = true,
    onEngine: () -> Unit,
    onReconnect: () -> Unit,
    botmux: Boolean? = null,
    botmuxSaving: Boolean = false,
    onBotmux: (Boolean) -> Unit = {},
    /** Move to… with several managers (the manager it is with now as the value); null: no row, as with one manager. */
    move: MachineMoveRow? = null,
    /** Rename, on a hub that renames computers (feature `machine_rename`); null: no row. */
    rename: MachineRenameRow? = null,
) {
    val rows = 1 + (if (rename != null) 1 else 0) + (if (botmux != null) 1 else 0) + (if (move != null) 1 else 0) + (if (canReconnect) 1 else 0)
    val first = if (rename != null) 1 else 0
    item("controls-title") { JSectionHeader(stringResource(R.string.machines_controls_title)) }
    if (rename != null) item("rename") {
        JGroupItem(GroupPosition.of(0, rows)) {
            JRow(stringResource(R.string.machines_rename), leadingIcon = PhIcons.PencilSimple, value = machine.displayName, enabled = !rename.busy,
                onClick = rename.onClick, trailing = if (rename.busy) ({ JSpinner() }) else null)
        }
    }
    item("engine") {
        JGroupItem(GroupPosition.of(first, rows)) {
            JRow(stringResource(R.string.machines_engine), leadingIcon = PhIcons.TerminalWindow, value = Engines.currentName(machine), onClick = onEngine)
        }
    }
    if (botmux != null) item("botmux") {
        JGroupItem(GroupPosition.of(first + 1, rows)) {
            JSwitchRow(stringResource(R.string.machines_botmux), botmux, onBotmux, leadingIcon = PhIcons.Stack, enabled = !botmuxSaving)
        }
    }
    if (move != null) item("move") {
        JGroupItem(GroupPosition.of(first + if (botmux != null) 2 else 1, rows)) {
            JRow(stringResource(R.string.machines_move), leadingIcon = PhIcons.Robot, value = move.current, enabled = !move.busy,
                onClick = move.onClick, trailing = if (move.busy) ({ JSpinner() }) else null)
        }
    }
    if (canReconnect) item("reconnect") {
        JGroupItem(GroupPosition.of(rows - 1, rows)) {
            JRow(stringResource(if (reconnecting) R.string.machines_reconnecting else R.string.machines_reconnect), leadingIcon = PhIcons.ArrowClockwise, enabled = !reconnecting,
                onClick = onReconnect, chevron = false, trailing = if (reconnecting) ({ JSpinner() }) else null)
        }
    }
    item("controls-note") {
        Column(Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            MachineCaption(stringResource(if (botmux == null) R.string.machines_engine_note else R.string.machines_botmux_note))
            MachineCaption(if (machine.kind == "local") stringResource(R.string.machines_local_note)
                else stringResource(R.string.machines_ssh_note, machine.sshHost?.takeIf { it.isNotBlank() } ?: machine.id))
        }
    }
}

/** The hub's feature for renaming computers (docs/protocol/protocol.md "Renaming a computer"). */
internal const val MachineRenameFeature = "machine_rename"

/** The Rename row: whether a rename is on its way. */
internal data class MachineRenameRow(val busy: Boolean, val onClick: () -> Unit)

/**
 * Rename: what the hub calls the computer (docs/protocol/protocol.md "Renaming a computer"); empty is its id again.
 */
@Composable
internal fun RenameMachineDialog(machine: Machine, onRename: (String) -> Unit, onDismiss: () -> Unit) {
    var name by rememberSaveable(machine.id) { mutableStateOf(machine.name.takeIf { it != machine.id }.orEmpty()) }
    val unchanged = name.trim() == machine.name.takeIf { it != machine.id }.orEmpty() || name.trim().length > 40
    JDialog(
        onDismiss = onDismiss, title = stringResource(R.string.machines_rename_title),
        confirm = stringResource(R.string.machines_rename_save) to { onRename(name); onDismiss() }, confirmEnabled = !unchanged,
    ) {
        JTextField(name, { name = it }, placeholder = machine.id, singleLine = true,
            supportingText = stringResource(R.string.machines_rename_hint))
    }
}

/** The Move to… row: the manager it is with now, and whether a move is on its way. */
internal data class MachineMoveRow(val current: String, val busy: Boolean, val onClick: () -> Unit)

/**
 * "Move to which manager": the installed managers, the default first, the one it is with checked and not offered again, an offline
 * one marked Offline (it takes the computer once it is back).
 */
@Composable
internal fun MoveToManagerSheet(roster: persona.core.contracts.api.ManagerRoster, current: String, onMove: (persona.core.contracts.api.HubManager) -> Unit, onDismiss: () -> Unit) {
    JActionSheet(roster.installed.map { m ->
        val name = m.name.ifBlank { m.id }
        JAction(if (m.online) name else stringResource(R.string.machines_move_offline, name), checked = m.id == current, enabled = m.id != current) { onMove(m) }
    }, onDismiss, title = stringResource(R.string.machines_move_title))
}

/**
 * "Launchers N": each launcher's name and the model it runs (the last part of a provider path). With [onLauncher] (the hub
 * manages extensions) a row opens the launcher's page, where extensions may draw a section for its gateway.
 */
internal fun LazyListScope.machineLaunchers(online: Boolean, state: MachineActionsState, onLauncher: ((String) -> Unit)? = null) {
    val launchers = state.launchers.orEmpty()
    item("launchers-title") { JSectionHeader(stringResource(R.string.machines_launchers), count = launchers.size.takeIf { online && state.launchers != null }) }
    when {
        !online -> item("launchers-offline") { MachineCaption(stringResource(R.string.machines_launchers_offline), Modifier.padding(horizontal = rowPadding)) }
        state.launchersError != null && launchers.isEmpty() -> item("launchers-error") {
            JInlineStatus(stringResource(R.string.machines_launchers_error, state.launchersError.text()), tone = InlineTone.Failed)
        }
        state.launchersLoading && state.launchers == null -> item("launchers-loading") { JLoading() }
        state.launchers != null && launchers.isEmpty() -> item("launchers-empty") {
            MachineCaption(stringResource(R.string.machines_launchers_empty), Modifier.padding(horizontal = rowPadding))
        }
    }
    itemsIndexed(launchers, key = { _, it -> "launcher-${it.name}" }, contentType = { _, _ -> "launcher" }) { index, launcher ->
        val model = launcherModel(launcher)
        JGroupItem(GroupPosition.of(index, launchers.size)) {
            JRow(chevron = onLauncher != null, onClick = onLauncher?.let { open -> { open(launcher.name) } }, trailing = if (model == null) null else ({
                Text(model, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
            })) {
                Text(launcher.name, style = PersonaTheme.type.code, color = PersonaTheme.palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        }
    }
}

/** "claude-opus-4" and "anthropic/claude-opus-4" both read as the last part of the path (iOS `Fmt.model`). */
internal fun launcherModel(launcher: AgentLauncher): String? = launcher.model?.takeIf { it.isNotBlank() }?.substringAfterLast('/')

@Composable
internal fun MachineCaption(text: String, modifier: Modifier = Modifier) {
    Text(text, modifier, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
}
