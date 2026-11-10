package persona.core.domain.tasks

import java.time.Instant
import java.time.ZoneId
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import persona.core.contracts.FeedItem

/**
 * What Persona sent to its tasks' agents (`routed` items it wrote, #1542; docs/assistant/unified-entry.md "Messages to
 * agents"; iOS `SentToAgents`, web `takenIn` / `sentTo` in lib/feed.ts): never drawn as the user's bubble. A message on its
 * own is one folded line; a task's card lists every one sent to it, and the hand-off sent together with the card is drawn
 * only there.
 */
object SentToAgents {
    /** How long after its task's card a message of Persona's counts as the card's hand-off: agent_send with a task writes
     *  the card and, right after it, what it sent. */
    const val HAND_OFF_MILLIS = 60_000L

    /**
     * The hand-offs the task cards take in (not drawn on their own): Persona's messages within [HAND_OFF_MILLIS] after
     * their own task's card in the same conversation. Only a card drawn in full (from today) has the list: from another day
     * it is one line, and its hand-off stays a folded line under it (#1668).
     */
    fun takenIn(feed: List<FeedItem>, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): Set<String> {
        val today = now.atZone(zone).toLocalDate()
        val cards = HashMap<String, MutableList<Instant>>()
        for (item in feed) {
            if (item.kind != "task") continue
            val ref = item.ref ?: continue
            val at = AssistantLines.time(item.at) ?: continue
            if (at.atZone(zone).toLocalDate() != today) continue
            cards.getOrPut(item.conversation + "/" + ref) { ArrayList() } += at
        }
        if (cards.isEmpty()) return emptySet()
        val out = HashSet<String>()
        for (item in feed) {
            if (!item.sentByPersona) continue
            val ref = item.ref ?: continue
            val at = AssistantLines.time(item.at) ?: continue
            val ats = cards[item.conversation + "/" + ref] ?: continue
            if (ats.any { !it.isAfter(at) && at.toEpochMilli() - it.toEpochMilli() <= HAND_OFF_MILLIS }) out += item.id
        }
        return out
    }

    /** The feed without the hand-offs the cards take in: what the chat draws among its lines. */
    fun shown(feed: List<FeedItem>, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): List<FeedItem> {
        val hidden = takenIn(feed, now, zone)
        return if (hidden.isEmpty()) feed else feed.filter { it.id !in hidden }
    }

    /** Persona's messages to task [todo], oldest first: what its card lists. */
    fun sent(feed: List<FeedItem>, todo: String): List<FeedItem> =
        feed.filter { it.sentByPersona && it.ref == todo }
            .sortedWith(compareBy<FeedItem>({ AssistantLines.time(it.at) ?: Instant.EPOCH }, { it.id }))

    /** The first line with words in it, trimmed: what a folded message shows. */
    fun firstLine(text: String): String = text.lineSequence().map { it.trim() }.firstOrNull { it.isNotEmpty() }.orEmpty()
}

/** `routed`: Persona wrote it to the task's agent (agent_send with a task, agent_interrupt's then, a rework; #1542), so it
 *  is drawn folded as Persona's, never as the user's bubble. What the user sent has no `from`. */
val FeedItem.sentByPersona: Boolean get() = kind == "routed" && field("from") == "assistant"

/** `routed`: Persona cut the agent's turn short before sending (agent_interrupt with `then`). */
val FeedItem.interrupting: Boolean
    get() = ((body as? JsonObject)?.get("interrupted") as? JsonPrimitive)?.takeIf { !it.isString }?.booleanOrNull == true
