package persona.core.hub.tunnel

import com.google.crypto.tink.subtle.Ed25519Verify
import java.net.URI
import java.util.Base64
import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.Request
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import persona.core.contracts.api.SshHostKey
import persona.core.contracts.api.TunnelException
import persona.core.contracts.api.TunnelProblem
import persona.core.hub.LoopbackHub
import persona.core.hub.closedPort

/** docs/operations/network.md "Connecting to hub through SSH", against a real SSH server. */
class SshTunnelTest {
    private val identity = SshIdentity(SshIdentity.generateSeed(), "persona-test")
    private val timings = SshTimings(connectMillis = 5000, openMillis = 5000, aliveMillis = 5000)
    private val LoopbackHub.port get() = URI(paired).port

    private suspend fun problem(block: suspend () -> Unit): TunnelProblem {
        try { block() } catch (error: TunnelException) { return error.problem }
        fail("No TunnelException")
        throw AssertionError()
    }

    @Test fun theFirstConnectionShowsTheServersKeyAndGoesNoFurther() = runBlocking {
        TestSshServer(identity).use { server ->
            val asked = problem { SshTunnel(server.route(1, hostKey = null), identity, timings).verify() }
            assertEquals(TunnelProblem.HostKeyUnknown(SshHostKey(server.fingerprint, "ED25519")), asked)
            assertEquals(0, server.logins.get())
        }
    }

    @Test fun thePinnedKeyLogsInAndOpensTheHubsAddress() = runBlocking {
        LoopbackHub(hubId = "hub-a").use { hub ->
            TestSshServer(identity).use { server ->
                val key = SshTunnel(server.route(hub.port), identity, timings).verify()
                assertEquals(SshHostKey(server.fingerprint, "ED25519"), key)
                assertTrue(server.logins.get() > 0)
            }
        }
    }

    @Test fun anEcdsaServerKeyIsShownAsOpenSshDoes() = runBlocking {
        LoopbackHub(hubId = "hub-a").use { hub ->
            TestSshServer(identity, hostKey = TestSshServer.ecdsa()).use { server ->
                assertEquals(TunnelProblem.HostKeyUnknown(SshHostKey(server.fingerprint, "ECDSA")),
                    problem { SshTunnel(server.route(hub.port, hostKey = null), identity, timings).verify() })
                assertEquals("ECDSA", SshTunnel(server.route(hub.port), identity, timings).verify().algorithm)
            }
        }
    }

    @Test fun anotherKeyThanThePinnedOneIsRefused() = runBlocking {
        TestSshServer(identity).use { server ->
            assertEquals(TunnelProblem.HostKeyChanged("SHA256:old", SshHostKey(server.fingerprint, "ED25519")),
                problem { SshTunnel(server.route(1, hostKey = "SHA256:old"), identity, timings).verify() })
            assertEquals(0, server.logins.get())
        }
    }

    @Test fun aKeyTheServerDoesNotKnowIsNotAuthorized() = runBlocking {
        TestSshServer(SshIdentity(SshIdentity.generateSeed(), "someone-else")).use { server ->
            assertEquals(TunnelProblem.NotAuthorized, problem { SshTunnel(server.route(1), identity, timings).verify() })
            assertEquals(TunnelProblem.NotAuthorized, problem { SshTunnel(server.route(1, user = "root"), identity, timings).verify() })
        }
    }

    @Test fun aForwardTheServerWillNotOpenSaysWhy() = runBlocking {
        TestSshServer(identity, forwarding = false).use { server ->
            assertEquals(TunnelProblem.TargetRefused("administratively prohibited"),
                problem { SshTunnel(server.route(1), identity, timings).verify() })
        }
        TestSshServer(identity).use { server ->
            assertTrue(problem { SshTunnel(server.route(closedPort()), identity, timings).verify() } is TunnelProblem.TargetRefused)
        }
    }

    @Test fun noServerIsUnreachable() = runBlocking {
        val route = persona.core.contracts.api.SshRoute("127.0.0.1", closedPort(), "persona", hostKey = "SHA256:x")
        assertTrue(problem { SshTunnel(route, identity, timings).verify() } is TunnelProblem.SshUnreachable)
    }

    @Test fun theTunnelCarriesTheHubsTrafficOnItsLoopbackPort() = runBlocking {
        LoopbackHub(hubId = "hub-a").use { hub ->
            TestSshServer(identity).use { server ->
                SshTunnel(server.route(hub.port), identity, timings).use { tunnel ->
                    val port = tunnel.start()
                    assertTrue(tunnel.alive)
                    val client = OkHttpClient.Builder().proxy(java.net.Proxy.NO_PROXY).build()
                    repeat(2) {
                        client.newCall(Request.Builder().url("http://127.0.0.1:$port/api/v1/health").build()).execute().use { response ->
                            assertEquals(200, response.code)
                            assertTrue(response.body!!.string().contains("hub-a"))
                        }
                    }
                    client.connectionPool.evictAll()
                    server.dropSessions()
                    eventually { !tunnel.alive }
                }
            }
        }
    }

    @Test fun theDeviceKeyIsAnOpenSshEd25519KeyThatSigns() {
        val line = identity.deviceKey.publicKey.split(' ')
        assertEquals(listOf("ssh-ed25519", "persona-test"), listOf(line[0], line[2]))
        val parts = SshWire.strings(Base64.getDecoder().decode(line[1]))!!
        assertEquals("ssh-ed25519", String(parts[0]))
        val signature = SshWire.signatureBody(identity.jsch.getSignature("data".toByteArray()), ED25519)!!
        Ed25519Verify(parts[1]).verify(signature, "data".toByteArray())
        assertEquals("persona-Pixel-9-Pro", SshIdentity.comment("  Pixel 9\tPro "))
        assertEquals("persona-android", SshIdentity.comment(" "))
        assertEquals("SshIdentity(<redacted>)", identity.toString())
    }

    @Test fun theAuthorizedKeysLineOnlyForwardsToTheTarget() {
        val key = identity.deviceKey
        assertEquals("restrict,port-forwarding,permitopen=\"127.0.0.1:18660\" ${key.publicKey}", key.authorizedKeysLine("127.0.0.1", 18660))
        assertEquals("restrict,port-forwarding,permitopen=\"[2001:db8::2]:18660\" ${key.publicKey}", key.authorizedKeysLine("2001:db8::2", 18660))
    }

    private suspend fun eventually(condition: () -> Boolean) = persona.core.hub.eventually(5000, condition)
}
