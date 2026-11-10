package persona.core.domain

/** Transport-independent decisions; human-readable server messages never affect recovery. */
enum class FailureCategory { Network, Pairing, Certificate, Incompatible, LocalPermission, Other }
enum class FailureAction {
    None, Retry, Verify, PairAgain, OpenTerminal, ReviewInput, UpdateHub, GrantPermission,
    // What the main conversation still needs (docs/assistant/onboarding.md 4.6): the step to open, not a retry.
    AddComputer, ConfirmComputer, WakeComputer, InstallClaude, LoginClaude, ConnectModel, EditProvider,
}
enum class FailureReason {
    Demo, PairingExpired, CertificateChanged, Incompatible, LocalPermission,
    InputDraft, DialogOpen, StaleSession, Invalid, IdReused, PromptChanged, Expired,
    RateLimited, NotDelivered, Unknown, Delivered, Failed,
    // The hub's coded refusals of the main conversation (docs/assistant/onboarding.md 4.6, 5.2).
    NoMachine, MachinePending, ComputerOffline, ClaudeMissing, LauncherMissing, NotLoggedIn, AuthFailed, AssistantRestarting,
    KnowledgeDisabled, KnowledgeUnavailable, KnowledgeReadOnly,
}

/**
 * The codes with which the hub refuses to start or answer in the main conversation until a step is done
 * (docs/assistant/onboarding.md 4.6): nothing was started, so the message waits for the user, not for the network.
 */
val SetupFailureCodes = setOf("no_machine", "machine_pending", "claude_missing", "launcher_missing", "not_logged_in", "auth_failed")
data class OperationFailure(val reason: FailureReason, val action: FailureAction, val offline: Boolean = false)

fun mapOperationFailure(
    code: String?, delivered: String?, retryable: Boolean,
    category: FailureCategory = FailureCategory.Other,
): OperationFailure {
    fun result(reason: FailureReason, action: FailureAction) = OperationFailure(reason, action)
    if (code == "demo") return result(FailureReason.Demo, FailureAction.None)
    if (code in setOf("pairing_expired", "unauthorized", "device_revoked") || category == FailureCategory.Pairing)
        return result(FailureReason.PairingExpired, FailureAction.PairAgain)
    if (category == FailureCategory.Certificate || code == "certificate_mismatch")
        return result(FailureReason.CertificateChanged, FailureAction.PairAgain)
    if (category == FailureCategory.Incompatible || code in setOf("incompatible", "tls_required"))
        return result(FailureReason.Incompatible, FailureAction.UpdateHub)
    if (category == FailureCategory.LocalPermission)
        return result(FailureReason.LocalPermission, FailureAction.GrantPermission)
    // Delivery evidence takes precedence over retryability and business refusal codes.
    if (delivered == "unknown") return result(FailureReason.Unknown, FailureAction.Verify)
    if (delivered == "yes") return result(FailureReason.Delivered, FailureAction.Verify)
    val business = when (code) {
        "kb_disabled" -> result(FailureReason.KnowledgeDisabled, FailureAction.None)
        "kb_unavailable" -> result(FailureReason.KnowledgeUnavailable, FailureAction.Retry)
        "kb_read_only" -> result(FailureReason.KnowledgeReadOnly, FailureAction.None)
        "input_draft" -> result(FailureReason.InputDraft, FailureAction.OpenTerminal)
        "dialog_open" -> result(FailureReason.DialogOpen, FailureAction.OpenTerminal)
        "stale_session" -> result(FailureReason.StaleSession, FailureAction.Verify)
        "op_id_reused", "client_id_reused" -> result(FailureReason.IdReused, FailureAction.Verify)
        "invalid", "invalid_request" -> result(FailureReason.Invalid, FailureAction.ReviewInput)
        "prompt_changed" -> result(FailureReason.PromptChanged, FailureAction.Verify)
        "expired", "request_expired", "already_handled" -> result(FailureReason.Expired, FailureAction.OpenTerminal)
        "rate_limited" -> result(FailureReason.RateLimited, if (retryable) FailureAction.Retry else FailureAction.None)
        "no_machine" -> result(FailureReason.NoMachine, FailureAction.AddComputer)
        "machine_pending" -> result(FailureReason.MachinePending, FailureAction.ConfirmComputer)
        // The computer is asleep or its Persona Link is off: the hub itself answered, so never "cannot reach hub".
        "node_unreachable" -> result(FailureReason.ComputerOffline, FailureAction.WakeComputer)
        "claude_missing" -> result(FailureReason.ClaudeMissing, FailureAction.InstallClaude)
        "launcher_missing" -> result(FailureReason.LauncherMissing, FailureAction.ConnectModel)
        "not_logged_in" -> result(FailureReason.NotLoggedIn, FailureAction.LoginClaude)
        "auth_failed" -> result(FailureReason.AuthFailed, FailureAction.EditProvider)
        // The assistant's process restarting: it comes back by itself in a few seconds.
        "assistant_offline" -> result(FailureReason.AssistantRestarting, FailureAction.Retry)
        else -> null
    }
    if (business != null) return business
    if (category == FailureCategory.Network) return OperationFailure(
        if (delivered in setOf("none", "no") || code == "no_network") FailureReason.NotDelivered else FailureReason.Unknown,
        if (delivered in setOf("none", "no") || code == "no_network") {
            if (retryable) FailureAction.Retry else FailureAction.None
        } else FailureAction.Verify,
        offline = true,
    )
    return result(FailureReason.Failed, if (retryable) FailureAction.Retry else FailureAction.None)
}
