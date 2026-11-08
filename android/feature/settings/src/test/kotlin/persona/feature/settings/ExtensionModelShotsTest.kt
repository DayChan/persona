package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.*
import persona.core.designsystem.*
import kotlinx.serialization.json.*
import java.io.File
import java.time.Instant

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1800dp-xhdpi")
class ExtensionModelShotsTest {
    @get:Rule val rule = createComposeRule()
    private val theme = ExtensionTheme("example", "示例外观", "ocean", "海蓝", mapOf("accent" to "#0059A6"), mapOf("accent" to "#8BC4FF"))
    private val template = ExtensionTriageTemplate("example", "示例规则", "quiet", "大群只看提到我的", "群聊只看提到我的消息；私聊照常保留。")
    private val contributions = listOf(
        ExtensionContribution("ui", "home", "首页入口", true, true),
        ExtensionContribution("themes", "ocean", "海蓝主题色", false, true),
        ExtensionContribution("connectors", "example-chat", "示例聊天", true, false, "connector"),
    )
    private val detail = ExtensionDetail(HubExtension("example", "示例扩展", "ui", "running", source = "installed", enabled = true,
        sandbox = ExtensionSandbox(enabled = true, active = false, why = "这台机器无法启用隔离", egress = ExtensionEgress(14, 2))),
        form = listOf(ExtensionField("triage_rules", title = "分拣规则")),
        config = JsonObject(mapOf("triage_rules" to JsonPrimitive("原有规则"))), contributions = contributions)
    private val preview = ExtensionPreview(previewId = "pack", permissionsDigest = "digest", manifest = PreviewManifest("starter", "入门合集", "1.0.0",
        extensionPack = listOf(ExtensionPackMember("notes"), ExtensionPackMember("theme"))),
        depends = listOf(ExtensionDependency("notes", state = "missing")),
        bundle = listOf(
            ExtensionPreview(previewId = "notes", manifest = PreviewManifest("notes", "笔记扩展", "1.0.0"), permissionsDigest = "d-notes",
                permissions = listOf(ExtensionPermission("network", "连接声明的服务", "notes.example.com"))),
            ExtensionPreview(previewId = "theme", manifest = PreviewManifest("theme", "外观扩展", "1.0.0"), permissionsDigest = "d-theme"),
        ))

    private fun capture(name: String, node: SemanticsNodeInteraction = rule.onNodeWithTag("shot")) {
        rule.waitForIdle()
        val image = node.captureToImage().asAndroidBitmap()
        assertTrue(image.width > 0 && image.height > 0)
        val background = image.getPixel(0, 0)
        var drawn = 0
        for (y in 0 until image.height step 7) for (x in 0 until image.width step 7) if (image.getPixel(x, y) != background) drawn++
        assertTrue("$name did not draw content", drawn > 100)
        val dir = File("build/extension-model-shots").also { it.mkdirs() }
        File(dir, "$name.png").outputStream().use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun actualDetailAndConfirmedTemplateInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var current = detail
        var saved = 0
        var switched = 0
        val api = object : ExtensionApiStub() {
            override suspend fun extension(id: String) = current
            override suspend fun setContribution(id: String, point: String, contribution: String, enabled: Boolean): ExtensionDetail {
                switched++
                current = current.copy(contributions = current.contributions!!.map { if (it.id == contribution && it.point == point) it.copy(enabled = enabled) else it })
                return current
            }
            override suspend fun setConfig(id: String, config: JsonObject): ExtensionDetail {
                saved++; current = current.copy(config = config); return current
            }
        }
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                CompositionLocalProvider(LocalExtensionTriageTemplates provides listOf(template)) {
                    Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) {
                        ExtensionPage(api, "example", {})
                    }
                }
            }
        }
        rule.waitUntil { rule.onAllNodesWithText("首页入口").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("首页入口").performClick()
        rule.waitForIdle()
        assertEquals(1, switched)
        assertEquals(0, saved)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("detail-${s.key}-${if (d) "dark" else "light"}")
            rule.onNodeWithText("从模板填").performClick()
            rule.onNodeWithText(template.title).performClick()
            assertEquals(0, saved)
            capture("template-confirm-${s.key}-${if (d) "dark" else "light"}", rule.onNode(isDialog()))
            rule.onNodeWithText("填入草稿").performClick()
            rule.waitForIdle()
            assertEquals(0, saved)
            rule.onNodeWithText(template.text).assertExists()
        }
        rule.onNodeWithText("保存").performClick()
        rule.waitForIdle()
        assertEquals(1, saved)
        assertEquals(JsonPrimitive(template.text), current.config["triage_rules"])
    }

    // "Manage markets" with the official market (#2351): first, as the hub lists it, with its "Official" tag; the owner's own after it.
    @Test fun officialMarketFirstWithItsTagInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val api = object : ExtensionApiStub() {
            override suspend fun markets() = listOf(
                ExtensionMarket("o1", "github:example/official", "官方扩展", extensions = 2, official = true),
                ExtensionMarket("m1", "https://plugins.example.com/team/", "团队扩展", hasToken = true, extensions = 3),
            )
        }
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) { ExtensionMarketsPage(api, {}) }
            }
        }
        rule.waitUntil { rule.onAllNodesWithText("官方扩展").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("官方").assertExists()
        assertTrue(rule.onNodeWithText("官方扩展").fetchSemanticsNode().boundsInRoot.top < rule.onNodeWithText("团队扩展").fetchSemanticsNode().boundsInRoot.top)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("markets-${s.key}-${if (d) "dark" else "light"}")
        }
    }

    @Test fun actualPreviewAndPermissionConfirmationInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var confirms = 0
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                PreviewSheet(null, preview, false, {}, { confirms++ }, Modifier.testTag("shot"))
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("bundle-${s.key}-${if (d) "dark" else "light"}")
            rule.onNodeWithText("连接声明的服务").assertExists()
            assertEquals(0, confirms)
        }
        rule.onNodeWithText("同意并安装").performClick()
        assertEquals(1, confirms)
    }

    @Test fun notReadyDependencyIsVisibleAndCannotBeConfirmedInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var confirms = 0
        val waiting = preview.copy(depends = listOf(ExtensionDependency("notes", name = "笔记扩展", state = "not_ready")))
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                PreviewSheet(null, waiting, false, {}, { confirms++ }, Modifier.testTag("shot"))
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.onNodeWithText("笔记扩展 · 依赖未就绪").assertExists()
            rule.onNodeWithText("先完成依赖扩展的设置", substring = true).assertExists()
            rule.onNodeWithText("同意并安装").assertIsNotEnabled()
            capture("not-ready-${s.key}-${if (d) "dark" else "light"}")
        }
        assertEquals(0, confirms)
    }

    /** One extension's preview the hub lets go at 10:10 UTC (#1316). */
    private val notes = ExtensionPreview(previewId = "notes", permissionsDigest = "d-notes", manifest = PreviewManifest("notes", "笔记扩展", "1.0.0"),
        permissions = listOf(ExtensionPermission("network", "连接声明的服务", "notes.example.com")), expiresAt = "2026-11-08T10:10:00Z")

    @Test fun anExpiredPreviewOffersPreviewAgainInsteadOfTheYesInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var confirms = 0
        var again = 0
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                PreviewSheet(null, notes, false, {}, { confirms++ }, Modifier.testTag("shot"), onPreviewAgain = { again++ },
                    now = { Instant.parse("2026-11-08T10:12:00Z") })
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.onNodeWithText("这次预览已经过期", substring = true).assertExists()
            rule.onNodeWithText("同意并安装").assertDoesNotExist()
            capture("expired-${s.key}-${if (d) "dark" else "light"}")
        }
        rule.onNodeWithText("重新预览").performClick()
        assertEquals(1, again)
        assertEquals(0, confirms)
    }

    @Test fun aPreviewThatExpiresWhileTheSheetIsOpenStopsOfferingTheYes() {
        var clock = Instant.parse("2026-11-08T10:09:30Z")
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                PreviewSheet(null, notes, false, {}, {}, Modifier.testTag("shot"), now = { clock })
            }
        }
        rule.mainClock.advanceTimeByFrame()
        rule.onNodeWithText("同意并安装").assertIsEnabled()
        rule.onNodeWithText("重新预览").assertDoesNotExist()
        clock = Instant.parse("2026-11-08T10:10:00Z")
        rule.mainClock.advanceTimeBy(30_000)
        rule.onNodeWithText("重新预览").assertExists()
        rule.onNodeWithText("同意并安装").assertDoesNotExist()
    }

    @Test fun themePickerOnlyChangesAfterSelection() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var selected by mutableStateOf<ExtensionTheme?>(null)
        rule.setContent {
            val context = LocalContext.current
            val store = remember { AppStyleStore(context) }
            SideEffect { store.setStyle(style) }
            CompositionLocalProvider(LocalAppStyleStore provides store, LocalExtensionThemeOptions provides listOf(theme),
                LocalExtensionThemeScope provides "test", LocalExtensionRoleTheme provides selected) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        ExtensionThemeChoices(listOf(theme), selected) { selected = it }
                        androidx.compose.material3.Text("强调色", style = PersonaTheme.type.body, color = PersonaTheme.palette.accent,
                            modifier = Modifier.padding(Tokens.Space.l))
                        JCheckbox(true, {})
                    }
                }
            }
        }
        assertNull(selected)
        rule.onNodeWithText("海蓝").performClick()
        rule.waitForIdle()
        assertEquals(theme, selected)
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("theme-${s.key}-${if (d) "dark" else "light"}")
        }
        rule.onNodeWithText("默认").performClick()
        assertNull(selected)
    }
}
