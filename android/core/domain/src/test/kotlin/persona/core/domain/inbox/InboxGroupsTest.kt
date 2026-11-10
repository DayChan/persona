package persona.core.domain.inbox

import java.time.Instant
import java.time.LocalDateTime
import java.time.ZoneId
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * The inbox's groups (#2059, #2576), as web lib/inbox-groups.test.ts: the time boundaries (calendar days; just now only
 * in Done; the last week by day, then 7+ days and 1+ month), the source order, one group means none, the folds and ticks.
 */
class InboxGroupsTest {
    private val zone = ZoneId.of("Asia/Shanghai")
    private fun local(month: Int, day: Int, hour: Int, minute: Int = 0) = LocalDateTime.of(2026, month, day, hour, minute).atZone(zone).toInstant()
    private val now = local(11, 9, 15) // Mon 9 Nov 2026, 15:00
    private fun ago(days: Long, hours: Long = 0, minutes: Long = 0): Instant =
        now.atZone(zone).minusDays(days).minusHours(hours).minusMinutes(minutes).toInstant()
    private fun key(at: Instant?, recent: Boolean = false) = InboxGroups.timeKey(at, now, zone, recent)

    @Test fun calendarDaysNot24Hours() {
        assertEquals("today", key(local(11, 9, 0, 5)))
        assertEquals("yesterday", key(local(11, 8, 23, 55)))
        assertEquals("d:2026-11-07", key(local(11, 7, 9)))
        assertEquals("d:2026-11-03", key(local(11, 3, 9))) // 6 days: still its own day
        assertEquals("week", key(local(11, 2, 9)))          // 7 days
        assertEquals("week", key(local(10, 11, 9)))         // 29 days
        assertEquals("month", key(local(10, 10, 9)))        // 30 days
    }

    @Test fun justNowOnlyInDoneAndWithinTheHour() {
        assertEquals("today", key(ago(0, 0, 30)))
        assertEquals("just", key(ago(0, 0, 30), recent = true))
        assertEquals("today", key(ago(0, 1, 5), recent = true))
    }

    @Test fun theFutureIsTodayAndNoTimeGoesLast() {
        assertEquals("today", key(now.plusSeconds(3600)))
        assertEquals("month", key(null))
    }

    @Test fun doneNewestFirst() {
        val done = listOf("m" to ago(40), "d5" to ago(5), "j" to ago(0, 0, 10), "w" to ago(9), "y" to ago(1), "t" to ago(0, 3), "d2" to ago(2))
        val groups = InboxGroups.done(done, now, zone) { it.second }
        assertEquals(listOf("just", "today", "yesterday", "d:2026-11-07", "d:2026-11-04", "week", "month"), groups.map { it.key })
        assertEquals(listOf(listOf("j"), listOf("t"), listOf("y"), listOf("d2"), listOf("d5"), listOf("w"), listOf("m")),
            groups.map { g -> g.items.map { it.first } })
    }

    @Test fun bySourceInTheFiltersOrderItemsInTheirOrder() {
        val list = listOf("g1" to InboxGroups.sourceKey("github", "gh", connector = true), "m1" to InboxGroups.sourceKey("mail", "acc1", false),
            "l1" to "lark", "x1" to InboxGroups.sourceKey("moments", "x", false), "a1" to InboxGroups.AGENTS, "l2" to "lark",
            "m2" to InboxGroups.sourceKey("mail", "acc2", false))
        val groups = InboxGroups.bySource(list) { it.second }
        assertEquals(listOf("agents", "lark", "mail:acc1", "mail:acc2", "c:gh", "moments"), groups.map { it.key })
        assertEquals(listOf("l1", "l2"), groups[1].items.map { it.first })
        assertEquals("other", InboxGroups.sourceKey("", "", false))
    }

    @Test fun oneGroupIsNoGroup() {
        assertEquals(emptyList<InboxGroups.Group<String>>(), InboxGroups.shown(InboxGroups.bySource(listOf("a", "b")) { "lark" }))
        assertEquals(2, InboxGroups.shown(InboxGroups.byTime(listOf(ago(0), ago(1)), now, zone) { it }).size)
    }

    @Test fun openAtFirstAndTheFoldsKept() {
        assertEquals(true, InboxGroups.openAtFirst("action", "lark"))
        assertEquals(listOf(true, true, false, false), listOf("just", "today", "yesterday", "week").map { InboxGroups.openAtFirst("done", it) })
        val folded = InboxGroups.toggle("action", "lark", emptyList())
        assertEquals(false, InboxGroups.isOpen("action", "lark", folded))
        assertEquals(true, InboxGroups.isOpen("action", "lark", InboxGroups.toggle("action", "lark", folded)))
        val opened = InboxGroups.toggle("done", "week", emptyList())
        assertEquals(true, InboxGroups.isOpen("done", "week", opened))
        // Expand all, then collapse all: Done's flips are only what differs from how they start.
        val keys = listOf("just", "today", "week")
        val all = InboxGroups.setAll("done", keys, open = true, flipped = listOf("action/lark"))
        assertEquals(setOf("action/lark", "done/week"), all.toSet())
        assertEquals(setOf("action/lark", "done/just", "done/today"), InboxGroups.setAll("done", keys, open = false, flipped = all).toSet())
    }

    @Test fun aGroupsTick() {
        val group = listOf("a", "b", "c")
        assertEquals(InboxGroups.Tick.None, InboxGroups.tick(group, setOf("x")))
        assertEquals(InboxGroups.Tick.Some, InboxGroups.tick(group, setOf("a")))
        assertEquals(InboxGroups.Tick.All, InboxGroups.tick(group, setOf("a", "b", "c", "x")))
        assertEquals(setOf("a", "b", "c", "x"), InboxGroups.pickGroup(group, setOf("a", "x")))
        assertEquals(setOf("x"), InboxGroups.pickGroup(group, setOf("a", "b", "c", "x")))
        assertEquals(InboxGroups.Tick.None, InboxGroups.tick(emptyList<String>(), emptySet()))
    }

    @Test fun aDayLandsWhereItsMomentsDo() {
        val today = local(11, 9, 15).atZone(zone).toLocalDate()
        for (d in listOf(0L, 1, 2, 6, 7, 29, 30, 400)) {
            val day = today.minusDays(d)
            assertEquals(key(day.atTime(12, 0).atZone(zone).toInstant()), InboxGroups.dayKey(day, today))
        }
    }

    /** The hub's done items per day (#2967, #3016) by group: "Just now" out of "Today", the last week by day, then 7+ and 1+ month. */
    @Test fun doneCountsFromTheHubsDays() {
        fun day(m: Int, d: Int) = java.time.LocalDate.of(2026, m, d)
        val days = listOf(day(11, 9) to 10, day(11, 8) to 4, day(11, 7) to 2, day(11, 3) to 1, day(11, 2) to 3, day(10, 20) to 5,
            day(10, 10) to 6, day(9, 1) to 7)
        assertEquals(listOf("just" to 3, "today" to 7, "yesterday" to 4, "d:2026-11-07" to 2, "d:2026-11-03" to 1, "week" to 8, "month" to 13),
            InboxGroups.doneCounts(days, justNow = 3, now = now, zone = zone))
        // More just now than the hub counted today (it counted a moment ago): no "Today" rather than a negative one.
        assertEquals(listOf("just" to 2), InboxGroups.doneCounts(listOf(day(11, 9) to 1), justNow = 2, now = now, zone = zone))
        assertEquals(emptyList<Pair<String, Int>>(), InboxGroups.doneCounts(emptyList(), 0, now, zone))
    }

    @Test fun aGroupsSpan() {
        assertEquals(local(11, 8, 0) to local(11, 9, 0), InboxGroups.span("yesterday", now, zone))
        assertEquals(local(11, 5, 0) to local(11, 6, 0), InboxGroups.span("d:2026-11-05", now, zone))
        assertEquals(local(10, 11, 0) to local(11, 3, 0), InboxGroups.span("week", now, zone))      // 7 to 29 days ago
        assertEquals(null to local(10, 11, 0), InboxGroups.span("month", now, zone))
        assertEquals(local(11, 9, 0) to local(11, 10, 0), InboxGroups.span("today", now, zone))
        // A group's span holds exactly the moments timeKey puts in it.
        for (k in listOf("yesterday", "d:2026-11-05", "week", "month")) {
            val (start, end) = InboxGroups.span(k, now, zone)
            assertEquals(k, key(end.minusSeconds(1)))
            start?.let { assertEquals(k, key(it)); assertEquals(true, key(it.minusSeconds(1)) != k) }
        }
    }

    @Test fun countedGroupsLoadedOrNot() {
        val loaded = listOf(InboxGroups.Group("just", listOf("a")), InboxGroups.Group("today", listOf("b", "c")), InboxGroups.Group("month", listOf("z")))
        val counts = listOf("just" to 1, "today" to 2, "yesterday" to 4, "week" to 9, "month" to 30)
        val shown = InboxGroups.withCounts(loaded, counts, now, zone)
        assertEquals(listOf("just", "today", "yesterday", "week", "month"), shown.map { it.key })
        assertEquals(listOf(1, 2, 4, 9, 30), shown.map { it.count })
        assertEquals(listOf(true, true, false, false, false), shown.map { it.whole })
        assertEquals(listOf("z"), shown.last().items)
        // A group the counts don't have yet (done since they were read) still shows, with what is loaded.
        val extra = InboxGroups.withCounts(listOf(InboxGroups.Group("d:2026-11-06", listOf("x", "y"))), counts, now, zone)
        assertEquals(2, extra.first { it.key == "d:2026-11-06" }.count)
        assertEquals("d:2026-11-06", extra[3].key)
    }
}
