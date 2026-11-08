package persona.core.hub

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.Endpoint
import persona.core.contracts.api.ConnectionEndpoints
import persona.core.contracts.api.ConnectionEndpointsState
import persona.core.contracts.api.EndpointCheck
import persona.core.contracts.api.EndpointEditException
import persona.core.contracts.api.EndpointEntry
import persona.core.contracts.api.EndpointOverrides
import persona.core.contracts.api.EndpointSource
import persona.core.contracts.api.EndpointTestResult
import java.io.IOException
import java.time.Instant
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.merge
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/**
 * Manual candidates, disabling, the order to try and tests for one pairing's [connection]. [stored] and [save]
 * are the pairing's persisted [EndpointOverrides]; every saved change is pushed into the connection.
 */
class HubEndpointSettings(
    private val connection: OkHttpHubConnection,
    private val stored: StateFlow<EndpointOverrides>,
    private val save: suspend ((EndpointOverrides) -> EndpointOverrides) -> EndpointOverrides,
    scope: CoroutineScope,
    private val monotonicMillis: () -> Long = { System.nanoTime() / 1_000_000 },
    private val wallMillis: () -> Long = System::currentTimeMillis,
) : ConnectionEndpoints {
    private val edits = Mutex()
    /** Latest result and running count per key; the state shows them under each entry's current url. */
    private val results = MutableStateFlow<Map<String, EndpointTestResult>>(emptyMap())
    private val running = MutableStateFlow<Map<String, Int>>(emptyMap())
    /**
     * What the client threw behind each endpoint's last failed test. The result keeps only a label, and "Connection
     * failed" covers every transport error the classification does not name (#1519): for diagnosis, never shown.
     */
    internal val lastFailures = ConcurrentHashMap<String, Throwable>()
    /** https addresses can be verified: by the pinned key, or (a pairing without one) by the system CAs. */
    private val pinned get() = connection.http.fingerprint != null || connection.http.systemTrusted
    private val policy get() = connection.http.localAddressPolicy

    init {
        // Synchronously, so the first route selection already sees the choices.
        connection.updateOverrides(stored.value)
        // Pushes always read the latest stored value under the edit lock, so a slow collector cannot reapply an old one.
        scope.launch { stored.collect { edits.withLock { connection.updateOverrides(stored.value) } } }
    }

    override val state: StateFlow<ConnectionEndpointsState> =
        merge(connection.endpoints, connection.advertised, connection.overrides, connection.route, results, running)
            .map { snapshot() }
            .stateIn(scope, SharingStarted.Eagerly, snapshot())

    private fun snapshot(): ConnectionEndpointsState {
        val overrides = connection.overrides.value
        val manual = overrides.manual.mapTo(HashSet(), ::endpointKey)
        val advertised = connection.advertised.value
        val route = connection.route.value
        val active = route?.endpoint?.url?.let(::endpointKey)
        // The one order: what the list shows is what the connection tries (disabled ones keep their place, skipped).
        val entries = arrangeEndpoints(connection.candidates(overrides), overrides).map {
            val key = endpointKey(it.url)
            EndpointEntry(
                url = it.url, kind = it.kind,
                source = when (key) { in manual -> EndpointSource.Manual; in advertised -> EndpointSource.Hub; else -> EndpointSource.Pairing },
                enabled = key !in overrides.disabled, active = key == active,
            )
        }
        val urls = entries.associate { endpointKey(it.url) to it.url }
        return ConnectionEndpointsState(
            entries = entries,
            activeUrl = route?.let { urls[active] ?: it.endpoint.url },
            customOrder = overrides.order != null,
            pinned = pinned,
            tests = results.value.mapNotNull { (key, result) -> urls[key]?.let { it to result.copy(url = it) } }.toMap(),
            testing = running.value.keys.mapNotNullTo(HashSet(), urls::get),
        )
    }

    override fun check(address: String, replacing: String?): EndpointCheck = check(address, stored.value, replacing?.let(::endpointKey))

    private fun check(address: String, current: EndpointOverrides, replacing: String?) = checkEndpoint(
        address, pinned, policy, connection.candidates(current).mapTo(HashSet()) { endpointKey(it.url) },
        current.manual.size, replacing,
    )

    override suspend fun add(address: String): String {
        var added = ""
        edit { current ->
            added = valid(address, current, null)
            // With the user's order, a new address goes to the end, where the user sees it appear: below the hub's new
            // addresses that follow the order too, so those are written into the order where they are shown.
            val order = current.order?.let { listed -> listed + shown(current).filter { it !in listed } - added + added }
            current.copy(manual = current.manual + added, disabled = current.disabled - added, order = order)
        }
        return added
    }

    override suspend fun replace(url: String, address: String): String {
        val old = endpointKey(url)
        var replaced = ""
        edit { current ->
            requireManual(current, old)
            val new = valid(address, current, old)
            replaced = new
            if (new == old) return@edit current
            current.copy(
                manual = current.manual.map { if (endpointKey(it) == old) new else it },
                disabled = (current.disabled - old - new).let { if (old in current.disabled) it + new else it },
                // In the old one's place; a stale copy of the new key (an address the hub no longer sends) goes.
                order = current.order?.mapNotNull { when (it) { old -> new; new -> null; else -> it } },
            )
        }
        if (replaced != old) results.update { it - old }
        return replaced
    }

    override suspend fun remove(url: String) {
        val key = endpointKey(url)
        edit { current ->
            requireManual(current, key)
            current.copy(
                manual = current.manual.filterNot { endpointKey(it) == key }, disabled = current.disabled - key,
                order = current.order?.minus(key),
            ).also { keepOneEnabled(current, it, key) }
        }
        results.update { it - key }
    }

    override suspend fun setEnabled(url: String, enabled: Boolean) {
        val key = endpointKey(url)
        edit { current ->
            requireEntry(current, key)
            when {
                enabled -> current.copy(disabled = current.disabled - key)
                key in current.disabled -> current
                else -> current.copy(disabled = current.disabled + key).also { keepOneEnabled(current, it, key) }
            }
        }
    }

    override suspend fun reorder(urls: List<String>) {
        edit { current ->
            val shown = shown(current)
            val wanted = urls.map(::endpointKey).filter { it in shown }.distinct()
            val order = wanted + shown.filter { it !in wanted }
            if (current.order != null && order == shown) current else current.copy(order = order)
        }
    }

    override suspend fun resetOrder() {
        edit { it.copy(order = null) }
    }

    /** The keys of every entry in the order settings lists them (and the connection tries the enabled ones). */
    private fun shown(current: EndpointOverrides) = arrangeEndpoints(connection.candidates(current), current).map { endpointKey(it.url) }

    override suspend fun test(url: String): EndpointTestResult {
        val key = endpointKey(url)
        val endpoint = connection.candidates().firstOrNull { endpointKey(it.url) == key }
            ?: throw EndpointEditException(CoreText.of(Key.EndpointNotListed))
        running.update { it + (key to (it[key] ?: 0) + 1) }
        try {
            val started = monotonicMillis()
            val failure = try {
                connection.http.health(endpoint, fresh = true)
                null
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                HubFailure.from(error)
            }
            if (failure == null) lastFailures.remove(key) else lastFailures[key] = failure.cause ?: failure
            val result = EndpointTestResult(endpoint.url, failure == null, endpointResultLabel(failure?.kind),
                (monotonicMillis() - started).coerceAtLeast(0), Instant.ofEpochMilli(wallMillis()).toString())
            results.update { it + (key to result) }
            return result
        } finally {
            running.update { counts ->
                val left = (counts[key] ?: 1) - 1
                if (left > 0) counts + (key to left) else counts - key
            }
        }
    }

    /** Validates [change] against the current choices first (a no-op saves nothing), then applies the saved value. */
    private suspend fun edit(change: (EndpointOverrides) -> EndpointOverrides): Unit = edits.withLock {
        if (change(stored.value) == stored.value) return@withLock
        val saved = try { save(change) } catch (failure: IOException) { throw EndpointEditException(CoreText.of(Key.EndpointSaveFailed)) }
        connection.updateOverrides(saved)
    }

    private fun valid(address: String, current: EndpointOverrides, replacing: String?): String =
        when (val result = check(address, current, replacing)) {
            is EndpointCheck.Valid -> result.url
            is EndpointCheck.Invalid -> throw EndpointEditException(result.message)
        }

    private fun requireEntry(current: EndpointOverrides, key: String): Endpoint =
        connection.candidates(current).firstOrNull { endpointKey(it.url) == key } ?: throw EndpointEditException(CoreText.of(Key.EndpointNotListed))

    private fun requireManual(current: EndpointOverrides, key: String) {
        requireEntry(current, key)
        if (current.manual.none { endpointKey(it) == key }) throw EndpointEditException(CoreText.of(Key.EndpointOnlyManual))
    }

    /** Refuses a change that would leave no enabled candidate when [key] was one. */
    private fun keepOneEnabled(current: EndpointOverrides, next: EndpointOverrides, key: String) {
        fun enabled(overrides: EndpointOverrides) = connection.candidates(overrides).map { endpointKey(it.url) }
            .filter { it !in overrides.disabled }
        if (key in enabled(current) && enabled(next).isEmpty()) throw EndpointEditException(CoreText.of(Key.EndpointKeepOne))
    }
}
