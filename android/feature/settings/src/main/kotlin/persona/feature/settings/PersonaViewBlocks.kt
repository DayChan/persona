package persona.feature.settings

import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ExtensionChartPoint
import persona.core.contracts.api.ExtensionChartSeries
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.ExtensionViewItem
import persona.core.contracts.api.ExtensionViewSection
import persona.core.contracts.generated.ExtensionCardPart
import persona.core.contracts.generated.ExtensionPersonaCard
import persona.core.designsystem.ViewBlocks
import kotlin.math.abs

// An assistant's reply drawn with the extensions' components (docs/extensions/extensions.md "Declarative UI", #1850): a ```persona-view
// block is a view (or one component), the older ```persona-card is turned into components. Nothing in them acts: no
// buttons, fields, search or "Load more" (links open as anywhere in a reply).

object PersonaViewBlocks {
    const val View = "persona-view"
    const val Card = "persona-card"

    /**
     * The view in a block, or null: another language, JSON that is not whole yet (still streaming) or not a view. A
     * card's own words ("Total", "Other"...) are [words]: the reference's by default, the shown language's where it is drawn.
     */
    fun parse(language: String, code: String, words: CardWords = CardWords.Reference): ExtensionView? = when (language.trim().lowercase()) {
        View -> view(code)
        Card -> card(code, words)
        else -> null
    }?.let(::quiet)?.takeIf { v -> v.sections.any { s -> s.items.any(ExtensionViewText::drawable) } }

    private fun view(code: String): ExtensionView? {
        val json = runCatching { JsonCodec.parseToJsonElement(code) }.getOrNull() as? JsonObject ?: return null
        // One component on its own is a view of one section.
        return if (json["sections"] == null && (json["type"] as? JsonPrimitive)?.contentOrNull != null) {
            runCatching { JsonCodec.decodeFromJsonElement(ExtensionViewItem.serializer(), json) }.getOrNull()
                ?.let { ExtensionView(sections = listOf(ExtensionViewSection(items = listOf(it)))) }
        } else runCatching { JsonCodec.decodeFromJsonElement(ExtensionView.serializer(), json) }.getOrNull()
    }

    private fun card(code: String, words: CardWords): ExtensionView? =
        runCatching { JsonCodec.decodeFromString(ExtensionPersonaCard.serializer(), code) }.getOrNull()?.let { fromCard(it, words) }

    /**
     * A ```persona-card as components, by the rules every app shares (the reference is `sdk/go/wire/personacard.go`, the
     * cases `schema/extension/examples/persona-card/`): one section titled with the card's title (a stat's goes on its
     * number), its subtitle first; `stat` a number; `stacked_bar` the total, a stacked bar of the parts (past four: three
     * and "Other") and a row per part; `ring` a donut of the parts (with what the total leaves over them; past eight: seven
     * and "Other"), the total in the hole, and a row per part; `line` / `bar` the current value (if any) over a chart of the last 120 points. A card
     * it cannot draw is null (and stays the code it is).
     */
    fun fromCard(card: ExtensionPersonaCard, words: CardWords = CardWords.Reference): ExtensionView? {
        var parts = card.segments.orEmpty() + listOfNotNull(card.rest)
        var sum = parts.sumOf { it.value }
        when (card.type) {
            "stat" -> if (card.value == null) return null
            "stacked_bar" -> if (parts.isEmpty() || sum <= 0 || parts.any { it.value < 0 }) return null
            "ring" -> {
                val total = card.total?.value
                if (total != null && total > sum) {
                    parts = parts + ExtensionCardPart(words.remaining, total - sum)
                    sum = total
                }
                if (parts.size < 2 || sum <= 0 || parts.any { it.value < 0 }) return null
            }
            "line" -> if (card.points.orEmpty().size < 2) return null
            "bar" -> if (card.points.isNullOrEmpty()) return null
            else -> return null
        }
        val unit = card.unit.orEmpty()
        val items = mutableListOf<ExtensionViewItem>()
        card.subtitle?.takeIf { it.isNotEmpty() }?.let { items += ExtensionViewItem("text", text = it, style = "secondary") }
        var title = card.title?.takeIf { it.isNotEmpty() }
        when (card.type) {
            "stat" -> {
                items += ExtensionViewItem("metric", label = title ?: words.value, value = amount(card.value!!, unit), detail = card.note.orNull())
                title = null
            }
            "stacked_bar" -> {
                val total = card.total
                val totalUnit = if (total != null) total.unit?.takeIf { it.isNotEmpty() } ?: unit else unit
                items += ExtensionViewItem("metric", label = words.total, value = amount(total?.value ?: sum, totalUnit), detail = card.note.orNull())
                val label = title ?: words.total
                fun series(name: String, value: Double, tone: String?) =
                    ExtensionChartSeries(name.orNull(), tone, listOf(ExtensionChartPoint(label, value, amount(value, totalUnit))))
                val shown = if (parts.size > 4) parts.take(3) else parts
                val series = shown.mapIndexed { i, part -> series(part.label, part.value, if (card.rest != null && i == parts.size - 1) "neutral" else null) } +
                    (if (parts.size > 4) listOf(series(words.other, parts.drop(3).sumOf { it.value }, "neutral")) else emptyList())
                items += ExtensionViewItem("chart", kind = "bar", stacked = true, unit = totalUnit.orNull(), series = series)
                parts.forEach { items += ExtensionViewItem("row", title = it.label, subtitle = it.note.orNull(), value = amount(it.value, totalUnit)) }
            }
            "ring" -> {
                val total = card.total
                val totalUnit = if (total != null) total.unit?.takeIf { it.isNotEmpty() } ?: unit else unit
                val shown = if (parts.size > 8) parts.take(7) + ExtensionCardPart(words.other, parts.drop(7).sumOf { it.value }) else parts
                val center = amount(total?.value ?: sum, totalUnit).takeIf { it.codePointCount(0, it.length) <= 20 }
                items += ExtensionViewItem("chart", kind = "donut", unit = totalUnit.orNull(), detail = card.note.orNull(), center = center,
                    series = listOf(ExtensionChartSeries(points = shown.map { ExtensionChartPoint(it.label, it.value, amount(it.value, totalUnit)) })))
                parts.forEach { items += ExtensionViewItem("row", title = it.label, subtitle = it.note.orNull(), value = amount(it.value, totalUnit)) }
            }
            else -> {
                card.value?.let { items += ExtensionViewItem("metric", label = words.current, value = amount(it, unit)) }
                val points = card.points.orEmpty().takeLast(120).map { ExtensionChartPoint(it.label, it.value, amount(it.value, unit)) }
                items += ExtensionViewItem("chart", kind = card.type, unit = unit.orNull(), detail = card.note.orNull(),
                    series = listOf(ExtensionChartSeries(points = points)))
            }
        }
        return ExtensionView(sections = listOf(ExtensionViewSection(title = title, items = items)))
    }

    private fun String?.orNull(): String? = this?.takeIf { it.isNotEmpty() }

    /** [value] as [number] writes it, then the unit after a space: 2,250 kcal. */
    private fun amount(value: Double, unit: String): String = number(value) + if (unit.isNotEmpty()) " $unit" else ""

    /** Nothing in a reply acts: buttons, fields, segmented controls, search and "Load more" go, rows lose their action. */
    fun quiet(view: ExtensionView): ExtensionView = view.copy(search = null, filter = null, refreshAfter = null, state = null,
        sections = view.sections.map { section ->
            section.copy(more = null, items = section.items.filter { it.type !in setOf("button", "field", "segmented") }
                .map { if (it.action != null || it.button != null) it.copy(action = null, button = null) else it })
        })

    /**
     * 2,250 · 7.5 · -1,200.3 · 0: grouped by thousands, at most one decimal, halves away from zero (as the reference
     * writes them, so every app shows the same).
     */
    fun number(value: Double): String {
        var tenths = Math.floor(abs(value) * 10 + 0.5).toLong()
        val negative = value < 0 && tenths > 0
        val whole = tenths / 10
        tenths %= 10
        val grouped = whole.toString().reversed().chunked(3).joinToString(",").reversed()
        return (if (negative) "-" else "") + grouped + if (tenths > 0) ".$tenths" else ""
    }

    /**
     * What the app provides for chat Markdown (LocalViewBlocks): a block is read where it streams in, then drawn with
     * a card's words in the shown language; an assistant's ```persona-view under its key and a live card
     * (AssistantViewCard.kt), which answer in an assistant conversation.
     */
    val blocks = ViewBlocks(parse = { language, code -> parse(language, code)?.let { Block(language, code) } }, draw = { block ->
        val (language, code) = block as Block
        val words = cardWords()
        remember(block, words) { parse(language, code, words) }?.let { PersonaViewBlock(it) }
    }, block = { key, raw, complete -> AssistantBlock(key, raw, complete) }, live = { id -> AssistantLiveCard(id) })

    private data class Block(val language: String, val code: String)
}

/** The words a ```persona-card's components get when the card does not give them: a stat's, a total, the rest, now. */
data class CardWords(val value: String, val total: String, val other: String, val current: String, val remaining: String) {
    companion object {
        /** As every app's reference writes them (sdk/go/wire/personacard.go), which the shared cases expect. */
        val Reference = CardWords(
            "数值", // i18n-ignore: the shared reference's word (personacard.go), what the cross-app cases expect
            "合计", // i18n-ignore: the shared reference's word (personacard.go), what the cross-app cases expect
            "其他", // i18n-ignore: the shared reference's word (personacard.go), what the cross-app cases expect
            "当前", // i18n-ignore: the shared reference's word (personacard.go), what the cross-app cases expect
            "剩余", // i18n-ignore: the shared reference's word (personacard.go), what the cross-app cases expect
        )
    }
}

@Composable
internal fun cardWords(): CardWords = CardWords(
    stringResource(R.string.settings_ext_card_value), stringResource(R.string.settings_ext_card_total),
    stringResource(R.string.settings_ext_card_other), stringResource(R.string.settings_ext_card_current),
    stringResource(R.string.settings_ext_card_remaining),
)


/** A reply's view, drawn in place with the extensions' renderer; links open as anywhere in a reply. */
@Composable
fun PersonaViewBlock(view: ExtensionView, modifier: Modifier = Modifier) {
    val uri = LocalUriHandler.current
    val state = remember(view) { ExtensionViewState(view = view) }
    val handle = remember(uri) {
        ExtensionViewHandle(press = { _, _ -> }, edit = { _, _ -> }, choose = {},
            open = { url -> if (ExtensionViewText.isHttps(url)) runCatching { uri.openUri(url) } })
    }
    ExtensionViewColumn(state, handle, modifier)
}
