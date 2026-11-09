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
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Injected
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.designsystem.*
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.ChatRow
import java.io.File

/**
 * Persona's messages for the model (docs/protocol/protocol.md "Messages Persona injects") as centered lines, never their words: a
 * task's notice from an older hub (read off the text) and a scheduled run the hub marked, in every style, light and
 * dark. PNGs in the module's `build/injected-shots/`; these are native JVM component images, not device captures.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h800dp-xhdpi")
class InjectedLineShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/injected-shots").also { it.mkdirs() }

    private val notice = Item("n#0", seq = 1, role = "user", kind = "text",
        text = "🔔 任务「整理发布说明」做完了（MacBook）\n（这是 Persona 的任务通知，不是用户发的消息。这个任务是从主对话派出去的……）\n\n" +
            "Agent 最后说的：\n整理好了。\n\n你来决定要不要告诉用户、怎么说……")
    private val run = Item("r#0", seq = 3, role = "user", kind = "text", text = "⏰ 午间整理（定时任务）\n\n看看上午的邮件。",
        injected = Injected("followup", "daily", "午间整理", line = "定时任务「午间整理」开始运行", detail = "看看上午的邮件。"))
    private val cut = Item("c#0", seq = 5, role = "user", kind = "text", text = "🔔 任务「修登录页」被模型服务出错打断了\n（这是 Persona 的任务通知，……）")
    private val answer = Item("a#0", seq = 2, role = "assistant", kind = "text", text = "发布说明整理好了，在 docs/release-notes.md，等你过目。")

    @Test fun linesNotBubbles() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                    for ((type, item) in listOf("user" to notice, "reply" to answer, "user" to run, "user" to cut)) {
                        ChatLineContent(ChatLine.Row(ChatRow(type, item.asLineItem())), SessionKey("mac", "main"), loader,
                            recordedDuration = null, onToggle = {}, onFold = {}, folding = emptySet(), foldErrors = emptyMap(), onFullText = {})
                    }
                }
            }
        }
        rule.onNodeWithText("任务「整理发布说明」做完了").assertIsDisplayed()
        rule.onNodeWithText("@午间整理").assertIsDisplayed()
        rule.onNodeWithText("看看上午的邮件。").assertIsDisplayed()
        rule.onNodeWithText("任务「修登录页」被打断了").assertIsDisplayed()
        rule.onNodeWithText("这是 Persona 的任务通知", substring = true).assertDoesNotExist()
        rule.onNodeWithText("你来决定", substring = true).assertDoesNotExist()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "injected-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1600dp-xhdpi")
    @Test fun sourcesEn() = sourceShots("en", "@Routine check", "Show more", "Show less")

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1600dp-xhdpi")
    @Test fun sourcesZh() = sourceShots("zh", "@例行巡检", "展开", "收起")

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1600dp-xhdpi")
    @Test fun sourcesJa() = sourceShots("ja", "@定期チェック", "もっと見る", "折りたたむ")

    @Config(sdk = [35], qualifiers = "en-w400dp-h1200dp-xhdpi")
    @Test fun searchRevealsOnlyItsSourceAndPreservesManualFolding() {
        val body = "Opening report.\nSecond line.\nThird line.\nFourth line.\nNeedle below preview.\nReport end."
        val item = Item("search-source", role = "user", kind = "text", text = """<persona-context kind="message-source" title="">{"kind":"conversation","conversation_id":"cv_review","title":"Release review"}</persona-context>
$body""")
        val other = item.copy(id = "unselected-source", source = persona.core.contracts.MessageSource("conversation", "cv_review", "Release review"),
            text = "Unselected report.\nSecond line.\nThird line.\nFourth line.\nUnselected needle.\nUnselected end.")
        val rows = listOf(item, other).map { ChatLine.Row(ChatRow("user", it.asLineItem())) }
        val labels = sourceLabelIds(rows)
        val search = ChatSearchState()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val resources = androidx.test.core.app.ApplicationProvider.getApplicationContext<android.content.Context>().resources
        val hit = kotlinx.coroutines.runBlocking { searchChat(resources, listOf(item), "Needle below preview").hits.single() }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                Column(Modifier.testTag("search-source-shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                    verticalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(Tokens.Space.m)) {
                    rows.forEach { line ->
                        ChatSearchHighlight(search, line.id) {
                            ChatLineContent(line, SessionKey("computer", "conversation"), loader, null, {}, {}, emptySet(), emptyMap(),
                                onFullText = {}, showSourceLabel = line.id in labels)
                        }
                    }
                }
            }
        }
        fun layout(text: String): TextLayoutResult {
            val layouts = mutableListOf<TextLayoutResult>()
            rule.onNodeWithText(text, substring = true).performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(layouts) }
            return layouts.single()
        }
        org.junit.Assert.assertTrue(layout("Needle below preview").hasVisualOverflow)
        org.junit.Assert.assertTrue(layout("Unselected needle").hasVisualOverflow)
        rule.runOnIdle { search.choose(hit) }
        rule.waitForIdle()
        rule.onNodeWithText("Show less").assertIsDisplayed()
        org.junit.Assert.assertFalse("The selected fifth line must be visible", layout("Needle below preview").hasVisualOverflow)
        org.junit.Assert.assertTrue(layout("Needle below preview").lineCount > ContentTokens.sourcePreviewLines)
        org.junit.Assert.assertTrue("Other sources stay folded", layout("Unselected needle").hasVisualOverflow)
        rule.onNodeWithTag("source-label-search-source").assertIsDisplayed()
        rule.onNodeWithTag("source-label-unselected-source").assertDoesNotExist()
        rule.onNodeWithText("message-source", substring = true).assertDoesNotExist()
        val light = mutableMapOf<AppStyle, Bitmap>()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("search-source-shot").captureToImage().asAndroidBitmap()
            if (!d) light[s] = bitmap.copy(Bitmap.Config.ARGB_8888, false)
            else org.junit.Assert.assertFalse(bitmap.sameAs(light.getValue(s)))
            File(out(), "sources-search-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        rule.onNodeWithTag("source-toggle-search-source").performClick()
        org.junit.Assert.assertTrue("Manual collapse remains available during search", layout("Needle below preview").hasVisualOverflow)
        rule.runOnIdle { search.selected = null }
        rule.waitForIdle()
        org.junit.Assert.assertTrue(layout("Needle below preview").hasVisualOverflow)
        rule.runOnIdle { search.choose(hit) }
        rule.waitForIdle()
        org.junit.Assert.assertFalse("Selecting the source again reveals it", layout("Needle below preview").hasVisualOverflow)
    }

    private fun sourceShots(language: String, patrolLabel: String, expandLabel: String, collapseLabel: String) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val conversation = persona.core.contracts.MessageSource("conversation", "cv_review", "Release review")
        val sourceItems = listOf(
            Item("relay-1", role = "user", kind = "text", source = conversation, text = "The release notes are ready for review."),
            Item("relay-2", role = "user", kind = "text", source = conversation, text = "The remaining check is the changelog."),
            Item("patrol", role = "user", kind = "text", source = persona.core.contracts.MessageSource("patrol"),
                text = "Routine report\n" + (1..12).joinToString("\n") { "Check $it: the scheduled review completed successfully." } + "\nEND OF REPORT"),
            Item("followup", role = "user", kind = "text", injected = Injected("followup", "once", line = "The requested follow-up is due.")),
            Item("supervision", role = "user", kind = "text", injected = Injected("system", "supervise", line = "A progress check is waiting.")),
            Item("peer", role = "user", kind = "text", from = "Peer session", text = "The test results are ready."),
            Item("future", role = "user", kind = "text", source = persona.core.contracts.MessageSource("future"), text = "New source kinds still render safely."),
            Item("persisted", role = "user", kind = "text", text = """<persona-context kind="message-source" title="">{"kind":"supervision","title":"Persisted review"}</persona-context>
A progress check restored from history."""),
            Item("ordinary", role = "user", kind = "text", clientMsgId = "client_1", text = "Please summarize the results."),
        )
        val rows = sourceItems.map { ChatLine.Row(ChatRow("user", it.asLineItem())) }
        val labels = sourceLabelIds(rows)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                Column(Modifier.testTag("sources-shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                    verticalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(Tokens.Space.m)) {
                    rows.forEach { line ->
                        ChatLineContent(line, SessionKey("computer", "conversation"), loader, null, {}, {}, emptySet(), emptyMap(),
                            onFullText = {}, showSourceLabel = line.id in labels)
                    }
                }
            }
        }
        rule.onNodeWithTag("source-label-relay-1").assertIsDisplayed()
        rule.onNodeWithTag("source-label-relay-2").assertDoesNotExist()
        rule.onNodeWithTag("source-ordinary").assertDoesNotExist()
        rule.onNodeWithText("@Persisted review").assertIsDisplayed()
        rule.onNodeWithText("A progress check restored from history.").assertIsDisplayed()
        rule.onNodeWithText("message-source", substring = true).assertDoesNotExist()
        rule.onNodeWithText(patrolLabel).assertIsDisplayed()
        rule.onNodeWithText("Please summarize the results.").assertIsDisplayed()
        rule.onNodeWithText(expandLabel).assertIsDisplayed()
        val layouts = mutableListOf<TextLayoutResult>()
        rule.onNodeWithText("END OF REPORT", substring = true).performSemanticsAction(SemanticsActions.GetTextLayoutResult) { it(layouts) }
        org.junit.Assert.assertTrue("Long source starts folded", layouts.single().hasVisualOverflow)
        org.junit.Assert.assertTrue(layouts.single().lineCount <= ContentTokens.sourcePreviewLines)
        val images = mutableMapOf<AppStyle, Bitmap>()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("sources-shot").captureToImage().asAndroidBitmap()
            if (!d) images[s] = bitmap.copy(Bitmap.Config.ARGB_8888, false)
            else org.junit.Assert.assertFalse("Light and dark must differ for $s", bitmap.sameAs(images.getValue(s)))
            File(out(), "sources-$language-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        rule.onNodeWithTag("source-toggle-patrol").performClick()
        rule.onNodeWithText(collapseLabel).assertIsDisplayed()
        rule.waitForIdle()
        val expanded = rule.onNodeWithTag("sources-shot").captureToImage().asAndroidBitmap()
        File(out(), "sources-$language-expanded.png").outputStream().use { expanded.compress(Bitmap.CompressFormat.PNG, 100, it) }
        rule.onNodeWithTag("source-toggle-patrol").performClick()
        rule.onNodeWithText(expandLabel).assertIsDisplayed()
    }
}
