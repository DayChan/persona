package persona.core.hub

import persona.core.contracts.Endpoint
import java.net.InetAddress
import java.net.ServerSocket
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlinx.coroutines.delay
import kotlinx.coroutines.withTimeout
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest

/** A plain-HTTP hub on loopback for route tests: counts health probes and WebSocket upgrades. */
internal class LoopbackHub(
    healthy: Boolean = true,
    /** Gets this hub's [url] at every health probe: one log shared by a test's hubs shows the order tried. */
    private val log: MutableCollection<String>? = null,
    /** The `endpoints` of the hello this hub sends. */
    private val endpoints: () -> String = { "[]" },
    /** The hub id health and hello report. */
    val hubId: String = "test-hub",
    /** Health's `version`; a hub leaves it out for a client it counts as the internet. */
    private val version: String? = null,
) : AutoCloseable {
    @Volatile var healthy = healthy
    /** Credentials this hub refuses with 401 (everything but health). */
    @Volatile var refused: Set<String> = emptySet()
    /** Every request's path and Authorization header, in arrival order. */
    val authorizations = java.util.concurrent.ConcurrentLinkedQueue<Pair<String, String?>>()
    /** How long a healthy hub takes to answer health. */
    @Volatile var answerMillis = 0L
    val probes = AtomicInteger()
    val sockets = AtomicInteger()
    /** WebSockets opened so far; the latest is [socket]. */
    val opened = AtomicInteger()
    /** The hub's end of the latest WebSocket, to send events with. */
    @Volatile var socket: WebSocket? = null
    private val server = MockWebServer().apply {
        dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                authorizations.add(request.requestUrl!!.encodedPath to request.getHeader("Authorization"))
                if (request.path != "/api/v1/health" && request.getHeader("Authorization")?.removePrefix("Bearer ") in refused) {
                    return MockResponse().setResponseCode(401).setBody("""{"error":"expired","code":"invalid_token","retryable":false}""")
                }
                return when (request.requestUrl!!.encodedPath) {
                "/api/v1/health" -> {
                    probes.incrementAndGet()
                    log?.add(url)
                    // Not the constructor parameter of the same name: the property, which tests change.
                    if (!this@LoopbackHub.healthy) MockResponse().setResponseCode(503)
                    else MockResponse().setBody("""{"ok":true,"hub_id":"$hubId"${version?.let { ""","version":"$it"""" }.orEmpty()}}""")
                        .setHeadersDelay(answerMillis, TimeUnit.MILLISECONDS)
                }
                "/api/v1/ws" -> {
                    sockets.incrementAndGet()
                    MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) {
                            socket = webSocket
                            webSocket.send("""{"type":"hello","hub_id":"$hubId","features":[],"endpoints":${endpoints()}}""")
                            opened.incrementAndGet()
                        }
                    })
                }
                "/api/v1/ping" -> MockResponse().setBody("{}")
                else -> MockResponse().setResponseCode(404)
                }
            }
        }
        start(InetAddress.getLoopbackAddress(), 0)
    }
    /** As the pairing stores it (trailing slash), and in canonical form. */
    val paired: String = server.url("/").newBuilder().host(InetAddress.getLoopbackAddress().hostAddress!!).build().toString()
    val url: String = endpointKey(paired)
    fun endpoint(kind: String) = Endpoint(paired, kind)
    /** This hub as an entry of the hub's endpoint list. */
    fun wire(kind: String) = """{"url":"$url","kind":"$kind"}"""
    /** Sends an `endpoints` event over the latest WebSocket. */
    fun advertise(endpoints: String) = check(socket!!.send("""{"type":"endpoints","endpoints":$endpoints}"""))
    override fun close() = server.close()
}

/** A loopback port nothing listens on: connections are refused at once. */
internal fun closedPort() = ServerSocket(0, 1, InetAddress.getLoopbackAddress()).use { it.localPort }

/** Polls [condition] until it holds; fails after [millis]. */
internal suspend fun eventually(millis: Long = 5000, condition: () -> Boolean) = withTimeout(millis) { while (!condition()) delay(10) }
