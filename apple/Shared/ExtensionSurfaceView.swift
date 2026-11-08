import SwiftUI
import Charts

/// No callbacks, WebViews, app renderer, navigation or credentials enter this read-only renderer.
struct ExtensionSurfaceView: View {
    var view: ExtensionView
    var family: String = "large"

    var body: some View {
        VStack(alignment: .leading, spacing: ExtensionSurfaceTokens.spacing) {
            ForEach(Array(ExtensionSurfaceContent.items(view, family: family).enumerated()), id: \.offset) { _, item in
                component(item)
            }
        }
        .font(ExtensionSurfaceTokens.body)
        .foregroundStyle(ExtensionSurfaceTokens.ink)
    }

    @ViewBuilder private func component(_ item: ExtensionComponent) -> some View {
        switch item {
        case .text(let text):
            Text(verbatim: String(text.text.prefix(200))).lineLimit(family == "accessory_inline" || family == "accessory_circular" ? 1 : 3)
        case .metric(let metric):
            if family == "accessory_inline" {
                Text(verbatim: metric.label + " " + metric.value)
            } else {
                stat(label: metric.label, value: metric.value, detail: metric.detail)
            }
        case .progress(let progress):
            ProgressView(value: progress.ratio.isFinite ? min(1, max(0, progress.ratio)) : 0) {
                Text(verbatim: progress.label).font(ExtensionSurfaceTokens.label).lineLimit(1)
            }
        case .row(let row):
            HStack(spacing: ExtensionSurfaceTokens.spacing) {
                VStack(alignment: .leading, spacing: ExtensionSurfaceTokens.compactSpacing) {
                    Text(verbatim: row.title).lineLimit(1)
                    if let subtitle = row.subtitle { Text(verbatim: subtitle).font(ExtensionSurfaceTokens.label).foregroundStyle(ExtensionSurfaceTokens.secondaryInk).lineLimit(1) }
                }
                Spacer(minLength: ExtensionSurfaceTokens.compactSpacing)
                if let value = row.value { Text(verbatim: value).lineLimit(1) }
            }
        case .statGrid(let grid):
            HStack(alignment: .top, spacing: ExtensionSurfaceTokens.spacing) {
                ForEach(Array(grid.stats.prefix(family == "small" ? 2 : 4).enumerated()), id: \.offset) { _, value in
                    stat(label: value.label, value: value.value, detail: value.detail)
                }
            }
        case .chart(let chart):
            ExtensionSurfaceChart(chart: chart)
        default: EmptyView()
        }
    }

    private func stat(label: String, value: String, detail: String?) -> some View {
        VStack(alignment: .leading, spacing: ExtensionSurfaceTokens.compactSpacing) {
            Text(verbatim: label).font(ExtensionSurfaceTokens.label).foregroundStyle(ExtensionSurfaceTokens.secondaryInk).lineLimit(1)
            Text(verbatim: value).font(ExtensionSurfaceTokens.value).lineLimit(1)
            if let detail { Text(verbatim: detail).font(ExtensionSurfaceTokens.label).foregroundStyle(ExtensionSurfaceTokens.secondaryInk).lineLimit(1) }
        }
    }
}

nonisolated enum ExtensionSurfaceContent {
    static func items(_ view: ExtensionView, family: String) -> [ExtensionComponent] {
        let all = view.sections.prefix(2).flatMap(\.items)
        if family.hasPrefix("accessory_") {
            return Array(all.filter {
                switch $0 {
                case .metric, .text: true
                case .progress: family != "accessory_inline"
                default: false
                }
            }.prefix(1))
        }
        let limit = family == "small" ? 4 : family == "medium" ? 6 : 12
        return Array(all.filter {
            switch $0 {
            case .metric, .statGrid, .progress, .chart, .text, .row: true
            default: false
            }
        }.prefix(limit))
    }
}

/// Monochrome labelled small multiples: series identity never depends on hue or rank. System widgets cannot hover.
/// Every mark has a VoiceOver table equivalent; the endpoint stays visible without interaction.
private struct ExtensionSurfaceChart: View {
    var chart: ExtensionChartComponent
    var body: some View {
        VStack(alignment: .leading, spacing: ExtensionSurfaceTokens.compactSpacing) {
            if let title = chart.title { Text(verbatim: title).font(ExtensionSurfaceTokens.label).lineLimit(1) }
            if chart.series.count > 1 {
                Text(verbatim: chart.series.compactMap(\.name).joined(separator: " · "))
                    .font(ExtensionSurfaceTokens.label).foregroundStyle(ExtensionSurfaceTokens.secondaryInk)
            }
            ForEach(Array(chart.series.prefix(4).enumerated()), id: \.offset) { _, series in
                let points = Array(series.points.filter { $0.value.isFinite }.prefix(64))
                if let name = series.name { Text(verbatim: name).font(ExtensionSurfaceTokens.label).lineLimit(1) }
                if chart.kind == "candlestick" {
                    // Preserve the OHLC meaning instead of inventing a close-price line.
                    ForEach(Array(points.prefix(4).enumerated()), id: \.offset) { _, point in
                        Text(verbatim: [point.label, point.open.map { String($0) }, point.high.map { String($0) }, point.low.map { String($0) }, point.close.map { String($0) }].compactMap { $0 }.joined(separator: " · "))
                            .font(ExtensionSurfaceTokens.label).lineLimit(1)
                    }
                } else {
                    Chart(Array(points.enumerated()), id: \.offset) { index, point in
                        if chart.kind == "line" || chart.kind == "area" {
                            if chart.kind == "area" {
                                AreaMark(x: .value(String(localized: "Index"), index), y: .value(String(localized: "Value"), point.value))
                                    .foregroundStyle(ExtensionSurfaceTokens.ink.opacity(ExtensionSurfaceTokens.chartWash))
                            }
                            LineMark(x: .value(String(localized: "Index"), index), y: .value(String(localized: "Value"), point.value))
                                .lineStyle(StrokeStyle(lineWidth: ExtensionSurfaceTokens.chartLine, lineCap: .round, lineJoin: .round))
                                .foregroundStyle(ExtensionSurfaceTokens.ink)
                        } else {
                            // Pie/donut data is compared on a common zero baseline, with labels retained.
                            BarMark(x: .value(String(localized: "Index"), index), y: .value(String(localized: "Value"), point.value), width: .fixed(ExtensionSurfaceTokens.chartBar))
                                .cornerRadius(ExtensionSurfaceTokens.chartRadius)
                                .foregroundStyle(ExtensionSurfaceTokens.ink)
                        }
                    }
                    .chartXAxis(.hidden).chartYAxis(.hidden)
                    .frame(height: ExtensionSurfaceTokens.chartHeight)
                    .accessibilityRepresentation {
                        VStack {
                            ForEach(Array(points.enumerated()), id: \.offset) { _, point in
                                Text(verbatim: point.label + ": " + (point.display ?? String(point.value)) + (chart.unit ?? ""))
                            }
                        }
                    }
                    if let last = points.last {
                        Text(verbatim: last.label + ": " + (last.display ?? String(last.value)) + (chart.unit ?? ""))
                            .font(ExtensionSurfaceTokens.label).lineLimit(1)
                    }
                }
            }
        }
    }
}
