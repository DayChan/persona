package persona.feature.chat

import com.mikepenz.markdown.model.parseMarkdown
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.SessionPathStat
import persona.core.contracts.api.SessionPathStats
import java.util.concurrent.CountDownLatch
import java.util.concurrent.atomic.AtomicInteger
import com.mikepenz.markdown.model.State as MarkdownResult

class ChatMarkdownCacheTest {
    private val parses = AtomicInteger()
    private val started = CompletableDeferred<Unit>()
    private val release = CountDownLatch(1)

    /** A cache whose parses count, and wait for [release] when [held]. */
    private fun cache(held: Boolean = false) = ChatMarkdownCache({ markdown ->
        parses.incrementAndGet()
        started.complete(Unit)
        if (held) release.await()
        parseMarkdown(markdown)
    })

    @Test fun aRowAndThePrewarmAskingAtOnceShareOneParse() = runBlocking {
        val cache = cache(held = true)
        val first = async(Dispatchers.Default) { cache.parse("a1#0/0", "## Hello") }
        started.await()
        // Undispatched: it has asked, and waits, before the parse is let go.
        val second = async(start = CoroutineStart.UNDISPATCHED) { cache.parse("a1#0/0", "## Hello") }
        release.countDown()
        assertSame(first.await(), second.await())
        assertEquals(1, parses.get())
        assertSame(first.await(), cache.cached("a1#0/0", "## Hello"))
    }

    @Test fun aCallerThatLeavesDoesNotCancelTheParseForTheOthers() = runBlocking {
        val cache = cache(held = true)
        val leaving = async(Dispatchers.Default) { cache.parse("a1#0/0", "## Hello") }
        started.await()
        leaving.cancel()
        val staying = async(start = CoroutineStart.UNDISPATCHED) { cache.parse("a1#0/0", "## Hello") }
        release.countDown()
        assertTrue(staying.await() is MarkdownResult.Success)
        assertEquals(1, parses.get())
        assertTrue(leaving.isCancelled)
        assertSame(staying.await(), cache.cached("a1#0/0", "## Hello"))
    }

    @Test fun anIdKeepsOnlyItsLatestText() = runBlocking {
        val cache = cache()
        val first = cache.parse("a1#0/0", "one")
        assertSame(first, cache.parse("a1#0/0", "one"))
        cache.parse("a1#0/0", "two")
        assertNull(cache.cached("a1#0/0", "one"))
        assertNotNull(cache.cached("a1#0/0", "two"))
        assertEquals(2, parses.get())
    }

    @Test fun thePrewarmedRepliesStayWarmWithOtherTextsParsedBetween() = runBlocking {
        val cache = cache()
        val replies = (0 until 80).map { "a$it#0/0" to "## Reply $it\n\nwith **words**" }
        // A plan, a recap or thinking parsed between two prewarms (1, then 40 more).
        for ((round, others) in listOf(1, 40).withIndex()) {
            cache.keep(replies.map { it.first })
            replies.forEach { (id, text) -> cache.parse(id, text) }
            repeat(others) { cache.parse("other$round/$it", "text $it") }
            val before = parses.get()
            replies.forEach { (id, text) -> cache.parse(id, text) }
            assertEquals("misses after $others other texts", before, parses.get())
        }
        // The other texts keep to their budget, the least recently used going first.
        assertNull(cache.cached("other1/7", "text 7"))
        assertNotNull(cache.cached("other1/8", "text 8"))
        // Replies the prewarm no longer asks for go the same way.
        cache.keep(emptyList())
        repeat(ChatMarkdownCache.OTHERS) { cache.parse("more/$it", "more $it") }
        assertNull(cache.cached(replies.last().first, replies.last().second))
        assertNotNull(cache.cached("more/0", "more 0"))
    }

    @Test fun theRowFindsThePrewarmsParseOfItsLinkedPaths() = runBlocking {
        val cache = cache()
        val presence = PathPresence(this, { paths -> SessionPathStats(paths.map { SessionPathStat(it, "file") }) })
        val text = "改了 `src/a.ts:12`，见 src/a.ts:40。"
        presence.ask(PathLinks.asked(text))
        withTimeout(10_000) { while (!presence.linkable("src/a.ts")) delay(5) }
        val warmed = MarkdownText("a1#0/0", text)
        cache.parse(warmed.key, warmed.source(presence))
        // What ChatMarkdownBody parses the row as.
        val source = PathLinks.markdown(text, PathLinks.asked(text).filterTo(LinkedHashSet(), presence::linkable))
        assertNotEquals(text, source)
        assertNotNull(cache.cached("a1#0/0", source)) // its first frame draws Markdown
        cache.parse("a1#0/0", source, streaming = false)
        cache.parse(warmed.key, warmed.source(presence)) // the next prewarm
        cache.parse("a1#0/0", source, streaming = false)
        assertEquals(1, parses.get())
        assertEquals(text, warmed.source(null)) // a page that opens no paths
    }

    @Test fun aStreamingReplyParsesItsTailAndWholeOnceItEnds() = runBlocking {
        val cache = cache()
        val text = ChatMarkdownStreamTest.reply(8_000) + "see https://example.com/x?q=1 and \$x^2\$ done"
        for (end in 40..text.length step 40) {
            val prefix = text.substring(0, end)
            val parsed = cache.parse("a1#0/0", prefix, streaming = true)
            ChatMarkdownStreamTest.assertParsedAlike(parseMarkdown(chatMarkdown(prefix)), parsed, "the first $end characters")
        }
        cache.parse("a1#0/0", text, streaming = true)
        cache.parse("a1#0/0", text) // the page's prewarm goes with the row
        assertEquals(1, parses.get())
        val final = cache.parse("a1#0/0", text, streaming = false)
        assertEquals(2, parses.get())
        ChatMarkdownStreamTest.assertParsedAlike(parseMarkdown(chatMarkdown(text)), final, "the whole reply")
        assertSame(final, cache.parse("a1#0/0", text))
        assertSame(final, cache.parse("a1#0/0", text, streaming = false))
        assertEquals(2, parses.get())
    }
}
