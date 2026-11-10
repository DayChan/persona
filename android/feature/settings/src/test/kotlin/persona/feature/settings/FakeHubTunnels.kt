package persona.feature.settings

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import persona.core.contracts.api.HubTunnelInfo
import persona.core.contracts.api.HubTunnelSettings
import persona.core.contracts.api.LocalProxyRoute
import persona.core.contracts.api.SshDeviceKey
import persona.core.contracts.api.SshHostKey
import persona.core.contracts.api.SshRoute
import persona.core.contracts.api.TunnelException
import persona.core.contracts.api.TunnelProblem

internal const val TUNNEL_FINGERPRINT = "SHA256:q0l6m2xO1b5yYp6nYc8Rr2hTn0eX4kB3dLw9sUa7VfM"

/** Settings › Hubs › SSH tunnel and Local proxy without a device: what is saved, and the answers a test asks for. */
internal class FakeHubTunnels(vararg hubs: HubTunnelInfo) : HubTunnelSettings {
    override val hubs = MutableStateFlow(hubs.toList())
    val calls = mutableListOf<String>()
    /** What the next SSH test meets; null: the server shows [TUNNEL_FINGERPRINT]. */
    var sshProblem: TunnelProblem? = null
    var proxyProblem: TunnelProblem? = null
    var keyUnavailable = false
    var saveFails = false

    override suspend fun deviceKey(): SshDeviceKey {
        if (keyUnavailable) throw TunnelException(TunnelProblem.KeyUnavailable)
        return SshDeviceKey("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq4o0hLr8pS0bV1yXc2m3Nn7tQe9wKz5fJd6aUo1iHs persona-Pixel-9")
    }

    override suspend fun verifySsh(hubId: String, route: SshRoute): SshHostKey {
        calls += "verifySsh ${route.label} key=${route.hostKey}"
        sshProblem?.let { throw TunnelException(it) }
        val key = SshHostKey(TUNNEL_FINGERPRINT, "ED25519")
        return when (route.hostKey) {
            null -> throw TunnelException(TunnelProblem.HostKeyUnknown(key))
            key.fingerprint -> key
            else -> throw TunnelException(TunnelProblem.HostKeyChanged(route.hostKey!!, key))
        }
    }

    override suspend fun saveSsh(hubId: String, route: SshRoute?) {
        calls += "saveSsh ${route?.label} key=${route?.hostKey} always=${route?.always}"
        if (saveFails) error("disk full")
        hubs.update { list -> list.map { if (it.hubId == hubId) it.copy(ssh = route) else it } }
    }

    override suspend fun verifyProxy(hubId: String, route: LocalProxyRoute) {
        calls += "verifyProxy ${route.label}"
        proxyProblem?.let { throw TunnelException(it) }
    }

    override suspend fun saveProxy(hubId: String, route: LocalProxyRoute?) {
        calls += "saveProxy ${route?.label} always=${route?.always}"
        if (saveFails) error("disk full")
        hubs.update { list -> list.map { if (it.hubId == hubId) it.copy(proxy = route) else it } }
    }
}
