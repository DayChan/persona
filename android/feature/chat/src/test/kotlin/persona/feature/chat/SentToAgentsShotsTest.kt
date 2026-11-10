package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import java.time.temporal.ChronoUnit
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.tasks.AssistantExtra
import persona.core.domain.tasks.SentToAgents
import persona.core.domain.tasks.TasksStore

/**
 * What Persona sends to a task's agent in the main conversation (#1623, docs/assistant/unified-entry.md "Messages to
 * agents"): the hand-off right after its card is drawn only on the card ("2 instructions sent"), a later rework and a message
 * to a task with no card here are folded lines on the left, the user's own word keeps its bubble. Native JVM images in
 * every style, light and dark, folded and opened (build/sent-shots/); not emulator screenshots.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1600dp-xhdpi")
class SentToAgentsShotsTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
    @After fun close() = scope.cancel()

    private val now = Instant.now().truncatedTo(ChronoUnit.SECONDS)
    private fun ago(min: Long, plusMillis: Long = 0) = now.minus(min, ChronoUnit.MINUTES).plusMillis(plusMillis).toString()
    private val handOff = "把设置页的内存占用降下来。\n\n1. 先用示例数据复现：打开设置页来回切 20 次。\n2. 找出没释放的缓存。\n3. 改完跑一遍单元测试，把结果写进 PR。"
    private val rework = "先停一下：缓存的大小不要写死，改成按屏幕数算。\n其余照旧。"
    private fun routed(id: String, ref: String, at: String, text: String, persona: Boolean, interrupted: Boolean = false) =
        FeedItem(id, "main", at, "routed", ref, buildJsonObject {
            put("text", JsonPrimitive(text))
            if (persona) put("from", JsonPrimitive("assistant"))
            if (interrupted) put("interrupted", JsonPrimitive(true))
        })
    private val feed = listOf(
        FeedItem("card1", "main", ago(10), "task", "task1"),
        routed("handoff", "task1", ago(10, 8), handOff, persona = true),
        routed("said", "task1", ago(8), "顺便看看日志里有没有报错", persona = false),
        routed("rework", "task1", ago(3), rework, persona = true, interrupted = true),
        routed("other", "task2", ago(2), "把示例项目的依赖升到最新的小版本，跑完测试告诉我。", persona = true),
    )

    private class Api(val feed: List<FeedItem>, val created: String) : TasksApi {
        override suspend fun todos() = listOf(
            Todo("task1", title = "降低设置页的内存占用", createdAt = created, origin = "main"),
            Todo("task2", title = "升级示例项目的依赖", createdAt = created, origin = "main"))
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(feed)
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = "main"
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }
    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private fun host(): AssistantTaskHost {
        val store = TasksStore(Api(feed, ago(10)), Connection(), scope)
        store.reloadTodos()
        store.load("main")
        return AssistantTaskHost(store, "main", emptyList(), { it }, {}, { throw it }, {})
    }

    @Test fun personasMessagesFoldAndTheCardListsThem() {
        val host = host()
        rule.setContent {
            // No ripples: the platform's (with its sparkle) runs on the render thread, which Robolectric has not, so a
            // tapped row would keep a frozen ripple in the shots.
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
              androidx.compose.runtime.CompositionLocalProvider(androidx.compose.material3.LocalRippleConfiguration provides null) {
                val feeds by host.tasks.feeds.collectAsStateWithLifecycle()
                // as the chat page does: the hand-offs the cards take in are not lines of their own
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    SentToAgents.shown(feeds["main"].orEmpty()).forEach { AssistantExtraLine(AssistantExtra.Feed(it), host) }
                }
              }
            }
        }
        rule.waitForIdle()
        // the rework and the message to a task with no card are folded lines; the hand-off is not a line; the user's word is a bubble
        rule.onAllNodesWithTag("sent-line", useUnmergedTree = true).assertCountEquals(2)
        rule.onNodeWithText("打断「降低设置页的内存占用」后发送").assertIsDisplayed()
        rule.onNodeWithText("发给「升级示例项目的依赖」").assertIsDisplayed()
        rule.onNodeWithText("顺便看看日志里有没有报错").assertIsDisplayed()
        rule.onNodeWithText("把设置页的内存占用降下来。", substring = true).assertDoesNotExist()
        rule.onNodeWithText("已发 2 条指令").assertIsDisplayed()
        shots("folded")

        // a folded line opens to its whole text with Copy; the card's list opens without opening the task
        rule.onNodeWithText("· 先停一下：缓存的大小不要写死，改成按屏幕数算。").performClick()
        rule.onNodeWithText("其余照旧。", substring = true).assertIsDisplayed()
        rule.onNodeWithTag("task-sent").performClick()
        rule.onAllNodesWithTag("task-sent-entry", useUnmergedTree = true).assertCountEquals(2)
        rule.onNodeWithText("把设置页的内存占用降下来。").performClick()
        rule.onNodeWithText("3. 改完跑一遍单元测试，把结果写进 PR。", substring = true).assertIsDisplayed()
        rule.onAllNodesWithTag("sent-detail", useUnmergedTree = true).assertCountEquals(2)
        rule.runOnIdle { assertNull(host.sheet.value) }
        // each style draws its own card, so its list is opened again after a switch
        shots("open") {
            if (rule.onAllNodesWithTag("task-sent-entry", useUnmergedTree = true).fetchSemanticsNodes().isEmpty()) {
                rule.onNodeWithTag("task-sent").performClick()
                rule.onNodeWithText("把设置页的内存占用降下来。").performClick()
                rule.waitForIdle()
            }
        }

        // the task's name on a folded line opens the task
        rule.onNodeWithText("发给「升级示例项目的依赖」").performClick()
        rule.runOnIdle { assertEquals(TaskSheet.Task("task2"), host.sheet.value) }
    }

    private fun shots(state: String, prepare: () -> Unit = {}) {
        val output = File("build/sent-shots").also { it.mkdirs() }
        val light = mutableMapOf<AppStyle, Bitmap>()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            setLook(s, d)
            prepare()
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            if (!d) light[s] = bitmap.copy(Bitmap.Config.ARGB_8888, false)
            else assertFalse("Light and dark must differ for $s", light.getValue(s).sameAs(bitmap))
            File(output, "sent-$state-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        setLook(AppStyle.Glass, false)
    }

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private fun setLook(style: AppStyle, dark: Boolean) {
        this.style = style; this.dark = dark
        Snapshot.sendApplyNotifications()
        rule.waitForIdle()
    }
}
