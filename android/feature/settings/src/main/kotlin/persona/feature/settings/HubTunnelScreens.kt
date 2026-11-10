package persona.feature.settings

import android.content.ClipData
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.ClipEntry
import androidx.compose.ui.platform.LocalClipboard
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.launch
import persona.core.contracts.api.HubTunnelInfo
import persona.core.contracts.api.HubTunnelSettings
import persona.core.contracts.api.LocalProxyRoute
import persona.core.contracts.api.SshRoute
import persona.core.contracts.api.TunnelKind
import persona.core.contracts.api.TunnelProblem
import persona.core.contracts.api.TunnelState
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons

/**
 * Settings › Hubs › SSH tunnel or Local proxy of one paired hub (docs/operations/network.md "Connecting to hub through SSH",
 * "Connecting to hub through a local SOCKS5 proxy"; iOS `SSHTunnelView`, `LocalProxyView`). Back once the hub is unpaired.
 */
@Composable
internal fun HubTunnelPage(tunnels: HubTunnelSettings, hubId: String, kind: TunnelKind, onBack: () -> Unit) {
    val hubs by tunnels.hubs.collectAsState()
    val info = hubs.firstOrNull { it.hubId == hubId }
    var seen by remember(hubId) { mutableStateOf(info != null) }
    LaunchedEffect(info == null) { if (info == null && seen) onBack() else if (info != null) seen = true }
    if (info == null) {
        JListPage(title = stringResource(if (kind == TunnelKind.Ssh) R.string.settings_tunnel_ssh else R.string.settings_tunnel_proxy),
            onBack = onBack, largeTitle = false) {}
        return
    }
    when (kind) {
        TunnelKind.Ssh -> SshTunnelPage(tunnels, info, onBack)
        TunnelKind.Proxy -> LocalProxyPage(tunnels, info, onBack)
    }
}

@Composable
private fun SshTunnelPage(tunnels: HubTunnelSettings, info: HubTunnelInfo, onBack: () -> Unit) {
    val res = LocalResources.current
    val feedback = LocalAppFeedback.current
    val clipboard = LocalClipboard.current
    val scope = rememberCoroutineScope()
    val model = remember(tunnels, info.hubId) { SshTunnelModel(tunnels, info.hubId, info.ssh) }
    val form by model.state.collectAsState()
    var deleting by remember { mutableStateOf(false) }
    LaunchedEffect(model) { model.loadKey() }
    fun copy(text: String) = scope.launch {
        clipboard.setClipEntry(ClipEntry(ClipData.newPlainText(res.getString(R.string.settings_tunnel_device_key), text)))
        feedback?.notice(res.getString(R.string.settings_tunnel_copied))
    }
    JListPage(title = stringResource(R.string.settings_tunnel_ssh), subtitle = info.name, onBack = onBack, largeTitle = false) {
        item("intro") { SettingsCaption(stringResource(R.string.settings_tunnel_ssh_intro), Modifier.padding(top = Tokens.Space.s)) }
        form.saved?.takeIf { it.usable }?.let { saved -> item("status") { TunnelStatusSection(info, TunnelKind.Ssh, info.sshState, ssh = saved) } }
        item("key") {
            FormSection(stringResource(R.string.settings_tunnel_device_key)) {
                val key = form.deviceKey
                if (key != null) Column {
                    JGroupItem(GroupPosition(first = true, last = false)) {
                        JRow(modifier = Modifier.padding(vertical = Tokens.Space.xs)) {
                            Text(key.publicKey, style = PersonaTheme.type.codeSmall, color = PersonaTheme.palette.ink2,
                                maxLines = 1, overflow = TextOverflow.MiddleEllipsis)
                        }
                    }
                    JGroupItem(GroupPosition(first = false, last = false)) {
                        JRow(stringResource(R.string.settings_tunnel_copy_key), leadingIcon = PhIcons.Copy, onClick = { copy(key.publicKey) }, chevron = false)
                    }
                    JGroupItem(GroupPosition(first = false, last = true)) {
                        JRow(stringResource(R.string.settings_tunnel_copy_line), leadingIcon = PhIcons.Key,
                            onClick = { form.authorizedKeysLine?.let(::copy) }, chevron = false)
                    }
                } else if (form.keyUnavailable) {
                    JNotice(stringResource(R.string.settings_tunnel_key_unavailable), status = Status.NeedsYou)
                }
                SettingsCaption(stringResource(R.string.settings_tunnel_key_footer))
            }
        }
        item("server") {
            FormSection(stringResource(R.string.settings_tunnel_ssh_server)) {
                JTextField(form.host, { model.edit(host = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_host),
                    placeholder = "ssh.example.com", singleLine = true, keyboardOptions = ASCII)
                JTextField(form.port, { model.edit(port = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_port),
                    placeholder = "22", singleLine = true, keyboardOptions = NUMBER)
                JTextField(form.user, { model.edit(user = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_username),
                    placeholder = stringResource(R.string.settings_tunnel_username_hint), singleLine = true, keyboardOptions = ASCII)
                SettingsCaption(stringResource(R.string.settings_tunnel_ssh_server_footer))
            }
        }
        item("target") {
            FormSection(stringResource(R.string.settings_tunnel_target)) {
                JTextField(form.targetHost, { model.edit(targetHost = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_address),
                    placeholder = SshRoute.DEFAULT_TARGET_HOST, singleLine = true, keyboardOptions = ASCII)
                JTextField(form.targetPort, { model.edit(targetPort = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_port),
                    placeholder = SshRoute.DEFAULT_TARGET_PORT.toString(), singleLine = true, keyboardOptions = NUMBER)
                SettingsCaption(stringResource(R.string.settings_tunnel_target_footer))
            }
        }
        form.pinned?.let { pinned -> item("fingerprint") {
            FormSection(stringResource(R.string.settings_tunnel_fingerprint)) {
                JGroupItem(GroupPosition.Single) {
                    JRow(pinned.hostKeyAlgorithm?.let { stringResource(R.string.settings_tunnel_verified_algorithm, it) } ?: stringResource(R.string.settings_tunnel_verified),
                        leadingIcon = PhIcons.ShieldCheck, subtitle = pinned.hostKey)
                }
                SettingsCaption(stringResource(R.string.settings_tunnel_fingerprint_footer))
            }
        } }
        item("always") {
            AlwaysSection(stringResource(R.string.settings_tunnel_ssh_always), form.always, !form.testing,
                stringResource(R.string.settings_tunnel_ssh_always_footer)) { on ->
                scope.launch { if (model.setAlways(on)) feedback?.notice(res.getString(R.string.settings_tunnel_ssh_saved)) }
            }
        }
        item("save") {
            SaveSection(form.problem, form.testing, form.canTest, stringResource(R.string.settings_tunnel_ssh_test_footer),
                deleteLabel = form.saved?.let { stringResource(R.string.settings_tunnel_ssh_delete) }, onDelete = { deleting = true }) {
                scope.launch { if (model.testAndSave()) feedback?.notice(res.getString(R.string.settings_tunnel_ssh_saved)) }
            }
        }
    }
    when (val question = form.question) {
        is TunnelProblem.HostKeyUnknown -> JDialog(onDismiss = model::dismissQuestion, title = stringResource(R.string.settings_tunnel_verify_title),
            text = stringResource(R.string.settings_tunnel_verify_body, question.key.algorithm, question.key.fingerprint),
            confirm = stringResource(R.string.settings_tunnel_trust_save) to {
                scope.launch { if (model.testAndSave(trust = question.key)) feedback?.notice(res.getString(R.string.settings_tunnel_ssh_saved)) }
                Unit
            })
        is TunnelProblem.HostKeyChanged -> JDialog(onDismiss = model::dismissQuestion, title = stringResource(R.string.settings_tunnel_changed_title),
            text = stringResource(R.string.settings_tunnel_changed_body, question.pinned, question.key.fingerprint), destructive = true,
            confirm = stringResource(R.string.settings_tunnel_trust_new) to {
                scope.launch { if (model.testAndSave(trust = question.key)) feedback?.notice(res.getString(R.string.settings_tunnel_ssh_saved)) }
                Unit
            })
        else -> Unit
    }
    if (deleting) JDialog(onDismiss = { deleting = false }, title = stringResource(R.string.settings_tunnel_ssh_delete_title),
        text = stringResource(R.string.settings_tunnel_ssh_delete_body), destructive = true,
        confirm = stringResource(R.string.settings_tunnel_delete) to {
            deleting = false
            scope.launch { if (model.delete()) onBack() }
            Unit
        })
}

@Composable
private fun LocalProxyPage(tunnels: HubTunnelSettings, info: HubTunnelInfo, onBack: () -> Unit) {
    val res = LocalResources.current
    val feedback = LocalAppFeedback.current
    val scope = rememberCoroutineScope()
    val model = remember(tunnels, info.hubId) { LocalProxyModel(tunnels, info.hubId, info.hubAddress, info.proxy) }
    val form by model.state.collectAsState()
    var deleting by remember { mutableStateOf(false) }
    JListPage(title = stringResource(R.string.settings_tunnel_proxy), subtitle = info.name, onBack = onBack, largeTitle = false) {
        form.saved?.takeIf { it.usable }?.let { saved ->
            item("status") { TunnelStatusSection(info, TunnelKind.Proxy, info.proxyState, proxy = saved) }
        }
        item("proxy") {
            FormSection(stringResource(R.string.settings_tunnel_socks)) {
                JTextField(form.host, { model.edit(host = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_host),
                    placeholder = LocalProxyRoute.DEFAULT_HOST, singleLine = true, keyboardOptions = ASCII)
                JTextField(form.port, { model.edit(port = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_port),
                    placeholder = stringResource(R.string.settings_tunnel_required), singleLine = true, keyboardOptions = NUMBER)
                if (form.remote) JNotice(stringResource(R.string.settings_tunnel_not_local), status = Status.NeedsYou, icon = PhIcons.Warning)
                SettingsCaption(stringResource(R.string.settings_tunnel_proxy_footer))
            }
        }
        item("login") {
            FormSection(stringResource(R.string.settings_tunnel_proxy_login)) {
                JTextField(form.username, { model.edit(username = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_username),
                    placeholder = stringResource(R.string.settings_tunnel_optional), singleLine = true, keyboardOptions = ASCII, autofill = false)
                JSecretField(form.password, { model.edit(password = it) }, stringResource(R.string.settings_tunnel_password), Modifier.fillMaxWidth())
                SettingsCaption(stringResource(R.string.settings_tunnel_login_footer))
            }
        }
        item("target") {
            FormSection(stringResource(R.string.settings_tunnel_hub_address)) {
                JTextField(form.targetHost, { model.edit(targetHost = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_address),
                    placeholder = LocalProxyRoute.defaultTargetHost(info.hubAddress), singleLine = true, keyboardOptions = ASCII)
                JTextField(form.targetPort, { model.edit(targetPort = it) }, Modifier.fillMaxWidth(), label = stringResource(R.string.settings_tunnel_port),
                    placeholder = LocalProxyRoute.defaultTargetPort(info.hubAddress).toString(), singleLine = true, keyboardOptions = NUMBER)
                SettingsCaption(stringResource(R.string.settings_tunnel_hub_address_footer))
            }
        }
        item("always") {
            AlwaysSection(stringResource(R.string.settings_tunnel_proxy_always), form.always, !form.testing,
                stringResource(R.string.settings_tunnel_proxy_always_footer)) { on ->
                scope.launch { if (model.setAlways(on)) feedback?.notice(res.getString(R.string.settings_tunnel_proxy_saved)) }
            }
        }
        item("save") {
            SaveSection(form.problem, form.testing, form.canTest, stringResource(R.string.settings_tunnel_proxy_test_footer),
                deleteLabel = form.saved?.let { stringResource(R.string.settings_tunnel_proxy_delete) }, onDelete = { deleting = true }) {
                scope.launch { if (model.testAndSave()) feedback?.notice(res.getString(R.string.settings_tunnel_proxy_saved)) }
            }
        }
    }
    if (deleting) JDialog(onDismiss = { deleting = false }, title = stringResource(R.string.settings_tunnel_proxy_delete_title),
        text = stringResource(R.string.settings_tunnel_proxy_delete_body), destructive = true,
        confirm = stringResource(R.string.settings_tunnel_delete) to {
            deleting = false
            scope.launch { if (model.delete()) onBack() }
            Unit
        })
}

@Composable
private fun TunnelStatusSection(info: HubTunnelInfo, kind: TunnelKind, state: TunnelState?, ssh: SshRoute? = null, proxy: LocalProxyRoute? = null) {
    FormSection(stringResource(R.string.settings_tunnel_status)) {
        val up = info.routed == kind && info.online
        JGroupItem(GroupPosition.Single) {
            JRow(stringResource(tunnelStatus(info, kind)), leadingIcon = if (kind == TunnelKind.Ssh) PhIcons.Terminal else PhIcons.ShareNetwork,
                trailing = { JStatusDot(if (up) Status.Unread else if (info.routed == kind) Status.Idle else Status.Offline) })
        }
        (state as? TunnelState.Down)?.let { down ->
            SettingsCaption(stringResource(R.string.settings_tunnel_last_failure,
                tunnelProblemText(down.problem, ssh = ssh, proxy = proxy, hubAddress = info.hubAddress).text()))
        }
    }
}

@Composable
private fun AlwaysSection(title: String, checked: Boolean, enabled: Boolean, footer: String, onChange: (Boolean) -> Unit) {
    Column(Modifier.padding(top = Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JGroupItem(GroupPosition.Single) { JSwitchRow(title, checked, onChange, enabled = enabled) }
        SettingsCaption(footer)
    }
}

@Composable
private fun SaveSection(problem: ResText?, testing: Boolean, canTest: Boolean, footer: String, deleteLabel: String?, onDelete: () -> Unit, onTest: () -> Unit) {
    Column(Modifier.padding(top = Tokens.Space.l, bottom = Tokens.Space.xl), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        problem?.let { JNotice(it.text(), status = Status.NeedsYou) }
        JButton(stringResource(R.string.settings_tunnel_test_save), onTest, Modifier.fillMaxWidth(), loading = testing,
            loadingText = stringResource(R.string.settings_tunnel_testing), enabled = canTest)
        SettingsCaption(footer)
        if (deleteLabel != null) JButton(deleteLabel, onDelete, Modifier.fillMaxWidth().padding(top = Tokens.Space.m),
            kind = JButtonKind.Destructive, enabled = !testing)
    }
}

@Composable
private fun FormSection(title: String, content: @Composable () -> Unit) {
    Column(Modifier.padding(top = Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JSectionHeader(title)
        content()
    }
}

private val ASCII = KeyboardOptions(keyboardType = KeyboardType.Ascii, autoCorrectEnabled = false)
private val NUMBER = KeyboardOptions(keyboardType = KeyboardType.Number, autoCorrectEnabled = false)
