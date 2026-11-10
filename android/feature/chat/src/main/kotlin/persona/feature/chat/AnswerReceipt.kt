package persona.feature.chat

import android.content.res.Resources
import persona.core.contracts.Interaction
import persona.core.contracts.Resolution
import persona.core.designsystem.resolve
import persona.core.domain.answer.permissionSummary
import persona.core.domain.plainMarkdown

/**
 * What the app says right after an answer reached the hub (#4740, web #4356): which command was allowed, which option
 * was picked, which plan was approved and how (#5121), so a glance confirms it without opening the conversation.
 * Leave to computer says it was handed back, not that a choice was made. [text] is null when there is nothing of its
 * own to say (a form, a dialog answered only with keys); the caller then keeps its plain "Your choice was sent back".
 */
data class AnswerReceipt(val interaction: Interaction, val resolution: Resolution) {
    fun text(res: Resources): String? {
        if (resolution.cancel == true) return res.getString(R.string.chat_question_closed)
        if (resolution.dismiss == true) return res.getString(R.string.chat_answered_dismissed)
        return when (interaction.kind) {
            "permission" -> {
                val about = oneLine(interaction.permissionSummary().text.resolve(res)) ?: return null
                when (resolution.allow) {
                    true -> res.getString(if (resolution.always == true) R.string.chat_receipt_always_allowed else R.string.chat_receipt_allowed, about)
                    false -> res.getString(R.string.chat_receipt_denied, about)
                    null -> null
                }
            }
            "plan" -> when {
                resolution.approve == true -> {
                    val mode = AnsweredText.modeLabel(resolution.mode)?.let(res::getString)
                    val about = planLine()
                    when {
                        mode != null && about != null -> res.getString(R.string.chat_receipt_plan_approved_mode_about, mode, about)
                        mode != null -> res.getString(R.string.chat_receipt_plan_approved_mode, mode)
                        about != null -> res.getString(R.string.chat_receipt_plan_approved_about, about)
                        else -> res.getString(R.string.chat_receipt_plan_approved)
                    }
                }
                resolution.approve == false -> oneLine(resolution.feedback.orEmpty())
                    ?.let { res.getString(R.string.chat_receipt_plan_rejected_feedback, it) } ?: res.getString(R.string.chat_receipt_plan_rejected)
                else -> null
            }
            "question" -> {
                if (resolution.answers.orEmpty().values.none(String::isNotEmpty)) return null
                oneLine(AnsweredText.summary(res, interaction, resolution))?.let { res.getString(R.string.chat_receipt_answered, it) }
            }
            "screen" -> {
                // Only a picked option or typed text says something; keys alone (Esc) read as the old notice.
                val picked = !resolution.option.isNullOrEmpty() || !resolution.options.isNullOrEmpty() || !resolution.text.isNullOrEmpty()
                if (!picked) return null
                val answer = oneLine(AnsweredText.summary(res, interaction, resolution)) ?: return null
                // The dialog's question, so the receipt says what "Yes" was the answer to (web receiptAbout).
                val asked = oneLine(plainMarkdown(interaction.dialog?.question?.takeIf(String::isNotBlank) ?: interaction.summary.orEmpty()), ABOUT_MAX)
                if (asked == null) res.getString(R.string.chat_receipt_answered, answer)
                else res.getString(R.string.chat_receipt_answered_about, asked, answer)
            }
            else -> null
        }
    }

    /** The plan's first line without its heading marks, or the card's summary (web receiptAbout). */
    private fun planLine(): String? {
        val first = interaction.plan.orEmpty().lineSequence().firstOrNull(String::isNotBlank)?.let(::plainMarkdown)
        return oneLine(first?.takeIf(String::isNotBlank) ?: interaction.summary.orEmpty(), ABOUT_MAX)
    }

    private fun oneLine(text: String, max: Int = MAX): String? {
        val line = text.lineSequence().map(String::trim).firstOrNull(String::isNotEmpty) ?: return null
        return if (line.length > max) line.take(max) + "…" else line
    }

    private companion object {
        const val MAX = 120
        /** What a card was about is a label next to the answer, so it is cut shorter. */
        const val ABOUT_MAX = 60
    }
}
