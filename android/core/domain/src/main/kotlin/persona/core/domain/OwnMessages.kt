package persona.core.domain

import persona.core.contracts.Item

/** The title of the context a message another Persona conversation sent carries (prompts/assistant/relay.md). */
private const val RELAY_TITLE = "来自对话「" // i18n-ignore: matches the hub's relay text (server/internal/prompts/assistant/relay.md, web/src/lib/turns.ts)

/** Sent by another Persona conversation (conversation_new / conversation_send), not typed by the user. */
fun isRelayed(text: String): Boolean =
    hasContextTag(text) && parseContexts(text).first.any { it.title.startsWith(RELAY_TITLE) }

/**
 * Typed by the user (or dictated, or a quick command expanded): the pale blue bubble (#2643). Persona's own messages
 * ([injectedOf]), another session's (`from`, `<cross-session-message>`) and another conversation's are not.
 */
fun isOwnMessage(item: Item): Boolean {
    if (item.role != "user" || sourceOf(item) != null || injectedOf(item) != null || !item.from.isNullOrBlank()) return false
    val text = item.text.orEmpty()
    val metadata = sourceMetadata(text)
    if (item.clientMsgId != null || (metadata.found && metadata.source == null)) return true
    return !text.contains("<cross-session-message") && !isRelayed(text)
}
