import Charts
import SwiftUI

/// A whole and its parts (an extension's `donut` or `pie` chart, the assistant's `ring` card): the ring, words in the
/// hole, and beside it each part's color, label and share. Holding a slice (iPhone) or hovering it (Mac) writes that part
/// in the hole instead. Parts take the data hues in order. Nothing here knows about extensions: the caller maps its data
/// onto `RingChart.Part`.
struct RingChart: View {
    struct Part: Sendable {
        var label: String
        var value: Double
        /// How the value is written ("555 kcal").
        var display: String
    }

    var parts: [Part]
    /// The words in the hole when nothing is held (a total); a pie has no hole.
    var center: String?
    /// A pie: slices from the middle, no hole.
    var pie = false

    @State private var picked: Double?

    /// The data hues, in order: the bars' four, then more for up to eight parts.
    static let hues: [Color] = DataChart.hues + [Theme.Hue.pink, Theme.Hue.indigo, Theme.caution, Theme.Hue.gray]

    private var shown: [(offset: Int, element: Part)] {
        Array(parts.prefix(Self.hues.count).enumerated()).filter { $0.element.value.isFinite && $0.element.value > 0 }
    }

    private var sum: Double { shown.reduce(0) { $0 + $1.element.value } }

    /// The part under the finger: the one whose slice holds the picked angle (in the values' units, as Charts gives it).
    private var held: Int? {
        guard let picked else { return nil }
        var start = 0.0
        for (i, p) in shown {
            start += p.value
            if picked <= start { return i }
        }
        return nil
    }

    var body: some View {
        HStack(alignment: .center, spacing: Theme.Space.l) {
            ring
            legend
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilitySummary)
    }

    private var ring: some View {
        let h = held
        return Chart(shown, id: \.offset) { i, p in
            SectorMark(angle: .value(String(localized: "Value [Data Chart cda1d5]"), p.value),
                       innerRadius: .ratio(pie ? 0 : 0.64), angularInset: Theme.Size.line)
                .cornerRadius(Theme.Size.bar)
                .foregroundStyle(Self.hues[i])
                .opacity(h == nil || h == i ? 1 : 0.4)
        }
        .chartLegend(.hidden)
        .chartAngleSelection(value: $picked)
        .chartBackground { proxy in
            GeometryReader { geo in
                if !pie, let frame = proxy.plotFrame {
                    let rect = geo[frame]
                    hole(h).frame(width: rect.width * 0.5).position(x: rect.midX, y: rect.midY)
                }
            }
        }
        .scaledFrame(width: Theme.Size.box120, height: Theme.Size.box120)
    }

    /// The hole: the held part's label and value, else the center words.
    @ViewBuilder
    private func hole(_ h: Int?) -> some View {
        VStack(spacing: Theme.Space.xxs) {
            if let h, h < parts.count {
                Text(parts[h].label).font(Theme.Font.label).foregroundStyle(.secondary)
                Text(parts[h].display).font(Theme.Font.labelStrong.monospacedDigit())
            } else if let center, !center.isEmpty {
                Text(center).font(Theme.Font.secondaryStrong.monospacedDigit())
            }
        }
        .lineLimit(1)
        .minimumScaleFactor(0.6)
        .multilineTextAlignment(.center)
    }

    private var legend: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            ForEach(shown, id: \.offset) { i, p in
                HStack(spacing: Theme.Space.s) {
                    Circle().fill(Self.hues[i]).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
                    Text(p.label).font(Theme.Font.label).lineLimit(1)
                    Spacer(minLength: Theme.Space.xs)
                    Text(share(p.value)).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary).lineLimit(1)
                }
                .opacity(held == nil || held == i ? 1 : 0.4)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func share(_ v: Double) -> String {
        sum > 0 ? (v / sum).formatted(.percent.precision(.fractionLength(0))) : ""
    }

    /// VoiceOver reads the whole, then each part's value and share.
    private var accessibilitySummary: String {
        let each = shown.map { "\($0.element.label) \($0.element.display) \(share($0.element.value))" }
            .joined(separator: String(localized: ", ", comment: "List separator"))
        guard let center, !center.isEmpty else { return each }
        return String(localized: "\(center): \(each)", comment: "Chart series name and its values")
    }
}
