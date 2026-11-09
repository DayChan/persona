package persona.core.domain

import persona.core.contracts.ProtocolCompatibility

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText

/**
 * One `<persona-context kind="…" title="…" url="…">body</persona-context>` block of a message: a piece of a page picked on
 * the phone, or part of what a Feishu `/agent` request hands its agent (instructions, where it was asked, the chat
 * before it, a document). [kind] is `context`, `edit`, `instructions`, `doc`…; [url] is null when missing or blank;
 * [body] may be empty, and the hub may have cut it (it then ends with the Chinese "…(truncated, N bytes total)" mark).
 */
data class MessageContext(val kind: String, val title: String, val url: String?, val body: String) {
    /** What a row calls it: the title, else the link, else "Quote". */
    val label: CoreText get() = (title.takeIf(String::isNotBlank) ?: url)?.let(CoreText::plain) ?: CoreText.of(Key.ContextQuote)
}

// The canonical context tag used by the hub, nodes and apps.
private val contextBlock = ProtocolCompatibility.contextBlock
private val contextAttribute = Regex("([\\w-]+)\\s*=\\s*(?:\"([^\"]*)\"|'([^']*)')")

/** Whether [text] has a context block's opening tag. */
fun hasContextTag(text: String): Boolean = ProtocolCompatibility.hasContextTag(text)

/**
 * The context blocks of [text] in order, and the text outside all of them (trimmed): what the user actually wrote,
 * the only part a bubble or a preview shows. Attributes may come in any order or be missing; they and the body are
 * read with the XML entities decoded, as iOS reads them.
 */
fun parseContexts(text: String): Pair<List<MessageContext>, String> {
    if (!hasContextTag(text)) return emptyList<MessageContext>() to text.trim()
    val contexts = contextBlock.findAll(text).map { block ->
        val attributes = contextAttribute.findAll(block.groupValues[1]).associate {
            it.groupValues[1] to decodeXmlEntities(it.groupValues[2].ifEmpty { it.groupValues[3] })
        }
        MessageContext(
            kind = attributes["kind"].orEmpty().ifBlank { "context" },
            title = attributes["title"].orEmpty().trim(),
            url = attributes["url"]?.trim()?.takeIf { it.isNotEmpty() },
            body = decodeXmlEntities(block.groupValues[2]).trim('\n', '\r').takeIf { it.isNotBlank() }.orEmpty(),
        )
    }.filterNot { it.kind == "message-source" }.toList()
    return contexts to contextBlock.replace(text, "").trim()
}

/** The five XML entities a context's attributes are written with; `&amp;` last, so `&amp;lt;` stays `&lt;`. */
fun decodeXmlEntities(value: String): String = value
    .replace("&quot;", "\"").replace("&apos;", "'")
    .replace("&lt;", "<").replace("&gt;", ">").replace("&amp;", "&")

/**
 * What a folded context row says: "Context · N items" for several, "Context · title" for one ("To change · title" when it
 * is a selection to edit).
 */
fun contextSummary(contexts: List<MessageContext>): CoreText = when (contexts.size) {
    0 -> CoreText.plain("")
    1 -> CoreText.of(if (contexts[0].kind == "edit") Key.ContextEditOne else Key.ContextOne, contexts[0].label)
    else -> CoreText.of(Key.ContextMany, contexts.size)
}
