package persona.core.designsystem

import persona.core.hub.HubError
import persona.core.hub.HubFailure
import kotlinx.serialization.SerializationException
import java.io.IOException

/**
 * Why a call failed, for the part after "Failed: ": the hub's own words when it answered with a reason (409 "cannot start",
 * 400 "unknown engine", 503 "machine offline"), "cannot reach hub" when it did not answer, the app's usual sentence for pairing and
 * certificate trouble, an exception's own message for the app's typed failures.
 * Resolve it with [UiText.text] in composition or [UiText.resolve] outside it.
 */
fun failureReasonText(error: Throwable): UiText {
    if (error is IOException) {
        val failure = HubFailure.from(error)
        if (failure.code in setOf("kb_disabled", "kb_unavailable", "kb_read_only", "assistant_offline") ||
            failure.code == "internal" && (failure.status ?: 0) >= 500)
            return operationFeedback(error).text
        if (failure.status != null && failure.kind != HubError.PairingExpired) {
            failure.message.takeIf { it.isNotBlank() && !it.startsWith("HTTP ") }?.let { return UiText.Plain(it) }
        }
        return when (failure.kind) {
            HubError.NoNetwork, HubError.AddressNotFound, HubError.ConnectionRefused, HubError.Unreachable,
            HubError.Timeout, HubError.Transport -> UiText.Res(R.string.designsystem_reason_unreachable)
            else -> operationFeedback(error).text
        }
    }
    if (error is SerializationException) return UiText.Res(R.string.designsystem_reason_unreadable)
    return error.message?.takeIf { it.isNotBlank() }?.let { UiText.Plain(it) } ?: UiText.Res(R.string.designsystem_reason_unknown)
}

/** The hub's own words when it answered with a status (null when it did not), for a page that reads them itself. */
fun hubMessage(error: Throwable): String? {
    if (error !is IOException && generateSequence(error) { it.cause }.take(16).none { it is HubFailure }) return null
    val failure = HubFailure.from(error)
    return failure.message.takeIf { failure.status != null && it.isNotBlank() }
}

/** The node's own words under a summary the hub said in the app's language (#5514), for matching a known failure. */
fun hubOriginal(error: Throwable): String? =
    generateSequence(error) { it.cause }.take(16).filterIsInstance<HubFailure>().firstOrNull()?.original

/** What "Copy details" copies for a report: the hub's message, its HTTP status and error code, or the exception as it came. */
fun failureDetail(error: Throwable): String {
    val failure = generateSequence(error) { it.cause }.take(16).filterIsInstance<HubFailure>().firstOrNull()
    if (failure?.status == null) return error.message?.takeIf(String::isNotBlank) ?: error.toString()
    return buildString {
        if (failure.code != "internal") append(failure.message)
        if (isNotEmpty()) append("\n")
        append("HTTP ").append(failure.status)
        failure.code?.let { append("\n").append(it) }
        failure.requestId?.let { append("\nRequest: ").append(it) }
    }
}

/** How the hub answered a refused call: its status, its words, and whether the body was JSON (an old hub's 404 is not). */
data class HubAnswer(val status: Int, val message: String, val json: Boolean)

fun hubAnswer(error: Throwable): HubAnswer? {
    val failure = generateSequence(error) { it.cause }.take(16).filterIsInstance<HubFailure>().firstOrNull() ?: return null
    val status = failure.status ?: return null
    return HubAnswer(status, failure.message, failure.body?.trimStart()?.startsWith("{") == true)
}
