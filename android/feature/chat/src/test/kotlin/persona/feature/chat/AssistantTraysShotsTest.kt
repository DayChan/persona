package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.height
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.After
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.tasks.TasksStore
import java.io.File

/** Native JVM evidence of the real ChatPage, not a hand-built tray mock or a device screenshot. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class AssistantTraysShotsTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    @After fun close() { scope.cancel() }

    private val main = SessionKey("computer-a", "main")
    private val running = Session("computer-a", "working", title = "Update the web interface", state = "working", status = "working",
        activity = "Checking the routes", plan = PlanInfo(3, 5, "Checking the routes"))
    private val blocked = Session("computer-b", "blocked", title = "Prepare the release", state = "needs_you", status = "blocked", attentionReason = "Waiting for signing access")
    private val unread = Session("computer-b", "unread", title = "Improve the mobile interface", state = "unread", status = "done", lastText = "PR #42 is merged")
    private val todoRows = listOf(
        Todo("task-a", running.title, origin = "main", agents = listOf(TodoAgent(running.machineId, running.sessionId)),
            supervision = Supervision(progress = TaskProgress("progress", 3, 5, "Checking the routes"))),
        Todo("task-b", blocked.title, origin = "main", agents = listOf(TodoAgent(blocked.machineId, blocked.sessionId)),
            supervision = Supervision(progress = TaskProgress("progress", 2, 4, "Waiting for signing access"))),
        Todo("task-c", unread.title, origin = "main", agents = listOf(TodoAgent(unread.machineId, unread.sessionId)), doneAt = "2026-11-01T10:00:00Z"),
    )
    private val permission = Interaction("permission", machineId = running.machineId, sessionId = running.sessionId, kind = "permission", toolName = "Bash", summary = "Run the integration tests")
    private val planRequest = Interaction("plan", machineId = blocked.machineId, sessionId = blocked.sessionId, kind = "plan", plan = "1. Prepare the release\n2. Check the build")
    private val question = Interaction("question", machineId = blocked.machineId, sessionId = blocked.sessionId, kind = "question", summary = "Which release window?")
    private val inboxDraft = AssistantInboxDraft("reply-item", "Reply to the release discussion", InboxSuggestion(version = 7, kind = "reply", text = "The release is planned for Thursday."))

    private inner class Fixture {
        val requests = MutableStateFlow(listOf(permission, planRequest, question))
        val drafts = MutableStateFlow(listOf(inboxDraft))
        val approvals = mutableListOf<Pair<String, Resolution>>()
        val sends = mutableListOf<AssistantInboxDraft>()
        val opened = mutableListOf<SessionKey>()
        var returnedTodos = todoRows
        val accepted = mutableListOf<Pair<String, String>>()
        val patches = mutableListOf<Pair<String, Boolean?>>()
        var completionGate: CompletableDeferred<Unit>? = null
        var completionFailure: Throwable? = null
        val sessionRows = MutableStateFlow(listOf(Session(main.machineId, main.sessionId, title = "Persona", assistant = true, status = "done", state = "idle"), running, blocked, unread))
        val sessions = object : SessionsRepository {
            override val sessions = sessionRows
            override val machines = MutableStateFlow(listOf(Machine("computer-a", "Computer A", online = true), Machine("computer-b", "Computer B", online = true)))
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = requests
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) {
                approvals += id to resolution
                requests.value = requests.value.filterNot { it.id == id }
            }
        }
        val inbox = object : AssistantInboxTrays {
            override fun drafts(scope: String) = drafts
            override suspend fun refresh(scope: String) = Unit
            override suspend fun send(scope: String, draft: AssistantInboxDraft, clientId: String): Boolean {
                if (drafts.value.none { it == draft }) return false
                sends += draft; drafts.value = drafts.value.filterNot { it == draft }; return true
            }
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val api = object : TasksApi {
            override suspend fun todos() = returnedTodos
            override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo {
                patches += id to done
                completionGate?.await()
                completionFailure?.let { throw it }
                val completed = returnedTodos.first { it.id == id }.copy(doneAt = "2026-11-01T10:00:00Z", ask = null)
                returnedTodos = returnedTodos.map { if (it.id == id) completed else it }
                return completed
            }
            override suspend fun acceptTask(todoId: String, askedAt: String) {
                accepted += todoId to askedAt
                returnedTodos = returnedTodos.map { if (it.id == todoId) it.copy(ask = null) else it }
            }
            override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
            override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("not used")
            override suspend fun dismissPlan(id: String): TaskPlan = error("not used")
            override suspend fun retryTask(todoId: String): Todo = error("not used")
            override suspend fun removeTask(todoId: String) = Unit
            override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("not used")
            override suspend fun today(conversation: String): FeedItem = error("not used")
            override suspend fun mainConversation() = "main"
            override suspend fun inboxReply(itemId: String, text: String, card: String?) = error("bulk approval must never send a draft")
        }
        val tasks = TasksStore(api, connection, scope).also { it.reloadTodos() }
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = flowOf(ChatSnapshot(loading = false, items = listOf(
                Item("user", 1, role = "user", kind = "text", text = "Simplify the Assistant page.", ts = "2026-11-01T09:00:00Z"),
                Item("answer", 2, role = "assistant", kind = "text", text = "Decisions, progress and agents are above the composer. Tap an entry to see the details.", ts = "2026-11-01T09:00:01Z"),
            )))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val management = object : AssistantManagement {
            override suspend fun tasks() = listOf(ScheduledTask("schedule", "Routine supervision", repeats = ScheduledTask.INTERVAL, everyMinutes = 30,
                enabled = true, nextRun = "2026-11-01T10:30:00Z", lastStatus = "ok"))
            override suspend fun warm() = Unit
            override suspend fun endConversation(session: SessionKey) = Unit
            override suspend fun saveTask(task: ScheduledTask) = task
            override suspend fun deleteTask(id: String) = Unit
            override suspend fun runTask(id: String) = main
            override suspend fun freshTask(id: String) = ScheduledTask(id)
            override suspend fun runs() = emptyList<Run>()
            override suspend fun notes() = AssistantNotes()
            override suspend fun setNotes(text: String) = Unit
            override suspend fun memory() = emptyList<MemoryFile>()
            override suspend fun saveMemory(name: String, content: String) = Unit
            override suspend fun deleteMemory(name: String) = Unit
        }
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        repeat(5) { rule.mainClock.advanceTimeByFrame() }
        // ChatPage has its own system-motion theme; let the real tray arrow finish before capture.
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()
    }
    private fun shot(name: String): Int {
        val image = rule.onNodeWithTag("assistant-page-shot").captureToImage().asAndroidBitmap()
        val output = File("build/assistant-trays-shots").also { it.mkdirs() }
        File(output, "$name.png").outputStream().use { image.compress(Bitmap.CompressFormat.PNG, 100, it) }
        return image.getPixel(2, 2)
    }

    @Test fun allStylesAndAppearancesRenderActualAssistantPageAndMutuallyExclusiveTrays() {
        val fixture = Fixture()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var tab by mutableStateOf("assistant")
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("assistant-page-shot")) {
                        HomeNavigation(listOf(NavItem("assistant", "Assistant", PhIcons.Sparkle), NavItem("agents", "Agents", PhIcons.Robot)), tab,
                            { tab = it }, leading = { JAppMark(Tokens.Space.xxl) })
                        Box(Modifier.weight(1f)) {
                            ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                                chrome = ChatChromeBindings(tasks = if (tab == "assistant") fixture.tasks else null,
                                    assistantManagement = fixture.management, inboxTrays = fixture.inbox, inboxScope = "fixture", onOpenSession = { fixture.opened += it }),
                                renderStyle = if (tab == "assistant") ChatRenderStyle.Assistant else ChatRenderStyle.Agent,
                                embedded = true, showTopBar = false)
                        }
                    }
                }
            }
        }
        settle()
        rule.waitUntil(10_000) { rule.onAllNodesWithTag("assistant-tray-bar").fetchSemanticsNodes().isNotEmpty() }
        for (s in AppStyle.entries) {
            val corners = mutableListOf<Int>()
            for (d in listOf(false, true)) {
                style = s; dark = d; fixture.requests.value = listOf(permission, planRequest, question); fixture.drafts.value = listOf(inboxDraft); settle()
                rule.onNodeWithTag("assistant-tray-pending").assertDoesNotExist()
                for (entry in listOf("pending", "progress", "agents"))
                    assertEquals(JChipTokens.height, rule.onNodeWithTag("assistant-entry-$entry").getUnclippedBoundsInRoot().height)
                val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
                corners += shot("collapsed-$suffix")
                rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
                rule.onNodeWithTag("assistant-tray-pending").assertIsDisplayed()
                rule.onNodeWithText("Sent on your behalf · Only after you tap").assertIsDisplayed()
                shot("pending-$suffix")
                rule.onNodeWithTag("assistant-entry-progress").performClick(); settle()
                rule.onNodeWithTag("assistant-tray-pending").assertDoesNotExist()
                rule.onNodeWithTag("assistant-tray-progress").assertIsDisplayed()
                rule.onNodeWithText("3 / 5 steps").assertIsDisplayed()
                rule.onNodeWithText("Blocked").assertIsDisplayed()
                shot("progress-$suffix")
                rule.onNodeWithTag("assistant-entry-agents").performClick(); settle()
                rule.onNodeWithTag("assistant-tray-progress").assertDoesNotExist()
                rule.onNodeWithTag("assistant-agent-working").assertIsDisplayed()
                rule.onNodeWithTag("assistant-task-board").assertExists()
                shot("agents-$suffix")
                rule.onNodeWithTag("assistant-entry-agents").performClick(); settle()
                fixture.requests.value = emptyList(); fixture.drafts.value = emptyList(); settle()
                rule.onNodeWithTag("assistant-entry-pending").assertIsEnabled()
                rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
                rule.onNodeWithText("All caught up").assertIsDisplayed()
                shot("empty-pending-$suffix")
                rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
            }
            assertNotEquals("Dark images must not retain light pixels", corners[0], corners[1])
        }
        tab = "agents"; settle()
        rule.onNodeWithTag("assistant-tray-bar").assertDoesNotExist()
    }

    /** The #2920 entry this change replaced, kept here only as the "before" in the height comparison. */
    @Composable
    private fun ReplacedTallEntry(label: String, count: Int, tone: AssistantTrayTone) {
        val palette = PersonaTheme.palette
        val shape = androidx.compose.foundation.shape.RoundedCornerShape(PersonaTheme.shapes.control)
        Row(Modifier.heightIn(min = 44.dp).clip(shape).background(palette.surfaceStrong).padding(Tokens.Space.s),
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            AssistantTrayMark(tone)
            androidx.compose.material3.Text(label, style = PersonaTheme.type.labelSmall, color = palette.ink, maxLines = 1)
            if (count > 0) androidx.compose.material3.Text("$count", style = PersonaTheme.type.micro, color = palette.ink3)
            JIcon(PhIcons.CaretUp, null, size = Tokens.Space.m, tint = palette.ink3)
        }
    }

    @Test fun trayEntriesAreExactlyTheHistoricalFilterChips() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Column(Modifier.width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.m).testTag("assistant-page-shot"),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    val caption = PersonaTheme.type.micro
                    androidx.compose.material3.Text("Before #2920: All / task chips (JChip)", style = caption, color = PersonaTheme.palette.ink3)
                    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        JChip("All 4", Modifier.testTag("historical-all"), onClick = {})
                        JChip("Update the web interface", Modifier.testTag("historical-task"), onClick = {}, status = Status.Working)
                        JChip("Route", onClick = {}, selected = true, icon = PhIcons.PaperPlaneTilt)
                    }
                    androidx.compose.material3.Text("#2920: tall entries (replaced)", style = caption, color = PersonaTheme.palette.ink3)
                    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        Box(Modifier.testTag("replaced-entry")) { ReplacedTallEntry("Pending", 4, AssistantTrayTone.Pending) }
                        ReplacedTallEntry("Progress", 2, AssistantTrayTone.Progress)
                        ReplacedTallEntry("Agents", 3, AssistantTrayTone.Agents)
                    }
                    androidx.compose.material3.Text("Now: Pending / Progress / Agents as JChip", style = caption, color = PersonaTheme.palette.ink3)
                    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        AssistantTrayEntry("Pending", 4, AssistantTrayTone.Pending, false, "", {}, Modifier.testTag("entry-pending"))
                        AssistantTrayEntry("Progress", 2, AssistantTrayTone.Progress, true, "", {}, Modifier.testTag("entry-progress"))
                        AssistantTrayEntry("Agents", 3, AssistantTrayTone.Agents, false, "", {}, Modifier.testTag("entry-agents"))
                    }
                    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        AssistantTrayEntry("Pending", 0, AssistantTrayTone.Pending, false, "", {}, Modifier.testTag("entry-empty"))
                        AssistantTrayEntry("Progress", 0, AssistantTrayTone.Progress, false, "", {})
                        AssistantTrayEntry("Agents", 0, AssistantTrayTone.Agents, false, "", {})
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; settle()
            val historical = rule.onNodeWithTag("historical-all").getUnclippedBoundsInRoot().height
            assertEquals(JChipTokens.height, historical)
            assertEquals(historical, rule.onNodeWithTag("historical-task").getUnclippedBoundsInRoot().height)
            for (tag in listOf("entry-pending", "entry-progress", "entry-agents", "entry-empty"))
                assertEquals(tag, historical, rule.onNodeWithTag(tag).getUnclippedBoundsInRoot().height)
            assertEquals(44.dp, rule.onNodeWithTag("replaced-entry").getUnclippedBoundsInRoot().height)
            shot("height-comparison-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
    }

    @Test fun approveAllNeverSendsDraftsAndSendRequiresItsOwnTap() {
        val fixture = Fixture()
        rule.mainClock.autoAdvance = false
        rule.setContent { PersonaTheme(reduceMotion = true) {
            ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                chrome = ChatChromeBindings(tasks = fixture.tasks, inboxTrays = fixture.inbox, inboxScope = "fixture"),
                renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
        } }
        settle()
        rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
        rule.onNodeWithTag("assistant-approve-all").performClick(); settle()
        assertEquals(setOf("permission", "plan"), fixture.approvals.map { it.first }.toSet())
        assertTrue(fixture.sends.isEmpty())
        assertEquals(listOf(question), fixture.requests.value)
        rule.onNodeWithTag("assistant-draft-reply-item").assertIsDisplayed().performClick(); settle()
        assertEquals(listOf(inboxDraft), fixture.sends)
        fixture.requests.value = emptyList(); settle()
        rule.onNodeWithTag("assistant-tray-pending").assertDoesNotExist()
        rule.onNodeWithTag("assistant-entry-pending").assertIsEnabled()
    }

    @Test fun agentRowsRetainNavigationAndTheExistingTaskBoardAction() {
        val fixture = Fixture()
        rule.mainClock.autoAdvance = false
        rule.setContent { PersonaTheme(reduceMotion = true) {
            ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                chrome = ChatChromeBindings(tasks = fixture.tasks, onOpenSession = { fixture.opened += it }),
                renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
        } }
        settle()
        rule.onNodeWithTag("assistant-entry-agents").performClick(); settle()
        rule.onNodeWithTag("assistant-agent-working").performClick(); settle()
        assertEquals(listOf(running.key), fixture.opened)
        rule.onNodeWithTag("assistant-task-board").performClick(); settle()
        rule.mainClock.advanceTimeBy(1_500); settle()
        rule.onNode(isDialog()).assertExists()
    }

    @Test fun anExistingMainWithNoTasksAgentsOrPendingStillShowsThreeEntries() {
        val fixture = Fixture()
        fixture.returnedTodos = emptyList()
        fixture.sessionRows.value = fixture.sessionRows.value.filter { it.key == main }
        fixture.requests.value = emptyList(); fixture.drafts.value = emptyList()
        fixture.tasks.reloadTodos()
        rule.mainClock.autoAdvance = false
        rule.setContent { PersonaTheme(reduceMotion = true) {
            ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                chrome = ChatChromeBindings(tasks = fixture.tasks, inboxTrays = fixture.inbox, inboxScope = "fixture"),
                renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
        } }
        settle()
        for (entry in listOf("pending", "progress", "agents")) rule.onNodeWithTag("assistant-entry-$entry").assertIsDisplayed()
        rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
        rule.onNodeWithText("All caught up").assertIsDisplayed()
    }

    private val ownerDecision = Todo("decision", "Choose the release scope", origin = "main",
        supervision = Supervision(state = "needs_user"),
        ask = TodoAsk("decide", "The core change is verified. Decide whether to include the optional change.", "Defer the optional change", "2026-11-01T09:00:00Z"))

    private fun decisionFixture() = Fixture().apply {
        returnedTodos = listOf(ownerDecision)
        requests.value = emptyList(); drafts.value = emptyList()
        tasks.reloadTodos()
    }

    @Test @Config(qualifiers = "zh-w400dp-h900dp-xhdpi")
    fun directFinishInRealPendingTrayRendersSixStylesAndConfirmedCompletionRemovesTheCard() {
        val fixture = decisionFixture()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("assistant-page-shot")) {
                        ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                            chrome = ChatChromeBindings(tasks = fixture.tasks), renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
                    }
                }
            }
        }
        settle()
        rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
        for (s in AppStyle.entries) {
            val pixels = mutableListOf<Int>()
            for (d in listOf(false, true)) {
                style = s; dark = d; settle()
                rule.onNodeWithText("直接完成").assertIsDisplayed()
                rule.onNodeWithText("按建议处理").assertIsDisplayed()
                rule.onNodeWithText("打开").assertIsDisplayed()
                val finish = rule.onNodeWithText("直接完成").getUnclippedBoundsInRoot()
                val open = rule.onNodeWithText("打开").getUnclippedBoundsInRoot()
                val follow = rule.onNodeWithText("按建议处理").getUnclippedBoundsInRoot()
                assertTrue(open.left < finish.left && finish.left < follow.left)
                assertEquals(open.top, finish.top); assertEquals(finish.top, follow.top)
                pixels += shot("direct-finish-${s.name.lowercase()}-${if (d) "dark" else "light"}")
            }
            assertNotEquals("Light and dark must differ", pixels[0], pixels[1])
        }
        fixture.completionGate = CompletableDeferred()
        rule.onNodeWithText("直接完成").performClick(); settle()
        rule.onNodeWithText("直接完成").assertIsNotEnabled()
        rule.onNodeWithText("按建议处理").assertIsNotEnabled()
        rule.onNodeWithText("打开").assertIsNotEnabled()
        assertFalse(fixture.tasks.todos.value.single().isDone)
        assertEquals(listOf(ownerDecision.id to true), fixture.patches)
        assertTrue(fixture.accepted.isEmpty())
        fixture.completionGate!!.complete(Unit); settle()
        rule.onNodeWithTag("assistant-task-decision-decision").assertDoesNotExist()
        assertTrue(fixture.tasks.todos.value.single().isDone)
        assertNull(fixture.tasks.todos.value.single().doneBy)
        rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
        rule.onNodeWithText("都处理完了").assertIsDisplayed()
        shot("direct-finish-confirmed")
    }

    @Test @Config(qualifiers = "zh-w400dp-h900dp-xhdpi")
    fun directFinishFailureStaysVisibleAndRetrySucceedsInPendingTray() {
        val fixture = decisionFixture().apply { completionFailure = IllegalStateException("synthetic rejection") }
        rule.mainClock.autoAdvance = false
        rule.setContent { PersonaTheme(reduceMotion = true) {
            Box(Modifier.fillMaxSize().testTag("assistant-page-shot")) {
                ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                    chrome = ChatChromeBindings(tasks = fixture.tasks), renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
            }
        } }
        settle()
        rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
        rule.onNodeWithText("直接完成").performClick(); settle()
        rule.onNodeWithText("未能完成此任务，请重试。").assertIsDisplayed()
        rule.onNodeWithText("直接完成").assertIsEnabled()
        assertEquals(ownerDecision, fixture.tasks.todos.value.single())
        shot("direct-finish-failure")
        fixture.completionFailure = null
        rule.onNodeWithText("直接完成").performClick(); settle()
        assertTrue(fixture.tasks.todos.value.single().isDone)
        assertEquals(2, fixture.patches.size)
    }

    @Test @Config(qualifiers = "en-w320dp-h900dp-xhdpi")
    fun narrowDecisionKeepsThePrimaryAndMovesOpenAndFinishIntoTheMenu() {
        val fixture = decisionFixture()
        rule.mainClock.autoAdvance = false
        rule.setContent { PersonaTheme(reduceMotion = true) {
            ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                chrome = ChatChromeBindings(tasks = fixture.tasks), renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
        } }
        settle()
        rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
        rule.onNodeWithText("Follow recommendation").assertIsDisplayed()
        rule.onNodeWithText("Finish now").assertDoesNotExist()
        rule.onNodeWithContentDescription("More").performClick(); settle()
        rule.onNodeWithText("Open").assertIsDisplayed()
        rule.onNodeWithText("Finish now").performClick(); settle()
        assertEquals(listOf(ownerDecision.id to true), fixture.patches)
        assertTrue(fixture.accepted.isEmpty())
    }

    @Test @Config(qualifiers = "ja-w400dp-h900dp-xhdpi")
    fun japaneseDecisionShowsNaturalFinishLabelAndStillFollowsTheCapturedRecommendation() {
        val fixture = decisionFixture()
        rule.mainClock.autoAdvance = false
        rule.setContent { PersonaTheme(reduceMotion = true) {
            ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                chrome = ChatChromeBindings(tasks = fixture.tasks), renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
        } }
        settle()
        rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
        rule.onNodeWithText("ここで完了").assertIsDisplayed()
        rule.onNodeWithText("推奨に従う").performClick(); settle()
        assertEquals(listOf(ownerDecision.id to ownerDecision.ask!!.askedAt), fixture.accepted)
        assertTrue(fixture.patches.isEmpty())
    }

    @Test fun anAlreadyCompletedDoneConclusionRetainsTheOriginalAcceptanceAction() = completedConclusion("done")
    @Test fun anAlreadyCompletedCannotConclusionRetainsTheOriginalAcknowledgementAction() = completedConclusion("cannot")

    private fun completedConclusion(outcome: String) {
        val fixture = Fixture()
        val ask = TodoAsk(outcome = outcome, verdict = "Please acknowledge the completed conclusion.", askedAt = "2026-11-01T09:00:00Z")
        fixture.returnedTodos = listOf(todoRows.first().copy(ask = ask, doneAt = "2026-11-01T09:00:00Z"))
        fixture.tasks.reloadTodos()
        rule.mainClock.autoAdvance = false
        rule.setContent { PersonaTheme(reduceMotion = true) {
            ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                chrome = ChatChromeBindings(tasks = fixture.tasks), renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
        } }
        settle()
        rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
        rule.onNodeWithTag("assistant-review-todo:task-a").performScrollTo().performClick(); settle()
        rule.mainClock.advanceTimeBy(1_500); settle()
        rule.onNode(hasText(ask.verdict) and hasAnyAncestor(isDialog())).assertIsDisplayed()
        rule.onNodeWithText("Finish now").assertDoesNotExist()
        rule.onNodeWithTag("assistant-accept-task-a").performClick(); settle()
        assertEquals(listOf("task-a" to ask.askedAt), fixture.accepted)
        assertTrue(fixture.patches.isEmpty())
    }

    @Test fun anOwnerAskAppearsOnceAndReviewOpensTheRealAcceptanceAction() {
        val fixture = Fixture()
        val ask = TodoAsk(outcome = "done", verdict = "The new page is deployed. Please verify it.", askedAt = "2026-11-01T09:00:00Z")
        fixture.returnedTodos = todoRows.map { if (it.id == "task-a") it.copy(ask = ask) else it }
        fixture.tasks.reloadTodos()
        rule.mainClock.autoAdvance = false
        rule.setContent { PersonaTheme(reduceMotion = true) {
            ChatScreen(main, fixture.chats, fixture.sender, fixture.interactions, fixture.sessions, {},
                chrome = ChatChromeBindings(tasks = fixture.tasks), renderStyle = ChatRenderStyle.Assistant, showTopBar = false)
        } }
        settle()
        rule.onNodeWithTag("assistant-entry-pending").performClick(); settle()
        rule.onNodeWithTag("assistant-review-todo:task-a").performScrollTo().performClick(); settle()
        rule.mainClock.advanceTimeBy(1_500); settle()
        rule.onNode(hasText(ask.verdict) and hasAnyAncestor(isDialog())).assertIsDisplayed()
        rule.onNodeWithTag("assistant-accept-task-a").performClick(); settle()
        assertEquals(listOf("task-a" to ask.askedAt), fixture.accepted)
        assertNull(fixture.tasks.todos.value.first { it.id == "task-a" }.ask)
    }

    @Test fun aReusedTodoKeepsOnlyItsCurrentPlanningSlotAndRealCounts() {
        val fixture = Fixture()
        val current = todoRows.first().copy(planId = "new")
        val old = TaskPlan("old", "main", listOf(PlanTask(current.title, todoId = current.id)), state = "started", createdAt = "2026-11-01T08:00:00Z")
        val fresh = old.copy(id = "new", createdAt = "2026-11-01T10:00:00Z")
        val host = AssistantTaskHost(fixture.tasks, "main", fixture.sessionRows.value, { it }, {}, {}, {})
        val feed = listOf(FeedItem("plan", "main", kind = "plan", ref = "old"), FeedItem("old-receipt", "main", at = "2026-11-01T09:00:00Z", kind = "receipt", ref = current.id,
            body = buildJsonObject { put("pr", "Old PR #41") }))
        val projected = listOf(AssistantTaskCard("plan:old:0", current.id, "old", 0), AssistantTaskCard("plan:new:0", current.id, "new", 0))
        val rows = trayProgress(listOf(current), listOf(old, fresh), host, feed, projected)
        assertEquals(listOf("plan:new:0"), rows.map { it.id })
        assertNull(rows.single().pr)
        assertEquals(3, rows.single().completed)
        assertEquals(5, rows.single().total)
        val unknown = current.copy(agents = emptyList(), supervision = Supervision(progress = TaskProgress(text = "Checking")))
        assertNull(trayProgress(listOf(unknown), emptyList(), host).single().completed)
        val completed = current.copy(doneAt = "2026-11-01T11:00:00Z", supervision = Supervision(state = "accepted", progress = TaskProgress(text = "Stuck on old signing access"),
            reviews = listOf(TaskReview(verdict = "accepted", note = "The reviewed build is verified"))))
        val terminal = trayProgress(listOf(completed), emptyList(), host).single()
        assertTrue(terminal.done)
        assertEquals("The reviewed build is verified", terminal.text)
    }

    @Test fun liveTaskStateOverridesStaleCompletionAndKeepsTheCurrentPullRequest() {
        val fixture = Fixture()
        val host = AssistantTaskHost(fixture.tasks, "main", fixture.sessionRows.value, { it }, {}, {}, {})
        val current = todoRows.first().copy(prUrl = "https://github.com/example/project/pull/42",
            supervision = Supervision(state = "accepted"))
        val old = AssistantTaskCard("todo:${current.id}", todoId = current.id, state = "accepted")
        val reopened = trayProgress(listOf(current), emptyList(), host, projected = listOf(old)).single()
        assertFalse(reopened.done)
        assertEquals(current.prUrl, reopened.pr)
        assertEquals("42", trayPullRequestNumber(reopened.pr!!))
        assertFalse(trayProgress(listOf(current), emptyList(), host, projected = listOf(old.copy(state = "blocked"))).single().blocked)
        val cancelled = current.copy(doneAt = "2026-11-01T11:00:00Z", doneBy = "cancelled")
        val cancellation = trayProgress(listOf(cancelled), emptyList(), host, projected = listOf(old)).single()
        assertFalse(cancellation.done)
        assertTrue(cancellation.cancelled)
        val failed = current.copy(doneAt = "2026-11-01T11:00:00Z", startError = "Launcher unavailable")
        val failure = trayProgress(listOf(failed), emptyList(), host).single()
        assertFalse(failure.done)
        assertTrue(failure.blocked)
    }

    @Test fun draftProjectionPrefersVersionedInboxAndNeverOffersPlaceholdersOrFieldPermissionsForApproval() {
        val feed = FeedItem("feed", kind = "message", body = buildJsonObject { put("item_id", "reply-item"); put("draft", "Old draft") })
        assertEquals(listOf(TrayDraft.Inbox(inboxDraft)), trayDrafts(listOf(inboxDraft), listOf(feed)))
        assertNull(trayApproval(permission.copy(fields = listOf(InteractionField("password", "Password", secret = true))), running))
        assertNull(trayApproval(question, blocked))
        assertEquals(Resolution(approve = true, mode = "acceptEdits"), trayApproval(planRequest, blocked))
    }
}
