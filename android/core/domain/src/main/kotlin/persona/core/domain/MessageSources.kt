package persona.core.domain

import persona.core.contracts.Item
import persona.core.contracts.MessageSource
import persona.core.contracts.Injected
import persona.core.contracts.JsonCodec
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.contentOrNull

/** Provenance, never guessed from ordinary user prose. Explicit metadata wins even for future kinds. */
fun sourceOf(item: Item): MessageSource? {
    if (item.role != "user" || item.kind != "text") return null
    item.source?.let { return it }
    sourceFromInjected(item.injected)?.let { return it }
    // `from` is a peer's display name, not a Persona conversation identifier.
    item.from?.trim()?.takeIf(String::isNotEmpty)?.let { return MessageSource("peer", title = it) }
    if (item.clientMsgId != null) return null
    val metadata = sourceMetadata(item.text.orEmpty())
    if (metadata.found) return metadata.source
    relaySource(item.text.orEmpty())?.let { return it }
    return sourceFromInjected(injectedOf(item))
}

private fun sourceFromInjected(injected: Injected?): MessageSource? {
    val kind = when (injected?.kind) {
        "followup" -> if (injected.state in setOf("daily", "interval")) "patrol" else "followup"
        "routine", "patrol" -> "patrol"
        "schedule_followup", "limit_continue" -> "followup"
        "supervision" -> "supervision"
        "system" -> if (injected.state == "supervise") "supervision" else null
        else -> null
    } ?: return null
    return MessageSource(kind, title = injected?.title)
}

internal data class SourceMetadata(val found: Boolean, val source: MessageSource? = null)
private val leadingContext = Regex("""^\s*<persona-context\b([^>]*)>([\s\S]*?)</persona-context>\s*""")
private val sourceKind = Regex("\\bkind=\"message-source\"")
private val relayTitle = Regex("\\btitle=\"来自对话「(.*?)」\"") // i18n-ignore: canonical relay attribute
private val relayBody = Regex("""^这条消息不是用户本人发的，是 Persona 的另一段对话（([^\s（）]+)）用 (conversation_send|conversation_new) 发来的，第 ([0-9]+) 跳（最多 ([0-9]+) 跳）。\n按里面说的做。要把结果告诉那段对话就用 conversation_send 发回 ([^\s；]+)；只是确认收到就不要回，免得两段对话来回发。$""") // i18n-ignore: canonical relay template

/** The first persisted metadata block is authoritative; malformed metadata also fences text inference. */
internal fun sourceMetadata(raw: String): SourceMetadata {
    var text = raw
    while (true) {
        val block = leadingContext.find(text) ?: return SourceMetadata(false)
        if (sourceKind.containsMatchIn(block.groupValues[1])) {
            val meta = runCatching { JsonCodec.parseToJsonElement(block.groupValues[2].trim()) as? JsonObject }.getOrNull()
                ?: return SourceMetadata(true)
            val author = (meta["author"] as? kotlinx.serialization.json.JsonPrimitive)?.contentOrNull
            val kind = (meta["kind"] as? kotlinx.serialization.json.JsonPrimitive)?.contentOrNull
            if (author == "user" || kind.isNullOrBlank()) return SourceMetadata(true)
            return SourceMetadata(true, runCatching { JsonCodec.decodeFromJsonElement(MessageSource.serializer(), meta) }.getOrNull())
        }
        text = text.substring(block.range.last + 1)
    }
}

/** Only complete leading canonical relay blocks establish provenance, never titles alone. */
internal fun relaySource(raw: String): MessageSource? {
    var text = raw
    while (true) {
        val block = leadingContext.find(text) ?: return null
        val title = relayTitle.find(block.groupValues[1])
        if (title != null && block.groupValues[1].contains("kind=\"context\"")) {
            canonicalRelaySource(decodeXmlEntities(title.groupValues[1]), block.groupValues[2].trim())?.let { return it }
        }
        text = text.substring(block.range.last + 1)
    }
}

private fun canonicalRelaySource(title: String, body: String): MessageSource? {
    val match = relayBody.matchEntire(body) ?: return null
    if (match.groupValues[1] != match.groupValues[5]) return null
    return MessageSource("conversation", match.groupValues[1], title)
}

/** The verified control envelope does not appear again as a quoted context below its source label. */
fun isRelaySourceContext(context: MessageContext): Boolean = context.kind == "context" &&
    context.title.startsWith("来自对话「") && context.title.endsWith("」") && // i18n-ignore: canonical relay title
    canonicalRelaySource(context.title.removePrefix("来自对话「").removeSuffix("」"), context.body) != null // i18n-ignore: canonical relay title

/** Stable grouping includes the identity when present: two equally titled conversations stay separate. */
fun sameMessageSource(a: MessageSource?, b: MessageSource?): Boolean {
    if (a == null || b == null || a.kind != b.kind) return false
    val first = a.conversationId?.takeIf(String::isNotBlank)
    val second = b.conversationId?.takeIf(String::isNotBlank)
    return if (first != null || second != null) first == second else a.title?.trim().orEmpty() == b.title?.trim().orEmpty()
}
