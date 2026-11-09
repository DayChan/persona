import Charts
import SwiftUI

/// A chart of values over categories (a plugin's `chart` item, docs/extensions/extensions.md「UI 插槽」): bars (side by side or
/// stacked) or lines, one to a few series, in the design system's colors. A series without a color of its own takes
/// the data hues in order (the first is the accent). Holding (iPhone) or hovering (Mac) a category shows its label and
/// every series' value above the plot. Compact (a card): no axis text, only the marks and the first and last labels.
/// Nothing here knows about plugins: the caller maps its data onto `DataChart.Series`.
struct DataChart: View {
    enum Kind: Sendable { case bar, line, area, candlestick, donut, pie }

    /// A candlestick's four prices (and how much traded), at one category.
    struct Candle: Sendable {
        var open: Double
        var high: Double
        var low: Double
        var close: Double
        var volume: Double?
    }

    struct Series: Identifiable, Sendable {
        var name: String
        /// A role color (`Theme.positive`…); nil: the next data hue.
        var color: Color?
        var values: [Double]
        /// How each value is written ("¥1.20"); nil at an index: `format` of the value.
        var displays: [String?] = []
        var id: String { name }

        func text(_ i: Int, _ format: (Double) -> String) -> String {
            if i < displays.count, let d = displays[i], !d.isEmpty { return d }
            return i < values.count ? format(values[i]) : ""
        }
    }

    var kind: Kind
    /// The categories along the x axis; every series' values line up with them.
    var labels: [String]
    var series: [Series]
    /// Bars of several series on top of each other (lines ignore it).
    var stacked = false
    /// How a value reads where the plugin gave no `display` (with its unit).
    var format: (Double) -> String = { DataChart.compact($0) }
    /// A card: a shorter plot, no axis text, no legend.
    var compact = false
    /// A candlestick's prices, one per label.
    var candles: [Candle] = []
    /// A candlestick's rising and falling colors (which is green is the market's).
    var rising: Color = Theme.Market.green
    var falling: Color = Theme.Market.red
    /// The words in a donut's hole.
    var center: String? = nil

    @State private var picked: String?

    private struct Point: Identifiable {
        var id: String { series + "@" + String(index) }
        var series: String
        var index: Int
        var label: String
        var value: Double
    }

    /// The data hues, in order: different enough from each other in light and dark.
    static let hues: [Color] = [Theme.accent, Theme.Hue.orange, Theme.Hue.green, Theme.Hue.purple]
    /// A donut's or a pie's slices: up to eight, each its own.
    static let sliceHues: [Color] = [Theme.accent, Theme.Hue.orange, Theme.Hue.green, Theme.Hue.purple, Theme.Hue.teal,
                                     Theme.Hue.pink, Theme.Hue.amber, Theme.Hue.indigo]

    private func color(_ i: Int) -> Color { series[i].color ?? Self.hues[i % Self.hues.count] }

    private var points: [Point] {
        series.flatMap { s in
            s.values.prefix(labels.count).enumerated().compactMap { i, v in
                v.isFinite ? Point(series: s.name, index: i, label: labels[i], value: v) : nil
            }
        }
    }

    /// The x labels written under the plot: all when they fit, else every few (always the first and the last), so they
    /// never overlap. A compact chart writes only the first and the last.
    private var shownLabels: [String] {
        guard labels.count > 2 else { return labels }
        if compact { return [labels[0], labels[labels.count - 1]] }
        let most = 6
        guard labels.count > most else { return labels }
        let step = Int((Double(labels.count - 1) / Double(most - 1)).rounded(.up))
        var out = stride(from: 0, to: labels.count - 1, by: step).map { labels[$0] }
        if labels.count - 1 - (out.count - 1) * step < step / 2 + 1, out.count > 1 { out.removeLast() }
        out.append(labels[labels.count - 1])
        return out
    }

    var body: some View {
        switch kind {
        case .donut, .pie: SharesChart(labels: labels, values: series.first?.values ?? [],
                                       displays: series.first?.displays ?? [], colors: series.first?.color.map { [$0] },
                                       hole: kind == .donut, center: center, format: format, compact: compact)
        case .candlestick: CandleChart(labels: labels, candles: candles, rising: rising, falling: falling,
                                       format: format, compact: compact, shownLabels: shownLabels)
        default: plot
        }
    }

    @ViewBuilder private var plot: some View {
        let pts = points
        let names = series.map(\.name)
        let shown = Set(shownLabels)
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if !compact { readout }
            Chart(pts) { p in
                switch kind {
                case .bar:
                    if stacked || series.count == 1 {
                        BarMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value))
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                            .opacity(picked == nil || picked == p.label ? 1 : 0.4)
                    } else {
                        BarMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value))
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                            .position(by: .value(String(localized: "Series"), p.series))
                            .opacity(picked == nil || picked == p.label ? 1 : 0.4)
                    }
                case .area:
                    if stacked {
                        AreaMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value), stacking: .standard)
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                            .interpolationMethod(.monotone)
                            .opacity(0.55)
                    } else {
                        AreaMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value), stacking: .unstacked)
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                            .interpolationMethod(.monotone)
                            .opacity(0.18)
                        LineMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value), series: .value(String(localized: "Series"), p.series))
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                            .interpolationMethod(.monotone)
                            .lineStyle(StrokeStyle(lineWidth: Theme.Size.rule))
                    }
                    if picked == p.label {
                        RuleMark(x: .value(String(localized: "Category"), p.label)).foregroundStyle(Theme.Fill.chip)
                    }
                // Candlesticks and shares are drawn by their own charts (`body`).
                case .line, .candlestick, .donut, .pie:
                    LineMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value), series: .value(String(localized: "Series"), p.series))
                        .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                        .interpolationMethod(.monotone)
                        .lineStyle(StrokeStyle(lineWidth: Theme.Size.rule))
                    if picked == p.label {
                        PointMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value))
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                    }
                }
            }
            .chartForegroundStyleScale(domain: names, range: names.indices.map(color))
            .chartLegend(.hidden)
            .chartXAxis {
                // Every category gets a mark (a categorical axis takes no subset); only the shown ones say anything.
                AxisMarks { v in
                    AxisValueLabel {
                        let l = v.as(String.self) ?? ""
                        Text(shown.contains(l) ? l : "")
                    }
                    .font(Theme.Font.label).foregroundStyle(Color.secondary)
                }
            }
            .chartYAxis {
                AxisMarks(position: .leading, values: .automatic(desiredCount: 4)) { v in
                    AxisGridLine(stroke: StrokeStyle(lineWidth: Theme.Size.hairline)).foregroundStyle(Theme.Fill.chip)
                    AxisValueLabel {
                        if let d = v.as(Double.self) { Text(Self.compact(d)) }
                    }
                    .font(Theme.Font.label).foregroundStyle(Color.secondary)
                }
            }
            .chartYAxis(compact ? .hidden : .automatic)
            .chartXSelection(value: $picked)
            .scaledFrame(height: compact ? Theme.Size.box80 : Theme.Size.box150)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(accessibilitySummary)

            if series.count > 1 && !compact { legend }
        }
    }

    /// What the held category reads: its label and every series' value; nothing held, a line of the same height (the
    /// plot does not jump when a finger goes down).
    private var readout: some View {
        let i = picked.flatMap { labels.firstIndex(of: $0) }
        return HStack(spacing: Theme.Space.m) {
            if let i {
                Text(labels[i]).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                ForEach(Array(series.enumerated()), id: \.offset) { n, s in
                    HStack(spacing: Theme.Space.xs) {
                        if series.count > 1 { Circle().fill(color(n)).scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS) }
                        Text(s.text(i, format)).font(Theme.Font.label.monospacedDigit())
                    }
                }
            } else {
                Text(" ").font(Theme.Font.labelStrong)
            }
        }
        .lineLimit(1)
        .minimumScaleFactor(0.7)
        .accessibilityHidden(true)
    }

    private var legend: some View {
        HStack(spacing: Theme.Space.m) {
            ForEach(Array(series.enumerated()), id: \.offset) { i, s in
                HStack(spacing: Theme.Space.xs) {
                    Circle().fill(color(i)).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
                    Text(s.name).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
        }
    }

    /// VoiceOver reads each series' values by category instead of the marks one by one.
    private var accessibilitySummary: String {
        series.map { s in
            let pairs = labels.indices.prefix(s.values.count).map { "\(labels[$0]) \(s.text($0, format))" }.joined(separator: String(localized: ", ", comment: "List separator"))
            return series.count > 1 && !s.name.isEmpty ? String(localized: "\(s.name): \(pairs)", comment: "Chart series name and its values") : pairs
        }.joined(separator: String(localized: "; ", comment: "Separator between chart series"))
    }

    /// A short number: whole ones as they are, a few decimals at most, large ones as 1.2k / 3.4M / 5.6B.
    static func compact(_ v: Double) -> String {
        let a = abs(v)
        func trim(_ x: Double, _ digits: Int) -> String {
            String(format: "%.\(digits)f", x).replacingOccurrences(of: #"\.0+$|(\.\d*?)0+$"#, with: "$1", options: .regularExpression)
        }
        if a >= 1e9 { return trim(v / 1e9, 1) + "B" }
        if a >= 1e6 { return trim(v / 1e6, 1) + "M" }
        if a >= 1e4 { return trim(v / 1e3, 0) + "k" }
        if a >= 1e3 { return trim(v / 1e3, 1) + "k" }
        if v == v.rounded() { return String(Int(v)) }
        return trim(v, a < 1 ? 2 : 1)
    }
}
