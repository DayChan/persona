package persona.core.hub.tunnel

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.cancel
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import persona.core.contracts.api.LocalProxyRoute
import persona.core.contracts.api.SshHostKey
import persona.core.contracts.api.SshRoute
import persona.core.contracts.api.TunnelException
import persona.core.contracts.api.TunnelKind
import persona.core.contracts.api.TunnelProblem
import persona.core.contracts.api.TunnelState

/** A way to a paired hub when its own addresses do not answer: its SSH tunnel or its local SOCKS5 proxy. */
data class HubTunnel(
    val hubId: String,
    /** The paired address: its path is kept through the tunnel, and the proxy's default target comes from it. */
    val hubAddress: String,
    val ssh: SshRoute? = null,
    val proxy: LocalProxyRoute? = null,
) {
    init { require((ssh == null) != (proxy == null)) { "A tunnel is either SSH or a local proxy" } }

    val kind: TunnelKind get() = if (ssh != null) TunnelKind.Ssh else TunnelKind.Proxy

    /** Through it every time, not only when the hub's own addresses do not answer. */
    val always: Boolean get() = ssh?.always ?: proxy!!.always

    /** The candidate kind the connection details show for it. */
    val endpointKind: String get() = if (ssh != null) "ssh" else "proxy"

    /** The same tunnel, whatever [always] says: switching it does not make the tunnel again. */
    fun sameWay(other: HubTunnel): Boolean = hubId == other.hubId && hubAddress == other.hubAddress &&
        ssh?.copy(always = false) == other.ssh?.copy(always = false) && proxy?.copy(always = false) == other.proxy?.copy(always = false)

    override fun toString(): String = "HubTunnel($hubId, $kind, always=$always)"

    companion object {
        /** The hub's address on a tunnel's loopback port: `http://127.0.0.1:<port>` with the paired address's path. */
        fun baseUrl(hubAddress: String, port: Int): String =
            "http://127.0.0.1:$port" + hubAddress.trim().toHttpUrlOrNull()?.encodedPath?.trimEnd('/').orEmpty()

        /** A hub's usable tunnels in the order they are tried: its SSH tunnel, then its local proxy. */
        fun of(hubId: String, hubAddress: String, ssh: SshRoute?, proxy: LocalProxyRoute?): List<HubTunnel> = listOfNotNull(
            ssh?.takeIf { it.usable }?.let { HubTunnel(hubId, hubAddress, ssh = it) },
            proxy?.takeIf { it.usable }?.let { HubTunnel(hubId, hubAddress, proxy = it) },
        )
    }
}

/**
 * How a connection picks its way when its hubs have tunnels (docs/operations/network.md "Connecting to hub through SSH"):
 * the tunnels set to Always first, in order, the first that comes up taken; then the hub's own addresses, while the
 * tunnels used last time start meanwhile; an address answering: straight to the hub; else the first of those to have come
 * up, then the other tunnels in order. Going through a tunnel only because the direct way did not answer, the direct way
 * is asked again every [RECHECK_MILLIS]. Pure: the connection carries out each step and tells it what came of it.
 */
object HubWay {
    const val RECHECK_MILLIS = 120_000L

    data class Option(val always: Boolean, val recent: Boolean)

    data class Seen(
        /** By tunnel index: whether it came up and the hub answered through it. */
        val opened: Map<Int, Boolean> = emptyMap(),
        /** Whether one of the hub's own addresses answered; null before they were asked. */
        val direct: Boolean? = null,
    )

    sealed interface Step {
        data class Open(val index: Int) : Step
        /** Ask the hub's own addresses, bringing up these tunnels meanwhile, in order, until one comes up. */
        data class AskDirect(val warming: List<Int>) : Step
        /** Straight to the hub; every tunnel closes. */
        data object Direct : Step
        data class Through(val index: Int, val watchDirect: Boolean) : Step
        /** No way: the attempt fails and the connection retries. */
        data object None : Step
    }

    fun next(options: List<Option>, seen: Seen): Step {
        for (i in options.indices) {
            if (!options[i].always) continue
            when (seen.opened[i]) {
                true -> return Step.Through(i, watchDirect = false)
                null -> return Step.Open(i)
                false -> continue
            }
        }
        val fallback = options.indices.filter { !options[it].always }
        val warm = fallback.filter { options[it].recent }
        val direct = seen.direct ?: return Step.AskDirect(warm)
        if (direct) return Step.Direct
        for (i in warm + fallback.filter { !options[it].recent }) {
            when (seen.opened[i]) {
                true -> return Step.Through(i, watchDirect = true)
                null -> return Step.Open(i)
                false -> continue
            }
        }
        return Step.None
    }
}

/**
 * The tunnels the app runs, at most one of each kind per paired hub, shared by every hub connection. A tunnel is
 * opened when a connection goes through it and closed once the hub answers straight again.
 */
class HubWays(
    private val identity: suspend () -> SshIdentity,
    private val sshTimings: SshTimings = SshTimings(),
    dispatcher: CoroutineDispatcher = Dispatchers.IO,
) : AutoCloseable {
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    private val lock = Any()

    private class Running(val tunnel: HubTunnel, val ssh: SshTunnel?, val forward: LocalProxyForward?) : AutoCloseable {
        val alive: Boolean get() = ssh?.alive ?: forward!!.listening

        suspend fun start(preferred: Int): Int = ssh?.start(preferred)
            ?: blocking(forward!!::close) { forward.check(); forward.listen(preferred) }

        override fun close() {
            ssh?.close()
            forward?.close()
        }
    }

    private class Starting(val epoch: Int, val tunnel: HubTunnel, val task: Deferred<String>)

    private val running = HashMap<Pair<String, TunnelKind>, Running>()
    private val starting = HashMap<Pair<String, TunnelKind>, Starting>()
    private val epochs = HashMap<Pair<String, TunnelKind>, Int>()
    /** The loopback port each tunnel had: the next one takes it again when it is free, so addresses handed out stay good. */
    private val ports = HashMap<Pair<String, TunnelKind>, Int>()
    /** Tunnels used last time: started while the direct way is asked, not after it timed out. */
    private val recent = HashSet<Pair<String, TunnelKind>>()
    /** A failure only the user can fix, with the settings it happened with: not dialled again until they change. */
    private val blocked = HashMap<Pair<String, TunnelKind>, Pair<HubTunnel, TunnelProblem>>()

    private val _states = MutableStateFlow<Map<Pair<String, TunnelKind>, TunnelState>>(emptyMap())
    /** By hub id and kind; a closed tunnel has none. */
    val states: StateFlow<Map<Pair<String, TunnelKind>, TunnelState>> = _states.asStateFlow()

    private val HubTunnel.key get() = hubId to kind

    fun recent(tunnel: HubTunnel): Boolean = synchronized(lock) { tunnel.key in recent }

    /**
     * The tunnel up, as the hub's base URL on its loopback port: the one running when it goes the same way (a local
     * proxy is asked for the hub once first, so one whose app quit is not taken), else a new one. Settings that change
     * while an earlier start is under way replace it. Throws [TunnelException].
     */
    suspend fun open(tunnel: HubTunnel): String {
        val key = tunnel.key
        val task = synchronized(lock) {
            val pending = starting[key]
            if (pending != null && pending.epoch == epochs[key] && pending.tunnel.sameWay(tunnel)) pending.task
            else {
                if (pending != null) bump(key)
                val epoch = epochs[key] ?: 0
                scope.async { start(tunnel, epoch) }.also { starting[key] = Starting(epoch, tunnel, it) }
            }
        }
        return try {
            task.await()
        } catch (cancelled: CancellationException) {
            currentCoroutineContext().ensureActive()
            throw TunnelException(TunnelProblem.Stopped, cancelled)
        }
    }

    private suspend fun start(tunnel: HubTunnel, epoch: Int): String {
        val key = tunnel.key
        var made: Running? = null
        try {
            val reuse = synchronized(lock) {
                blocked[key]?.let { (way, problem) -> if (way.sameWay(tunnel)) throw TunnelException(problem) else blocked.remove(key) }
                running[key]?.takeIf { it.tunnel.sameWay(tunnel) && it.alive }
            }
            if (reuse != null) {
                reuse.forward?.let { forward -> blocking({}) { forward.check() } }
                return synchronized(lock) {
                    current(key, epoch)
                    recent += key
                    setState(key, TunnelState.Up)
                    HubTunnel.baseUrl(tunnel.hubAddress, reuse.ssh?.localPort ?: reuse.forward!!.localPort)
                }
            }
            val ssh = tunnel.ssh?.let { SshTunnel(it, identity(), sshTimings) }
            val forward = tunnel.proxy?.let { LocalProxyForward(LocalProxyForward.Dial.of(it, tunnel.hubAddress)) }
            val handle = Running(tunnel, ssh, forward)
            made = handle
            forward?.onDial = { problem ->
                synchronized(lock) { if (running[key] === handle) setState(key, problem?.let(TunnelState::Down) ?: TunnelState.Up) }
            }
            val (preferred, replaced) = synchronized(lock) {
                current(key, epoch)
                val replaced = running.remove(key)
                running[key] = handle
                setState(key, TunnelState.Starting)
                (ports[key] ?: 0) to replaced
            }
            replaced?.close()
            val port = handle.start(preferred)
            return synchronized(lock) {
                current(key, epoch)
                if (running[key] !== handle) throw CancellationException("Tunnel replaced")
                ports[key] = port
                recent += key
                setState(key, TunnelState.Up)
                HubTunnel.baseUrl(tunnel.hubAddress, port)
            }
        } catch (cancelled: CancellationException) {
            made?.let { handle -> synchronized(lock) { if (running[key] === handle) running.remove(key) }; handle.close() }
            throw cancelled
        } catch (error: Exception) {
            val detail = error.message ?: error.javaClass.simpleName
            val problem = (error as? TunnelException)?.problem
                ?: if (tunnel.ssh != null) TunnelProblem.SshUnreachable(detail) else TunnelProblem.ProxyUnreachable(detail)
            val stale = synchronized(lock) {
                val handle = made
                if (handle != null && running[key] === handle) running.remove(key)
                if ((epochs[key] ?: 0) == epoch) {
                    setState(key, TunnelState.Down(problem))
                    if (problem.needsUser) blocked[key] = tunnel to problem
                    if (handle == null) running.remove(key) else null
                } else null
            }
            stale?.close()
            made?.close()
            throw error as? TunnelException ?: TunnelException(problem, error)
        } finally {
            synchronized(lock) { if (starting[key]?.epoch == epoch) starting.remove(key) }
        }
    }

    /** Throws when a close or newer settings came since the start [epoch]. Callers hold [lock]. */
    private fun current(key: Pair<String, TunnelKind>, epoch: Int) {
        if ((epochs[key] ?: 0) != epoch) throw CancellationException("Tunnel closed")
    }

    private fun bump(key: Pair<String, TunnelKind>) {
        epochs[key] = (epochs[key] ?: 0) + 1
        starting.remove(key)?.task?.cancel()
    }

    private fun setState(key: Pair<String, TunnelKind>, state: TunnelState?) =
        _states.update { if (state == null) it - key else it + (key to state) }

    /**
     * The hub's tunnels close ([kind]: only that one), and a start under way for them never routes. Without [forget]
     * the last failure stays on show and one that waits for the user is not dialled again; with it, the tunnel is
     * not started early next time either.
     */
    fun close(hubId: String, kind: TunnelKind? = null, forget: Boolean = true) {
        val closing = synchronized(lock) {
            val keys = (running.keys + starting.keys + recent + blocked.keys + _states.value.keys)
                .filter { it.first == hubId && (kind == null || it.second == kind) }.toSet()
            keys.mapNotNull { key ->
                bump(key)
                if (forget || _states.value[key] !is TunnelState.Down) setState(key, null)
                if (forget) {
                    recent -= key
                    blocked -= key
                }
                running.remove(key)
            }
        }
        closing.forEach { it.close() }
    }

    /**
     * The user fixed what a tunnel waited for outside the app (the server took the key, its key was confirmed) and
     * tested it: it is dialled again, and its last failure goes.
     */
    fun unblock(hubId: String, kind: TunnelKind) = synchronized(lock) {
        val key = hubId to kind
        blocked -= key
        if (_states.value[key] is TunnelState.Down) setState(key, null)
    }

    /** Tunnels of hubs no longer paired close. */
    fun retain(hubIds: Set<String>) {
        val gone = synchronized(lock) { (running.keys + starting.keys + recent + _states.value.keys).map { it.first }.toSet() - hubIds }
        gone.forEach { close(it) }
    }

    /** Connects, logs in and opens the hub's address once with [route]: the server's key ("Test connection"). */
    suspend fun verifySsh(route: SshRoute): SshHostKey = SshTunnel(route, identity(), sshTimings).verify()

    /** The hub answers its health check through [route]. */
    suspend fun verifyProxy(route: LocalProxyRoute, hubAddress: String, hubId: String?) =
        blocking({}) { LocalProxyForward.verify(route, hubAddress, hubId) }

    override fun close() {
        val closing = synchronized(lock) {
            running.values.toList().also {
                running.clear()
                starting.clear()
                _states.value = emptyMap()
            }
        }
        closing.forEach { it.close() }
        scope.cancel()
    }
}
