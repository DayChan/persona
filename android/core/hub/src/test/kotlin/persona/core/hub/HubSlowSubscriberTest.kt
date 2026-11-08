package persona.core.hub

import persona.core.contracts.Hello
import persona.core.contracts.ServerEvent
import persona.core.contracts.api.ConnectionState
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.dropWhile
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.onSubscription
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Collections
import java.util.concurrent.atomic.AtomicInteger

/**
 * A subscriber slow over an event (a reload over the network, a lock) must not hold up the socket's loop (#3627): over
 * real sockets, a hub that restarts or drops is reconnected to and the connection comes back Online while it waits.
 * Each assertion waits for the events it is about (a subscription, a socket's hello taken, an event handed over), not
 * for an amount of time, so its outcome does not depend on how busy the machine is.
 */
class HubSlowSubscriberTest {
    /**
     * Sockets whose hello the connection has taken. It calls onReady once it handled a socket's hello; the loopback hub
     * never resumes, so by then the socket started over and events of earlier sockets still queued are dropped.
     */
    private val ready = AtomicInteger()

    private fun connection(hub: LoopbackHub) = OkHttpHubConnection(
        HubConnectionConfig("test-hub", null, "test-token", listOf(hub.endpoint("lan")), "test"), HubNetwork("network"),
        onReady = { ready.incrementAndGet() })

    private suspend fun OkHttpHubConnection.awaitOnline(millis: Long = 10_000) =
        withTimeout(millis) { state.first { it == ConnectionState.Online } }

    /**
     * Waits until the hub opened its [n]th socket and the connection took that socket's hello. Online alone proves
     * little: a restart's grace keeps showing it.
     */
    private suspend fun awaitSocket(hub: LoopbackHub, n: Int) =
        eventually(10_000) { hub.opened.get() == n && ready.get() == n }

    /**
     * Sends one more event over the latest socket and returns what [subscriber] took once that event arrived. It is
     * queued after anything still on its way, so nothing else came.
     */
    private suspend fun closeOut(hub: LoopbackHub, subscriber: Subscriber): List<String> {
        check(hub.socket!!.send("""{"type":"inbox"}"""))
        eventually { subscriber.got.lastOrNull() == "InboxEvent" }
        return subscriber.got.toList()
    }

    /** A subscriber that takes each event's type, then waits on [gate] before taking the next. */
    private class Subscriber private constructor(connection: OkHttpHubConnection, private val gate: suspend () -> Unit) :
        AutoCloseable {
        val got: MutableList<String> = Collections.synchronizedList(mutableListOf())
        private val subscribed = CompletableDeferred<Unit>()
        private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        init {
            // The connection's forwarding coroutine hands the first socket's hello over on its own schedule, so it can
            // reach a subscriber that subscribed after Online: the record starts with the first event the test sends.
            scope.launch {
                connection.events.onSubscription { subscribed.complete(Unit) }.dropWhile { it is Hello }
                    .collect { event: ServerEvent -> got += event::class.simpleName.orEmpty(); gate() }
            }
        }
        override fun close() = scope.cancel()

        companion object {
            /** A subscriber that gets every event emitted from now on. */
            suspend fun subscribe(connection: OkHttpHubConnection, gate: suspend () -> Unit = {}) =
                Subscriber(connection, gate).also { withTimeout(5_000) { it.subscribed.await() } }
        }
    }

    @Test fun aHubRestartIsReconnectedToWhileASubscriberIsStuck() = runBlocking<Unit> {
        LoopbackHub().use { hub ->
            connection(hub).use { connection ->
                connection.awaitOnline()
                val stuck = CompletableDeferred<Unit>()
                Subscriber.subscribe(connection) { stuck.await() }.use { subscriber ->
                    // Never waits: it gets each event the moment the connection hands it over.
                    Subscriber.subscribe(connection).use { keepingUp ->
                        eventually { hub.opened.get() == 1 }
                        hub.socket!!.send("""{"type":"todos"}""")
                        eventually { subscriber.got.isNotEmpty() }
                        // The deploy: the hub says it is going away and closes the socket.
                        val start = System.currentTimeMillis()
                        hub.socket!!.close(1001, "hub restarting")
                        awaitSocket(hub, 2)
                        connection.awaitOnline()
                        println("reading: reconnected after a 1001 close in ${System.currentTimeMillis() - start} ms, subscriber stuck")
                        assertEquals(ConnectionState.Online, connection.state.value)
                        // The second socket's hello is being handed over: the subscriber keeping up has it, the stuck one
                        // has yet to take it.
                        eventually { keepingUp.got.toList() == listOf("TodosEvent", "Hello") }
                        // A loop stuck on the new hello would never open a third socket.
                        hub.socket!!.close(1001, "hub restarting")
                        awaitSocket(hub, 3)
                        stuck.complete(Unit)
                        // Let go, it gets the second socket's hello (already being handed over, not taken back), then the
                        // third's.
                        assertEquals(listOf("TodosEvent", "Hello", "Hello", "InboxEvent"), closeOut(hub, subscriber))
                    }
                }
            }
        }
    }

    @Test fun anOrdinaryDropIsReconnectedToWhileASubscriberIsStuck() = runBlocking<Unit> {
        LoopbackHub().use { hub ->
            connection(hub).use { connection ->
                connection.awaitOnline()
                val stuck = CompletableDeferred<Unit>()
                Subscriber.subscribe(connection) { stuck.await() }.use { subscriber ->
                    eventually { hub.opened.get() == 1 }
                    hub.socket!!.send("""{"type":"todos"}""")
                    eventually { subscriber.got.isNotEmpty() }
                    // Not a restart (no 1001): the ordinary drop, retried with the backoff.
                    val start = System.currentTimeMillis()
                    hub.socket!!.close(1011, "internal error")
                    awaitSocket(hub, 2)
                    connection.awaitOnline()
                    println("reading: reconnected after an ordinary drop (1011) in ${System.currentTimeMillis() - start} ms, subscriber stuck")
                    hub.socket!!.close(1011, "internal error")
                    awaitSocket(hub, 3)
                    stuck.complete(Unit)
                }
            }
        }
    }

    @Test fun restartsInARowAreEachReconnectedToWhileASubscriberStaysStuck() = runBlocking<Unit> {
        LoopbackHub().use { hub ->
            connection(hub).use { connection ->
                connection.awaitOnline()
                val stuck = CompletableDeferred<Unit>()
                Subscriber.subscribe(connection) { stuck.await() }.use { subscriber ->
                    Subscriber.subscribe(connection).use { keepingUp ->
                        eventually { hub.opened.get() == 1 }
                        hub.socket!!.send("""{"type":"todos"}""")
                        eventually { subscriber.got.isNotEmpty() }
                        val times = mutableListOf<Long>()
                        for (round in 2..4) {
                            val start = System.currentTimeMillis()
                            hub.socket!!.close(1001, "hub restarting")
                            awaitSocket(hub, round)
                            connection.awaitOnline()
                            times += System.currentTimeMillis() - start
                            // The second socket's hello is the one being handed over to the stuck subscriber.
                            if (round == 2) eventually { keepingUp.got.toList() == listOf("TodosEvent", "Hello") }
                            hub.socket!!.send("""{"type":"todos"}""")
                        }
                        println("reading: three restarts in a row reconnected in ${times.joinToString()} ms, subscriber stuck")
                        // Still on the first event the whole time.
                        assertEquals(1, subscriber.got.size)
                        stuck.complete(Unit)
                        // Then the second socket's hello (being handed over, not taken back) and the latest socket's events;
                        // the rest of the earlier sockets' were dropped when a later one started over (the loopback hub
                        // does not resume).
                        assertEquals(listOf("TodosEvent", "Hello", "Hello", "TodosEvent", "InboxEvent"), closeOut(hub, subscriber))
                    }
                }
            }
        }
    }

    @Test fun aSlowSubscriberGetsABurstInOrder() = runBlocking<Unit> {
        LoopbackHub().use { hub ->
            connection(hub).use { connection ->
                connection.awaitOnline()
                Subscriber.subscribe(connection) { delay(20) }.use { subscriber ->
                    eventually { hub.opened.get() == 1 }
                    repeat(20) { i -> hub.socket!!.send(if (i % 2 == 0) """{"type":"todos"}""" else """{"type":"inbox"}""") }
                    eventually { subscriber.got.size >= 20 }
                    assertEquals(List(10) { listOf("TodosEvent", "InboxEvent") }.flatten(), subscriber.got.toList())
                }
            }
        }
    }

    @Test fun aSlowSubscriberDoesNotDelayTheReconnect() = runBlocking<Unit> {
        LoopbackHub().use { hub ->
            connection(hub).use { connection ->
                connection.awaitOnline()
                // Slow at a pace the test sets: it takes the next event only when given a permit, and every event once
                // the permits are closed.
                val permits = Channel<Unit>(Channel.UNLIMITED)
                Subscriber.subscribe(connection) { permits.receiveCatching() }.use { subscriber ->
                    eventually { hub.opened.get() == 1 }
                    // Twenty events, far more than the subscriber is let take before the restart.
                    repeat(20) { hub.socket!!.send("""{"type":"todos"}""") }
                    eventually { subscriber.got.size == 1 }
                    permits.send(Unit)
                    eventually { subscriber.got.size == 2 }
                    val start = System.currentTimeMillis()
                    hub.socket!!.close(1001, "hub restarting")
                    // A reconnect that waited for the subscriber would never get here.
                    awaitSocket(hub, 2)
                    connection.awaitOnline()
                    val reconnect = System.currentTimeMillis() - start
                    val taken = subscriber.got.size
                    println("reading: reconnected in $reconnect ms with $taken of 21 events taken by a slow subscriber")
                    assertEquals("events taken when the reconnect completed", 2, taken)
                    // The old socket's events still queued are dropped once the new one starts over; its hello comes last.
                    permits.close()
                    eventually(5_000) { subscriber.got.lastOrNull() == "Hello" }
                    val got = subscriber.got.toList()
                    assertEquals(List(got.size - 1) { "TodosEvent" } + "Hello", got)
                    assertTrue("old events delivered after the restart: ${got.size - 1}", got.size - 1 < 20)
                }
            }
        }
    }
}
