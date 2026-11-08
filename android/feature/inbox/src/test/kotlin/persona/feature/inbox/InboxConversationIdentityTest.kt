package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.*
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.*
import java.io.File

/** Exercises the production grouped renderer, with distinct conversation, sender and message identities. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class InboxConversationIdentityTest {
    @get:Rule val rule = createComposeRule()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    private fun conversation(group: Boolean, multiple: Boolean = false): InboxConversationRow {
        fun record(id: String, recent: Boolean) = InboxRecord(
            InboxItem(id, kind = if (group) "group" else "dm", title = if (recent) "Recent event" else "Older event",
                sender = if (recent) "Recent sender" else "Older sender", preview = if (recent) "Recent preview" else "Older preview",
                updatedAt = if (recent) "2026-11-08T14:00:00Z" else "2026-11-08T13:00:00Z", tier = "action"),
            feedId = "synthetic-feed", source = "lark", isGroup = group, groupName = if (group) "Planning room" else null,
        )
        val records = if (multiple) listOf(record("older", false), record("recent", true)) else listOf(record("recent", true))
        return buildInboxConversations("synthetic-scope", InboxSnapshot(records = records)).rows.single()
            .copy(title = if (group) "Planning room" else "Direct conversation")
    }
    private fun content(row: InboxConversationRow) {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                InboxConversationItemRow(row, navigator, GroupPosition.Single, null, false, false, {}, {})
            }
        }
    }
    @Test fun groupTagSurvivesConversationGrouping() {
        content(conversation(true, true))
        rule.onNodeWithText("Group", useUnmergedTree = true).assertExists()
    }
    @Test fun directTagSurvivesConversationGrouping() {
        content(conversation(false, true))
        rule.onNodeWithText("Direct", useUnmergedTree = true).assertExists()
    }
    @Test fun senderIsNotReplacedByTheConversationTitle() {
        content(conversation(true, true))
        rule.onNodeWithText("Recent sender", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Older sender", useUnmergedTree = true).assertDoesNotExist()
    }
    @Test fun titleSenderAndPreviewStayOnDistinctLinesForBothKindsAndMultipleEventsInEveryStyle() {
        var row by mutableStateOf(conversation(true))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.width(380.dp).background(PersonaTheme.palette.background).testTag("identity-row")) {
                    InboxConversationItemRow(row, navigator, GroupPosition.Single, null, false, false, {}, {})
                }
            }
        }
        for (group in listOf(true, false)) for (multiple in listOf(false, true)) {
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                rule.runOnIdle { row = conversation(group, multiple); style = s; dark = d }
                rule.waitForIdle()
                val title = rule.onNodeWithText(row.title, useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                val sender = rule.onNodeWithText("Recent sender", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                val preview = rule.onNodeWithText("Recent preview", useUnmergedTree = true).fetchSemanticsNode().boundsInRoot
                assertTrue("Conversation, sender and message need separate lines", title.bottom <= sender.top && sender.bottom <= preview.top)
                rule.onNodeWithText(if (group) "Group" else "Direct", useUnmergedTree = true).assertExists()
                rule.onNodeWithText("Older sender", useUnmergedTree = true).assertDoesNotExist()
                val dir = File("build/reports/tests/inbox-identity-shots").apply { mkdirs() }
                val name = "${if (group) "group" else "direct"}-${if (multiple) "multiple" else "single"}-${s.name}-${if (d) "dark" else "light"}.png"
                File(dir, name).outputStream().use {
                    assertTrue(rule.onNodeWithTag("identity-row").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
                }
            }
        }
    }
    @Test fun currentConversationNameIsNotReplacedByAnOlderCachedGroupName() {
        val row = conversation(true).let { it.copy(records = it.records.map { record -> record.copy(groupName = "Old cached name") }) }
        content(row)
        rule.onNodeWithText("Planning room", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Old cached name", useUnmergedTree = true).assertDoesNotExist()
    }
    @Test fun explicitChatKindWinsOverMentionKindAndMissingSenderDoesNotInventAnIdentity() {
        var row by mutableStateOf(conversation(false).let { it.copy(records = it.records.map { r -> r.copy(item = r.item.copy(kind = "mention")) }) })
        rule.setContent { PersonaTheme { InboxConversationItemRow(row, navigator, GroupPosition.Single, null, false, false, {}, {}) } }
        rule.onNodeWithText("Direct", useUnmergedTree = true).assertExists()
        rule.onNodeWithText("Group", useUnmergedTree = true).assertDoesNotExist()
        rule.runOnIdle { row = row.copy(records = row.records.map { it.copy(item = it.item.copy(sender = null)) }) }
        rule.onNodeWithText("Recent sender", useUnmergedTree = true).assertDoesNotExist()
        rule.onAllNodesWithText("Direct conversation", useUnmergedTree = true).assertCountEquals(1)
        rule.onNodeWithText("Recent preview", useUnmergedTree = true).assertExists()
        for ((kind, label) in listOf("group" to "Group", "dm" to "Direct")) {
            rule.runOnIdle { row = row.copy(records = row.records.map { it.copy(isGroup = null, item = it.item.copy(kind = kind)) }) }
            rule.onNodeWithText(label, useUnmergedTree = true).assertExists()
        }
        rule.runOnIdle { row = row.copy(records = row.records.map { it.copy(item = it.item.copy(kind = "unknown")) }) }
        rule.onNodeWithText("Group", useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithText("Direct", useUnmergedTree = true).assertDoesNotExist()
    }
}
