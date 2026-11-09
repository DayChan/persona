import Charts
import SwiftUI

/// A small trend line with no axes (a stock's day in a row, the week's use under a number): `DataChart`'s line in a
/// box the size of a word, optionally shaded under it. In the quiet ink unless the caller gives a color.
struct TrendLine: View {
    var values: [Double]
    var color: Color = .secondary
    var fill = false
    var width: CGFloat? = Theme.Size.sparkWidth
    var height: CGFloat = Theme.Size.sparkHeight

    private var points: [(Int, Double)] { values.enumerated().filter { $0.element.isFinite }.map { ($0.offset, $0.element) } }

    var body: some View {
        let pts = points
        let lo = pts.map(\.1).min() ?? 0, hi = pts.map(\.1).max() ?? 1
        // A flat line sits in the middle rather than on the floor.
        let pad = hi == lo ? max(abs(hi), 1) : (hi - lo) * 0.1
        Chart {
            ForEach(pts, id: \.0) { i, v in
                if fill {
                    AreaMark(x: .value("i", i), yStart: .value("v", lo - pad), yEnd: .value("v", v))
                        .foregroundStyle(LinearGradient(colors: [color.opacity(0.28), color.opacity(0.02)],
                                                        startPoint: .top, endPoint: .bottom))
                        .interpolationMethod(.monotone)
                }
                LineMark(x: .value("i", i), y: .value("v", v))
                    .foregroundStyle(color)
                    .interpolationMethod(.monotone)
                    .lineStyle(StrokeStyle(lineWidth: Theme.Size.lineMedium, lineCap: .round, lineJoin: .round))
            }
        }
        .chartXAxis(.hidden)
        .chartYAxis(.hidden)
        .chartLegend(.hidden)
        .chartYScale(domain: (lo - pad)...(hi + pad))
        .chartXScale(domain: 0...max(values.count - 1, 1))
        .scaledFrame(width: width, height: height)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Self.spoken(values))
    }

    /// What VoiceOver says: where it went (from the first to the last), and its lowest and highest.
    static func spoken(_ values: [Double]) -> String {
        guard let first = values.first, let last = values.last else { return "" }
        let trend = last > first ? String(localized: "走势向上") : last < first ? String(localized: "走势向下") : String(localized: "走势持平")
        let lo = DataChart.compact(values.min() ?? first), hi = DataChart.compact(values.max() ?? first)
        return String(localized: "\(trend)，从 \(DataChart.compact(first)) 到 \(DataChart.compact(last))，最低 \(lo)，最高 \(hi)")
    }
}
