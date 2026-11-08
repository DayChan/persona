package persona.platform.notify

import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.Interaction

@OptIn(ExperimentalCoroutinesApi::class)
class InteractionNotificationReconcilerTest {
    private fun ix(id: String) = Interaction(id, machineId = "machine", sessionId = "session", kind = "permission")
    private val settle = InteractionNotificationReconciler.SETTLE_MILLIS

    // A reconnect that did not resume: the refresh drops `a` (answered on the computer) without any resolved event.
    // `old` was left in the shade by an earlier process and is not pending either.
    @Test fun requestsNoLongerPendingLoseTheirNotificationsOnceOnline() = runTest {
        val showing = mutableSetOf("a", "b", "old")
        val pending = MutableStateFlow(listOf(ix("a"), ix("b")))
        val online = MutableStateFlow(false)
        val job = launch {
            InteractionNotificationReconciler({ showing.toSet() }, { showing -= it }).run(pending, online, MutableStateFlow(setOf(null)))
        }
        advanceTimeBy(settle + 1); runCurrent()
        // Offline (or before the first refresh): the list may be stale, nothing is compared.
        pending.value = listOf(ix("b"))
        advanceTimeBy(settle + 1); runCurrent()
        assertEquals(setOf("a", "b", "old"), showing)
        online.value = true
        advanceTimeBy(settle + 1); runCurrent()
        assertEquals(setOf("b"), showing)
        job.cancel()
    }

    // A list rebuilt under a group switch is empty for a moment: requests still pending keep their notifications.
    @Test fun aMomentaryDipIsNotARetraction() = runTest {
        val showing = mutableSetOf("a", "b")
        val pending = MutableStateFlow(listOf(ix("a"), ix("b")))
        val job = launch {
            InteractionNotificationReconciler({ showing.toSet() }, { showing -= it }).run(pending, MutableStateFlow(true), MutableStateFlow(setOf(null)))
        }
        advanceTimeBy(settle + 1); runCurrent()
        pending.value = emptyList()
        advanceTimeBy(settle / 2); runCurrent()
        pending.value = listOf(ix("a"), ix("b"))
        advanceTimeBy(settle + 1); runCurrent()
        assertEquals(setOf("a", "b"), showing)
        job.cancel()
    }

    // A group closed or merged into another: its requests left the list unanswered, so their notifications stay.
    // An open group's answered request still goes, and so does one before any machines are known.
    @Test fun requestsOfGroupsNotOpenAreLeftAlone() {
        val other = "a~gcafe00a1"
        val closed = "b~g89abcdef"
        assertEquals(setOf(other), InteractionNotificationReconciler.stale(setOf("p", other, closed), setOf("p"), setOf(null, "gcafe00a1")))
        assertEquals(emptySet<String>(), InteractionNotificationReconciler.stale(setOf("p", other), emptySet(), emptySet()))
    }

}
