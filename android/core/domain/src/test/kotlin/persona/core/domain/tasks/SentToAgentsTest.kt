package persona.core.domain.tasks

import java.time.Instant
import java.time.ZoneOffset
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.FeedItem
import persona.core.contracts.JsonCodec

/** What Persona sent to its tasks' agents (#1542, #1623), as iOS SentToAgentsTests and web lib/feed takenIn / sentTo. */
class SentToAgentsTest {
    private val t0 = Instant.ofEpochSecond(1_800_000_000) // 08:00 UTC
    private fun takenIn(feed: List<FeedItem>, now: Instant = t0) = SentToAgents.takenIn(feed, now, ZoneOffset.UTC)
    private fun shown(feed: List<FeedItem>, now: Instant = t0) = SentToAgents.shown(feed, now, ZoneOffset.UTC).map { it.id }

    private fun item(id: String, kind: String, ref: String, afterMillis: Long, conversation: String = "c1", body: JsonElement? = null) =
        FeedItem(id, conversation, t0.plusMillis(afterMillis).toString(), kind, ref, body)

    private fun persona(id: String, ref: String, afterMillis: Long, conversation: String = "c1", text: String = "x", interrupted: Boolean? = null) =
        item(id, "routed", ref, afterMillis, conversation, buildJsonObject {
            put("text", JsonPrimitive(text)); put("from", JsonPrimitive("assistant"))
            if (interrupted != null) put("interrupted", JsonPrimitive(interrupted))
        })

    private fun user(id: String, ref: String, afterMillis: Long) =
        item(id, "routed", ref, afterMillis, body = buildJsonObject { put("text", JsonPrimitive("顺便看看日志")) })

    @Test fun onlyAMarkedRoutedItemIsPersonas() {
        val decoded = JsonCodec.decodeFromString<List<FeedItem>>("""
            [{"id":"a","conversation":"c1","at":"2026-11-08T12:00:00Z","kind":"routed","ref":"t","body":{"text":"把内存降下来","from":"assistant"}},
             {"id":"b","conversation":"c1","at":"2026-11-08T12:01:00Z","kind":"routed","ref":"t","body":{"text":"顺便看看日志"}},
             {"id":"c","conversation":"c1","at":"2026-11-08T12:02:00Z","kind":"routed","ref":"t","body":{"text":"先停","interrupted":true}},
             {"id":"d","conversation":"c1","at":"2026-11-08T12:03:00Z","kind":"report","ref":"t","body":{"text":"x","from":"assistant"}},
             {"id":"e","conversation":"c1","at":"2026-11-08T12:04:00Z","kind":"routed","ref":"t","body":{"text":"停下改","from":"assistant","interrupted":true}}]
        """.trimIndent())
        assertEquals(listOf(true, false, false, false, true), decoded.map { it.sentByPersona })
        assertEquals(listOf(false, false, true, false, true), decoded.map { it.interrupting })
        assertEquals("把内存降下来", decoded[0].field("text"))
    }

    @Test fun theCardTakesInOnlyItsOwnHandOff() {
        val feed = listOf(
            item("card", "task", "t1", 0),
            persona("handoff", "t1", 8),
            user("said", "t1", 1_000),                         // the user's own word, right after: stays a bubble
            persona("rework", "t1", 600_000),                   // later: a folded line of its own
            persona("other", "t2", 2_000),                      // another task, no card of its own here
            persona("elsewhere", "t3", 3_000, conversation = "c2"),
            item("card3", "task", "t3", 2_000),                 // t3's card is in c1, the message in c2
            persona("before", "t4", 4_000),
            item("card4", "task", "t4", 5_000),                 // written before its card: not a hand-off
        )
        assertEquals(setOf("handoff"), takenIn(feed))
        assertEquals(listOf("card", "said", "rework", "other", "elsewhere", "card3", "before", "card4"), shown(feed))
        assertEquals(listOf("handoff", "rework"), SentToAgents.sent(feed, "t1").map { it.id })
        assertEquals(listOf("card"), shown(listOf(item("card", "task", "t1", 0))))
    }

    @Test fun aHandOffComesWithinAMinuteOfItsCard() {
        assertTrue(takenIn(listOf(item("card", "task", "t1", 0), persona("late", "t1", SentToAgents.HAND_OFF_MILLIS + 1_000))).isEmpty())
        assertEquals(setOf("edge"), takenIn(listOf(item("card", "task", "t1", 0), persona("edge", "t1", SentToAgents.HAND_OFF_MILLIS))))
    }

    /** A card from another day is one line with no list (#1668): its hand-off stays a folded line under it. */
    @Test fun aCardOfAnotherDayGivesItsHandOffBack() {
        val feed = listOf(item("card", "task", "t1", 0), persona("handoff", "t1", 8))
        assertEquals(setOf("handoff"), takenIn(feed, t0.plusSeconds(15 * 3600))) // 23:00 the same day
        assertTrue(takenIn(feed, t0.plusSeconds(17 * 3600)).isEmpty())          // 01:00 the next day
        assertEquals(listOf("card", "handoff"), shown(feed, t0.plusSeconds(17 * 3600)))
    }

    @Test fun theListIsOldestFirst() {
        val feed = listOf(persona("b", "t1", 2_000), persona("a", "t1", 1_000), persona("a2", "t1", 1_000), user("u", "t1", 0))
        assertEquals(listOf("a", "a2", "b"), SentToAgents.sent(feed, "t1").map { it.id })
    }

    @Test fun theFoldedLineShowsTheFirstLineWithWords() {
        assertEquals("把内存降下来", SentToAgents.firstLine("\n  \n  把内存降下来  \n第二行"))
        assertEquals("", SentToAgents.firstLine(" \n\t"))
        assertEquals("one", SentToAgents.firstLine("one\r\ntwo"))
    }
}
