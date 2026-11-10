package persona.feature.settings

import android.content.res.Resources
import persona.core.contracts.api.HubSent
import persona.core.contracts.api.SentItem
import persona.core.contracts.api.SentLetThrough
import persona.core.contracts.api.SentOrigin
import persona.core.contracts.api.SentPage
import persona.core.contracts.api.SentRule
import persona.core.contracts.api.SentWire
import persona.core.contracts.api.hubCodedFailure
import persona.core.contracts.parseTime
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.failureReasonText
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import java.math.BigDecimal
import java.time.Duration
import java.time.Instant
import java.time.ZoneId
import java.time.temporal.ChronoUnit

// Settings › Sent as you (#5278, docs/extensions/connectors.md "Sent as the user"): what Persona wrote in the user's name through the
// connectors, newest first, from GET /api/v1/connectors/sent. The words and rules follow the web page (web/src/lib/sent.ts,
// views/settings/SentSettings.tsx) and iOS (SentModel.swift): where a send went, how the gate let it through, who asked, paging
// and refreshing the list, the connector chips, and the hub's refusals in plain words. The page is in SentScreen.kt.

/** One field of a send's target, for the details. */
internal data class SentField(val key: String, val value: String)

/** How the gate let a send through, for the badge and the details. */
internal data class SentAllowed(val label: String, val tone: SentTone, val detail: String)

internal enum class SentTone {
    Neutral,
    /** The user did not decide: the assistant (or someone else) answered the card, or the extension sent on its own. */
    Orange,
}

/** Why the list cannot be read, in plain words. */
internal data class SentProblem(val kind: Kind, val text: ResText) {
    enum class Kind { NoDatabase, Forbidden, Unsupported, Error }
}

/** Why a recall of row [id] did not happen, in plain words. */
internal data class SentRecallError(val id: Long, val text: ResText)

/** What reading the first page again leaves ([SentText.refreshed]). */
internal data class SentRefresh(val items: List<SentItem>, val next: Cursor, val loadedOlder: Boolean) {
    sealed interface Cursor {
        /** Keep the cursor the list has: it is the older end's. */
        data object Keep : Cursor
        data class Set(val next: Long?) : Cursor
    }
}

internal object SentText {
    /** The id of the connector the node's Lark gate writes under: its sends are in the ledger though no connector is registered for them. */
    const val Lark = "lark"

    // ------------------------------------------------------------------ failures

    /**
     * Why the list can't be read. The hub's own answers come before the usual text, which would read a 503 as "can't reach
     * the hub" (`no_database` is the hub saying it has no ledger) and a 404 as "not found": 403 is a device without full
     * rights, and a 404 with no code is a hub from before the ledger.
     */
    fun problem(error: Throwable): SentProblem {
        val coded = hubCodedFailure(error)
        return when {
            coded?.status == 503 && coded.code == "no_database" -> SentProblem(SentProblem.Kind.NoDatabase, ResText.of(R.string.settings_sent_no_database))
            coded?.status == 403 -> SentProblem(SentProblem.Kind.Forbidden, ResText.of(R.string.settings_sent_forbidden))
            coded?.status == 404 && coded.code == null -> SentProblem(SentProblem.Kind.Unsupported, ResText.of(R.string.settings_sent_unsupported))
            else -> SentProblem(SentProblem.Kind.Error, ResText.of(R.string.settings_src_read_failed, failureReasonText(error)))
        }
    }

    /**
     * Why a recall did not happen, in plain words (docs/extensions/connectors.md "Sent as the user", Failures); a code this page
     * does not know, and `recall_refused`, say what the hub said.
     */
    fun recallProblem(error: Throwable): ResText {
        val coded = hubCodedFailure(error)
        val known = when {
            coded?.status == 403 -> R.string.settings_sent_recall_forbidden
            (coded?.status == 404 && coded.code == null) || coded?.status == 405 -> R.string.settings_sent_recall_unsupported
            else -> when (coded?.code) {
                "not_found" -> R.string.settings_sent_recall_not_found
                "not_recallable" -> R.string.settings_sent_recall_not_recallable
                "too_old" -> R.string.settings_sent_recall_too_old
                "already_recalled" -> R.string.settings_sent_recall_already
                "connector_unavailable" -> R.string.settings_sent_recall_unavailable
                "no_database" -> R.string.settings_sent_recall_no_database
                "recall_unknown" -> R.string.settings_sent_recall_unknown
                else -> null
            }
        }
        return known?.let { ResText.of(it) } ?: ResText.of(R.string.settings_sent_recall_refused, failureReasonText(error))
    }

    // ------------------------------------------------------------------ the list

    /** [have] and [incoming] together, one row per id (the newer copy wins), newest first. */
    fun merge(have: List<SentItem>, incoming: List<SentItem>): List<SentItem> {
        val byId = LinkedHashMap<Long, SentItem>()
        for (item in have) byId[item.id] = item
        for (item in incoming) byId[item.id] = item
        return byId.values.sortedByDescending { it.id }
    }

    /**
     * What reading the first page again leaves. Nothing older loaded: the page alone (rows the hub pruned go). Older pages
     * loaded: the page merged in and the cursor kept (it is the older end's); unless the page does not reach back to the
     * newest row held (more came in than a page holds, so merging would leave a hole), then the page alone and the older
     * pages go.
     */
    fun refreshed(have: List<SentItem>, page: SentPage, loadedOlder: Boolean): SentRefresh {
        val oldest = page.items.lastOrNull()?.id ?: 0L
        val reaches = page.items.isNotEmpty() && (page.next == null || oldest <= (have.firstOrNull()?.id ?: 0L))
        if (!loadedOlder || !reaches) return SentRefresh(page.items, SentRefresh.Cursor.Set(page.next), loadedOlder = false)
        return SentRefresh(merge(have, page.items), SentRefresh.Cursor.Keep, loadedOlder = true)
    }

    // ------------------------------------------------------------------ connectors (the filter)

    /**
     * A connector's name: mail and Lark (its writes through the node's gate are in the ledger though no connector is registered
     * for them) by their usual words, GitHub as it is, any other, an extension's, as its id, which is what the ledger calls it.
     */
    fun connectorName(res: Resources, id: String): String = when (id) {
        "mail" -> res.getString(R.string.settings_sent_connector_mail)
        "github" -> "GitHub"
        Lark -> res.getString(R.string.settings_sent_connector_lark)
        else -> id
    }

    /**
     * The filter's chips: Lark, the connectors rows have shown (so one with no record is never offered: Messages only reads,
     * and the hub's list of connectors can't say which ones write) and the chosen one, so its chip never goes while it is
     * chosen. No duplicates, in that order. A connector whose rows are all on pages not loaded yet gets its chip when they are.
     */
    fun connectorChoices(seen: List<String>, selected: String?): List<String> =
        (listOf(Lark) + seen + listOfNotNull(selected)).filter { it.isNotEmpty() }.distinct()

    /** The ids of [items]' connectors added to [seen], in the order they first showed. */
    fun seenConnectors(seen: List<String>, items: List<SentItem>): List<String> =
        (seen + items.map { it.connector }.filter { it.isNotEmpty() }).distinct()

    // ------------------------------------------------------------------ a row

    /** A target field's value as text: a list joined, a number or boolean as it is (12, not 12.0), anything nested as JSON. */
    fun fieldText(value: JsonElement?): String = when (value) {
        null, JsonNull -> ""
        is JsonPrimitive -> when {
            value.isString -> value.content.trim()
            value.content == "true" || value.content == "false" -> value.content
            else -> value.content.toDoubleOrNull()?.let(::numberText) ?: value.content
        }
        is JsonArray -> value.map(::fieldText).filter { it.isNotEmpty() }.joinToString(", ")
        is JsonObject -> sorted(value).toString()
    }

    /** An object's keys in name order all the way down, so the same value always reads the same. */
    private fun sorted(value: JsonElement): JsonElement = when (value) {
        is JsonObject -> JsonObject(value.entries.sortedBy { it.key }.associate { it.key to sorted(it.value) })
        is JsonArray -> JsonArray(value.map(::sorted))
        else -> value
    }

    /** A number as JavaScript prints it: a whole one in full (12, not 12.0; below 1e21), others in the shortest digits, with an exponent beyond 1e21 and below 1e-6. */
    fun numberText(n: Double): String {
        if (n == 0.0) return "0"
        if (n.isNaN() || n.isInfinite()) return n.toString()
        val digits = BigDecimal(n.toString()).stripTrailingZeros()
        val magnitude = Math.abs(n)
        if (magnitude >= 1e21 || magnitude < 1e-6) {
            val exponent = digits.precision() - digits.scale() - 1
            val mantissa = digits.unscaledValue().abs().toString().let { if (it.length > 1) it[0] + "." + it.substring(1) else it }
            return (if (n < 0) "-" else "") + mantissa + "e" + (if (exponent >= 0) "+" else "-") + Math.abs(exponent)
        }
        return digits.toPlainString()
    }

    /** The arguments connectors usually name their destination by, in the order the hub keeps them (extmgr targetFields). */
    private val common = listOf("chat", "thread", "channel", "conversation", "recipient", "to", "reply_to", "subject", "repo", "number")

    /** The order the details list a target's fields in (the hub's JSON objects have none): where it went first, then the rest of what the connectors above name, then any other field by name. */
    private val detailOrder = common + listOf("ref", "cc", "bcc", "event", "method", "path", "command", "account_id", "arg_keys")

    /**
     * Where a send went, in a few words: mail the recipients and subject, GitHub the repo#number, Lark the conversation (else
     * the method and path the node's gate saw); any other connector's the common fields it named, up to three. "—" when
     * there is nothing to say (a connector that gave only the names of its arguments).
     */
    fun targetText(item: SentItem): String {
        fun f(key: String) = fieldText(item.target[key])
        when (item.connector) {
            "mail" -> if (f("to").isNotEmpty()) return if (f("subject").isEmpty()) f("to") else "${f("to")} · ${f("subject")}"
            "github" -> if (f("ref").isNotEmpty()) return f("ref")
            Lark -> {
                if (f("conversation").isNotEmpty()) return f("conversation")
                val call = listOf(f("method"), f("path")).filter { it.isNotEmpty() }.joinToString(" ")
                if (call.isNotEmpty()) return call
            }
        }
        val shown = common.map(::f).filter { it.isNotEmpty() }.take(3)
        return if (shown.isEmpty()) "—" else shown.joinToString(" · ")
    }

    /** The target's fields for the details in a fixed order ([detailOrder], then the rest by name), empty values left out. */
    fun targetFields(target: Map<String, JsonElement>): List<SentField> {
        fun rank(key: String) = detailOrder.indexOf(key).let { if (it < 0) detailOrder.size else it }
        return target.keys.sortedWith(compareBy<String>({ rank(it) }, { it }))
            .map { SentField(it, fieldText(target[it])) }
            .filter { it.value.isNotEmpty() }
    }

    /** The first [most] lines of the text (blank ones skipped) for the row; with no text, the one-line summary. Empty: nothing was recorded. */
    fun previewLines(item: SentItem, most: Int = 2): List<String> {
        // Only \n and \r\n are line breaks, as on the web.
        val lines = item.text.split(LineBreak).map { it.trim() }.filter { it.isNotEmpty() }
        if (lines.isNotEmpty()) return lines.take(most)
        val summary = item.summary.trim()
        return if (summary.isEmpty()) emptyList() else listOf(summary)
    }

    private val LineBreak = Regex("\r?\n")

    /** Whether the ledger cut the text: it is exactly as long as the ledger keeps (counted in characters of the text, as the hub counts them, not in UTF-16 units). */
    fun textCut(text: String): Boolean = text.codePointCount(0, text.length) >= SentWire.TextMost

    /** A rule in a line, the way connector_gate writes it: `lark · POST /im/v1/messages (chat_id=…) → allow`. */
    fun ruleText(rule: SentRule?): String {
        if (rule == null) return ""
        val head = listOf(rule.connector, rule.action).filter { it.isNotEmpty() }.joinToString(" · ")
        val match = rule.match.keys.sorted().joinToString(", ") { "$it=${fieldText(rule.match[it])}" }
        return listOf(head, if (match.isEmpty()) "" else "($match)", if (rule.decision.isEmpty()) "" else "→ ${rule.decision}")
            .filter { it.isNotEmpty() }.joinToString(" ")
    }

    /**
     * How the gate let it through: the badge's words and tone (orange where the user did not decide: the assistant or someone
     * else answered the card, or the extension sent on its own) and a sentence for the details. A kind this page does not know
     * is shown as it is.
     */
    fun allowedBy(res: Resources, through: SentLetThrough): SentAllowed = when (through.kind) {
        "rule" -> SentAllowed(res.getString(R.string.settings_sent_by_rule), SentTone.Neutral,
            ruleText(through.rule).ifEmpty { res.getString(R.string.settings_sent_detail_rule) })
        "conversation_always" -> SentAllowed(res.getString(R.string.settings_sent_by_always), SentTone.Neutral, res.getString(R.string.settings_sent_detail_always))
        "card" -> {
            // `answeredBy` is empty when the user answered (in the app, on the phone, the Mac, the web or a chat channel, which the
            // ledger does not say), "assistant" when the assistant did, and anyone else is named: not the user's own word.
            val by = through.answeredBy
            when {
                by == "assistant" -> SentAllowed(res.getString(R.string.settings_sent_by_assistant), SentTone.Orange, res.getString(R.string.settings_sent_detail_assistant))
                by.isNotEmpty() -> SentAllowed(res.getString(R.string.settings_sent_by_named, by), SentTone.Orange, res.getString(R.string.settings_sent_detail_card_by, by))
                else -> SentAllowed(res.getString(R.string.settings_sent_by_card), SentTone.Neutral, res.getString(R.string.settings_sent_detail_card))
            }
        }
        "auto" -> if (through.setting.isEmpty()) {
            SentAllowed(res.getString(R.string.settings_sent_by_auto), SentTone.Orange, res.getString(R.string.settings_sent_detail_auto))
        } else {
            SentAllowed(res.getString(R.string.settings_sent_by_auto_setting, through.setting), SentTone.Orange,
                res.getString(R.string.settings_sent_detail_auto_setting, through.setting))
        }
        else -> SentAllowed(through.kind.ifEmpty { "—" }, SentTone.Neutral, "")
    }

    /** Who asked: the conversation to open (both ids named) and the extension when the row is one an extension recorded (what it names of the conversation is its own word). Neither: no conversation. */
    data class Origin(val machine: String, val session: String, val extensionId: String) {
        val hasChat: Boolean get() = machine.isNotEmpty() && session.isNotEmpty()
    }

    fun originInfo(origin: SentOrigin): Origin {
        val machine = origin.machineId.trim()
        val session = origin.sessionId.trim()
        val both = machine.isNotEmpty() && session.isNotEmpty()
        return Origin(if (both) machine else "", if (both) session else "", origin.extensionId.trim())
    }

    /**
     * How long ago, as the web page says it (`ago` in web/src/lib/format.ts): just now, minutes, hours, then days by the local
     * calendar (yesterday morning seen late tonight is yesterday) up to six, then the date; a dash when the hub gave no
     * readable time. The first three and the date are the settings pages' usual [agoText].
     */
    fun ago(res: Resources, at: String, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
        val time = parseTime(at) ?: return "—"
        if (Duration.between(time, now) < Duration.ofDays(1)) return agoText(res, time, now, zone)
        // A 25-hour day can leave a whole day's gap on the same date: at least yesterday.
        val days = ChronoUnit.DAYS.between(time.atZone(zone).toLocalDate(), now.atZone(zone).toLocalDate()).toInt().coerceAtLeast(1)
        return if (days <= DaysAgoMost) res.getQuantityString(R.plurals.settings_src_days_ago, days, days) else agoText(res, time, now, zone)
    }

    /** The most days a row says "N days ago" for; a send older than that says its date. */
    private const val DaysAgoMost = 6

    /** The moment, in full (the details; a row says how long ago). */
    fun full(res: Resources, at: String, zone: ZoneId = ZoneId.systemDefault()): String =
        parseTime(at)?.let { LocalizedFormats.format(it, LocalizedFormats.DATE_TIME_SECONDS, res.configuration.locales[0], zone) } ?: "—"

    /** The 4000 in "cut at 4,000 characters", in the language's grouping. */
    fun textMost(res: Resources): String = LocalizedFormats.number(SentWire.TextMost, res.configuration.locales[0])
}

/** The page's state. */
internal data class SentState(
    /** Null until the first page came in. */
    val items: List<SentItem>? = null,
    /** The `before` of the page after the list, null when it holds the oldest row. */
    val next: Long? = null,
    /** Why the first page could not be read (a list that is there stays when a later read fails). */
    val problem: SentProblem? = null,
    /** The connector the list is of, null for all. */
    val filter: String? = null,
    val loadingMore: Boolean = false,
    /** Why the last Load earlier failed. */
    val moreError: ResText? = null,
    /** The connectors the loaded rows have shown, in the order they first showed (the chips). */
    val seen: List<String> = emptyList(),
    /** The row a recall is on its way for. */
    val recalling: Long? = null,
    /** Why the last recall failed. */
    val recallError: SentRecallError? = null,
) {
    val choices: List<String> get() = SentText.connectorChoices(seen, filter)

    /**
     * Nothing to filter by while the ledger is empty and every connector is shown, or when the unfiltered list can't be read;
     * under a filter the chips stay, so a failed read can be left by choosing another one or All.
     */
    val showsChips: Boolean get() = filter != null || (problem == null && seen.isNotEmpty())
}

/**
 * Settings › Sent as you: the first page, older pages on request, the connector filter, and what a read that comes back late is
 * worth. The list starts over when the filter changes (what comes in for the old one is dropped); a read of the first page
 * that was superseded by a newer one, and a read of older rows for a cursor the list has moved on from, are dropped too.
 * Meant for one thread (the page's); a read that is cancelled says nothing.
 */
internal class SentModel(private val api: HubSent) {
    private val mutable = MutableStateFlow(SentState())
    val state = mutable.asStateFlow()

    /** Moves when the filter changes: a read that started under an earlier one is dropped. */
    private var generation = 0

    /**
     * Reads of the first page started under this filter, and the newest of them whose answer was taken: an answer is taken
     * when it is newer than that (an older one that comes back after a newer one is dropped, and a newer one that was
     * cancelled or failed does not strand an older one that is good).
     */
    private var reads = 0
    private var applied = 0

    /** Older pages were loaded: reading the first page again merges instead of replacing. */
    private var loadedOlder = false

    /** The first page has come in once under this filter. */
    private var hasFirst = false

    /** Reads the first page: on opening, on pull to refresh, on Retry. */
    suspend fun load() {
        reads += 1
        val mine = reads
        val gen = generation
        val connector = state.value.filter
        try {
            val page = api.page(connector = connector)
            if (gen != generation || mine <= applied) return
            applied = mine
            hasFirst = true
            val out = SentText.refreshed(state.value.items.orEmpty(), page, loadedOlder)
            loadedOlder = out.loadedOlder
            mutable.update {
                val cursor = out.next
                it.copy(problem = null, seen = SentText.seenConnectors(it.seen, page.items), items = out.items,
                    next = when (cursor) { is SentRefresh.Cursor.Set -> cursor.next; SentRefresh.Cursor.Keep -> it.next })
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            // A cancelled read says nothing; a list that is there stays when a later read fails: only the first read's failure is shown.
            currentCoroutineContext().ensureActive()
            if (gen != generation || mine <= applied || hasFirst) return
            mutable.update { it.copy(problem = SentText.problem(error)) }
        }
    }

    /** Retry: the first page again, with the spinner where the failure was. */
    suspend fun retry() {
        mutable.update { it.copy(problem = null) }
        load()
    }

    /** Chooses a connector's chip (null: All; the chosen one again: All) and reads its first page. */
    suspend fun choose(connector: String?) {
        val chosen = if (connector == state.value.filter) null else connector
        if (chosen == state.value.filter) return
        generation += 1
        reads = 0
        applied = 0
        loadedOlder = false
        hasFirst = false
        mutable.update { it.copy(filter = chosen, items = null, next = null, problem = null, moreError = null, loadingMore = false) }
        load()
    }

    /**
     * Recall: takes back row [id]'s message once the user confirmed it. Reads of the first page already on their way are dropped
     * (they would bring back the row as it was), the row the hub answers with replaces the old one, and the first page is read
     * again whether it worked or not (a row now `too_old` or `already_recalled` changes too).
     */
    suspend fun recall(id: Long) {
        if (state.value.recalling != null) return
        val gen = generation
        applied = reads
        mutable.update { it.copy(recalling = id, recallError = null) }
        try {
            val row = api.recall(id)
            if (gen == generation && row != null) mutable.update { s -> s.copy(items = s.items?.map { if (it.id == row.id) row else it }) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            currentCoroutineContext().ensureActive()
            if (gen == generation) mutable.update { it.copy(recallError = SentRecallError(id, SentText.recallProblem(error))) }
        } finally {
            mutable.update { it.copy(recalling = null) }
        }
        if (gen == generation) load()
    }

    /** Load earlier: the page after the list, added by id. */
    suspend fun loadMore() {
        val asked = state.value.next ?: return
        if (state.value.loadingMore) return
        val gen = generation
        val connector = state.value.filter
        mutable.update { it.copy(loadingMore = true, moreError = null) }
        try {
            val page = api.page(before = asked, connector = connector)
            if (gen != generation || state.value.next != asked) return
            loadedOlder = true
            mutable.update { it.copy(seen = SentText.seenConnectors(it.seen, page.items), items = SentText.merge(it.items.orEmpty(), page.items), next = page.next) }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            currentCoroutineContext().ensureActive()
            if (gen != generation) return
            mutable.update { it.copy(moreError = SentText.problem(error).text) }
        } finally {
            if (gen == generation) mutable.update { it.copy(loadingMore = false) }
        }
    }
}
