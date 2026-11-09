package persona.feature.agents

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Todo
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File
import java.time.Instant

/**
 * A done to-do offers no "Hand to agent" (#1856): the hub refuses every hand-off for it, so the page's bar has only Reopen and its
 * empty agent list says to reopen it first; an open one keeps both buttons. Every style, light and dark.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class TodoDoneHandOffShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/i18n-shots").also { it.mkdirs() }

    private val now = Instant.parse("2026-11-09T09:30:00Z")
    private val todos = listOf(
        Todo("open", title = "整理发布清单", createdAt = "2026-11-09T07:30:00Z"),
        Todo("done", title = "写发布说明", notes = "这周合并的 PR 都要提到", createdAt = "2026-11-09T04:30:00Z", doneAt = "2026-11-09T09:00:00Z", doneBy = "user"),
    )

    private fun has(text: String, substring: Boolean = true) =
        rule.onAllNodesWithText(text, substring = substring, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()

    private fun shot(id: String, style: AppStyle, dark: Boolean) {
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                    TodoDetailScreen(id, todos, emptyList(), emptyList(), null, onBack = {}, onOpenSession = {}, onHandOff = {}, now = now)
                }
            }
        }
        rule.waitForIdle()
        if (id == "done") {
            assertFalse("a done to-do offers Hand to agent", has("交给 Agent", substring = false))
            assertTrue(has("重新打开"))
            assertTrue(has("要交给 Agent，先重新打开。"))
        } else {
            assertTrue(has("交给 Agent", substring = false))
            assertTrue(has("点击底部「交给 Agent」"))
        }
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out(), "todo-handoff-$id-${style.name.lowercase()}-${if (dark) "dark" else "light"}.png").outputStream()
            .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun doneGlass() = shot("done", AppStyle.Glass, false)
    @Test fun doneGlassDark() = shot("done", AppStyle.Glass, true)
    @Test fun doneBrief() = shot("done", AppStyle.Brief, false)
    @Test fun doneBriefDark() = shot("done", AppStyle.Brief, true)
    @Test fun doneBoard() = shot("done", AppStyle.Board, false)
    @Test fun doneBoardDark() = shot("done", AppStyle.Board, true)
    @Test fun openGlass() = shot("open", AppStyle.Glass, false)
    @Test fun openBoardDark() = shot("open", AppStyle.Board, true)
}
