package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.LocalRippleConfiguration
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import java.time.temporal.ChronoUnit
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxBulkAction
import persona.core.contracts.api.InboxDoneTarget
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.inbox.InboxInteraction
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator

/**
 * The inbox's groups, Done's history and selection on Android (#2576, the design of #2059): Needs you and FYI by source
 * (the agents' cards first, Lark, each mail account, a connector) or by time, Done by when it was done with older pages
 * from the hub's history and "Jump to", and a long-press selection with group ticks that marks the picked items done in
 * one request. Robolectric's native graphics in every style, light and dark; PNGs in build/inbox-groups-shots/. Mock data.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h1500dp-xhdpi")
class InboxGroupsShotsTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.now().truncatedTo(ChronoUnit.SECONDS)
    private fun ago(minutes: Long) = now.minus(minutes, ChronoUnit.MINUTES).toString()
    private fun days(n: Long) = n * 24 * 60

    private fun lark(id: String, title: String, sender: String, preview: String, tier: String, at: String, doneAt: String? = null) =
        InboxRecord(InboxItem("lark:$id", account = "feishu", kind = "dm", title = title, sender = sender, preview = preview, tier = tier,
            updatedAt = at, doneAt = doneAt, version = "v-$id"), sourceLabel = "飞书", source = "lark")
    private fun mail(id: String, account: String, label: String, title: String, sender: String, tier: String, at: String, doneAt: String? = null) =
        InboxRecord(InboxItem("mail:$id", account = account, kind = "mail", title = title, sender = sender, preview = "示例邮件的正文摘要。",
            tier = tier, updatedAt = at, doneAt = doneAt, version = "v-$id"), sourceLabel = label, source = "mail")

    private val open = listOf(
        lark("c1", "周五发版前要确认的三件事", "示例同事甲", "测试报告、发布说明和回滚方案还差一个", "action", ago(20)),
        mail("m1", "acc1", "me@example.com", "合同第二版的修改意见", "示例律师", "action", ago(120)),
        mail("m2", "acc2", "work@example.com", "下周评审的时间", "示例经理", "action", ago(days(1))),
        mail("m3", "acc2", "work@example.com", "预算表需要你签字", "示例财务", "action", ago(days(1) + 30)),
        InboxRecord(InboxItem("gh:p1", account = "gh", kind = "pull", title = "#128 修复登录页的崩溃", sender = "example-bot",
            preview = "CI 通过，等你审阅", tier = "action", updatedAt = ago(days(3)), version = "v-p1"), sourceLabel = "GitHub", source = "github",
            connector = InboxConnectorRef("t1", label = "Pull request")),
        lark("c2", "项目群：周会纪要", "示例同事乙", "本周进展和下周计划", "fyi", ago(180)),
        mail("m4", "acc1", "me@example.com", "订阅：每周技术简报", "示例简报", "fyi", ago(days(2))),
    )
    private val done = listOf(
        lark("d1", "设计稿已更新", "示例设计师", "按评审意见改了第三页", "fyi", ago(30), doneAt = ago(10)),
        mail("d2", "acc1", "me@example.com", "会议纪要：季度规划", "示例同事丙", "fyi", ago(300), doneAt = ago(240)),
        mail("d3", "acc2", "work@example.com", "报销已到账", "示例财务", "fyi", ago(days(1) + 60), doneAt = ago(days(1))),
    )
    private val card = InboxInteraction("q1", "mac", "s1", createdAt = ago(5), sessionTitle = "照片同步脚本", summary = "转换后的照片保存为哪种格式？")
    private val pageOne = listOf(done[2], mail("h1", "acc1", "me@example.com", "发票已开具", "示例商户", "fyi", ago(days(2)), doneAt = ago(days(2))),
        lark("h2", "旧版文档的链接", "示例同事丁", "已经迁到新地址", "fyi", ago(days(9)), doneAt = ago(days(9))))
    private val pageTwo = listOf(mail("h3", "acc2", "work@example.com", "去年的年度总结", "示例经理", "fyi", ago(days(40)), doneAt = ago(days(40))))

    private class Inbox(snapshot: InboxSnapshot, prefs: InboxPreferences, val pages: List<InboxDonePage>,
                        val days: List<InboxDoneDay>? = null, val pageFor: ((InboxDoneCursor?) -> InboxDonePage)? = null) : InboxRepository {
        val asked = mutableListOf<Pair<List<InboxDoneTarget>, InboxBulkAction>>()
        val historyAsked = mutableListOf<InboxDoneCursor?>()
        val prefs = MutableStateFlow(prefs)
        override val preferences = this.prefs
        override suspend fun savePreferences(value: InboxPreferences) { prefs.value = value }
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(snapshot)
        override val connectionState = flowOf(ConnectionState.Online)
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun doneHistory(manager: String?, cursor: InboxDoneCursor?): InboxDonePage {
            historyAsked += cursor
            return pageFor?.invoke(cursor) ?: pages.getOrElse(historyAsked.size - 1) { InboxDonePage() }
        }
        override suspend fun doneDays(manager: String?, zone: java.time.ZoneId) = days
        override suspend fun bulk(items: List<InboxDoneTarget>, action: InboxBulkAction): InboxBulkResult {
            asked += items to action
            return InboxBulkResult(changed = items.map { it.id })
        }
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

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)

    private fun inbox(prefs: InboxPreferences = InboxPreferences()) = Inbox(
        InboxSnapshot(records = open + done, interactions = listOf(card)), prefs,
        listOf(InboxDonePage(pageOne, InboxDoneCursor(ago(days(9)), "lark:h2")), InboxDonePage(pageTwo)))

    private fun show(inbox: Inbox) {
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                // No ripples: Robolectric has no render thread, so a tapped row would keep a frozen one in the shots.
                CompositionLocalProvider(LocalRippleConfiguration provides null) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1500.dp).background(PersonaTheme.palette.background)) { InboxScreen(inbox, navigator) }
                }
            }
        }
        rule.waitForIdle()
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/inbox-groups-shots").also { it.mkdirs() }

    /** One PNG per style and appearance of what is on screen now; [styles] narrows it. */
    private fun shots(name: String, styles: List<AppStyle> = AppStyle.entries, sheet: Boolean = false) {
        for (s in styles) for (d in listOf(false, true)) {
            style = s; dark = d
            Snapshot.sendApplyNotifications(); rule.waitForIdle()
            // A sheet is its own window: captured as the parity shots do (the page window's glass buttons don't draw on the JVM).
            val window = if (sheet) listOf(androidx.compose.ui.test.isDialog(), androidx.compose.ui.test.isPopup())
                .firstOrNull { rule.onAllNodes(it).fetchSemanticsNodes().isNotEmpty() } else null
            val bitmap = (if (window != null) rule.onAllNodes(window).onLast() else rule.onNodeWithTag("shot")).captureToImage().asAndroidBitmap()
            File(out(), "$name-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        style = AppStyle.Glass; dark = false
        Snapshot.sendApplyNotifications(); rule.waitForIdle()
    }

    /** A group header with [title] is drawn (Needs you and FYI may each have one: Lark in both). */
    private fun assertHeader(title: String) = assertTrue("header $title",
        rule.onAllNodes(hasText(title) and androidx.compose.ui.test.isHeading()).fetchSemanticsNodes().isNotEmpty())

    @Test fun needsYouBySourceTheAgentsFirst() {
        show(inbox())
        for (title in listOf("Agent 在等你", "飞书", "邮件 · me@example.com", "邮件 · work@example.com", "GitHub")) assertHeader(title)
        shots("inbox-by-source")
    }

    @Test fun byTimeIsKeptOnThisDevice() {
        val inbox = inbox()
        show(inbox)
        rule.onNodeWithText("按时间").performClick()
        rule.waitForIdle()
        assertEquals("time", inbox.prefs.value.grouping)
        for (title in listOf("Agent 在等你", "昨天")) assertHeader(title)
        shots("inbox-by-time")
    }

    @Test fun doneByTimeWithOlderPagesAndJumpTo() {
        val inbox = inbox(InboxPreferences(section = "done", expanded = listOf("action", "fyi", "done")))
        show(inbox)
        // The first page of history when Done opens, deduplicated against the list's own done items.
        assertEquals(listOf<InboxDoneCursor?>(null), inbox.historyAsked)
        // Boundaries that hold at any hour: 10 minutes ago is just now, a day ago yesterday, 9 days 7+ days.
        for (title in listOf("刚刚处理", "昨天", "7 天前")) assertHeader(title)
        rule.onAllNodesWithText("报销已到账").assertCountEquals(0) // "昨天" starts folded
        rule.onNode(androidx.compose.ui.test.hasScrollToIndexAction()).performScrollToNode(hasText("加载更早的"))
        rule.onNodeWithText("加载更早的").performClick()
        rule.waitForIdle()
        assertEquals(InboxDoneCursor(ago(days(9)), "lark:h2"), inbox.historyAsked.last())
        assertHeader("1 个月前")
        rule.onNode(androidx.compose.ui.test.hasScrollToIndexAction()).performScrollToNode(hasText("跳到"))
        shots("inbox-done")
        rule.onNodeWithText("跳到").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("7 天前 · 1").assertIsDisplayed()
        shots("inbox-jump", listOf(AppStyle.Glass), sheet = true)
        // Jumping to a folded group opens it.
        rule.onNodeWithText("7 天前 · 1").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("旧版文档的链接").assertExists()
    }

    /**
     * With the hub's counts per day (#3016): Done lists every group with its count, loaded or not, with no history read
     * when it opens and no Show older at its end; Jump To goes straight to an older group, whose first page is the one
     * before the group's end (no id), and its Show older goes on from where the hub said that page stopped.
     */
    @Test fun doneWithTheHubsCountsJumpsStraightToAGroup() {
        val zone = java.time.ZoneId.systemDefault()
        fun dayOf(at: String) = Instant.parse(at).atZone(zone).toLocalDate()
        // The list's done items each counted on their day, then 5 more 9 days ago and 30 more 40 days ago.
        val counted = (done.map { dayOf(it.item.doneAt!!) } + List(5) { dayOf(ago(days(9))) } + List(30) { dayOf(ago(days(40))) })
            .groupingBy { it }.eachCount().map { (day, n) -> InboxDoneDay(day, n) }.sortedByDescending { it.day }
        val monthEnd = persona.core.domain.inbox.InboxGroups.span("month", now, zone).second
        val next = InboxDoneCursor(ago(days(40)), "mail:h3")
        val inbox = Inbox(InboxSnapshot(records = open + done, interactions = listOf(card)),
            InboxPreferences(section = "done", expanded = listOf("action", "fyi", "done")), emptyList(), counted) { cursor ->
            if (cursor?.beforeId == null) InboxDonePage(pageTwo, next) else InboxDonePage()
        }
        show(inbox)
        assertEquals(emptyList<InboxDoneCursor?>(), inbox.historyAsked)
        for (title in listOf("刚刚处理", "昨天", "7 天前", "1 个月前")) assertHeader(title)
        rule.onAllNodesWithText("加载更早的").assertCountEquals(0)
        // Done's own count is all of it, as its groups add up (3 in the list, 5 and 30 older): its title and the Done chip.
        assertTrue("Done's title and chip count 38", rule.onAllNodes(hasText("38"), useUnmergedTree = true).fetchSemanticsNodes().size >= 2)
        rule.onNode(androidx.compose.ui.test.hasScrollToIndexAction()).performScrollToNode(hasText("1 个月前"))
        shots("inbox-done-counted")
        rule.onNode(androidx.compose.ui.test.hasScrollToIndexAction()).performScrollToNode(hasText("跳到"))
        rule.onNodeWithText("跳到").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("7 天前 · 5").assertIsDisplayed()
        rule.onNodeWithText("1 个月前 · 30").assertIsDisplayed()
        shots("inbox-done-counted-jump", listOf(AppStyle.Glass), sheet = true)
        rule.onNodeWithText("1 个月前 · 30").performClick()
        rule.waitForIdle()
        // Its own first page: before the group's end, no id; then its Show older from the hub's cursor.
        assertEquals(listOf<InboxDoneCursor?>(InboxDoneCursor(monthEnd.toString(), null)), inbox.historyAsked)
        rule.onNodeWithText("去年的年度总结").assertExists()
        rule.onNode(androidx.compose.ui.test.hasScrollToIndexAction()).performScrollToNode(hasText("加载更早的"))
        shots("inbox-done-counted-group")
        rule.onNodeWithText("加载更早的").performClick()
        rule.waitForIdle()
        assertEquals(next, inbox.historyAsked.last())
        // That page was its last: the group stops offering more.
        rule.onAllNodesWithText("加载更早的").assertCountEquals(0)
    }

    @Test fun aLongPressSelectsAndGroupTicksPickWholeGroups() {
        val inbox = inbox()
        show(inbox)
        rule.onNodeWithText("合同第二版的修改意见").performTouchInput { longClick() }
        rule.waitForIdle()
        rule.onNodeWithText("已选 1 项").assertExists()
        rule.onNodeWithText("周五发版前要确认的三件事").performClick()
        rule.onNodeWithContentDescription("选中「邮件 · work@example.com」整组").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("已选 4 项").assertExists()
        rule.onNodeWithText("已选 2", useUnmergedTree = true).assertExists() // the work account's group says what it has picked
        shots("inbox-select")
        rule.onNodeWithText("标为已处理").performClick()
        rule.waitForIdle()
        assertEquals(1, inbox.asked.size)
        assertEquals(InboxBulkAction.Done, inbox.asked.single().second)
        assertEquals(setOf("lark:c1", "mail:m1", "mail:m2", "mail:m3"), inbox.asked.single().first.map { it.id }.toSet())
        assertTrue("selection ended", rule.onAllNodesWithText("已选", substring = true).fetchSemanticsNodes().isEmpty())
    }
}
