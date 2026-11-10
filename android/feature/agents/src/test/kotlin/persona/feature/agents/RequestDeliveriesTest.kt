package persona.feature.agents

import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.Interaction

/** How an answer's delivery moves once its result is unknown (#3791). */
@OptIn(ExperimentalCoroutinesApi::class)
class RequestDeliveriesTest {
    private val card = Interaction("card", machineId = "m", sessionId = "s", kind = "permission")
    private val other = Interaction("other", machineId = "m", sessionId = "s", kind = "permission")

    @Test fun onlyARetryLeavesTheButtonsWorking() {
        assertFalse(RequestDeliveries.locks(null))
        assertFalse(RequestDeliveries.locks(RequestDelivery.Retry))
        assertTrue(RequestDeliveries.locks(RequestDelivery.Sending))
        assertTrue(RequestDeliveries.locks(RequestDelivery.Sent))
        assertTrue(RequestDeliveries.locks(RequestDelivery.Unknown))
    }

    /** The request sheet closes once the answer given since it opened is delivered; a history item stays (#2821). */
    @Test fun theSheetClosesWhenItsAnswerIsDelivered() {
        val cases = listOf(
            Triple(null, RequestDelivery.Sent, true),
            Triple(RequestDelivery.Retry, RequestDelivery.Sent, true),
            Triple(null, RequestDelivery.Sending, false),
            Triple(null, RequestDelivery.Unknown, false),
            Triple(RequestDelivery.Unknown, RequestDelivery.Retry, false),
            Triple(RequestDelivery.Sent, RequestDelivery.Sent, false),
            Triple(null, null, false),
        )
        for ((atOpen, now, closes) in cases) assertEquals("$atOpen -> $now", closes, RequestDeliveries.closesSheet(atOpen, now))
    }

    @Test fun aCheckThatFindsTheCardGoneEndsIt() {
        val after = RequestDeliveries.checked(mapOf("card" to RequestDelivery.Unknown), "card", DeliveryCheck.Gone)
        assertEquals(emptyMap<String, RequestDelivery>(), after)
    }

    @Test fun aCheckThatFindsTheCardStillThereLetsItBeAnsweredAgain() {
        val after = RequestDeliveries.checked(mapOf("card" to RequestDelivery.Unknown), "card", DeliveryCheck.StillPending)
        assertEquals(mapOf("card" to RequestDelivery.Retry), after)
    }

    @Test fun aFailedOrTimedOutCheckLetsItBeAnsweredAgain() {
        for (check in listOf(DeliveryCheck.Failed, DeliveryCheck.TimedOut)) {
            val after = RequestDeliveries.checked(mapOf("card" to RequestDelivery.Unknown), "card", check)
            assertEquals(check.name, mapOf("card" to RequestDelivery.Retry), after)
        }
    }

    @Test fun aCheckOnlyChangesAnUnknownEntry() {
        val sending = mapOf("card" to RequestDelivery.Sending)
        assertEquals(sending, RequestDeliveries.checked(sending, "card", DeliveryCheck.StillPending))
        assertEquals(emptyMap<String, RequestDelivery>(), RequestDeliveries.checked(emptyMap(), "card", DeliveryCheck.TimedOut))
    }

    @Test fun pruningDropsEntriesWhoseCardLeftButKeepsThoseInFlightOrBeingChecked() {
        val delivery = mapOf(
            "card" to RequestDelivery.Retry,
            "sent" to RequestDelivery.Sent,
            "retry" to RequestDelivery.Retry,
            "sending" to RequestDelivery.Sending,
            "unknown" to RequestDelivery.Unknown,
        )
        assertEquals(
            mapOf("card" to RequestDelivery.Retry, "sending" to RequestDelivery.Sending, "unknown" to RequestDelivery.Unknown),
            RequestDeliveries.prune(delivery, listOf(card, other)),
        )
    }

    @Test fun checkingWaitsForTheHubThenReportsWhetherTheCardIsThere() = runTest {
        val state = MutableStateFlow(ConnectionState.Connecting)
        var refreshed = 0
        val check = async { checkDelivery("card", state, 60_000) { refreshed++; listOf(card) } }
        advanceTimeBy(10_000); runCurrent()
        assertEquals("No refresh while the hub is out of reach", 0, refreshed)
        state.value = ConnectionState.Online
        assertEquals(DeliveryCheck.StillPending, check.await())
        assertEquals(1, refreshed)
        assertEquals(DeliveryCheck.Gone, checkDelivery("card", state, 60_000) { listOf(other) })
    }

    @Test fun aRefreshThatFailsIsAFailedCheck() = runTest {
        val state = MutableStateFlow(ConnectionState.Online)
        assertEquals(DeliveryCheck.Failed, checkDelivery("card", state, 60_000) { error("hub restarting") })
    }

    @Test fun aHubThatDoesNotComeBackTimesOut() = runTest {
        val state = MutableStateFlow(ConnectionState.Connecting)
        var refreshed = false
        assertEquals(DeliveryCheck.TimedOut, checkDelivery("card", state, 60_000) { refreshed = true; listOf(card) })
        assertFalse(refreshed)
        assertEquals(60_000L, testScheduler.currentTime)
    }
}
