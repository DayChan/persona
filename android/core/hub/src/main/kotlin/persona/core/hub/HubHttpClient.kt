package persona.core.hub

import persona.core.contracts.ProtocolCompatibility

import persona.core.contracts.Endpoint
import persona.core.contracts.Health
import persona.core.contracts.JsonCodec
import java.net.InetAddress
import java.net.Proxy
import java.security.KeyStore
import java.security.MessageDigest
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import java.util.Base64
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import javax.net.SocketFactory
import javax.net.ssl.SSLContext
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager
import kotlinx.coroutines.suspendCancellableCoroutine
import okhttp3.Call
import okhttp3.Callback
import okhttp3.ConnectionPool
import okhttp3.Dns
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import okhttp3.internal.tls.OkHostnameVerifier
import java.io.IOException
import kotlin.coroutines.resumeWithException

/**
 * Trust is established by pairing, never by the unauthenticated health response. [pins] are further fingerprints this
 * client accepts: the other hubs of a group, which were learned from a paired hub's authenticated hello. Which of them
 * an endpoint must present is checked per request ([HubHttpClient.health], [spkiFingerprint]).
 *
 * [system] is set only for a pairing without a fingerprint (`fp`): a hub without its own TLS behind a reverse proxy
 * whose certificate comes from a public CA. A certificate that matches no pin is then checked like any HTTPS site:
 * the system's trust store here, the host name in [HubHttpClient]'s verifier. That trusts every CA the system trusts
 * (the same as the iOS app and the web client) instead of one key; a pairing with `fp` never falls back to it.
 */
class SpkiTrustManager(
    fingerprint: String?, pins: Set<String> = emptySet(), private val system: X509TrustManager? = null,
) : X509TrustManager {
    private val expected = fingerprint?.let(::decodePin)
    @Volatile private var extra: List<ByteArray> = decodePins(pins)

    internal fun updatePins(pins: Set<String>) { extra = decodePins(pins) }

    fun matches(certificate: X509Certificate): Boolean {
        val digest = MessageDigest.getInstance("SHA-256").digest(certificate.publicKey.encoded)
        return expected?.let { MessageDigest.isEqual(it, digest) } == true || extra.any { MessageDigest.isEqual(it, digest) }
    }

    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        if (chain.isNullOrEmpty()) throw CertificateException("Hub SPKI mismatch")
        if (matches(chain[0])) return
        system?.checkServerTrusted(chain, authType) ?: throw CertificateException("Hub SPKI mismatch")
    }

    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        throw CertificateException("Client certificates are not supported")
    }

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()

    private companion object {
        fun decodePin(pin: String): ByteArray {
            require(pin.matches(Regex("[A-Za-z0-9_-]{43}"))) { "Invalid SPKI fingerprint" }
            return Base64.getUrlDecoder().decode(pin).also { bytes ->
                require(bytes.size == 32 && Base64.getUrlEncoder().withoutPadding().encodeToString(bytes) == pin)
            }
        }

        /** Mesh-provided pins that are malformed are ignored rather than failing the whole client. */
        fun decodePins(pins: Set<String>) = pins.mapNotNull { runCatching { decodePin(it) }.getOrNull() }
    }
}

/** The SPKI fingerprint (SHA-256, unpadded base64url) of [certificate]. */
internal fun spkiFingerprint(certificate: X509Certificate): String =
    Base64.getUrlEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(certificate.publicKey.encoded))

/** The platform's default trust manager: the system CAs (on Android, as the network security config allows). */
fun systemTrustManager(): X509TrustManager {
    val factory = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm())
    factory.init(null as KeyStore?)
    return factory.trustManagers.filterIsInstance<X509TrustManager>().first()
}

/** Names the manager a request is for (docs/protocol/protocol.md「Per-manager routing」); omitted: the default manager. */
const val ManagerHeader = "X-Persona-Manager"

/** The same as a query (WebSockets, links, one request pinned to a manager); empty: the default, sent as nothing. */
const val ManagerQuery = "manager"

/**
 * [this] naming the manager the calling coroutine's request is for (persona.core.contracts.api.RequestManager, an inbox
 * item's): on the request itself, so it holds wherever the request goes later; "" (the default's) is taken off again by
 * the client, together with any stamp a list noted for the path. Unchanged outside one, or when it names one already.
 */
internal suspend fun HttpUrl.withRequestManager(): HttpUrl {
    val named = kotlinx.coroutines.currentCoroutineContext()[persona.core.contracts.api.RequestManager]?.id
    return if (named == null || queryParameter(ManagerQuery) != null) this else newBuilder().addQueryParameter(ManagerQuery, named).build()
}

/**
 * `…/api/v1/assistant[/…]`, under whatever sub-path the hub sits at: the assistant page's requests. The health
 * space's (`?space=`, docs/assistant/health.md) are not: that page is the default manager's and has no switcher.
 */
internal fun isAssistantPath(url: HttpUrl): Boolean {
    if (url.queryParameter("space") != null) return false
    val api = apiSegments(url) ?: return false
    // One conversation's routes are not the page's: the hub sends them to the conversation's manager (byConversation),
    // which naming the page's pick would override.
    return api.firstOrNull() == "assistant" && !(api.size > 2 && api[1] == "conversations")
}

/** The segments after `api/v1`, under whatever sub-path the hub sits at; null for another path. */
private fun apiSegments(url: HttpUrl): List<String>? {
    val segments = url.pathSegments
    for (i in 0 until segments.size - 1) {
        if (segments[i] == "api" && segments[i + 1] == "v1") return segments.subList(i + 2, segments.size)
    }
    return null
}

/**
 * The `X-Persona-Manager` a request to [url] carries, if any (docs/protocol/protocol.md「Per-manager routing」): the assistant
 * page's pick on its routes; else a pin or a list's stamp (an inbox item, an extension's page, a conversation the page
 * listed) of [hubId]'s hub.
 */
internal fun managerHeaderFor(url: HttpUrl, hubId: String?): String? {
    val choice = persona.core.contracts.api.AssistantManagerChoice
    if (isAssistantPath(url)) choice.header(hubId)?.let { return it }
    return apiSegments(url)?.let { choice.itemHeader(it, hubId) }
}

/** How TLS pins are keyed: the host as OkHttp hands it to the hostname verifier, and the port. */
internal fun tlsKey(host: String, port: Int) = "${host.lowercase()}:$port"

/** Additional VPN ranges must be supplied by the caller; endpoint labels are not trusted. */
class LocalAddressPolicy(private val isVpnAddress: (InetAddress) -> Boolean = { false }) {
    fun allows(address: InetAddress): Boolean {
        val bytes = address.address
        val ula = bytes.size == 16 && (bytes[0].toInt() and 0xfe) == 0xfc
        return address.isLoopbackAddress || address.isSiteLocalAddress || address.isLinkLocalAddress || ula || isVpnAddress(address)
    }
}

/**
 * One instance owns all HTTP and WebSocket traffic for one paired hub, or for one group of hubs: [pins] adds the other
 * hubs' fingerprints to the trust manager, and callers check per endpoint which hub answered.
 */
class HubHttpClient(
    val hubId: String?,
    val fingerprint: String?,
    version: String,
    val localAddressPolicy: LocalAddressPolicy = LocalAddressPolicy(),
    dns: Dns = Dns.SYSTEM,
    pins: Set<String> = emptySet(),
    socketFactory: SocketFactory = SocketFactory.getDefault(),
    /** Where HTTPS without a pinned fingerprint is checked ([SpkiTrustManager]); tests pass their own CA. */
    systemTrust: () -> X509TrustManager = ::systemTrustManager,
) : AutoCloseable {
    val client: OkHttpClient
    /** No fingerprint from pairing: HTTPS endpoints are checked against the system CAs and the host name. */
    val systemTrusted: Boolean = fingerprint == null
    private val trust = SpkiTrustManager(fingerprint, pins, if (systemTrusted) systemTrust() else null)
    @Volatile private var extraPins = pins
    /** For a group: which hub's pin each TLS endpoint must present, set by the connection. */
    private val expectedPins = java.util.concurrent.ConcurrentHashMap<String, String>()
    /** The leaf pin each TLS endpoint presented last. OkHttp's cleaned handshake has no certificates for a pinned self-signed hub. */
    private val seenPins = java.util.concurrent.ConcurrentHashMap<String, String>()

    /** The other hubs of the group changed. */
    internal fun updatePins(pins: Set<String>) {
        extraPins = pins
        trust.updatePins(pins)
    }

    /** Per endpoint (`url` of a candidate) the pin its hub must present; endpoints not listed accept any trusted pin. */
    internal fun expectPins(pins: Map<HttpUrl, String>) {
        val keys = pins.entries.filter { it.key.isHttps }.associate { tlsKey(it.key.host, it.key.port) to it.value }
        expectedPins.keys.retainAll(keys.keys)
        expectedPins.putAll(keys)
    }

    /** The pin the endpoint at [url] presented in its last TLS handshake. */
    internal fun seenPin(url: HttpUrl): String? = seenPins[tlsKey(url.host, url.port)]

    init {
        val tls = SSLContext.getInstance("TLS").apply { init(null, arrayOf(trust), null) }
        client = OkHttpClient.Builder()
            .sslSocketFactory(tls.socketFactory, trust)
            .hostnameVerifier { host, session ->
                val leaf = session.peerCertificates.firstOrNull() as? X509Certificate
                if (leaf != null && !trust.matches(leaf) && systemTrusted) {
                    // Passed the system CAs in the trust manager: the name must be this endpoint's, as for any site.
                    OkHostnameVerifier.verify(host, session)
                } else if (leaf == null || !trust.matches(leaf)) false else {
                    val key = tlsKey(host, session.peerPort)
                    val pin = spkiFingerprint(leaf)
                    seenPins[key] = pin
                    // A group's client trusts every hub's pin; an endpoint of a known hub must present that hub's.
                    expectedPins[key]?.let { it == pin } ?: true
                }
            }
            .dns(dns)
            .socketFactory(socketFactory)
            .proxy(Proxy.NO_PROXY)
            .followRedirects(false)
            .followSslRedirects(false)
            .retryOnConnectionFailure(false)
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(15, TimeUnit.SECONDS)
            .pingInterval(0, TimeUnit.SECONDS)
            .addInterceptor { chain ->
                val request = chain.request()
                if (request.url.username.isNotEmpty() || request.url.password.isNotEmpty()) {
                    throw HubFailure(HubError.Protocol, retryable = false)
                }
                // The language the app shows, so the hub answers in it (docs/protocol/i18n.md); also on WebSocket upgrades.
                val builder = request.newBuilder().header("Accept-Language", persona.core.contracts.ClientLanguage.tag)
                ProtocolCompatibility.clientHeaders.forEach { builder.header(it, "android/$version") }
                ProtocolCompatibility.operationHeaders.firstNotNullOfOrNull { request.header(it) }?.let { id ->
                    ProtocolCompatibility.operationHeaders.forEach { builder.header(it, id) }
                }
                // The manager the assistant page shows (docs/assistant/managers.md): its requests name it; none for the default.
                // A thing a merged list showed as another manager's (an inbox item, an extension) names that one. A
                // request pinned to one (`?manager=`) keeps it: the default's (empty) goes without either.
                var url = request.url
                val pinned = url.queryParameter(ManagerQuery)
                if (pinned != null) {
                    if (pinned.isEmpty()) url = url.newBuilder().removeAllQueryParameters(ManagerQuery).build().also(builder::url)
                } else if (request.header(ManagerHeader) == null) managerHeaderFor(url, hubId)?.let { builder.header(ManagerHeader, it) }
                if (!url.isHttps) {
                    // WebSocket upgrades skip network interceptors. Freeze the checked DNS
                    // result into the URL so the upgrade cannot resolve to a different peer.
                    val peer = dns.lookup(url.host).firstOrNull(localAddressPolicy::allows)
                        ?: throw HubFailure(HubError.TlsRequired, retryable = false)
                    val authority = url.toString().substringAfter("://").substringBefore('/')
                    builder.url(url.newBuilder().host(peer.hostAddress!!).build()).header("Host", authority)
                }
                chain.proceed(builder.build())
            }
            .addNetworkInterceptor { chain ->
                // Inspect the actual peer before sending headers, including after DNS resolution.
                if (!chain.request().url.isHttps) {
                    val peer = chain.connection()?.route()?.socketAddress?.address
                    if (peer == null || !localAddressPolicy.allows(peer)) {
                        throw HubFailure(HubError.TlsRequired, retryable = false)
                    }
                }
                chain.proceed(chain.request())
            }
            .build()
    }

    /**
     * The unauthenticated identity check: TLS pin (or a local peer for http), then the hub id. [fresh] uses a new
     * connection pool so the time includes connecting and the TLS handshake (settings tests); pinning and the
     * plaintext guard apply as always.
     */
    suspend fun health(
        endpoint: Endpoint, fresh: Boolean = false,
        /** The hub this endpoint belongs to; another group member's id answering here is a mismatch. */
        expectedHubId: String? = hubId,
        /** The pin the TLS leaf must have. Defaults to this client's own when it also trusts other hubs' pins. */
        expectedPin: String? = null,
    ): Health {
        val url = endpoint.baseUrl().hubUrl("/api/v1/health")
        // A pool of its own, keeping its one connection a second after the call: with no idle connection allowed, OkHttp
        // closes it as soon as the body's Content-Length bytes are read, and over HTTP/2 the stream's end can still be on
        // its way in a frame of its own; the read that waits for it then fails ("Connection failed", #1519).
        val caller = if (!fresh) client else client.newBuilder().connectionPool(ConnectionPool(1, 1, TimeUnit.SECONDS)).build()
        caller.newCall(Request.Builder().url(url).build()).awaitResponse().use { response ->
            if (url.isHttps) {
                // The trust manager accepts every pin of the group; the leaf must be this endpoint's own hub's.
                val pin = expectedPin ?: fingerprint.takeIf { extraPins.isNotEmpty() }
                if (pin != null && seenPin(url) != pin) throw HubFailure(HubError.CertificateMismatch, retryable = false)
            }
            if (!response.isSuccessful) throw HubFailure.from(response)
            val health = runCatching { JsonCodec.decodeFromString<Health>(response.body!!.string()) }
                .getOrElse { throw HubFailure(HubError.Protocol, cause = it) }
            if (!health.ok) throw HubFailure(HubError.Server)
            if (expectedHubId != null && health.hubId != expectedHubId) throw HubFailure(HubError.HubMismatch, retryable = false)
            return health
        }
    }

    /**
     * Drops the pooled connections. After the WebSocket went silent (a relay or NAT dropping the flow without a reset)
     * the idle HTTP connections on that path are just as dead, and the next health probe would wait out the read
     * timeout on one of them before reconnecting (#2124: 102s instead of 45s). Off the caller's thread, as in [close].
     */
    fun evictConnections() {
        if (closed.get()) return
        runCatching { client.dispatcher.executorService.execute { client.connectionPool.evictAll() } }
    }

    private val closed = AtomicBoolean()

    override fun close() {
        if (!closed.compareAndSet(false, true)) return
        client.dispatcher.cancelAll()
        // Closing a TLS connection writes close_notify, and callers close from the main thread (pairing's finally):
        // NetworkOnMainThreadException, thrown after the pairing was saved. The dispatcher's thread evicts instead.
        val executor = client.dispatcher.executorService
        executor.execute { client.connectionPool.evictAll() }
        executor.shutdown()
    }
}

/**
 * The endpoint's base: its origin and, for a hub behind a reverse proxy at a sub-path (`https://hub.example.com/persona`,
 * the proxy strips the prefix), that path prefix. Every API URL is built from it with [hubUrl], never with
 * `resolve("/api/…")`, which would drop the prefix.
 */
internal fun Endpoint.baseUrl(): HttpUrl = try {
    url.toHttpUrl().also {
        require(it.username.isEmpty() && it.password.isEmpty() && it.query == null && it.fragment == null)
    }
} catch (error: IllegalArgumentException) {
    throw HubFailure(HubError.Protocol, retryable = false, cause = error)
}

/** The base's path prefix without its trailing slash: "" at the root, "/persona" under a sub-path. */
private val HttpUrl.hubPrefix: String get() = encodedPath.trimEnd('/')

/**
 * The URL of the hub's [path] (absolute and already encoded, `/api/v1/…`, optionally with `?query`) under this base,
 * keeping the base's path prefix: `https://hub.example.com/persona` + `/api/v1/ws` is
 * `https://hub.example.com/persona/api/v1/ws`. A trailing slash on the base makes no difference.
 */
fun HttpUrl.hubUrl(path: String): HttpUrl {
    require(path.startsWith("/") && !path.startsWith("//") && '#' !in path) { "Expected an absolute hub path" }
    val query = path.substringAfter('?', missingDelimiterValue = "").takeIf { '?' in path }
    return newBuilder().encodedPath(hubPrefix + path.substringBefore('?')).encodedQuery(query).fragment(null).build()
}

/**
 * The hub path (`/api/…`) of [url] when it is on this base: same scheme, host and port, under the base's path prefix.
 * Null for anything else, so a same-host check also keeps requests inside the prefix.
 */
fun HttpUrl.hubPath(url: HttpUrl): String? {
    if (url.scheme != scheme || url.host != host || url.port != port || url.username.isNotEmpty() || url.password.isNotEmpty()) return null
    val prefix = hubPrefix
    if (!url.encodedPath.startsWith("$prefix/")) return null
    return url.encodedPath.removePrefix(prefix)
}

/**
 * A reference the hub hands out (a media `src`, an update's `download_url`) as a URL under this base. The hub does not
 * know the proxy's prefix, so its absolute paths (`/api/…`) are under the base's prefix; a full URL is taken as is and
 * must stay on this base ([hubPath]). Null for anything else.
 */
fun HttpUrl.hubResolve(reference: String): HttpUrl? {
    val target = if (reference.startsWith("/") && !reference.startsWith("//")) runCatching { hubUrl(reference) }.getOrNull()
        else resolve(reference)
    return target?.takeIf { hubPath(it) != null && it.fragment == null }
}

/**
 * Callers parse the response wherever they run, often on the main thread (Android forbids network reads there:
 * NetworkOnMainThreadException), so by default the body is read here on OkHttp's thread. Streaming downloads pass
 * buffered = false and read inside Dispatchers.IO themselves.
 */
internal suspend fun Call.awaitResponse(buffered: Boolean = true): Response = suspendCancellableCoroutine { continuation ->
    continuation.invokeOnCancellation { cancel() }
    enqueue(object : Callback {
        override fun onFailure(call: Call, e: IOException) {
            if (!continuation.isCancelled) continuation.resumeWithException(HubFailure.from(e))
        }

        override fun onResponse(call: Call, response: Response) {
            val ready = if (!buffered) response else try {
                val body = response.body
                if (body == null) response
                else response.newBuilder().body(body.use { it.bytes() }.toResponseBody(body.contentType())).build()
            } catch (e: IOException) {
                response.close()
                if (!continuation.isCancelled) continuation.resumeWithException(HubFailure.from(e))
                return
            }
            continuation.resume(ready) { _, value, _ -> value.close() }
        }
    })
}
