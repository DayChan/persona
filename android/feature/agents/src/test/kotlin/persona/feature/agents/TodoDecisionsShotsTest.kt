package persona.feature.agents

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.SemanticsNodeInteraction
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.TodoDecision
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.tasks.TodoDecisionsSource
import java.io.File
import java.time.Instant

/**
 * A to-do's Decisions on Android (#1853), as iOS and the web show them: a question nobody answered that became a to-do is
 * answered here (Add records the decision that goes back to the conversation that asked), the current ones with what was
 * chosen, why, when and where from, the revised ones struck through on request, and the Record / Revise form. Every style,
 * light and dark; the form in English and Japanese too.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1400dp-xhdpi")
class TodoDecisionsShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    private val now = Instant.parse("2026-11-09T09:30:00Z")
    private fun ago(minutes: Long) = now.minusSeconds(minutes * 60).toString()
    private val machines = listOf(Machine("mac", "工作笔记本", online = true))
    private val sessions = listOf(Session("mac", "s1", title = "发布检查", cwd = "/code/app", state = "idle", updatedAt = ago(30)))
    private val asked = Todo("todo_ask_0a1b2c", title = "现在合并发布 PR 吗？",
        notes = "11-09 15:04 在「发布」对话里问的。\n\n选项：\n- 合并（推荐）：CI 全绿\n- 再等等：等评审\n\n在这条待办里记下你的决定，决定会发回那段对话，待办随后关闭。",
        createdAt = ago(150), updatedAt = ago(30))
    private val task = Todo("todo_1", title = "发登录修复", createdAt = ago(300), updatedAt = ago(20),
        agents = listOf(TodoAgent("mac", "s1", title = "发布检查", cwd = "/code/app")))
    private val todos = listOf(asked, task)

    private class Source(var list: List<TodoDecision>?) : TodoDecisionsSource {
        val added = mutableListOf<List<String>>()
        override suspend fun decisions(todoId: String) = list?.filter { it.todoId == todoId }
        override suspend fun addDecision(todoId: String, question: String, choice: String, reason: String, supersedesId: String): TodoDecision {
            added += listOf(todoId, question, choice, reason, supersedesId)
            val d = TodoDecision("dec_new", todoId, question, choice, reason, source = "manual", supersedesId = supersedesId,
                createdAt = "2026-11-09T09:29:00Z")
            list = list.orEmpty() + d
            return d
        }
    }

    private fun decisions() = listOf(
        TodoDecision("dec_1", "todo_1", "先修哪个崩溃？", "登录页那个", "用户反馈最多", source = "card", sourceMachine = "mac", sourceSession = "s1",
            supersededBy = "dec_3", createdAt = ago(240)),
        TodoDecision("dec_2", "todo_1", "要不要加回归测试？", "加，覆盖空密码", source = "agent", sourceMachine = "mac", sourceSession = "s1",
            createdAt = ago(200)),
        TodoDecision("dec_3", "todo_1", "先修哪个崩溃？", "启动时那个", "影响所有人", source = "manual", supersedesId = "dec_1", createdAt = ago(60)),
    )

    private fun page(id: String, source: Source, style: AppStyle, dark: Boolean) {
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    TodoDetailScreen(id, todos, machines, sessions, null, onBack = {}, onOpenSession = {}, onHandOff = {}, now = now, decisions = source)
                }
            }
        }
        rule.waitForIdle()
    }

    private fun has(text: String) = rule.onAllNodesWithText(text, substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()

    private fun save(node: SemanticsNodeInteraction, name: String) {
        val bitmap = node.captureToImage().asAndroidBitmap()
        File(out(), "todo-decisions-$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun shot(scene: String, style: AppStyle, dark: Boolean, expected: String) {
        val tag = "$scene-${java.util.Locale.getDefault().language}-${style.name.lowercase()}-${if (dark) "dark" else "light"}"
        when (scene) {
            "empty" -> page(asked.id, Source(emptyList()), style, dark)
            "list", "history" -> page(task.id, Source(decisions()), style, dark)
            "form" -> { page(asked.id, Source(emptyList()), style, dark); rule.onNodeWithTag("todo.decisions.add").performClick() }
            "revise" -> { page(task.id, Source(decisions()), style, dark); rule.onNodeWithTag("todo.decision.revise.dec_3").performScrollTo().performClick() }
        }
        if (scene == "history") rule.onNodeWithTag("todo.decisions.history").performScrollTo().performClick()
        rule.waitForIdle()
        assertTrue("$scene: no 「$expected」", has(expected))
        save(if (scene == "form" || scene == "revise") rule.onNode(isDialog()) else rule.onNodeWithTag("shot"), tag)
    }

    @Test fun emptyGlass() = shot("empty", AppStyle.Glass, false, "这件事上拍过的板记在这里")
    @Test fun emptyGlassDark() = shot("empty", AppStyle.Glass, true, "这件事上拍过的板记在这里")
    @Test fun emptyBrief() = shot("empty", AppStyle.Brief, false, "这件事上拍过的板记在这里")
    @Test fun emptyBriefDark() = shot("empty", AppStyle.Brief, true, "这件事上拍过的板记在这里")
    @Test fun emptyBoard() = shot("empty", AppStyle.Board, false, "这件事上拍过的板记在这里")
    @Test fun emptyBoardDark() = shot("empty", AppStyle.Board, true, "这件事上拍过的板记在这里")

    @Test fun listGlass() = shot("list", AppStyle.Glass, false, "看全历史（1 条已改判）")
    @Test fun listGlassDark() = shot("list", AppStyle.Glass, true, "看全历史（1 条已改判）")
    @Test fun listBrief() = shot("list", AppStyle.Brief, false, "看全历史（1 条已改判）")
    @Test fun listBriefDark() = shot("list", AppStyle.Brief, true, "看全历史（1 条已改判）")
    @Test fun listBoard() = shot("list", AppStyle.Board, false, "看全历史（1 条已改判）")
    @Test fun listBoardDark() = shot("list", AppStyle.Board, true, "看全历史（1 条已改判）")

    @Test fun historyGlass() = shot("history", AppStyle.Glass, false, "已被改判")
    @Test fun historyGlassDark() = shot("history", AppStyle.Glass, true, "已被改判")
    @Test fun historyBrief() = shot("history", AppStyle.Brief, false, "已被改判")
    @Test fun historyBriefDark() = shot("history", AppStyle.Brief, true, "已被改判")
    @Test fun historyBoard() = shot("history", AppStyle.Board, false, "已被改判")
    @Test fun historyBoardDark() = shot("history", AppStyle.Board, true, "已被改判")

    @Test fun formGlass() = shot("form", AppStyle.Glass, false, "记一条决策")
    @Test fun formGlassDark() = shot("form", AppStyle.Glass, true, "记一条决策")
    @Test fun formBrief() = shot("form", AppStyle.Brief, false, "记一条决策")
    @Test fun formBriefDark() = shot("form", AppStyle.Brief, true, "记一条决策")
    @Test fun formBoard() = shot("form", AppStyle.Board, false, "记一条决策")
    @Test fun formBoardDark() = shot("form", AppStyle.Board, true, "记一条决策")
    @Config(qualifiers = "en-w400dp-h1400dp-xhdpi") @Test fun formEnglish() = shot("form", AppStyle.Glass, false, "Record a decision")
    @Config(qualifiers = "ja-w400dp-h1400dp-xhdpi") @Test fun formJapanese() = shot("form", AppStyle.Glass, false, "決定事項を記録")

    @Test fun reviseGlass() = shot("revise", AppStyle.Glass, false, "原来选的「启动时那个」会保留")
    @Test fun reviseBoardDark() = shot("revise", AppStyle.Board, true, "原来选的「启动时那个」会保留")
    @Config(qualifiers = "en-w400dp-h1400dp-xhdpi") @Test fun listEnglish() = shot("list", AppStyle.Glass, false, "Full history (1 revised)")
    @Config(qualifiers = "ja-w400dp-h1400dp-xhdpi") @Test fun listJapanese() = shot("list", AppStyle.Brief, false, "全履歴（変更済み1件）")

    /**
     * Answering the question on Android: Add, the question and the choice typed, Record. The decision goes to the hub for this
     * to-do (the hub sends it back to the conversation that asked), the form closes and the decision is listed.
     */
    @Test fun answeringAQuestionToDoRecordsItsDecision() = runBlocking {
        val source = Source(emptyList())
        page(asked.id, source, AppStyle.Glass, false)
        rule.onNodeWithTag("todo.decisions.add").performClick()
        rule.waitForIdle()
        val fields = rule.onAllNodes(hasSetTextAction() and hasAnyAncestor(isDialog()))
        fields[0].performTextInput("现在合并发布 PR 吗？")
        fields[1].performTextInput("合并")
        fields[2].performTextInput("CI 全绿")
        rule.onNodeWithTag("todo.decision.save").performClick()
        rule.waitForIdle()
        assertEquals(listOf(listOf(asked.id, "现在合并发布 PR 吗？", "合并", "CI 全绿", "")), source.added)
        assertTrue("the form closed", rule.onAllNodes(isDialog()).fetchSemanticsNodes().isEmpty())
        assertTrue("listed", has("选了：合并"))
        assertTrue("by hand", has("手动记的"))
    }
}
