package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxItem
import persona.core.domain.sort.AgentSession
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.Navigator
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class InboxHandOffShotsTest {
    @get:Rule val rule = createComposeRule()
    private val repository = HandOffTestRepository()
    private val navigator = HandOffTestNavigator()
    private var dismissed = false

    private fun show() {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                InboxHandOffSheet(repository, repository.record, navigator, onDismiss = { dismissed = true })
            }
        }
    }

    @Test fun targetDefaultsFollowSelectionUntilTheSwitchIsChosen() {
        show()
        rule.onNode(isToggleable()).assertIsOff()
        rule.onNodeWithText("Example agent").performClick()
        rule.onNode(isToggleable()).assertIsOn()
        rule.onNodeWithText("Persona").performClick()
        rule.onNode(isToggleable()).assertIsOff()
        rule.onNode(isToggleable()).performClick().assertIsOn()
        rule.onNodeWithText("Example agent").performClick()
        rule.onNodeWithText("Persona").performClick()
        rule.onNode(isToggleable()).assertIsOn()
        rule.onNode(isToggleable()).performClick().assertIsOff()
        rule.onNodeWithText("Example agent").performClick()
        rule.onNode(isToggleable()).assertIsOff()
    }

    @Test fun mainSessionOpensAfterSuccessWithoutMarkingDone() {
        repository.result = SessionKey("hub", "main")
        show()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertTrue(dismissed)
        assertEquals(listOf(ChatRoute("hub", "main", ConversationKind.Assistant)), navigator.routes)
        assertTrue(navigator.tabs.isEmpty())
        assertEquals(0, repository.doneCalls)
    }

    @Test fun agentTargetMarksDoneAndOpensAgentConversation() {
        show()
        rule.onNodeWithText("Example agent").performClick()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertTrue(dismissed)
        assertEquals(1, repository.doneCalls)
        assertEquals("laptop", repository.requests.single().machineId)
        assertEquals("agent", repository.requests.single().sessionId)
        assertEquals(listOf(ChatRoute("laptop", "agent", ConversationKind.Agent)), navigator.routes)
        assertTrue(navigator.tabs.isEmpty())
    }

    @Test fun oldHubFallbackSelectsPersona() {
        show()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertEquals(listOf(AppTab.Persona), navigator.tabs)
        assertTrue(navigator.routes.isEmpty())
    }

    @Test fun failedAttemptStaysOpenAndRetryUsesTheSameOperation() {
        repository.failure = IllegalStateException("network offline")
        show()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertFalse(dismissed)
        assertTrue(navigator.routes.isEmpty())
        assertTrue(rule.onAllNodesWithText("network offline", substring = true).fetchSemanticsNodes().isNotEmpty())
        repository.failure = null
        repository.result = SessionKey("hub", "main")
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertEquals(2, repository.requests.size)
        assertEquals(repository.requests[0].operationId, repository.requests[1].operationId)
        assertTrue(dismissed)
    }

    /** Per source (#2121): the sender's last target is preselected and marked; one that has ended falls back with a note. */
    @Test fun aSourceStartsWhereItWentLastTime() {
        val remembering = HandOffTestRepository(listOf(InboxMuteOption("sender", "me@example.com", "Me")))
        remembering.handOffMemory.value = listOf(HandOffMemoryEntry("mail|sender:me@example.com", "agent:laptop/agent"))
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxHandOffSheet(remembering, remembering.record, navigator, onDismiss = { dismissed = true }) } }
        rule.waitForIdle()
        rule.onNode(isToggleable()).assertIsOn()
        rule.onNodeWithText("Last time").assertExists()
        rule.onNodeWithText("Persona").performClick()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertEquals(HandOffMemoryEntry("mail|sender:me@example.com", "main"), remembering.handOffMemory.value.first())
    }

    @Test fun anEndedAgentFallsBackToPersonaAndSaysSo() {
        val remembering = HandOffTestRepository(listOf(InboxMuteOption("sender", "me@example.com", "Me")))
        remembering.handOffMemory.value = listOf(HandOffMemoryEntry("mail|sender:me@example.com", "agent:laptop/gone"))
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxHandOffSheet(remembering, remembering.record, navigator, onDismiss = {}) } }
        rule.waitForIdle()
        rule.onNode(isToggleable()).assertIsOff()
        rule.onNodeWithText("Last time").assertDoesNotExist()
        rule.onNodeWithText("has ended", substring = true).assertExists()
    }

    @Test fun sendingToAnAgentRemembersTheSource() {
        val remembering = HandOffTestRepository(listOf(InboxMuteOption("repo", "example/weather", "example/weather")))
        rule.setContent { PersonaTheme(reduceMotion = true) { InboxHandOffSheet(remembering, remembering.record, navigator, onDismiss = {}) } }
        rule.onNodeWithText("Example agent").performClick()
        rule.onNodeWithText("Send", substring = false).performClick()
        rule.waitForIdle()
        assertEquals(listOf(HandOffMemoryEntry("mail|repo:example/weather", "agent:laptop/agent")), remembering.handOffMemory.value)
    }

    @Test fun lastTimeInEveryStyleAndColorMode() {
        val remembering = HandOffTestRepository(listOf(InboxMuteOption("sender", "me@example.com", "Me")))
        remembering.handOffMemory.value = listOf(HandOffMemoryEntry("mail|sender:me@example.com", "agent:laptop/agent"))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background)) {
                    InboxHandOffSheet(remembering, remembering.record, navigator, onDismiss = {})
                }
            }
        }
        val dir = File(System.getProperty("persona.shots") ?: "build/inbox-handoff-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            rule.onNodeWithText("Last time").assertExists()
            val window = listOf(isDialog(), isPopup()).first { rule.onAllNodes(it).fetchSemanticsNodes().isNotEmpty() }
            val bitmap = rule.onAllNodes(window).onLast().captureToImage().asAndroidBitmap()
            File(dir, "inbox-handoff-last-time-${s.key}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }

    @Test fun handOffSheetInEveryStyleAndColorMode() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background)) {
                    InboxHandOffSheet(repository, repository.record, navigator, onDismiss = {})
                }
            }
        }
        val dir = File(System.getProperty("persona.shots") ?: "build/inbox-handoff-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            rule.onNode(isToggleable()).assertIsOff()
            val window = listOf(isDialog(), isPopup()).first { rule.onAllNodes(it).fetchSemanticsNodes().isNotEmpty() }
            val bitmap = rule.onAllNodes(window).onLast().captureToImage().asAndroidBitmap()
            File(dir, "inbox-handoff-${s.key}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
    }
}

private class HandOffTestRepository(mutes: List<InboxMuteOption> = emptyList()) : InboxRepository {
    val record = InboxRecord(InboxItem("mail:example", title = "Project update", preview = "Please review the plan."), source = "mail", mutes = mutes)
    override val handOffMemory = MutableStateFlow(emptyList<HandOffMemoryEntry>())
    override suspend fun saveHandOffMemory(value: List<HandOffMemoryEntry>) { handOffMemory.value = value }
    override val entries = MutableStateFlow(emptyList<InboxEntry>())
    override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(record), sessions = listOf(
        AgentSession(Session(machineId = "laptop", sessionId = "agent", title = "Example agent", status = "idle")),
    )))
    override val connectionState = flowOf(ConnectionState.Online)
    var result: SessionKey? = null
    var failure: Exception? = null
    val requests = mutableListOf<InboxHandOffRequest>()
    var doneCalls = 0
    override suspend fun handOff(request: InboxHandOffRequest): SessionKey? {
        requests += request
        failure?.let { throw it }
        return result
    }
    override suspend fun refresh() = Unit
    override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
    override suspend fun loadOlder(feedId: String) = Unit
    override suspend fun refreshConversation(feedId: String) = Unit
    override suspend fun setDone(itemId: String, done: Boolean) { doneCalls++ }
    override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
    override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
    override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
    override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
}

private class HandOffTestNavigator : Navigator {
    val routes = mutableListOf<NavKey>()
    val tabs = mutableListOf<AppTab>()
    override fun navigate(key: NavKey) { routes += key }
    override fun back() = Unit
    override fun selectTab(tab: AppTab) { tabs += tab }
}
