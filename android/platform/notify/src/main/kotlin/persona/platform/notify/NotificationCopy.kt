package persona.platform.notify

import android.content.Context
import androidx.annotation.StringRes
import persona.core.contracts.Interaction
import persona.core.contracts.Question
import persona.core.contracts.api.ConnectionState
import persona.core.designsystem.resolve

@StringRes
internal fun ongoingTitle(state: ConnectionState): Int = when (state) {
    ConnectionState.Online -> R.string.notify_connection_online
    ConnectionState.Connecting -> R.string.notify_connection_connecting
    ConnectionState.Updating -> R.string.notify_connection_updating
    ConnectionState.WaitingForNetwork -> R.string.notify_connection_waiting_network
    ConnectionState.NeedsLocalNetworkPermission -> R.string.notify_connection_local_network
    ConnectionState.CertificateMismatch -> R.string.notify_connection_certificate
    ConnectionState.Revoked -> R.string.notify_connection_revoked
}

/** The title and the detail of the notice that replaces a request's notification, as string resources. */
internal fun actionResultCopy(result: NotificationActionResult): Pair<Int, Int>? = when (result) {
    NotificationActionResult.Expired -> R.string.notify_result_expired_title to R.string.notify_result_expired_detail
    NotificationActionResult.Gone -> R.string.notify_result_gone_title to R.string.notify_result_gone_detail
    NotificationActionResult.Locked -> R.string.notify_result_locked_title to R.string.notify_result_locked_detail
    NotificationActionResult.Invalid -> R.string.notify_result_invalid_title to R.string.notify_result_invalid_detail
    NotificationActionResult.Duplicate -> R.string.notify_result_unconfirmed_title to R.string.notify_result_duplicate_detail
    NotificationActionResult.ReadOnly -> R.string.notify_result_readonly_title to R.string.notify_result_readonly_detail
    // Handled by Notifier.notSent: the request comes back with its buttons.
    NotificationActionResult.Submitted, NotificationActionResult.NotSent -> null
}

/**
 * The line under a request notification's title and the text it expands to, in [context]'s language: what a glance at
 * the lock screen should tell. An MCP tool reads "github · create issue" as on the Agents list, not its raw
 * `mcp__github__create_issue`; an MCP form says what it asks for (the server's message) instead of only that one waits.
 */
internal fun requestCopy(context: Context, ix: Interaction): Pair<String, String> {
    val headline = ix.headline.resolve(context)
    val detail = when (ix.kind) {
        // Without questions the hub's push says the summary (push.go), so this does too.
        "question" -> ix.questions?.takeIf { it.isNotEmpty() }?.let { questionBody(context, it) } ?: ix.summary.orEmpty()
        "plan" -> ix.plan ?: headline
        // The headline already carries the summary; the summary alone would drop which tool asks. A password card's
        // headline names only the computer (#1137): its summary (the purpose and every command) is the detail.
        "permission" -> if (ix.isSecretRun) ix.summary?.takeIf(String::isNotBlank) ?: headline else headline
        "screen" -> ix.dialog?.let { "${it.question}\n${it.detail.orEmpty()}" } ?: ix.screen.orEmpty()
        "elicitation" -> elicitationMessage(ix)?.let { context.getString(R.string.notify_body_elicitation, it) } ?: headline
        else -> headline
    }
    val line = if (ix.kind == "elicitation") detail.lineSequence().first() else headline
    return line to detail
}

private fun elicitationMessage(ix: Interaction): String? =
    listOf(ix.summary, ix.elicitation?.message).firstNotNullOfOrNull { it?.trim()?.takeIf(String::isNotEmpty) }

// The same body as the hub's push for a question (questionAlertBody, #4308): the first question, its choices (at most
// QUESTION_OPTIONS, each clipped) and how many questions follow, so a glance tells what can be picked (#4726).
private const val QUESTION_OPTIONS = 4
private const val QUESTION_OPTION_MAX = 20
// Unicode White_Space, as Go's strings.Fields, on both Android and the desktop JVM.
// Android rejects the Unicode character class flag; plain \\s differs on the desktop JVM.
private val UNICODE_SPACE = Regex("[\\p{Z}\\u0009-\\u000D\\u0085]+")

private fun questionBody(context: Context, questions: List<Question>): String {
    val lines = mutableListOf<String>()
    questions.first().question.trim().takeIf(String::isNotEmpty)?.let(lines::add)
    // A label's line break would break the notification's lines.
    val labels = questions.first().options.map { it.label.split(UNICODE_SPACE).filter(String::isNotEmpty).joinToString(" ") }
        .filter(String::isNotEmpty)
    if (labels.isNotEmpty()) {
        val shown = labels.take(QUESTION_OPTIONS).joinToString(" / ") { clip(it, QUESTION_OPTION_MAX) }
        val rest = labels.size - QUESTION_OPTIONS
        lines += if (rest > 0) "$shown ${context.getString(R.string.notify_question_more_options, rest)}" else shown
    }
    val rest = questions.size - 1
    if (rest > 0) lines += context.resources.getQuantityString(R.plurals.notify_question_more, rest, rest)
    return lines.joinToString("\n")
}

/** [s] cut to [max] characters (code points, not UTF-16 units) with an ellipsis, as the hub's textfmt.Clip. */
private fun clip(s: String, max: Int): String =
    if (s.codePointCount(0, s.length) <= max) s else s.substring(0, s.offsetByCodePoints(0, max)) + "…"
