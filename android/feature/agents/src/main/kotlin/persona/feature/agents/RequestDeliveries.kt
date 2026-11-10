package persona.feature.agents

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.withTimeoutOrNull
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.Interaction

/** What checking the hub after an unconfirmed answer found (#3791). */
internal enum class DeliveryCheck {
    /** The hub no longer lists the request: it took the answer, or the request ended. */
    Gone,
    /** The hub still lists the request: the answer may not have arrived. */
    StillPending,
    /** The hub could not be asked. */
    Failed,
    /** The hub did not come back, or did not answer, in time. */
    TimedOut,
}

/**
 * How answers on the Agents page move between [RequestDelivery] states. An unconfirmed answer is checked against the
 * hub's pending list and then either ends or lets the buttons work again. Answering a second time is safe because
 * the hub's resolve is idempotent: an answered request returns 200 `already`, an ended one returns 410 (docs/protocol/protocol.md).
 * iOS and the web put the card back the same way.
 */
internal object RequestDeliveries {
    /** How long an unconfirmed answer waits for the hub to come back and answer before its buttons work again. */
    const val CHECK_TIMEOUT_MILLIS = 60_000L

    /** Whether the request's buttons stay disabled: while an answer is in flight, landed or unconfirmed. */
    fun locks(delivery: RequestDelivery?): Boolean = delivery != null && delivery != RequestDelivery.Retry

    /**
     * Whether the request sheet closes: an answer was delivered since it opened ([atOpen] the delivery then). A sheet
     * opened on an answered request (a history item) stays open, read-only; one still sending, unconfirmed or to
     * retry stays too (#2821).
     */
    fun closesSheet(atOpen: RequestDelivery?, now: RequestDelivery?): Boolean =
        atOpen != RequestDelivery.Sent && now == RequestDelivery.Sent

    /**
     * Removes the entries whose request left the pending list so the map does not keep growing. An answer that is
     * still sending, or is waiting for its check, stays: the check decides what happens to it.
     */
    fun prune(delivery: Map<String, RequestDelivery>, pending: List<Interaction>): Map<String, RequestDelivery> {
        val ids = pending.mapTo(HashSet()) { it.id }
        return delivery.filter { (id, state) -> id in ids || state == RequestDelivery.Sending || state == RequestDelivery.Unknown }
    }

    /**
     * Applies a [check] to request [id]. A request that is gone ends. Any other result lets it be answered again.
     * Only an entry that is still [RequestDelivery.Unknown] changes.
     */
    fun checked(delivery: Map<String, RequestDelivery>, id: String, check: DeliveryCheck): Map<String, RequestDelivery> {
        if (delivery[id] != RequestDelivery.Unknown) return delivery
        return when (check) {
            DeliveryCheck.Gone -> delivery - id
            DeliveryCheck.StillPending, DeliveryCheck.Failed, DeliveryCheck.TimedOut -> delivery + (id to RequestDelivery.Retry)
        }
    }
}

/**
 * Waits for [connection] (the request's own hub group's) to be [ConnectionState.Online], then reads its pending list with [refresh] and reports whether
 * request [id] is still in it. Gives up after [timeoutMillis].
 */
internal suspend fun checkDelivery(
    id: String,
    connection: Flow<ConnectionState>,
    timeoutMillis: Long = RequestDeliveries.CHECK_TIMEOUT_MILLIS,
    refresh: suspend () -> List<Interaction>,
): DeliveryCheck = withTimeoutOrNull(timeoutMillis) {
    connection.first { it == ConnectionState.Online }
    try {
        if (refresh().none { it.id == id }) DeliveryCheck.Gone else DeliveryCheck.StillPending
    } catch (e: CancellationException) {
        throw e
    } catch (_: Exception) {
        DeliveryCheck.Failed
    }
} ?: DeliveryCheck.TimedOut
