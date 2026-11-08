package persona.core.hub

import java.io.FilterInputStream
import java.io.InputStream
import kotlinx.serialization.ExperimentalSerializationApi
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromStream
import okhttp3.ResponseBody
import persona.core.contracts.JsonCodec

/** Bound both wire size and DOM amplification before the decoder allocates the next chunk. */
internal object BoundedInboxJson {
    const val MaxBytes = 4L * 1024 * 1024
    const val MaxStructureTokens = 100_000
    const val MaxDepth = 128

    private fun tooLarge(): Nothing = throw HubFailure(
        HubError.Protocol, message = "Inbox response exceeds the safe decoding limit",
        code = "response_too_large", retryable = false,
    )

    @OptIn(ExperimentalSerializationApi::class)
    fun read(body: ResponseBody?, checkActive: () -> Unit): JsonObject {
        checkActive()
        if (body == null) return JsonObject(emptyMap())
        if (body.contentLength() > MaxBytes) tooLarge()
        return LimitedStream(body.byteStream(), checkActive).buffered().use { input ->
            // Empty/whitespace responses historically mean an empty object (including 204).
            while (true) {
                input.mark(1)
                val first = input.read()
                if (first == -1) return@use JsonObject(emptyMap())
                if (first !in listOf(9, 10, 13, 32)) { input.reset(); break }
            }
            try { JsonCodec.decodeFromStream(JsonObject.serializer(), input) }
            catch (error: SerializationException) {
                // The streaming lexer wraps I/O failures; preserve our explicit size failure.
                throw generateSequence(error.cause) { it.cause }.filterIsInstance<HubFailure>().firstOrNull() ?: error
            }
        }
    }

    private class LimitedStream(input: InputStream, private val checkActive: () -> Unit) : FilterInputStream(input) {
        private var bytes = 0L
        private var tokens = 0
        private var depth = 0
        private var quoted = false
        private var escaped = false

        override fun read(): Int {
            checkActive()
            return `in`.read().also { if (it != -1) inspect(it) }
        }

        override fun read(buffer: ByteArray, offset: Int, length: Int): Int {
            checkActive()
            // Read at most one byte beyond the budget, even for a chunked or compressed body.
            val allowed = minOf(length.toLong(), MaxBytes - bytes + 1).toInt()
            val count = `in`.read(buffer, offset, allowed)
            if (count > 0) for (index in offset until offset + count) inspect(buffer[index].toInt() and 255)
            return count
        }

        private fun inspect(value: Int) {
            if (++bytes > MaxBytes) tooLarge()
            if (quoted) {
                if (escaped) escaped = false
                else if (value == 92) escaped = true
                else if (value == 34) quoted = false
                return
            }
            when (value) {
                34 -> quoted = true
                123, 91 -> { if (++depth > MaxDepth) tooLarge(); if (++tokens > MaxStructureTokens) tooLarge() }
                125, 93 -> depth--
                44, 58 -> if (++tokens > MaxStructureTokens) tooLarge()
            }
        }
    }
}
