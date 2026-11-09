package persona.core.contracts

import java.time.Instant

/**
 * Finds a message sent from the phone in the agent's transcript. Hubs since #968 tag every item a message became with
 * its client message id ([Item.clientMsgId]), which decides alone. Older hubs don't, and Claude Code keeps its own id
 * for the user's message (not the client message id), so there the transcript's copy is found by its words: the same
 * rules as iOS (`PendingMessage.arrived` / `same` in apple/Shared/ChatStore.swift).
 */
object MessageEcho {
    /** The computer's clock may be behind the phone's by this much. */
    const val CLOCK_SLACK_MILLIS = 120_000L

    private val dropped = listOf(
        Regex("<attached_images>[\\s\\S]*?</attached_images>"),
        Regex("<[\\\\/]*pasted_content[^>]*>"),
        Regex("\\[Image #\\d+]"),
        Regex("\\s+"),
    )

    /** The words the hub adds where it cut a long text for the page (`…(truncated, N bytes total)` in Chinese, [CUT_MARK]). */
    private const val CUT_MARK = "\n…（已截断" // i18n-ignore: the hub's cut mark it strips

    /** Comparable form: the phone's image note, pasted wrappers and Claude's "[Image #21]" placeholders removed, no whitespace. */
    fun normalize(text: String): String = dropped.fold(text) { t, pattern -> pattern.replace(t, "") }

    /** [item]'s words without the hub's cut mark when the page cut it ([Item.fullSize]); its whole text otherwise. */
    fun uncut(item: Item): String = item.text.orEmpty().let { if (item.fullSize == null) it else it.substringBeforeLast(CUT_MARK) }

    /**
     * The transcript's copy of a message sent from here: equal once normalized, or one containing the other (Claude
     * adds placeholders, the phone adds notes). Containing only counts for longer texts, or a pending single-character
     * Chinese acknowledgement would be taken for any message with that character in it.
     */
    fun same(a: String, b: String): Boolean {
        val x = normalize(a)
        val y = normalize(b)
        if (x.isEmpty() || y.isEmpty()) return false
        return x == y || (minOf(x.length, y.length) >= 8 && (x.contains(y) || y.contains(x)))
    }

    /** The client message id the hub tagged [item] with (#968); null from older hubs and for items typed elsewhere. */
    fun tag(item: Item): String? = item.clientMsgId?.takeIf { it.isNotEmpty() }

    /** [item] may be the copy of a message sent from here: tagged (whatever its kind: a notice, an image), or a user text. */
    private fun userText(item: Item): Boolean = item.role == "user" && item.kind == "text" &&
        item.source == null && item.injected == null && item.from.isNullOrBlank()

    fun candidate(item: Item): Boolean = tag(item) != null || userText(item)

    /**
     * [item] carries the message sent as [clientMessageId]: tagged with it (a tagged item is no other message's), or,
     * untagged, a user text with it as its own id (runtimes that keep the client message id).
     */
    fun carries(item: Item, clientMessageId: String): Boolean =
        tag(item)?.let { it == clientMessageId } ?: (userText(item) && item.id == clientMessageId)

    /**
     * [item] is the message [text] written at [createdAt] (epoch millis): an untagged user message (a tagged one is
     * matched by [carries] only) that arrived after it was written (with [CLOCK_SLACK_MILLIS] for the computer's
     * clock), with its words — when the page cut a long one, its start. An older message with the same words (a second
     * "continue") is not it.
     */
    fun arrived(text: String, createdAt: Long, item: Item): Boolean {
        if (tag(item) != null || !userText(item)) return false
        val at = item.ts?.let { runCatching { Instant.parse(it).toEpochMilli() }.getOrNull() }
        if (at != null && at < createdAt - CLOCK_SLACK_MILLIS) return false
        val words = item.text.orEmpty()
        if (item.fullSize == null) return same(words, text)
        val start = normalize(uncut(item))
        return start.length >= 8 && normalize(text).startsWith(start)
    }
}
