package persona.feature.chat

import com.mikepenz.markdown.model.parseMarkdown
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Test
import persona.core.contracts.Item
import persona.core.designsystem.MathMarkdown
import java.io.File
import java.lang.management.ManagementFactory
import java.util.concurrent.atomic.AtomicInteger
import com.mikepenz.markdown.model.State as MarkdownResult

/**
 * The chat's Markdown parsing, timed: CPU and allocation summed over every thread of the JVM (ThreadMXBean), the cache
 * before this change (copied below) and this one in alternating rounds. Not a test: it runs only with PERSONA_BENCH=1
 * (`PERSONA_BENCH=1 ./gradlew :feature:chat:testDebugUnitTest --tests '*ChatMarkdownBench*' -i`), never in CI.
 */
class ChatMarkdownBench {
    @Before fun optIn() = assumeTrue("PERSONA_BENCH=1 runs the bench", System.getenv("PERSONA_BENCH") == "1")

    /** The cache as it was before single-flight, the kept prewarm and streaming tails (0453b49bc). */
    private class OldCache(private val parser: (String) -> MarkdownResult = { parseMarkdown(it) }) {
        private val entries = object : LinkedHashMap<String, Pair<String, MarkdownResult>>(80, 0.75f, true) {
            override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, Pair<String, MarkdownResult>>) = size > 80
        }
        @Synchronized fun cached(id: String, text: String) = entries[id]?.takeIf { it.first == text }?.second
        suspend fun parse(id: String, text: String): MarkdownResult {
            cached(id, text)?.let { return it }
            val parsed = withContext(Dispatchers.Default) { parser(ChatLinks.markdown(MathMarkdown.rewrite(text))) }
            synchronized(this) { entries[id] = text to parsed }
            return parsed
        }
    }

    private val mx = ManagementFactory.getThreadMXBean() as com.sun.management.ThreadMXBean

    private class Cost(val cpuMs: Double, val allocMb: Double)

    /** What [block] cost every thread: those it started count from zero. */
    private fun cost(block: () -> Unit): Cost {
        fun now(): Map<Long, LongArray> {
            val ids = mx.allThreadIds
            val cpu = mx.getThreadCpuTime(ids)
            val alloc = mx.getThreadAllocatedBytes(ids)
            return ids.indices.associate { ids[it] to longArrayOf(cpu[it].coerceAtLeast(0), alloc[it].coerceAtLeast(0)) }
        }
        val before = now()
        block()
        val after = now()
        var cpu = 0L
        var alloc = 0L
        for ((id, a) in after) {
            val b = before[id]
            cpu += a[0] - (b?.get(0) ?: 0)
            alloc += a[1] - (b?.get(1) ?: 0)
        }
        return Cost(cpu / 1e6, alloc / 1048576.0)
    }

    private fun load() = File("/proc/loadavg").takeIf { it.exists() }?.readText()?.trim().orEmpty()

    private val reply16k = ChatMarkdownStreamTest.reply(16_000)

    /** Eleven rounds, the arms' order alternating; each arm sets up (not timed) and returns what is timed, [reps] times. */
    private fun rounds(name: String, reps: Int, arms: Map<String, () -> (() -> Unit)>) {
        arms.values.forEach { arm -> repeat(maxOf(3, reps)) { arm()() } } // warm up
        println("BENCH $name load_before=${load()}")
        for (round in 1..11) {
            val order = if (round % 2 == 1) arms.keys.toList() else arms.keys.reversed()
            for (arm in order) {
                val runs = List(reps) { arms.getValue(arm)() }
                val c = cost { runs.forEach { it() } }
                println("BENCH $name round=$round arm=$arm cpu_ms=${"%.3f".format(c.cpuMs / reps)} alloc_mb=${"%.3f".format(c.allocMb / reps)}")
            }
        }
        println("BENCH $name load_after=${load()}")
    }

    /** [callers] callers of one 16 KB text at once, each dispatched alike, so one caller and two differ only by the second. */
    private fun callers(callers: Int, parse: suspend () -> Unit) = runBlocking { List(callers) { async(Dispatchers.Default) { parse() } }.awaitAll() }

    @Test fun twoCallersOfTheSameText() = rounds("concurrent_16k", 40, mapOf(
        "before_one" to { val c = OldCache(); { callers(1) { c.parse("a/0", reply16k) } } },
        "before_two" to { val c = OldCache(); { callers(2) { c.parse("a/0", reply16k) } } },
        "after_one" to { val c = ChatMarkdownCache(); { callers(1) { c.parse("a/0", reply16k) } } },
        "after_two" to { val c = ChatMarkdownCache(); { callers(2) { c.parse("a/0", reply16k) } } },
    ))

    @Test fun thePrewarmAfterOtherTexts() {
        val replies = (0 until 80).map { "a$it#0/0" to ChatMarkdownStreamTest.sample(it, 2_000, definitions = false) }
        for (others in listOf(1, 40)) {
            val misses = AtomicInteger()
            val counting: (String) -> MarkdownResult = { misses.incrementAndGet(); parseMarkdown(it) }
            // A prewarm's cycle over its 80 replies, then [others] other texts (a plan, a recap, thinking): what the next
            // cycle costs.
            val arms = mapOf<String, () -> (() -> Unit)>(
                "before" to {
                    val c = OldCache(counting)
                    runBlocking { replies.forEach { (id, t) -> c.parse(id, t) }; repeat(others) { c.parse("o/$it", "## Other $it") } };
                    { runBlocking { replies.forEach { (id, t) -> c.parse(id, t) } } }
                },
                "after" to {
                    val c = ChatMarkdownCache(counting).apply { keep(replies.map { it.first }) }
                    runBlocking { replies.forEach { (id, t) -> c.parse(id, t) }; repeat(others) { c.parse("o/$it", "## Other $it") } };
                    { runBlocking { replies.forEach { (id, t) -> c.parse(id, t) } } }
                },
            )
            for ((arm, setup) in arms) {
                val cycle = setup()
                misses.set(0)
                cycle()
                println("BENCH prewarm_misses others=$others arm=$arm misses=${misses.get()}")
            }
            rounds("prewarm_cycle_2k others=$others", 1, arms)
        }
    }

    @Test fun aReplyStreamedTo16kIn400Deltas() {
        val steps = (40..reply16k.length step 40).map { reply16k.substring(0, it) }
        rounds("stream_16k_400", 1, mapOf(
            "before" to { val c = OldCache(); { runBlocking { steps.forEach { c.parse("a/0", it) }; c.parse("a/0", reply16k) } } },
            "after" to { val c = ChatMarkdownCache(); { runBlocking { steps.forEach { c.parse("a/0", it, streaming = true) }; c.parse("a/0", reply16k, streaming = false) } } },
        ))
    }

    @Test fun thePrewarmSplitAsTheLastReplyStreams() {
        // A long conversation: 40 turns of a question, 25 tool calls and a reply; the last reply streams, 100 deltas.
        // Short replies (the size the issue measured) and 2 KB ones, the last growing to 4 KB.
        for ((name, reply) in listOf<Pair<String, (Int) -> String>>(
            "short" to { "Done with step $it. ".repeat(10) },
            "2k" to { ChatMarkdownStreamTest.sample(it, 2_000, definitions = false) },
        )) {
            var seq = 0
            val base = (0 until 40).flatMap { turn ->
                listOf(Item("u$turn#0", seq++, role = "user", kind = "text", text = "Question $turn")) +
                    (0 until 25).map { Item("t$turn-$it#0", seq++, role = "assistant", kind = "tool_use", toolName = "Read") } +
                    Item("a$turn#0", seq++, role = "assistant", kind = "text", text = reply(turn))
            }.dropLast(1)
            val last = if (name == "short") reply(39) + "w ".repeat(100) else reply16k.take(4_000)
            val updates = (0..100).map { base + Item("a39#0", seq, role = "assistant", kind = "text", text = last.take(last.length * it / 100)) }
            // Timed: the 100 deltas after the first (which the kept split splits whole).
            rounds("prewarm_split_${name}_100_updates", 1, mapOf(
                "before" to { { updates.drop(1).forEach { splitAfresh(it) } } },
                "after" to { val kept = MarkdownPrewarm().also { it.split(updates.first()) }; { updates.drop(1).forEach { kept.split(it) } } },
            ))
            println("BENCH prewarm_split_${name}_100_updates items=${updates.last().size} last_reply_chars=${last.length}")
        }
    }

    @Test fun retainedHeapPerEntry() {
        val rt = Runtime.getRuntime()
        fun used(): Long {
            repeat(4) { System.gc(); Thread.sleep(50) }
            return rt.totalMemory() - rt.freeMemory()
        }
        for (size in listOf(2_048, 16_384, 65_536)) {
            val n = if (size > 20_000) 20 else 60
            val texts = (0 until n).map { ChatMarkdownStreamTest.sample(it, size, definitions = false).take(size) }
            val before = used()
            val kept = texts.map { parseMarkdown(chatMarkdown(it)) }
            val after = used()
            println("BENCH retained size=$size entries=$n per_entry_kb=${"%.1f".format((after - before) / n / 1024.0)} " +
                "per_source_byte=${"%.1f".format((after - before).toDouble() / n / size)} (the parse and its Markdown; not the item's text)")
            check(kept.size == n)
        }
        // A full cache: the prewarm's 80 replies kept and its budget of others.
        for (size in listOf(2_048, 16_384)) {
            val texts = (0 until 80 + ChatMarkdownCache.OTHERS).map { ChatMarkdownStreamTest.sample(it, size, definitions = false).take(size) }
            val before = used()
            val cache = ChatMarkdownCache().apply { keep((0 until 80).map { "a$it" }) }
            runBlocking { texts.forEachIndexed { i, t -> cache.parse(if (i < 80) "a$i" else "o$i", t) } }
            val after = used()
            val held = texts.indices.count { cache.cached(if (it < 80) "a$it" else "o$it", texts[it]) != null }
            println("BENCH retained_full_cache size=$size entries=$held mb=${"%.1f".format((after - before) / 1048576.0)}")
        }
    }
}
