package persona.core.designsystem

import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlin.math.abs
import kotlin.math.ceil
import kotlin.math.floor
import kotlin.math.log10
import kotlin.math.max
import kotlin.math.min
import kotlin.math.pow

// Charts and number grids drawn from the design system (an extension's chart and stat_grid, docs/extensions/extensions.md "UI slots"):
// series colours come from the palette (the style's accent first) or a role colour (Tone); sizes, type and lines
// from the style in use. Nothing here knows about extensions.

enum class JChartKind { Bar, Line, Area, Candlestick, Donut, Pie }

/**
 * One line, or one colour of bars: a value per label, how each is written, and a role colour ([tone]), a named one
 * ([hue]) or the next series colour.
 */
data class JChartSeries(val name: String?, val values: List<Double>, val displays: List<String>, val tone: Tone? = null, val hue: JHue? = null)

/** One candle of a candlestick chart: its open, high, low and close, and the volume when the chart has one. */
data class JCandle(val open: Double, val high: Double, val low: Double, val close: Double, val volume: Double? = null) {
    val rising: Boolean get() = close >= open
}

/**
 * A chart's data: the labels along the bottom (one per value) and 1–4 series. [axis] writes the side scale's numbers.
 * A candlestick has one series (its closes) and a [candles] per label; [risingRed] draws rising candles red (and
 * falling green) as some markets do. A donut writes [center] in its hole.
 */
data class JChartData(
    val kind: JChartKind,
    val labels: List<String>,
    val series: List<JChartSeries>,
    val stacked: Boolean = false,
    val axis: (Double) -> String = ChartMath::compact,
    val candles: List<JCandle> = emptyList(),
    val risingRed: Boolean = false,
    val center: String? = null,
)

/** A colour per series when the chart gives no role colour: the style's accent, then three hues that differ from it. */
fun PersonaPalette.series(index: Int): Color {
    val hues = if (dark) listOf(Tokens.Common.Dark.Hue.purple, Tokens.Common.Dark.Hue.orange, Tokens.Common.Dark.Hue.green)
    else listOf(Tokens.Common.Light.Hue.purple, Tokens.Common.Light.Hue.orange, Tokens.Common.Light.Hue.green)
    return if (index <= 0) accent else hues[(index - 1) % hues.size]
}

/** The arithmetic of a chart, apart from drawing it (tested on the JVM). */
object ChartMath {
    /** A number written short: 1,234 → 1.2K, 1,200,000 → 1.2M; small ones keep up to two decimals. */
    fun compact(value: Double): String {
        if (!value.isFinite()) return "—"
        val a = abs(value)
        val (scaled, suffix) = when {
            a >= 1e12 -> value / 1e12 to "T"
            a >= 1e9 -> value / 1e9 to "B"
            a >= 1e6 -> value / 1e6 to "M"
            a >= 1e4 -> value / 1e3 to "K"
            else -> value to ""
        }
        val digits = when {
            suffix.isNotEmpty() -> if (abs(scaled) >= 100) 0 else 1
            a >= 100 || a == floor(a) -> 0
            a >= 1 -> 1
            else -> 2
        }
        val text = if (digits == 0 && suffix.isEmpty() && a >= 1000) String.format(java.util.Locale.ROOT, "%,.0f", value)
        else String.format(java.util.Locale.ROOT, "%.${digits}f", scaled)
        return text.trimDecimals() + suffix
    }

    private fun String.trimDecimals(): String = if (contains('.')) trimEnd('0').trimEnd('.') else this

    /**
     * The lowest and highest the plot shows: always with zero, stacked bars and areas by their sums above and below it;
     * a candlestick from its lowest low to its highest high (prices are far from zero), a little room on each side.
     */
    fun range(data: JChartData): Pair<Double, Double> {
        val n = data.labels.size
        var low = 0.0
        var high = 0.0
        if (data.kind == JChartKind.Candlestick && data.candles.isNotEmpty()) {
            val lows = data.candles.map { it.low }.filter(Double::isFinite)
            val highs = data.candles.map { it.high }.filter(Double::isFinite)
            if (lows.isEmpty() || highs.isEmpty()) return 0.0 to 1.0
            val l = lows.min(); val h = highs.max()
            val pad = ((h - l).takeIf { it > 0 } ?: abs(h).coerceAtLeast(1.0)) * 0.05
            return (l - pad) to (h + pad)
        }
        if ((data.kind == JChartKind.Bar || data.kind == JChartKind.Area) && data.stacked && data.series.size > 1) {
            for (i in 0 until n) {
                val values = data.series.mapNotNull { it.values.getOrNull(i)?.takeIf(Double::isFinite) }
                high = max(high, values.filter { it > 0 }.sum())
                low = min(low, values.filter { it < 0 }.sum())
            }
        } else data.series.forEach { s -> s.values.filter(Double::isFinite).forEach { high = max(high, it); low = min(low, it) } }
        if (high == low) high = low + 1.0
        return low to high
    }

    /** About [count] evenly spaced round values that cover low…high, zero among them. */
    fun ticks(low: Double, high: Double, count: Int = 3): List<Double> {
        val span = (high - low).takeIf { it > 0 } ?: 1.0
        val raw = span / max(1, count)
        val magnitude = 10.0.pow(floor(log10(raw)))
        val step = listOf(1.0, 2.0, 2.5, 5.0, 10.0).map { it * magnitude }.first { it >= raw }
        val first = floor(low / step) * step
        val last = ceil(high / step) * step
        return generateSequence(first) { it + step }.takeWhile { it <= last + step / 2 }.map { if (abs(it) < step / 1e6) 0.0 else it }.toList()
    }

    /**
     * Which labels to write when labels [widths] px wide, each centred under its slot of [width] / n px (kept inside
     * the plot), share [width] px: every k-th from the first, with the last in place of the one before it when they
     * would crowd. No two written labels come closer than [gap].
     */
    fun labelIndices(widths: List<Float>, width: Float, gap: Float): List<Int> {
        val n = widths.size
        if (n == 0) return emptyList()
        val spans = widths.indices.map { labelLeft(it, widths[it], width, n).let { left -> left to left + widths[it] } }
        fun fits(list: List<Int>) = list.zipWithNext().all { (a, b) -> spans[b].first >= spans[a].second + gap }
        var every = max(1, ceil((widths.max() + gap) / (width / n)).toInt())
        while (true) {
            val shown = (0 until n step every).toMutableList()
            if (shown.last() != n - 1) {
                if (fits(listOf(shown.last(), n - 1))) shown += n - 1
                else if (shown.size > 1) shown[shown.lastIndex] = n - 1
            }
            if (fits(shown)) return shown
            if (every >= n) return listOf(0)
            every++
        }
    }

    /** Where a label [w] px wide under slot [i] of [n] in [width] px starts: centred, but never past either end. */
    fun labelLeft(i: Int, w: Float, width: Float, n: Int): Float {
        val slot = width / max(1, n)
        return (slot * i + slot / 2 - w / 2).coerceIn(0f, max(0f, width - w))
    }

    /** Only the first and the last (a card): both when they fit side by side, else the first. */
    fun endIndices(n: Int, width: Float, first: Float, last: Float, gap: Float): List<Int> = when {
        n <= 0 -> emptyList()
        n == 1 -> listOf(0)
        first + last + gap <= width -> listOf(0, n - 1)
        else -> listOf(0)
    }

    /** The label under [x] px of a plot [width] px wide with [n] equal slots. */
    fun indexAt(x: Float, width: Float, n: Int): Int = if (n <= 0 || width <= 0f) 0 else floor(x / width * n).toInt().coerceIn(0, n - 1)

    /** A donut's or a pie's slices: each value's share of the sum (negatives count as none); all none when the sum is not more than 0. */
    fun shares(values: List<Double>): List<Double> {
        val clean = values.map { if (it.isFinite() && it > 0) it else 0.0 }
        val sum = clean.sum()
        return if (sum <= 0) clean.map { 0.0 } else clean.map { it / sum }
    }

    /** A share written as a whole percent ("42%"), or with one decimal under 10 ("4.5%"). */
    fun percent(share: Double): String {
        val p = share * 100
        return if (p < 10 && p != floor(p)) String.format(java.util.Locale.ROOT, "%.1f%%", p).replace(".0%", "%")
        else "${kotlin.math.round(p).toInt()}%"
    }

    /**
     * The slice under a point [dx], [dy] from a circle's centre (angles from twelve o'clock, clockwise), or null outside
     * the ring from [inner] to [outer].
     */
    fun sliceAt(dx: Float, dy: Float, inner: Float, outer: Float, shares: List<Double>): Int? {
        val r = kotlin.math.sqrt(dx * dx + dy * dy)
        if (r < inner || r > outer) return null
        var angle = Math.toDegrees(kotlin.math.atan2(dx.toDouble(), -dy.toDouble()))
        if (angle < 0) angle += 360.0
        var start = 0.0
        shares.forEachIndexed { i, share ->
            val end = start + share * 360.0
            if (share > 0 && angle >= start && angle < end) return i
            start = end
        }
        return null
    }

    /** The stacked tops of [series] at each label: series k's top is the sum of 0…k (non-finite as none). */
    fun stackedTops(series: List<List<Double>>, n: Int): List<List<Double>> {
        val sums = DoubleArray(n)
        return series.map { values -> List(n) { i -> sums[i] += values.getOrNull(i)?.takeIf(Double::isFinite) ?: 0.0; sums[i] } }
    }
}

/**
 * A bar or line chart. [compact] (a card) is short and writes no scale, only the first and last labels; otherwise the
 * scale is on the right and the labels below are thinned so they never overlap. Tapping (or dragging across) picks a
 * label: its values show above the plot; tapping it again lets go. More than one series gets a legend.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun JChart(data: JChartData, modifier: Modifier = Modifier, compact: Boolean = false, title: String? = null, detail: String? = null) {
    if (data.kind == JChartKind.Donut || data.kind == JChartKind.Pie) return JPieChart(data, modifier, compact, title, detail)
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val n = data.labels.size
    var picked by remember(data) { mutableStateOf<Int?>(null) }
    val colors = data.series.mapIndexed { index, s -> s.tone?.let { palette.tone(it).dot } ?: s.hue?.let { palette.hue(it).dot } ?: palette.series(index) }
    val candle = stringResource(R.string.designsystem_candle)
    val volume = stringResource(R.string.designsystem_candle_volume)
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(if (compact) 4.dp else 6.dp)) {
        if (title != null) Text(title, style = if (compact) type.caption else type.headline, color = if (compact) palette.ink2 else palette.ink,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (data.series.size > 1) FlowRow(horizontalArrangement = Arrangement.spacedBy(12.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            data.series.forEachIndexed { index, s -> Swatch(colors[index], s.name.orEmpty(), null) }
        }
        val index = picked?.takeIf { it < n }
        if (index != null) FlowRow(horizontalArrangement = Arrangement.spacedBy(12.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
            Text(data.labels[index], style = type.micro, color = palette.ink2, maxLines = 1)
            val c = data.candles.getOrNull(index)
            if (data.kind == JChartKind.Candlestick && c != null) {
                Text(candleText(candle, c, data.axis), style = type.micro, color = palette.ink, maxLines = 1)
                c.volume?.let { Text(volume.format(data.axis(it)), style = type.micro, color = palette.ink2, maxLines = 1) }
            } else data.series.forEachIndexed { i, s ->
                Swatch(colors[i], s.name?.takeIf { data.series.size > 1 }, s.displays.getOrNull(index) ?: "—")
            }
        }
        val separator = stringResource(R.string.designsystem_separator)
        val seriesSeparator = stringResource(R.string.designsystem_series_separator)
        val labeled = stringResource(R.string.designsystem_labeled)
        val summary = remember(data, separator, seriesSeparator, labeled, candle) {
            if (data.kind == JChartKind.Candlestick) data.labels.indices.joinToString(seriesSeparator) { i ->
                "${data.labels[i]} ${data.candles.getOrNull(i)?.let { candleText(candle, it, data.axis) }.orEmpty()}"
            } else data.series.joinToString(seriesSeparator) { s ->
                val values = data.labels.indices.joinToString(separator) { "${data.labels[it]} ${s.displays.getOrNull(it).orEmpty()}" }
                s.name?.let { labeled.format(it, values) } ?: values
            }
        }
        ChartPlot(data, chartValues(data), colors, compact, index, onPick = { picked = it },
            Modifier.fillMaxWidth().height(if (compact) 72.dp else if (hasVolume(data)) 220.dp else 180.dp).semantics { contentDescription = summary })
        if (detail != null) Text(detail, style = type.caption, color = palette.ink3)
    }
}

/** A candle's four numbers in the page's words ("开 1.2 · 高 1.3 · 低 1.1 · 收 1.25"). */
private fun candleText(format: String, c: JCandle, axis: (Double) -> String) = format.format(axis(c.open), axis(c.high), axis(c.low), axis(c.close))

private fun hasVolume(data: JChartData) = data.kind == JChartKind.Candlestick && data.candles.isNotEmpty() && data.candles.all { it.volume != null }

@Composable
private fun Swatch(color: Color, name: String?, value: String?) {
    val palette = PersonaTheme.palette
    Row(verticalAlignment = Alignment.CenterVertically) {
        Box(Modifier.size(8.dp).background(color, PersonaTheme.shapes.controlShape))
        if (name != null) {
            Spacer(Modifier.width(4.dp))
            Text(name, style = PersonaTheme.type.micro, color = palette.ink2, maxLines = 1)
        }
        if (value != null) {
            Spacer(Modifier.width(4.dp))
            Text(value, style = PersonaTheme.type.micro, color = palette.ink, maxLines = 1)
        }
    }
}

@Composable
private fun ChartPlot(data: JChartData, values: List<List<Double>>, colors: List<Color>, compact: Boolean, picked: Int?, onPick: (Int?) -> Unit, modifier: Modifier) {
    val palette = PersonaTheme.palette
    val measurer = rememberTextMeasurer()
    val textStyle = PersonaTheme.type.micro.copy(color = palette.ink3)
    val n = data.labels.size
    val (low, high) = remember(data) { ChartMath.range(data) }
    val ticks = remember(low, high) { if (compact) listOf(low, high) else ChartMath.ticks(low, high) }
    val top = ticks.max()
    val bottom = ticks.min()
    // The scale's numbers (not on a card) and every label, measured once per chart.
    val scale = remember(data, ticks, compact, textStyle) {
        if (compact) emptyList() else ticks.map { measurer.measure(data.axis(it), textStyle) }
    }
    val labels: List<TextLayoutResult> = remember(data, textStyle) { data.labels.map { measurer.measure(it, textStyle, maxLines = 1) } }
    val currentPick by rememberUpdatedState(picked)
    val pick by rememberUpdatedState(onPick)
    val gutter = (scale.maxOfOrNull { it.size.width } ?: 0).toFloat()
    Canvas(modifier
        .pointerInput(n, gutter) {
            detectTapGestures { offset ->
                val width = size.width - (if (gutter > 0) gutter + 8.dp.toPx() else 0f)
                val i = ChartMath.indexAt(offset.x, width, n)
                pick(if (offset.x > width || currentPick == i) null else i)
            }
        }
        .pointerInput(n, gutter) {
            val width = { size.width - (if (gutter > 0) gutter + 8.dp.toPx() else 0f) }
            detectHorizontalDragGestures(onDragEnd = {}) { change, _ ->
                change.consume()
                pick(ChartMath.indexAt(change.position.x, width(), n))
            }
        }) {
        val gap = 8.dp.toPx()
        val labelHeight = labels.maxOfOrNull { it.size.height }?.toFloat() ?: 0f
        val plotWidth = (size.width - (if (gutter > 0) gutter + gap else 0f)).coerceAtLeast(1f)
        val fullHeight = (size.height - labelHeight - 6.dp.toPx()).coerceAtLeast(1f)
        // a candlestick's volume has the bottom fifth of the plot to itself, under the prices
        val volumeHeight = if (hasVolume(data) && !compact) fullHeight * 0.22f else 0f
        val plotHeight = (fullHeight - (if (volumeHeight > 0) volumeHeight + 4.dp.toPx() else 0f)).coerceAtLeast(1f)
        val slot = plotWidth / max(1, n)
        fun y(v: Double) = (plotHeight * (1 - (v - bottom) / (top - bottom))).toFloat()
        fun cx(i: Int) = slot * i + slot / 2
        // Grid lines and the scale on the right; a card has only the zero line.
        if (!compact) ticks.forEachIndexed { k, tick ->
            val yy = y(tick)
            drawLine(if (tick == 0.0) palette.ink3.copy(alpha = 0.5f) else palette.line, Offset(0f, yy), Offset(plotWidth, yy), 1.dp.toPx())
            val label = scale[k]
            drawText(label, topLeft = Offset(plotWidth + gap, (yy - label.size.height / 2).coerceIn(0f, max(0f, plotHeight - label.size.height))))
        } else if (data.kind != JChartKind.Candlestick) drawLine(palette.line, Offset(0f, y(0.0)), Offset(plotWidth, y(0.0)), 1.dp.toPx())
        // The labels under the plot: thinned on a page, the first and last on a card.
        val widths = labels.map { it.size.width.toFloat() }
        val shown = if (compact) ChartMath.endIndices(n, plotWidth, widths.firstOrNull() ?: 0f, widths.lastOrNull() ?: 0f, gap)
        else ChartMath.labelIndices(widths, plotWidth, gap)
        shown.forEach { i ->
            val w = widths[i]
            val left = when {
                compact && i == 0 -> 0f
                compact && i == n - 1 -> plotWidth - w
                else -> ChartMath.labelLeft(i, w, plotWidth, n)
            }.coerceIn(0f, max(0f, plotWidth - w))
            drawText(labels[i], topLeft = Offset(left, fullHeight + 6.dp.toPx()))
        }
        val dim = picked != null
        when (data.kind) {
            JChartKind.Bar -> {
                val group = min(slot * 0.72f, 28.dp.toPx() * (if (data.stacked) 1 else data.series.size))
                val corner = CornerRadius(min(3.dp.toPx(), group / 6), min(3.dp.toPx(), group / 6))
                for (i in 0 until n) {
                    val alpha = if (dim && i != picked) 0.35f else 1f
                    if (data.stacked && data.series.size > 1) {
                        var up = 0.0
                        var down = 0.0
                        data.series.forEachIndexed { k, _ ->
                            val v = values[k].getOrNull(i)?.takeIf(Double::isFinite) ?: return@forEachIndexed
                            val (from, to) = if (v >= 0) (up to up + v).also { up += v } else (down to down + v).also { down += v }
                            val y1 = y(to); val y0 = y(from)
                            drawRect(colors[k].copy(alpha = alpha), Offset(cx(i) - group / 2, min(y0, y1)), Size(group, abs(y0 - y1)))
                        }
                    } else {
                        val bar = group / data.series.size
                        data.series.forEachIndexed { k, _ ->
                            val v = values[k].getOrNull(i)?.takeIf(Double::isFinite) ?: return@forEachIndexed
                            val y0 = y(0.0); val y1 = y(v)
                            val x = cx(i) - group / 2 + bar * k
                            val inset = if (data.series.size > 1) 1.dp.toPx() else 0f
                            drawRoundRect(colors[k].copy(alpha = alpha), Offset(x + inset / 2, min(y0, y1)), Size(bar - inset, max(1f, abs(y0 - y1))), corner)
                        }
                    }
                }
            }
            JChartKind.Line -> {
                if (picked != null) drawLine(palette.ink3, Offset(cx(picked), 0f), Offset(cx(picked), plotHeight), 1.dp.toPx())
                data.series.forEachIndexed { k, s ->
                    val points = (0 until n).mapNotNull { i -> values[k].getOrNull(i)?.takeIf(Double::isFinite)?.let { Offset(cx(i), y(it)) } }
                    if (points.isEmpty()) return@forEachIndexed
                    val line = Path().apply { points.forEachIndexed { i, p -> if (i == 0) moveTo(p.x, p.y) else lineTo(p.x, p.y) } }
                    if (data.series.size == 1 && points.size > 1) {
                        val area = Path().apply { addPath(line); lineTo(points.last().x, y(max(bottom, 0.0))); lineTo(points.first().x, y(max(bottom, 0.0))); close() }
                        drawPath(area, colors[k].copy(alpha = 0.1f))
                    }
                    drawPath(line, colors[k], style = Stroke((if (compact) 1.5.dp else 2.dp).toPx(), cap = StrokeCap.Round, join = StrokeJoin.Round))
                    if (points.size == 1 || (!compact && n <= 24)) points.forEach { drawCircle(colors[k], 2.5.dp.toPx(), it) }
                    picked?.let { i -> values[k].getOrNull(i)?.takeIf(Double::isFinite)?.let { v ->
                        drawCircle(palette.background, 5.dp.toPx(), Offset(cx(i), y(v)))
                        drawCircle(colors[k], 3.5.dp.toPx(), Offset(cx(i), y(v)))
                    } }
                }
            }
            JChartKind.Area -> {
                if (picked != null) drawLine(palette.ink3, Offset(cx(picked), 0f), Offset(cx(picked), plotHeight), 1.dp.toPx())
                val stacked = data.stacked && data.series.size > 1
                val tops = if (stacked) ChartMath.stackedTops(values, n) else values
                val base = y(max(bottom, 0.0))
                // stacked: each band between the one under it and its own top, the lowest first
                data.series.indices.forEach { k ->
                    val points = (0 until n).mapNotNull { i -> tops[k].getOrNull(i)?.takeIf(Double::isFinite)?.let { Offset(cx(i), y(it)) } }
                    if (points.size < 2) { points.forEach { drawCircle(colors[k], 2.5.dp.toPx(), it) }; return@forEach }
                    val line = Path().apply { points.forEachIndexed { i, p -> if (i == 0) moveTo(p.x, p.y) else lineTo(p.x, p.y) } }
                    val area = Path().apply {
                        addPath(line)
                        if (stacked && k > 0) (n - 1 downTo 0).forEach { i -> lineTo(cx(i), y(tops[k - 1][i])) }
                        else { lineTo(points.last().x, base); lineTo(points.first().x, base) }
                        close()
                    }
                    drawPath(area, colors[k].copy(alpha = if (stacked) 0.32f else if (data.series.size > 1) 0.16f else 0.22f))
                    drawPath(line, colors[k], style = Stroke((if (compact) 1.5.dp else 2.dp).toPx(), cap = StrokeCap.Round, join = StrokeJoin.Round))
                    picked?.let { i -> tops[k].getOrNull(i)?.takeIf(Double::isFinite)?.let { v ->
                        drawCircle(palette.background, 5.dp.toPx(), Offset(cx(i), y(v)))
                        drawCircle(colors[k], 3.5.dp.toPx(), Offset(cx(i), y(v)))
                    } }
                }
            }
            JChartKind.Candlestick -> {
                val up = if (data.risingRed) palette.danger.dot else palette.tone(Tone.Positive).dot
                val down = if (data.risingRed) palette.tone(Tone.Positive).dot else palette.danger.dot
                val body = min(slot * 0.7f, 12.dp.toPx()).coerceAtLeast(1f)
                val wick = min(1.dp.toPx(), body)
                val volumes = data.candles.mapNotNull { it.volume }
                val most = volumes.maxOrNull()?.takeIf { it > 0 } ?: 1.0
                val volumeTop = fullHeight - volumeHeight
                data.candles.take(n).forEachIndexed { i, c ->
                    if (!c.open.isFinite() || !c.close.isFinite() || !c.high.isFinite() || !c.low.isFinite()) return@forEachIndexed
                    val color = (if (c.rising) up else down).copy(alpha = if (dim && i != picked) 0.35f else 1f)
                    drawLine(color, Offset(cx(i), y(c.high)), Offset(cx(i), y(c.low)), wick)
                    val y0 = y(c.open); val y1 = y(c.close)
                    drawRect(color, Offset(cx(i) - body / 2, min(y0, y1)), Size(body, max(1.dp.toPx(), abs(y0 - y1))))
                    if (volumeHeight > 0) c.volume?.let { v ->
                        val h = (volumeHeight * (v / most)).toFloat().coerceAtLeast(1f)
                        drawRect(color.copy(alpha = color.alpha * 0.45f), Offset(cx(i) - body / 2, volumeTop + volumeHeight - h), Size(body, h))
                    }
                }
                if (picked != null) drawLine(palette.ink3, Offset(cx(picked), 0f), Offset(cx(picked), fullHeight), 1.dp.toPx())
            }
            JChartKind.Donut, JChartKind.Pie -> Unit
        }
    }
}

/**
 * The values a chart's marks are drawn at this frame (#2811): the data's own, but when a chart is drawn again with the
 * same labels and series its marks go from where they were to the new values. The scale goes to the new one at once.
 * Nothing moves the first time or with reduced motion.
 */
@Composable
private fun chartValues(data: JChartData): List<List<Double>> {
    val target = data.series.map { it.values }
    val shape = data.kind to data.labels to target.map { it.size }
    val reduce = PersonaTheme.reduceMotion
    val duration = PersonaTheme.motion.count
    var last by remember { mutableStateOf(shape to target) }
    var shown by remember { mutableStateOf(target) }
    val progress = remember { androidx.compose.animation.core.Animatable(1f) }
    LaunchedEffect(target, shape) {
        val (lastShape, lastTarget) = last
        last = shape to target
        if (reduce || lastShape != shape || lastTarget == target) { shown = target; return@LaunchedEffect }
        val from = shown.takeIf { it.map(List<Double>::size) == target.map(List<Double>::size) } ?: lastTarget
        progress.snapTo(0f)
        progress.animateTo(1f, androidx.compose.animation.core.tween(duration, easing = androidx.compose.animation.core.FastOutSlowInEasing)) {
            val p = value.toDouble()
            shown = target.mapIndexed { k, row -> row.mapIndexed { i, to ->
                val f = from[k][i]
                if (f.isFinite() && to.isFinite()) f + (to - f) * p else to
            } }
        }
        shown = target
    }
    // the frame the data changed in still draws the old marks (same shape), the effect moves them from there
    return if (last.first == shape && shown.map(List<Double>::size) == target.map(List<Double>::size)) shown else target
}

/** A slice's colour: the series colours first, then named hues that differ from them (up to eight slices). */
fun PersonaPalette.slice(index: Int): Color = if (index < 4) series(index)
else hue(listOf(JHue.Teal, JHue.Pink, JHue.Amber, JHue.Indigo)[(index - 4) % 4]).dot

/**
 * A donut or a pie: one series' values as slices of a ring (a donut writes [JChartData.center] in its hole), a legend
 * beside it (label, value, share). Tapping a slice or its legend line picks it (the others dim); tapping again lets go.
 * A card's is small and has no legend values.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun JPieChart(data: JChartData, modifier: Modifier, compact: Boolean, title: String?, detail: String?) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val series = data.series.firstOrNull() ?: return
    val shares = remember(series) { ChartMath.shares(series.values) }
    val colors = data.labels.indices.map { i -> if (series.hue != null && data.labels.size == 1) palette.hue(series.hue).dot else palette.slice(i) }
    var picked by remember(data) { mutableStateOf<Int?>(null) }
    val donut = data.kind == JChartKind.Donut
    val separator = stringResource(R.string.designsystem_separator)
    val summary = remember(data, separator) {
        data.labels.indices.joinToString(separator) { "${data.labels[it]} ${series.displays.getOrNull(it).orEmpty()} ${ChartMath.percent(shares[it])}" }
    }
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(if (compact) 4.dp else 8.dp)) {
        if (title != null) Text(title, style = if (compact) type.caption else type.headline, color = if (compact) palette.ink2 else palette.ink,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.l)) {
            val ring = if (compact) 72.dp else 148.dp
            Box(Modifier.size(ring).semantics { contentDescription = summary }, contentAlignment = Alignment.Center) {
                val current by rememberUpdatedState(picked)
                Canvas(Modifier.matchParentSize().pointerInput(shares) {
                    detectTapGestures { o ->
                        val outer = size.width / 2f
                        val i = ChartMath.sliceAt(o.x - outer, o.y - outer, if (donut) outer * 0.62f else 0f, outer, shares)
                        picked = if (i == null || i == current) null else i
                    }
                }) {
                    val stroke = size.minDimension * 0.19f
                    var start = -90f
                    shares.forEachIndexed { i, share ->
                        val sweep = (share * 360).toFloat()
                        if (sweep <= 0f) return@forEachIndexed
                        val color = colors[i].copy(alpha = if (picked != null && picked != i) 0.35f else 1f)
                        // a hairline of the page between slices
                        val gap = if (shares.count { it > 0 } > 1) min(1.2f, sweep / 2) else 0f
                        if (donut) drawArc(color, start + gap / 2, sweep - gap, false, Offset(stroke / 2, stroke / 2),
                            Size(size.width - stroke, size.height - stroke), style = Stroke(stroke))
                        else drawArc(color, start + gap / 2, sweep - gap, true, Offset.Zero, size)
                        start += sweep
                    }
                }
                if (donut) {
                    val pickedIndex = picked
                    val words = if (pickedIndex != null) ChartMath.percent(shares[pickedIndex]) else data.center
                    words?.takeIf { it.isNotBlank() }?.let {
                        Text(it, style = if (compact) type.micro else type.bodyStrong, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.padding(horizontal = 4.dp))
                    }
                }
            }
            if (!compact || data.labels.size <= 4) Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(if (compact) 2.dp else 6.dp)) {
                data.labels.forEachIndexed { i, label ->
                    Row(Modifier.fillMaxWidth().pointerInput(i) { detectTapGestures { picked = if (picked == i) null else i } }
                        .stableAlpha(if (picked != null && picked != i) 0.45f else 1f), verticalAlignment = Alignment.CenterVertically) {
                        Box(Modifier.size(8.dp).background(colors[i], PersonaTheme.shapes.controlShape))
                        Spacer(Modifier.width(6.dp))
                        Text(label, Modifier.weight(1f), style = if (compact) type.micro else type.callout, color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        if (!compact) {
                            Spacer(Modifier.width(6.dp))
                            Text(series.displays.getOrNull(i).orEmpty(), style = type.micro, color = palette.ink2, maxLines = 1)
                        }
                        Spacer(Modifier.width(6.dp))
                        Text(ChartMath.percent(shares[i]), style = type.micro, color = palette.ink3, maxLines = 1)
                    }
                }
            }
        }
        if (detail != null) Text(detail, style = type.caption, color = palette.ink3)
    }
}

/** A small trend line's data (a row's, a number's, a tile's): [values] only, in a [hue] or a [tone], [fill] shading under it. */
data class JSpark(val values: List<Double>, val hue: JHue? = null, val tone: Tone? = null, val fill: Boolean = false)

/** The size of a [JSparkline] in a row or a tile. */
object SparkSize {
    val width = 56.dp
    val height = 20.dp
}

/**
 * A small trend line, no axes: [spark]'s values from lowest to highest across the box (a row's or a tile's, or as wide
 * as the cell under a number). Without a hue or tone it is the quiet ink. Read out as its first and last values.
 */
@Composable
fun JSparkline(spark: JSpark, modifier: Modifier = Modifier.size(SparkSize.width, SparkSize.height)) {
    val palette = PersonaTheme.palette
    val color = spark.tone?.takeIf { it != Tone.Neutral }?.let { palette.tone(it).dot } ?: spark.hue?.let { palette.hue(it).dot } ?: palette.ink3
    val values = spark.values.filter(Double::isFinite)
    if (values.size < 2) return
    val description = "${ChartMath.compact(values.first())} → ${ChartMath.compact(values.last())}"
    Canvas(modifier.semantics { contentDescription = description }) {
        val low = values.min(); val high = values.max()
        val span = (high - low).takeIf { it > 0 } ?: 1.0
        val inset = 1.5.dp.toPx()
        val h = size.height - inset * 2
        val points = values.mapIndexed { i, v -> Offset(size.width * i / (values.size - 1), (inset + h * (1 - (v - low) / span)).toFloat()) }
        val line = Path().apply { points.forEachIndexed { i, p -> if (i == 0) moveTo(p.x, p.y) else lineTo(p.x, p.y) } }
        if (spark.fill) drawPath(Path().apply { addPath(line); lineTo(size.width, size.height); lineTo(0f, size.height); close() }, color.copy(alpha = 0.16f))
        drawPath(line, color, style = Stroke(1.5.dp.toPx(), cap = StrokeCap.Round, join = StrokeJoin.Round))
    }
}

/** One number of a [JStatGrid]: [detail] is coloured by [tone] (neutral is the quiet grey); [spark] a trend line under it. */
data class JStat(val label: String, val value: String, val detail: String? = null, val tone: Tone = Tone.Neutral, val spark: JSpark? = null)

/**
 * A few numbers side by side: two or three to a row by the width (three where three fit, and four as two rows of
 * two), each a label, the number and a detail line.
 */
@Composable
fun JStatGrid(stats: List<JStat>, modifier: Modifier = Modifier) {
    if (stats.isEmpty()) return
    BoxWithConstraints(modifier.fillMaxWidth()) {
        val columns = statColumns(stats.size, maxWidth.value)
        Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
            stats.chunked(columns).forEach { row ->
                Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    row.forEach { StatCell(it, Modifier.weight(1f)) }
                    repeat(columns - row.size) { Spacer(Modifier.weight(1f)) }
                }
            }
        }
    }
}

/** How many numbers go on a row of [width] dp: all of one or two, three where they fit, four as two and two. */
fun statColumns(count: Int, width: Float): Int = when {
    count <= 2 -> max(1, count)
    count == 4 -> 2
    width >= 300f -> 3
    else -> 2
}

@Composable
private fun StatCell(stat: JStat, modifier: Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Column(modifier, verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Text(stat.label, style = type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        JChangingText(stat.value, style = type.number, color = palette.ink)
        stat.spark?.let { JSparkline(it, Modifier.fillMaxWidth().height(SparkSize.height)) }
        stat.detail?.takeIf { it.isNotBlank() }?.let {
            Text(it, style = type.caption, color = if (stat.tone == Tone.Neutral) palette.ink3 else palette.tone(stat.tone).text, maxLines = 2,
                overflow = TextOverflow.Ellipsis)
        }
    }
}
