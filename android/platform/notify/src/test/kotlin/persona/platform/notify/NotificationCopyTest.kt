package persona.platform.notify

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import persona.core.contracts.Elicitation
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.QuestionOption
import persona.core.contracts.api.ConnectionState
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

/** The notification text in each language: notifications are drawn by the system, so no screenshot shows them. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class NotificationCopyTest {
    private val context: Context get() = ApplicationProvider.getApplicationContext()
    private val states = listOf(
        ConnectionState.Online, ConnectionState.Connecting, ConnectionState.Updating,
        ConnectionState.WaitingForNetwork, ConnectionState.NeedsLocalNetworkPermission,
        ConnectionState.CertificateMismatch, ConnectionState.Revoked,
    )

    private fun text(id: Int) = context.getString(id)

    @Test fun systemNoticesUseTheUxVocabularyWithoutReportingUnknownResultsAsFailures() {
        assertEquals(listOf("已连接 hub", "连接中…", "更新中…", "等待网络", "需要本地网络权限", "hub 的证书变了", "配对已失效"),
            states.map { text(ongoingTitle(it)) })
        assertEquals("结果待确认", actionResultCopy(NotificationActionResult.Duplicate)?.first?.let(::text))
        assertEquals("已在电脑上处理", actionResultCopy(NotificationActionResult.Gone)?.first?.let(::text))
        assertEquals("已过期，回到终端处理", actionResultCopy(NotificationActionResult.Expired)?.first?.let(::text))
        assertEquals("这个会话只能查看，请打开会话或到电脑上处理。", actionResultCopy(NotificationActionResult.ReadOnly)?.second?.let(::text))
        assertNull(actionResultCopy(NotificationActionResult.Submitted))
        assertEquals(listOf("需要你", "提醒", "做完了", "实时状态"), listOf(R.string.notify_channel_input,
            R.string.notify_channel_alert, R.string.notify_channel_done, R.string.notify_channel_status).map(::text))
    }

    @Config(sdk = [35], qualifiers = "en")
    @Test fun english() {
        assertEquals(listOf("Connected to hub", "Connecting…", "Updating…", "Waiting for network", "Local network permission needed",
            "The hub's certificate changed", "Pairing is no longer valid"), states.map { text(ongoingTitle(it)) })
        assertEquals(listOf("Deny", "Allow", "View", "Approve", "Send back"), buttons().map(::text))
        assertEquals("Persona is asking you", text(R.string.notify_title_assistant_asks))
        assertEquals("Can't approve on this phone", actionResultCopy(NotificationActionResult.ReadOnly)?.first?.let(::text))
    }

    @Config(sdk = [35], qualifiers = "ja")
    @Test fun japanese() {
        assertEquals("ハブに接続済み", text(ongoingTitle(ConnectionState.Online)))
        assertEquals(listOf("拒否", "許可", "表示", "承認", "差し戻し"), buttons().map(::text))
        assertEquals("Personaが質問しています", text(R.string.notify_title_assistant_asks))
        assertEquals("「再開」をタップすると通知を再び受け取れます", text(R.string.notify_stopped_text))
    }

    private val mcpPermission = Interaction("p", kind = "permission", toolName = "mcp__github__create_issue", summary = "Fix login")
    private val personaPermission = Interaction("q", kind = "permission", toolName = "mcp__persona__mail_send")
    private val form = Interaction("f", kind = "elicitation", summary = "Pick a repository",
        elicitation = Elicitation(message = "Pick a repository"))

    /** A glance at the lock screen says which tool and what form: no raw mcp__ names, not only "An MCP form is waiting for you" (#4354). */
    @Test fun requestNotificationsNameTheToolAndTheForm() {
        assertEquals("请求使用 github · create issue：Fix login" to "请求使用 github · create issue：Fix login", requestCopy(context, mcpPermission))
        assertEquals("请求使用 Persona · mail send", requestCopy(context, personaPermission).first)
        assertEquals("MCP 表单：Pick a repository" to "MCP 表单：Pick a repository", requestCopy(context, form))
        assertEquals("MCP 表单等你填写", requestCopy(context, Interaction("e", kind = "elicitation")).first)
        val bash = Interaction("b", kind = "permission", toolName = "Bash", summary = "rm -rf build")
        assertEquals("请求使用 Bash：rm -rf build", requestCopy(context, bash).second)
        // A blank or missing summary falls back to the form's own message; a long message keeps its first line collapsed.
        val message = Interaction("m", kind = "elicitation", summary = " ", elicitation = Elicitation(message = "Pick a repository\nfrom your org"))
        assertEquals("MCP 表单：Pick a repository" to "MCP 表单：Pick a repository\nfrom your org", requestCopy(context, message))
        assertEquals("MCP 表单：Pick a repository", requestCopy(context, form.copy(summary = null)).first)
        val codex = Interaction("c", kind = "permission", agent = "codex", toolName = "mcp__github__create_issue")
        assertEquals("Codex 请求使用 github · create issue", requestCopy(context, codex).first)
        val question = Interaction("k", kind = "question", questions = listOf(Question(question = "Which branch?")))
        assertEquals("Which branch?" to "Which branch?", requestCopy(context, question))
        // A password card (#1137): the line names the computer, the detail is the hub's summary with every command.
        val secretRun = Interaction("s", kind = "permission", toolName = "secret_run", summary = "Mac mini\n\$ sudo -S -k -p '' true",
            toolInput = kotlinx.serialization.json.buildJsonObject { put("machine_name", kotlinx.serialization.json.JsonPrimitive("Mac mini")) })
        assertEquals("用你的密码在 Mac mini 上运行" to "Mac mini\n\$ sudo -S -k -p '' true", requestCopy(context, secretRun))
    }

    private fun options(vararg labels: String) = labels.map { QuestionOption(label = it) }
    private fun ask(vararg questions: Question) = Interaction("a", kind = "question", questions = questions.toList())
    private val deploy = Question(question = "这次部署到哪？", options = options("测试环境", "预发", "生产环境", "回滚"))

    /** A question's body is the hub push's (#4308): the first question, its choices and how many questions follow (#4726). */
    @Test fun questionNotificationsListTheChoicesAndTheQuestionsLeft() {
        assertEquals("这次部署到哪？\n测试环境 / 预发 / 生产环境 / 回滚\n还有 1 个问题",
            requestCopy(context, ask(deploy, Question(question = "要不要通知群里？", options = options("要", "不要")))).second)
        // Past four choices the rest are counted; each label is clipped to 20 characters and kept on one line.
        val long = Question(question = "Pick one", multiSelect = true,
            options = options("a\nb", "一二三四五六七八九十一二三四五六七八九十一二", "c", "d", "e", "f"))
        assertEquals("Pick one\na b / 一二三四五六七八九十一二三四五六七八九十… / c / d 等 2 个", requestCopy(context, ask(long)).second)
        // No choices (free text): the question alone; blank labels do not count as choices.
        assertEquals("Why?", requestCopy(context, ask(Question(question = " Why? ", options = options(" ")))).second)
        assertEquals("Why?\nA B", requestCopy(context, ask(Question(question = "Why?", options = options("A\u00A0 B")))).second)
        assertEquals("Choose a target", requestCopy(context, Interaction("n", kind = "question", summary = "Choose a target")).second)
        assertEquals("这次部署到哪？\n测试环境 / 预发 / 生产环境 / 回滚\n还有 2 个问题",
            requestCopy(context, ask(deploy, Question(question = "b"), Question(question = "c"))).second)
    }

    @Config(sdk = [35], qualifiers = "en")
    @Test fun questionNotificationsInEnglish() {
        val five = Question(question = "Where?", options = options("a", "b", "c", "d", "e"))
        assertEquals("Where?\na / b / c / d and 1 more\n1 more question", requestCopy(context, ask(five, Question(question = "Tell the team?"))).second)
        assertEquals("Where?\na / b / c / d and 1 more\n2 more questions",
            requestCopy(context, ask(five, Question(question = "x"), Question(question = "y"))).second)
    }

    @Config(sdk = [35], qualifiers = "ja")
    @Test fun questionNotificationsInJapanese() {
        val five = Question(question = "どこ？", options = options("a", "b", "c", "d", "e"))
        assertEquals("どこ？\na / b / c / d ほか 1 個\nほかに 1 問", requestCopy(context, ask(five, Question(question = "x"))).second)
    }

    @Config(sdk = [35], qualifiers = "en")
    @Test fun requestNotificationsInEnglish() {
        assertEquals("Wants to use github · create issue: Fix login", requestCopy(context, mcpPermission).first)
        assertEquals("MCP form: Pick a repository", requestCopy(context, form).first)
    }

    @Config(sdk = [35], qualifiers = "ja")
    @Test fun requestNotificationsInJapanese() {
        assertEquals("github · create issueの使用をリクエスト：Fix login", requestCopy(context, mcpPermission).first)
        assertEquals("MCPフォーム：Pick a repository", requestCopy(context, form).first)
    }

    private fun buttons() = listOf(R.string.notify_action_deny, R.string.notify_action_allow, R.string.notify_action_view,
        R.string.notify_action_approve, R.string.notify_action_reject)
}
