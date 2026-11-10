package persona.core.hub.tunnel

import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.hub.tunnel.HubWay.Option
import persona.core.hub.tunnel.HubWay.Seen
import persona.core.hub.tunnel.HubWay.Step

/** The order ways to a hub are tried in (iOS `HubWay`). */
class HubWayTest {
    private val fallback = Option(always = false, recent = false)
    private val recent = Option(always = false, recent = true)
    private val always = Option(always = true, recent = false)

    @Test fun withoutAlwaysTheAddressesComeFirstAndAnAnswerClosesEveryTunnel() {
        val options = listOf(fallback, fallback)
        assertEquals(Step.AskDirect(emptyList()), HubWay.next(options, Seen()))
        assertEquals(Step.Direct, HubWay.next(options, Seen(direct = true)))
    }

    @Test fun noAnswerTriesTheTunnelsInOrderAndWatchesTheAddresses() {
        val options = listOf(fallback, fallback)
        assertEquals(Step.Open(0), HubWay.next(options, Seen(direct = false)))
        assertEquals(Step.Open(1), HubWay.next(options, Seen(direct = false, opened = mapOf(0 to false))))
        assertEquals(Step.Through(1, watchDirect = true), HubWay.next(options, Seen(direct = false, opened = mapOf(0 to false, 1 to true))))
        assertEquals(Step.None, HubWay.next(options, Seen(direct = false, opened = mapOf(0 to false, 1 to false))))
    }

    @Test fun aTunnelUsedLastTimeStartsWhileTheAddressesAreAskedAndIsTriedFirst() {
        val options = listOf(fallback, recent)
        assertEquals(Step.AskDirect(listOf(1)), HubWay.next(options, Seen()))
        assertEquals(Step.Through(1, watchDirect = true), HubWay.next(options, Seen(direct = false, opened = mapOf(1 to true))))
        assertEquals(Step.Open(0), HubWay.next(options, Seen(direct = false, opened = mapOf(1 to false))))
    }

    @Test fun alwaysGoesFirstAndIsNotLeftForTheAddresses() {
        val options = listOf(fallback, always)
        assertEquals(Step.Open(1), HubWay.next(options, Seen()))
        assertEquals(Step.Through(1, watchDirect = false), HubWay.next(options, Seen(opened = mapOf(1 to true))))
        assertEquals(Step.AskDirect(emptyList()), HubWay.next(options, Seen(opened = mapOf(1 to false))))
        assertEquals(Step.Open(0), HubWay.next(options, Seen(opened = mapOf(1 to false), direct = false)))
    }
}
