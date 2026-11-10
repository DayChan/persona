package persona.core.designsystem

import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.ui.res.stringResource
import persona.core.domain.*
import persona.core.hub.HubError
import persona.core.hub.HubFailure

/**
 * What to tell the user after an operation failed. Pages show [localizedMessage] / [localizedActionLabel], and code
 * outside composition resolves [text] and [actionLabelRes] with its `Resources` (docs/protocol/i18n.md): there are no fixed
 * words to fall back on. [hubWords] is the hub's own sentence for the main conversation's coded refusals it words (in
 * the request's language), kept for reports; never the hub's raw error (see [operationFeedback]).
 */
data class OperationFeedback(val text: UiText, val action: FailureAction, val offline: Boolean = false, val hubWords: String? = null) {
    @get:StringRes val actionLabelRes: Int? get() = when (action) {
        FailureAction.None -> null
        FailureAction.Retry -> R.string.designsystem_retry
        FailureAction.Verify -> R.string.designsystem_feedback_verify
        FailureAction.PairAgain -> R.string.designsystem_feedback_pair_again
        FailureAction.OpenTerminal -> R.string.designsystem_feedback_open_terminal
        FailureAction.ReviewInput -> R.string.designsystem_feedback_review_input
        FailureAction.UpdateHub -> R.string.designsystem_feedback_update_hub
        FailureAction.GrantPermission -> R.string.designsystem_feedback_grant_permission
        FailureAction.AddComputer -> R.string.designsystem_readiness_action_add_computer
        FailureAction.ConfirmComputer -> R.string.designsystem_readiness_action_confirm_computer
        FailureAction.WakeComputer -> R.string.designsystem_readiness_action_wake_computer
        FailureAction.InstallClaude -> R.string.designsystem_readiness_action_install_claude
        FailureAction.LoginClaude -> R.string.designsystem_readiness_action_login_claude
        FailureAction.ConnectModel -> R.string.designsystem_readiness_action_connect_model
        FailureAction.EditProvider -> R.string.designsystem_readiness_action_edit_provider
    }

    val localizedMessage: String @Composable get() = text.text()
    val localizedActionLabel: String? @Composable get() = actionLabelRes?.let { stringResource(it) }
}

fun operationFeedback(error: Throwable): OperationFeedback {
    if (error is persona.core.contracts.api.InteractionGoneException)
        return operationFeedback(mapOperationFailure("already_handled", "none", false))
    if (error is persona.core.contracts.api.InteractionPromptChangedException)
        return operationFeedback(mapOperationFailure("prompt_changed", "none", false))
    val failure = HubFailure.from(error)
    val category = when (failure.kind) {
        HubError.PairingExpired, HubError.HubMismatch -> FailureCategory.Pairing
        HubError.CertificateMismatch -> FailureCategory.Certificate
        HubError.Incompatible, HubError.TlsRequired -> FailureCategory.Incompatible
        HubError.LocalNetworkPermission -> FailureCategory.LocalPermission
        HubError.NoNetwork, HubError.AddressNotFound, HubError.ConnectionRefused, HubError.Unreachable,
        HubError.Timeout, HubError.Transport -> FailureCategory.Network
        else -> FailureCategory.Other
    }
    val code = failure.code ?: when (failure.kind) {
        HubError.NoNetwork -> "no_network"
        HubError.RateLimited -> "rate_limited"
        else -> null
    }
    val delivered = if ((error as? persona.core.contracts.api.AgentOperationFailure)?.uncertain == true && failure.delivered == null)
        "unknown" else failure.delivered
    // The hub words these refusals in the request's language (docs/assistant/onboarding.md 4.6, apierr.codeActions); for the
    // other codes its `error` is raw detail (node_unreachable: "rpc: connection closed"), never shown.
    val words = failure.message.takeIf { code in HubWordedCodes && failure.status != null && it.isNotBlank() && !it.startsWith("HTTP ") }
    return operationFeedback(mapOperationFailure(code, delivered, failure.retryable, category), words)
}

/**
 * [hubWords]: the hub's own sentence for one of [HubWordedCodes], kept as [OperationFeedback.hubWords] (the page shows
 * the app's wording, [OperationFeedback.text]).
 */
fun operationFeedback(failure: OperationFailure, hubWords: String? = null): OperationFeedback {
    codedFeedback(failure.reason)?.let { id ->
        return OperationFeedback(UiText.Res(id), failure.action, failure.offline, hubWords)
    }
    val retry = failure.action == FailureAction.Retry
    val id = when (failure.reason) {
        FailureReason.Demo -> R.string.designsystem_feedback_demo
        FailureReason.PairingExpired -> R.string.designsystem_feedback_pairing_expired
        FailureReason.CertificateChanged -> R.string.designsystem_feedback_certificate_changed
        FailureReason.Incompatible -> R.string.designsystem_feedback_incompatible
        FailureReason.LocalPermission -> R.string.designsystem_feedback_local_permission
        FailureReason.InputDraft -> R.string.designsystem_feedback_input_draft
        FailureReason.DialogOpen -> R.string.designsystem_feedback_dialog_open
        FailureReason.StaleSession -> R.string.designsystem_feedback_stale_session
        FailureReason.Invalid -> R.string.designsystem_feedback_invalid
        FailureReason.IdReused -> R.string.designsystem_feedback_id_reused
        FailureReason.PromptChanged -> R.string.designsystem_feedback_prompt_changed
        FailureReason.Expired -> R.string.designsystem_feedback_expired
        FailureReason.RateLimited -> if (retry) R.string.designsystem_feedback_rate_limited_retry else R.string.designsystem_feedback_rate_limited
        FailureReason.NotDelivered -> R.string.designsystem_feedback_not_delivered
        FailureReason.Unknown -> R.string.designsystem_feedback_unknown
        FailureReason.Delivered -> R.string.designsystem_feedback_delivered
        FailureReason.Failed -> if (retry) R.string.designsystem_feedback_failed_retry else R.string.designsystem_feedback_failed
        else -> error("Coded refusals are worded by codedFeedback")
    }
    return OperationFeedback(UiText.Res(id), failure.action, failure.offline)
}

/** The refusals whose `error` the hub writes in plain words in the request's language (server apierr.codeActions). */
private val HubWordedCodes = setOf("no_machine", "machine_pending", "claude_missing", "launcher_missing")

/** The words of the main conversation's coded refusals. */
private fun codedFeedback(reason: FailureReason): Int? = when (reason) {
    FailureReason.KnowledgeDisabled -> R.string.designsystem_knowledge_disabled
    FailureReason.KnowledgeUnavailable -> R.string.designsystem_knowledge_unavailable
    FailureReason.KnowledgeReadOnly -> R.string.designsystem_knowledge_read_only
    FailureReason.NoMachine -> R.string.designsystem_feedback_no_machine
    FailureReason.MachinePending -> R.string.designsystem_feedback_machine_pending
    FailureReason.ComputerOffline -> R.string.designsystem_feedback_computer_offline
    FailureReason.ClaudeMissing -> R.string.designsystem_feedback_claude_missing
    FailureReason.LauncherMissing -> R.string.designsystem_feedback_launcher_missing
    FailureReason.NotLoggedIn -> R.string.designsystem_feedback_not_logged_in
    FailureReason.AuthFailed -> R.string.designsystem_feedback_auth_failed
    FailureReason.AssistantRestarting -> R.string.designsystem_feedback_assistant_restarting
    else -> null
}
