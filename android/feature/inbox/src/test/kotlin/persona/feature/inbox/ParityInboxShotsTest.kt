package persona.feature.inbox

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.isPopup
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onLast
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import java.io.File

/**
 * The inbox and Feishu chat additions of #3045 in every style, light and dark: a row's "Mute" and "Rule" options, "Muted", "Pinned messages",
 * and my messages with their read receipts beside a recalled message "Keep recalled messages" kept and an edited one with "Edit history";
 * the Inbox's menu with "Rules", a new rule and one the hub has (with Remove), and "Rules" with some and with none.
 * Written as PNGs to `build/parity-shots/` to look at.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class ParityInboxShotsTest {
    @get:Rule val rule = createComposeRule()

    private val record = InboxRecord(InboxItem("github:1", kind = "mention", title = "example-org/example-repo#12", sender = "octo-example",
        preview = "Can you take a look?", tier = "action", updatedAt = "2026-11-04T09:30:00Z"), sourceLabel = "GitHub", source = "github",
        mutes = listOf(InboxMuteOption("repo", "example-org/example-repo", "example-org/example-repo"), InboxMuteOption("sender", "octo", "octo-example")))

    private fun message(id: String, text: String, mine: Boolean = true, position: Long = 1) =
        LarkMessage(id, if (mine) "me" else "u2", if (mine) "我" else "示例同事", position = position, time = "2026-11-04T09:30:00Z",
            mine = mine, parts = listOf(LarkPart.Text(text)))

    private inner class Fake : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot(records = listOf(record)))
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
        override suspend fun inboxMutes(manager: String?) = listOf(
            InboxMute("m1", "github", "GitHub", "repo", "example-org/example-repo", "example-org/example-repo", "2026-11-04T09:00:00Z"),
            InboxMute("m2", "lark", "飞书", "chat", "oc_1", "周末徒步群", "2026-11-03T09:00:00Z"),
            InboxMute("m3", "mail", "me@example.com", "sender", "news@example.com", "news@example.com"))
        override suspend fun larkChatPins(feedId: String, accountId: String?, cursor: String?) = LarkChatPinsPage("acct", listOf(
            LarkChatPin("p1", "m1", message("m1", "评审改到周四下午三点，会议室见日历", mine = false).copy(canUnpin = true), createdAt = "2026-11-04T09:30:00Z"),
            LarkChatPin("p2", "m2", message("m2", "", mine = false).copy(recalled = true, system = true, parts = emptyList()), createdAt = "2026-11-03T10:00:00Z"),
            LarkChatPin("p3", "m3", null, deleted = true),
        ), nextCursor = "c2", total = 5)
        var policies = rules
        override suspend fun inboxPolicies(record: InboxRecord?, manager: String?) = policies
        override suspend fun savePolicy(target: InboxPolicyTarget, tier: InboxPolicyTier, twin: InboxPolicyTwin): InboxPolicy? = null
        override suspend fun removePolicy(id: String, manager: String?) = Unit
    }

    private val rules = listOf(
        InboxPolicy("p1", "github", "GitHub", "sender", "octo", "octo-example", InboxPolicyTier.Action, InboxPolicyTwin.First, "2026-11-04T09:00:00Z"),
        InboxPolicy("p2", "lark", "飞书", "chat", "oc_1", "周末徒步群", InboxPolicyTier.Noise, createdAt = "2026-11-03T09:00:00Z"),
        InboxPolicy("p3", "mail", "me@example.com", "sender", "news@example.com", "news@example.com", InboxPolicyTier.Fyi, InboxPolicyTwin.Never,
            "2026-11-02T09:00:00Z"),
        InboxPolicy("p4", "github", "GitHub", "repo", "example-org/docs", "example-org/docs", twin = InboxPolicyTwin.Never, createdAt = "2026-11-01T09:00:00Z"),
    )

    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private enum class Shot { RowMenu, More, Mutes, Pins, Bubbles, Thread, RuleNew, RuleEdit, Rules, RulesEmpty }

    // A thread whose reply was recalled and kept: "Keep recalled messages" shows it there too.
    private val threadRoot = message("t", "周四评审的议程", mine = false).copy(replyCount = 1, thread = LarkThreadSource {
        LarkThreadPage(listOf(message("r1", "这条回复撤回了但留着", mine = false).copy(recalled = true, retained = true, system = true)), false, null)
    })

    @Test fun drawsTheAdditionsInEveryStyle() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var shot by mutableStateOf(Shot.RowMenu)
        var run by mutableIntStateOf(0)
        val repository = Fake()
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                key(run) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        when (shot) {
                            Shot.RowMenu, Shot.More -> InboxScreen(repository, navigator)
                            Shot.Mutes -> InboxMutesSheet(repository, onDismiss = {})
                            Shot.Pins -> LarkPinsSheet(repository, "f1", onOpen = {}, onDismiss = {})
                            Shot.Bubbles -> CompositionLocalProvider(LocalLarkMessageDisplay provides LarkMessageDisplay("acct", antiRecall = true, showEditHistory = true)) {
                                Column(Modifier.padding(Tokens.Space.l)) {
                                    LarkBubble(message("a", "下午三点的会挪到四点"), true, repository, null, retrying = false, onRetry = {}, onError = {},
                                        readMark = LarkReadMark.of(LarkReadState("a", 1, 0), isGroup = false))
                                    LarkBubble(message("b", "大家看一下这个方案"), false, repository, null, retrying = false, onRetry = {}, onError = {},
                                        readMark = LarkReadMark.of(LarkReadState("b", 3, 5), isGroup = true))
                                    LarkBubble(message("c", "收到"), false, repository, null, retrying = false, onRetry = {}, onError = {},
                                        readMark = LarkReadMark.of(LarkReadState("c", 8, 0), isGroup = true))
                                    LarkBubble(message("d", "我先撤回这句", mine = false).copy(recalled = true, retained = true, system = true),
                                        true, repository, null, retrying = false, onRetry = {}, onError = {})
                                    LarkBubble(message("e", "改过的版本", mine = false).copy(edited = true,
                                        editHistory = listOf(LarkEditVersion("2026-11-04T09:00:00Z", "第一版"))), true, repository, null,
                                        retrying = false, onRetry = {}, onError = {})
                                    LarkBubble(message("f", "没留存的撤回", mine = false).copy(recalled = true, system = true, parts = emptyList()),
                                        true, repository, null, retrying = false, onRetry = {}, onError = {})
                                }
                            }
                            Shot.Thread -> LarkChatThreadSheet(threadRoot, repository, LarkMessageDisplay("acct", antiRecall = true), onDismiss = {})
                            // A repository without a rule; the sender's from the hub's list, prefilled.
                            Shot.RuleNew -> InboxPolicySheet(repository, InboxPolicyTarget.of(record, record.mutes[0]), onDismiss = {})
                            Shot.RuleEdit -> InboxPolicySheet(repository, InboxPolicyTarget.of(record, record.mutes[1]), onDismiss = {})
                            Shot.Rules, Shot.RulesEmpty -> InboxRulesSheet(repository, onDismiss = {})
                        }
                    }
                }
            }
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (next in Shot.entries) {
            repository.policies = if (next == Shot.RulesEmpty) emptyList() else rules
            style = s; dark = d; shot = next; run++
            rule.waitForIdle()
            when (next) {
                Shot.RowMenu -> {
                    // A long-press starts a selection (#2576); with the one row picked, the bar's More holds its menu.
                    rule.onNodeWithText("example-org/example-repo#12").performTouchInput { longClick() }
                    rule.waitForIdle()
                    rule.onNodeWithContentDescription("更多").performClick()
                    rule.waitForIdle()
                    rule.assertText("静音这个仓库 · example-repo"); rule.assertText("静音这个发件人 · octo-example")
                    rule.assertText("为这个仓库设规则 · example-repo"); rule.assertText("为这个发件人设规则 · octo-example")
                }
                Shot.More -> {
                    rule.onNodeWithContentDescription("更多").performClick()
                    rule.waitForIdle()
                    rule.assertText("已静音"); rule.assertText("规则")
                }
                Shot.Mutes -> { rule.assertText("周末徒步群"); rule.assertText("会话") }
                Shot.Pins -> { rule.assertText("5 条 Pin"); rule.assertText("原消息已不可用"); rule.assertText("消息已撤回") }
                Shot.Bubbles -> { rule.assertText("已读"); rule.assertText("5 人未读"); rule.assertText("全部已读"); rule.assertText("我先撤回这句")
                    rule.assertText("已撤回"); rule.assertText("消息已撤回") }
                Shot.Thread -> { rule.assertText("这条回复撤回了但留着"); rule.assertText("已撤回") }
                Shot.RuleNew -> { rule.assertText("example-repo 的规则"); rule.assertText("GitHub · 仓库"); rule.assertText("交给分拣")
                    assertTrue(rule.onAllNodesWithText("移除").fetchSemanticsNodes().isEmpty()) }
                Shot.RuleEdit -> { rule.assertText("octo-example 的规则"); rule.assertText("移除"); rule.assertText("分身优先") }
                Shot.Rules -> { rule.assertText("周末徒步群"); rule.assertText("GitHub · 发件人 · 始终需要你 · 分身优先"); rule.assertText("编辑") }
                Shot.RulesEmpty -> rule.assertText("还没有规则")
            }
            capture("parity-inbox-${next.name.lowercase()}-${s.name.lowercase()}-${if (d) "dark" else "light"}", window = next != Shot.Bubbles)
        }
    }

    private fun androidx.compose.ui.test.junit4.ComposeContentTestRule.assertText(text: String) =
        assertTrue("missing $text", onAllNodesWithText(text, substring = true).fetchSemanticsNodes().isNotEmpty())

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/parity-shots").also { it.mkdirs() }

    private fun capture(name: String, window: Boolean) {
        rule.waitForIdle()
        // Sheets are windows of their own, drawn only when captured as that window.
        val sheet = if (window) listOf(isDialog(), isPopup()).firstOrNull { rule.onAllNodes(it).fetchSemanticsNodes().isNotEmpty() } else null
        val bitmap = (if (sheet != null) rule.onAllNodes(sheet).onLast() else rule.onNodeWithTag("shot")).captureToImage().asAndroidBitmap()
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}
