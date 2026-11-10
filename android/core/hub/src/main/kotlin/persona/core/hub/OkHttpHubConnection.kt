package persona.core.hub

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.coroutines.flow.first
import persona.core.contracts.*
import persona.core.contracts.api.*
import java.security.MessageDigest
import java.time.Instant
import java.util.Base64
import java.util.concurrent.atomic.AtomicLong
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.*
import okhttp3.*
import kotlin.random.Random
import persona.core.hub.tunnel.HubTunnel
import persona.core.hub.tunnel.HubWay
import persona.core.hub.tunnel.HubWays

data class HubNetwork(
    val id: String,
    val available: Boolean = true,
    val homeWifi: Boolean = false,
    val localNetworkPermissionGranted: Boolean = true,
    // Include global IPv6 hosts on the same local link when permission is required.
    val localHosts: Set<String> = emptySet(),
    val transport: HubNetworkTransport = HubNetworkTransport.Other,
)

data class HubConnectionConfig(
    val hubId: String?,
    val fingerprint: String?,
    val token: String,
    val endpoints: List<Endpoint>,
    val version: String,
    val deviceId: String? = null,
    val build: JsonElement? = null,
    val os: JsonObject? = null,
    val requiredFeatures: Set<String> = emptySet(),
    val clientFeatures: Set<String> = emptySet(),
    val heartbeatPolicy: HeartbeatPolicy = HeartbeatPolicy(),
    /** How long a drop keeps showing Online and how fast a restarting hub is retried (#1574). */
    val reconnectPolicy: ReconnectPolicy = ReconnectPolicy(),
    /**
     * The other hubs of this hub's group, tried after this hub's own candidates in list order (docs/android/android.md "Multiple hubs").
     * Empty for a hub without a group: everything then behaves exactly as for a single paired hub.
     */
    val members: List<HubMember> = emptyList(),
    /** Whether this connection's own hub is the group's center. */
    val center: Boolean = false,
    /** The hub id of the group's center, if known. */
    val centerHubId: String? = null,
    /**
     * Ways through to this hub and the group's other hubs when their addresses do not answer (docs/operations/network.md
     * "Connecting to hub through SSH"), in the order tried. Opened through [OkHttpHubConnection]'s `ways`.
     */
    val tunnels: List<HubTunnel> = emptyList(),
)

/** Call onReady to restore subscriptions and reconcile pending interactions after hello. */
class OkHttpHubConnection(
    private val config: HubConnectionConfig,
    initialNetwork: HubNetwork,
    val http: HubHttpClient = HubHttpClient(config.hubId, config.fingerprint, config.version,
        pins = config.members.mapNotNullTo(LinkedHashSet()) { it.fingerprint }),
    dispatcher: CoroutineDispatcher = Dispatchers.IO,
    private val onReady: suspend (OkHttpHubConnection) -> Unit = {},
    // Android callers should inject SystemClock.elapsedRealtime, which includes deep sleep.
    private val clockMillis: () -> Long = { System.nanoTime() / 1_000_000 },
    private val webSocketFactory: WebSocket.Factory = http.client,
    /** How often a running socket looks for an answer from a candidate listed above it; shorter only in tests. */
    private val recheckMillis: Long = RECHECK_MILLIS,
    /**
     * A hub of the group refused its credential (401): it is no longer tried by this connection. [ownToken] is false
     * when the hub only rejected the borrowed group credential. Not called for a connection without [HubConnectionConfig.members].
     */
    private val onMemberRevoked: (hubId: String, ownToken: Boolean) -> Unit = { _, _ -> },
    /** Runs the tunnels of [HubConnectionConfig.tunnels]; without it they are not used. */
    private val ways: HubWays? = null,
    /** How often a socket that went through a tunnel because no address answered asks the addresses again. */
    private val tunnelRecheckMillis: Long = HubWay.RECHECK_MILLIS,
) : HubConnection, ConnectionDiagnosticsSource, AutoCloseable {
    private val scope = CoroutineScope(SupervisorJob() + dispatcher)
    private val lock = Any()
    private val selector = HubRouteSelector(clockMillis) { endpoint -> probe(endpoint) }
    @Volatile private var members: List<HubMember> = config.members
    @Volatile private var centerHubId: String? = config.centerHubId
        ?: if (config.center) config.hubId else config.members.firstOrNull { it.center }?.hubId
    val centerId: String? get() = centerHubId ?: if (config.center) config.hubId else members.firstOrNull { it.center }?.hubId
    /** Hubs that refused their credential, with the member as it was then; a changed member is tried again. */
    private val excluded = HashMap<String, HubMember>()
    @Volatile private var homeExcluded = false
    @Volatile private var tunnels: List<HubTunnel> = config.tunnels
    /** By the key of a tunnel's loopback address: the tunnel and the hub that answers through it. */
    private val tunnelRoutes = java.util.concurrent.ConcurrentHashMap<String, Pair<HubTunnel, Owner>>()
    private val _memberEndpoints = MutableStateFlow<Map<String, List<Endpoint>>>(emptyMap())
    /** Candidates the other hubs of the group sent in their hello / endpoints events while connected through them. */
    val memberEndpoints = _memberEndpoints.asStateFlow()
    /** Every address this hub and the group's other hubs go by: the pairing's, the lists they sent, the members'. */
    fun knownAddresses(): List<String> =
        (config.endpoints + endpoints.value + members.flatMap { it.endpoints } + memberEndpoints.value.values.flatten()).map { it.url }.distinct()
    /** The hub a selected route belongs to; kept after the route is cleared, so a failure is charged to it. */
    @Volatile private var routeOwner: Pair<HubRoute, Owner>? = null
    @Volatile private var attemptOwner: Owner? = null
    private val _state = MutableStateFlow(ConnectionState.Connecting)
    override val state = _state.asStateFlow()
    private val _serverFeatures = MutableStateFlow<Set<String>>(emptySet())
    val serverFeatures = _serverFeatures.asStateFlow()
    /** A pairing-owned consumer for phone calls, before typed event decoding. */
    @Volatile var phoneMessageHandler: ((JsonObject) -> Boolean)? = null
    private val _events = MutableSharedFlow<ServerEvent>()
    override val events = _events.asSharedFlow()
    /** Sockets opened so far; each one's events are queued with its number. */
    private val socketCount = AtomicLong()
    /** The latest socket that started over: events of earlier sockets still queued are dropped. */
    private val startedOver = AtomicLong()
    /**
     * Events on their way to [events], in the order they came, each with the socket it came on. The socket's loop
     * only queues them: a subscriber that takes long over one (a reload over the network, a lock) must not hold up the
     * loop, or a drop, the end of the catch-up and the heartbeat go unhandled and the connection never reconnects nor
     * comes back Online (#3627). A socket that starts over (not resumed) drops what earlier sockets still had queued:
     * what reacts to coming back Online refetches it, and an old event delivered after that refetch would undo it (a
     * question answered meanwhile shown again). The one event already being handed over is not taken back: the
     * subscribers that are keeping up took it at once, before any refetch, and only the slow one gets it late.
     * (Cancelling an emit that waits on a slow subscriber is not an option: it breaks the flow, the collector fails.)
     */
    private val outgoing = Channel<Pair<Long, ServerEvent>>(Channel.UNLIMITED).also { queue ->
        scope.launch { for ((socket, event) in queue) if (socket >= startedOver.get()) _events.emit(event) }
    }
    private val _endpoints = MutableStateFlow(config.endpoints)
    /** The pairing's candidates until the hub sends its own list (hello, endpoints events); manual ones are separate. */
    val endpoints = _endpoints.asStateFlow()
    private val _advertised = MutableStateFlow<Set<String>>(emptySet())
    /** Keys of the list the hub sent most recently: tells the hub's candidates from the pairing's. */
    val advertised = _advertised.asStateFlow()
    private val _overrides = MutableStateFlow(EndpointOverrides())
    /** Route choices made on this phone; a list from the hub never changes them. */
    val overrides = _overrides.asStateFlow()
    private val _route = MutableStateFlow<HubRoute?>(null)
    val route = _route.asStateFlow()
    private val _tunnel = MutableStateFlow<HubTunnel?>(null)
    /** The tunnel [route] goes through; null while it goes straight to a hub, or there is none. */
    val tunnel = _tunnel.asStateFlow()
    private val _failure = MutableStateFlow<HubFailure?>(null)
    val failure = _failure.asStateFlow()
    private val _heartbeat = MutableStateFlow(Heartbeat(20, 15))
    val heartbeat = _heartbeat.asStateFlow()
    private val _weakNetwork = MutableStateFlow(false)
    val weakNetwork = _weakNetwork.asStateFlow()
    private val _diagnostics = MutableStateFlow(ConnectionDiagnostics())
    override val diagnostics = _diagnostics.asStateFlow()
    /** The credential for the hub the current route reaches: its own token, else the group credential. */
    val token: String get() = ownerOf(_route.value).token

    @Volatile private var network = initialNetwork
    @Volatile private var foreground = true
    @Volatile private var socket: WebSocket? = null
    @Volatile private var mailbox: Channel<Signal>? = null
    /** Completed by [onWake] to cut a retry backoff short: the user is looking at hub content right now. */
    @Volatile private var retryWait: CompletableDeferred<Unit>? = null
    @Volatile private var closed = false
    @Volatile private var revoked = false
    private var runner: Job? = null
    private var pendingState: Job? = null
    private var stateTarget: ConnectionState? = null
    private var stateDeadline = 0L
    /** When the published Online started being held back from a change (clock millis); null while nothing is held. */
    private var heldSince: Long? = null
    private val pingIds = AtomicLong()
    private val policy = config.reconnectPolicy
    @Volatile override var lastHello: Hello? = null
        private set
    @Volatile override var resumed = false
        private set
    /**
     * The hub's broadcast stream this client can resume (#1574) and the last broadcast of it seen; kept across
     * sockets. The stream is only resumable once a socket that started over reached its first sync.
     */
    @Volatile private var eventStream: String? = null
    private val lastEventSeq = AtomicLong(0)
    /** When the hub last said it is restarting (clock millis): quick retries and the longer grace until the window ends. */
    @Volatile private var restartSince: Long? = null
    @Volatile private var restartAttempts = 0

    /** Where the next socket resumes from: the stream and its last broadcast seen. */
    internal val resumePoint: Pair<String, Long>? get() = eventStream?.let { it to lastEventSeq.get() }

    init { updateDiagnostics { it }; reconnect() }

    override suspend fun readDiagnostics(): ConnectionDiagnostics {
        val deviceId = config.deviceId ?: return diagnostics.value
        if (route.value == null) return diagnostics.value
        val path = HttpUrl.Builder().scheme("https").host("example.com")
            .addPathSegments("api/v1/devices").addPathSegment(deviceId).addPathSegment("sessions")
            .build().encodedPath
        val rows = try {
            request(path).use { response ->
                JsonCodec.parseToJsonElement(response.body!!.string()).jsonObject["sessions"] as? JsonArray
            }
        } catch (error: HubFailure) {
            if (error.status == 404) return diagnostics.value
            throw error
        }
        val sessions = rows.orEmpty().map { row ->
            val value = row.jsonObject
            fun text(key: String) = value[key]?.jsonPrimitive?.contentOrNull
            DeviceConnectionSession(text("connected_at").orEmpty(), text("disconnected_at"), text("reason"))
        }
        updateDiagnostics { it.copy(sessions = sessions) }
        return diagnostics.value
    }

    private fun updateDiagnostics(change: (ConnectionDiagnostics) -> ConnectionDiagnostics) {
        _diagnostics.update { previous ->
            val value = change(previous)
            value.copy(unmeasuredFields = buildMap {
                val unmeasured = CoreText.of(Key.NotMeasured)
                if (value.currentPath == null) put("currentPath", unmeasured)
                if (value.fingerprint == null) put("fingerprint", unmeasured)
                if (value.heartbeatSeconds == null) put("heartbeatSeconds", unmeasured)
                if (value.foregroundHeartbeatSeconds == null) put("foregroundHeartbeatSeconds", unmeasured)
                if (value.backgroundHeartbeatSeconds == null) put("backgroundHeartbeatSeconds", unmeasured)
                if (value.pongTimeoutSeconds == null) put("pongTimeoutSeconds", unmeasured)
                if (value.lastReceivedAt == null) put("lastReceivedAt", unmeasured)
                if (value.lastDisconnectReason == null) put("lastDisconnectReason", unmeasured)
            })
        }
    }

    private fun clearRoute() {
        _route.value = null
        _tunnel.value = null
        updateDiagnostics { it.copy(currentPath = null, fingerprint = null, heartbeatSeconds = null, pongTimeoutSeconds = null) }
    }

    /**
     * Races the enabled candidates in the order settings lists them; diagnostics start over for this attempt. With
     * tunnels, the way is picked step by step as [HubWay.next] says.
     */
    private suspend fun selectRoute(): HubRoute {
        val candidates = usableEndpoints()
        syncPins()
        tunnelRoutes.clear()
        updateDiagnostics { it.copy(candidates = candidates.map { endpoint ->
            ConnectionCandidate(endpoint.url, endpoint.kind, CoreText.of(Key.NotMeasured))
        }) }
        val tunnels = usableTunnels()
        val ways = ways
        if (tunnels.isEmpty() || ways == null) return selectDirect(candidates)
        val options = tunnels.map { HubWay.Option(it.always, ways.recent(it)) }
        var seen = HubWay.Seen()
        val through = HashMap<Int, HubRoute>()
        var direct: Result<HubRoute>? = null
        while (true) {
            when (val step = HubWay.next(options, seen)) {
                is HubWay.Step.Open -> {
                    val route = openTunnel(ways, tunnels[step.index])
                    if (route != null) through[step.index] = route
                    seen = seen.copy(opened = seen.opened + (step.index to (route != null)))
                }
                is HubWay.Step.AskDirect -> {
                    // A tunnel used last time starts while the addresses are asked, so one that does not answer costs no wait on top.
                    val (answer, warmed) = coroutineScope {
                        val warming = if (step.warming.isEmpty()) null else async {
                            val tried = LinkedHashMap<Int, HubRoute?>()
                            for (index in step.warming) {
                                tried[index] = openTunnel(ways, tunnels[index])
                                if (tried[index] != null) break
                            }
                            tried
                        }
                        val answer = try { Result.success(selectDirect(candidates)) }
                            catch (cancelled: CancellationException) { throw cancelled }
                            catch (error: Exception) { Result.failure(error) }
                        if (answer.isSuccess) warming?.cancel()
                        answer to if (answer.isSuccess) emptyMap() else warming?.await().orEmpty()
                    }
                    direct = answer
                    warmed.forEach { (index, route) -> if (route != null) through[index] = route }
                    seen = seen.copy(direct = answer.isSuccess, opened = seen.opened + warmed.mapValues { it.value != null })
                }
                HubWay.Step.Direct -> {
                    closeTunnels()
                    return direct!!.getOrThrow()
                }
                is HubWay.Step.Through -> {
                    closeTunnels(except = tunnels[step.index], forget = false)
                    return through.getValue(step.index)
                }
                HubWay.Step.None -> {
                    closeTunnels(forget = false)
                    throw direct?.exceptionOrNull() ?: HubFailure(HubError.Unreachable)
                }
            }
        }
    }

    private suspend fun selectDirect(candidates: List<Endpoint>): HubRoute {
        val centerEndpoints = routes().filter { it.second.center }.map { endpointKey(it.first.url) }.toSet()
        return probeRoutes(candidates, centerEndpoints = centerEndpoints)
    }

    /** The tunnel up and the hub answering its health check through it; null when either fails. */
    private suspend fun openTunnel(ways: HubWays, tunnel: HubTunnel): HubRoute? {
        val owner = ownerForHub(tunnel.hubId) ?: return null
        val base = try { ways.open(tunnel) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { return null }
        val endpoint = Endpoint(base, kind = tunnel.endpointKind)
        tunnelRoutes[endpointKey(base)] = tunnel to owner
        updateDiagnostics { snapshot -> snapshot.copy(candidates = snapshot.candidates.filter { it.url != base } +
            ConnectionCandidate(base, tunnel.endpointKind, CoreText.of(Key.NotMeasured))) }
        return try { probeRoutes(listOf(endpoint), TUNNEL_PROBE_MILLIS) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { null }
    }

    /** The tunnels of hubs this connection still tries (a hub that refused its credential is out). */
    private fun usableTunnels(): List<HubTunnel> = tunnels.filter { ownerForHub(it.hubId) != null }

    /** Closes this connection's tunnels but [except]; [forget] as [HubWays.close] says. */
    private fun closeTunnels(except: HubTunnel? = null, forget: Boolean = true) {
        val ways = ways ?: return
        tunnels.filter { except == null || it.hubId != except.hubId || it.kind != except.kind }
            .forEach { ways.close(it.hubId, it.kind, forget) }
    }

    /** The tunnel the route goes through; null for a route straight to a hub. */
    private fun tunnelOf(route: HubRoute?): HubTunnel? = route?.let { tunnelRoutes[endpointKey(it.endpoint.url)]?.first }

    /** Probes [candidates] in order ([HubRouteSelector]), recording each measured probe in the diagnostics. */
    private suspend fun probeRoutes(
        candidates: List<Endpoint>,
        probeTimeoutMillis: Long? = null,
        centerEndpoints: Set<String> = emptySet(),
    ): HubRoute =
        selector.select(candidates, probeTimeoutMillis, centerEndpoints = centerEndpoints, onProbeMeasured = { endpoint, elapsed, result ->
            updateDiagnostics { snapshot -> snapshot.copy(candidates = snapshot.candidates.map { candidate ->
                if (candidate.url != endpoint.url) candidate else candidate.copy(
                    result = endpointResultLabel(result.exceptionOrNull()?.let { HubFailure.from(it).kind }),
                    checkedAt = Instant.now().toString(), probeDurationMillis = elapsed,
                    sampleCount = 1, measurementNote = CoreText.of(Key.SingleProbe),
                )
            }) }
        })

    override fun reconnect() = synchronized(lock) {
        if (closed || revoked) return@synchronized
        val previous = runner
        previous?.cancel()
        socket?.cancel()
        socket = null
        clearRoute()
        runner = scope.launch { previous?.join(); connectLoop() }
    }

    fun updateNetwork(value: HubNetwork) {
        if (network == value) return
        network = value
        // A tunnel's connection was made on the network before: it is made again on this one.
        closeTunnels(forget = false)
        reconnect()
    }

    /**
     * The tunnels changed (Settings › Hubs): the way is picked again when it may change, so a fallback saved while the
     * hub answers straight does not drop the socket.
     */
    fun updateTunnels(value: List<HubTunnel>) {
        val previous = synchronized(lock) { tunnels.also { tunnels = value.toList() } }
        if (previous == value) return
        val ways = ways
        if (ways != null) previous.filter { old -> value.none { it.sameWay(old) } }.forEach { ways.close(it.hubId, it.kind) }
        val through = _tunnel.value
        val newAlways = value.any { tunnel -> tunnel.always && previous.none { it.sameWay(tunnel) && it.always } }
        if (_route.value == null || newAlways || through != null && value.none { it == through }) reconnect()
    }

    fun updateEndpoints(value: List<Endpoint>) {
        if (value == _endpoints.value) return
        _endpoints.value = value.toList()
        reconnect()
    }

    /** Manual candidates first, then the pairing's or the hub's, deduplicated by key; disabled ones included. */
    fun candidates(overrides: EndpointOverrides = _overrides.value): List<Endpoint> =
        mergeEndpoints(overrides.manual, _endpoints.value, http.localAddressPolicy)

    /** This hub's candidates, then each other hub's of the group (disabled ones included), deduplicated by key. Center first. */
    fun allCandidates(): List<Endpoint> {
        val center = centerId
        val homeIsCenter = (center != null && config.hubId != null && config.hubId == center) || (center == null && config.center)
        val own = candidates().map { it to homeIsCenter }
        val group = members.flatMap { member ->
            val memberIsCenter = (center != null && member.hubId == center) || (center == null && member.center)
            memberCandidates(member).map { it to memberIsCenter }
        }
        val all = (own + group).distinctBy { endpointKey(it.first.url) }
        return (all.filter { it.second } + all.filter { !it.second }).map { it.first }
    }

    /** Candidates with their hub and center markings (docs/protocol/multi-hub.md "Connections and routing"), center first. */
    fun routeCandidates(overrides: EndpointOverrides = _overrides.value): List<HubRoute.Candidate> =
        routes(overrides).map { (endpoint, owner) ->
            HubRoute.Candidate(endpoint, hub = owner.hubId, center = owner.center)
        }

    /** Whether the current route is on a hub other than the group's center. */
    fun onMember(): Boolean {
        val current = _route.value ?: return false
        val centerEndpoints = routes().filter { it.second.center }.map { endpointKey(it.first.url) }.toSet()
        if (centerEndpoints.isEmpty()) return false
        tunnelRoutes[endpointKey(current.endpoint.url)]?.let { return !it.second.center }
        return endpointKey(current.endpoint.url) !in centerEndpoints
    }

    /**
     * Replaces the other hubs of the group. A hub that refused its credential stays out unless it comes back changed.
     * Starts over only when the route in use belongs to a hub no longer listed, or there is no route.
     */
    fun updateMembers(value: List<HubMember>, centerHubId: String? = null) {
        val reconnect = synchronized(lock) {
            if (centerHubId != null) this.centerHubId = centerHubId
            else {
                val newCenter = value.firstOrNull { it.center }?.hubId
                if (newCenter != null && this.centerHubId == null) this.centerHubId = newCenter
            }
            if (value == members) return
            members = value.toList()
            excluded.entries.removeAll { (hubId, member) -> value.none { it.hubId == hubId } || value.first { it.hubId == hubId } != member }
            http.updatePins(value.mapNotNullTo(LinkedHashSet()) { it.fingerprint })
            syncPins()
            val owner = routeOwner?.takeIf { it.first === _route.value }?.second
            _route.value == null || (owner != null && !owner.home && value.none { it.hubId == owner.hubId })
        }
        if (reconnect) reconnect()
    }

    /**
     * Applies new route choices. Without a route, or when the one in use is no longer an enabled candidate, the
     * connection starts over. A new order is only stored: the connection follows it at the next reconnect or periodic
     * recheck in [runSocket], so dragging an address never drops a working connection (user decision, 2026-11-02).
     */
    fun updateOverrides(value: EndpointOverrides) {
        val previous = _overrides.getAndUpdate { value }
        if (previous == value) return
        val active = _route.value?.endpoint?.url?.let(::endpointKey)
        // Through a tunnel the addresses are asked again every so often anyway.
        if (active != null && tunnelRoutes.containsKey(active)) return
        if (active == null || active !in usableEndpoints(value).map { endpointKey(it.url) }) reconnect()
    }

    fun setForeground(value: Boolean) {
        if (foreground == value) return
        foreground = value
        mailbox?.trySend(Signal.Foreground)
    }

    /** Validate the live connection when the Android service is woken after sleeping. */
    fun onWake() {
        val box = mailbox
        if (box != null) box.trySend(Signal.Wake) else retryWait?.complete(Unit)
    }

    override fun send(message: JsonObject): Boolean = socket?.send(message.toString()) ?: false

    /** The selected origin has already passed health identity validation. Caller owns the response. */
    suspend fun request(
        path: String, method: String = "GET", body: RequestBody? = null,
        headers: Map<String, String> = emptyMap(), buffered: Boolean = true,
        expectedRoute: HubRoute? = null,
        /** Longer than the client's 15 s, for answers the hub waits on a machine for (a CLI starting). */
        readTimeoutSeconds: Long? = null,
    ): Response {
        val selected = awaitRoute()
        if (expectedRoute != null && selected !== expectedRoute) throw HubFailure(HubError.NoNetwork)
        val owner = synchronized(lock) {
            activeRoute().takeIf { it === selected } ?: throw HubFailure(HubError.NoNetwork)
            routeOwner?.takeIf { it.first === selected }?.second ?: throw HubFailure(HubError.NoNetwork)
        }
        require(path.startsWith("/api/") && !path.contains('#')) { "Expected an API path" }
        val base = selected.endpoint.baseUrl()
        val url = base.hubUrl(path).withRequestManager()
        require(base.hubPath(url)?.startsWith("/api/") == true) { "Expected an API path" }
        val request = Request.Builder().url(url).apply {
            headers.forEach { (name, value) -> header(name, value) }
        }.header("Authorization", "Bearer ${owner.token}").method(method, compatibleRequestBody(path, body)).build()
        val client = readTimeoutSeconds?.let { http.client.newBuilder().readTimeout(it, java.util.concurrent.TimeUnit.SECONDS).build() } ?: http.client
        val response = client.newCall(request).awaitResponse(buffered)
        if (!response.isSuccessful) {
            val error = response.use { HubFailure.from(it) }
            // recordFailure owns revocation: it cancels the socket, clears the route and diagnostics. In a group only
            // the hub that refused is dropped, and the connection moves on to the others.
            if (error.kind == HubError.PairingExpired && recordFailure(error, owner)) reconnect()
            throw error
        }
        return response
    }

    /** Requests made while the first route is still being chosen (right after pairing, after a network change) wait for it. */
    private suspend fun awaitRoute(): HubRoute {
        if (_route.value == null && !revoked && !closed) withTimeoutOrNull(10_000) { _route.first { it != null } }
        return activeRoute()
    }

    internal fun activeRoute(): HubRoute = synchronized(lock) {
        if (revoked) throw _failure.value ?: HubFailure(HubError.PairingExpired, retryable = false)
        if (closed) throw HubFailure(HubError.NoNetwork, retryable = false)
        _route.value ?: throw HubFailure(HubError.NoNetwork)
    }

    private suspend fun connectLoop() {
        var backoff = 1L
        while (currentCoroutineContext().isActive && !closed && !revoked) {
            if (!network.available) {
                publishState(ConnectionState.WaitingForNetwork)
                return
            }
            publishState(ConnectionState.Connecting)
            updateDiagnostics { it.copy(nextRetryAt = null) }
            try {
                val candidates = usableEndpoints()
                if (candidates.isEmpty() && usableTunnels().isEmpty() && permissionMissing()) {
                    throw HubFailure(HubError.LocalNetworkPermission, retryable = false)
                }
                attemptOwner = null
                val selected = selectRoute()
                currentCoroutineContext().ensureActive()
                val owner = ownerFor(selected.endpoint)
                routeOwner = selected to owner
                attemptOwner = owner
                _tunnel.value = tunnelOf(selected)
                _route.value = selected
                updateDiagnostics { it.copy(currentPath = "${selected.endpoint.kind} · ${selected.endpoint.url}") }
                runSocket(selected) { backoff = 1L; updateDiagnostics { it.copy(lastAttemptFailure = null) } }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                currentCoroutineContext().ensureActive()
                var failure = HubFailure.from(error)
                if (failure.kind == HubError.Timeout && permissionMissing()) {
                    failure = HubFailure(HubError.LocalNetworkPermission, retryable = false, cause = failure)
                }
                clearRoute()
                if (failure.kind == HubError.Timeout) http.evictConnections()
                // A hub of the group refusing its credential is dropped; the next round tries the others at once.
                if (recordFailure(failure)) { backoff = 1L; continue }
                if (!failure.retryable || revoked) return
                // A restarting hub is back within seconds: retry soon and steadily, and do not count it toward the backoff.
                if (policy.restarting(restartSince, clockMillis())) {
                    waitToRetry(policy.restartDelay(restartAttempts++))
                    continue
                }
                val retryMillis = failure.retryAfterSeconds?.let { it.coerceAtMost(86400) * 1000 }
                    ?: (backoff * 1000 * Random.nextDouble(0.8, 1.0)).toLong()
                backoff = if (waitToRetry(retryMillis)) 1L else (backoff * 2).coerceAtMost(20)
            }
        }
    }

    /** Waits [millis] before the next attempt, saying when it is (the connection details' "Retrying in N s"). */
    private suspend fun waitToRetry(millis: Long): Boolean {
        updateDiagnostics { it.copy(nextRetryAt = Instant.now().plusMillis(millis).toString()) }
        val wait = CompletableDeferred<Unit>()
        retryWait = wait
        try { withTimeoutOrNull(millis) { wait.await() } }
        finally {
            if (retryWait === wait) retryWait = null
            updateDiagnostics { it.copy(nextRetryAt = null) }
        }
        return wait.isCompleted
    }

    private suspend fun runSocket(selected: HubRoute, ready: () -> Unit): Unit = coroutineScope {
        val messages = Channel<Signal>(Channel.UNLIMITED)
        val activity = Channel<Unit>(Channel.CONFLATED)
        val lastReceived = AtomicLong(clockMillis())
        fun received() {
            lastReceived.set(clockMillis())
            activity.trySend(Unit)
            updateDiagnostics { it.copy(lastReceivedAt = Instant.now().toString()) }
        }
        _heartbeat.value = Heartbeat(20, 15)
        val owner = ownerOf(selected)
        resumed = false
        val resumeFrom = resumePoint
        val socketNumber = socketCount.incrementAndGet()
        val ws = webSocketFactory.newWebSocket(
            authorized(socketUrl(selected.endpoint.baseUrl(), resumeFrom), owner).build(),
            object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) {
                    received()
                    val fingerprint = response.handshake?.peerCertificates?.firstOrNull()?.publicKey?.encoded?.let {
                        Base64.getUrlEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(it))
                    // A successful TLS upgrade has already passed SpkiTrustManager.
                    } ?: http.seenPin(selected.endpoint.baseUrl()).takeIf { members.isNotEmpty() }
                        ?: owner.fingerprint?.takeIf { selected.endpoint.baseUrl().isHttps }
                    updateDiagnostics { it.copy(fingerprint = fingerprint) }
                    messages.trySend(Signal.Open)
                }
                override fun onMessage(webSocket: WebSocket, text: String) {
                    received()
                    messages.trySend(Signal.Text(text))
                }
                override fun onMessage(webSocket: WebSocket, bytes: okio.ByteString) { received() }
                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                    // 1001 "going away": the hub is stopping (a deploy) and the next one takes over within seconds.
                    if (code == 1001) markRestart()
                    webSocket.close(code, null)
                    messages.trySend(Signal.Failed(HubFailure(HubError.Transport, "WebSocket closed ($code): $reason")))
                }
                override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                    val error = response?.use { HubFailure.from(it) } ?: HubFailure.from(t)
                    messages.trySend(Signal.Failed(error))
                }
            },
        )
        socket = ws
        mailbox = messages
        _weakNetwork.value = false
        var features = emptySet<String>()
        val languageWatch = launch { persona.core.contracts.ClientLanguage.changes.drop(1).collect { messages.trySend(Signal.Language) } }
        var receivedHello = false
        var synchronizedData = false
        var pingJob: Job? = null
        val pendingPing = AtomicReference<Pair<String, CompletableDeferred<Unit>>?>(null)
        var syncing: Job? = null
        /** A stream this socket started over in: resumable once the data is in sync. */
        var freshStream: String? = null

        fun probe(reconnectOnTimeout: Boolean = false, immediate: Boolean = true) {
            if (pingJob?.isActive == true || "app_ping" !in features) return
            pingJob = launch {
                // OkHttp does not expose control ping callbacks. An application pong makes
                // background liveness observable without relying on hidden socket state.
                if (!immediate) delay(_heartbeat.value.interval * 1000L)
                do {
                    val id = pingIds.incrementAndGet().toString()
                    val pong = CompletableDeferred<Unit>()
                    pendingPing.set(id to pong)
                    if (!ws.send(buildJsonObject { put("type", "ping"); put("id", id) }.toString())) {
                        messages.send(Signal.Failed(HubFailure(HubError.Transport)))
                        return@launch
                    }
                    val start = clockMillis()
                    val timeout = if (foreground) 5000L else _heartbeat.value.pongTimeout * 1000L
                    val timely = withTimeoutOrNull(timeout) { pong.await(); true } == true && clockMillis() - start <= timeout
                    pendingPing.set(null)
                    messages.send(Signal.ProbeResult(timely))
                    if (!timely && reconnectOnTimeout) {
                        messages.send(Signal.Failed(HubFailure(HubError.Timeout)))
                        return@launch
                    }
                    val interval = _heartbeat.value.interval * 1000L
                    delay((interval - (clockMillis() - start)).coerceAtLeast(0))
                } while (isActive)
            }
        }

        fun remainingLifetime(front: Boolean = foreground): Long {
            // Legacy hubs only send control frames, which OkHttp consumes internally.
            // Keep their background behavior until application probes are supported.
            if (!front && "app_ping" !in features) return Long.MAX_VALUE
            val limit = if (front) 45_000L else
                (_heartbeat.value.interval.toLong() + _heartbeat.value.pongTimeout) * 1000L
            return limit - (clockMillis() - lastReceived.get())
        }

        fun checkAlive() {
            if (remainingLifetime() <= 0) throw HubFailure(HubError.Timeout)
        }

        /**
         * On return to the front or a wake, silence within the background limit is normal (#4695): only that limit
         * fails here. Past the foreground one the clock starts over and returns true, so an unanswered ping decides.
         */
        fun checkAliveAfterBackground(): Boolean {
            if ("app_ping" !in features) { checkAlive(); return false }
            if (remainingLifetime(front = false) <= 0) throw HubFailure(HubError.Timeout)
            if (remainingLifetime() > 0) return false
            lastReceived.set(clockMillis())
            return true
        }

        val inactivity = launch {
            while (isActive) {
                val remaining = remainingLifetime()
                if (remaining <= 0) {
                    messages.send(Signal.Failed(HubFailure(HubError.Timeout)))
                    break
                }
                withTimeoutOrNull(remaining) { activity.receive() }
            }
        }
        val helloDeadline = launch {
            delay(15_000)
            if (!receivedHello) messages.send(Signal.Failed(HubFailure(HubError.Timeout)))
        }
        val tunnel = tunnelOf(selected)
        val recheck = launch {
            // Always through the tunnel: nothing to move back to.
            if (tunnel?.always == true) return@launch
            while (isActive) {
                delay(if (tunnel != null) tunnelRecheckMillis else recheckMillis)
                if (!foreground) continue
                if (tunnel != null) {
                    // Through a tunnel only because no address answered: back straight to the hub once one does.
                    val direct = usableEndpoints().takeIf { it.isNotEmpty() } ?: continue
                    try { probeRoutes(direct, DIRECT_PROBE_MILLIS) }
                    catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { continue }
                    reconnect()
                    break
                }
                // Only a candidate listed above the one in use can take over; the list's order is the order tried. Each
                // gets the step it has alone in the race: one that answers later would lose the race to the one in use
                // again, and the connection would start over at every recheck for nothing.
                val candidates = usableEndpoints()
                val position = candidates.indexOfFirst { endpointKey(it.url) == endpointKey(selected.endpoint.url) }
                if (position < 0) {
                    messages.send(Signal.Failed(HubFailure(HubError.Transport)))
                    break
                }
                if (position == 0) continue
                try { probeRoutes(candidates.take(position), HubRouteSelector.STEP_MILLIS) }
                catch (cancelled: CancellationException) { throw cancelled }
                catch (_: Exception) { continue }
                // Starts over as a choice in settings does (no failure recorded, no backoff): the race picks the first that answers.
                reconnect()
                break
            }
        }
        try {
            for (signal in messages) when (signal) {
                Signal.Open -> {
                    publishState(ConnectionState.Updating)
                }
                Signal.Foreground -> {
                    // Before the inactivity job wakes and measures the background silence against 45 s.
                    val quiet = foreground && checkAliveAfterBackground()
                    activity.trySend(Unit)
                    if ("client_hello" in features) ws.send(clientHello(features).toString())
                    pingJob?.cancelAndJoin()
                    pingJob = null
                    pendingPing.set(null)
                    if (foreground) probe(reconnectOnTimeout = quiet)
                    else probe(immediate = false)
                }
                Signal.Language -> if ("client_hello" in features) ws.send(clientHello(features).toString())
                Signal.Wake -> {
                    checkAliveAfterBackground()
                    pingJob?.cancelAndJoin()
                    pingJob = null
                    pendingPing.set(null)
                    if ("app_ping" in features) probe(reconnectOnTimeout = true)
                    else if (receivedHello) throw HubFailure(HubError.Transport)
                }
                Signal.Synced -> {
                    synchronizedData = true
                    ready()
                    restartSince = null
                    freshStream?.let { eventStream = it; freshStream = null }
                    if (!_weakNetwork.value) {
                        _failure.value = null
                        publishState(ConnectionState.Online)
                    }
                }
                is Signal.ProbeResult -> {
                    _weakNetwork.value = !signal.timely
                    if (signal.timely && synchronizedData) {
                        _failure.value = null
                        publishState(ConnectionState.Online)
                    } else if (!signal.timely) {
                        _failure.value = HubFailure(HubError.Timeout)
                        // A slow answer on a live socket, not a drop: shown as soon as it always was.
                        publishState(ConnectionState.Connecting, policy.otherStateDelayMillis)
                    }
                }
                is Signal.Failed -> throw signal.error
                is Signal.Text -> {
                    val raw = runCatching { JsonCodec.parseToJsonElement(signal.value) as? JsonObject }.getOrNull()
                    val type = (raw?.get("type") as? JsonPrimitive)?.contentOrNull
                    // Every broadcast carries its number (the hello's is where the stream stood, handled with it).
                    if (type != "hello") (raw?.get("event_seq") as? JsonPrimitive)?.longOrNull?.let { seq ->
                        lastEventSeq.accumulateAndGet(seq, ::maxOf)
                    }
                    if (type == "hub_restarting") {
                        markRestart()
                        continue
                    }
                    if (raw != null && phoneMessageHandler?.invoke(raw) == true) continue
                    // Decoded from the tree parsed above: frames (a backlog of items is large) are read from text once.
                    val event = raw?.let(ServerEvent::decode) ?: continue
                    when (event) {
                        is Hello -> {
                            if (owner.hubId != null && event.hubId != owner.hubId) {
                                throw HubFailure(HubError.HubMismatch, retryable = false)
                            }
                            lastHello = event
                            event.mesh?.hubs?.firstOrNull { it.center == true }?.hubId?.let { learnedCenter ->
                                centerHubId = learnedCenter
                            }
                            features = event.features.toSet()
                            _serverFeatures.value = features
                            if (!features.containsAll(config.requiredFeatures)) {
                                throw HubFailure(HubError.Incompatible, retryable = false)
                            }
                            receivedHello = true
                            helloDeadline.cancel()
                            resumed = event.resumed == true && resumeFrom != null && event.eventStream == resumeFrom.first
                            if (!resumed) {
                                // Starting over: what the gap missed is refetched, so the stream counts from here. It is
                                // not resumable until that is done; a drop before then starts over again.
                                startedOver.set(socketNumber)
                                eventStream = null
                                lastEventSeq.set(event.eventSeq ?: 0)
                                freshStream = event.eventStream
                                surfaceHeldDrop()
                            }
                            // Resumed: the missed broadcasts follow and raise lastEventSeq themselves; jumping to
                            // hello.event_seq now would skip them if this socket dropped during the replay.
                            if (event.endpoints.isNotEmpty()) {
                                if (owner.home) {
                                    _endpoints.value = event.endpoints
                                    _advertised.value = event.endpoints.mapTo(HashSet()) { endpointKey(it.url) }
                                } else _memberEndpoints.update { it + (owner.hubId!! to event.endpoints) }
                            }
                            if ("client_hello" in features) ws.send(clientHello(features).toString())
                            if (foreground) probe()
                            else probe(immediate = false)
                            if (syncing == null) syncing = launch {
                                try {
                                    withTimeoutOrNull(8000) { onReady(this@OkHttpHubConnection) }
                                    messages.send(Signal.Synced)
                                } catch (cancelled: CancellationException) { throw cancelled }
                                catch (error: Exception) { messages.send(Signal.Failed(HubFailure.from(error))) }
                            }
                        }
                        is ClientHelloAck -> if ("client_hello" in features && "heartbeat" in features &&
                            event.heartbeat.interval > 0 && event.heartbeat.pongTimeout > 0) {
                            _heartbeat.value = event.heartbeat
                            activity.trySend(Unit)
                            updateDiagnostics { it.copy(
                                heartbeatSeconds = event.heartbeat.interval, pongTimeoutSeconds = event.heartbeat.pongTimeout,
                                foregroundHeartbeatSeconds = if (foreground) event.heartbeat.interval else it.foregroundHeartbeatSeconds,
                                backgroundHeartbeatSeconds = if (!foreground) event.heartbeat.interval else it.backgroundHeartbeatSeconds,
                            ) }
                            pingJob?.cancelAndJoin()
                            pingJob = null
                            pendingPing.set(null)
                            probe(immediate = foreground)
                        }
                        is EndpointsEvent -> {
                            if (!owner.home) {
                                // Another hub's list: it never replaces this hub's own candidates or settings.
                                val hubId = owner.hubId!!
                                if (_memberEndpoints.value[hubId] != event.endpoints) {
                                    _memberEndpoints.update { it + (hubId to event.endpoints) }
                                    throw HubFailure(HubError.Transport)
                                }
                                continue
                            }
                            outgoing.send(socketNumber to event)
                            _advertised.value = event.endpoints.mapTo(HashSet()) { endpointKey(it.url) }
                            if (_endpoints.value != event.endpoints) {
                                _endpoints.value = event.endpoints
                                throw HubFailure(HubError.Transport)
                            }
                            continue
                        }
                        is Pong -> pendingPing.get()?.takeIf { it.first == event.id }?.second?.complete(Unit)
                        else -> Unit
                    }
                    outgoing.send(socketNumber to event)
                }
            }
        } finally {
            languageWatch.cancel()
            inactivity.cancel()
            helloDeadline.cancel()
            recheck.cancel()
            syncing?.cancel()
            pingJob?.cancel()
            ws.cancel()
            messages.close()
            activity.close()
            if (socket === ws) socket = null
            if (mailbox === messages) mailbox = null
        }
    }

    private fun clientHello(features: Set<String>) = buildJsonObject {
        put("type", "client_hello")
        put("platform", "android")
        put("version", config.version)
        config.deviceId?.let { put("device_id", it) }
        config.build?.let { put("build", it) }
        config.os?.let { put("os", it) }
        // The language the app shows (docs/protocol/i18n.md "Protocol"): this socket's notices, and the user's language.
        put("locale", persona.core.contracts.ClientLanguage.tag)
        putJsonArray("features") {
            (setOf("endpoints", "ws_notify", "app_ping", "elicitation", ProtocolCompatibility.CAPABILITY) + config.clientFeatures).forEach { add(it) }
        }
        if ("heartbeat" in features) {
            putJsonObject("heartbeat") {
                put("interval", config.heartbeatPolicy.interval(foreground, network.transport))
                put("pong_timeout", config.heartbeatPolicy.pongTimeoutSeconds)
            }
        }
    }

    /** The socket's URL; with the stream and last broadcast seen, so the hub replays what was missed (#1574). */
    internal fun socketUrl(base: HttpUrl, resumeFrom: Pair<String, Long>?): HttpUrl =
        base.hubUrl("/api/v1/ws").newBuilder().apply {
            addQueryParameter("features", "sessions_list,extension_ui_stream_v1,${ProtocolCompatibility.CAPABILITY}")
            resumeFrom?.let { (stream, seq) ->
                addQueryParameter("event_stream", stream)
                addQueryParameter("event_seq", seq.toString())
            }
        }.build()

    /** The hub is restarting: retry quickly and keep showing Online for the restart's grace window. */
    private fun markRestart() = synchronized(lock) {
        val now = clockMillis()
        if (!policy.restarting(restartSince, now)) {
            restartSince = now
            restartAttempts = 0
        }
    }

    private fun authorized(url: HttpUrl, owner: Owner) = Request.Builder().url(url).header("Authorization", "Bearer ${owner.token}")

    private fun permissionMissing() = network.homeWifi && !network.localNetworkPermissionGranted

    /**
     * The enabled candidates in the order settings lists them (the order tried), minus what local-network permission
     * rules out; then each other hub's of the group the same way, by its own route choices.
     */
    private fun usableEndpoints(overrides: EndpointOverrides = _overrides.value) = routes(overrides).map { it.first }

    private fun routes(overrides: EndpointOverrides = _overrides.value): List<Pair<Endpoint, Owner>> {
        val center = centerId
        val home = homeOwner()
        val own = if (homeExcluded) emptyList() else usable(candidates(overrides), overrides).map { it to home }
        val group = members.filter { synchronized(lock) { it.hubId !in excluded } }.flatMap { member ->
            usable(memberCandidates(member), member.overrides).map { it to memberOwner(member, center) }
        }
        val all = (own + group).distinctBy { endpointKey(it.first.url) }
        return all.filter { it.second.center } + all.filter { !it.second.center }
    }

    private fun usable(candidates: List<Endpoint>, overrides: EndpointOverrides) =
        arrangeEndpoints(candidates, overrides).filter { endpoint ->
            if (endpointKey(endpoint.url) in overrides.disabled) false
            else if (!permissionMissing()) true else {
                val host = runCatching { endpoint.baseUrl().host }.getOrNull()
                val literal = host?.let(::literalAddress)
                endpoint.kind != "lan" && host !in network.localHosts &&
                    (literal == null || literal.isLoopbackAddress || !http.localAddressPolicy.allows(literal))
            }
        }

    /** A member's manual candidates, then what it sent while connected through it, else what the group knew of it. */
    private fun memberCandidates(member: HubMember): List<Endpoint> = mergeEndpoints(member.overrides.manual,
        _memberEndpoints.value[member.hubId]?.takeIf { it.isNotEmpty() } ?: member.endpoints, http.localAddressPolicy)

    /** Who answers at [endpoint], with which credential: this hub unless the group lists it under another hub. */
    private fun ownerFor(endpoint: Endpoint): Owner = tunnelRoutes[endpointKey(endpoint.url)]?.second
        ?: routes().firstOrNull { endpointKey(it.first.url) == endpointKey(endpoint.url) }?.second ?: homeOwner()

    /** The hub [hubId] as this connection tries it; null when it is not one of its hubs or refused its credential. */
    private fun ownerForHub(hubId: String): Owner? {
        if (hubId == config.hubId) return homeOwner().takeUnless { homeExcluded }
        val member = members.firstOrNull { it.hubId == hubId } ?: return null
        if (synchronized(lock) { hubId in excluded }) return null
        return memberOwner(member)
    }

    /**
     * Tells the client which hub's pin every TLS candidate must present (the client of a group trusts them all). All
     * candidates of every hub, disabled ones too, so a settings test is held to the same pin as the connection.
     */
    private fun syncPins() {
        if (members.isEmpty()) return http.expectPins(emptyMap())
        val pins = LinkedHashMap<HttpUrl, String>()
        fun add(endpoints: List<Endpoint>, pin: String?) = endpoints.forEach { endpoint ->
            val url = runCatching { endpoint.baseUrl() }.getOrNull() ?: return@forEach
            if (pin != null && url !in pins) pins[url] = pin
        }
        add(candidates(), config.fingerprint)
        members.forEach { add(memberCandidates(it), it.fingerprint) }
        http.expectPins(pins)
    }

    private fun memberOwner(member: HubMember, center: String? = centerId): Owner {
        val memberIsCenter = (center != null && member.hubId == center) || (center == null && member.center)
        return Owner(member.hubId, member.fingerprint, member.token ?: config.token, member.token != null, home = false, center = memberIsCenter)
    }

    private fun ownerOf(route: HubRoute?): Owner = routeOwner?.takeIf { route != null && it.first === route }?.second ?: homeOwner()

    private fun homeOwner(): Owner {
        val center = centerId
        val homeIsCenter = (center != null && config.hubId != null && config.hubId == center) || (center == null && config.center)
        return Owner(config.hubId, config.fingerprint, config.token, ownToken = true, home = true, center = homeIsCenter)
    }

    /** Pinned health probes; without other hubs exactly the single-hub probe. */
    private suspend fun probe(endpoint: Endpoint): Health {
        if (members.isEmpty()) return http.health(endpoint)
        val owner = ownerFor(endpoint)
        // HTTPS without a pin is only checked against the system CAs when the group's own pairing had no fingerprint.
        if (endpoint.baseUrl().isHttps && owner.fingerprint == null && !http.systemTrusted) {
            throw HubFailure(HubError.CertificateMismatch, retryable = false)
        }
        return http.health(endpoint, expectedHubId = owner.hubId, expectedPin = owner.fingerprint)
    }

    /** The hub behind a route: id, pin, the credential to send and whether it is that hub's own. */
    private class Owner(val hubId: String?, val fingerprint: String?, val token: String, val ownToken: Boolean, val home: Boolean, val center: Boolean = false)

    /** Route legacy HTTP authentication failures through the same terminal state as live requests. */
    fun revoke() {
        if (recordFailure(HubFailure(HubError.PairingExpired, retryable = false), ownerOf(_route.value).takeIf { _route.value != null })) reconnect()
    }

    /**
     * Records [error]. Returns true when it was a group hub refusing its credential and only that hub was dropped
     * (the caller starts over with the others); false otherwise, including when the whole pairing is now revoked.
     * [refusedBy] is the hub the refused request went to; by default the one the connection last ran on.
     */
    private fun recordFailure(error: HubFailure, refusedBy: Owner? = null): Boolean {
        if (error.kind != HubError.PairingExpired || members.isEmpty()) {
            recordTerminal(error)
            return false
        }
        val owner: Owner
        val remaining: Boolean
        synchronized(lock) {
            if (revoked || closed) return false
            owner = refusedBy ?: attemptOwner ?: homeOwner()
            if (owner.home) homeExcluded = true
            else members.firstOrNull { it.hubId == owner.hubId }?.let { excluded[it.hubId] = it }
            remaining = routes().isNotEmpty() || usableTunnels().isNotEmpty()
            if (remaining) {
                _failure.value = error
                updateDiagnostics { it.copy(lastDisconnectReason = error.message, lastAttemptFailure = endpointResultLabel(error.kind)) }
                socket?.cancel()
                socket = null
                clearRoute()
                publishState(if (network.available) ConnectionState.Connecting else ConnectionState.WaitingForNetwork)
            }
        }
        if (!remaining) recordTerminal(error)
        notifyRevoked(owner)
        return remaining
    }

    private fun notifyRevoked(owner: Owner) = onMemberRevoked(owner.hubId.orEmpty(), owner.ownToken)

    private fun recordTerminal(error: HubFailure) = synchronized(lock) {
        if (revoked) return@synchronized
        _failure.value = error
        updateDiagnostics { it.copy(lastDisconnectReason = error.message, lastAttemptFailure = endpointResultLabel(error.kind)) }
        // A socket that went silent was already gone for a long while: say so at once, unless the hub is restarting.
        if (error.kind == HubError.Timeout && !policy.restarting(restartSince, clockMillis())) setState(ConnectionState.Connecting)
        when (error.kind) {
            HubError.PairingExpired -> {
                revoked = true
                runner?.cancel()
                socket?.cancel()
                socket = null
                clearRoute()
                publishState(ConnectionState.Revoked)
            }
            HubError.CertificateMismatch, HubError.HubMismatch -> publishState(ConnectionState.CertificateMismatch)
            HubError.LocalNetworkPermission -> publishState(ConnectionState.NeedsLocalNetworkPermission)
            else -> publishState(if (network.available) ConnectionState.Connecting else ConnectionState.WaitingForNetwork)
        }
    }

    /**
     * Publishes [value]. A change away from Online is held back (#1574) — Connecting / Updating for the drop's grace
     * (longer while the hub restarts), anything else for [ReconnectPolicy.otherStateDelayMillis] — counted from when
     * Online was first held, so a reconnect that keeps changing between Connecting and Updating does not extend it.
     * [graceMillis] overrides the hold. Online and Revoked are published at once.
     */
    private fun publishState(value: ConnectionState, graceMillis: Long? = null) = synchronized(lock) {
        if (revoked && value != ConnectionState.Revoked) return@synchronized
        if (value == ConnectionState.Revoked || value == ConnectionState.Online ||
            _state.value != ConnectionState.Online && pendingState == null) {
            setState(value)
            return@synchronized
        }
        val now = clockMillis()
        val since = heldSince ?: now.also { heldSince = it }
        val connecting = value == ConnectionState.Connecting || value == ConnectionState.Updating
        val deadline = since + (graceMillis ?: policy.graceMillis(connecting, policy.restarting(restartSince, now)))
        if (pendingState != null && stateTarget == value && stateDeadline == deadline) return@synchronized
        pendingState?.cancel()
        stateTarget = value
        stateDeadline = deadline
        lateinit var job: Job
        job = scope.launch(start = CoroutineStart.LAZY) {
            delay((deadline - clockMillis()).coerceAtLeast(0))
            synchronized(lock) { if (pendingState === job) setState(value) }
        }
        pendingState = job
        job.start()
    }

    /** Publishes [value] now, dropping any held change. Callers hold [lock]. */
    private fun setState(value: ConnectionState) {
        pendingState?.cancel()
        pendingState = null
        stateTarget = null
        heldSince = null
        _state.value = value
    }

    /**
     * A socket that did not resume the event stream after a drop the user would have seen before the grace window
     * existed: show Updating now, so what reacts to coming back Online refetches what the gap missed, as it always did.
     */
    private fun surfaceHeldDrop() = synchronized(lock) {
        val since = heldSince ?: return@synchronized
        if (pendingState != null && !revoked && policy.surfaceFreshStart(clockMillis() - since)) setState(ConnectionState.Updating)
    }

    override fun close() {
        synchronized(lock) {
            if (closed) return
            closed = true
            runner?.cancel()
            socket?.cancel()
            socket = null
            clearRoute()
            scope.cancel()
            http.close()
            closeTunnels(forget = false)
        }
    }

    companion object {
        /** How often the connection looks for an answer from a candidate listed above the one in use; configured, not measured. */
        const val RECHECK_MILLIS = 300_000L
        /** How long the hub has to answer its health check through a tunnel that just came up. */
        const val TUNNEL_PROBE_MILLIS = 10_000L
        /** How long an address has to answer while the socket goes through a tunnel. */
        const val DIRECT_PROBE_MILLIS = 3_000L
    }

    private sealed interface Signal {
        data object Open : Signal
        data object Foreground : Signal
        data object Wake : Signal
        /** Settings › Language or the phone's language changed: say hello again with the new locale. */
        data object Language : Signal
        data object Synced : Signal
        data class ProbeResult(val timely: Boolean) : Signal
        data class Text(val value: String) : Signal
        data class Failed(val error: HubFailure) : Signal
    }
}
