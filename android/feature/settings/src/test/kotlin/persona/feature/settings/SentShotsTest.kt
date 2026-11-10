package persona.feature.settings

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.SemanticsMatcher
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.hasScrollAction
import androidx.compose.ui.test.performScrollToNode
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.core.navigation.ChatRoute
import persona.core.navigation.SettingsPage
import java.io.File
import java.time.Instant

private val SentNow: Instant = Instant.parse("2026-11-05T12:30:00Z")

private fun target(json: String): Map<String, JsonElement> = JsonCodec.parseToJsonElement(json) as JsonObject

/** Made-up rows: every way through, a chat to open, an extension's own record and a connector the page does not know. */
private val SentSample = listOf(
    SentItem(9, "2026-11-05T12:25:00Z", "github", "github_comment", target("""{"ref":"example-org/app#12"}"""),
        "Looks good to me. One nit on the retry loop: it should stop after the third try.\n\nI'll look again once it is pushed.",
        "github_comment example-org/app#12", SentOrigin("mac", "s-1"), SentLetThrough("card", interactionId = "ix-4f2a")),
    SentItem(8, "2026-11-05T11:40:00Z", "mail", "mail_send", target("""{"to":["ann@example.com"],"subject":"Lunch on Friday","cc":["bo@example.com"]}"""),
        "Hi Ann,\n\nFriday works for me. Noon at the usual place?\n\nBest",
        "mail_send ann@example.com", SentOrigin("mac", "s-2"),
        SentLetThrough("rule", rule = SentRule("mail", "mail_send", target("""{"to":"ann@example.com"}"""), "allow"))),
    SentItem(7, "2026-11-05T09:10:00Z", "lark", "POST /im/v1/messages", target("""{"conversation":"oc_demo01","method":"POST","path":"/im/v1/messages"}"""),
        "On it. I'll have the draft by 3pm.", "POST /im/v1/messages oc_demo01", SentOrigin("mac", "s-3"),
        SentLetThrough("card", interactionId = "ix-77c1", answeredBy = "assistant")),
    SentItem(6, "2026-11-04T17:00:00Z", "examplechat", "reply", target("""{"chat":"general","thread":"t-9"}"""),
        "Thanks, noted.", "examplechat: general · t-9", SentOrigin(extensionId = "examplechat"), SentLetThrough("auto", setting = "auto_reply")),
    SentItem(5, "2026-11-03T08:00:00Z", "lark", "POST /im/v1/messages", target("""{"method":"POST","path":"/docs/v1/comments"}"""),
        "", "", SentOrigin(), SentLetThrough("conversation_always")),
    SentItem(4, "2026-11-02T08:00:00Z", "github", "github_review", target("""{"ref":"example-org/app#9","event":"APPROVE"}"""),
        "Approved.", "github_review example-org/app#9", SentOrigin("mac", "s-1"), SentLetThrough("voice_confirm")),
)

private val SentLoaded = SentState(items = SentSample, next = 4, seen = listOf("github", "mail", "lark", "examplechat"))

/** A hub answering Settings › Sent as you from memory, as the mock hub does. */
internal class FakeSent(var rows: List<SentItem> = SentSample, private val pageSize: Int = 50) : HubSent {
    var fail: Exception? = null
    val asked = mutableListOf<Triple<Long?, Int?, String?>>()

    override suspend fun page(before: Long?, limit: Int?, connector: String?): SentPage {
        asked += Triple(before, limit, connector)
        fail?.let { throw it }
        val hit = rows.filter { (before == null || it.id < before) && (connector.isNullOrEmpty() || it.connector == connector) }
        val got = hit.take(pageSize)
        return SentPage(got, next = if (hit.size > pageSize) got.last().id else null)
    }

    override suspend fun recall(id: Long): SentItem? {
        fail?.let { throw it }
        rows = rows.map { if (it.id == id) it.copy(recalledAt = "2026-11-05T12:05:00Z", canRecall = false) else it }
        return rows.firstOrNull { it.id == id }
    }
}

/**
 * Settings › Sent as you drawn on the JVM with Robolectric's native graphics (no emulator), in every style, light and dark: the
 * settings row among its neighbours, the list (every way through, a chat to open, an extension's own record), a send's details
 * with its whole text, and the empty and refused states. Each is checked to draw something and written as a PNG to
 * `build/sent-shots/` to look at. The behaviour (the page over a fake hub) is driven too: a row and its chat, the chips, Load
 * earlier, Retry, and the settings row.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2100dp-xhdpi")
class SentShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/sent-shots").also { it.mkdirs() }

    private fun capture(name: String) {
        rule.waitForIdle()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val background = bitmap.getPixel(1, 1)
        var other = 0
        for (y in 0 until bitmap.height step 7) for (x in 0 until bitmap.width step 7) if (bitmap.getPixel(x, y) != background) other++
        assertTrue("$name drew nothing", other > 100)
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    /** [height] in dp of the picture; null lets it be as tall as what it draws (the sheet's body). */
    private enum class Shot(val height: Int?) { Settings(2600), List(1100), Detail(null), DetailCut(null), Recall(null), Recalled(null), Empty(700), Filtered(700), NoDatabase(700), Failed(700) }

    /** A chip, not a row that has the same words: rows are buttons, chips check boxes. */
    private fun chip(text: String) = hasText(text) and SemanticsMatcher.expectValue(SemanticsProperties.Role, Role.Checkbox)

    @Test fun drawsTheSentPagesInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shot by mutableStateOf(Shot.Settings)
        val services = settingsServices(FakeSent())
        val cut = SentSample[0].copy(text = "Another line of a long mail that goes on and on. ".repeat(100).take(SentWire.TextMost))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val palette = PersonaTheme.palette
                Box(Modifier.testTag("shot").width(400.dp).then(shot.height?.let { Modifier.height(it.dp) } ?: Modifier).background(palette.background)) {
                    when (shot) {
                        // The real settings list (docs/lessons/android.md: not hand-drawn rows), its Connection section on top.
                        Shot.Settings -> SettingsScreen(services, onOpen = {})
                        // The review row was recalled: its tag.
                        Shot.List -> SentContent(SentLoaded.copy(items = SentSample.map { if (it.id == 4L) it.copy(recalledAt = "2026-11-02T08:05:00Z") else it }), SentNow, {}, false, {}, {}, {}, {}, {}, onChat = { _, _ -> })
                        // The sheet's body on the sheet's own surface (JSheet draws it over a scrim in the app).
                        Shot.Detail, Shot.DetailCut, Shot.Recall, Shot.Recalled -> Column(Modifier.fillMaxWidth().background(if (style == AppStyle.Brief) palette.background else palette.surfaceOpaque)
                            .padding(horizontal = pagePadding, vertical = Tokens.Space.l)) {
                            val row = when (shot) {
                                Shot.Detail -> SentSample[1]
                                // Recall offered, after a refusal; and a row taken back.
                                Shot.Recall -> SentSample[0].copy(canRecall = true)
                                Shot.Recalled -> SentSample[0].copy(recalledAt = "2026-11-05T12:28:00Z")
                                else -> cut
                            }
                            androidx.compose.material3.Text(SentText.connectorName(androidx.compose.ui.platform.LocalResources.current, row.connector),
                                style = PersonaTheme.type.title, color = palette.ink)
                            Spacer(Modifier.height(Tokens.Space.m))
                            SentDetailBody(row, onChat = { _, _ -> }, onDone = {}, onRecall = {},
                                recallError = if (shot == Shot.Recall) SentText.recallProblem(HubFailure(HubError.Server, "unknown", 504, "recall_unknown")) else null)
                        }
                        Shot.Empty -> SentContent(SentState(items = emptyList()), SentNow, {}, false, {}, {}, {}, {}, {}, onChat = null)
                        Shot.Filtered -> SentContent(SentState(items = emptyList(), filter = "lark", seen = listOf("mail")), SentNow, {}, false, {}, {}, {}, {}, {}, onChat = null)
                        Shot.Failed -> SentContent(SentState(problem = SentText.problem(HubFailure(HubError.Server, "boom", 500, "internal"))), SentNow,
                            {}, false, {}, {}, {}, {}, {}, onChat = null)
                        Shot.NoDatabase -> SentContent(SentState(problem = SentText.problem(HubFailure(HubError.Server, "no database", 503, "no_database"))), SentNow,
                            {}, false, {}, {}, {}, {}, {}, onChat = null)
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            for (next in Shot.entries) {
                shot = next
                capture("sent-${next.name.lowercase()}-$suffix")
            }
        }
    }

    /** Recall is offered only where the hub says so, asks in a dialog first, and the details then say the row was recalled. */
    @Test fun recallAsksFirstThenShowsTheRowRecalled() {
        val hub = FakeSent(SentSample.mapIndexed { i, it -> if (i == 0) it.copy(canRecall = true) else it })
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(2400.dp)) { SentAsYouPage(hub, onBack = {}, onChat = null) }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("ann@example.com · Lunch on Friday").performClick()
        rule.waitForIdle()
        rule.onAllNodesWithText("撤回…").assertCountEquals(0)
        rule.onNode(hasText("完成") and hasClickAction()).performClick()
        rule.waitForIdle()
        rule.onNodeWithText("example-org/app#12").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("撤回…").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("撤回这条消息？").assertExists()
        assertTrue(hub.rows[0].recalledAt.isEmpty()) // asked first
        rule.onNode(hasText("撤回") and hasClickAction()).performClick()
        rule.waitForIdle()
        assertTrue(hub.rows[0].recalledAt.isNotEmpty())
        rule.onAllNodesWithText("撤回…").assertCountEquals(0)
        rule.onAllNodesWithText("已撤回").assertCountEquals(2) // the row's tag and the details' fact
        assertEquals(2, hub.asked.size) // the list read again
    }

    /** A row opens its details, and the chat it names opens by its ids; a row with no chat has no link. */
    @Test fun aRowOpensItsDetailsAndItsChatOpens() {
        val hub = FakeSent()
        var opened: Pair<String, String>? = null
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(2400.dp)) { SentAsYouPage(hub, onBack = {}, onChat = { machine, session -> opened = machine to session }) }
            }
        }
        rule.waitForIdle()
        assertEquals(listOf(Triple<Long?, Int?, String?>(null, null, null)), hub.asked)
        // The words a row has: the connector, where it went, the first lines, how it was let through.
        rule.onNodeWithText("example-org/app#12").assertIsDisplayed()
        rule.onNodeWithText("Looks good to me. One nit on the retry loop: it should stop after the third try.").assertIsDisplayed()
        rule.onNodeWithText("ann@example.com · Lunch on Friday").assertIsDisplayed()
        rule.onNodeWithText("你在确认卡片上同意").assertIsDisplayed()
        rule.onNodeWithText("按规则").assertIsDisplayed()
        rule.onNodeWithText("助手代答").assertIsDisplayed()
        rule.onNodeWithText("扩展自行发送 · auto_reply").assertIsDisplayed()
        rule.onNodeWithText("voice_confirm").assertIsDisplayed()
        rule.onNodeWithText("没有记下正文").assertIsDisplayed()
        // An extension's own record says so, and one with no chat says that.
        rule.onNodeWithText("扩展自述").assertIsDisplayed()
        rule.onAllNodesWithText("无对话").assertCountEquals(1)
        // The chat link is named by its row for a screen reader, and opens the chat by its ids.
        rule.onAllNodesWithContentDescription("打开对话：", substring = true).assertCountEquals(4)
        rule.onNodeWithContentDescription("打开对话：邮件 ann@example.com · Lunch on Friday").performClick()
        assertEquals("mac" to "s-2", opened)
        // The row opens the details: the time, the action, every field of the target, the rule, the whole text.
        rule.onNodeWithText("Looks good to me. One nit on the retry loop: it should stop after the third try.").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("发送时间").assertExists()
        rule.onNodeWithText("github_comment").assertExists()
        rule.onNodeWithText("ix-4f2a").assertExists()
        rule.onNodeWithText("复制正文").assertExists()
        rule.onNodeWithText(SentSample[0].text).assertExists()
        rule.onAllNodesWithText("这里的正文在", substring = true).assertCountEquals(0)
        rule.onNode(hasText("完成") and hasClickAction()).performClick()
        rule.waitForIdle()
        rule.onAllNodesWithText("发送时间").assertCountEquals(0)
    }

    /** Chats open from the details too, the sheet closing first; a text of exactly 4000 characters says it was cut. */
    @Test fun theDetailsOpenTheChatAndSayWhenTheTextWasCut() {
        val cut = SentSample[0].copy(text = "y".repeat(4000))
        val hub = FakeSent(listOf(cut))
        var opened: Pair<String, String>? = null
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(2400.dp)) { SentAsYouPage(hub, onBack = {}, onChat = { machine, session -> opened = machine to session }) }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("y".repeat(4000)).performClick()
        rule.waitForIdle()
        rule.onNodeWithText("这里的正文在 4,000 个字处被截断，实际发出的可能更长。").assertExists()
        // the details' own Open chat (the row's is named by its row)
        rule.onNode(hasText("打开对话") and hasClickAction() and hasContentDescription("打开对话：", substring = true).not()).performClick()
        rule.waitForIdle()
        assertEquals("mac" to "s-1", opened)
        rule.onAllNodesWithText("发送时间").assertCountEquals(0)
    }

    /** The chips: Lark and the connectors the rows showed; one reads that connector's rows, the same one again is All. */
    @Test fun theChipsFilterByConnectorAndTheSameOneAgainIsAll() {
        val hub = FakeSent()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(2400.dp)) { SentAsYouPage(hub, onBack = {}, onChat = null) }
            }
        }
        rule.waitForIdle()
        for (name in listOf("全部", "飞书", "GitHub", "邮件", "examplechat")) rule.onNode(chip(name)).assertExists()
        rule.onNode(chip("GitHub")).performClick()
        rule.waitForIdle()
        assertEquals("github", hub.asked.last().third)
        rule.onAllNodesWithText("ann@example.com · Lunch on Friday").assertCountEquals(0)
        rule.onNodeWithText("example-org/app#12").assertIsDisplayed()
        rule.onNode(chip("GitHub")).performClick()
        rule.waitForIdle()
        assertEquals(null, hub.asked.last().third)
        rule.onNodeWithText("ann@example.com · Lunch on Friday").assertIsDisplayed()
        // Lark has rows here; a connector with none says so and offers Show all.
        hub.rows = hub.rows.filter { it.connector != "lark" }
        rule.onNode(chip("飞书")).performClick()
        rule.waitForIdle()
        rule.onNodeWithText("飞书 还没有记录").assertIsDisplayed()
        rule.onNode(hasText("显示全部") and hasClickAction()).performClick()
        rule.waitForIdle()
        rule.onNodeWithText("example-org/app#12").assertIsDisplayed()
    }

    /** Load earlier reads the page after the list with `before`, adds its rows and goes when there is no more. */
    @Test fun loadEarlierAddsThePageAfterTheListAndGoesWithTheLastOne() {
        val hub = FakeSent(pageSize = 4)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(2400.dp)) { SentAsYouPage(hub, onBack = {}, onChat = null) }
            }
        }
        rule.waitForIdle()
        rule.onAllNodesWithText("Approved.").assertCountEquals(0)
        rule.onNode(hasText("加载更早") and hasClickAction()).performClick()
        rule.waitForIdle()
        assertEquals(6L, hub.asked.last().first)
        rule.onAllNodesWithText("加载更早").assertCountEquals(0)
        rule.onNodeWithText("Approved.").assertExists()
    }

    /** The hub's refusals are said in their own words before the usual one; only that one has Retry. */
    @Test fun theRefusalsAreSaidInTheirWordsAndOnlyAFailureHasRetry() {
        // A failure of the first read: the usual words with Retry, and Retry reads again.
        val failing = FakeSent().apply { fail = HubFailure(HubError.Server, "boom", 500, "internal") }
        var current by mutableStateOf<HubSent>(failing)
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(2400.dp)) { SentAsYouPage(current, onBack = {}, onChat = null) }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("读取失败：boom").assertIsDisplayed()
        failing.fail = null
        rule.onNode(hasText("重试") and hasClickAction()).performClick()
        rule.waitForIdle()
        rule.onNodeWithText("example-org/app#12").assertIsDisplayed()
        rule.onAllNodesWithText("重试").assertCountEquals(0)
        for ((error, words) in listOf(
            HubFailure(HubError.Server, "no database", 503, "no_database") to "这台 hub 没有数据库，所以不记录以你的名义发出的内容。",
            HubFailure(HubError.Server, "forbidden", 403, "forbidden") to "只有拥有完整权限的设备才能看这个列表。请换一台有完整权限的自己的设备打开。",
            HubFailure(HubError.Server, "404 page not found", 404) to "这台 hub 的版本还不记录以你的名义发出的内容。升级 hub 后再来。",
        )) {
            current = FakeSent().apply { fail = error }
            rule.waitForIdle()
            rule.onNodeWithText(words).assertIsDisplayed()
            rule.onAllNodesWithText("重试").assertCountEquals(0)
        }
    }


    /** The settings screen hands the navigation the chat a row names, by the ids the page was given. */
    @Test fun theSettingsScreenOpensARowsChatByRoute() {
        val navigated = mutableListOf<androidx.navigation3.runtime.NavKey>()
        val services = settingsServices(FakeSent())
        var onNavigate by mutableStateOf<((androidx.navigation3.runtime.NavKey) -> Unit)?>({ navigated += it })
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.width(400.dp).height(2400.dp)) { SettingsScreen(services, initialPage = "sent", onNavigate = onNavigate) }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithContentDescription("打开对话：邮件 ann@example.com · Lunch on Friday").performClick()
        rule.onNodeWithContentDescription("打开对话：GitHub example-org/app#12").performClick()
        assertEquals(listOf<androidx.navigation3.runtime.NavKey>(ChatRoute("mac", "s-2"), ChatRoute("mac", "s-1")), navigated)
        // Without a navigation host there is nothing to open a chat with: no link.
        onNavigate = null
        rule.waitForIdle()
        rule.onAllNodesWithText("打开对话").assertCountEquals(0)
        rule.onAllNodesWithContentDescription("打开对话：", substring = true).assertCountEquals(0)
    }

    /** Where a chat can't be opened the details have no Chat fact and no Open chat; a send with no chat still says so there. */
    @Test fun withoutAWayToOpenChatsTheDetailsHaveNoChatFactAndNoButton() {
        val hub = FakeSent()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.width(400.dp).height(2400.dp)) { SentAsYouPage(hub, onBack = {}, onChat = null) }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Looks good to me. One nit on the retry loop: it should stop after the third try.").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("发送时间").assertExists()
        rule.onAllNodesWithText("对话").assertCountEquals(0)
        rule.onAllNodesWithText("打开对话").assertCountEquals(0)
        rule.onNode(hasText("完成") and hasClickAction()).performClick()
        rule.waitForIdle()
        // A send that names no chat says so in its details.
        rule.onNodeWithText("没有记下正文").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("发送时间").assertExists()
        rule.onAllNodesWithText("对话").assertCountEquals(1)
        rule.onAllNodesWithText("无对话").assertCountEquals(2)
    }

    /** Only a failure is red (and has Retry); what the hub says about itself reads plainly. */
    @Test fun onlyAFailureIsRed() {
        var state by mutableStateOf(SentState())
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").width(400.dp).height(500.dp).background(PersonaTheme.palette.background)) {
                    SentContent(state, SentNow, {}, false, {}, {}, {}, {}, {}, onChat = null)
                }
            }
        }
        fun red(): Int {
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            var count = 0
            for (y in 0 until bitmap.height) for (x in 0 until bitmap.width) {
                val pixel = bitmap.getPixel(x, y)
                val (r, g, b) = Triple(android.graphics.Color.red(pixel), android.graphics.Color.green(pixel), android.graphics.Color.blue(pixel))
                if (r > 150 && g < 110 && b < 110) count++
            }
            return count
        }
        val refusals = listOf(
            HubFailure(HubError.Server, "no database", 503, "no_database"),
            HubFailure(HubError.Server, "forbidden", 403, "forbidden"),
            HubFailure(HubError.Server, "404 page not found", 404),
        )
        for (refusal in refusals) {
            state = SentState(problem = SentText.problem(refusal))
            assertEquals("${refusal.status}", 0, red())
            rule.onAllNodesWithText("重试").assertCountEquals(0)
        }
        state = SentState(problem = SentText.problem(HubFailure(HubError.Server, "boom", 500, "internal")))
        assertTrue("a failure is red", red() > 20)
        rule.onNode(hasText("重试") and hasClickAction()).assertExists()
    }

    /** The real Settings home has one Connectors entry; the ledger is a secondary page with Back to Connectors. */
    @Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
    @Test fun theSettingsRowSitsAfterTheConnectorsAndOpensThePage() {
        var services by mutableStateOf(settingsServices(FakeSent()))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Box(Modifier.testTag("shot").width(400.dp).height(900.dp)) { SettingsScreen(services) }
            }
        }
        rule.onNodeWithText("以你的名义发出的记录").assertDoesNotExist()
        rule.onNode(hasScrollAction()).performScrollToNode(hasText("连接器") and hasClickAction())
        rule.onNode(hasText("连接器") and hasClickAction()).performClick()
        rule.onNodeWithText("以你的名义发出的记录").assertExists()
        val row = rule.onNode(hasText("以你的名义发出的记录") and hasClickAction()).fetchSemanticsNode().boundsInRoot
        val mail = rule.onNode(hasText("邮箱") and hasClickAction()).fetchSemanticsNode().boundsInRoot
        assertTrue(row.top >= mail.bottom)
        rule.onNodeWithText("以你的名义发出的记录").performClick()
        rule.onNodeWithText("Persona 通过连接器以你的名义发出的内容，新的在前。点一行看全文。").assertExists()
        rule.onNodeWithContentDescription("返回").performClick()
        rule.onNode(hasText("邮箱") and hasClickAction()).assertExists()
        rule.onNodeWithContentDescription("返回").performClick()
        rule.onNodeWithText("以你的名义发出的记录").assertDoesNotExist()
    }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
    @Test fun connectorsEntryKeepsTheNavigationTargetAndCapabilityGate() {
        var opened: SettingsPage? = null
        var services by mutableStateOf(settingsServices(FakeSent()))
        rule.setContent { PersonaTheme(reduceMotion = true) {
            SettingsScreen(services, initialPage = "connectors", onOpen = { opened = it })
        } }
        rule.onNodeWithText("以你的名义发出的记录").performClick()
        assertEquals(SettingsPage.Sent, opened)
        services = services.copy(demo = true)
        rule.onNodeWithText("以你的名义发出的记录").assertDoesNotExist()
        services = services.copy(demo = false, sent = null)
        rule.onNodeWithText("以你的名义发出的记录").assertDoesNotExist()
    }

    /** JVM native-rendered evidence uses the real connector list and real ledger, not lookalike rows. */
    @Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
    @Test fun nestedPagesInEveryStyleAndAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var page by mutableStateOf("connectors")
        val services = settingsServices(FakeSent())
        rule.setContent { PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
            Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                SettingsScreen(services, initialPage = page)
            }
        } }
        for (s in AppStyle.entries) {
            style = s
            for (d in listOf(false, true)) {
                dark = d
                androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
                page = "connectors"
                rule.onNodeWithText("以你的名义发出的记录").assertExists()
                capture("nested-connectors-${s.key}-${if (d) "dark" else "light"}")
                // The legacy standalone page name remains accepted, but Back now reaches Connectors.
                page = "sent"
                rule.onNodeWithText("Persona 通过连接器以你的名义发出的内容，新的在前。点一行看全文。").assertExists()
                capture("nested-ledger-${s.key}-${if (d) "dark" else "light"}")
                rule.onNodeWithContentDescription("返回").performClick()
                rule.onNode(hasText("邮箱") and hasClickAction()).assertExists()
            }
        }
    }

    private fun settingsServices(api: HubSent) = SettingsServices(
        pairing = object : PairingService {
            override val isPaired = MutableStateFlow(true)
            override suspend fun pair(request: PairingRequest) = error("not used")
            override suspend fun unpair() = error("not used")
        },
        connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = error("not used")
            override fun send(message: JsonObject) = false
        },
        sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(emptyList<Machine>())
            override val sessions = MutableStateFlow(emptyList<Session>())
            override suspend fun refresh() {}
        },
        updates = object : AppUpdates { override suspend fun check(): AppUpdate? = null },
        sent = api,
    )
}
