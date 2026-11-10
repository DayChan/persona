package persona.core.hub.tunnel

import com.jcraft.jsch.ChannelDirectTCPIP
import com.jcraft.jsch.HostKey
import com.jcraft.jsch.HostKeyRepository
import com.jcraft.jsch.JSch
import com.jcraft.jsch.JSchAlgoNegoFailException
import com.jcraft.jsch.JSchException
import com.jcraft.jsch.Session
import com.jcraft.jsch.UserInfo
import java.net.ConnectException
import java.net.NoRouteToHostException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import kotlinx.coroutines.CancellationException
import persona.core.contracts.api.SshHostKey
import persona.core.contracts.api.SshRoute
import persona.core.contracts.api.TunnelException
import persona.core.contracts.api.TunnelProblem

/** How long the steps may take; shorter in tests. */
data class SshTimings(
    /** TCP connect, then each read until logged in. */
    val connectMillis: Int = 15_000,
    /** Opening one forwarded connection on a logged-in session. */
    val openMillis: Int = 10_000,
    /** A silent session is sent a keepalive this often and dropped after three unanswered. */
    val aliveMillis: Int = 15_000,
)

/**
 * One SSH connection to [route]'s server that forwards a loopback port to the hub's address (`direct-tcpip`, one channel
 * per connection), as `ssh -N -L` would. The server's key must be the pinned one; logs in with this device's key only.
 */
class SshTunnel(
    private val route: SshRoute,
    private val identity: SshIdentity,
    private val timings: SshTimings = SshTimings(),
) : AutoCloseable {
    @Volatile private var session: Session? = null
    @Volatile private var closed = false

    /** The loopback port it listens on; 0 before [start]. */
    @Volatile var localPort = 0
        private set

    /** Logged in and forwarding. A dropped session is not: the next open dials again. */
    val alive: Boolean get() = !closed && localPort != 0 && session?.isConnected == true

    /**
     * Connects, logs in, opens the hub's address once, then listens on 127.0.0.1: at [preferred] when it is free (the
     * port the last tunnel had, so addresses handed out stay good), else on any free one. Throws [TunnelException].
     */
    suspend fun start(preferred: Int = 0): Int {
        val (session, _) = connect()
        var port = 0
        try {
            openTarget(session)
            // A forward bound after a close is not ended by disconnect(), which does nothing once the session is down.
            port = blocking(session::disconnect, onLate = { bound -> unlisten(session, bound) }) { listen(session, preferred) }
            if (closed) throw CancellationException("SSH tunnel closed")
            if (!session.isConnected) throw TunnelException(TunnelProblem.SshUnreachable("connection closed"))
            localPort = port
            return port
        } catch (error: Throwable) {
            if (port != 0) unlisten(session, port)
            session.disconnect()
            throw error
        }
    }

    /** The server's key, after connecting, logging in and opening the hub's address once ("Test connection"). */
    suspend fun verify(): SshHostKey {
        val (session, key) = connect()
        try {
            openTarget(session)
            return key
        } finally {
            session.disconnect()
        }
    }

    override fun close() {
        closed = true
        session?.disconnect()
    }

    private suspend fun connect(): Pair<Session, SshHostKey> {
        SshAlgorithms.install()
        val jsch = JSch()
        jsch.addIdentity(identity.jsch, null)
        val pinned = PinnedHostKey(route.hostKey?.takeIf { it.isNotEmpty() })
        val session = jsch.getSession(route.user.trim(), route.host.trim(), route.port)
        session.setHostKeyRepository(pinned)
        session.setConfig("StrictHostKeyChecking", "yes")
        session.setConfig("PreferredAuthentications", "publickey")
        session.setConfig("server_host_key", SshWire.hostKeyAlgorithms(route.hostKeyAlgorithm))
        session.setDaemonThread(true)
        this.session = session
        if (closed) throw CancellationException("SSH tunnel closed")
        try {
            // Cancelled before the TCP connection is up, disconnect() does nothing: the login that finishes later ends here.
            blocking(session::disconnect, onLate = { session.disconnect() }) { session.connect(timings.connectMillis) }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            session.disconnect()
            throw TunnelException(pinned.problem ?: classify(error), error)
        }
        session.setServerAliveInterval(timings.aliveMillis)
        session.setServerAliveCountMax(3)
        return session to checkNotNull(pinned.seen)
    }

    /** The server opens the hub's address: `permitopen` and the target are right, and the hub listens there. */
    private suspend fun openTarget(session: Session) {
        val channel = try {
            session.getStreamForwarder(route.targetHost.trim(), route.targetPort) as ChannelDirectTCPIP
        } catch (error: JSchException) {
            throw TunnelException(TunnelProblem.SshUnreachable(error.message.orEmpty()), error)
        }
        try {
            blocking(channel::disconnect) { channel.connect(timings.openMillis) }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            val problem = when (val reason = channel.exitStatus) {
                -1 -> if (session.isConnected) TunnelProblem.SshTimedOut else TunnelProblem.Handshake(error.message.orEmpty())
                else -> TunnelProblem.TargetRefused(openFailure(reason))
            }
            throw TunnelException(problem, error)
        } finally {
            channel.disconnect()
        }
    }

    private fun unlisten(session: Session, port: Int) {
        runCatching { session.delPortForwardingL("127.0.0.1", port) }
    }

    private fun listen(session: Session, preferred: Int): Int {
        fun bind(port: Int) = session.setPortForwardingL(
            "127.0.0.1", port, route.targetHost.trim(), route.targetPort, null, timings.openMillis,
        )
        if (preferred != 0) runCatching { return bind(preferred) }
        return bind(0)
    }

    /** Accepts only the pinned key, and remembers the key the server showed. */
    private class PinnedHostKey(private val pinned: String?) : HostKeyRepository {
        @Volatile var seen: SshHostKey? = null
        @Volatile var problem: TunnelProblem? = null

        override fun check(host: String?, key: ByteArray): Int {
            val shown = SshWire.hostKey(key)
            seen = shown
            return when (pinned) {
                null -> HostKeyRepository.NOT_INCLUDED.also { problem = TunnelProblem.HostKeyUnknown(shown) }
                shown.fingerprint -> HostKeyRepository.OK
                else -> HostKeyRepository.CHANGED.also { problem = TunnelProblem.HostKeyChanged(pinned, shown) }
            }
        }

        override fun add(hostkey: HostKey?, ui: UserInfo?) = Unit
        override fun remove(host: String?, type: String?) = Unit
        override fun remove(host: String?, type: String?, key: ByteArray?) = Unit
        override fun getKnownHostsRepositoryID(): String = "persona"
        override fun getHostKey(): Array<HostKey> = emptyArray()
        override fun getHostKey(host: String?, type: String?): Array<HostKey> = emptyArray()
    }

    companion object {
        internal fun classify(error: Throwable): TunnelProblem {
            val causes = generateSequence(error) { it.cause }.toList()
            val message = error.message.orEmpty()
            return when {
                causes.any { it is JSchAlgoNegoFailException } -> TunnelProblem.Handshake("no common algorithm")
                message.startsWith("Auth fail") || message.startsWith("Auth cancel") -> TunnelProblem.NotAuthorized
                causes.any { it is UnknownHostException } -> TunnelProblem.SshUnreachable("unknown host")
                causes.any { it is SocketTimeoutException } || message.startsWith("timeout") -> TunnelProblem.SshTimedOut
                causes.any { it is ConnectException || it is NoRouteToHostException } ->
                    TunnelProblem.SshUnreachable(causes.first { it is ConnectException || it is NoRouteToHostException }.message.orEmpty())
                error is JSchException && causes.size == 1 -> TunnelProblem.Handshake(message)
                else -> TunnelProblem.SshUnreachable(causes.last().message ?: causes.last().javaClass.simpleName)
            }
        }

        /** RFC 4254 section 5.1's reason codes. */
        internal fun openFailure(reason: Int): String = when (reason) {
            1 -> "administratively prohibited"
            2 -> "connect failed"
            3 -> "unknown channel type"
            4 -> "resource shortage"
            else -> "code $reason"
        }
    }
}
