package persona.platform.notify

import persona.core.contracts.Interaction
import persona.core.contracts.NotifyEvent
import persona.core.contracts.Resolution
import persona.core.contracts.SessionKey
import persona.core.domain.answer.AnswerEntry
import persona.core.domain.answer.AnswerRequirement
import persona.core.domain.answer.ApprovalAction
import persona.core.domain.answer.ApprovalPolicy
import persona.core.domain.answer.Authentication
import persona.core.domain.answer.AnswerAction
import persona.core.domain.answer.AnswerBuilder
import persona.core.domain.answer.AnswerContext
import persona.core.domain.answer.AnswerDraft
import persona.core.domain.answer.AnswerRetry
import persona.core.domain.answer.RequestExpiry
import persona.core.domain.answer.needsFieldValues
import androidx.annotation.StringRes
import java.time.Instant
import java.util.UUID

internal data class NotificationAction(
    val key: String,
    /** A question's or a dialog's own option; null when [labelRes] names a fixed button. */
    val label: String?,
    val authenticationRequired: Boolean,
    @StringRes val replyLabel: Int? = null,
    val answer: AnswerAction,
    val draft: AnswerDraft,
    val retry: AnswerRetry,
    @StringRes val labelRes: Int? = null,
)

/** Notification presentation only; action availability and authentication belong to ApprovalPolicy. */
internal object NotificationPolicy {
    fun expired(ix: Interaction, now: Instant = Instant.now()): Boolean = RequestExpiry.expired(ix.expiresAt, now)

    fun actions(ix: Interaction): List<NotificationAction> =
        ApprovalPolicy.actions(AnswerEntry.Notification, ix.kind, locked = true).sortedBy { order(it.id) }.flatMap { policy ->
            fun action(key: String, label: String?, answer: AnswerAction, draft: AnswerDraft = AnswerDraft(), @StringRes labelRes: Int? = null) =
                NotificationAction(
                    key, label, policy.authentication != Authentication.None,
                    // A password card takes nothing typed (#1137): no box beside it where a password could go by mistake.
                    when (policy.input.takeUnless { ix.isSecretRun }) { "message" -> R.string.notify_reply_deny_reason; "feedback" -> R.string.notify_reply_feedback; else -> null },
                    answer, draft, policy.retry, labelRes,
                )
            when (policy.id) {
                // A card with fields to fill in opens the app instead (#4987).
                ApprovalAction.Allow -> if (ix.needsFieldValues) emptyList()
                    else listOf(action(policy.id.wire, null, AnswerAction.Allow, labelRes = R.string.notify_action_allow))
                ApprovalAction.Deny -> listOf(action(policy.id.wire, null, AnswerAction.Deny, labelRes = R.string.notify_action_deny))
                ApprovalAction.ApproveEdits -> listOf(action(policy.id.wire, null, AnswerAction.ApproveEdits, labelRes = R.string.notify_action_approve))
                ApprovalAction.Reject -> listOf(action(policy.id.wire, null, AnswerAction.Reject, labelRes = R.string.notify_action_reject))
                ApprovalAction.ChooseOption -> when {
                    AnswerRequirement.SingleQuestionSingleChoice in policy.requirements ->
                        ix.questions?.singleOrNull()?.takeUnless { it.multiSelect }?.let { question ->
                            question.options.take(policy.maxOptions ?: 0).mapIndexed { index, option ->
                                action("answer.$index", option.label, AnswerAction.PickOption, AnswerDraft(optionIndex = index))
                            }
                        }.orEmpty()
                    AnswerRequirement.PlainDialogOption in policy.requirements -> ix.dialog?.options.orEmpty()
                        .filter { it.kind == null || it.kind == "" || it.kind == "plain" }
                        .take(policy.maxOptions ?: 0).map { action("option.${it.key}", it.label, AnswerAction.ScreenOption, AnswerDraft(option = it.key)) }
                    else -> emptyList()
                }
                else -> emptyList()
            }
        }

    /**
     * Saying no on the left, yes on the right ("Deny" / "Allow", "Send back" / "Approve"), as in Android's dialogs and on the ongoing
     * notification; a question's or a terminal dialog's options keep their own order. The policy's order only keeps its
     * fixtures stable.
     */
    private fun order(id: ApprovalAction) = when (id) {
        ApprovalAction.Deny, ApprovalAction.Reject -> 0
        ApprovalAction.Allow, ApprovalAction.ApproveEdits -> 1
        else -> 2
    }

    fun resolution(ix: Interaction, key: String, reply: String?): Resolution? {
        val action = actions(ix).firstOrNull { it.key == key } ?: return null
        if (reply.orEmpty().length > 4000) return null
        if (action.replyLabel == null && !reply.isNullOrBlank()) return null
        val draft = when (action.answer) {
            AnswerAction.Deny -> action.draft.copy(message = reply.orEmpty())
            AnswerAction.Reject -> action.draft.copy(feedback = reply.orEmpty())
            else -> action.draft
        }
        return AnswerBuilder.build(ix, action.answer, draft, AnswerContext(entry = AnswerEntry.Notification)).resolution
    }

    fun suppressed(session: SessionKey, foreground: Boolean, visibleSession: SessionKey?): Boolean =
        foreground && session == visibleSession

    fun channel(event: NotifyEvent): String? = when (event.kind) {
        "done" -> Notifier.CH_DONE
        "reply" -> Notifier.CH_DONE // an agent still waiting on the user's answer (#5096): with its finished turn's
        "assistant" -> Notifier.CH_ASSISTANT
        "alert" -> Notifier.CH_ALERT
        else -> null
    }

    fun session(event: NotifyEvent): SessionKey? =
        if (event.machineId.isBlank() || event.sessionId.isBlank()) null else SessionKey(event.machineId, event.sessionId)

    /** The tags of a session's result notifications (done, assistant): put away once it is seen. Its cards stay. */
    fun resultTags(session: SessionKey): List<String> = listOf("done", "assistant", "reply").map { "$it:${session.path}" }

    /** An alert with an id keeps one tag per id (another group's id carries its suffix); one without is its own. */
    fun tag(event: NotifyEvent): String = if (event.kind == "alert") event.id?.takeIf { it.isNotEmpty() }?.let { "alert:id:$it" } ?: "alert:${UUID.randomUUID()}"
        else "${event.kind}:${SessionKey(event.machineId, event.sessionId).path}"
}
