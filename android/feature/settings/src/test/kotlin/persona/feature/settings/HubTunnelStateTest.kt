package persona.feature.settings

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.HubTunnelInfo
import persona.core.contracts.api.LocalProxyRoute
import persona.core.contracts.api.SshHostKey
import persona.core.contracts.api.SshRoute
import persona.core.contracts.api.TunnelKind
import persona.core.contracts.api.TunnelProblem

/** Settings › Hubs › SSH tunnel and Local proxy (iOS `SSHTunnelView`, `LocalProxyView`): what is tested, asked and saved. */
class HubTunnelStateTest {
    private val hub = HubTunnelInfo("hub-a", "Home", "https://hub.example.com/")
    private val saved = SshRoute("ssh.example.com", 22, "me", hostKey = TUNNEL_FINGERPRINT, hostKeyAlgorithm = "ED25519")

    @Test fun emptyFieldsTakeTheDefaultsAndAMissingOneKeepsTheButtonOff() {
        val form = SshTunnelForm(host = " ssh.example.com ", user = "me")
        assertEquals(SshRoute("ssh.example.com", 22, "me", "127.0.0.1", 18660), form.route)
        assertNull(form.copy(user = " ").route)
        assertNull(form.copy(port = "70000").route)
        assertNull(form.copy(targetPort = "x").route)
        assertFalse(form.copy(testing = true).canTest)
    }

    @Test fun aKeyIsConfirmedOnceThenSavedWithTheRoute() = runBlocking {
        val tunnels = FakeHubTunnels(hub)
        val model = SshTunnelModel(tunnels, "hub-a", null)
        model.edit(host = "ssh.example.com", user = "me")
        assertFalse(model.testAndSave())
        val asked = model.state.value.question as TunnelProblem.HostKeyUnknown
        assertEquals(SshHostKey(TUNNEL_FINGERPRINT, "ED25519"), asked.key)
        assertTrue(model.testAndSave(trust = asked.key))
        assertNull(model.state.value.question)
        assertEquals(TUNNEL_FINGERPRINT, tunnels.hubs.value.single().ssh?.hostKey)
        assertEquals("ED25519", model.state.value.saved?.hostKeyAlgorithm)
    }

    @Test fun defaultsStayEmptyAndTheFingerprintFollowsTheServerTyped() {
        val form = SshTunnelForm.of(saved)
        assertEquals(listOf("", "", ""), listOf(form.port, form.targetHost, form.targetPort))
        assertEquals(saved, form.route)
        assertEquals(TUNNEL_FINGERPRINT, form.copy(user = "").pinned?.hostKey)
        assertNull(form.copy(port = "2222").pinned)
        assertEquals("2222", SshTunnelForm.of(saved.copy(port = 2222)).port)
    }

    @Test fun anotherServerNeedsItsKeyConfirmedAgain() {
        val form = SshTunnelForm.of(saved)
        assertEquals(TUNNEL_FINGERPRINT, form.route?.hostKey)
        assertEquals(TUNNEL_FINGERPRINT, form.copy(user = "other").route?.hostKey)
        assertNull(form.copy(host = "bastion.example.com").route?.hostKey)
        assertNull(form.copy(port = "2222").route?.hostKey)
    }

    @Test fun alwaysAloneIsSavedAtOnceButNotWithOtherChanges() = runBlocking {
        val tunnels = FakeHubTunnels(hub.copy(ssh = saved))
        val model = SshTunnelModel(tunnels, "hub-a", saved)
        assertTrue(model.setAlways(true))
        assertEquals(true, tunnels.hubs.value.single().ssh?.always)
        model.edit(user = "someone")
        assertFalse(model.setAlways(false))
        assertEquals(listOf("saveSsh me@ssh.example.com key=$TUNNEL_FINGERPRINT always=true"), tunnels.calls)
    }

    @Test fun aRefusalSaysWhatToCheckAndSavesNothing() = runBlocking {
        val tunnels = FakeHubTunnels(hub).apply { sshProblem = TunnelProblem.NotAuthorized }
        val model = SshTunnelModel(tunnels, "hub-a", null)
        model.edit(host = "ssh.example.com", user = "me:hub")
        assertFalse(model.testAndSave(trust = SshHostKey(TUNNEL_FINGERPRINT, "ED25519")))
        assertEquals(ResText.of(R.string.settings_tunnel_problem_not_authorized, "me:hub"), model.state.value.problem)
        assertTrue(tunnels.calls.none { it.startsWith("save") })
        model.edit(user = "me")
        assertNull(model.state.value.problem)
    }

    @Test fun aKeyThatCannotBeReadIsSaid() = runBlocking {
        val model = SshTunnelModel(FakeHubTunnels(hub).apply { keyUnavailable = true }, "hub-a", null)
        model.loadKey()
        assertTrue(model.state.value.keyUnavailable)
        assertNull(model.state.value.authorizedKeysLine)
    }

    @Test fun theAuthorizedKeysLineFollowsTheTargetAsTyped() = runBlocking {
        val model = SshTunnelModel(FakeHubTunnels(hub), "hub-a", null)
        model.loadKey()
        assertTrue(model.state.value.authorizedKeysLine!!.startsWith("restrict,port-forwarding,permitopen=\"127.0.0.1:18660\" ssh-ed25519 "))
        model.edit(targetHost = "2001:db8::2", targetPort = "8080")
        assertTrue(model.state.value.authorizedKeysLine!!.startsWith("restrict,port-forwarding,permitopen=\"[2001:db8::2]:8080\" "))
    }

    @Test fun deletingForgetsTheTunnel() = runBlocking {
        val tunnels = FakeHubTunnels(hub.copy(ssh = saved))
        val model = SshTunnelModel(tunnels, "hub-a", saved)
        assertTrue(model.delete())
        assertNull(tunnels.hubs.value.single().ssh)
        assertNull(model.state.value.saved)
    }

    @Test fun aSaveThatFailsSaysSo() = runBlocking {
        val model = SshTunnelModel(FakeHubTunnels(hub).apply { saveFails = true }, "hub-a", null)
        model.edit(host = "ssh.example.com", user = "me")
        assertFalse(model.testAndSave(trust = SshHostKey(TUNNEL_FINGERPRINT, "ED25519")))
        assertEquals(ResText.of(R.string.settings_tunnel_save_failed), model.state.value.problem)
        assertFalse(model.state.value.testing)
    }

    @Test fun theProxyNeedsAPortAndTakesThisDeviceByDefault() {
        val form = LocalProxyForm(port = "7890")
        assertEquals(LocalProxyRoute("127.0.0.1", 7890), form.route)
        assertNull(LocalProxyForm().route)
        assertNull(form.copy(targetPort = "0").route)
        assertEquals(LocalProxyRoute("127.0.0.1", 7890, targetHost = "192.0.2.10", targetPort = 18660),
            form.copy(targetHost = " 192.0.2.10 ", targetPort = "18660").route)
        assertFalse(form.remote)
        assertFalse(form.copy(host = "localhost").remote)
        assertTrue(form.copy(host = "192.0.2.20").remote)
        assertFalse(form.copy(host = "127.0.0.2").remote)
        assertTrue(form.copy(host = "127.example.com").remote)
        assertEquals("", LocalProxyForm.of(LocalProxyRoute(port = 7890)).host)
        assertFalse("secret" in LocalProxyForm(port = "7890", username = "me", password = "secret").toString())
    }

    @Test fun theProxyIsSavedOnlyOnceTheHubAnswersThroughIt() = runBlocking {
        val tunnels = FakeHubTunnels(hub).apply { proxyProblem = TunnelProblem.ConnectFailed(5) }
        val model = LocalProxyModel(tunnels, "hub-a", hub.hubAddress, null)
        model.edit(port = "7890")
        assertFalse(model.testAndSave())
        assertEquals(ResText.of(R.string.settings_tunnel_problem_connect_failed, "hub.example.com:18660", "connection refused, 5"), model.state.value.problem)
        tunnels.proxyProblem = null
        assertTrue(model.testAndSave())
        assertEquals(listOf("verifyProxy 127.0.0.1:7890", "verifyProxy 127.0.0.1:7890", "saveProxy 127.0.0.1:7890 always=false"), tunnels.calls)
        assertTrue(model.setAlways(true))
        assertEquals(true, tunnels.hubs.value.single().proxy?.always)
    }

    @Test fun theHubsRowsSayWhetherEachTunnelIsSetInUseAlwaysOrAFallback() {
        assertEquals(R.string.settings_tunnel_not_set, tunnelRowValue(hub, TunnelKind.Ssh))
        assertEquals(R.string.settings_tunnel_not_set, tunnelRowValue(hub.copy(ssh = saved.copy(hostKey = null)), TunnelKind.Ssh))
        assertEquals(R.string.settings_tunnel_fallback, tunnelRowValue(hub.copy(ssh = saved), TunnelKind.Ssh))
        assertEquals(R.string.settings_tunnel_always, tunnelRowValue(hub.copy(ssh = saved.copy(always = true)), TunnelKind.Ssh))
        assertEquals(R.string.settings_hubs_in_use, tunnelRowValue(hub.copy(ssh = saved, routed = TunnelKind.Ssh), TunnelKind.Ssh))
        assertEquals(R.string.settings_tunnel_fallback, tunnelRowValue(hub.copy(proxy = LocalProxyRoute(port = 1)), TunnelKind.Proxy))
    }

    @Test fun theStatusSaysWhetherTheConnectionGoesThroughTheTunnel() {
        assertEquals(R.string.settings_tunnel_ssh_connected, tunnelStatus(hub.copy(routed = TunnelKind.Ssh, online = true), TunnelKind.Ssh))
        assertEquals(R.string.settings_tunnel_ssh_connecting, tunnelStatus(hub.copy(routed = TunnelKind.Ssh), TunnelKind.Ssh))
        assertEquals(R.string.settings_tunnel_proxy_connected, tunnelStatus(hub.copy(routed = TunnelKind.Proxy, online = true), TunnelKind.Proxy))
        assertEquals(R.string.settings_tunnel_not_in_use, tunnelStatus(hub.copy(online = true), TunnelKind.Proxy))
        assertEquals(R.string.settings_tunnel_not_in_use_proxy, tunnelStatus(hub.copy(routed = TunnelKind.Proxy, online = true), TunnelKind.Ssh))
        assertEquals(R.string.settings_tunnel_not_in_use_ssh, tunnelStatus(hub.copy(routed = TunnelKind.Ssh, online = true), TunnelKind.Proxy))
        assertEquals(R.string.settings_tunnel_not_connected, tunnelStatus(hub, TunnelKind.Ssh))
    }
}
