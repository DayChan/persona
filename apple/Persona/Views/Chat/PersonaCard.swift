import Foundation
import SwiftUI

/// The assistant's cards in a reply (docs/extensions/extensions.md「Assistant cards: `persona-view` and `persona-card`」): a ```persona-view block holds a View or one
/// component, a ```persona-card block (#886, the older way) one card that becomes components by the rules every app
/// shares (`sdk/go/wire/personacard.go`). Both are drawn by the renderer the extensions' views are drawn by, with nothing
/// to use (no actions, fields, search, filter or more); links open as usual. Anything the app cannot draw (unknown,
/// broken or still-streaming JSON, a card without what it needs) stays the code block it is, so nothing is lost on an
/// older app, the web or Android.
enum AssistantCardBlock {
    static let viewLanguage = "persona-view"
    static let cardLanguage = "persona-card"

    /// The View in a fenced block of `language`, nil when it is not one the app can draw.
    static func parse(language: String?, _ text: String) -> ExtensionView? {
        guard let data = text.data(using: .utf8) else { return nil }
        switch language {
        case viewLanguage:
            guard let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
            if json["sections"] != nil {
                guard let v = try? JSONDecoder().decode(ExtensionView.self, from: data), v.drawable else { return nil }
                return v
            }
            guard json["type"] is String, let c = try? JSONDecoder().decode(ExtensionComponent.self, from: data) else { return nil }
            let v = ExtensionView(sections: [ExtensionSection(items: [c])])
            return v.drawable ? v : nil
        case cardLanguage:
            guard let card = try? JSONDecoder().decode(ExtensionPersonaCard.self, from: data) else { return nil }
            return card.view()
        default:
            return nil
        }
    }

    static func handles(_ language: String?) -> Bool { language == viewLanguage || language == cardLanguage }
}

nonisolated extension ExtensionView {
    /// Something on it the app draws: a View of only components it does not know stays a code block.
    var drawable: Bool {
        sections.contains { section in
            section.items.contains { component in
                switch component {
                case .unknown, .divider, .button, .field, .segmented, .html: false
                // UI version 4's code-hosting components are a page's, not a card's.
                case .header, .breadcrumb, .fileTree, .code, .diff, .commentThread, .composer: false
                default: true
                }
            }
        }
    }
}

nonisolated extension ExtensionPersonaCard {
    /// The card as the apps draw it, nil when they cannot (it stays a code block): `sdk/go/wire/personacard.go`'s rules,
    /// held to `schema/extension/examples/persona-card/` by the tests.
    func view() -> ExtensionView? {
        var parts = (segments ?? []) + (rest.map { [$0] } ?? [])
        var sum = parts.reduce(0) { $0 + $1.value }
        switch type {
        case ExtensionPersonaCardKind.stat: guard value != nil else { return nil }
        case ExtensionPersonaCardKind.stackedBar: guard !parts.isEmpty, sum > 0, parts.allSatisfy({ $0.value >= 0 }) else { return nil }
        case ExtensionPersonaCardKind.ring:
            // What the total leaves over the parts is a part of its own, last.
            if let t = total?.value, t > sum {
                parts.append(ExtensionCardPart(label: String(localized: "Remaining", comment: "Chart card: what the total leaves over the parts"), value: t - sum))
                sum = t
            }
            guard parts.count >= 2, sum > 0, parts.allSatisfy({ $0.value >= 0 }) else { return nil }
        case ExtensionPersonaCardKind.line: guard (points ?? []).count >= 2 else { return nil }
        case ExtensionPersonaCardKind.bar: guard !(points ?? []).isEmpty else { return nil }
        default: return nil
        }
        var s = ExtensionSection(title: Self.nonEmpty(title), items: [])
        if let sub = Self.nonEmpty(subtitle) { s.items.append(.text(ExtensionTextComponent(text: sub, style: "secondary"))) }
        switch type {
        case ExtensionPersonaCardKind.stat:
            s.title = nil
            s.items.append(.metric(ExtensionMetricComponent(label: Self.nonEmpty(title) ?? String(localized: "Value [Persona Card c5500a]", comment: "Chart card: label of a single value"), value: Self.amount(value ?? 0, unit),
                                                            detail: Self.nonEmpty(note))))
        case ExtensionPersonaCardKind.stackedBar:
            var u = unit, total = sum
            if let t = self.total { u = Self.nonEmpty(t.unit) ?? unit; total = t.value }
            s.items.append(.metric(ExtensionMetricComponent(label: String(localized: "Total"), value: Self.amount(total, u), detail: Self.nonEmpty(note))))
            let label = Self.nonEmpty(title) ?? String(localized: "Total")
            let shown = parts.count > 4 ? Array(parts.prefix(3)) : parts
            var series: [ExtensionChartSeries] = shown.enumerated().map { i, p in
                ExtensionChartSeries(name: p.label, tone: rest != nil && i == parts.count - 1 ? ExtensionTone.neutral : nil,
                                     points: [ExtensionChartPoint(label: label, value: p.value, display: Self.amount(p.value, u))])
            }
            if parts.count > 4 {
                let other = parts.dropFirst(3).reduce(0) { $0 + $1.value }
                series.append(ExtensionChartSeries(name: String(localized: "Other", comment: "Chart series: the remaining items"), tone: ExtensionTone.neutral,
                                                   points: [ExtensionChartPoint(label: label, value: other, display: Self.amount(other, u))]))
            }
            s.items.append(.chart(ExtensionChartComponent(kind: ExtensionChartKind.bar, series: series, unit: Self.nonEmpty(u), stacked: true)))
            for p in parts {
                s.items.append(.row(ExtensionRowComponent(title: p.label, subtitle: Self.nonEmpty(p.note), value: Self.amount(p.value, u))))
            }
        case ExtensionPersonaCardKind.ring:
            var u = unit, whole = sum
            if let t = self.total { u = Self.nonEmpty(t.unit) ?? unit; whole = t.value }
            var shown = parts
            if parts.count > 8 {
                shown = Array(parts.prefix(7)) + [ExtensionCardPart(label: String(localized: "Other", comment: "Chart series: the remaining items"),
                                                                    value: parts.dropFirst(7).reduce(0) { $0 + $1.value })]
            }
            let center = Self.amount(whole, u)
            s.items.append(.chart(ExtensionChartComponent(kind: ExtensionChartKind.donut,
                                                          series: [ExtensionChartSeries(points: shown.map { ExtensionChartPoint(label: $0.label, value: $0.value, display: Self.amount($0.value, u)) })],
                                                          unit: Self.nonEmpty(u), detail: Self.nonEmpty(note), center: center.count <= 20 ? center : nil)))
            for p in parts {
                s.items.append(.row(ExtensionRowComponent(title: p.label, subtitle: Self.nonEmpty(p.note), value: Self.amount(p.value, u))))
            }
        default: // line, bar
            if let v = value { s.items.append(.metric(ExtensionMetricComponent(label: String(localized: "Current", comment: "Chart card: the current value"), value: Self.amount(v, unit)))) }
            let pts = (points ?? []).suffix(120).map { ExtensionChartPoint(label: $0.label, value: $0.value, display: Self.amount($0.value, unit)) }
            s.items.append(.chart(ExtensionChartComponent(kind: type == ExtensionPersonaCardKind.bar ? ExtensionChartKind.bar : ExtensionChartKind.line,
                                                          series: [ExtensionChartSeries(points: Array(pts))], unit: Self.nonEmpty(unit),
                                                          detail: Self.nonEmpty(note))))
        }
        return ExtensionView(sections: [s])
    }

    private static func nonEmpty(_ s: String?) -> String? { s?.isEmpty == false ? s : nil }

    /// `v` written grouped with at most one decimal, then the unit: "2,250 kcal", "7.5 hours", "8,432".
    static func amount(_ v: Double, _ unit: String?) -> String {
        let n = number(v)
        guard let unit, !unit.isEmpty else { return n }
        return n + " " + unit
    }

    /// Grouped by thousands with at most one decimal, the same in every language: 2,250 · 7.5 · -1,200.3 · 0.
    static func number(_ v: Double) -> String {
        var r = (v * 10).rounded() / 10
        if r == 0 { r = 0 } // not -0
        let negative = r < 0
        r = abs(r)
        var whole = r.rounded(.down)
        var tenth = Int(((r - whole) * 10).rounded())
        if tenth == 10 { whole += 1; tenth = 0 }
        let digits = String(format: "%.0f", whole)
        var out = negative && (whole > 0 || tenth > 0) ? "-" : ""
        for (i, d) in digits.enumerated() {
            if i > 0 && (digits.count - i) % 3 == 0 { out.append(",") }
            out.append(d)
        }
        if tenth > 0 { out += "." + String(tenth) }
        return out
    }
}

/// An assistant's card in its reply: the View in one card, its sections' titles as headings; nothing to use, links
/// open where the chat opens them.
struct AssistantViewCard: View {
    var view: ExtensionView
    @Environment(\.openURL) private var openURL

    var body: some View {
        ExtensionViewStack(view: view, model: nil, cards: false, titles: true, link: Binding(
            get: { nil },
            set: { if let url = $0?.url { openURL(url) } }))
            .frame(maxWidth: .infinity, alignment: .leading)
            .card(padding: Theme.Space.m)
            .accessibilityIdentifier("assistant-view-card")
    }
}
