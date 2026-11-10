package persona.feature.inbox

import kotlinx.serialization.Serializable

/** One source and where its last item was handed: "main" or "agent:<machine>/<session>" (iOS also keeps "new"). */
@Serializable
data class HandOffMemoryEntry(val source: String, val target: String)

/**
 * Where "Hand to AI" last sent an item from each source (#2121; docs/assistant/inbox.md "Item actions";
 * spec/fixtures/inbox/handoff_memory.json, which iOS and the web read too): the repository, else the chat, else the
 * sender (the item's mute options), in the hub group. The next item from that source starts on that target, marked
 * Last time; nothing is sent until the user sends. Kept on the device in the group's own data (gone with the group):
 * ids only, the [LIMIT] most recent sources, never synced.
 */
object InboxHandOffMemory {
    const val LIMIT = 200
    const val MAIN = "main"

    /** Most specific first: an item from a repository goes by the repository, not by who wrote it. */
    private val tags = listOf("repo", "chat", "sender")

    /** What to preselect: the target ("main" or "agent:…"), whether it is the remembered one, and whether a remembered
     *  Agent was gone so Persona stands in (said once). */
    data class Resolved(val target: String, val lastTime: Boolean, val gone: Boolean)

    /** The item's source key ("github|repo:example/weather"); null when it has none of the three (a Lark doc, …). */
    fun key(source: String, mutes: List<InboxMuteOption>?): String? {
        for (tag in tags) {
            val value = mutes?.firstOrNull { it.tag == tag && it.value.isNotEmpty() }?.value ?: continue
            return "$source|$tag:$value"
        }
        return null
    }

    fun agent(target: HandOffTarget): String = "agent:${target.key}"

    /**
     * The remembered target while it is still there. A remembered Agent no longer listed resolves to Persona's main
     * conversation (`gone`). Nothing remembered, a value that doesn't read, or New Chat (not a target here) is null: the
     * sheet's own default.
     */
    fun resolve(stored: String?, isListed: (String) -> Boolean): Resolved? {
        if (stored == MAIN) return Resolved(MAIN, lastTime = true, gone = false)
        if (stored == null || !stored.startsWith("agent:")) return null
        val path = stored.removePrefix("agent:")
        val slash = path.indexOf('/')
        if (slash <= 0 || slash == path.length - 1) return null
        if (!isListed(path)) return Resolved(MAIN, lastTime = false, gone = true)
        return Resolved(stored, lastTime = true, gone = false)
    }

    fun recall(entries: List<HandOffMemoryEntry>, key: String): String? = entries.firstOrNull { it.source == key }?.target

    /** The entries with [key] going to [target] now: latest first, one per source, at most [LIMIT]. */
    fun remember(entries: List<HandOffMemoryEntry>, key: String, target: String): List<HandOffMemoryEntry> =
        (listOf(HandOffMemoryEntry(key, target)) + entries.filter { it.source != key }).take(LIMIT)
}
