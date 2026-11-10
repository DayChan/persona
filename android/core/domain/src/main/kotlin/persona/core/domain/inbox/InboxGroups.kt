package persona.core.domain.inbox

import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.temporal.ChronoUnit

/**
 * Groups inside the inbox's sections (#2059, #2576; web lib/inbox-groups.ts, iOS InboxGroups.swift): Needs you, FYI and
 * Collapsed by source or by time (the switch under the filters, kept on this device), Done always by when it was done.
 * Pure: the caller gives `now` and the zone.
 */
object InboxGroups {
    const val SOURCE = "source"
    const val TIME = "time"

    /** The group of the agents' cards waiting on the user: first in Needs you, by source and by time alike. */
    const val AGENTS = "agents"

    /** A group of a section: its key (stable, for the folds kept on this device) and its items in the section's order. */
    data class Group<T>(val key: String, val items: List<T>)

    /**
     * The time group of a moment: "just" (within the hour; Done only, so a slip is easy to find and undo), "today",
     * "yesterday", "d:YYYY-MM-DD" for each of the 2 to 6 days before (the last week, not the calendar week: a Monday still
     * shows last week's days), "week" (7 to 29 days ago) and "month" (30 days or more, or no time). Calendar days in
     * [zone]; a moment in the future is today.
     */
    fun timeKey(at: Instant?, now: Instant, zone: ZoneId, recent: Boolean = false): String {
        if (at == null) return "month"
        if (recent && now.toEpochMilli() - at.toEpochMilli() <= 3_600_000) return "just"
        return dayKey(at.atZone(zone).toLocalDate(), now.atZone(zone).toLocalDate())
    }

    /** The time group of a whole calendar [day] seen on [today] (as [timeKey] for a moment of it; never "just"). */
    fun dayKey(day: LocalDate, today: LocalDate): String {
        val days = ChronoUnit.DAYS.between(day, today).coerceAtLeast(0)
        return when {
            days == 0L -> "today"
            days == 1L -> "yesterday"
            days <= 6 -> "d:$day"
            days <= 29 -> "week"
            else -> "month"
        }
    }

    /**
     * Done's group counts from the hub's done items per day (#2967, #3016): each day's count goes to its time group;
     * "Just now" is [justNow] (the list holds the last hour whole) and comes out of "Today". Newest group first; groups
     * with none are left out.
     */
    fun doneCounts(days: List<Pair<LocalDate, Int>>, justNow: Int, now: Instant, zone: ZoneId): List<Pair<String, Int>> {
        val today = now.atZone(zone).toLocalDate()
        val counts = LinkedHashMap<String, Int>()
        days.forEach { (day, n) -> dayKey(day, today).let { counts[it] = (counts[it] ?: 0) + n } }
        if (justNow > 0) {
            counts["just"] = justNow
            counts["today"] = ((counts["today"] ?: 0) - justNow).coerceAtLeast(0)
        }
        return counts.entries.filter { it.value > 0 }.sortedBy { timeRank(it.key, now, zone) }.map { it.key to it.value }
    }

    /**
     * When Done's group [key] begins and ends, [start, end) in [zone] (no start: the oldest). Its own first page is the one
     * before its end; it is whole once that far back is in.
     */
    fun span(key: String, now: Instant, zone: ZoneId): Pair<Instant?, Instant> {
        val today = now.atZone(zone).toLocalDate()
        fun at(day: LocalDate) = day.atStartOfDay(zone).toInstant()
        return when (key) {
            "just" -> now.minusSeconds(3600) to at(today.plusDays(1))
            "today" -> at(today) to at(today.plusDays(1))
            "yesterday" -> at(today.minusDays(1)) to at(today)
            "week" -> at(today.minusDays(29)) to at(today.minusDays(6))
            "month" -> null to at(today.minusDays(29))
            else -> dayOf(key)?.let { at(it) to at(it.plusDays(1)) } ?: (null to at(today.plusDays(1)))
        }
    }

    /**
     * Done's groups as drawn with the hub's [counts] ([doneCounts]): every counted group, loaded or not, and any loaded
     * one the counts don't have yet, newest first; a group's count is the hub's, or what is loaded when that is more.
     */
    fun <T> withCounts(loaded: List<Group<T>>, counts: List<Pair<String, Int>>, now: Instant, zone: ZoneId,
                       size: (T) -> Int = { 1 }): List<Counted<T>> {
        val byKey = loaded.associateBy { it.key }
        val counted = counts.toMap()
        return (counts.map { it.first } + loaded.map { it.key }).distinct().sortedBy { timeRank(it, now, zone) }.map { key ->
            val items = byKey[key]?.items.orEmpty()
            val have = items.sumOf(size)
            Counted(key, items, maxOf(counted[key] ?: 0, have), have)
        }
    }

    /** A group with its count: [loaded] of its [count] items are in. */
    data class Counted<T>(val key: String, val items: List<T>, val count: Int, val loaded: Int) {
        val whole: Boolean get() = loaded >= count
    }

    /** A day group's date ("d:2026-11-06"), null for the others. */
    fun dayOf(key: String): LocalDate? = key.takeIf { it.startsWith("d:") }?.let { runCatching { LocalDate.parse(it.substring(2)) }.getOrNull() }

    /** Where a time group goes: newest first (just now, today, yesterday, the days before, 7+ days, 1+ month). */
    private fun timeRank(key: String, now: Instant, zone: ZoneId): Long = when (key) {
        "just" -> 0; "today" -> 1; "yesterday" -> 2; "week" -> 100; "month" -> 101
        else -> 2 + (dayOf(key)?.let { ChronoUnit.DAYS.between(it, now.atZone(zone).toLocalDate()) } ?: 50)
    }

    /** [items] by time group, newest group first; [at] is the moment that counts (the last message, or when it was done). */
    fun <T> byTime(items: List<T>, now: Instant, zone: ZoneId, recent: Boolean = false, at: (T) -> Instant?): List<Group<T>> =
        items.groupBy { timeKey(at(it), now, zone, recent) }.entries
            .sortedBy { timeRank(it.key, now, zone) }.map { Group(it.key, it.value) }

    /** An item's source group: Lark, each mail account, each connector or extension source by its account, else its source. */
    fun sourceKey(source: String, account: String, connector: Boolean): String = when {
        connector -> "c:$account"
        source == "mail" -> "mail:$account"
        source.isBlank() -> "other"
        else -> source
    }

    /**
     * [items] by source in the filters' order: the agents' cards, Lark, each mail account (as they first show up), then
     * the connectors and extensions (as they first show up).
     */
    fun <T> bySource(items: List<T>, key: (T) -> String): List<Group<T>> {
        val groups = items.groupBy(key)
        val order = groups.keys.toList()
        fun rank(k: String) = when {
            k == AGENTS -> 0
            k == "lark" -> 1
            k.startsWith("mail:") -> 2
            else -> 3
        }
        return groups.entries.sortedWith(compareBy({ rank(it.key) }, { order.indexOf(it.key) })).map { Group(it.key, it.value) }
    }

    /** A section's groups as drawn: one group is no group (its items show without a title, as before). */
    fun <T> shown(groups: List<Group<T>>): List<Group<T>> = if (groups.size > 1) groups else emptyList()

    /** Done's groups: always by when they were done, "Just now" first. */
    fun <T> done(items: List<T>, now: Instant, zone: ZoneId, doneAt: (T) -> Instant?): List<Group<T>> =
        byTime(items, now, zone, recent = true, at = doneAt)

    /** Groups open at first: every group of Needs you, FYI and Collapsed; in Done only "Just now" and "Today". */
    fun openAtFirst(section: String, key: String): Boolean = section != "done" || key == "just" || key == "today"

    /**
     * Whether group [key] of [section] is open: as it is at first ([openAtFirst]) unless the user flipped it, which
     * [flipped] keeps as "section/key".
     */
    fun isOpen(section: String, key: String, flipped: Collection<String>): Boolean =
        openAtFirst(section, key) != ("$section/$key" in flipped)

    /** [flipped] after opening or folding group [key] of [section]. */
    fun toggle(section: String, key: String, flipped: Collection<String>): List<String> {
        val id = "$section/$key"
        return if (id in flipped) flipped.filter { it != id } else flipped + id
    }

    /** [flipped] with every group of [section] among [keys] set [open]. */
    fun setAll(section: String, keys: Collection<String>, open: Boolean, flipped: Collection<String>): List<String> {
        val ids = keys.map { "$section/$it" }.toSet()
        val others = flipped.filter { it !in ids }
        return others + keys.filter { openAtFirst(section, it) != open }.map { "$section/$it" }
    }

    /** A group's tick in selection: none, some or all of its items picked. */
    enum class Tick { None, Some, All }

    fun <T> tick(selectable: List<T>, picked: Set<T>): Tick {
        val n = selectable.count { it in picked }
        return when { selectable.isEmpty() || n == 0 -> Tick.None; n == selectable.size -> Tick.All; else -> Tick.Some }
    }

    /** [picked] after tapping a group's tick: all of its selectable items when not all were, else none of them. */
    fun <T> pickGroup(selectable: List<T>, picked: Set<T>): Set<T> =
        if (tick(selectable, picked) == Tick.All) picked - selectable.toSet() else picked + selectable
}
