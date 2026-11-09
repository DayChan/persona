package persona.core.domain

import persona.core.contracts.ProtocolCompatibility

import persona.core.contracts.Injected
import persona.core.contracts.Item

// Messages Persona types into a conversation for the model (a task's notice, a scheduled run, a wake-up, the
// background tasks a node restart stopped): the chat shows one line instead of a user bubble, never the instructions
// inside (docs/protocol/protocol.md "Messages Persona injects"). The hub marks them (Item.injected); an older hub does not, so the
// text is read the same way it does (prompts.ParseInjected; spec/fixtures/injected is shared with it).

private val noticeHead = Regex("^🔔 任务「(.+)」(做完了|在等用户|被模型服务出错打断了|没开起来)(?:（(.+)）)?$") // i18n-ignore: the injected text it reads (prompts.ParseInjected)
private val followupHead = Regex("^⏰ (.+)（(你之前安排的跟进|定时任务(?:，每 [^）]+)?)）$") // i18n-ignore: the injected text it reads (prompts.ParseInjected)
private val wakeHead = Regex("^⏰（Persona 叫醒你，第 \\d+ 次）你上一轮停下时说在等「(.*?)」") // i18n-ignore: the injected text it reads (prompts.ParseInjected)
private val leadContext = ProtocolCompatibility.leadContext
private const val NOTICE_MARKER = "这是 Persona 的任务通知" // i18n-ignore: the injected text it reads (prompts.ParseInjected)
private const val LOST_TASKS_HEAD = "⚠️ 运行你的 node 刚重启过" // i18n-ignore: the injected text it reads (prompts.ParseInjected)
private const val KB_HINT_HEAD = "（知识库" // i18n-ignore: the injected text it reads (prompts.ParseInjected)
private val noticeStates = mapOf("做完了" to "done", "在等用户" to "needs", "被模型服务出错打断了" to "interrupted", "没开起来" to "failed") // i18n-ignore: the injected text it reads (prompts.ParseInjected)
private val noticeLines = mapOf("done" to "做完了", "needs" to "在等你", "interrupted" to "被打断了", "failed" to "没开起来") // i18n-ignore: fallback for unmarked lines, as prompts.ParseInjected words it (spec/fixtures/injected); the hub sends Item.injected.line in the user's language

/** What [raw] is when it is one of Persona's messages for the model; null for anything else. */
fun parseInjected(raw: String): Injected? {
    var text = raw
    while (true) text = leadContext.find(text)?.let { text.substring(it.range.last + 1) } ?: break
    text = text.trim()
    val head = text.substringBefore('\n')
    val rest = if ('\n' in text) text.substringAfter('\n') else ""
    return when {
        head.startsWith("🔔 任务「") -> { // i18n-ignore: the injected text it reads (prompts.ParseInjected)
            val m = noticeHead.find(head) ?: return null
            if (NOTICE_MARKER !in rest) return null
            val (title, said, machine) = m.destructured
            val state = noticeStates.getValue(said)
            Injected("task_notice", state, title, machine.ifEmpty { null }, line = "任务「$title」${noticeLines.getValue(state)}") // i18n-ignore: fallback for unmarked lines, as prompts.ParseInjected words it (spec/fixtures/injected); the hub sends Item.injected.line in the user's language
        }
        head.startsWith("⏰（") -> wakeHead.find(head)?.let { // i18n-ignore: the injected text it reads (prompts.ParseInjected)
            val wait = it.groupValues[1]
            Injected("followup", "wake", wait, line = "Persona 叫醒了它：看看「$wait」好了没有") // i18n-ignore: fallback for unmarked lines, as prompts.ParseInjected words it (spec/fixtures/injected); the hub sends Item.injected.line in the user's language
        }
        head.startsWith("⏰ ") -> {
            val m = followupHead.find(head) ?: return null
            val (title, what) = m.destructured
            val detail = followupDetail(rest).ifEmpty { null }
            when {
                what == "你之前安排的跟进" -> Injected("followup", "once", title, line = "跟进「$title」到时间了", detail = detail) // i18n-ignore: fallback for unmarked lines, as prompts.ParseInjected words it (spec/fixtures/injected); the hub sends Item.injected.line in the user's language
                what.startsWith("定时任务，每 ") -> Injected("followup", "interval", title, line = "定时任务「$title」开始运行", detail = detail) // i18n-ignore: fallback for unmarked lines, as prompts.ParseInjected words it (spec/fixtures/injected); the hub sends Item.injected.line in the user's language
                else -> Injected("followup", "daily", title, line = "定时任务「$title」开始运行", detail = detail) // i18n-ignore: fallback for unmarked lines, as prompts.ParseInjected words it (spec/fixtures/injected); the hub sends Item.injected.line in the user's language
            }
        }
        head.startsWith(LOST_TASKS_HEAD) -> {
            val n = rest.split('\n').count { it.startsWith("- ") }
            Injected("system", "lost_tasks", line = if (n > 0) "node 重启过，$n 个后台任务停了" else "node 重启过，后台任务停了") // i18n-ignore: fallback for unmarked lines, as prompts.ParseInjected words it (spec/fixtures/injected); the hub sends Item.injected.line in the user's language
        }
        else -> null
    }
}

private fun followupDetail(rest: String): String {
    val s = "\n" + rest
    val at = s.indexOf("\n\n")
    if (at < 0) return ""
    var body = s.substring(at + 2).trim()
    val hint = body.lastIndexOf("\n\n" + KB_HINT_HEAD)
    if (hint >= 0) body = body.substring(0, hint).trim() else if (body.startsWith(KB_HINT_HEAD)) body = ""
    return body
}

/** [item] as one of Persona's messages for the model: the hub's mark, else read off the text. A line sent from an
 *  app (client_msg_id) or by another session is never one. */
fun injectedOf(item: Item): Injected? {
    if (item.role != "user" || item.kind != "text") return null
    item.injected?.let { return it }
    if (item.clientMsgId != null || item.from != null) return null
    val metadata = sourceMetadata(item.text.orEmpty())
    if (metadata.found) return null
    return parseInjected(item.text.orEmpty())
}

/** The task a task notice is about: the hub's todo id, else the newest task of that title opened from
 *  [conversation]; null when none is known. */
fun taskOfInjected(n: Injected, todos: List<persona.core.contracts.Todo>, conversation: String): String? {
    if (n.kind != "task_notice") return null
    n.todoId?.let { return it }
    return todos.filter { it.title == n.title && it.origin == conversation }.maxByOrNull { it.createdAt.orEmpty() }?.id
}
