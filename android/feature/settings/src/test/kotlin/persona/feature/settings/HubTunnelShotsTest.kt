package persona.feature.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.*
import persona.core.designsystem.*

/**
 * Settings › Hubs with each paired hub's SSH tunnel and local proxy rows, and the two pages (empty, saved and in use, a
 * failure), drawn on the JVM with Robolectric's native graphics in every style, light and dark, and in English and
 * Japanese; written as PNGs to `build/tunnel-shots/` (or `-Dpersona.shots=<dir>`) to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class HubTunnelShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/tunnel-shots").also { it.mkdirs() }

    private val ssh = SshRoute("ssh.example.com", 22, "me", hostKey = TUNNEL_FINGERPRINT, hostKeyAlgorithm = "ED25519")
    private val proxy = LocalProxyRoute(port = 7890)
    private val home = HubTunnelInfo("h1", "家里的服务器", "https://hub.example.com/", ssh = ssh, sshState = TunnelState.Up,
        routed = TunnelKind.Ssh, online = true)
    private val office = HubTunnelInfo("h3", "办公室", "http://192.0.2.10:18660/", proxy = proxy.copy(always = true),
        proxyState = TunnelState.Down(TunnelProblem.ProxyUnreachable("Connection refused")))
    private val groups = object : HubGroups {
        override val groups = MutableStateFlow(listOf(
            HubGroupInfo("home", "家里", null, selected = true, state = ConnectionState.Online, hubs = listOf(
                HubMemberInfo("h1", "家里的服务器", paired = true, connected = true, online = true),
                HubMemberInfo("h2", "NAS", paired = false, online = false),
            )),
            HubGroupInfo("office", "公司", "o", selected = false, state = ConnectionState.Connecting, hubs = listOf(
                HubMemberInfo("h3", "办公室", paired = true, online = true),
            )),
        ))
        override suspend fun select(groupId: String) = Unit
        override suspend fun rename(groupId: String, name: String) = Unit
        override suspend fun removeGroup(groupId: String) = Unit
        override suspend fun removeHub(groupId: String, hubId: String) = Unit
    }

    private enum class Shot { Hubs, SshEmpty, SshSaved, SshProblem, ProxyEmpty, ProxySaved, ProxyRemote }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h2400dp-xhdpi")
    @Test fun simplifiedChinese() = shots("zh", AppStyle.entries.flatMap { listOf(it to false, it to true) })

    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
    @Test fun english() = shots("en", listOf(AppStyle.Glass to false))

    @Config(sdk = [35], qualifiers = "ja-w400dp-h2400dp-xhdpi")
    @Test fun japanese() = shots("ja", listOf(AppStyle.Glass to false))

    private fun shots(language: String, variants: List<Pair<AppStyle, Boolean>>) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shot by mutableStateOf(Shot.Hubs)
        var tunnels by mutableStateOf(FakeHubTunnels())
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(2400.dp).background(PersonaTheme.palette.background)) {
                    key(shot, style, dark, tunnels) {
                        when (shot) {
                            Shot.Hubs -> HubsScreen(groups, onAdd = {}, onBack = {}, tunnels = tunnels)
                            Shot.SshEmpty, Shot.SshSaved, Shot.SshProblem -> HubTunnelPage(tunnels, "h1", TunnelKind.Ssh, onBack = {})
                            Shot.ProxyEmpty, Shot.ProxySaved, Shot.ProxyRemote -> HubTunnelPage(tunnels, "h3", TunnelKind.Proxy, onBack = {})
                        }
                    }
                }
            }
        }
        for ((s, d) in variants) for (next in Shot.entries) {
            tunnels = when (next) {
                Shot.SshEmpty, Shot.ProxyEmpty -> FakeHubTunnels(home.copy(ssh = null, routed = null), office.copy(proxy = null))
                Shot.SshProblem -> FakeHubTunnels(home.copy(ssh = null, routed = null), office).apply { sshProblem = TunnelProblem.NotAuthorized }
                else -> FakeHubTunnels(home, office)
            }
            style = s; dark = d; shot = next
            rule.waitForIdle()
            when (next) {
                Shot.SshProblem -> {
                    typeSsh()
                    rule.onNode(hasText(text(R.string.settings_tunnel_test_save)) and hasClickAction()).performClick()
                    rule.waitForIdle()
                    rule.onNodeWithText(text(R.string.settings_tunnel_problem_not_authorized, "me")).assertExists()
                }
                Shot.ProxyRemote -> {
                    rule.onAllNodes(hasSetTextAction())[0].performTextReplacement("192.0.2.20")
                    rule.waitForIdle()
                    rule.onNodeWithText(text(R.string.settings_tunnel_not_local)).assertExists()
                }
                else -> Unit
            }
            capture("tunnel-$language-${next.name.lowercase()}-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }

    /** Unknown key: the fingerprint is asked about first, and trusting it saves. */
    @Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
    @Test fun theFirstTestAsksAboutTheFingerprintThenSaves() {
        val tunnels = FakeHubTunnels(home.copy(ssh = null, routed = null))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(2400.dp)) { HubTunnelPage(tunnels, "h1", TunnelKind.Ssh, onBack = {}) }
            }
        }
        rule.waitForIdle()
        typeSsh()
        rule.onNode(hasText(text(R.string.settings_tunnel_test_save)) and hasClickAction()).performClick()
        rule.waitForIdle()
        rule.onNodeWithText(text(R.string.settings_tunnel_verify_title)).assertExists()
        rule.onNode(hasText(text(R.string.settings_tunnel_trust_save)) and hasClickAction()).performClick()
        rule.waitForIdle()
        assertEquals(TUNNEL_FINGERPRINT, tunnels.hubs.value.single().ssh?.hostKey)
        rule.onNodeWithText(text(R.string.settings_tunnel_verified_algorithm, "ED25519")).assertExists()
    }

    /** Every problem is put into words in every language, with all its arguments. */
    @Config(sdk = [35], qualifiers = "ja")
    @Test fun everyProblemHasItsWords() {
        val key = SshHostKey(TUNNEL_FINGERPRINT, "ED25519")
        val problems = listOf(TunnelProblem.SshUnreachable("refused"), TunnelProblem.HostKeyUnknown(key), TunnelProblem.HostKeyChanged("SHA256:old", key),
            TunnelProblem.NotAuthorized, TunnelProblem.Handshake("x"), TunnelProblem.TargetRefused("connect failed"), TunnelProblem.SshTimedOut,
            TunnelProblem.KeyUnavailable, TunnelProblem.Stopped, TunnelProblem.ProxyUnreachable("refused"), TunnelProblem.NotSocks5,
            TunnelProblem.NoAcceptableMethod(true), TunnelProblem.NoAcceptableMethod(false), TunnelProblem.LoginRejected, TunnelProblem.ConnectFailed(4),
            TunnelProblem.Malformed, TunnelProblem.ProxyClosed, TunnelProblem.ProxyTimedOut, TunnelProblem.LoginTooLong, TunnelProblem.BadHost,
            TunnelProblem.NoAnswer("192.0.2.10:18660"), TunnelProblem.NotAHub("192.0.2.10:18660"), TunnelProblem.OtherHub("192.0.2.10:18660"),
            TunnelProblem.SeenAsInternet("192.0.2.10:18660"))
        val res = rule.activity.resources
        for (problem in problems) {
            val words = tunnelProblemText(problem, ssh = ssh, proxy = proxy, hubAddress = "http://192.0.2.10:18660/").resolve(res)
            assertTrue("$problem: $words", words.isNotBlank() && "%" !in words)
        }
    }

    /** The SSH page's fields in order: host, port, username, target address, target port. */
    private fun typeSsh() {
        rule.onAllNodes(hasSetTextAction())[0].performTextReplacement("ssh.example.com")
        rule.onAllNodes(hasSetTextAction())[2].performTextReplacement("me")
        rule.waitForIdle()
    }

    private fun text(id: Int, vararg args: Any) = rule.activity.getString(id, *args)

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}
