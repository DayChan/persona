package persona.core.hub.tunnel

import java.net.URI
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Endpoint
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.LocalProxyRoute
import persona.core.contracts.api.TunnelKind
import persona.core.contracts.api.TunnelProblem
import persona.core.contracts.api.TunnelState
import persona.core.hub.HubConnectionConfig
import persona.core.hub.HubNetwork
import persona.core.hub.LoopbackHub
import persona.core.hub.OkHttpHubConnection
import persona.core.hub.closedPort
import persona.core.hub.endpointKey
import persona.core.hub.eventually

/** A hub connection picks its way through its hub's tunnels (iOS `HubWay`, docs/operations/network.md). */
class HubTunnelConnectionTest {
    private val identity = SshIdentity(SshIdentity.generateSeed(), "persona-test")
    private val LoopbackHub.port get() = URI(paired).port

    private fun connection(address: String, ways: HubWays, vararg tunnels: HubTunnel, recheckMillis: Long = HubWay.RECHECK_MILLIS) =
        OkHttpHubConnection(
            HubConnectionConfig("hub-a", null, "token", listOf(Endpoint(address, "lan")), "test", tunnels = tunnels.toList()),
            HubNetwork("offline", available = false), ways = ways, tunnelRecheckMillis = recheckMillis,
        )

    private suspend fun OkHttpHubConnection.awaitOnline() = withTimeout(10_000) { state.first { it == ConnectionState.Online } }

    @Test fun aHubWhoseAddressDoesNotAnswerIsReachedThroughItsLocalProxy() = runBlocking<Unit> {
        LoopbackHub(hubId = "hub-a", version = "1").use { hub ->
            FakeSocks5().use { proxy ->
                val address = "http://127.0.0.1:${closedPort()}/"
                val tunnel = HubTunnel("hub-a", address, proxy = LocalProxyRoute(port = proxy.port, targetHost = "127.0.0.1", targetPort = hub.port))
                HubWays(identity = { identity }).use { ways ->
                    connection(address, ways, tunnel).use { connection ->
                        connection.updateNetwork(HubNetwork("network"))
                        connection.awaitOnline()
                        assertEquals(TunnelKind.Proxy, connection.tunnel.value?.kind)
                        assertTrue(endpointKey(connection.route.value!!.endpoint.url).startsWith("http://127.0.0.1:"))
                        assertEquals(TunnelState.Up, ways.states.value["hub-a" to TunnelKind.Proxy])
                        connection.request("/api/v1/ping").close()
                        assertTrue(hub.authorizations.any { it.first == "/api/v1/ping" && it.second == "Bearer token" })
                    }
                    assertNull(ways.states.value["hub-a" to TunnelKind.Proxy])
                }
            }
        }
    }

    @Test fun anAnsweringAddressGoesStraightAndTheProxyIsNotAsked() = runBlocking<Unit> {
        LoopbackHub(hubId = "hub-a", version = "1").use { hub ->
            FakeSocks5().use { proxy ->
                val tunnel = HubTunnel("hub-a", hub.paired, proxy = LocalProxyRoute(port = proxy.port))
                HubWays(identity = { identity }).use { ways ->
                    connection(hub.paired, ways, tunnel).use { connection ->
                        connection.updateNetwork(HubNetwork("network"))
                        connection.awaitOnline()
                        assertNull(connection.tunnel.value)
                        assertEquals(hub.url, endpointKey(connection.route.value!!.endpoint.url))
                        assertTrue(proxy.asked.isEmpty())
                    }
                }
            }
        }
    }

    @Test fun alwaysGoesThroughTheProxyEvenWhenTheAddressAnswers() = runBlocking<Unit> {
        LoopbackHub(hubId = "hub-a", version = "1").use { hub ->
            FakeSocks5().use { proxy ->
                val tunnel = HubTunnel("hub-a", hub.paired, proxy = LocalProxyRoute(port = proxy.port, always = true))
                HubWays(identity = { identity }).use { ways ->
                    connection(hub.paired, ways, tunnel).use { connection ->
                        connection.updateNetwork(HubNetwork("network"))
                        connection.awaitOnline()
                        assertEquals(TunnelKind.Proxy, connection.tunnel.value?.kind)
                        assertTrue(proxy.carried.get() > 0)
                    }
                }
            }
        }
    }

    @Test fun throughTheTunnelTheAddressIsAskedAgainAndTakesOverOnceItAnswers() = runBlocking<Unit> {
        LoopbackHub(hubId = "hub-a", healthy = false, version = "1").use { direct ->
            LoopbackHub(hubId = "hub-a", version = "1").use { behind ->
                FakeSocks5().use { proxy ->
                    val tunnel = HubTunnel("hub-a", direct.paired,
                        proxy = LocalProxyRoute(port = proxy.port, targetHost = "127.0.0.1", targetPort = behind.port))
                    HubWays(identity = { identity }).use { ways ->
                        connection(direct.paired, ways, tunnel, recheckMillis = 200).use { connection ->
                            connection.updateNetwork(HubNetwork("network"))
                            connection.awaitOnline()
                            assertEquals(TunnelKind.Proxy, connection.tunnel.value?.kind)
                            direct.healthy = true
                            eventually(10_000) { connection.tunnel.value == null && connection.route.value?.endpoint?.url?.let(::endpointKey) == direct.url }
                            eventually { ways.states.value["hub-a" to TunnelKind.Proxy] == null }
                        }
                    }
                }
            }
        }
    }

    @Test fun aFallbackSavedWhileTheAddressAnswersKeepsTheSocketAndAlwaysTakesOver() = runBlocking<Unit> {
        LoopbackHub(hubId = "hub-a", version = "1").use { hub ->
            FakeSocks5().use { proxy ->
                HubWays(identity = { identity }).use { ways ->
                    connection(hub.paired, ways).use { connection ->
                        connection.updateNetwork(HubNetwork("network"))
                        connection.awaitOnline()
                        val sockets = hub.sockets.get()
                        val fallback = HubTunnel("hub-a", hub.paired, proxy = LocalProxyRoute(port = proxy.port))
                        connection.updateTunnels(listOf(fallback))
                        delay(500)
                        assertEquals(sockets, hub.sockets.get())
                        assertTrue(proxy.asked.isEmpty())
                        connection.updateTunnels(listOf(HubTunnel("hub-a", hub.paired, proxy = LocalProxyRoute(port = proxy.port, always = true))))
                        eventually(10_000) { connection.tunnel.value?.kind == TunnelKind.Proxy && connection.state.value == ConnectionState.Online }
                    }
                }
            }
        }
    }

    @Test fun anSshTunnelCarriesTheConnectionAndItsSocket() = runBlocking<Unit> {
        LoopbackHub(hubId = "hub-a").use { hub ->
            TestSshServer(identity).use { server ->
                val address = "http://127.0.0.1:${closedPort()}/"
                val tunnel = HubTunnel("hub-a", address, ssh = server.route(hub.port))
                HubWays(identity = { identity }, sshTimings = SshTimings(5000, 5000, 5000)).use { ways ->
                    assertEquals(server.fingerprint, ways.verifySsh(server.route(hub.port)).fingerprint)
                    connection(address, ways, tunnel).use { connection ->
                        connection.updateNetwork(HubNetwork("network"))
                        connection.awaitOnline()
                        assertEquals(TunnelKind.Ssh, connection.tunnel.value?.kind)
                        assertTrue(hub.opened.get() > 0)
                        assertEquals(TunnelState.Up, ways.states.value["hub-a" to TunnelKind.Ssh])
                    }
                }
            }
        }
    }

    @Test fun aChangedServerKeyIsNotDialledAgainUntilTheUserActs() = runBlocking<Unit> {
        LoopbackHub(hubId = "hub-a").use { hub ->
            TestSshServer(identity).use { server ->
                val address = "http://127.0.0.1:${closedPort()}/"
                val tunnel = HubTunnel("hub-a", address, ssh = server.route(hub.port, hostKey = "SHA256:old"))
                HubWays(identity = { identity }, sshTimings = SshTimings(5000, 5000, 5000)).use { ways ->
                    connection(address, ways, tunnel).use { connection ->
                        connection.updateNetwork(HubNetwork("network"))
                        eventually(10_000) { ways.states.value["hub-a" to TunnelKind.Ssh] is TunnelState.Down }
                        val down = ways.states.value["hub-a" to TunnelKind.Ssh] as TunnelState.Down
                        assertTrue(down.problem is TunnelProblem.HostKeyChanged)
                        assertEquals(0, server.logins.get())
                        val dialled = server.connections.get()
                        delay(3_000)
                        assertEquals(dialled, server.connections.get())
                        assertTrue(connection.state.value != ConnectionState.Online)
                    }
                }
            }
        }
    }

    @Test fun aRefusedKeyIsDialledAgainOnceTheSettingsAreSaved() = runBlocking<Unit> {
        LoopbackHub(hubId = "hub-a").use { hub ->
            TestSshServer(identity).use { server ->
                server.accepting = false
                val address = "http://127.0.0.1:${closedPort()}/"
                val tunnel = HubTunnel("hub-a", address, ssh = server.route(hub.port))
                HubWays(identity = { identity }, sshTimings = SshTimings(5000, 5000, 5000)).use { ways ->
                    connection(address, ways, tunnel).use { connection ->
                        connection.updateNetwork(HubNetwork("network"))
                        eventually(10_000) { (ways.states.value["hub-a" to TunnelKind.Ssh] as? TunnelState.Down)?.problem == TunnelProblem.NotAuthorized }
                        server.accepting = true
                        ways.unblock("hub-a", TunnelKind.Ssh)
                        assertNull(ways.states.value["hub-a" to TunnelKind.Ssh])
                        connection.reconnect()
                        connection.awaitOnline()
                        assertEquals(TunnelKind.Ssh, connection.tunnel.value?.kind)
                    }
                }
            }
        }
    }
}
