package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onLast
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.CoreText
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubManager
import persona.core.contracts.api.InboxEntry
import persona.core.contracts.api.ManagerRoster
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalManagerRoster
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxGroup
import persona.core.domain.inbox.InboxItem
import persona.core.domain.inbox.InboxRow
import persona.core.domain.inbox.InboxRowType
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File
import java.time.Instant

/**
 * The inbox with several managers (docs/assistant/managers.md step 4): each manager's items under its name with its own
 * sections, the default's first (unstamped items are its), the offline one greyed with 最后同步于, in every style,
 * light and dark. Written to `build/managers-shots/inbox-*.png`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
// Tall enough for the last manager's rows under the grouping switch (#2576): a lazy list builds only what is on screen.
@Config(sdk = [35], qualifiers = "zh-w400dp-h1400dp-xhdpi")
class InboxManagersShotsTest {
    @get:Rule val rule = createComposeRule()

    private val roster = ManagerRoster(listOf(
        HubManager("assistant", "助手", online = true, isDefault = true, builtin = true),
        HubManager("m-3fa21c", "工作", online = true),
        HubManager("m-8b04d2", "家里", online = false, lastSeen = Instant.now().minusSeconds(3 * 3600).toString()),
        HubManager("m-c1e9f0", "测试", installing = "2026-11-05T06:07:00Z"),
    ), "assistant")

    private val records = listOf(
        InboxRecord(InboxItem("mail:a1:t:7", kind = "mail", title = "周报", sender = "示例同事", preview = "这周的进展和下周的计划，见附件。",
            tier = "action", updatedAt = "2026-11-04T09:30:00Z"), sourceLabel = "邮件", source = "mail"),
        InboxRecord(InboxItem("lark:f2:120", kind = "group", title = "项目群", sender = "示例同事", preview = "评审改到周四下午了",
            tier = "fyi", updatedAt = "2026-11-04T08:10:00Z"), sourceLabel = "飞书", feedId = "f2", count = 2, source = "lark", manager = "m-3fa21c"),
        InboxRecord(InboxItem("mail:a2:t:9", kind = "mail", title = "发布检查", sender = "示例同事", preview = "上线前还差两项检查",
            tier = "action", updatedAt = "2026-11-04T07:00:00Z"), sourceLabel = "邮件", source = "mail", manager = "m-3fa21c"),
        InboxRecord(InboxItem("mail:a3:t:2", kind = "mail", title = "物业通知", sender = "示例物业", preview = "本周六停水半天",
            tier = "fyi", updatedAt = "2026-11-04T06:00:00Z"), sourceLabel = "邮件", source = "mail", manager = "m-8b04d2"),
    )

    private class OneInbox(snapshot: InboxSnapshot) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private fun top(text: String) = rule.onAllNodesWithText(text)[0].fetchSemanticsNode().boundsInRoot.top

    @Test fun inboxGroupsByManager() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shown by mutableStateOf<ManagerRoster?>(roster)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalManagerRoster provides shown) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                        InboxScreen(OneInbox(InboxSnapshot(records = records)), navigator)
                    }
                }
            }
        }
        rule.waitForIdle()
        assertTrue(top("助手") < top("周报") && top("周报") < top("工作"))
        assertTrue(top("工作") < top("发布检查") && top("发布检查") < top("项目群") && top("项目群") < top("家里"))
        assertTrue(top("家里") < top("物业通知"))
        assertTrue(rule.onAllNodesWithText("最后同步于 3 小时前").fetchSemanticsNodes().isNotEmpty())
        assertEquals(0, rule.onAllNodesWithText("测试").fetchSemanticsNodes().size)
        val dir = File(System.getProperty("persona.shots") ?: "build/managers-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(dir, "inbox-grouped-${s.key}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        shown = ManagerRoster(listOf(roster.managers.first()), "assistant")
        rule.waitForIdle()
        assertEquals(0, rule.onAllNodesWithText("助手").fetchSemanticsNodes().size)
    }

    /** Each manager's own rules and mutes: 工作's and the default's, 家里 offline. */
    private class ManagerLists : InboxRepository by OneInbox(InboxSnapshot()) {
        override suspend fun inboxPolicies(record: InboxRecord?, manager: String?) = when (manager) {
            "m-8b04d2" -> throw IllegalStateException("503")
            "m-3fa21c" -> listOf(InboxPolicy("p1", "mail", "邮件", "sender", "boss@example.com", "王经理", InboxPolicyTier.Action, InboxPolicyTwin.First,
                "2026-11-04T09:00:00Z"))
            else -> listOf(InboxPolicy("p1", "github", "GitHub", "repo", "example-org/docs", "example-org/docs", InboxPolicyTier.Noise, createdAt = "2026-11-03T09:00:00Z"))
        }
        override suspend fun inboxMutes(manager: String?) = when (manager) {
            "m-8b04d2" -> throw IllegalStateException("503")
            "m-3fa21c" -> listOf(InboxMute("m1", "github", "GitHub", "repo", "example-org/example-repo", "example-org/example-repo", "2026-11-04T09:00:00Z"))
            else -> listOf(InboxMute("m1", "mail", "邮件", "sender", "news@example.com", "news@example.com", "2026-11-03T09:00:00Z"))
        }
    }

    @Test fun rulesAndMutedGroupByManager() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var rules by mutableStateOf(true)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalManagerRoster provides roster) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        if (rules) InboxRulesSheet(ManagerLists(), onDismiss = {}) else InboxMutesSheet(ManagerLists(), onDismiss = {})
                    }
                }
            }
        }
        val dir = File(System.getProperty("persona.shots") ?: "build/managers-shots").also { it.mkdirs() }
        for (r in listOf(true, false)) {
            rules = r
            rule.waitForIdle()
            val (mine, theirs, away) = if (r) Triple("example-org/docs", "王经理", "离线中：它回来后这里会显示它的规则。")
                else Triple("news@example.com", "example-org/example-repo", "离线中：它回来后这里会显示它的静音。")
            assertTrue(top("助手") < top(mine) && top(mine) < top("工作") && top("工作") < top(theirs))
            assertTrue(top(theirs) < top("家里") && top("家里") < top(away))
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                style = s; dark = d
                rule.waitForIdle()
                val bitmap = rule.onAllNodes(isDialog()).onLast().captureToImage().asAndroidBitmap()
                File(dir, "inbox-${if (r) "rules" else "muted"}-${s.key}-${if (d) "dark" else "light"}.png").outputStream()
                    .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            }
        }
    }

    @Test fun sectionsSplitByManagerInRosterOrder() {
        fun row(id: String) = InboxRow(InboxRowType.Item, id)
        val groups = listOf(
            InboxGroup("action", CoreText.plain("需要你"), listOf(row("a"), row("b"), row("c")), listOf(row("a"), row("b")), false),
            InboxGroup("fyi", CoreText.plain("知悉"), listOf(row("d")), listOf(row("d")), false),
        )
        val owners = mapOf("a" to "m-8b04d2", "b" to null, "c" to "m-8b04d2", "d" to "m-8b04d2")
        val split = requireNotNull(inboxByManager(roster, groups) { owners[it.id] })
        assertEquals(listOf("assistant", "m-8b04d2"), split.map { it.first.id })
        assertEquals(listOf("action"), split[0].second.map { it.id })
        assertEquals(listOf(row("b")), split[0].second[0].rows)
        assertEquals(listOf(listOf(row("a"), row("c")), listOf(row("d"))), split[1].second.map { it.rows })
        // The fold stays: c was folded away, it still is.
        assertEquals(listOf(row("a")), split[1].second[0].visibleRows)
        assertNull(inboxByManager(ManagerRoster(listOf(roster.managers.first()), "assistant"), groups) { owners[it.id] })
        assertNull(inboxByManager(null, groups) { owners[it.id] })
    }
}
