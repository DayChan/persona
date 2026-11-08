package persona.core.hub

import java.io.ByteArrayInputStream
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import kotlinx.coroutines.*
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.*
import okhttp3.ResponseBody.Companion.toResponseBody
import okio.Buffer
import okio.BufferedSource
import okio.Source
import okio.Timeout
import okio.buffer
import okio.source
import org.junit.Assert.*
import org.junit.Test

class BoundedInboxJsonTest {
    private fun oversized(body: ResponseBody) {
        try { body.use { BoundedInboxJson.read(it) {} }; fail("Oversized JSON accepted") }
        catch (error: HubFailure) { assertEquals("response_too_large", error.code); assertFalse(error.retryable) }
    }

    @Test fun largeValidBodyAndQuotedStructuralCharactersKeepTheirContent() {
        val text = "[{},:\\\"]".repeat(100_000)
        val wire = persona.core.contracts.JsonCodec.encodeToString(kotlinx.serialization.json.JsonObject.serializer(),
            kotlinx.serialization.json.buildJsonObject { put("text", kotlinx.serialization.json.JsonPrimitive(text)) })
        val actual = wire.toResponseBody().use { BoundedInboxJson.read(it) {} }
        assertEquals(text, actual["text"]!!.jsonPrimitive.content)
        assertTrue(BoundedInboxJson.read(" \n\t".toResponseBody()) {}.isEmpty())
    }

    @Test fun declaredOversizeIsRejectedBeforeReading() {
        val body = object : ResponseBody() {
            override fun contentType(): MediaType? = null
            override fun contentLength() = BoundedInboxJson.MaxBytes + 1
            override fun source(): BufferedSource = error("Must reject the length before reading")
        }
        try { BoundedInboxJson.read(body) {}; fail("Oversized body accepted") }
        catch (error: HubFailure) { assertEquals("response_too_large", error.code) }
    }

    @Test fun unknownLengthStopsBeforeBufferingTheWholeResponse() {
        var emitted = 0L
        val source = object : Source {
            override fun timeout() = Timeout.NONE
            override fun close() = Unit
            override fun read(sink: Buffer, byteCount: Long): Long {
                if (emitted == 0L) { sink.writeUtf8("{\"text\":\""); emitted += 9; return 9 }
                val count = minOf(byteCount, 8192).toInt()
                sink.write(ByteArray(count) { 'x'.code.toByte() }); emitted += count
                return count.toLong()
            }
        }.buffer()
        oversized(object : ResponseBody() {
            override fun contentType(): MediaType? = null
            override fun contentLength() = -1L
            override fun source() = source
        })
        assertTrue(emitted <= BoundedInboxJson.MaxBytes + 8192)
    }

    @Test fun denseAndDeepSmallResponsesCannotAmplifyIntoUnboundedTrees() {
        oversized(("{\"items\":[" + "{},".repeat(BoundedInboxJson.MaxStructureTokens) + "{}]}").toResponseBody())
        oversized(("{\"items\":" + "[".repeat(BoundedInboxJson.MaxDepth + 1) +
            "0" + "]".repeat(BoundedInboxJson.MaxDepth + 1) + "}").toResponseBody())
    }

    @Test fun cancellationAndScopeChecksInterruptDecoding() {
        var checks = 0
        try {
            BoundedInboxJson.read(("{\"text\":\"" + "x".repeat(100_000) + "\"}").toResponseBody()) {
                if (++checks == 3) throw CancellationException("Pairing changed")
            }
            fail("Stale response accepted")
        } catch (_: CancellationException) { assertEquals(3, checks) }
    }

    @Test fun differentTransportInstancesDoNotBufferWhileAnotherParsesAndQueuedWorkCancels() = runBlocking {
        val reading = CompletableDeferred<Unit>()
        val release = CountDownLatch(1)
        val calls = AtomicInteger()
        val client = OkHttpClient.Builder().addInterceptor { chain ->
            calls.incrementAndGet()
            val stream = object : ByteArrayInputStream("{\"items\":[]}".toByteArray()) {
                override fun read(bytes: ByteArray, offset: Int, length: Int): Int {
                    reading.complete(Unit)
                    check(release.await(5, TimeUnit.SECONDS)) { "Test did not release the reader" }
                    return super.read(bytes, offset, length)
                }
            }
            val source = stream.source().buffer()
            Response.Builder().request(chain.request()).protocol(Protocol.HTTP_1_1).code(200).message("OK")
                .body(object : ResponseBody() {
                    override fun contentType(): MediaType? = null
                    override fun contentLength() = -1L
                    override fun source() = source
                }).build()
        }.build()
        val first = HttpInboxAssistantTransport("one", "https://example.com/", "fixture", client)
        val second = HttpInboxAssistantTransport("two", "https://example.com/", "fixture", client)
        val job = async { first.inbox() }
        try {
            withTimeout(5_000) { reading.await() }
            val queued = async(start = CoroutineStart.UNDISPATCHED) { second.inbox() }
            queued.cancelAndJoin()
            assertEquals(1, calls.get())
        } finally { release.countDown() }
        assertNotNull(job.await()["items"])
        assertNotNull(second.inbox()["items"])
        assertEquals(2, calls.get())
    }
}
