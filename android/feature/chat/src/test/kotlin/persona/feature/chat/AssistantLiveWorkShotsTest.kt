package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onFirst
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.designsystem.*
import persona.core.domain.lines.ChatLine
import java.io.File
import java.time.Instant

/**
 * #1614: in Persona's own chat the running work line lists every tool call as it arrives (name, short input, state),
 * folds to its summary when the turn ends, and opens again on a tap with all of them. A fold during the run holds until
 * the end. The lines come from `ChatTimeline` and the taps from [toggleWork] / [settleLiveFolds], as on the chat page.
 * Every style, light and dark; PNGs in `build/assistant-live-work-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1200dp-xhdpi")
class AssistantLiveWorkShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/assistant-live-work-shots").also { it.mkdirs() }

    private val start = Instant.now().minusSeconds(20)
    private fun at(seq: Int) = start.plusSeconds(seq.toLong()).toString()
    private fun use(seq: Int, name: String, vararg input: Pair<String, String>) = Item("t$seq", seq, role = "assistant",
        kind = "tool_use", toolUseId = "t$seq", toolName = name, toolInput = buildJsonObject { input.forEach { (k, v) -> put(k, v) } },
        ts = at(seq))
    private fun result(seq: Int, of: Int, failed: Boolean = false) = Item("r$seq", seq, role = "user", kind = "tool_result",
        toolUseId = "t$of", text = if (failed) "not found" else "ok", isError = failed.takeIf { it }, ts = at(seq))

    private val running = listOf(
        Item("u1", 1, role = "user", kind = "text", text = "看看明天的日程，顺便整理一下待办", ts = at(1)),
        use(2, "Bash", "command" to "personactl calendar --day tomorrow"), result(3, 2),
        use(4, "Read", "file_path" to "/notes/todo.md"), result(5, 4),
        use(6, "Grep", "pattern" to "deadline"), result(7, 6, failed = true),
        use(8, "Edit", "file_path" to "/notes/todo.md", "old_string" to "a", "new_string" to "b"),
    )
    private val ended = running + result(9, 8) +
        Item("a10", 10, role = "assistant", kind = "text", text = "明天有两个会，待办已经按日期排好。", ts = at(10))

    @Test fun theRunningLineListsItsCallsThenFoldsWhenTheTurnEnds() {
        var items by mutableStateOf(running)
        var live by mutableStateOf(true)
        var state by mutableStateOf(WorkOpenState())
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        // A fresh timeline for each build, as after the history reloads: the open state is all in [state].
        fun lines() = ChatTimeline().lines(items, live, state.open, openLive = true, liveFolded = state.liveFolded).lines
        rule.mainClock.autoAdvance = false // the clock and the spinner never settle
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                val lines = remember(items, live, state) { lines() }
                val groups = lines.filterIsInstance<ChatLine.Work>().associateBy { it.id }
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                    for (line in lines) ChatLineContent(line, SessionKey("mac", "persona"), loader, recordedDuration = 9,
                        onToggle = { id -> state = toggleWork(state, id, id in liveWorkIds(lines), openLive = true).first },
                        onFold = {}, folding = emptySet(), foldErrors = emptyMap(), onFullText = {},
                        style = ChatRenderStyle.Assistant,
                        workRunning = (line as? ChatLine.Step)?.let { groups[it.groupId]?.live } == true,
                        lastWorkStep = (line as? ChatLine.Step)?.let { groups[it.groupId]?.steps?.lastOrNull()?.id == it.step.id } == true)
                }
            }
        }
        fun settle() { Snapshot.sendApplyNotifications(); repeat(2) { rule.mainClock.advanceTimeByFrame() } }
        fun shoot(name: String) = AppStyle.entries.forEach { s -> listOf(false, true).forEach { d ->
            style = s; dark = d; settle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "assistant-live-work-$name-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        } }
        fun stepsShown() = rule.onNodeWithText("deadline", substring = true).assertIsDisplayed()
        fun stepsHidden() = rule.onNodeWithText("deadline", substring = true).assertDoesNotExist()
        // The work line's header: the one button saying it is open or folded.
        fun summary() = rule.onNode(hasClickAction() and (hasContentDescription("，已展开", substring = true) or
            hasContentDescription("，已收起", substring = true)))
        fun open(expected: Boolean) = summary().assert(hasContentDescription(if (expected) "，已展开" else "，已收起", substring = true))
        settle()

        // Running: open on its own, every call its own line.
        open(true)
        stepsShown()
        rule.onNodeWithText("calendar", substring = true).assertIsDisplayed()
        rule.onAllNodesWithText("todo.md", substring = true).onFirst().assertIsDisplayed()
        shoot("running")

        // Folded by the user while it runs: stays folded while it runs.
        summary().performClick(); settle()
        stepsHidden()
        items = running + result(9, 8); settle()
        stepsHidden()

        // The turn ends: what was tapped is dropped and the line folds to its summary.
        items = ended; live = false
        state = state.copy(liveFolded = settleLiveFolds(state.liveFolded, liveWorkIds(lines()))); settle()
        check(state.liveFolded.isEmpty())
        open(false)
        stepsHidden()
        shoot("ended")

        // A tap opens it with all of the turn's calls.
        summary().performClick(); settle()
        open(true)
        stepsShown()
        rule.onNodeWithText("calendar", substring = true).assertIsDisplayed()
        shoot("reopened")
    }

    /** Never folded by hand, the running line folds at the end all the same; the Agents chat keeps it folded throughout. */
    @Test fun theLineFoldsAtTheEndAndTheAgentsChatIsUnchanged() {
        val open = ChatTimeline().lines(running, live = true, open = emptySet(), openLive = true).lines
        check(open.filterIsInstance<ChatLine.Step>().size == 4)
        val done = ChatTimeline().lines(ended, live = false, open = emptySet(), openLive = true).lines
        check(done.none { it is ChatLine.Step })
        val agents = ChatTimeline().lines(running, live = true, open = emptySet()).lines
        check(agents.none { it is ChatLine.Step })
    }
    /**
     * Opened mid-run, the hub sends the earlier calls as a fold: the line, open by default, asks for it on its own
     * ([liveFoldsToFetch]), and once loaded every call of the turn is a row; folded by the user, it asks for nothing.
     */
    @Test fun aChatOpenedMidRunListsTheCallsTheHubFolded() {
        val fold = Item("fold:t2", 2, role = "assistant", kind = "fold",
            fold = persona.core.contracts.Fold(items = 6, tools = 3, throughSeq = 7, throughId = "r7"))
        val items = listOf(running.first(), fold) + running.filter { it.seq >= 8 }
        val timeline = ChatTimeline()
        val before = timeline.lines(items, live = true, open = emptySet(), openLive = true).lines
        check(before.filterIsInstance<ChatLine.Step>().any { it.step is persona.core.domain.lines.WorkItem.Folded })
        val fetch = liveFoldsToFetch(before, openLive = true, folding = emptySet(), failed = emptySet())
        check(fetch.map { it.id } == listOf(fold.id))
        val id = before.filterIsInstance<ChatLine.Work>().single().id
        val folded = timeline.lines(items, live = true, open = emptySet(), openLive = true, liveFolded = setOf(id)).lines
        check(liveFoldsToFetch(folded, openLive = true, folding = emptySet(), failed = emptySet()).isEmpty())
        timeline.unfold(fold, running.filter { it.seq in 2..7 })
        val after = timeline.lines(items, live = true, open = emptySet(), openLive = true).lines
        val steps = after.filterIsInstance<ChatLine.Step>()
        check(steps.none { it.step is persona.core.domain.lines.WorkItem.Folded })
        check(steps.count { it.step is persona.core.domain.lines.WorkItem.Tool } == 4)
        check(liveFoldsToFetch(after, openLive = true, folding = emptySet(), failed = emptySet()).isEmpty())
    }
}
