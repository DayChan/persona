package persona.core.contracts.api

import java.net.URI
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * A paired hub's SSH tunnel (docs/operations/network.md "Connecting to hub through SSH"): the hub is reached through an
 * SSH server when its own addresses do not answer, or every time with [always].
 */
@Serializable
data class SshRoute(
    val host: String,
    val port: Int = 22,
    /** As typed; a Warpgate login (`user:target`) included. */
    val user: String,
    /** The hub's address as the SSH server sees it. */
    @SerialName("target_host") val targetHost: String = DEFAULT_TARGET_HOST,
    @SerialName("target_port") val targetPort: Int = DEFAULT_TARGET_PORT,
    /** `SHA256:…` of the server's key once the user confirmed it; null before (the tunnel is not used then). */
    @SerialName("host_key") val hostKey: String? = null,
    /** The key's algorithm as OpenSSH prints it (`ED25519`), for showing the fingerprint. */
    @SerialName("host_key_algorithm") val hostKeyAlgorithm: String? = null,
    val always: Boolean = false,
) {
    /** Ready to carry traffic: a server, a user and a confirmed key. */
    val usable: Boolean get() = host.isNotBlank() && user.isNotBlank() && port in 1..65535 && !hostKey.isNullOrEmpty()

    /** `user@host` (`:port` when not 22), what lists show. */
    val label: String get() = "${user.trim()}@${host.trim()}" + if (port == 22) "" else ":$port"

    /** Another server needs its key confirmed again. */
    fun sameServer(other: SshRoute): Boolean = host.trim() == other.host.trim() && port == other.port

    companion object {
        const val DEFAULT_TARGET_HOST = "127.0.0.1"
        const val DEFAULT_TARGET_PORT = 18660
    }
}

/**
 * A paired hub's local SOCKS5 proxy (docs/operations/network.md "Connecting to hub through a local SOCKS5 proxy"): another
 * app's proxy on this device that reaches the hub's network.
 */
@Serializable
data class LocalProxyRoute(
    val host: String = DEFAULT_HOST,
    val port: Int,
    /** Empty: the proxy asks for no login. */
    val username: String = "",
    val password: String = "",
    /** The hub's host as the proxy's network sees it; null: the paired address's. */
    @SerialName("target_host") val targetHost: String? = null,
    /** The hub's HTTP port as the proxy's network sees it; null: the paired address's ([target]). */
    @SerialName("target_port") val targetPort: Int? = null,
    val always: Boolean = false,
) {
    val usable: Boolean get() = host.isNotBlank() && port in 1..65535 && (targetPort == null || targetPort in 1..65535)

    /** `host:port` of the proxy, what lists show. */
    val label: String get() = bracketed(host.trim()) + ":$port"

    /** The proxy is on this device. Anywhere else, the hub's plain HTTP and the proxy's password cross the network. */
    val onThisDevice: Boolean get() {
        val h = host.trim().trim('[', ']').lowercase()
        if (h == "localhost" || h == "::1") return true
        val parts = h.split('.')
        return parts.size == 4 && parts[0] == "127" &&
            parts.all { part -> part.length in 1..3 && part.all { it in '0'..'9' } && part.toInt() <= 255 }
    }

    /**
     * The hub's host and port the proxy is asked for: as set, else the paired address's host, with its port for an
     * `http` address (80 when it names none) and the hub's HTTP port for an `https` one (the forward carries plain HTTP).
     */
    fun target(hubAddress: String): Pair<String, Int> {
        val host = targetHost?.trim()?.takeIf { it.isNotEmpty() } ?: defaultTargetHost(hubAddress)
        return host to (targetPort ?: defaultTargetPort(hubAddress))
    }

    override fun toString(): String = "LocalProxyRoute($label, login=${username.isNotEmpty()}, always=$always)"

    companion object {
        const val DEFAULT_HOST = "127.0.0.1"
        const val HUB_HTTP_PORT = 18660

        fun defaultTargetHost(hubAddress: String): String =
            runCatching { URI(hubAddress.trim()).host }.getOrNull()?.trim('[', ']').orEmpty()

        fun defaultTargetPort(hubAddress: String): Int {
            val uri = runCatching { URI(hubAddress.trim()) }.getOrNull()
            return if (uri?.scheme.equals("http", ignoreCase = true)) uri!!.port.takeIf { it > 0 } ?: 80 else HUB_HTTP_PORT
        }

        fun bracketed(host: String): String = if (':' in host && !host.startsWith('[')) "[$host]" else host
    }
}

/** An SSH server's key as `ssh-keygen -lf` prints it: `SHA256:…` and `ED25519`. */
data class SshHostKey(val fingerprint: String, val algorithm: String)

/** Why a way to the hub through a tunnel is not up. */
sealed interface TunnelProblem {
    /** Trying again without the user changing something does not help: the tunnel waits for new settings. */
    val needsUser: Boolean get() = false

    data class SshUnreachable(val detail: String) : TunnelProblem
    /** First connection: the server's key is shown to the user to confirm, never trusted silently. */
    data class HostKeyUnknown(val key: SshHostKey) : TunnelProblem { override val needsUser get() = true }
    /** The server's key is not the one pinned: refused. */
    data class HostKeyChanged(val pinned: String, val key: SshHostKey) : TunnelProblem { override val needsUser get() = true }
    data object NotAuthorized : TunnelProblem { override val needsUser get() = true }
    data class Handshake(val detail: String) : TunnelProblem
    /** Logged in, but the server would not or could not open the hub's address. */
    data class TargetRefused(val detail: String) : TunnelProblem
    data object SshTimedOut : TunnelProblem
    /** This device's SSH key cannot be read right now. */
    data object KeyUnavailable : TunnelProblem
    /** Closed while it was starting: the hub's tunnels or its network changed. */
    data object Stopped : TunnelProblem

    data class ProxyUnreachable(val detail: String) : TunnelProblem
    data object NotSocks5 : TunnelProblem
    data class NoAcceptableMethod(val loginOffered: Boolean) : TunnelProblem
    data object LoginRejected : TunnelProblem
    /** The proxy's reply code (RFC 1928 section 6). */
    data class ConnectFailed(val code: Int) : TunnelProblem
    data object Malformed : TunnelProblem
    data object ProxyClosed : TunnelProblem
    data object ProxyTimedOut : TunnelProblem
    data object LoginTooLong : TunnelProblem
    data object BadHost : TunnelProblem

    /** No answer from [target] through the proxy: it may not reach it, or the hub is not listening there. */
    data class NoAnswer(val target: String) : TunnelProblem
    data class NotAHub(val target: String) : TunnelProblem
    data class OtherHub(val target: String) : TunnelProblem
    /** The hub counts connections through the proxy as the internet and refuses plain HTTP from there. */
    data class SeenAsInternet(val target: String) : TunnelProblem
}

class TunnelException(val problem: TunnelProblem, cause: Throwable? = null) : Exception(problem.toString(), cause)

enum class TunnelKind { Ssh, Proxy }

/** A tunnel's state; none while it is closed. */
sealed interface TunnelState {
    data object Starting : TunnelState
    /** Carrying the hub's traffic. */
    data object Up : TunnelState
    data class Down(val problem: TunnelProblem) : TunnelState
}

/** One paired hub as Settings › Hubs shows its tunnels. */
data class HubTunnelInfo(
    val hubId: String,
    val name: String,
    /** The paired address: the local proxy's default target comes from it. */
    val hubAddress: String,
    val ssh: SshRoute? = null,
    val proxy: LocalProxyRoute? = null,
    val sshState: TunnelState? = null,
    val proxyState: TunnelState? = null,
    /** The tunnel the hub's connection goes through right now. */
    val routed: TunnelKind? = null,
    /** The connection of the hub's group is up. */
    val online: Boolean = false,
)

/** This device's SSH key, one per device, never synced. */
data class SshDeviceKey(
    /** `ssh-ed25519 AAAA… persona-<device>`. */
    val publicKey: String,
) {
    /** The line for the server's authorized_keys: forwarding to the hub's address only. */
    fun authorizedKeysLine(targetHost: String, targetPort: Int): String =
        "restrict,port-forwarding,permitopen=\"${LocalProxyRoute.bracketed(targetHost.trim())}:$targetPort\" $publicKey"
}

/** Settings › Hubs › SSH Tunnel and Local Proxy. */
interface HubTunnelSettings {
    val hubs: StateFlow<List<HubTunnelInfo>>

    /** Made and saved the first time; throws [TunnelException] with [TunnelProblem.KeyUnavailable] when unreadable. */
    suspend fun deviceKey(): SshDeviceKey

    /**
     * Connects, logs in and opens the hub's address once. Returns the server's key when [SshRoute.hostKey] matches;
     * throws [TunnelException] otherwise ([TunnelProblem.HostKeyUnknown] / [TunnelProblem.HostKeyChanged] carry it).
     */
    suspend fun verifySsh(hubId: String, route: SshRoute): SshHostKey

    /** Null removes it. The hub's connection picks its way again. */
    suspend fun saveSsh(hubId: String, route: SshRoute?)

    /** The hub answers its health check through [route]; throws [TunnelException] otherwise. */
    suspend fun verifyProxy(hubId: String, route: LocalProxyRoute)

    suspend fun saveProxy(hubId: String, route: LocalProxyRoute?)

    companion object {
        val None: HubTunnelSettings = object : HubTunnelSettings {
            override val hubs = MutableStateFlow(emptyList<HubTunnelInfo>())
            override suspend fun deviceKey() = throw TunnelException(TunnelProblem.KeyUnavailable)
            override suspend fun verifySsh(hubId: String, route: SshRoute) = throw TunnelException(TunnelProblem.KeyUnavailable)
            override suspend fun saveSsh(hubId: String, route: SshRoute?) = Unit
            override suspend fun verifyProxy(hubId: String, route: LocalProxyRoute) = Unit
            override suspend fun saveProxy(hubId: String, route: LocalProxyRoute?) = Unit
        }
    }
}
