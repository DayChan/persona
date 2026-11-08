package persona.feature.assistant

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.yield
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * What a Persona conversation was always allowed, in its "Session info" (#890): the list, a hub without it, failures,
 * and a revoke asked first, with the warning when the running conversation keeps the permission. Every style, light
 * and dark; the PNGs are in build/always-grants-shots.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class AlwaysGrantsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val res get() = rule.activity.resources

    private val main = Session(machineId = "hub", sessionId = "main", title = "主对话", assistant = true)
    private val bash = AlwaysGrant("a".repeat(64), "Bash", "2026-11-08T09:30:00Z")
    private val read = AlwaysGrant("b".repeat(64), "Read", "2026-11-08T18:05:00+08:00")
    private val unnamed = AlwaysGrant("c".repeat(64), "", null)
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private inner class Hub(var grants: List<AlwaysGrant>? = listOf(bash, read)) : AssistantRepository, MessageSender, AssistantManagement {
        override val assistant = MutableStateFlow(AssistantSnapshot(main.key, listOf(main)))
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val reads = mutableListOf<SessionKey>()
        val revoked = mutableListOf<Pair<SessionKey, String>>()
        var fail: Exception? = null
        var restart = false
        var gate: CompletableDeferred<Unit>? = null
        override suspend fun alwaysGrants(session: SessionKey): List<AlwaysGrant>? {
            reads += session
            fail?.let { throw it }
            return grants
        }
        override suspend fun revokeAlwaysGrant(session: SessionKey, keyHash: String): Boolean {
            gate?.await()
            fail?.let { throw it }
            revoked += session to keyHash
            grants = grants?.filterNot { it.keyHash == keyHash }
            return restart
        }
        override suspend fun refresh() = Unit
        override suspend fun warm() = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = error("Unexpected send")
        override suspend fun endConversation(session: SessionKey) = error("Unexpected end")
        override suspend fun tasks(): List<ScheduledTask> = emptyList()
        override suspend fun saveTask(task: ScheduledTask) = error("Unexpected save")
        override suspend fun deleteTask(id: String) = error("Unexpected delete")
        override suspend fun runTask(id: String): SessionKey = error("Unexpected run")
        override suspend fun freshTask(id: String): ScheduledTask = error("Unexpected fresh")
        override suspend fun runs(): List<Run> = emptyList()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = error("Unexpected notes")
        override suspend fun memory(): List<MemoryFile> = emptyList()
        override suspend fun saveMemory(name: String, content: String) = error("Unexpected memory")
        override suspend fun deleteMemory(name: String) = error("Unexpected delete")
    }

    @Test fun theListLoadsAndAHubWithoutItShowsNothing() = runBlocking {
        val hub = Hub()
        val model = AlwaysGrantsModel(hub, main.key, res)
        model.load()
        assertEquals(listOf(bash, read), model.state.value.grants)
        assertEquals(listOf(main.key), hub.reads)
        // 404 / 403 come back as null: no section, no error.
        hub.grants = null
        model.load()
        assertTrue(model.state.value.unavailable)
        assertNull(model.state.value.error)
        val older = object : AssistantManagement by hub {
            override suspend fun alwaysGrants(session: SessionKey): List<AlwaysGrant>? = throw UnsupportedOperationException("old transport")
        }
        AlwaysGrantsModel(older, main.key, res).also { it.load() }.let { assertTrue(it.state.value.unavailable) }
    }

    @Test fun aFailedReadSaysWhyAndCanBeRetried() = runBlocking {
        val hub = Hub()
        hub.fail = IllegalStateException("权限库暂时读不到")
        val model = AlwaysGrantsModel(hub, main.key, res)
        model.load()
        assertNull(model.state.value.grants)
        assertEquals("权限库暂时读不到", model.state.value.error)
        assertFalse(model.state.value.unavailable)
        hub.fail = null
        model.load()
        assertNull(model.state.value.error)
        assertEquals(2, model.state.value.grants?.size)
    }

    @Test fun aRevokeTakesItsRowAwayAndARunningConversationThatKeepsItSaysSo() = runBlocking {
        val hub = Hub()
        val other = SessionKey("hub", "other")
        val model = AlwaysGrantsModel(hub, SessionKey("hub", "warned"), res)
        model.load()
        assertTrue(model.revoke(bash))
        assertEquals(listOf(read), model.state.value.grants)
        assertFalse(model.state.value.restartNeeded)
        hub.restart = true
        assertTrue(model.revoke(read))
        assertEquals(emptyList<AlwaysGrant>(), model.state.value.grants)
        assertTrue(model.state.value.restartNeeded)
        assertEquals(listOf("a".repeat(64), "b".repeat(64)), hub.revoked.map { it.second })
        // Only the user's restart clears it: the warning outlives the sheet, for this conversation alone.
        assertTrue(AlwaysGrantsModel(hub, SessionKey("hub", "warned"), res).state.value.restartNeeded)
        assertFalse(AlwaysGrantsModel(hub, other, res).state.value.restartNeeded)
    }

    @Test fun aFailedRevokeKeepsTheRowAndSaysWhy() = runBlocking {
        val hub = Hub()
        val model = AlwaysGrantsModel(hub, main.key, res)
        model.load()
        hub.fail = IllegalStateException("hub 没有回答")
        assertFalse(model.revoke(bash))
        assertEquals("没有撤销：hub 没有回答", model.state.value.error)
        assertEquals(listOf(bash, read), model.state.value.grants)
        assertNull(model.state.value.revoking)
    }

    @Test fun oneRevokeAtATime() = runBlocking {
        val hub = Hub()
        hub.gate = CompletableDeferred()
        val model = AlwaysGrantsModel(hub, main.key, res)
        model.load()
        val first = async { model.revoke(bash) }
        while (model.state.value.revoking == null) yield()
        assertFalse(model.revoke(read))
        hub.gate!!.complete(Unit)
        assertTrue(first.await())
        assertEquals(listOf("a".repeat(64)), hub.revoked.map { it.second })
    }

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)

    private fun mount(hub: Hub) {
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val configuration = Configuration(LocalConfiguration.current).apply {
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
                CompositionLocalProvider(LocalConfiguration provides configuration) {
                    AssistantScreen(navigator, hub, hub, startConversation = { error("Unexpected creation") },
                        requestedSession = main.key, management = hub, composerService = null,
                        chatContent = { Text("chat:${it.sessionId}") })
                }
            }
        }
        rule.onNodeWithText("chat:main").assertExists()
    }

    private fun openSessionInfo() {
        rule.onNodeWithText("Persona").performClick()
        rule.onNodeWithText("会话信息").performClick()
        rule.waitForIdle()
    }

    /** The sheet's body, or the dialog over it. */
    private fun capture(name: String, node: SemanticsNodeInteraction = rule.onNodeWithTag("session-info")) {
        rule.waitForIdle()
        val image = node.captureToImage().asAndroidBitmap()
        val dir = File("build/always-grants-shots").also { it.mkdirs() }
        File(dir, "$name.png").outputStream().use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun sessionInfoListsTheGrantsAndRevokesOnlyAfterAskingInEveryStyle() {
        val hub = Hub(listOf(bash, read, unnamed))
        hub.restart = true
        mount(hub)
        openSessionInfo()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.onNodeWithText("当前对话：main").assertExists()
            rule.onNodeWithText("这段对话中总是允许的权限").assertExists()
            rule.onNodeWithText("Bash").assertExists()
            rule.onNodeWithText("未命名权限").assertExists()
            capture("list-${s.key}-${if (d) "dark" else "light"}")
        }
        rule.runOnIdle { style = AppStyle.Glass; dark = false }
        rule.onAllNodesWithText("撤销")[0].performClick()
        rule.onNodeWithText("撤销这项权限？").assertExists()
        assertTrue(hub.revoked.isEmpty())
        capture("confirm-glass-light", rule.onNode(isDialog()))
        rule.onAllNodesWithText("撤销").onLast().performClick()
        rule.waitUntil(5_000) { hub.revoked.isNotEmpty() }
        rule.onNodeWithText("已撤销保存的权限", substring = true).assertExists()
        rule.onAllNodesWithText("Bash").assertCountEquals(0)
        assertEquals(listOf(main.key to "a".repeat(64)), hub.revoked)
        capture("restart-glass-light")
    }

    @Test fun aHubWithoutTheListShowsOnlyTheConversation() {
        mount(Hub(grants = null))
        openSessionInfo()
        rule.onNodeWithText("当前对话：main").assertExists()
        rule.onAllNodesWithText("这段对话中总是允许的权限").assertCountEquals(0)
    }
}
