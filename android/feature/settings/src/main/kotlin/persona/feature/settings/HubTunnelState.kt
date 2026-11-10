package persona.feature.settings

import androidx.annotation.StringRes
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import persona.core.contracts.api.HubTunnelInfo
import persona.core.contracts.api.HubTunnelSettings
import persona.core.contracts.api.LocalProxyRoute
import persona.core.contracts.api.SshDeviceKey
import persona.core.contracts.api.SshHostKey
import persona.core.contracts.api.SshRoute
import persona.core.contracts.api.TunnelException
import persona.core.contracts.api.TunnelKind
import persona.core.contracts.api.TunnelProblem
import persona.core.hub.tunnel.Socks5Handshake

/** Settings › Hubs › SSH tunnel as typed (iOS `SSHTunnelView`); empty ports and an empty target take the defaults. */
internal data class SshTunnelForm(
    val saved: SshRoute? = null,
    val host: String = "",
    val port: String = "",
    val user: String = "",
    val targetHost: String = "",
    val targetPort: String = "",
    val always: Boolean = false,
    val testing: Boolean = false,
    val problem: ResText? = null,
    /** [TunnelProblem.HostKeyUnknown] or [TunnelProblem.HostKeyChanged]: the user confirms the server's key before it is saved. */
    val question: TunnelProblem? = null,
    val deviceKey: SshDeviceKey? = null,
    val keyUnavailable: Boolean = false,
) {
    /** Null while a field is missing or out of range. A pinned key carries over only while the server stays the same. */
    val route: SshRoute? get() {
        val port = portOrDefault(port, 22) ?: return null
        val targetPort = portOrDefault(targetPort, SshRoute.DEFAULT_TARGET_PORT) ?: return null
        if (host.isBlank() || user.isBlank()) return null
        val typed = SshRoute(host.trim(), port, user.trim(), targetHost.trim().ifEmpty { SshRoute.DEFAULT_TARGET_HOST }, targetPort, always = always)
        return typed.copy(hostKey = pinned?.hostKey, hostKeyAlgorithm = pinned?.hostKeyAlgorithm)
    }

    /** The saved route while the server typed is still its server: its confirmed key holds. */
    val pinned: SshRoute? get() = saved?.takeIf {
        !it.hostKey.isNullOrEmpty() && it.host.trim() == host.trim() && it.port == portOrDefault(port, 22)
    }

    val canTest: Boolean get() = route != null && !testing

    /** The authorized_keys line for the target as typed. */
    val authorizedKeysLine: String? get() = deviceKey?.authorizedKeysLine(
        targetHost.trim().ifEmpty { SshRoute.DEFAULT_TARGET_HOST },
        targetPort.trim().toIntOrNull() ?: SshRoute.DEFAULT_TARGET_PORT)

    companion object {
        /** Defaults stay empty, shown as the fields' placeholders (iOS `SSHTunnelView`). */
        fun of(saved: SshRoute?) = SshTunnelForm(saved, host = saved?.host.orEmpty(),
            port = saved?.port?.takeUnless { it == 22 }?.toString().orEmpty(), user = saved?.user.orEmpty(),
            targetHost = saved?.targetHost?.takeUnless { it == SshRoute.DEFAULT_TARGET_HOST }.orEmpty(),
            targetPort = saved?.targetPort?.takeUnless { it == SshRoute.DEFAULT_TARGET_PORT }?.toString().orEmpty(),
            always = saved?.always ?: false)
    }
}

/** Settings › Hubs › Local proxy as typed (iOS `LocalProxyView`); an empty host is this device. */
internal data class LocalProxyForm(
    val saved: LocalProxyRoute? = null,
    val host: String = "",
    val port: String = "",
    val username: String = "",
    val password: String = "",
    val targetHost: String = "",
    val targetPort: String = "",
    val always: Boolean = false,
    val testing: Boolean = false,
    val problem: ResText? = null,
) {
    val route: LocalProxyRoute? get() {
        val port = portOrDefault(port, null) ?: return null
        val targetPort = if (targetPort.isBlank()) null else portOrDefault(targetPort, null) ?: return null
        return LocalProxyRoute(host.trim().ifEmpty { LocalProxyRoute.DEFAULT_HOST }, port, username.trim(), password,
            targetHost.trim().ifEmpty { null }, targetPort, always)
    }

    val canTest: Boolean get() = route != null && !testing

    /** The proxy typed is elsewhere: what goes through it crosses the network in the clear. */
    val remote: Boolean get() = host.isNotBlank() && !LocalProxyRoute(host = host, port = 1).onThisDevice

    override fun toString(): String = "LocalProxyForm($host:$port, login=${username.isNotEmpty()}, always=$always, testing=$testing)"

    companion object {
        fun of(saved: LocalProxyRoute?) = LocalProxyForm(saved,
            host = saved?.host?.takeUnless { it == LocalProxyRoute.DEFAULT_HOST }.orEmpty(), port = saved?.port?.toString().orEmpty(),
            username = saved?.username.orEmpty(), password = saved?.password.orEmpty(), targetHost = saved?.targetHost.orEmpty(),
            targetPort = saved?.targetPort?.toString().orEmpty(), always = saved?.always ?: false)
    }
}

/** Load → edit → test → save for a hub's SSH tunnel. */
internal class SshTunnelModel(private val api: HubTunnelSettings, private val hubId: String, saved: SshRoute?) {
    private val mutable = MutableStateFlow(SshTunnelForm.of(saved))
    val state = mutable.asStateFlow()

    suspend fun loadKey() {
        try {
            val key = api.deviceKey()
            mutable.update { it.copy(deviceKey = key, keyUnavailable = false) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { mutable.update { it.copy(keyUnavailable = true) } }
    }

    fun edit(host: String? = null, port: String? = null, user: String? = null, targetHost: String? = null, targetPort: String? = null) =
        mutable.update {
            it.copy(host = host ?: it.host, port = port ?: it.port, user = user ?: it.user,
                targetHost = targetHost ?: it.targetHost, targetPort = targetPort ?: it.targetPort, problem = null)
        }

    /** With nothing else changed on a saved tunnel the switch is saved at once; otherwise with the next test. */
    suspend fun setAlways(on: Boolean): Boolean {
        mutable.update { it.copy(always = on, problem = null) }
        val now = state.value
        val saved = now.saved?.takeIf { it.usable } ?: return false
        val switched = saved.copy(always = on)
        if (now.testing || now.route != switched) return false
        return save(switched)
    }

    /**
     * Signs in and opens the hub's address once, then saves the route with the key the server showed. A key not pinned
     * yet, or another one, is asked about first ([SshTunnelForm.question]); [trust] is the key the user confirmed.
     */
    suspend fun testAndSave(trust: SshHostKey? = null): Boolean {
        val now = state.value
        val typed = now.route ?: return false
        if (now.testing) return false
        val route = trust?.let { typed.copy(hostKey = it.fingerprint, hostKeyAlgorithm = it.algorithm) } ?: typed
        mutable.update { it.copy(testing = true, problem = null, question = null) }
        try {
            val seen = try { api.verifySsh(hubId, route) }
            catch (failure: TunnelException) {
                val problem = failure.problem
                if (problem is TunnelProblem.HostKeyUnknown || problem is TunnelProblem.HostKeyChanged) mutable.update { it.copy(question = problem) }
                else mutable.update { it.copy(problem = tunnelProblemText(problem, ssh = route)) }
                return false
            }
            return save(route.copy(hostKey = seen.fingerprint, hostKeyAlgorithm = seen.algorithm))
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) {
            mutable.update { it.copy(problem = ResText.of(R.string.settings_tunnel_save_failed)) }
            return false
        } finally { mutable.update { it.copy(testing = false) } }
    }

    fun dismissQuestion() = mutable.update { it.copy(question = null) }

    suspend fun delete(): Boolean = try {
        api.saveSsh(hubId, null)
        mutable.update { SshTunnelForm(deviceKey = it.deviceKey) }
        true
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) {
        mutable.update { it.copy(problem = ResText.of(R.string.settings_tunnel_save_failed)) }
        false
    }

    private suspend fun save(route: SshRoute): Boolean = try {
        api.saveSsh(hubId, route)
        mutable.update { it.copy(saved = route) }
        true
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) {
        mutable.update { it.copy(problem = ResText.of(R.string.settings_tunnel_save_failed)) }
        false
    }
}

/** Load → edit → test → save for a hub's local proxy. */
internal class LocalProxyModel(private val api: HubTunnelSettings, private val hubId: String, private val hubAddress: String, saved: LocalProxyRoute?) {
    private val mutable = MutableStateFlow(LocalProxyForm.of(saved))
    val state = mutable.asStateFlow()

    fun edit(host: String? = null, port: String? = null, username: String? = null, password: String? = null,
        targetHost: String? = null, targetPort: String? = null) = mutable.update {
        it.copy(host = host ?: it.host, port = port ?: it.port, username = username ?: it.username, password = password ?: it.password,
            targetHost = targetHost ?: it.targetHost, targetPort = targetPort ?: it.targetPort, problem = null)
    }

    /** With nothing else changed on a saved proxy the switch is saved at once; otherwise with the next test. */
    suspend fun setAlways(on: Boolean): Boolean {
        mutable.update { it.copy(always = on, problem = null) }
        val now = state.value
        val saved = now.saved?.takeIf { it.usable } ?: return false
        val switched = saved.copy(always = on)
        if (now.testing || now.route != switched) return false
        return save(switched)
    }

    /** Reaches the hub's health check through the proxy, then saves. */
    suspend fun testAndSave(): Boolean {
        val now = state.value
        val route = now.route ?: return false
        if (now.testing) return false
        mutable.update { it.copy(testing = true, problem = null) }
        try {
            try { api.verifyProxy(hubId, route) }
            catch (failure: TunnelException) {
                mutable.update { it.copy(problem = tunnelProblemText(failure.problem, proxy = route, hubAddress = hubAddress)) }
                return false
            }
            return save(route)
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) {
            mutable.update { it.copy(problem = ResText.of(R.string.settings_tunnel_save_failed)) }
            return false
        } finally { mutable.update { it.copy(testing = false) } }
    }

    suspend fun delete(): Boolean = try {
        api.saveProxy(hubId, null)
        mutable.update { LocalProxyForm() }
        true
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) {
        mutable.update { it.copy(problem = ResText.of(R.string.settings_tunnel_save_failed)) }
        false
    }

    private suspend fun save(route: LocalProxyRoute): Boolean = try {
        api.saveProxy(hubId, route)
        mutable.update { it.copy(saved = route) }
        true
    } catch (cancelled: CancellationException) { throw cancelled }
    catch (_: Exception) {
        mutable.update { it.copy(problem = ResText.of(R.string.settings_tunnel_save_failed)) }
        false
    }
}

/** Blank: [default] (null: required); otherwise a port number. */
private fun portOrDefault(text: String, default: Int?): Int? =
    if (text.isBlank()) default else text.trim().toIntOrNull()?.takeIf { it in 1..65535 }

/** A hub's tunnel row on Settings › Hubs: not set, in use, always or fallback (iOS `HubsView`). */
@StringRes internal fun tunnelRowValue(info: HubTunnelInfo, kind: TunnelKind): Int {
    val usable = if (kind == TunnelKind.Ssh) info.ssh?.usable == true else info.proxy?.usable == true
    val always = if (kind == TunnelKind.Ssh) info.ssh?.always == true else info.proxy?.always == true
    return when {
        !usable -> R.string.settings_tunnel_not_set
        info.routed == kind -> R.string.settings_hubs_in_use
        always -> R.string.settings_tunnel_always
        else -> R.string.settings_tunnel_fallback
    }
}

/** The status line of a hub's saved tunnel. */
@StringRes internal fun tunnelStatus(info: HubTunnelInfo, kind: TunnelKind): Int = when {
    info.routed == kind && info.online ->
        if (kind == TunnelKind.Ssh) R.string.settings_tunnel_ssh_connected else R.string.settings_tunnel_proxy_connected
    info.routed == kind ->
        if (kind == TunnelKind.Ssh) R.string.settings_tunnel_ssh_connecting else R.string.settings_tunnel_proxy_connecting
    info.routed == null && info.online -> R.string.settings_tunnel_not_in_use
    info.routed == TunnelKind.Ssh && info.online -> R.string.settings_tunnel_not_in_use_ssh
    info.routed == TunnelKind.Proxy && info.online -> R.string.settings_tunnel_not_in_use_proxy
    else -> R.string.settings_tunnel_not_connected
}

/** What went wrong and what to check, for the route that was tried. */
internal fun tunnelProblemText(problem: TunnelProblem, ssh: SshRoute? = null, proxy: LocalProxyRoute? = null, hubAddress: String = ""): ResText {
    fun proxyTarget() = proxy?.target(hubAddress)?.let { (host, port) -> LocalProxyRoute.bracketed(host) + ":" + port }.orEmpty()
    return when (problem) {
        is TunnelProblem.SshUnreachable ->
            ResText.of(R.string.settings_tunnel_problem_ssh_unreachable, ssh?.host.orEmpty(), (ssh?.port ?: 22).toString(), problem.detail)
        is TunnelProblem.HostKeyUnknown -> ResText.of(R.string.settings_tunnel_problem_host_key_unknown)
        is TunnelProblem.HostKeyChanged -> ResText.of(R.string.settings_tunnel_problem_host_key_changed)
        TunnelProblem.NotAuthorized -> ResText.of(R.string.settings_tunnel_problem_not_authorized, ssh?.user.orEmpty())
        is TunnelProblem.Handshake -> ResText.of(R.string.settings_tunnel_problem_handshake, problem.detail)
        is TunnelProblem.TargetRefused -> ResText.of(R.string.settings_tunnel_problem_target_refused,
            ssh?.let { LocalProxyRoute.bracketed(it.targetHost) + ":" + it.targetPort }.orEmpty(), problem.detail)
        TunnelProblem.SshTimedOut -> ResText.of(R.string.settings_tunnel_problem_ssh_timed_out)
        TunnelProblem.KeyUnavailable -> ResText.of(R.string.settings_tunnel_key_unavailable)
        TunnelProblem.Stopped -> ResText.of(R.string.settings_tunnel_problem_stopped)
        is TunnelProblem.ProxyUnreachable -> ResText.of(R.string.settings_tunnel_problem_proxy_unreachable, proxy?.label.orEmpty(), problem.detail)
        TunnelProblem.NotSocks5 -> ResText.of(R.string.settings_tunnel_problem_not_socks5, proxy?.label.orEmpty())
        is TunnelProblem.NoAcceptableMethod -> ResText.of(
            if (problem.loginOffered) R.string.settings_tunnel_problem_no_method else R.string.settings_tunnel_problem_login_required)
        TunnelProblem.LoginRejected -> ResText.of(R.string.settings_tunnel_problem_login_rejected)
        is TunnelProblem.ConnectFailed -> ResText.of(R.string.settings_tunnel_problem_connect_failed, proxyTarget(),
            Socks5Handshake.reply(problem.code).let { if (problem.code in 1..8) "$it, ${problem.code}" else it })
        TunnelProblem.Malformed -> ResText.of(R.string.settings_tunnel_problem_malformed)
        TunnelProblem.ProxyClosed -> ResText.of(R.string.settings_tunnel_problem_proxy_closed)
        TunnelProblem.ProxyTimedOut -> ResText.of(R.string.settings_tunnel_problem_proxy_timed_out)
        TunnelProblem.LoginTooLong -> ResText.of(R.string.settings_tunnel_problem_login_too_long)
        TunnelProblem.BadHost -> ResText.of(R.string.settings_tunnel_problem_bad_host)
        is TunnelProblem.NoAnswer -> ResText.of(R.string.settings_tunnel_problem_no_answer, problem.target)
        is TunnelProblem.NotAHub -> ResText.of(R.string.settings_tunnel_problem_not_a_hub, problem.target)
        is TunnelProblem.OtherHub -> ResText.of(R.string.settings_tunnel_problem_other_hub, problem.target)
        is TunnelProblem.SeenAsInternet -> ResText.of(R.string.settings_tunnel_problem_seen_as_internet, problem.target)
    }
}
