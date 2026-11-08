import PersonaKit
import SwiftUI

/// What the table viewer knows how to open (docs/apple/ios.md「表格」): CSV / TSV as text, xlsx as a workbook. Old .xls and
/// Numbers stay with QuickLook.
nonisolated enum TableFileType {
    static let delimited: Set<String> = ["csv", "tsv", "tab"]
    static let workbooks: Set<String> = ["xlsx", "xlsm"]

    static func isDelimited(_ name: String) -> Bool { delimited.contains((name as NSString).pathExtension.lowercased()) }
    static func isWorkbook(_ name: String) -> Bool { workbooks.contains((name as NSString).pathExtension.lowercased()) }
}

/// A table file read into sheets: CSV / TSV a page at a time (the file viewer's paging, the next page as the end
/// comes near), xlsx downloaded whole and parsed off the main thread.
@MainActor @Observable
final class TableFileModel {
    let ref: FileRef
    let file: FileContent
    @ObservationIgnored private let api: APIClient?
    private(set) var sheets: [TableSheet] = []
    var sheet = 0
    private(set) var error: String?
    /// Where the next page of a CSV starts; 0: read to the end.
    private(set) var next: Int64 = 0
    private(set) var loadingMore = false
    /// Per sheet and column, the widest cell of the first rows below the header (by character cells, CJK counting
    /// two): the view measures it, and the header, in their fonts.
    private(set) var widest: [[String]] = []
    @ObservationIgnored private var parser: DelimitedParser?
    @ObservationIgnored private var started = false

    /// Rows looked at to size the columns.
    static let sampleRows = 300

    init(ref: FileRef, file: FileContent, api: APIClient?) {
        self.ref = ref
        self.file = file
        self.api = api
    }

    var current: TableSheet? { sheet < sheets.count ? sheets[sheet] : nil }

    func start() async {
        guard !started else { return }
        started = true
        if let text = file.text {
            let firstLine = String(text.prefix(4096).split(separator: "\n", maxSplits: 1).first ?? "")
            var p = DelimitedParser(separator: DelimitedParser.separator(name: file.name, firstLine: firstLine))
            let final = (file.next ?? 0) == 0
            let rows = await Task.detached(priority: .userInitiated) { (p.feed(text, final: final), p) }.value
            parser = rows.1
            next = file.next ?? 0
            var s = TableSheet(name: (file.name as NSString).deletingPathExtension)
            s.append(rows.0)
            sheets = [s]
            measure()
            return
        }
        guard let api else {
            error = String(localized: "Not connected to the hub [App Model 93d955]")
            return
        }
        do {
            let bytes = try await api.fileBytes(machine: ref.machineID, path: file.path, cwd: ref.cwd)
            let read = await Task.detached(priority: .userInitiated) { Result { try XLSXReader.read(bytes) } }.value
            sheets = try read.get()
            measure()
        } catch {
            self.error = error.shown
        }
    }

    /// The next page of a CSV, its rows appended.
    func loadMore() async {
        guard next > 0, !loadingMore, let api, var p = parser else { return }
        loadingMore = true
        defer { loadingMore = false }
        guard let page = try? await api.file(machine: ref.machineID, path: file.path, cwd: ref.cwd, offset: next), let text = page.text else { return }
        let final = (page.next ?? 0) == 0
        let rows = await Task.detached(priority: .userInitiated) { (p.feed(text, final: final), p) }.value
        parser = rows.1
        next = page.next ?? 0
        sheets[0].append(rows.0)
        if widest.first?.count ?? 0 < sheets[0].columns { measure() }
    }

    private func measure() {
        widest = sheets.map { s in
            var cells = Array(repeating: 0, count: s.columns)
            var text = Array(repeating: "", count: s.columns)
            for row in s.rows.dropFirst().prefix(Self.sampleRows) {
                for (c, cell) in row.enumerated() where c < cells.count {
                    let n = Self.cells(cell)
                    if n > cells[c] { cells[c] = n; text[c] = String(cell.prefix(80)) }
                }
            }
            return text
        }
    }

    /// How wide text is in character cells: two for CJK and other wide characters, one for the rest.
    nonisolated static func cells(_ text: String) -> Int {
        var n = 0
        for u in text.unicodeScalars.prefix(80) { n += u.properties.isEmojiPresentation || (u.value >= 0x1100 && Self.wide(u.value)) ? 2 : 1 }
        return n
    }

    nonisolated private static func wide(_ v: UInt32) -> Bool {
        (0x1100...0x115F).contains(v) || (0x2E80...0xA4CF).contains(v) || (0xAC00...0xD7A3).contains(v) ||
            (0xF900...0xFAFF).contains(v) || (0xFE30...0xFE4F).contains(v) || (0xFF00...0xFF60).contains(v) || (0xFFE0...0xFFE6).contains(v)
    }

    /// The tab-separated row, for Copy Row (pastes into a spreadsheet as cells).
    func rowText(_ row: Int) -> String { (current?.rows[safe: row] ?? []).joined(separator: "\t") }
}

#if os(iOS) || os(visionOS)
private typealias PlatformFont = UIFont
#else
private typealias PlatformFont = NSFont
#endif

private extension Array {
    subscript(safe i: Int) -> Element? { indices.contains(i) ? self[i] : nil }
}

/// The table viewer: the first row frozen at the top, the row numbers frozen at the left, only the columns on screen
/// drawn; columns sized to their first rows and draggable at the header's edge; a tapped cell shows whole underneath.
struct TableFileView: View {
    let model: TableFileModel
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var scrollX: CGFloat = 0
    @State private var viewport: CGFloat = 0
    @State private var selected: Cell?
    /// Widths the user dragged, by sheet and column (points at the default text size).
    @State private var custom: [Int: [Int: CGFloat]] = [:]
    @State private var dragBase: CGFloat?

    struct Cell: Hashable {
        var row: Int
        var column: Int
    }

    var body: some View {
        VStack(spacing: 0) {
            if model.sheets.count > 1 { sheetBar }
            if let sheet = model.current {
                if sheet.rows.isEmpty {
                    EmptyStateView(symbol: Theme.Icon.sheet, title: String(localized: "Empty Sheet"), message: String(localized: "This sheet is empty."))
                } else {
                    grid(sheet)
                    footer(sheet)
                }
            } else if let error = model.error {
                EmptyStateView(symbol: Theme.Icon.error, title: String(localized: "Can't Open This Spreadsheet"), message: error)
            } else {
                ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .task(id: ObjectIdentifier(model)) { await model.start() }
        .onChange(of: model.sheet) { selected = nil }
    }

    // MARK: Sizes

    private var scale: CGFloat { Theme.sizeScale(typeSize) }
    private var rowHeight: CGFloat { Theme.Size.wellS * scale }

    /// How wide `text` is in the table's font (`Theme.Font.secondary`, digits all one width), or its header's.
    private func measured(_ text: String, header: Bool = false) -> CGFloat {
        let weight: PlatformFont.Weight = header ? .semibold : .regular
        #if os(iOS) || os(visionOS)
        let points = UIFont.preferredFont(forTextStyle: .subheadline,
                                          compatibleWith: UITraitCollection(preferredContentSizeCategory: UIContentSizeCategory(typeSize))).pointSize
        #else
        let points = Theme.Font.pointSize(.secondary)
        #endif
        let font = PlatformFont.monospacedDigitSystemFont(ofSize: points, weight: weight)
        return ceil((text as NSString).size(withAttributes: [.font: font]).width)
    }

    private func widths(_ sheet: TableSheet) -> [CGFloat] {
        let widest = model.sheet < model.widest.count ? model.widest[model.sheet] : []
        let pad = Theme.Space.s * 2 + Theme.Space.xs
        return (0..<sheet.columns).map { c in
            if let w = custom[model.sheet]?[c] { return w * scale }
            let fitted = max(measured(c < widest.count ? widest[c] : ""), measured(sheet[0, c], header: true)) + pad
            return min(max(fitted, Theme.Size.box56 * scale), Theme.Size.box240 * scale)
        }
    }

    private func numberWidth(_ sheet: TableSheet) -> CGFloat {
        measured(String(sheet.rows.count)) + Theme.Space.s * 2
    }

    // MARK: Grid

    private func grid(_ sheet: TableSheet) -> some View {
        let widths = widths(sheet)
        var xs = [CGFloat](repeating: 0, count: widths.count + 1)
        for (i, w) in widths.enumerated() { xs[i + 1] = xs[i] + w }
        let lead = numberWidth(sheet)
        let total = lead + (xs.last ?? 0)
        // The columns on screen, and one either side.
        let left = scrollX - lead, right = scrollX + max(viewport, Theme.Size.box380)
        let first = max(0, (xs.firstIndex { $0 > left } ?? 1) - 2)
        let last = min(widths.count - 1, xs.firstIndex { $0 > right } ?? widths.count - 1)
        let columns = widths.isEmpty || first > last ? [] : Array(first...last)
        return ScrollView([.horizontal, .vertical]) {
            LazyVStack(alignment: .leading, spacing: 0, pinnedViews: [.sectionHeaders]) {
                Section {
                    ForEach(1..<max(1, sheet.rows.count), id: \.self) { r in
                        row(r, sheet: sheet, columns: columns, widths: widths, xs: xs, lead: lead, total: total)
                            .onAppear { if r >= sheet.rows.count - 60 { Task { await model.loadMore() } } }
                    }
                } header: {
                    row(0, sheet: sheet, columns: columns, widths: widths, xs: xs, lead: lead, total: total)
                }
            }
            .frame(width: total, alignment: .leading)
        }
        .accessibilityIdentifier("file-table")
        .onScrollGeometryChange(for: CGFloat.self, of: { $0.contentOffset.x + $0.contentInsets.leading }) { _, x in scrollX = max(0, x) }
        .onScrollGeometryChange(for: CGFloat.self, of: { $0.containerSize.width }) { _, w in viewport = w }
        .background(Theme.systemPage)
    }

    private func row(_ r: Int, sheet: TableSheet, columns: [Int], widths: [CGFloat], xs: [CGFloat], lead: CGFloat, total: CGFloat) -> some View {
        let header = r == 0
        return ZStack(alignment: .topLeading) {
            HStack(spacing: .zero) {
                Color.clear.frame(width: lead + (columns.first.map { xs[$0] } ?? 0))
                ForEach(columns, id: \.self) { c in
                    cell(Cell(row: r, column: c), text: sheet[r, c], width: widths[c], header: header)
                }
            }
            Text(String(r + 1))
                .font(Theme.Font.label)
                .monospacedDigit()
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .padding(.trailing, Theme.Space.s)
                .frame(width: lead, height: rowHeight, alignment: .trailing)
                .background(Theme.systemCard)
                .overlay(alignment: .trailing) { Rectangle().fill(Theme.Fill.pressed).frame(width: Theme.Size.hairline) }
                .offset(x: scrollX)
        }
        .frame(width: total, height: rowHeight, alignment: .leading)
        .background(header ? Theme.systemCard : Theme.systemPage)
        .overlay(alignment: .bottom) { Rectangle().fill(header ? Theme.Fill.pressed : Theme.Fill.chip).frame(height: Theme.Size.hairline) }
    }

    private func cell(_ at: Cell, text: String, width: CGFloat, header: Bool) -> some View {
        let number = !header && !text.isEmpty && Double(text.replacingOccurrences(of: ",", with: "")) != nil
        return Text(text)
            .font(header ? Theme.Font.secondaryStrong : Theme.Font.secondary)
            .monospacedDigit()
            .lineLimit(1)
            .truncationMode(.tail)
            .padding(.horizontal, Theme.Space.s)
            .frame(width: width, height: rowHeight, alignment: number ? .trailing : .leading)
            .overlay(alignment: .trailing) {
                ZStack(alignment: .trailing) {
                    Rectangle().fill(Theme.Fill.chip).frame(width: Theme.Size.hairline)
                    if header { resizeHandle(at.column, width: width) }
                }
            }
            .overlay {
                if selected == at {
                    Rectangle().strokeBorder(Theme.accent, lineWidth: Theme.Size.rule)
                }
            }
            .contentShape(.rect)
            .onTapGesture { selected = selected == at ? nil : at }
            .contextMenu {
                Button { UIPasteboard.general.string = text } label: { Label("Copy Cell", systemImage: Theme.Icon.copy) }
                Button { UIPasteboard.general.string = model.rowText(at.row) } label: { Label("Copy Row", systemImage: Theme.Icon.copy) }
            }
            .accessibilityLabel("\(TableSheet.columnName(at.column))\(at.row + 1) \(text)")
    }

    /// The header cell's right edge: drag to make the column wider or narrower.
    private func resizeHandle(_ column: Int, width: CGFloat) -> some View {
        Color.clear
            .frame(width: Theme.Space.m)
            .contentShape(.rect)
            #if os(macOS)
            .onHover { inside in if inside { NSCursor.resizeLeftRight.push() } else { NSCursor.pop() } }
            #endif
            .gesture(DragGesture(minimumDistance: Theme.Space.xxs).onChanged { g in
                let base = dragBase ?? width / scale
                dragBase = base
                custom[model.sheet, default: [:]][column] = min(max(base + g.translation.width / scale, Theme.Size.box50), Theme.Size.readable)
            }.onEnded { _ in dragBase = nil })
            .accessibilityHidden(true)
    }

    // MARK: Bars

    private var sheetBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: Theme.Space.s) {
                ForEach(Array(model.sheets.enumerated()), id: \.offset) { i, s in
                    Button {
                        Haptics.tap()
                        model.sheet = i
                    } label: {
                        Chip(text: s.name, look: i == model.sheet ? .tint(Theme.accent) : .neutral)
                    }
                    .buttonStyle(.plain)
                    .accessibilityAddTraits(i == model.sheet ? .isSelected : [])
                }
            }
            .padding(.horizontal, Theme.Space.page)
            .padding(.vertical, Theme.Space.s)
        }
    }

    private func footer(_ sheet: TableSheet) -> some View {
        HStack(spacing: Theme.Space.s) {
            if let at = selected {
                Text(TableSheet.columnName(at.column) + String(at.row + 1))
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Text(sheet[at.row, at.column].isEmpty ? " " : sheet[at.row, at.column])
                    .font(Theme.Font.secondary)
                    .lineLimit(3)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Button { UIPasteboard.general.string = sheet[at.row, at.column] } label: {
                    ThemedIcon(Theme.Icon.copy).font(Theme.Font.caption)
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
                .accessibilityLabel("Copy Cell")
            } else {
                Text(summary(sheet))
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.s)
        .background(Theme.systemCard)
    }

    private func summary(_ sheet: TableSheet) -> String {
        var parts = [String(localized: "\(sheet.rows.count) × \(sheet.columns) table")]
        if model.next > 0 {
            parts.append(String(localized: "Read \(model.next.storageText) of \(model.file.size.storageText); scroll down to read more"))
        }
        if sheet.capped { parts.append(String(localized: "Showing only the first \(TableSheet.maxRows) rows")) }
        return parts.joined(separator: " · ")
    }
}
