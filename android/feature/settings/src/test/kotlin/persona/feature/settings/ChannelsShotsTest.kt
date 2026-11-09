package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsSelected
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.LocalDetailKey
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute
import kotlinx.serialization.json.JsonObject
import java.io.File
import java.time.Instant

/**
 * Settings › Channels drawn on the JVM with Robolectric's native graphics (no emulator), in every style, light and dark:
 * the real settings home, the list, WeChat's page, a QR code to scan and a pairing code to send (binding a
 * step at a time). The data is the mock hub's MOCK_CHANNELS. Each is checked to draw something and written as a PNG to
 * `build/channel-shots/` (or `-Dpersona.shots=<dir>`) to look at (docs/android/android.md "Channels").
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2100dp-xhdpi")
class ChannelsShotsTest {
    @get:Rule val rule = createComposeRule()

    private val minutesAgo = { m: Long -> Instant.now().minusSeconds(m * 60).toString() }
    private val textCaps = ChannelCapabilities(imagesIn = true, voiceIn = true, format = "plain", maxText = 3500, asks = "text", push = true, typing = true)
    private val extensionFeatures = listOf(
        ChannelFeature("asks", "提问和批准", "它在驱动的对话（发到的对话、在等回答的）要你回答或批准时，发到这里", true),
        ChannelFeature("all_asks", "所有 Agent 的提问", "各台电脑上所有 Agent 的提问和批准都发到这里", false),
        ChannelFeature("pushes", "推送也发到这里", "通知推到手机的同时发一份；定时任务那段对话的回答也发", false),
        ChannelFeature("sync", "同步主对话", "主对话里不是从这里发起的一轮结束时，把回答发过来", false),
    )
    private val wechat = HubChannel("wechat", "微信", "im", "plugin", extension = "wechat", installed = true, enabled = true, state = "connected",
        account = ChannelAccount(name = "微信用户 a1b2c3"), bind = "qr", lastEventAt = minutesAgo(3),
        description = "在个人微信里私聊机器人：扫码绑定，消息进主对话，回答以纯文字发回", capabilities = textCaps,
        target = ChannelTarget.Main, features = extensionFeatures)
    private val feishu = HubChannel("feishu", "飞书机器人", "im", "builtin", installed = true, enabled = true, state = "connected",
        account = ChannelAccount(name = "示例用户"), bind = "fields", lastEventAt = minutesAgo(12),
        description = "私聊飞书自建应用的机器人：消息进主对话，回答、进度和提问都是卡片",
        capabilities = ChannelCapabilities(imagesIn = true, filesIn = true, voiceIn = true, format = "markdown", maxText = 6000, asks = "buttons",
            push = true, progress = true, threads = true), target = ChannelTarget.Main,
        features = listOf(ChannelFeature("main", "和 Persona 对话", "消息发进主对话；/new 开新对话、/list 选最近的对话、/main 回主对话", true)))
    private val channels = listOf(
        HubChannel("app", "Persona", "app", "builtin", installed = true, enabled = true, state = "connected", bind = "none", readOnly = true,
            description = "Persona 自己的各端：在哪一端说的话都进同一段对话，提问和批准在卡片上原地作答",
            surfaces = listOf("iPhone", "Mac", "Apple Watch", "Android", "网页", "Siri"),
            capabilities = ChannelCapabilities(imagesIn = true, filesIn = true, voiceIn = true, format = "markdown", asks = "buttons", push = true, typing = true, progress = true)),
        feishu, wechat,
        HubChannel("lark_agent", "飞书 /agent", "mention", "builtin", installed = true, enabled = false, state = "disabled", bind = "config", readOnly = true,
            capabilities = ChannelCapabilities(format = "markdown", asks = "none"),
            description = "在飞书任何会话、文档评论里自己发「/agent …」，Agent 处理完以你的名义回复（hub.json 的 lark_agent）"),
        HubChannel("examplechat", "Example Chat", "im", "plugin", extension = "example-chat", installed = true, enabled = true, state = "not_connected",
            error = "还没绑定：填机器人的令牌", bind = "fields", description = "示例聊天软件的机器人（第三方扩展）",
            capabilities = textCaps.copy(format = "markdown", maxText = 4000), target = ChannelTarget.Main, features = extensionFeatures),
        HubChannel("examplebroken", "Example Team", "im", "plugin", extension = "example-team", installed = true, enabled = true, state = "error",
            error = "令牌失效了，重新绑定", bind = "fields", account = ChannelAccount(name = "示例团队"), description = "示例团队聊天的机器人",
            capabilities = textCaps.copy(format = "markdown"), target = ChannelTarget.Main, features = extensionFeatures),
        HubChannel("examplemarket", "Example Rooms", "im", "market", extension = "example-rooms", market = "example", state = "not_installed",
            bind = "none", description = "在示例聊天室里和 Persona 对话"),
    )

    // A made-up 21 × 21 grid with the three finder squares, enough to look like a code (it scans as nothing).
    private val qrRows = (0 until 21).map { y ->
        (0 until 21).joinToString("") { x ->
            fun finder(ox: Int, oy: Int): Boolean? {
                val dx = x - ox; val dy = y - oy
                if (dx !in 0..6 || dy !in 0..6) return null
                return dx == 0 || dx == 6 || dy == 0 || dy == 6 || (dx in 2..4 && dy in 2..4)
            }
            val f = finder(0, 0) ?: finder(14, 0) ?: finder(0, 14)
            if (f ?: ((x * 7 + y * 13 + x * y) % 5 < 2)) "1" else "0"
        }
    }

    /** MOCK_CHANNELS, with [unbound] ones not bound yet so that binding them shows its steps. */
    private inner class FakeChannels(private val unbound: Set<String> = emptySet()) : ChannelManagement {
        override val available = MutableStateFlow(true)
        override val conversations = flowOf(emptyList<persona.core.contracts.Session>())
        private fun of(id: String) = channels.first { it.id == id }.let {
            if (id in unbound) it.copy(state = "not_connected", account = null, lastEventAt = null) else it
        }
        override suspend fun channels() = channels
        override suspend fun channel(id: String) = ChannelDetail(of(id), ChannelWaiting(answers = if (id == "wechat") 1 else 0),
            conversations = if (id == "wechat") listOf(ChannelConversation("hub", "s1", "周末去哪儿", minutesAgo(3))) else emptyList(),
            settingsUrl = if (id == "wechat") "settings/plugins/wechat" else null)
        override suspend fun update(id: String, change: ChannelChange) = channel(id)
        override suspend fun bind(id: String, step: String?, values: Map<String, String>?): BindPrompt {
            // Asked again while the code is on screen: never answers, so the step stays put for the picture.
            if (step != null) awaitCancellation()
            return if (id == "wechat") BindPrompt("qr", kind = "qr", url = "https://example.com/qr", interval = 1, qr = BindQrCode(qrRows.size, qrRows),
                hint = "用微信扫一扫这个二维码，在手机上确认后这里会自动继续。在同一部手机上：先保存二维码，再在微信里扫一扫、从相册选它。")
            else BindPrompt("pair", kind = "pair_code", userCode = "482913", interval = 3, hint = "在飞书里私聊机器人，把这 6 位数字发给它（10 分钟内有效）")
        }
        override suspend fun unbind(id: String) = channel(id)
        override suspend fun test(id: String) {}
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/channel-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private enum class Shot { Settings, List, WeChat, Qr, PairCode }

    @Test fun drawsTheChannelPagesInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shot by mutableStateOf(Shot.Settings)
        // A new page (and a new model) for every picture: binding starts from its first step each time.
        var run by mutableIntStateOf(0)
        val services = settingsServices(FakeChannels())
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(if (shot == Shot.Settings || shot == Shot.WeChat) 2050.dp else 1500.dp)
                        .background(PersonaTheme.palette.background)) {
                        when (shot) {
                            Shot.Settings -> SettingsScreen(services, onOpen = {})
                            Shot.List -> ChannelsPage(FakeChannels(), onBack = {}, onChannel = {}, onMarketExtension = { _, _ -> })
                            Shot.WeChat -> ChannelPage(FakeChannels(), "wechat", onBack = {}, onOpen = {})
                            Shot.Qr -> ChannelPage(FakeChannels(setOf("wechat")), "wechat", onBack = {}, onOpen = {})
                            Shot.PairCode -> ChannelPage(FakeChannels(setOf("feishu")), "feishu", onBack = {}, onOpen = {})
                        }
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            for (next in Shot.entries) {
                shot = next; run++
                rule.waitForIdle()
                when (next) {
                    Shot.Settings -> assertSettingsEntryOrder()
                    Shot.Qr -> rule.onNode(hasText("扫码绑定") and hasClickAction()).performClick()
                    Shot.PairCode -> rule.onNode(hasText("绑定") and hasClickAction()).performClick()
                    else -> Unit
                }
                capture("channel-${next.name.lowercase()}-$suffix")
            }
        }
    }

    private fun assertSettingsEntryOrder() {
        // With a navigation host, the removed introduction row used to be present.
        rule.onAllNodesWithText("助手").assertCountEquals(0)
        rule.onAllNodesWithText("Persona").assertCountEquals(0)
        rule.onAllNodesWithText("认识 Persona").assertCountEquals(0)
        rule.onNode(hasText("连接器") and hasClickAction()).performScrollTo()
        rule.onAllNodesWithText("渠道").assertCountEquals(2) // Its own section header and its one clickable row.
        val computer = rule.onNode(hasText("Claude Code 设置") and hasClickAction()).fetchSemanticsNode().boundsInRoot
        val channelHeader = rule.onNode(hasText("渠道") and !hasClickAction()).fetchSemanticsNode().boundsInRoot
        val channelRow = rule.onNode(hasText("渠道") and hasClickAction()).fetchSemanticsNode().boundsInRoot
        val connectors = rule.onNode(hasText("连接器") and !hasClickAction()).fetchSemanticsNode().boundsInRoot
        assertTrue(computer.bottom <= channelHeader.top)
        assertTrue(channelHeader.bottom <= channelRow.top)
        assertTrue(channelRow.bottom <= connectors.top)
    }

    @Test fun channelEntryWorksWithoutExtensionsAndTracksCapabilityChanges() {
        val api = FakeChannels()
        val services = settingsServices(api, extensions = false)
        var opened: SettingsPage? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(2050.dp)) {
                    androidx.compose.runtime.CompositionLocalProvider(LocalDetailKey provides SettingsPageRoute(SettingsPage.Channels)) {
                        SettingsScreen(services, onOpen = { opened = it })
                    }
                }
            }
        }
        assertSettingsEntryOrder()
        rule.onAllNodesWithText("扩展").assertCountEquals(0)
        val row = rule.onNode(hasText("渠道") and hasClickAction())
        row.assertIsSelected().performClick()
        assertEquals(SettingsPage.Channels, opened)
        api.available.value = false
        rule.onAllNodesWithText("渠道").assertCountEquals(0)
        api.available.value = true
        rule.onNode(hasText("渠道") and hasClickAction()).performScrollTo().performClick()
        assertEquals(SettingsPage.Channels, opened)
    }

    @Test fun extensionsRemainIndependentWhenChannelsAreUnavailable() {
        val api = FakeChannels().also { it.available.value = false }
        val services = settingsServices(api)
        var opened: SettingsPage? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(2050.dp)) {
                    SettingsScreen(services, onOpen = { opened = it })
                }
            }
        }
        rule.onAllNodesWithText("渠道").assertCountEquals(0)
        rule.onAllNodesWithText("扩展").assertCountEquals(2)
        rule.onNode(hasText("扩展") and hasClickAction()).performScrollTo().performClick()
        assertEquals(SettingsPage.Extensions, opened)
    }

    @Test fun standaloneSettingsStillOpensChannelsAndDemoHidesThem() {
        val services = settingsServices(FakeChannels())
        var demo by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                key(demo) {
                    Box(Modifier.width(400.dp).height(2050.dp)) { SettingsScreen(services.copy(demo = demo)) }
                }
            }
        }
        rule.onNode(hasText("渠道") and hasClickAction()).performScrollTo().performClick()
        rule.onNodeWithText("和 Persona 对话的入口：", substring = true).assertExists()
        demo = true
        rule.onAllNodesWithText("渠道").assertCountEquals(0)
        rule.onAllNodesWithText("扩展").assertCountEquals(0)
    }

    /** Only the APIs actually read by the real settings home; no hub, Hilt application or network is involved. */
    private fun settingsServices(api: ChannelManagement, extensions: Boolean = true) = SettingsServices(
        pairing = object : PairingService {
            override val isPaired = MutableStateFlow(true)
            override suspend fun pair(request: PairingRequest) = error("not used")
            override suspend fun unpair() = error("not used")
        },
        connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = error("not used")
            override fun send(message: JsonObject) = false
        },
        sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(emptyList<Machine>())
            override val sessions = MutableStateFlow(emptyList<Session>())
            override suspend fun refresh() {}
        },
        updates = object : AppUpdates { override suspend fun check(): AppUpdate? = null },
        extensions = if (extensions) object : ExtensionApiStub() {
            override suspend fun extensions() = emptyList<HubExtension>()
        } else null,
        channels = api,
    )
}
