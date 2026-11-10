import MarkdownUI
import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

/// How wide each column of a Markdown table is drawn. MarkdownUI lays a table out as a Grid, every column as wide as
/// its widest cell; in a reply that ran a long Chinese column off the screen with nothing saying it scrolls. Columns
/// keep their natural width when the table fits; otherwise the short ones (#, names) keep theirs and the long ones
/// share what is left and wrap; only when even that does not fit does the table scroll, each column capped.
nonisolated struct TablePlan: Equatable {
    /// The text width of each column.
    var widths: [CGFloat]
    var scrolls: Bool

    /// A column's width when the table has to scroll anyway: long text wraps there.
    static let scrollCap: CGFloat = 220

    /// natural: each column's widest cell on one line; minimum: its longest piece that cannot wrap. available: the
    /// width for the columns' text (the cells' padding and borders already taken off).
    static func fit(natural: [CGFloat], minimum: [CGFloat], available: CGFloat) -> TablePlan {
        let n = natural.count
        guard n > 0 else { return TablePlan(widths: [], scrolls: false) }
        let mins = (0..<n).map { min(minimum[$0], natural[$0]) }
        if natural.reduce(0, +) <= available { return TablePlan(widths: natural, scrolls: false) }
        if mins.reduce(0, +) > available {
            return TablePlan(widths: (0..<n).map { max(mins[$0], min(natural[$0], scrollCap)) }, scrolls: true)
        }
        // Water-filling: a column narrower than an even share of what is left keeps its natural width; the rest share
        // the remainder in proportion to their natural widths, never under their minimum.
        var widths = Array(repeating: CGFloat(0), count: n)
        var open = Set(0..<n)
        var room = available
        var settled = true
        while settled && !open.isEmpty {
            settled = false
            let share = room / CGFloat(open.count)
            for c in open.sorted() where natural[c] <= share {
                widths[c] = natural[c]
                room -= natural[c]
                open.remove(c)
                settled = true
            }
        }
        while !open.isEmpty {
            let total = open.reduce(CGFloat(0)) { $0 + natural[$1] }
            var pinned = false
            for c in open.sorted() where room * natural[c] / total < mins[c] {
                widths[c] = mins[c]
                room -= mins[c]
                open.remove(c)
                pinned = true
            }
            if pinned { continue }
            for c in open { widths[c] = (room * natural[c] / total).rounded(.down) }
            open.removeAll()
        }
        return TablePlan(widths: widths, scrolls: false)
    }
}

/// A table's cells as text, measured: what `TablePlan` needs, from the table's Markdown.
nonisolated enum MarkdownTableText {
    struct Measure: Sendable {
        var natural: [CGFloat]
        var minimum: [CGFloat]
    }

    private static let memo = TextMemo<Measure>(limit: 200)

    /// The rows of a GFM table (header first, the delimiter row left out), cells without their inline markers.
    static func rows(_ markdown: String) -> [[String]] {
        var rows: [[String]] = []
        for line in markdown.split(separator: "\n").map({ $0.trimmingCharacters(in: .whitespaces) }) where line.hasPrefix("|") {
            var cells: [String] = []
            var cur = ""
            var escaped = false
            for ch in line.dropFirst() {
                if escaped { cur.append(ch); escaped = false; continue }
                if ch == "\\" { escaped = true; continue }
                if ch == "|" { cells.append(cur); cur = ""; continue }
                cur.append(ch)
            }
            if !cur.trimmingCharacters(in: .whitespaces).isEmpty { cells.append(cur) }
            let trimmed = cells.map { $0.trimmingCharacters(in: .whitespaces) }
            if trimmed.allSatisfy({ !$0.isEmpty && $0.allSatisfy { ":-".contains($0) } }) { continue } // | --- | :-: |
            rows.append(trimmed.map(plain))
        }
        return rows
    }

    /// `**a**`, `` `b` ``, `[c](u)`, `<u>` → the words shown.
    static func plain(_ s: String) -> String {
        var t = s.replacingOccurrences(of: #"\[([^\]]*)\]\([^)]*\)"#, with: "$1", options: .regularExpression)
        t = t.replacingOccurrences(of: #"<(https?://[^>]*)>"#, with: "$1", options: .regularExpression)
        for m in ["**", "__", "`", "~~"] { t = t.replacingOccurrences(of: m, with: "") }
        return t
    }

    /// Natural and minimum widths per column at the cell font (`fontSize`, the header semibold).
    static func measure(_ markdown: String, fontSize: CGFloat) -> Measure {
        memo("\(fontSize)\n" + markdown) { _ in
            let rows = rows(markdown)
            let n = rows.map(\.count).max() ?? 0
            let body = UIFont.systemFont(ofSize: fontSize), head = UIFont.systemFont(ofSize: fontSize, weight: .semibold)
            func width(_ s: String, _ f: UIFont) -> CGFloat { ceil((s as NSString).size(withAttributes: [.font: f]).width) + 2 }
            var natural = Array(repeating: CGFloat(0), count: n), minimum = Array(repeating: CGFloat(0), count: n)
            for (r, row) in rows.enumerated() {
                for (c, cell) in row.enumerated() where c < n {
                    let f = r == 0 ? head : body
                    natural[c] = max(natural[c], width(cell, f))
                    // What cannot wrap: a short header whole, else the longest ASCII word (CJK wraps between characters).
                    let piece = r == 0 && cell.count <= 8 ? cell : longestWord(cell)
                    minimum[c] = max(minimum[c], min(width(piece, f), 160))
                }
            }
            return Measure(natural: natural, minimum: minimum)
        }
    }

    private static func longestWord(_ s: String) -> String {
        var best = "", cur = ""
        for ch in s {
            if ch.isASCII && !ch.isWhitespace { cur.append(ch); continue }
            if cur.count > best.count { best = cur }
            cur = ch.isWhitespace ? "" : String(ch) // a CJK character stands alone
            if cur.count > best.count { best = cur }
            cur = ""
        }
        return cur.count > best.count ? cur : best
    }
}

/// One table's preparation, retained across geometry and scroll updates. Check the parsed content before rendering
/// it back to Markdown; the shared width memo otherwise saves measurement but still serializes the table every time.
/// Keep only the current content and font's measurement. Font changes reuse the source, content changes replace it.
@MainActor
final class MarkdownTablePreparation {
    private var content: MarkdownContent?
    private var markdown = ""
    private var fontSize: CGFloat?
    private var measured: MarkdownTableText.Measure?
    private let render: (MarkdownContent) -> String
    private let measureText: (String, CGFloat) -> MarkdownTableText.Measure

    init(render: @escaping (MarkdownContent) -> String = { $0.renderMarkdown() },
         measure: @escaping (String, CGFloat) -> MarkdownTableText.Measure = { MarkdownTableText.measure($0, fontSize: $1) }) {
        self.render = render
        self.measureText = measure
    }

    func measure(_ content: MarkdownContent, fontSize: CGFloat) -> MarkdownTableText.Measure {
        if self.content != content {
            markdown = render(content)
            self.content = content
            measured = nil
        }
        if self.fontSize == fontSize, let measured { return measured }
        let result = measureText(markdown, fontSize)
        self.fontSize = fontSize
        measured = result
        return result
    }
}

extension EnvironmentValues {
    /// The text width of each column of the table being drawn (`TablePlan`); empty until measured.
    @Entry var tableColumnWidths: [CGFloat] = []
}

/// The reply theme's table: fitted to the width it gets (`TablePlan`); when it still has to scroll, its right edge fades
/// while there is more to see (accessibility id `table-more`).
struct FittedTable: View {
    let config: BlockConfiguration
    @ScaledMetric(relativeTo: .body) private var fontSize: CGFloat = Theme.Level.body.size * 0.88 * Theme.Font.scale
    @State private var available: CGFloat = 0
    @State private var atEnd = false
    @State private var preparation = MarkdownTablePreparation()

    static let cellPadding: CGFloat = 16
    static let border: CGFloat = 1

    /// Its rules and rows' backgrounds (also the picture of it in a message being selected, `ReplyTable`).
    static func drawn(_ label: some View) -> some View {
        label
            .markdownTableBorderStyle(.init(color: Color.primary.opacity(0.14)))
            .markdownTableBackgroundStyle(.alternatingRows(Color.clear, Color.primary.opacity(0.035), header: Color.primary.opacity(0.07)))
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("FittedTable")
        #endif
        let measure = preparation.measure(config.content, fontSize: fontSize)
        let n = CGFloat(measure.natural.count)
        let plan = available > 0
            ? TablePlan.fit(natural: measure.natural, minimum: measure.minimum,
                            available: available - n * (Self.cellPadding + Self.border) - Self.border * 3)
            : nil
        ScrollView(.horizontal, showsIndicators: false) {
            Self.drawn(config.label).environment(\.tableColumnWidths, plan?.widths ?? [])
        }
        .scrollDisabled(plan?.scrolls != true)
        // What shows of it, not offset + container: on an iPad the chat's scroll views run on under the sidebar, inset
        // by it, and the container leaves that out, so the fade stayed at the end (#5737).
        .onScrollGeometryChange(for: Bool.self) { g in g.visibleRect.maxX >= g.contentSize.width - 4 } action: { _, end in
            atEnd = end
        }
        .mask {
            HStack(spacing: 0) {
                Color.black
                if plan?.scrolls == true && !atEnd {
                    LinearGradient(colors: [.black, .black.opacity(0.15)], startPoint: .leading, endPoint: .trailing).scaledFrame(width: Theme.Size.wellL)
                }
            }
        }
        .overlay(alignment: .trailing) {
            if plan?.scrolls == true && !atEnd {
                Color.clear // the fade says it; this says it to VoiceOver (a chevron here sat on the last column's words)
                    .scaledFrame(width: Theme.Size.wellL)
                    .allowsHitTesting(false)
                    .accessibilityElement()
                    .accessibilityLabel("Table scrolls horizontally")
                    .accessibilityIdentifier("table-more")
            }
        }
        .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { available = $0 }
        .markdownMargin(top: 4, bottom: 12)
    }
}

/// A cell of `FittedTable`: as wide as its column's plan, wrapping inside it.
struct FittedTableCell: View {
    @Environment(\.tableColumnWidths) private var widths
    let config: TableCellConfiguration

    var body: some View {
        let label = config.label
            .markdownTextStyle {
                FontSize(.em(0.88))
                if config.row == 0 { FontWeight(.semibold) }
            }
            .relativeLineSpacing(.em(0.15))
        Group {
            if config.column < widths.count {
                label.frame(width: widths[config.column], alignment: .leading)
            } else {
                MaxWidthLayout(max: 280) { label } // before the table knows its width
            }
        }
        .padding(.horizontal, FittedTable.cellPadding / 2)
        .padding(.vertical, Theme.Space.sm)
    }
}
