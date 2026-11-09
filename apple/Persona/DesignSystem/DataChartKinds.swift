import Charts
import SwiftUI

// `DataChart`'s candlesticks and shares (UI version 6's `candlestick`, `donut` and `pie`, docs/extensions.md「UI
// version 6」): drawn apart from its bars and lines because their marks and readouts differ.

/// Candlesticks over categories: a wick from low to high, a body from open to close in the rising or falling color,
/// the volume as bars under them on the same x axis when every candle has one. Holding (iPhone) or hovering (Mac) a
/// candle reads its open, high, low, close and volume above the plot.
struct CandleChart: View {
    var labels: [String]
    var candles: [DataChart.Candle]
    var rising: Color
    var falling: Color
    var format: (Double) -> String
    var compact: Bool
    var shownLabels: [String]
    @State private var picked: String?

    private struct Candle: Identifiable {
        var id: Int
        var label: String
        var c: DataChart.Candle
        var up: Bool { c.close >= c.open }
    }

    private var items: [Candle] {
        zip(labels, candles).enumerated().compactMap { i, p in
            let c = p.1
            guard [c.open, c.high, c.low, c.close].allSatisfy(\.isFinite) else { return nil }
            return Candle(id: i, label: p.0, c: c)
        }
    }

    private var hasVolume: Bool { !candles.isEmpty && candles.allSatisfy { $0.volume != nil } }

    var body: some View {
        let items = items
        let lo = items.map(\.c.low).min() ?? 0, hi = items.map(\.c.high).max() ?? 1
        let pad = hi == lo ? max(abs(hi) * 0.01, 1) : (hi - lo) * 0.06
        let shown = Set(shownLabels)
        let plotHeight = compact ? Theme.Size.box80 : Theme.Size.box150
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if !compact { readout(items) }
            VStack(spacing: Theme.Space.xxs) {
                Chart(items) { k in
                    RuleMark(x: .value("类别", k.label), yStart: .value("最低", k.c.low), yEnd: .value("最高", k.c.high))
                        .lineStyle(StrokeStyle(lineWidth: Theme.Size.line))
                        .foregroundStyle(k.up ? rising : falling)
                        .opacity(picked == nil || picked == k.label ? 1 : 0.4)
                    RectangleMark(x: .value("类别", k.label),
                                  yStart: .value("开盘", min(k.c.open, k.c.close)),
                                  yEnd: .value("收盘", max(k.c.open, k.c.close) + (k.c.open == k.c.close ? pad * 0.05 : 0)),
                                  width: .ratio(0.6))
                        .foregroundStyle(k.up ? rising : falling)
                        .opacity(picked == nil || picked == k.label ? 1 : 0.4)
                }
                .chartYScale(domain: (lo - pad)...(hi + pad))
                .chartXAxis(hasVolume ? .hidden : .automatic)
                .chartXAxis { axis(shown) }
                .chartYAxis {
                    AxisMarks(position: .leading, values: .automatic(desiredCount: 4)) { v in
                        AxisGridLine(stroke: StrokeStyle(lineWidth: Theme.Size.hairline)).foregroundStyle(Theme.Fill.chip)
                        AxisValueLabel { if let d = v.as(Double.self) { Text(DataChart.compact(d)) } }
                            .font(Theme.Font.label).foregroundStyle(Color.secondary)
                    }
                }
                .chartYAxis(compact ? .hidden : .automatic)
                .chartXSelection(value: $picked)
                .scaledFrame(height: plotHeight)
                if hasVolume && !compact {
                    Chart(items) { k in
                        BarMark(x: .value("类别", k.label), y: .value("成交量", k.c.volume ?? 0), width: .ratio(0.6))
                            .foregroundStyle((k.up ? rising : falling).opacity(0.5))
                    }
                    .chartXAxis { axis(shown) }
                    .chartYAxis {
                        AxisMarks(position: .leading, values: .automatic(desiredCount: 2)) { v in
                            AxisValueLabel { if let d = v.as(Double.self) { Text(DataChart.compact(d)) } }
                                .font(Theme.Font.label).foregroundStyle(Color.secondary)
                        }
                    }
                    .chartXSelection(value: $picked)
                    .scaledFrame(height: plotHeight * Theme.Size.volumeShare / (1 - Theme.Size.volumeShare) + Theme.Space.xl)
                }
            }
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(items.map { spoken($0) }.joined(separator: String(localized: "；", comment: "Separator between chart series")))
        }
    }

    private func axis(_ shown: Set<String>) -> some AxisContent {
        AxisMarks { v in
            AxisValueLabel {
                let l = v.as(String.self) ?? ""
                Text(shown.contains(l) ? l : "")
            }
            .font(Theme.Font.label).foregroundStyle(Color.secondary)
        }
    }

    private func readout(_ items: [Candle]) -> some View {
        let k = picked.flatMap { p in items.first { $0.label == p } } ?? items.last
        return HStack(spacing: Theme.Space.m) {
            if let k {
                Text(k.label).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                field(String(localized: "开"), k.c.open)
                field(String(localized: "高"), k.c.high)
                field(String(localized: "低"), k.c.low)
                field(String(localized: "收"), k.c.close).foregroundStyle(k.up ? rising : falling)
                if let v = k.c.volume { field(String(localized: "量"), v, plain: true) }
            } else {
                Text(" ").font(Theme.Font.labelStrong)
            }
        }
        .lineLimit(1)
        .minimumScaleFactor(0.7)
        .accessibilityHidden(true)
    }

    private func field(_ name: String, _ v: Double, plain: Bool = false) -> some View {
        HStack(spacing: Theme.Space.xxs) {
            Text(name).foregroundStyle(.secondary)
            Text(plain ? DataChart.compact(v) : format(v)).monospacedDigit()
        }
        .font(Theme.Font.label)
    }

    private func spoken(_ k: Candle) -> String {
        var s = String(localized: "\(k.label)：开 \(format(k.c.open))，高 \(format(k.c.high))，低 \(format(k.c.low))，收 \(format(k.c.close))")
        if let v = k.c.volume { s += String(localized: "，量 \(DataChart.compact(v))") }
        return s
    }
}

/// One series' shares as a donut (its total in the hole) or a pie, with a legend beside it: each slice its label, its
/// value and its share. Tapping (or hovering) a slice brings it forward and reads it in the hole.
struct SharesChart: View {
    var labels: [String]
    var values: [Double]
    var displays: [String?]
    /// The slices' colors, else the slice hues in order.
    var colors: [Color]?
    var hole: Bool
    var center: String?
    var format: (Double) -> String
    var compact: Bool
    @State private var angle: Double?

    private struct Slice: Identifiable {
        var id: Int
        var label: String
        var value: Double
        var text: String
    }

    private var slices: [Slice] {
        zip(labels, values).enumerated().compactMap { i, p in
            guard p.1.isFinite, p.1 >= 0 else { return nil }
            let d = i < displays.count ? displays[i] : nil
            return Slice(id: i, label: p.0, value: p.1, text: d?.isEmpty == false ? d! : format(p.1))
        }
    }

    private func color(_ i: Int) -> Color {
        if let colors, colors.count > 1 { return colors[i % colors.count] }
        return DataChart.sliceHues[i % DataChart.sliceHues.count]
    }

    /// The slice the held angle falls in.
    private func picked(_ slices: [Slice], total: Double) -> Slice? {
        guard let angle else { return nil }
        var run = 0.0
        for s in slices {
            run += s.value
            if angle <= run { return s }
        }
        return nil
    }

    var body: some View {
        let slices = slices
        let total = slices.reduce(0) { $0 + $1.value }
        let on = picked(slices, total: total)
        let side = compact ? Theme.Size.box80 : Theme.Size.box150
        HStack(alignment: .center, spacing: Theme.Space.l) {
            Chart(slices) { s in
                SectorMark(angle: .value("值", s.value), innerRadius: .ratio(hole ? Theme.Size.donutHole : 0),
                           angularInset: Theme.Size.line)
                    .foregroundStyle(color(s.id))
                    .opacity(on == nil || on?.id == s.id ? 1 : 0.4)
            }
            .chartLegend(.hidden)
            .chartAngleSelection(value: $angle)
            .chartBackground { _ in
                if hole && !compact {
                    VStack(spacing: Theme.Space.flush) {
                        if let on {
                            Text(on.label).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                            Text(on.text).font(Theme.Font.labelStrong.monospacedDigit()).lineLimit(1)
                        } else if let center, !center.isEmpty {
                            Text(center).font(Theme.Font.heading.monospacedDigit()).lineLimit(1).minimumScaleFactor(0.6)
                        }
                    }
                    .padding(.horizontal, Theme.Space.s)
                }
            }
            .scaledFrame(width: side, height: side)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(slices.map { spoken($0, total) }.joined(separator: String(localized: "，", comment: "List separator")))
            if !compact { legend(slices, total: total, on: on) }
        }
    }

    private func legend(_ slices: [Slice], total: Double, on: Slice?) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            ForEach(slices) { s in
                HStack(spacing: Theme.Space.s) {
                    Circle().fill(color(s.id)).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
                    Text(s.label).font(Theme.Font.label).lineLimit(1)
                    Spacer(minLength: Theme.Space.s)
                    Text(s.text).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                    Text(Self.percent(s.value, total)).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.tertiary).lineLimit(1)
                }
                .opacity(on == nil || on?.id == s.id ? 1 : 0.5)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityHidden(true)
    }

    static func percent(_ v: Double, _ total: Double) -> String {
        guard total > 0 else { return "" }
        return (v / total).formatted(.percent.precision(.fractionLength(0)))
    }

    private func spoken(_ s: Slice, _ total: Double) -> String {
        String(localized: "\(s.label) \(s.text)，\(Self.percent(s.value, total))")
    }
}
