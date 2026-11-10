package persona.core.hub.tunnel

import java.io.DataInputStream
import java.io.EOFException
import java.io.IOException
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.Proxy
import java.net.Socket
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import persona.core.contracts.api.TunnelException
import persona.core.contracts.api.TunnelProblem

/**
 * The client side of a SOCKS5 handshake (RFC 1928) with an optional username and password (RFC 1929), up to the CONNECT
 * that makes the connection carry the target's bytes. Only bytes, no network: it says what to write and how many bytes
 * to read next. Every read is of an exact length, so what the target sends right after the proxy's reply stays on the
 * connection.
 */
class Socks5Handshake(host: String, port: Int, login: Pair<String, String>?) {
    sealed interface Step {
        /** Write these bytes, then read exactly [then] bytes and hand them to [read]. */
        class Write(val bytes: ByteArray, val then: Int) : Step
        /** Read exactly this many bytes and hand them to [read]. */
        data class Read(val count: Int) : Step
        /** The proxy connected: what the connection carries from now on is the target's. */
        data object Done : Step
    }

    private enum class Phase { Method, Login, Reply, ReplyNameLength, ReplyAddress, Done }

    private var phase = Phase.Method
    private var expected = 2
    private val greeting: ByteArray
    private val loginRequest: ByteArray?
    private val connectRequest: ByteArray

    init {
        if (port !in 1..65535) fail(TunnelProblem.BadHost)
        connectRequest = connectRequest(host, port)
        if (login != null) {
            val user = login.first.toByteArray(Charsets.UTF_8)
            val pass = login.second.toByteArray(Charsets.UTF_8)
            if (user.size > 255 || pass.size > 255) fail(TunnelProblem.LoginTooLong)
            loginRequest = byteArrayOf(1, user.size.toByte()) + user + byteArrayOf(pass.size.toByte()) + pass
            greeting = byteArrayOf(VERSION, 2, NO_LOGIN, USERNAME_PASSWORD)
        } else {
            loginRequest = null
            greeting = byteArrayOf(VERSION, 1, NO_LOGIN)
        }
    }

    /** The greeting: the ways to sign in offered (no login, and a username and password when there is one). */
    fun start(): Step = Step.Write(greeting, 2)

    /** The bytes the last step asked for; what to do next. Throws when the proxy refuses or breaks the protocol. */
    fun read(bytes: ByteArray): Step {
        if (bytes.size != expected || phase == Phase.Done) fail(TunnelProblem.Malformed)
        return when (phase) {
            Phase.Method -> {
                if (bytes[0] != VERSION) fail(TunnelProblem.NotSocks5)
                when (bytes[1]) {
                    NO_LOGIN -> send(connectRequest, 4, Phase.Reply)
                    // Not offered, yet some proxies (mihomo) answer it to every client once they ask for a login.
                    USERNAME_PASSWORD -> send(loginRequest ?: fail(TunnelProblem.NoAcceptableMethod(loginOffered = false)), 2, Phase.Login)
                    NO_ACCEPTABLE -> fail(TunnelProblem.NoAcceptableMethod(loginOffered = loginRequest != null))
                    else -> fail(TunnelProblem.Malformed)
                }
            }
            // RFC 1929's reply is version 1; some proxies answer 5. Only the status counts.
            Phase.Login -> if (bytes[1] != 0.toByte()) fail(TunnelProblem.LoginRejected) else send(connectRequest, 4, Phase.Reply)
            Phase.Reply -> {
                if (bytes[0] != VERSION) fail(TunnelProblem.Malformed)
                if (bytes[1] != 0.toByte()) fail(TunnelProblem.ConnectFailed(bytes[1].toInt() and 0xFF))
                when (bytes[3].toInt()) {
                    1 -> expect(4 + 2, Phase.ReplyAddress)
                    4 -> expect(16 + 2, Phase.ReplyAddress)
                    3 -> expect(1, Phase.ReplyNameLength)
                    else -> fail(TunnelProblem.Malformed)
                }
            }
            Phase.ReplyNameLength -> expect((bytes[0].toInt() and 0xFF) + 2, Phase.ReplyAddress)
            Phase.ReplyAddress -> { phase = Phase.Done; Step.Done }
            Phase.Done -> fail(TunnelProblem.Malformed)
        }
    }

    private fun send(bytes: ByteArray, then: Int, next: Phase): Step {
        phase = next
        expected = then
        return Step.Write(bytes, then)
    }

    private fun expect(count: Int, next: Phase): Step {
        phase = next
        expected = count
        return Step.Read(count)
    }

    companion object {
        const val VERSION: Byte = 5
        const val NO_LOGIN: Byte = 0
        const val USERNAME_PASSWORD: Byte = 2
        const val NO_ACCEPTABLE: Byte = -1

        /**
         * CONNECT to `host:port`: an IPv4 or IPv6 literal as its address, anything else as a name the proxy resolves.
         * Nothing is looked up on this device: the name belongs to the proxy's network.
         */
        fun connectRequest(host: String, port: Int): ByteArray {
            val h = host.trim().trim('[', ']')
            val v4 = dottedQuad(h)
            val address = when {
                v4 != null -> byteArrayOf(1) + v4
                ':' in h -> byteArrayOf(4) + ipv6(h)
                else -> {
                    val name = h.toByteArray(Charsets.UTF_8)
                    if (name.size !in 1..255) fail(TunnelProblem.BadHost)
                    byteArrayOf(3, name.size.toByte()) + name
                }
            }
            return byteArrayOf(VERSION, 1, 0) + address + byteArrayOf((port shr 8).toByte(), port.toByte())
        }

        private fun dottedQuad(host: String): ByteArray? {
            val parts = host.split('.')
            if (parts.size != 4 || parts.any { part -> part.length !in 1..3 || part.any { it !in '0'..'9' } }) return null
            val values = parts.map(String::toInt).takeIf { values -> values.all { it <= 255 } } ?: return null
            return ByteArray(4) { values[it].toByte() }
        }

        /**
         * 16 bytes, an IPv4-mapped address too (Java hands those back as 4). Only a literal's characters reach
         * [InetAddress], so a mistyped `host:port` is refused rather than looked up.
         */
        private fun ipv6(host: String): ByteArray {
            fun Char.literal() = this == ':' || this == '.' || this in '0'..'9' || this in 'a'..'f' || this in 'A'..'F'
            if (host.first() == '.' || !host.all { it.literal() }) fail(TunnelProblem.BadHost)
            val bytes = runCatching { InetAddress.getByName(host).address }.getOrElse { fail(TunnelProblem.BadHost) }
            return if (bytes.size == 16) bytes else ByteArray(10) + byteArrayOf(-1, -1) + bytes
        }

        /** RFC 1928 section 6's reply codes, for what the user is shown. */
        fun reply(code: Int): String = when (code) {
            1 -> "general SOCKS server failure"
            2 -> "connection not allowed by ruleset"
            3 -> "network unreachable"
            4 -> "host unreachable"
            5 -> "connection refused"
            6 -> "TTL expired"
            7 -> "command not supported"
            8 -> "address type not supported"
            else -> "code $code"
        }

        private fun fail(problem: TunnelProblem): Nothing = throw TunnelException(problem)
    }
}

/** SOCKS5 on a real connection: dials the proxy and carries out a [Socks5Handshake]. */
internal object Socks5Dial {
    /** The connection to the proxy, carrying the target's bytes from now on. Throws [TunnelException]. */
    fun connect(host: String, port: Int, handshake: Socks5Handshake, timeoutMillis: Int = 10_000): Socket {
        val socket = Socket(Proxy.NO_PROXY)
        try {
            try {
                socket.connect(InetSocketAddress(host.trim().trim('[', ']'), port), timeoutMillis)
            } catch (error: IOException) {
                throw TunnelException(TunnelProblem.ProxyUnreachable(reason(error)), error)
            }
            socket.soTimeout = timeoutMillis
            socket.tcpNoDelay = true
            val input = DataInputStream(socket.getInputStream())
            val output = socket.getOutputStream()
            var step = handshake.start()
            while (true) {
                step = when (step) {
                    is Socks5Handshake.Step.Write -> {
                        output.write(step.bytes)
                        output.flush()
                        handshake.read(ByteArray(step.then).also(input::readFully))
                    }
                    is Socks5Handshake.Step.Read -> handshake.read(ByteArray(step.count).also(input::readFully))
                    Socks5Handshake.Step.Done -> break
                }
            }
            socket.soTimeout = 0
            return socket
        } catch (error: TunnelException) {
            socket.close()
            throw error
        } catch (error: SocketTimeoutException) {
            socket.close()
            throw TunnelException(TunnelProblem.ProxyTimedOut, error)
        } catch (error: EOFException) {
            socket.close()
            throw TunnelException(TunnelProblem.ProxyClosed, error)
        } catch (error: IOException) {
            socket.close()
            throw TunnelException(TunnelProblem.ProxyClosed, error)
        }
    }

    private fun reason(error: IOException): String = when (error) {
        is UnknownHostException -> "unknown host"
        is SocketTimeoutException -> "timed out"
        else -> error.message ?: error.javaClass.simpleName
    }
}
