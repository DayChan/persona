package persona.feature.settings

import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.annotation.StringRes
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

/**
 * Settings › hub (docs/android/android.md "Multiple hubs"): the phone's hub groups. A group is hubs linked by the hub mesh; the lists
 * show every group together, the hub-level pages (Persona, inbox, hub settings) show the selected one. Adding a hub
 * puts it into the group its mesh says, else it starts a new group. Each paired hub has its SSH tunnel and local proxy rows.
 */
@Composable
internal fun HubsScreen(hubs: HubGroups, onAdd: (() -> Unit)?, onBack: () -> Unit, footer: @Composable () -> Unit = {},
    tunnels: HubTunnelSettings? = null, onTunnel: (hubId: String, kind: TunnelKind) -> Unit = { _, _ -> }) {
    val groups by hubs.groups.collectAsState()
    val tunnelInfos by (tunnels ?: HubTunnelSettings.None).hubs.collectAsState()
    val scope = rememberCoroutineScope()
    var menu by remember { mutableStateOf<HubGroupInfo?>(null) }
    var hubMenu by remember { mutableStateOf<Pair<HubGroupInfo, HubMemberInfo>?>(null) }
    var renaming by remember { mutableStateOf<HubGroupInfo?>(null) }
    var removing by remember { mutableStateOf<HubGroupInfo?>(null) }
    var removingHub by remember { mutableStateOf<Pair<HubGroupInfo, HubMemberInfo>?>(null) }
    var busy by remember { mutableStateOf(false) }
    var message by remember { mutableStateOf<Int?>(null) } // a string resource
    fun run(@StringRes failure: Int, block: suspend () -> Unit) {
        if (busy) return
        busy = true
        scope.launch {
            try { block(); message = null }
            catch (error: CancellationException) { throw error }
            catch (_: Exception) { message = failure }
            finally { busy = false }
        }
    }
    JListPage(title = stringResource(R.string.settings_hubs_title), onBack = onBack) {
        item {
            SettingsCaption(stringResource(if (groups.size > 1) R.string.settings_hubs_caption_groups else R.string.settings_hubs_caption_single),
                Modifier.padding(top = Tokens.Space.m))
        }
        if (groups.isEmpty()) item { JEmptyState(stringResource(R.string.settings_hubs_empty_title), stringResource(R.string.settings_hubs_empty_body)) }
        groups.forEach { group ->
            item(key = "head-" + group.id) { JSectionHeader(group.name) }
            val withTunnels = group.hubs.filter { it.paired }.mapNotNull { hub -> tunnelInfos.firstOrNull { it.hubId == hub.hubId } }
            val rows = group.hubs.size + 1 + 2 * withTunnels.size
            item(key = "state-" + group.id) {
                JGroupItem(GroupPosition.of(0, rows)) {
                    JRow(if (group.state == ConnectionState.Online) stringResource(R.string.settings_hubs_connected) else group.state.label(), leadingIcon = PhIcons.TreeStructure,
                        subtitle = groupStateText(group, groups.size > 1), onClick = { menu = group })
                }
            }
            group.hubs.forEachIndexed { index, hub ->
                item(key = "hub-" + group.id + "-" + hub.hubId) {
                    JGroupItem(GroupPosition.of(index + 1, rows)) {
                        JRow(hub.name, leadingIcon = PhIcons.HardDrives, subtitle = hubText(hub),
                            value = if (hub.connected) stringResource(R.string.settings_hubs_in_use) else null,
                            onClick = if (hub.paired && group.hubs.count { it.paired } > 1) ({ hubMenu = group to hub }) else null)
                    }
                }
            }
            withTunnels.forEachIndexed { index, info ->
                listOf(TunnelKind.Ssh, TunnelKind.Proxy).forEachIndexed { offset, kind ->
                    item(key = "tunnel-" + group.id + "-" + info.hubId + "-" + kind) {
                        JGroupItem(GroupPosition.of(group.hubs.size + 1 + 2 * index + offset, rows)) {
                            JRow(tunnelRowTitle(kind, info.name.takeIf { withTunnels.size > 1 }),
                                leadingIcon = if (kind == TunnelKind.Ssh) PhIcons.Terminal else PhIcons.ShareNetwork,
                                value = stringResource(tunnelRowValue(info, kind)), onClick = { onTunnel(info.hubId, kind) })
                        }
                    }
                }
            }
        }
        if (onAdd != null) item {
            JButton(stringResource(R.string.settings_hubs_add), onAdd, Modifier.fillMaxWidth().padding(top = Tokens.Space.xl), icon = PhIcons.Plus, enabled = !busy)
        }
        message?.let { id -> item { JNotice(stringResource(id), Modifier.padding(top = Tokens.Space.m), status = Status.NeedsYou) } }
        item { footer() }
    }
    menu?.let { group ->
        JActionSheet(listOfNotNull(
            JAction(stringResource(R.string.settings_hubs_use_for), PhIcons.Sparkle, checked = group.selected, enabled = !group.selected) {
                run(R.string.settings_hubs_failed_switch) { hubs.select(group.id) }
            }.takeIf { groups.size > 1 },
            JAction(stringResource(R.string.settings_hubs_rename), PhIcons.PencilSimple) { renaming = group },
            JAction(stringResource(R.string.settings_hubs_disconnect_group), PhIcons.Trash, destructive = true, separatorBefore = true) { removing = group },
        ), onDismiss = { menu = null }, title = group.name)
    }
    hubMenu?.let { (group, hub) ->
        JActionSheet(listOf(JAction(stringResource(R.string.settings_hubs_disconnect_hub), PhIcons.Trash, destructive = true) { removingHub = group to hub }),
            onDismiss = { hubMenu = null }, title = hub.name)
    }
    renaming?.let { group ->
        var name by remember(group.id) { mutableStateOf(group.name) }
        JDialog(onDismiss = { renaming = null }, title = stringResource(R.string.settings_hubs_rename_title),
            confirm = stringResource(R.string.settings_hubs_save) to { renaming = null; run(R.string.settings_hubs_failed_rename) { hubs.rename(group.id, name) } },
            confirmEnabled = name.isNotBlank()) {
            JTextField(name, { name = it }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_hubs_name), singleLine = true)
            SettingsCaption(stringResource(R.string.settings_hubs_rename_note), Modifier.padding(top = Tokens.Space.s))
        }
    }
    removing?.let { group ->
        JDialog(onDismiss = { removing = null }, title = stringResource(R.string.settings_hubs_disconnect_title, group.name),
            text = group.hubs.count { it.paired }.let { pluralStringResource(R.plurals.settings_hubs_disconnect_group_body, it, it) },
            destructive = true, confirmEnabled = !busy,
            confirm = stringResource(R.string.settings_hubs_disconnect) to { removing = null; run(R.string.settings_hubs_failed_disconnect) { hubs.removeGroup(group.id) } })
    }
    removingHub?.let { (group, hub) ->
        JDialog(onDismiss = { removingHub = null }, title = stringResource(R.string.settings_hubs_disconnect_title, hub.name),
            text = stringResource(R.string.settings_hubs_disconnect_hub_body, group.name),
            destructive = true, confirmEnabled = !busy,
            confirm = stringResource(R.string.settings_hubs_disconnect) to { removingHub = null; run(R.string.settings_hubs_failed_disconnect) { hubs.removeHub(group.id, hub.hubId) } })
    }
}

@Composable
private fun tunnelRowTitle(kind: TunnelKind, hub: String?): String = when {
    kind == TunnelKind.Ssh && hub != null -> stringResource(R.string.settings_tunnel_ssh_of, hub)
    kind == TunnelKind.Ssh -> stringResource(R.string.settings_tunnel_ssh)
    hub != null -> stringResource(R.string.settings_tunnel_proxy_of, hub)
    else -> stringResource(R.string.settings_tunnel_proxy)
}

@Composable
private fun groupStateText(group: HubGroupInfo, several: Boolean): String {
    val paired = group.hubs.count { it.paired }
    val mesh = group.hubs.size - paired
    val hubs = if (mesh == 0) pluralStringResource(R.plurals.settings_hubs_count, paired, paired)
        else stringResource(R.string.settings_hubs_count_mesh, paired, mesh)
    return if (several && group.selected) stringResource(R.string.settings_hubs_selected, hubs) else hubs
}

@Composable
private fun hubText(hub: HubMemberInfo): String? = when {
    !hub.paired && hub.online == false -> stringResource(R.string.settings_hubs_mesh_offline)
    !hub.paired -> stringResource(R.string.settings_hubs_mesh)
    hub.online == false -> stringResource(R.string.settings_hubs_offline)
    else -> null
}
