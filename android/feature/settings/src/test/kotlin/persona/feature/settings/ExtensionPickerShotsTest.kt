package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.api.*
import persona.core.designsystem.*
import java.io.File

/**
 * An extension's form picks one machine (format machine, by name, of the field's systems) and launchers (format launcher,
 * the hub's, hidden ones left out) from a sheet, and steps whole numbers (#2448, as iOS and the web since #2385): no
 * machine id or launcher name typed in, a launcher the hub no longer has still shown and marked, the choices saved with
 * the rest of the form. Every style, light and dark; the PNGs are in build/extension-picker-shots.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2200dp-xhdpi")
class ExtensionPickerShotsTest {
    @get:Rule val rule = createComposeRule()

    private val detail = ExtensionDetail(
        HubExtension("example-agent-chat", "Example 聊天", "process", "running", version = "1.0.0", source = "installed", enabled = true),
        form = listOf(
            ExtensionField("agent", "boolean", title = "聊天里的 /agent", default = JsonPrimitive(false)),
            ExtensionField("agent_machine", title = "/agent 在哪台电脑上跑", description = "空着用助手的", format = "machine", os = listOf("darwin", "linux")),
            ExtensionField("agent_launcher", title = "/agent 用的启动器", description = "空着用助手的", format = "launcher"),
            ExtensionField("agent_continue_hours", "integer", title = "接着上一次对话的时间（小时）", default = JsonPrimitive(4), minimum = 1.0, maximum = 72.0),
            ExtensionField("auto_reply_launcher", title = "代答用的启动器", description = "空着用 /agent 的", format = "launcher"),
        ),
        config = buildJsonObject { put("agent_machine", "server"); put("auto_reply_launcher", "cc_retired") },
    )
    private val machines = listOf(
        Machine("macbook", name = "MacBook", os = "darwin", online = true),
        Machine("studio", name = "Mac Studio", os = "darwin", online = true),
        Machine("server", name = "服务器", os = "linux", online = true),
        Machine("pc", name = "游戏电脑", os = "windows", online = false),
    )
    private val launchers = listOf(
        HubLauncher("claude", source = "builtin"), HubLauncher("codex", source = "builtin"),
        HubLauncher("cc_main", displayName = "主力网关", source = "persona"), HubLauncher("cc_old", source = "zshrc", hidden = true),
    )

    private fun capture(name: String, node: SemanticsNodeInteraction = rule.onNodeWithTag("shot")) {
        rule.waitForIdle()
        val image = node.captureToImage().asAndroidBitmap()
        assertTrue(image.width > 0 && image.height > 0)
        val dir = File("build/extension-picker-shots").also { it.mkdirs() }
        File(dir, "$name.png").outputStream().use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun machinesAndLaunchersArePickedInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var current = detail
        val configs = mutableListOf<JsonObject>()
        var launcherReads = 0
        val api = object : ExtensionApiStub() {
            override suspend fun extension(id: String) = current
            override suspend fun setConfig(id: String, config: JsonObject): ExtensionDetail { configs += config; current = current.copy(config = config); return current }
        }
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) {
                    ExtensionPage(api, "example-agent-chat", onBack = {}, machines = flowOf(machines), launchers = { launcherReads++; launchers })
                }
            }
        }
        rule.waitUntil { rule.onAllNodesWithText("代答用的启动器").fetchSemanticsNodes().isNotEmpty() }
        rule.waitUntil { rule.onAllNodesWithText("cc_retired（找不到这个启动器）").fetchSemanticsNodes().isNotEmpty() }
        // The machine by name, never its id; a launcher the hub no longer has, marked.
        rule.onNodeWithText("服务器").assertExists()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("form-${s.key}-${if (d) "dark" else "light"}")
        }
        style = AppStyle.Glass; dark = false
        assertEquals("the launchers are read once for the page", 1, launcherReads)

        // The machines of the field's systems (no Windows PC), "Not set" first. A sheet is a window of its own: each style opens it anew.
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithText("/agent 在哪台电脑上跑").performScrollTo().performClick()
            rule.onNode(isDialog()).assertExists()
            capture("machines-${s.key}-${if (d) "dark" else "light"}", rule.onNode(isDialog()))
            rule.onAllNodesWithText("游戏电脑").filterToOne(hasAnyAncestor(isDialog())).assertDoesNotExist()
            rule.onAllNodesWithText("服务器").filterToOne(hasAnyAncestor(isDialog())).performClick()
        }
        style = AppStyle.Glass; dark = false
        rule.waitForIdle()
        rule.onNodeWithText("/agent 在哪台电脑上跑").performScrollTo().performClick()
        rule.onAllNodesWithText("MacBook").filterToOne(hasAnyAncestor(isDialog())).performClick()
        rule.waitForIdle()
        rule.onNodeWithText("MacBook").assertExists()

        // The launchers, hidden ones left out, a display name before the name.
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithText("/agent 用的启动器").performScrollTo().performClick()
            capture("launchers-${s.key}-${if (d) "dark" else "light"}", rule.onNode(isDialog()))
            rule.onAllNodesWithText("cc_old").filterToOne(hasAnyAncestor(isDialog())).assertDoesNotExist()
            rule.onAllNodesWithText("主力网关 (cc_main)").filterToOne(hasAnyAncestor(isDialog())).performClick()
        }
        style = AppStyle.Glass; dark = false
        rule.waitForIdle()
        rule.onNodeWithText("主力网关 (cc_main)").assertExists()

        // A whole number steps from its default within its bounds.
        rule.onAllNodesWithContentDescription("增加").onFirst().performScrollTo().performClick()
        rule.waitForIdle()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            capture("steps-${s.key}-${if (d) "dark" else "light"}")
        }
        style = AppStyle.Glass; dark = false

        rule.onNodeWithText("保存").performScrollTo().performClick()
        rule.waitUntil { configs.isNotEmpty() }
        assertEquals(buildJsonObject {
            put("agent_machine", "macbook"); put("auto_reply_launcher", "cc_retired"); put("agent_launcher", "cc_main"); put("agent_continue_hours", 5)
        }, configs.single())
    }

    /** Launchers not in yet are typed, and say they are coming; ones that could not be read stay typed. */
    @Test fun launchersNotInAreTyped() {
        val gate = CompletableDeferred<List<HubLauncher>>()
        val api = object : ExtensionApiStub() { override suspend fun extension(id: String) = detail }
        rule.setContent {
            PersonaTheme(style = AppStyle.Glass, darkTheme = false, reduceMotion = true) {
                Box(Modifier.testTag("shot").fillMaxSize().background(PersonaTheme.palette.background)) {
                    ExtensionPage(api, "example-agent-chat", onBack = {}, machines = flowOf(machines), launchers = { gate.await() })
                }
            }
        }
        rule.waitUntil { rule.onAllNodesWithText("正在读启动器…").fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("cc_retired（找不到这个启动器）").assertDoesNotExist()
        gate.completeExceptionally(IllegalStateException("timeout"))
        rule.waitUntil { rule.onAllNodesWithText("正在读启动器…").fetchSemanticsNodes().isEmpty() }
        rule.onNodeWithText("cc_retired（找不到这个启动器）").assertDoesNotExist()
        // The machine is still a pick: it does not wait on the launchers.
        rule.onNodeWithText("服务器").assertExists()
    }
}
