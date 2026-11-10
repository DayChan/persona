package persona.core.hub.tunnel

import java.io.IOException
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.Collections
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicReference
import kotlin.concurrent.thread
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import okhttp3.OkHttpClient
import okhttp3.Request
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.LocalProxyRoute
import persona.core.contracts.api.TunnelException
import persona.core.contracts.api.TunnelProblem

/**
 * A loopback listener that carries every connection made to it through a SOCKS5 proxy to the hub
 * (docs/operations/network.md "Connecting to hub through a local SOCKS5 proxy"): each connection dials the proxy, has it
 * CONNECT to the hub's address, then the bytes go both ways untouched, HTTP and WebSocket alike. Nothing is read from a
 * connection until its way to the hub is there.
 */
class LocalProxyForward(val dial: Dial) : AutoCloseable {
    data class Dial(
        val proxyHost: String,
        val proxyPort: Int,
        val targetHost: String,
        val targetPort: Int,
        val username: String? = null,
        val password: String = "",
    ) {
        fun handshake() = Socks5Handshake(targetHost, targetPort, username?.let { it to password })

        /** `host:port` of the hub as the proxy is asked for it (IPv6 in brackets). */
        val target: String get() = LocalProxyRoute.bracketed(targetHost) + ":$targetPort"

        override fun toString(): String = "Dial($proxyHost:$proxyPort → $target, login=${username != null})"

        companion object {
            fun of(route: LocalProxyRoute, hubAddress: String): Dial {
                val (host, port) = route.target(hubAddress)
                return Dial(route.host.trim(), route.port, host, port,
                    route.username.takeIf { it.isNotEmpty() }, route.password)
            }
        }
    }

    @Volatile private var server: ServerSocket? = null
    @Volatile private var stopped = false
    private val sockets = Collections.synchronizedSet(HashSet<Socket>())

    /** How each connection's dial went (null: it got through), on the connection's thread. Set before [listen]. */
    @Volatile var onDial: ((TunnelProblem?) -> Unit)? = null

    /** The loopback port it listens on; 0 before [listen]. */
    val localPort: Int get() = server?.localPort?.takeIf { it > 0 } ?: 0

    val listening: Boolean get() = !stopped && server?.isClosed == false

    /**
     * One connection through the proxy to the hub's address, closed again: a proxy that is not running, or a login it
     * refuses, fails here. Whether the hub answers there it does not say: many proxies accept the CONNECT first.
     */
    fun check() {
        Socks5Dial.connect(dial.proxyHost, dial.proxyPort, dial.handshake()).close()
    }

    /** Listens on 127.0.0.1: at [preferred] when it is free, else on any free one. Already listening: that port. */
    fun listen(preferred: Int = 0): Int {
        server?.let { return it.localPort }
        val loopback = InetAddress.getByName("127.0.0.1")
        fun bind(port: Int) = ServerSocket().apply {
            reuseAddress = true
            try { bind(InetSocketAddress(loopback, port)) } catch (error: IOException) { close(); throw error }
        }
        val socket = (if (preferred != 0) runCatching { bind(preferred) }.getOrNull() else null) ?: bind(0)
        server = socket
        if (stopped) socket.close()
        thread(isDaemon = true, name = "Local proxy forward ${socket.localPort}") { accept(socket) }
        return socket.localPort
    }

    override fun close() {
        stopped = true
        runCatching { server?.close() }
        synchronized(sockets) { sockets.toList() }.forEach { runCatching { it.close() } }
    }

    private fun accept(server: ServerSocket) {
        while (!stopped) {
            val local = try { server.accept() } catch (_: IOException) { break }
            if (!track(local)) break
            thread(isDaemon = true, name = "Local proxy connection") { carry(local) }
        }
    }

    /** On a thread of its own, where anything thrown would end the app: a close under way only drops the connection. */
    private fun carry(local: Socket) {
        val remote = try {
            Socks5Dial.connect(dial.proxyHost, dial.proxyPort, dial.handshake())
        } catch (error: TunnelException) {
            onDial?.invoke(error.problem)
            drop(local)
            return
        }
        onDial?.invoke(null)
        if (!track(remote)) { drop(local); return }
        try { local.tcpNoDelay = true } catch (_: IOException) { drop(local); drop(remote); return }
        val ended = AtomicInteger()
        fun end() { if (ended.incrementAndGet() == 2) { drop(local); drop(remote) } }
        thread(isDaemon = true, name = "Local proxy upstream") { pump(local, remote); end() }
        pump(remote, local)
        end()
    }

    /** Copies [from] to [to] until [from] ends, then ends [to]'s output; a broken connection closes both. */
    private fun pump(from: Socket, to: Socket) {
        try {
            from.getInputStream().copyTo(to.getOutputStream(), 16 * 1024)
            to.shutdownOutput()
        } catch (_: IOException) {
            drop(from)
            drop(to)
        }
    }

    private fun track(socket: Socket): Boolean {
        sockets += socket
        if (stopped) { drop(socket); return false }
        return true
    }

    private fun drop(socket: Socket) {
        sockets -= socket
        runCatching { socket.close() }
    }

    companion object {
        /**
         * The hub answers through [route] ("Test connection and save"): the proxy is asked for the hub's address once,
         * then the hub's health check goes through a forward of its own. Kept only when it is this hub ([hubId]) and it
         * takes plain HTTP from the proxy's side: a hub that sees the proxy as the internet answers its health check
         * all the same, then refuses every request.
         */
        fun verify(route: LocalProxyRoute, hubAddress: String, hubId: String?) {
            LocalProxyForward(Dial.of(route, hubAddress)).use { forward ->
                val dialFailure = AtomicReference<TunnelProblem?>()
                forward.onDial = { problem -> if (problem != null) dialFailure.set(problem) }
                forward.check()
                val port = forward.listen()
                val target = forward.dial.target
                val url = HubTunnel.baseUrl(hubAddress, port) + "/api/v1/health"
                val client = OkHttpClient.Builder()
                    .proxy(java.net.Proxy.NO_PROXY)
                    .followRedirects(false)
                    .followSslRedirects(false)
                    .retryOnConnectionFailure(false)
                    .callTimeout(10, TimeUnit.SECONDS)
                    .build()
                val body = try {
                    client.newCall(Request.Builder().url(url).build()).execute().use { response ->
                        if (response.code != 200) throw TunnelException(TunnelProblem.NotAHub(target))
                        response.body?.string().orEmpty()
                    }
                } catch (error: TunnelException) {
                    throw error
                } catch (error: IOException) {
                    throw TunnelException(dialFailure.get() ?: TunnelProblem.NoAnswer(target), error)
                } finally {
                    client.dispatcher.executorService.shutdown()
                    client.connectionPool.evictAll()
                }
                val health = runCatching { JsonCodec.parseToJsonElement(body) as? JsonObject }.getOrNull()
                if ((health?.get("ok") as? JsonPrimitive)?.booleanOrNull != true) throw TunnelException(TunnelProblem.NotAHub(target))
                val answered = (health["hub_id"] as? JsonPrimitive)?.contentOrNull
                if (!hubId.isNullOrEmpty() && !answered.isNullOrEmpty() && answered != hubId) {
                    throw TunnelException(TunnelProblem.OtherHub(target))
                }
                // The hub leaves its version out for a source it counts as the internet, where it refuses plain HTTP.
                if (health["version"] == null) throw TunnelException(TunnelProblem.SeenAsInternet(target))
            }
        }
    }
}
