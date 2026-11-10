package persona.android.di

import persona.android.AppText
import persona.android.R
import android.os.SystemClock
import persona.android.PersonaApp
import persona.android.data.ApiClient
import persona.android.data.Hub
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.data.HubCredentials
import persona.core.data.ScopedHubData
import persona.core.data.StoredGroup
import persona.core.hub.*
import persona.core.hub.tunnel.HubTunnel
import persona.core.hub.tunnel.HubWays
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject

/** What every group's connection shares (client identity, heartbeat and sync policy). */
class GroupConnectionSettings internal constructor(
    val version: String,
    val build: JsonElement,
    val os: JsonObject,
    val heartbeatPolicy: HeartbeatPolicy,
    val reconciliationTimeoutMillis: Long,
)

/**
 * One hub group's connection and data (docs/android/android.md "Multiple hubs"): what ConnectionRuntime held for its only pairing
 * before groups existed. One WebSocket for the whole group: the group's first paired hub is the connection's own
 * hub, the group's other hubs (paired, or learned from the mesh) are its members and take over when it is down.
 * Everything here speaks the group's plain machine ids; [namespace] is applied above, by the facades.
 */
class GroupRuntime internal constructor(
    group: StoredGroup,
    namespace: String?,
    private val app: PersonaApp,
    private val settings: GroupConnectionSettings,
    private val listener: Listener,
    private val ways: HubWays? = null,
) {
    interface Listener {
        /** Every event of the group, with the group's plain ids. */
        suspend fun onEvent(group: GroupRuntime, event: ServerEvent)
        fun onHello(group: GroupRuntime, hello: Hello)
        /** No hub of the group accepts this phone any more. */
        fun onRevoked(group: GroupRuntime)
        fun onMemberRevoked(group: GroupRuntime, hubId: String, ownToken: Boolean)
    }

    val id: String = group.id
    @Volatile var group: StoredGroup = group
        private set
    @Volatile var namespace: String? = namespace
        private set
    private val job = SupervisorJob(app.scope.coroutineContext[Job])
    private val scope = CoroutineScope(app.scope.coroutineContext + job)
    @Volatile private var live: OkHttpHubConnection? = null
    @Volatile var data: ScopedHubData? = null
        private set
    private val mutableState = MutableStateFlow(ConnectionState.WaitingForNetwork)
    val state = mutableState.asStateFlow()
    private val mutableDiagnostics = MutableStateFlow(ConnectionDiagnostics())
    val diagnostics = mutableDiagnostics.asStateFlow()
    private val reconciling = MutableStateFlow(false)
    private var reconciliationJob: Job? = null
    private val mutableEndpointSettings = MutableStateFlow<HubEndpointSettings?>(null)
    val endpointSettings = mutableEndpointSettings.asStateFlow()
    /** The compatibility handle the shell and platform services use while this group is selected. */
    @Volatile var hub: Hub? = null
        private set
    @Volatile private var foreground = false
    private val mutableHello = MutableStateFlow<Hello?>(null)
    /** The last hello: which hub the connection runs through, its name and its mesh. */
    val hello = mutableHello.asStateFlow()
    private val mutableTunnel = MutableStateFlow<HubTunnel?>(null)
    /** The tunnel the connection goes through now (Settings › Hubs). */
    val tunnel = mutableTunnel.asStateFlow()

    val connection: OkHttpHubConnection get() = live ?: throw CancellationException("Group closed")
    val connectionOrNull: OkHttpHubConnection? get() = live
    val active: Boolean get() = job.isActive && data?.coroutineScope?.coroutineContext?.get(Job)?.isActive == true

    /** Called by HubDataStore.openGroup (its transport factory): builds the group's one connection. */
    fun createTransport(storage: HubScope, credentials: HubCredentials, members: List<HubMember>,
        tunnels: List<HubTunnel> = emptyList()): MessageTransport {
        lateinit var transport: HttpMessageTransport
        val isCenter = credentials.hubId.takeUnless { it.startsWith("legacy-") }?.let { id ->
            id == group.centerHubId
        } ?: false
        val connection = OkHttpHubConnection(
            HubConnectionConfig(credentials.hubId.takeUnless { it.startsWith("legacy-") }, credentials.fp,
                credentials.deviceToken, pairingEndpoints(credentials), settings.version, deviceId = credentials.deviceId,
                clientFeatures = setOf("lock_test", "extension_operations", "extension_auth", "managers"), build = settings.build, os = settings.os,
                heartbeatPolicy = settings.heartbeatPolicy, members = members, center = isCenter, centerHubId = group.centerHubId,
                tunnels = tunnels),
            HubNetwork("initial", available = false),
            onReady = { readyConnection ->
                reconciliationJob?.cancelAndJoin()
                reconciling.value = true
                // Run outside the socket's hello deadline; the public state owns this barrier.
                reconciliationJob = scope.launch {
                    try {
                        val data = checkNotNull(data)
                        // A resumed socket had the pending interactions with the hub's snapshot and replay (#1574).
                        data.openWindowReconciliation.reconcile(settings.reconciliationTimeoutMillis, listOfNotNull(
                            if (readyConnection.resumed) null else suspend { HubChatTransport(readyConnection).interactions(); Unit },
                            suspend { data.messages.onReady() },
                        ))
                    } catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { /* Individual repositories retain unverified state. */ }
                    finally {
                        if (isActive && live === readyConnection) {
                            transport.onReady()
                            reconciling.value = false
                        }
                    }
                }
            },
            clockMillis = SystemClock::elapsedRealtime,
            onMemberRevoked = { hubId, ownToken -> listener.onMemberRevoked(this, hubId, ownToken) },
            ways = ways,
        )
        live = connection
        connection.setForeground(foreground)
        transport = HttpMessageTransport(storage, connection, scope) {
            if (live === connection) connection.revoke()
        }
        return transport
    }

    /** Wires the opened data to the connection; the group is usable afterwards. */
    fun activate(data: ScopedHubData) {
        val connection = checkNotNull(live)
        this.data = data
        val credentials = data.credentials
        val api = ApiClient(credentials.hubAddress, credentials.deviceToken, connection.http.client,
            onUnauthorized = connection::revoke,
            selectedOrigin = {
                check(this.data === data && active) { AppText.get(R.string.app_error_switched) }
                connection.route.value?.endpoint?.url ?: throw HubFailure(HubError.NoNetwork)
            })
        val hub = Hub(api, scope, connection)
        this.hub = hub
        hub.start()
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.events.collect { event ->
                if (event is Hello) {
                    mutableHello.value = event
                    listener.onHello(this@GroupRuntime, event)
                }
                listener.onEvent(this@GroupRuntime, event)
                if (event is ItemsEvent) data.messages.observeTranscript(SessionKey(event.machineId, event.sessionId), event.items)
            }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            combine(connection.state, reconciling) { state, syncing ->
                // A resumed socket only resubscribes the open chats: nothing to show (#1574).
                if (state == ConnectionState.Online && syncing && !connection.resumed) ConnectionState.Updating else state
            }.collect { state ->
                if (live !== connection) return@collect
                if (state !in setOf(ConnectionState.Online, ConnectionState.Updating, ConnectionState.Connecting)) {
                    reconciliationJob?.cancel()
                    reconciling.value = false
                }
                mutableState.value = state
                if (state == ConnectionState.Revoked) listener.onRevoked(this@GroupRuntime)
            }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.diagnostics.collect { if (live === connection) mutableDiagnostics.value = it }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            connection.tunnel.collect { if (live === connection) mutableTunnel.value = it }
        }
        // Before the network is known, so the first route selection already honours the stored route choices.
        mutableEndpointSettings.value = HubEndpointSettings(connection, data.endpoints.value, data.endpoints::update,
            scope, SystemClock::elapsedRealtime, System::currentTimeMillis)
    }

    /** A new registry entry for the same group: names, mesh hubs and the other paired hubs. */
    fun update(group: StoredGroup, namespace: String?, members: List<HubMember>, tunnels: List<HubTunnel>) {
        this.group = group
        this.namespace = namespace
        live?.updateMembers(members, group.centerHubId)
        live?.updateTunnels(tunnels)
    }

    /** The group's hubs' tunnels changed (Settings › Hubs). */
    fun updateTunnels(tunnels: List<HubTunnel>) { live?.updateTunnels(tunnels) }

    fun setForeground(value: Boolean) {
        foreground = value
        live?.setForeground(value)
    }

    fun onWake() { live?.onWake() }

    fun reconnect() {
        if (state.value == ConnectionState.Revoked) return
        live?.reconnect()
    }

    fun updateNetwork(network: HubNetwork) { live?.updateNetwork(network) }

    /** Stops the connection and everything launched for it; the data scope is closed by the caller. */
    fun close() {
        reconciliationJob?.cancel()
        reconciliationJob = null
        reconciling.value = false
        mutableEndpointSettings.value = null
        hub?.stop()
        hub = null
        live?.close()
        live = null
        job.cancel()
        mutableTunnel.value = null
        mutableDiagnostics.value = ConnectionDiagnostics()
        mutableState.value = ConnectionState.WaitingForNetwork
    }
}

/** The pairing's own candidates (the pairing address and its alternatives), before the hub sends its list. */
internal fun pairingEndpoints(credentials: HubCredentials) =
    (credentials.candidateAddresses + credentials.hubAddress).distinctBy(::endpointKey).map { address ->
        Endpoint(address, endpointKind(address))
    }

/** A paired hub of the group other than the connection's own, with its own credential and route choices. */
internal fun pairedMember(credentials: HubCredentials, overrides: EndpointOverrides, center: Boolean = false) =
    HubMember(credentials.hubId, credentials.fp, credentials.deviceToken, pairingEndpoints(credentials), overrides, center)

/** A hub learned from the mesh: reached with the group credential, pinned by the fingerprint the hello gave. */
internal fun meshMember(hub: persona.core.data.StoredMeshHub, center: Boolean = false) =
    HubMember(hub.hubId, hub.fp, null, hub.endpoints.distinctBy(::endpointKey).map { Endpoint(it, endpointKind(it)) }, center = center || hub.center == true)
