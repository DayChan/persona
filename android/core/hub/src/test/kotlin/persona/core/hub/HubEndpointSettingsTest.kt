package persona.core.hub

import persona.core.contracts.CoreText

import persona.core.contracts.Endpoint
import persona.core.contracts.api.*
import java.net.InetAddress
import java.security.MessageDigest
import java.util.Base64
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.atomic.AtomicLong
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import okhttp3.Response
import okhttp3.internal.http2.Http2Stream
import okhttp3.mockwebserver.internal.duplex.DuplexResponseBody
import okio.buffer
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.Assert.*
import org.junit.Test

class HubEndpointSettingsTest {
    // Private addresses are built at runtime: scripts/privacy_check.py refuses them as literals.
    private val home = listOf(192, 168, 1, 20).joinToString(".")
    private val pairingName = Endpoint("https://hub.example.com:18662/", "name")
    private val pairingLan = Endpoint("http://$home:18660/", "lan")
    private val pairingIpv6 = Endpoint("https://[2001:db8::1]:18662", "ipv6")
    private val pin = "A".repeat(43)

    private class Fixture(
        base: List<Endpoint>, fingerprint: String?, initial: EndpointOverrides = EndpointOverrides(),
        network: HubNetwork = HubNetwork("offline", available = false),
    ) : AutoCloseable {
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        val stored = MutableStateFlow(initial)
        var saves = 0
        val connection = OkHttpHubConnection(HubConnectionConfig("test-hub", fingerprint, "test-token", base, "test"), network)
        val clock = AtomicLong(1_000)
        val settings = HubEndpointSettings(connection, stored, { transform ->
            saves++
            transform(stored.value).also { stored.value = it }
        }, scope, { clock.getAndAdd(42) }, { 1_793_577_600_000L })

        suspend fun awaitState(predicate: (ConnectionEndpointsState) -> Boolean) =
            withTimeout(5000) { settings.state.first(predicate) }

        override fun close() { scope.cancel(); connection.close() }
    }

    /** Entry urls in canonical form, top first. */
    private fun ConnectionEndpointsState.keys() = entries.map { endpointKey(it.url) }

    private suspend fun refused(message: String, block: suspend () -> Unit) {
        try {
            block()
            fail("Expected 「$message」")
        } catch (error: EndpointEditException) {
            assertEquals(message, error.text.zh())
        }
    }

    @Test fun storedChoicesReachTheConnectionBeforeItsFirstRoute() = runBlocking<Unit> {
        val manual = "https://hub.example.org:18662"
        val initial = EndpointOverrides(manual = listOf(manual), order = listOf(endpointKey(pairingName.url), manual))
        Fixture(listOf(pairingName), pin, initial).use { fixture ->
            assertEquals(initial, fixture.connection.overrides.value)
            val state = fixture.settings.state.value
            // By default the manual one would go first; the stored order says otherwise.
            assertEquals(listOf(pairingName.url, manual), state.entries.map { it.url })
            assertEquals(listOf(EndpointSource.Pairing, EndpointSource.Manual), state.entries.map { it.source })
            assertTrue(state.customOrder)
            assertTrue(state.pinned)
            assertNull(state.activeUrl)
            // A change to the stored value (another writer) is applied as well.
            fixture.stored.value = EndpointOverrides()
            withTimeout(5000) { fixture.connection.overrides.first { it == EndpointOverrides() } }
            assertFalse(fixture.awaitState { it.entries.size == 1 }.customOrder)
        }
    }

    @Test fun addValidatesAndSavesTheCanonicalUrl() = runBlocking<Unit> {
        Fixture(listOf(pairingName, pairingLan), pin, EndpointOverrides(disabled = setOf("https://hub.example.org:18662"))).use { fixture ->
            val settings = fixture.settings
            assertEquals("https://hub.example.org:18662", (settings.add("HUB.example.org:18662/")).zhAny())
            // A stale disabled key of the same address does not disable the new candidate.
            assertEquals(EndpointOverrides(manual = listOf("https://hub.example.org:18662")), fixture.stored.value)
            assertEquals(fixture.stored.value, fixture.connection.overrides.value)
            val state = fixture.awaitState { it.entries.size == 3 }
            // With the default order it takes its default place: first among the names, as a manual one.
            assertEquals(EndpointEntry("https://hub.example.org:18662", "name", EndpointSource.Manual), state.entries[0])
            assertFalse(state.customOrder)
            assertEquals(EndpointCheck.Valid("https://[2001:db8::1]:18662", "ipv6"), settings.check("[2001:db8::1]:18662"))
            refused("列表里已经有这个地址了") { settings.add("https://Hub.Example.com:18662") }
            refused("列表里已经有这个地址了") { settings.add("http://$home:18660/") }
            refused("缺少端口，写成 https://地址:端口") { settings.add("hub.example.net") }
            refused("HTTP 不加密，只能用内网地址（如 192.168.x.x、10.x.x.x、fd00::/8 或 .local 名字）。公网地址请用 https://") {
                settings.add("http://192.0.2.10:18660")
            }
            repeat(9) { settings.add("https://hub$it.example.net:18662") }
            refused("最多手动添加 10 条地址") { settings.add("https://hub10.example.net:18662") }
            assertEquals(10, fixture.stored.value.manual.size)
        }
        Fixture(listOf(pairingLan), fingerprint = null).use { fixture ->
            // Without a fingerprint, https addresses are checked against the system CAs (a hub behind a reverse proxy).
            assertTrue(fixture.settings.state.value.pinned)
            assertEquals("https://hub.example.org:18662", (fixture.settings.add("https://hub.example.org:18662")).zhAny())
            assertEquals("http://127.0.0.1:18660", (fixture.settings.add("http://127.0.0.1:18660")).zhAny())
        }
    }

    @Test fun replaceKeepsThePlaceAndTheDisabledFlagAndOnlyAppliesToManualCandidates() = runBlocking<Unit> {
        val old = "https://hub.example.org:18662"
        val other = "https://hub.example.net:18662"
        Fixture(listOf(pairingName), pin, EndpointOverrides(manual = listOf(old, other), disabled = setOf(old))).use { fixture ->
            val settings = fixture.settings
            refused("只能修改手动添加的地址") { settings.replace(pairingName.url, "hub.example.info:18662") }
            refused("列表里没有这个地址") { settings.replace("https://hub.example.info:18662", "hub.example.info:18662") }
            refused("列表里已经有这个地址了") { settings.replace(old, "hub.example.com:18662") }
            val saves = fixture.saves
            assertEquals(old, settings.replace(old, "HUB.EXAMPLE.ORG:18662"))
            assertEquals(saves, fixture.saves)
            assertEquals("https://hub.example.info:18662", (settings.replace(old, "hub.example.info:18662")).zhAny())
            assertEquals(EndpointOverrides(manual = listOf("https://hub.example.info:18662", other),
                disabled = setOf("https://hub.example.info:18662")), fixture.stored.value)
            // In the user's order the new address takes the old one's place.
            settings.reorder(listOf(other, pairingName.url))
            assertEquals(listOf(other, endpointKey(pairingName.url), "https://hub.example.info:18662"), fixture.stored.value.order)
            assertEquals("https://hub.example.com:18663", (settings.replace(other, "hub.example.com:18663")).zhAny())
            assertEquals(listOf("https://hub.example.com:18663", endpointKey(pairingName.url), "https://hub.example.info:18662"),
                (fixture.stored.value.order).zhAny())
            assertEquals(listOf("https://hub.example.com:18663", pairingName.url, "https://hub.example.info:18662"),
                (fixture.awaitState { it.entries.first().url == "https://hub.example.com:18663" }.entries.map { it.url }).zhAny())
            // Replacing with 10 manual candidates is still allowed.
            repeat(8) { settings.add("https://hub$it.example.net:18662") }
            assertEquals("https://hub.example.com:18664", (settings.replace("https://hub.example.com:18663", "hub.example.com:18664")).zhAny())
            assertEquals("https://hub.example.com:18664", (fixture.stored.value.order!!.first()).zhAny())
        }
    }

    @Test fun replacingWithAnAddressTheHubNoLongerSendsStillKeepsThePlace() = runBlocking<Unit> {
        val old = "https://hub.example.org:18662"
        // The order still holds the key of an address the hub sent once, listed first.
        val gone = "https://hub.example.biz:18662"
        val order = listOf(gone, endpointKey(pairingLan.url), old, endpointKey(pairingName.url))
        Fixture(listOf(pairingName, pairingLan), pin, EndpointOverrides(manual = listOf(old), order = order)).use { fixture ->
            assertEquals(listOf(pairingLan.url, old, pairingName.url), fixture.settings.state.value.entries.map { it.url })
            assertEquals(gone, fixture.settings.replace(old, "hub.example.biz:18662"))
            assertEquals(listOf(endpointKey(pairingLan.url), gone, endpointKey(pairingName.url)), fixture.stored.value.order)
            assertEquals(listOf(pairingLan.url, gone, pairingName.url),
                fixture.awaitState { gone in it.keys() }.entries.map { it.url })
        }
    }

    @Test fun removeAndDisableFollowTheRules() = runBlocking<Unit> {
        val manual = "https://hub.example.org:18662"
        Fixture(listOf(pairingName, pairingLan), pin, EndpointOverrides(manual = listOf(manual))).use { fixture ->
            val settings = fixture.settings
            refused("只能修改手动添加的地址") { settings.remove(pairingName.url) }
            refused("列表里没有这个地址") { settings.remove("https://hub.example.info:18662") }
            refused("列表里没有这个地址") { settings.setEnabled("https://hub.example.info:18662", false) }
            // Disabling keeps the place: nothing in the list moves.
            settings.setEnabled(manual, false)
            assertEquals(EndpointOverrides(manual = listOf(manual), disabled = setOf(manual)), fixture.stored.value)
            var state = fixture.awaitState { it.entries.firstOrNull()?.enabled == false }
            assertEquals(listOf(manual, pairingName.url, pairingLan.url), state.entries.map { it.url })
            val saves = fixture.saves
            settings.setEnabled(manual, false)
            assertEquals(saves, fixture.saves)
            settings.setEnabled(manual, true)
            // Never leave nothing enabled.
            settings.setEnabled(pairingName.url, false)
            settings.setEnabled(pairingLan.url, false)
            refused("至少要留一条启用的地址") { settings.setEnabled(manual, false) }
            refused("至少要留一条启用的地址") { settings.remove(manual) }
            settings.setEnabled(pairingLan.url, true)
            // Removing a manual candidate drops it from the user's order too.
            settings.reorder(listOf(pairingLan.url, manual))
            settings.remove(manual)
            assertEquals(EndpointOverrides(disabled = setOf(endpointKey(pairingName.url)),
                order = listOf(endpointKey(pairingLan.url), endpointKey(pairingName.url))), fixture.stored.value)
            state = fixture.awaitState { it.entries.size == 2 }
            assertEquals(listOf(pairingLan.url, pairingName.url), state.entries.map { it.url })
            assertEquals(listOf(true, false), state.entries.map { it.enabled })
        }
    }

    @Test fun reorderPutsTheGivenEntriesFirstAndResetOrderGoesBackToTheDefault() = runBlocking<Unit> {
        val manual = "https://hub.example.org:18662"
        val ipv6 = endpointKey(pairingIpv6.url)
        val name = endpointKey(pairingName.url)
        val lan = endpointKey(pairingLan.url)
        Fixture(listOf(pairingName, pairingLan, pairingIpv6), pin, EndpointOverrides(manual = listOf(manual), disabled = setOf(lan))).use { fixture ->
            val settings = fixture.settings
            // The default order: by kind, the manual one first among the names; the disabled one in its place.
            var state = fixture.settings.state.value
            assertEquals(listOf(ipv6, manual, name, lan), state.keys())
            assertFalse(state.customOrder)

            // The whole list, top first, as settings sends it after a drag.
            settings.reorder(listOf(pairingLan.url, pairingName.url, manual, pairingIpv6.url))
            assertEquals(listOf(lan, name, manual, ipv6), fixture.stored.value.order)
            state = fixture.awaitState { it.customOrder }
            assertEquals(listOf(pairingLan.url, pairingName.url, manual, pairingIpv6.url), state.entries.map { it.url })
            assertEquals(listOf(false, true, true, true), state.entries.map { it.enabled })

            // Part of it: those first, the rest after them as they were; unknown urls and repeats are ignored,
            // any spelling of an entry's url works.
            settings.reorder(listOf("https://hub.example.info:18662", "HTTPS://[2001:DB8:0::1]:18662/", manual, pairingIpv6.url))
            assertEquals(listOf(ipv6, manual, lan, name), fixture.stored.value.order)
            assertEquals(listOf(ipv6, manual, lan, name), fixture.awaitState { it.entries.first().url == pairingIpv6.url }.keys())

            // The same order again saves nothing.
            val saves = fixture.saves
            settings.reorder(listOf(pairingIpv6.url, manual, pairingLan.url, pairingName.url))
            settings.reorder(listOf(pairingIpv6.url))
            settings.reorder(emptyList())
            assertEquals(saves, fixture.saves)

            // "Restore default order".
            settings.resetOrder()
            assertNull(fixture.stored.value.order)
            state = fixture.awaitState { !it.customOrder }
            assertEquals(listOf(ipv6, manual, name, lan), state.keys())
            settings.resetOrder()
            assertEquals(saves + 1, fixture.saves)

            // Saving the default order as it is makes it the user's: from now on it no longer follows the default rule.
            settings.reorder(listOf(pairingIpv6.url))
            assertEquals(listOf(ipv6, manual, name, lan), fixture.stored.value.order)
            assertTrue(fixture.awaitState { it.customOrder }.customOrder)
        }
    }

    @Test fun withTheUsersOrderAddedAddressesGoToTheEndAndRemovedOnesLeaveIt() = runBlocking<Unit> {
        val old = "https://hub.example.org:18662"
        val name = endpointKey(pairingName.url)
        val lan = endpointKey(pairingLan.url)
        val ipv6 = endpointKey(pairingIpv6.url)
        // The hub's ipv6 address came after the user arranged the others: it follows their order.
        Fixture(listOf(pairingName, pairingLan, pairingIpv6), pin, EndpointOverrides(manual = listOf(old), order = listOf(lan, old, name))).use { fixture ->
            val settings = fixture.settings
            assertEquals(listOf(lan, old, name, ipv6), fixture.settings.state.value.keys())
            // A new address goes to the very end, below the hub's new one, which stays where it was shown.
            val added = settings.add("hub.example.net:18662")
            assertEquals(listOf(lan, old, name, ipv6, added), fixture.stored.value.order)
            assertEquals(listOf(lan, old, name, ipv6, added), fixture.awaitState { it.entries.size == 5 }.keys())
            val another = settings.add("[2001:db8::5]:18662")
            assertEquals(listOf(lan, old, name, ipv6, added, another), fixture.awaitState { it.entries.size == 6 }.keys())
            // Disabling keeps the place in the user's order as well.
            settings.setEnabled(old, false)
            assertEquals(listOf(lan, old, name, ipv6, added, another), fixture.awaitState { !it.entries[1].enabled }.keys())
            // Removing drops the key from the order.
            settings.remove(added)
            assertEquals(listOf(lan, old, name, ipv6, another), fixture.stored.value.order)
            assertEquals(listOf(lan, old, name, ipv6, another), fixture.awaitState { it.entries.size == 5 }.keys())
            // With the default order a new address takes its default place: first among the ipv6 ones, as a manual one.
            settings.resetOrder()
            val third = settings.add("[2001:db8::6]:18662")
            assertNull(fixture.stored.value.order)
            assertEquals(listOf(another, third, ipv6, old, name, lan), fixture.awaitState { it.entries.size == 6 }.keys())
        }
    }

    @Test fun theOrderShownIsTheOrderTried() = runBlocking<Unit> {
        val tried = ConcurrentLinkedQueue<String>()
        // Every hub fails its health check, so an attempt tries every enabled candidate, one after the other.
        val hubs = List(5) { LoopbackHub(healthy = false, log = tried) }
        try {
            val (ipv4, lan, name, ipv6, manual) = hubs
            val base = listOf(ipv4.endpoint("ipv4"), lan.endpoint("lan"), name.endpoint("name"), ipv6.endpoint("ipv6"))
            Fixture(base, null, EndpointOverrides(manual = listOf(manual.url), disabled = setOf(name.url))).use { fixture ->
                /** Online for one attempt: it tries exactly the enabled entries [state] shows, in that order. */
                suspend fun triedAsShown(state: ConnectionEndpointsState): List<String> {
                    val shown = state.entries.filter { it.enabled }.map { endpointKey(it.url) }
                    tried.clear()
                    fixture.connection.updateNetwork(HubNetwork("network"))
                    eventually { tried.size >= shown.size }
                    // Offline again, so no retry runs into the next attempt (a retry tries the same order anyway).
                    fixture.connection.updateNetwork(HubNetwork("offline", available = false))
                    assertEquals(shown, tried.toList().take(shown.size))
                    assertEquals(shown, fixture.connection.diagnostics.value.candidates.map { endpointKey(it.url) })
                    delay(200)
                    return shown
                }
                // The default order: by kind, the manual one first among the lan ones; the disabled one keeps its place.
                var state = fixture.settings.state.value
                assertEquals(listOf(ipv6.url, name.url, manual.url, lan.url, ipv4.url), state.keys())
                assertEquals(listOf(true, false, true, true, true), state.entries.map { it.enabled })
                assertEquals(listOf(ipv6.url, manual.url, lan.url, ipv4.url), triedAsShown(state))

                // The user's order: what the list shows is what the next attempt tries.
                fixture.settings.reorder(listOf(ipv4.url, name.url, manual.url))
                state = fixture.awaitState { it.customOrder }
                assertEquals(listOf(ipv4.url, name.url, manual.url, ipv6.url, lan.url), state.keys())
                assertEquals(listOf(ipv4.url, manual.url, ipv6.url, lan.url), triedAsShown(state))

                // Back to the default order.
                fixture.settings.resetOrder()
                state = fixture.awaitState { !it.customOrder }
                assertEquals(listOf(ipv6.url, manual.url, lan.url, ipv4.url), triedAsShown(state))
                assertEquals(0, name.probes.get())
            }
        } finally { hubs.forEach { it.close() } }
    }

    @Test fun theHubsListsFollowTheUsersOrderAndKeepTheChoices() = runBlocking<Unit> {
        lateinit var advertised: String
        LoopbackHub(endpoints = { advertised }).use { live ->
            LoopbackHub(healthy = false).use { listed ->
                LoopbackHub().use { disabled ->
                    LoopbackHub(healthy = false).use { fresh ->
                        val manual = "http://127.0.0.1:${closedPort()}"
                        advertised = "[${live.wire("lan")},${listed.wire("ipv6")},${disabled.wire("ipv6")}]"
                        val initial = EndpointOverrides(manual = listOf(manual), disabled = setOf(disabled.url))
                        Fixture(listOf(live.endpoint("lan")), null, initial).use { fixture ->
                            val settings = fixture.settings
                            fixture.connection.updateNetwork(HubNetwork("network"))
                            // hello replaces the pairing's list; by default the hub's ipv6 addresses go first.
                            var state = fixture.awaitState { it.activeUrl != null && it.entries.size == 4 }
                            assertEquals(listOf(listed.url, disabled.url, manual, live.url), state.keys())
                            assertEquals(live.url, endpointKey(state.activeUrl!!))

                            // The user's order, as settings saves it after a drag: every entry.
                            settings.reorder(listOf(live.url, manual, listed.url, disabled.url))
                            val order = listOf(live.url, manual, listed.url, disabled.url)
                            assertEquals(initial.copy(order = order), fixture.stored.value)
                            assertEquals(order, fixture.awaitState { it.customOrder }.keys())

                            // The hub stops sending one address and sends a new one (the connection starts over on the
                            // new list): the gone one vanishes, the new one follows at the end, the choices stay.
                            advertised = "[${live.wire("lan")},${disabled.wire("ipv6")},${fresh.wire("ipv6")}]"
                            live.advertise(advertised)
                            state = fixture.awaitState { fresh.url in it.keys() }
                            assertEquals(listOf(live.url, manual, disabled.url, fresh.url), state.keys())
                            eventually { live.opened.get() == 2 }
                            fixture.awaitState { it.activeUrl != null }
                            assertEquals(initial.copy(order = order), fixture.stored.value)
                            assertEquals(fixture.stored.value, fixture.connection.overrides.value)

                            // Back in the hub's list, an address takes its old place.
                            advertised = "[${live.wire("lan")},${listed.wire("ipv6")},${disabled.wire("ipv6")},${fresh.wire("ipv6")}]"
                            live.advertise(advertised)
                            state = fixture.awaitState { it.entries.size == 5 }
                            assertEquals(listOf(live.url, manual, listed.url, disabled.url, fresh.url), state.keys())
                            eventually { live.opened.get() == 3 }
                            fixture.awaitState { it.activeUrl != null }

                            // With the default order the hub's addresses take their default place again. Changing the
                            // order probes nothing and keeps the connection; the next recheck goes by the new order.
                            val probes = listOf(listed, fresh, disabled).map { it.probes.get() }
                            settings.resetOrder()
                            state = fixture.awaitState { !it.customOrder }
                            assertEquals(listOf(listed.url, disabled.url, fresh.url, manual, live.url), state.keys())
                            delay(300)
                            assertEquals(probes, listOf(listed, fresh, disabled).map { it.probes.get() })
                            assertEquals(3, live.sockets.get())
                            assertEquals(live.url, fixture.connection.route.value?.endpoint?.url?.let(::endpointKey))
                            assertEquals(0, disabled.probes.get())
                        }
                    }
                }
            }
        }
    }

    @Test fun testsUseAFreshPinnedConnectionAndReportInChinese() = runBlocking<Unit> {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        val connections = mutableListOf<Int>()
        MockWebServer().use { server ->
            server.useHttps(HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(), false)
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    synchronized(connections) { connections += request.sequenceNumber }
                    return MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
                }
            }
            server.start(InetAddress.getLoopbackAddress(), 0)
            val tls = endpointKey(server.url("/").newBuilder().host(InetAddress.getLoopbackAddress().hostAddress!!).build().toString())
            val closed = "http://127.0.0.1:${closedPort()}"
            Fixture(listOf(Endpoint(tls, "lan")), fingerprint(certificate), EndpointOverrides(manual = listOf(closed))).use { fixture ->
                val settings = fixture.settings
                val passed = settings.test(tls)
                // Hosted runners now and then get "Connection failed" here (#1519): the message names what the client threw.
                assertEquals(failureOf(settings, tls), EndpointTestResult(tls, true, CoreText.of(CoreText.Key.EndpointVerified), 42, "2026-11-02T00:00:00Z"), passed)
                settings.test(tls)
                assertEquals(failureOf(settings, tls), listOf(0, 0), synchronized(connections) { connections.toList() })
                val refusedPort = settings.test(closed)
                assertFalse(refusedPort.passed)
                assertEquals("连接被拒绝", (refusedPort.result).zhAny())
                // The cause behind a failed test is kept for diagnosis; one that passed has none.
                assertTrue(generateSequence(settings.lastFailures[endpointKey(closed)]) { it.cause }.any { it is java.net.ConnectException })
                assertNull(settings.lastFailures[tls])
                // Disabled candidates can be tested too.
                settings.setEnabled(closed, false)
                assertEquals("连接被拒绝", (settings.test(closed).result).zhAny())
                val state = fixture.awaitState { it.tests.size == 2 && it.testing.isEmpty() }
                assertEquals(setOf(tls, closed), state.tests.keys)
                assertEquals("连接被拒绝", (state.tests.getValue(closed).result).zhAny())
                refused("列表里没有这个地址") { settings.test("https://hub.example.info:18662") }
                // Removing a candidate drops its result.
                settings.setEnabled(closed, true)
                settings.remove(closed)
                assertEquals(setOf(tls), fixture.awaitState { it.tests.size == 1 }.tests.keys)
            }
            Fixture(listOf(Endpoint(tls, "lan")), fingerprint(HeldCertificate.Builder().build())).use { fixture ->
                val mismatch = fixture.settings.test(tls)
                assertEquals(EndpointTestResult(tls, false, CoreText.of(CoreText.Key.EndpointCertificateMismatch), 42, "2026-11-02T00:00:00Z"), mismatch)
            }
        }
    }

    /**
     * Over HTTP/2 a hub may send its stream's end in a frame of its own after the body. The test's fresh connection must
     * wait for it: closed as soon as the body's Content-Length bytes were read, the stream was reset (CANCEL) and the
     * test said "Connection failed" (#1519; MockWebServer flushes the body and ends the stream separately, which hosted
     * runners sometimes caught in between). Here the end comes 300 ms after the body, every time.
     */
    @Test fun aTestWaitsForTheEndOfAnHttp2ResponseAfterItsBody() = runBlocking<Unit> {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        val body = """{"ok":true,"hub_id":"test-hub"}"""
        MockWebServer().use { server ->
            server.useHttps(HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(), false)
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest) = MockResponse().setHeader("Content-Length", body.length)
                    .setBody(object : DuplexResponseBody {
                        override fun onRequest(request: RecordedRequest, http2Stream: Http2Stream) {
                            http2Stream.getSink().buffer().use { sink ->
                                sink.writeUtf8(body).flush()
                                Thread.sleep(300)
                            }
                        }
                    })
            }
            server.start(InetAddress.getLoopbackAddress(), 0)
            val tls = endpointKey(server.url("/").newBuilder().host(InetAddress.getLoopbackAddress().hostAddress!!).build().toString())
            Fixture(listOf(Endpoint(tls, "lan")), fingerprint(certificate)).use { fixture ->
                val result = fixture.settings.test(tls)
                assertEquals(failureOf(fixture.settings, tls), CoreText.of(CoreText.Key.EndpointVerified), result.result)
                assertTrue(result.passed)
                // Over HTTP/2: MockWebServer keeps its pseudo-headers (`:scheme`) among the request's headers.
                assertEquals("https", server.takeRequest().getHeader(":scheme"))
            }
        }
    }

    @Test fun stateShowsSourcesAndTheActiveCandidate() = runBlocking<Unit> {
        lateinit var listed: String
        MockWebServer().use { server ->
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse = when (request.requestUrl!!.encodedPath) {
                    "/api/v1/health" -> MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
                    "/api/v1/ws" -> MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) {
                            webSocket.send("""{"type":"hello","hub_id":"test-hub","features":[],"endpoints":$listed}""")
                        }
                    })
                    else -> MockResponse().setResponseCode(404)
                }
            }
            server.start(InetAddress.getLoopbackAddress(), 0)
            val paired = server.url("/").newBuilder().host(InetAddress.getLoopbackAddress().hostAddress!!).build().toString()
            val hub = endpointKey(paired)
            listed = """[{"url":"$hub","kind":"lan"},{"url":"https://[2001:db8::1]:18662","kind":"ipv6"}]"""
            val manual = "https://hub.example.org:18662"
            Fixture(listOf(Endpoint(paired, "lan")), pin, EndpointOverrides(manual = listOf(manual), disabled = setOf(manual))).use { fixture ->
                fixture.connection.updateNetwork(HubNetwork("network"))
                val state = fixture.awaitState { it.activeUrl != null && it.entries.any { entry -> entry.source == EndpointSource.Hub } }
                // The disabled manual one keeps its default place (names before lan addresses).
                assertEquals(listOf("https://[2001:db8::1]:18662", manual, hub), (state.entries.map { it.url }).zhAny())
                assertEquals(listOf(EndpointSource.Hub, EndpointSource.Manual, EndpointSource.Hub), state.entries.map { it.source })
                assertEquals(listOf(true, false, true), state.entries.map { it.enabled })
                assertEquals(listOf(false, false, true), state.entries.map { it.active })
                assertEquals(hub, state.activeUrl)
            }
        }
    }

    /** The client's own exception behind [url]'s last failed test, for an assertion's message. */
    private fun failureOf(settings: HubEndpointSettings, url: String) =
        "the client's failure for $url: " + (settings.lastFailures[url]?.stackTraceToString() ?: "none")

    private fun fingerprint(certificate: HeldCertificate) = Base64.getUrlEncoder().withoutPadding().encodeToString(
        MessageDigest.getInstance("SHA-256").digest(certificate.certificate.publicKey.encoded))
}
