package persona.feature.assistant.settings

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.runtime.entryProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.*
import persona.feature.assistant.*

/** Exercise both production entry paths, including the legacy-manager response in the original report. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1800dp-xhdpi")
class AssistantSettingsEntriesTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    @After fun reset() = AssistantManagerChoice.reset()

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    private val roster = ManagerRoster(listOf(
        HubManager("assistant", "助手", online = true, isDefault = true),
        HubManager("work", "工作", online = true),
    ), "assistant")

    private class Conversations : AssistantRepository, MessageSender, MainConversationSender {
        override val assistant = MutableStateFlow(AssistantSnapshot())
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun refresh() = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = error("No sends in settings")
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = error("No sends in settings")
    }
    private val drafts = object : DraftRepository {
        private fun empty(key: DraftKey) = DraftSnapshot(HubScope("test", "settings"), key)
        override fun observe(key: DraftKey) = flowOf(empty(key))
        override suspend fun read(key: DraftKey) = empty(key)
        override suspend fun save(key: DraftKey, text: String, expectedVersion: Long) = error("No draft saves in settings")
    }

    @Test fun settingsNavigationHasNoOwnerRowForEitherBackendOrLegacyResponse() = entries(inPlace = false)
    @Test fun assistantProfileHasNoOwnerRowForEitherBackendOrLegacyResponse() = entries(inPlace = true)

    private fun entries(inPlace: Boolean) {
        val conversations = Conversations()
        var scenario by mutableStateOf("legacy")
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        AssistantManagerChoice.choose("test", null, "assistant")
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                val scope = rememberCoroutineScope()
                val legacy = scenario !in listOf("claude", "codex")
                val config = AssistantConfig(manager = "assistant".takeUnless { legacy },
                    launcher = if (scenario == "codex") "codex" else "claude", timezone = "Asia/Shanghai",
                    permissionDefaults = if (legacy) null else
                        AssistantPermissionDefaults(1, "assistant", AssistantPermissionDefaults.MODES))
                val management = remember(scenario) { AssistantSettingsManagerTest.Hub(mutableMapOf("assistant" to config)) }
                val host = AssistantHostBindings("test/settings", management, drafts, scope, null, emptyList(), conversations, { _, _ -> })
                val shownRoster = when (scenario) {
                    "no-roster" -> null
                    "single" -> roster.copy(managers = roster.managers.take(1))
                    else -> roster
                }
                CompositionLocalProvider(LocalManagerRoster provides shownRoster, LocalAssistantHost provides host) {
                    Box(Modifier.testTag("entry-shot").width(400.dp).height(1800.dp)) {
                        key(scenario) {
                            if (inPlace) AssistantScreen(navigator, conversations, conversations,
                                startConversation = { error("No conversation starts in settings") }, management = management,
                                composerService = null, draftRepository = null, assistantChatContent = null)
                            else entryProvider<NavKey> { assistantEntries(navigator) }(AssistantSettingsRoute).Content()
                        }
                    }
                }
            }
        }
        for (response in listOf("legacy", "no-roster", "single", "claude", "codex")) {
            rule.runOnIdle { scenario = response }
            rule.waitForIdle()
            if (inPlace) rule.onNodeWithContentDescription("助手资料").performClick()
            for (s in AppStyle.entries) for (night in listOf(false, true)) {
                rule.runOnIdle { style = s; dark = night }
                rule.waitForIdle()
                // One navigation title and one switcher chip. The deleted owner row added two more matches.
                val switching = response !in listOf("no-roster", "single")
                rule.onAllNodesWithText("助手", useUnmergedTree = true).assertCountEquals(if (switching) 2 else 1)
                rule.onAllNodesWithText("工作", useUnmergedTree = true).assertCountEquals(if (switching) 1 else 0)
                val permission = rule.onAllNodesWithText("新会话默认权限").onLast()
                if (response in listOf("claude", "codex")) permission.assertIsEnabled() else permission.assertIsNotEnabled()
                val image = rule.onNodeWithTag("entry-shot").captureToImage().asAndroidBitmap()
                val out = File("build/settings-entry-shots").also { check(it.isDirectory || it.mkdirs()) }
                val name = "${if (inPlace) "profile" else "navigation"}-$response-${s.key}-${if (night) "dark" else "light"}.png"
                File(out, name).outputStream().use { check(image.compress(Bitmap.CompressFormat.PNG, 100, it)) }
            }
        }
        assertEquals(emptyList<OutboxMessage>(), conversations.outbox.value)
    }
}
