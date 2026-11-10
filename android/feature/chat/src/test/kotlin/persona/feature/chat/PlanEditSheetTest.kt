package persona.feature.chat

import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.PlanTask
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.PersonaTheme
import persona.core.domain.tasks.AssistantExtra
import persona.core.domain.tasks.TasksStore

/** 开始 in 改一下 closes the sheet only once the plan started; a failed start keeps the sheet and its edits (#5312). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "zh")
class PlanEditSheetTest {
    @get:Rule val rule = createComposeRule()

    private val plan = TaskPlan("plan1", "main", listOf(
        PlanTask("写发布说明", machine = "mac", cwd = "/w/shop", prompt = "Write the release notes"),
    ), state = "open")

    private class Api(val plan: TaskPlan, val fail: Boolean) : TasksApi {
        val sent = mutableListOf<List<PlanTask>?>()
        override suspend fun todos() = emptyList<Todo>()
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList(), mapOf(plan.id to plan))
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = startPlan(id, remember, null)
        override suspend fun startPlan(id: String, remember: Boolean, tasks: List<PlanTask>?): TaskPlan {
            sent += tasks
            if (fail) throw IllegalStateException("task_no_machine")
            return plan.copy(tasks = tasks ?: plan.tasks, state = "started")
        }
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
        override fun send(message: JsonObject): Boolean = true
    }

    /** Shows the plan's card. */
    private fun showCard(api: Api, errors: MutableList<Throwable>) {
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        store.load("main")
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                val host = AssistantTaskHost(store, "main", emptyList(), { it }, {}, { errors += it }, {})
                AssistantExtraLine(AssistantExtra.Feed(FeedItem("f1", "main", kind = "plan", ref = "plan1")), host)
            }
        }
    }

    /** Opens 改一下 on the plan's card, renames its task to "Write the changelog" and taps 开始 in the sheet. */
    private fun editAndStart(api: Api, errors: MutableList<Throwable>) {
        showCard(api, errors)
        rule.onNodeWithText("改一下").performClick()
        rule.onNode(hasSetTextAction() and hasText("写发布说明")).performTextReplacement("Write the changelog")
        rule.onNode(hasText("开始") and hasAnyAncestor(isDialog())).performClick()
        rule.waitForIdle()
        assertEquals("Write the changelog", api.sent.single()?.single()?.title)
    }

    @Test fun aFailedStartKeepsTheSheetAndItsEdits() {
        val errors = mutableListOf<Throwable>()
        editAndStart(Api(plan, fail = true), errors)
        assertEquals("task_no_machine", errors.single().message)
        rule.onNode(isDialog()).assertExists()
        rule.onNode(hasSetTextAction() and hasText("Write the changelog")).assertExists()
    }

    /** Auto is a mode the card names and the sheet offers: the hub may settle an unset one from Claude Code's default mode (#3026). */
    @Test fun autoIsNamedOnTheCardAndOfferedInTheSheet() {
        val auto = Api(plan.copy(tasks = plan.tasks.map { it.copy(permission = "auto") }), fail = false)
        showCard(auto, mutableListOf())
        rule.onNodeWithText("自动判断").assertExists()
        rule.onNodeWithText("改一下").performClick()
        rule.onNode(hasText("每步都问") and hasAnyAncestor(isDialog())).performClick()
        rule.onNode(hasText("自动判断") and hasAnyAncestor(isDialog())).performClick()
        rule.onNode(hasText("开始") and hasAnyAncestor(isDialog())).performClick()
        rule.waitForIdle()
        assertEquals("auto", auto.sent.single()?.single()?.permission)
    }

    @Test fun aStartedPlanClosesTheSheet() {
        val errors = mutableListOf<Throwable>()
        editAndStart(Api(plan, fail = false), errors)
        assertEquals(emptyList<Throwable>(), errors)
        rule.onNode(isDialog()).assertDoesNotExist()
    }
}
