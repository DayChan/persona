import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

// Native rendering of `documents/read?format=json` (ProseMirror-style nodes, see the Lark gateway's
// src/server/lark/domains/documents-content.ts for what each docx block becomes). Used when the owner forbids
// export (no Markdown), and for mind notes / slides that arrive as trees. Top-level blocks are separate lazy rows so
// a 400-block document scrolls smoothly and the outline can jump to any heading.

// MARK: - Colors

nonisolated enum FeishuDocsColor {
    /// `#rgb`, `#rrggbb[aa]`, `rgb(r, g, b)` or `rgba(r, g, b, a)` → components 0…1.
    static func parse(_ s: String?) -> (r: Double, g: Double, b: Double, a: Double)? {
        guard var t = s?.trimmingCharacters(in: .whitespaces).lowercased(), !t.isEmpty else { return nil }
        if t.hasPrefix("#") {
            t.removeFirst()
            if t.count == 3 || t.count == 4 { t = t.map { "\($0)\($0)" }.joined() }
            guard t.count == 6 || t.count == 8, let v = UInt64(t, radix: 16) else { return nil }
            let rgba = t.count == 6 ? (v << 8 | 0xff) : v
            return (Double((rgba >> 24) & 0xff) / 255, Double((rgba >> 16) & 0xff) / 255, Double((rgba >> 8) & 0xff) / 255, Double(rgba & 0xff) / 255)
        }
        guard t.hasPrefix("rgb"), let open = t.firstIndex(of: "("), let close = t.lastIndex(of: ")") else { return nil }
        let nums = t[t.index(after: open)..<close].split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }
        guard nums.count >= 3 else { return nil }
        func channel(_ s: String) -> Double? {
            s.hasSuffix("%") ? Double(s.dropLast()).map { $0 / 100 } : Double(s).map { $0 / 255 }
        }
        guard let r = channel(nums[0]), let g = channel(nums[1]), let b = channel(nums[2]) else { return nil }
        let a = nums.count > 3 ? (nums[3].hasSuffix("%") ? Double(nums[3].dropLast()).map { $0 / 100 } : Double(nums[3])) ?? 1 : 1
        return (min(1, r), min(1, g), min(1, b), min(1, max(0, a)))
    }

    static func luminance(_ c: (r: Double, g: Double, b: Double, a: Double)) -> Double { 0.2126 * c.r + 0.7152 * c.g + 0.0722 * c.b }

    /// A text color that stays readable: authors' near-black text becomes the primary color (white in dark mode).
    static func text(_ s: String?) -> Color? {
        guard let c = parse(s) else { return nil }
        if luminance(c) < 0.25 && abs(c.r - c.g) < 0.08 && abs(c.g - c.b) < 0.08 { return nil }
        let ui = UIColor(red: c.r, green: c.g, blue: c.b, alpha: c.a)
        return Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? ui.withAlphaComponent(c.a).lighter : ui })
    }

    /// A surface tint: the author's pastel in light mode, the same hue dimmed in dark mode.
    static func background(_ s: String?) -> Color? {
        guard let c = parse(s), c.a > 0.01 else { return nil }
        let ui = UIColor(red: c.r, green: c.g, blue: c.b, alpha: c.a)
        return Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? ui.withAlphaComponent(min(c.a, 1) * 0.28) : ui })
    }
}

#if os(iOS) || os(visionOS)
private extension UIColor {
    var lighter: UIColor {
        var h: CGFloat = 0, s: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        guard getHue(&h, saturation: &s, brightness: &b, alpha: &a) else { return self }
        return UIColor(hue: h, saturation: s * 0.85, brightness: max(b, 0.82), alpha: a)
    }
}
#else
private extension NSColor {
    /// NSColor's getHue returns nothing and needs an RGB color space first.
    nonisolated var lighter: NSColor {
        guard let rgb = usingColorSpace(.sRGB) else { return self }
        var h: CGFloat = 0, s: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
        rgb.getHue(&h, saturation: &s, brightness: &b, alpha: &a)
        return NSColor(hue: h, saturation: s * 0.85, brightness: max(b, 0.82), alpha: a)
    }
}
#endif

// MARK: - Inline

/// Scheme of the links a user mention carries: `feishu-user://<id>?name=…` (the reader opens the profile card).
nonisolated let feishuDocsUserScheme = "feishu-user"

nonisolated enum FeishuDocsInline {
    /// Text runs, mentions, equations and line breaks of one block as an attributed string (marks become attributes).
    static func attributed(_ nodes: [FeishuDocsNode], monospaced: Bool = false) -> AttributedString {
        var out = AttributedString()
        for n in nodes { out += piece(n, monospaced: monospaced) }
        return out
    }

    static func piece(_ n: FeishuDocsNode, monospaced: Bool) -> AttributedString {
        switch n.type {
        case "text":
            var s = AttributedString(n.text ?? "")
            if monospaced { s.font = Theme.Font.body.monospaced() }
            apply(n.marks ?? [], to: &s)
            return s
        case "hardBreak":
            return AttributedString("\n")
        case "mention":
            let kind = n.attr("kind") ?? ""
            let label = n.attr("label") ?? ""
            var s: AttributedString
            switch kind {
            case "user":
                s = AttributedString("@" + label)
                if let id = n.attr("userId"), var c = URLComponents(string: "\(feishuDocsUserScheme)://\(id)") {
                    c.queryItems = [URLQueryItem(name: "name", value: label)]
                    s.link = c.url
                }
                s.foregroundColor = LarkColor.blue
            case "reminder":
                let d = JSON.parseDate(label)
                s = AttributedString("⏰ " + (d.map { n.flag("wholeDay") ? $0.formatted(.dateTime.month().day()) : $0.formatted(.dateTime.month().day().hour().minute()) } ?? label))
                s.foregroundColor = LarkColor.hue("orange")
            default:
                // doc / preview / button: a link titled by the target.
                s = AttributedString((kind == "doc" ? "📄 " : "") + label)
                if let href = n.attr("href") { s.link = URL(string: href) }
                s.foregroundColor = LarkColor.blue
            }
            apply(n.marks ?? [], to: &s)
            return s
        case "equation":
            var s = AttributedString(n.attr("latex") ?? "")
            s.font = .system(.body, design: .serif).italic() // type-scale: serif (a quoted passage, the body size)
            return s
        case "statusTag":
            var s = AttributedString(" \(n.attr("text") ?? "") ")
            s.foregroundColor = LarkColor.hue(n.attr("color"))
            s.backgroundColor = LarkColor.hue(n.attr("color")).opacity(0.15)
            s.font = .footnote.weight(.medium)
            return s
        default:
            return attributed(n.children, monospaced: monospaced)
        }
    }

    static func apply(_ marks: [FeishuDocsMark], to s: inout AttributedString) {
        var bold = false, italic = false, code = false
        for m in marks {
            switch m.type {
            case "bold": bold = true
            case "italic": italic = true
            case "underline": s.underlineStyle = .single
            case "strike": s.strikethroughStyle = .single
            case "code": code = true
            case "link":
                if let href = m.attrs?["href"]?.string, let u = URL(string: href) {
                    s.link = u
                    s.foregroundColor = LarkColor.blue
                }
            case "textStyle":
                if let c = FeishuDocsColor.text(m.attrs?["color"]?.string) { s.foregroundColor = c }
                if let b = FeishuDocsColor.background(m.attrs?["backgroundColor"]?.string) { s.backgroundColor = b }
            default: break
            }
        }
        if code {
            s.font = Theme.Font.code
            if s.backgroundColor == nil { s.backgroundColor = Theme.codeBackground }
        } else if bold || italic {
            var f = Theme.Font.body
            if bold { f = f.weight(.semibold) }
            if italic { f = f.italic() }
            s.font = f
        }
    }
}

// MARK: - Blocks

/// What the blocks around need to know: the gallery's images, whether text may be copied.
struct FeishuDocsRenderContext {
    var images: [String] = []
    var selectable = true
    var openImage: (String) -> Void = { _ in }
}

extension EnvironmentValues {
    @Entry var feishuDocsContext = FeishuDocsRenderContext()
}

/// A stack of blocks (the children of a quote, callout, column, list item, table cell…).
struct FeishuDocsBlocks: View {
    var nodes: [FeishuDocsNode]
    var spacing: CGFloat = 8

    var body: some View {
        let numbers = FeishuDocsTree.orderedNumbers(nodes)
        VStack(alignment: .leading, spacing: spacing) {
            ForEach(Array(nodes.enumerated()), id: \.offset) { i, n in
                FeishuDocsBlock(node: n, number: numbers[i])
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct FeishuDocsBlock: View {
    var node: FeishuDocsNode
    /// The number an ordered list item shows (counted across sibling one-item lists).
    var number: Int?
    @Environment(\.feishuDocsContext) private var context

    var body: some View {
        switch node.type {
        case "paragraph":
            paragraph
        case "heading":
            heading
        case "bulletList", "orderedList", "taskList":
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                ForEach(Array(node.children.enumerated()), id: \.offset) { i, item in
                    FeishuDocsListItem(item: item, marker: node.type == "orderedList" ? "\((number ?? 1) + i)." : nil)
                }
            }
        case "listItem":
            FeishuDocsListItem(item: node, marker: nil)
        case "blockquote":
            FeishuDocsBlocks(nodes: node.children, spacing: 6)
                .padding(.leading, Theme.Space.m)
                .overlay(alignment: .leading) { Capsule().fill(Color.secondary.opacity(0.35)).frame(width: Theme.Size.bar) }
                .foregroundStyle(.secondary)
        case "callout":
            callout
        case "columns":
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                ForEach(Array(node.children.enumerated()), id: \.offset) { _, col in FeishuDocsBlocks(nodes: col.children) }
            }
        case "column", "synced", "agenda", "agendaContent", "doc", "mindnote", "slideNotes":
            FeishuDocsBlocks(nodes: node.children)
        case "agendaItem":
            FeishuDocsBlocks(nodes: node.children)
                .padding(Theme.Space.m)
                .background(Color.primary.opacity(0.035), in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        case "agendaTitle":
            inline(node.children).font(Theme.Font.heading)
        case "horizontalRule":
            Divider().padding(.vertical, Theme.Space.sm)
        case "codeBlock":
            FeishuDocsCodeBlock(node: node)
        case "table":
            FeishuDocsTable(table: node)
        case "image":
            FeishuDocsImage(node: node)
        case "missingImage":
            placeholder("photo", node.attr("caption").flatMap { $0.isEmpty ? nil : $0 } ?? String(localized: "Image can't be shown"))
        case "bookmark":
            card(symbol: Theme.Icon.link, hue: "blue", title: node.attr("title") ?? node.attr("href") ?? "", subtitle: node.attr("summary"), href: node.attr("href"))
        case "embedLink":
            embed
        case "file":
            FeishuDocsFileBlock(node: node)
        case "docTask":
            docTask
        case "mermaid", "plantuml":
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(node.type == "mermaid" ? String(localized: "Mermaid diagram") : String(localized: "PlantUML diagram")).font(Theme.Font.label).foregroundStyle(.secondary)
                FeishuDocsCodeBlock(node: node)
            }
        case "mindnoteItem":
            FeishuDocsListItem(item: node, marker: nil)
        case "mindnoteLabel":
            inline(node.children)
        case "slide":
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text("Page \(Int(node.number("index") ?? 0))").font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                FeishuDocsBlocks(nodes: node.children)
            }
            .padding(Theme.Space.l)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Color.primary.opacity(0.035), in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        case "toc", "readReceipt", "widgetEmpty":
            EmptyView()
        case "unsupported":
            placeholder("square.dashed", node.attr("kind") == "isv" ? String(localized: "Third-party widget. View it in Lark.") : String(localized: "This block isn't supported yet. View it in Lark."),
                        href: node.attr("sourceUrl"))
        default:
            if node.children.isEmpty {
                placeholder("square.dashed", String(localized: "This block isn't supported yet. View it in Lark."), href: node.attr("sourceUrl") ?? node.attr("href"))
            } else if node.children.allSatisfy({ ["text", "mention", "hardBreak", "equation"].contains($0.type) }) {
                inline(node.children)
            } else {
                FeishuDocsBlocks(nodes: node.children)
            }
        }
    }

    // MARK: Text

    private func inline(_ nodes: [FeishuDocsNode]) -> some View {
        FeishuDocsText(text: FeishuDocsInline.attributed(nodes), blockID: node.attrs?["id"]?.string)
    }

    @ViewBuilder private var paragraph: some View {
        if node.children.isEmpty {
            Color.clear.frame(height: Theme.Size.barWide)
        } else {
            inline(node.children)
                .frame(maxWidth: .infinity, alignment: alignment)
                .multilineTextAlignment(textAlignment)
                .padding(.horizontal, background == nil ? 0 : 6)
                .background(background ?? .clear, in: .rect(cornerRadius: 4))
        }
    }

    private var heading: some View {
        let level = Int(node.number("level") ?? 1)
        // Persona' levels: the first heading is a title, the rest headings, told apart by the space above them.
        let font: Font = level == 1 ? Theme.Font.title : Theme.Font.heading
        return FeishuDocsText(text: FeishuDocsInline.attributed(node.children), blockID: node.attrs?["id"]?.string)
            .font(font)
            .frame(maxWidth: .infinity, alignment: alignment)
            .padding(.top, level <= 2 ? 10 : 4)
            .accessibilityAddTraits(.isHeader)
    }

    private var alignment: Alignment {
        switch node.attr("align") {
        case "center": .center
        case "right": .trailing
        default: .leading
        }
    }

    private var textAlignment: TextAlignment {
        switch node.attr("align") {
        case "center": .center
        case "right": .trailing
        default: .leading
        }
    }

    private var background: Color? { FeishuDocsColor.background(node.attr("backgroundColor")) }

    // MARK: Containers

    private var callout: some View {
        let tint = FeishuDocsColor.background(node.attr("backgroundColor")) ?? LarkColor.hue("blue").opacity(0.08)
        let stroke = FeishuDocsColor.background(node.attr("borderColor")) ?? .clear
        return HStack(alignment: .top, spacing: Theme.Space.m) {
            if let e = node.attr("emoji"), !e.isEmpty { Text(e).font(Theme.Font.title) }
            FeishuDocsBlocks(nodes: node.children, spacing: 6)
                .foregroundStyle(FeishuDocsColor.text(node.attr("color")) ?? .primary)
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(tint, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(stroke, lineWidth: 1))
    }

    private var docTask: some View {
        let done = node.flag("checked")
        let assignees = node.attr("assignees").flatMap { try? JSON.decoder.decode([FeishuDocsPerson].self, from: Data($0.utf8)) } ?? []
        return HStack(alignment: .top, spacing: Theme.Space.m) {
            Image(systemName: done ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                .foregroundStyle(done ? LarkColor.hue("green") : .secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                Text(node.attr("summary").flatMap { $0.isEmpty ? nil : $0 } ?? node.plainText)
                    .strikethrough(done)
                    .foregroundStyle(done ? .secondary : .primary)
                if !assignees.isEmpty || node.attr("due") != nil {
                    HStack(spacing: Theme.Space.sm) {
                        ForEach(Array(assignees.prefix(3).enumerated()), id: \.offset) { _, p in
                            LarkAvatarImage(name: p.name, url: p.avatarUrl, size: 16)
                        }
                        if let d = node.attr("due").flatMap(JSON.parseDate) {
                            Text("Due \(d.formatted(.dateTime.month().day()))").font(Theme.Font.label).foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.primary.opacity(0.035), in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }

    @ViewBuilder private var embed: some View {
        let kind = node.attr("kind") ?? ""
        let (symbol, hue, label): (String, String, String) = switch kind {
        case "sheet": (Theme.Icon.fileSheet, "green", String(localized: "Sheet [Feishu Doc Renderer 4ec5b2]"))
        case "bitable", "base": (Theme.Icon.baseFilled, "purple", String(localized: "Base"))
        case "mindnote": (Theme.Icon.connectors, "wathet", String(localized: "Mindnote"))
        case "chat": ("bubble.left.and.bubble.right.fill", "blue", String(localized: "Group chat"))
        case "calendarEvent": ("calendar", "orange", String(localized: "Event [Feishu Doc Renderer a14f23]"))
        default: ("link", "blue", String(localized: "Link [Feishu Doc Renderer 305378]"))
        }
        let title = node.attr("title").flatMap { $0.isEmpty ? nil : $0 } ?? label
        let href = node.attr("href").flatMap { $0.hasPrefix("http") ? $0 : nil }
        card(symbol: symbol, hue: hue, title: title, subtitle: title == label ? nil : label, href: href)
    }

    private func card(symbol: String, hue: String, title: String, subtitle: String?, href: String?) -> some View {
        FeishuDocsLinkCard(symbol: symbol, hue: hue, title: title, subtitle: subtitle, href: href)
    }

    private func placeholder(_ symbol: String, _ text: String, href: String? = nil) -> some View {
        FeishuDocsLinkCard(symbol: symbol, hue: "grey", title: text, subtitle: nil, href: href, quiet: true)
    }
}

/// Selectable text when the document allows copying. Inside the chat's link sheet (`docPick` set) a long press
/// offers Edit with AI / Add as Context for the paragraph instead (SwiftUI's selection menu takes no custom items); where
/// comments can be written (`feishuDocComment`), Comment on This (`FeishuDocsCommentable`, #1290). `blockID`: the block the
/// text is in (a comment on it is anchored there).
struct FeishuDocsText: View {
    var text: AttributedString
    var blockID: String? = nil
    @Environment(\.feishuDocsContext) private var context
    @Environment(\.docPick) private var docPick
    @Environment(\.feishuDocComment) private var comment
    @State private var selecting = false

    var body: some View {
        let t = Text(text).lineSpacing(3).fixedSize(horizontal: false, vertical: true)
        if let docPick, context.selectable {
            let plain = String(text.characters)
            t.contextMenu { DocPickMenu(text: plain, pick: docPick, selecting: $selecting) }
                .sheet(isPresented: $selecting) { PickTextSheet(text: plain, pick: docPick) }
        } else if let comment, !text.characters.isEmpty {
            t.modifier(FeishuDocsCommentable(text: String(text.characters), blockID: blockID, copyable: context.selectable, comment: comment))
        } else if context.selectable {
            t.textSelection(.enabled)
        } else {
            t
        }
    }
}

struct FeishuDocsListItem: View {
    var item: FeishuDocsNode
    var marker: String?

    var body: some View {
        let todo = item.flag("todo")
        let done = item.flag("checked")
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            Group {
                if todo {
                    Image(systemName: done ? "checkmark.square.fill" : Theme.Icon.uncheckedBox)
                        .foregroundStyle(done ? LarkColor.blue : .secondary)
                } else if let marker {
                    Text(marker).monospacedDigit().foregroundStyle(.secondary)
                } else {
                    Text("•").foregroundStyle(.secondary)
                }
            }
            .scaledFrame(minWidth: Theme.Size.markM, alignment: .trailing)
            FeishuDocsBlocks(nodes: item.children, spacing: 4)
                .opacity(todo && done ? 0.55 : 1)
        }
    }
}

struct FeishuDocsCodeBlock: View {
    var node: FeishuDocsNode

    var body: some View {
        let text = node.plainText
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            if let lang = node.attr("language"), !lang.isEmpty, lang.lowercased() != "plaintext" {
                Text(lang).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
            }
            ScrollView(.horizontal, showsIndicators: false) {
                Text(text).font(Theme.Font.code).fixedSize().textSelection(.enabled)
            }
        }
        .padding(Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
    }
}

/// A docx table: merged cells span, columns sized to the screen (see `FeishuDocsTableSizing`).
struct FeishuDocsTable: View {
    var table: FeishuDocsNode

    var body: some View {
        let layout = FeishuDocsTableLayout(table)
        let rows = table.children.filter { $0.type == "tableRow" }
        let node = { (r: Int, slot: FeishuDocsTableLayout.Slot) in slot.cell.map { rows[r].children[$0] } }
        FeishuDocsTableGrid(layout: layout, natural: layout.naturalWidths { r, slot in node(r, slot)?.plainText ?? "" }) { r, slot in
            if let n = node(r, slot) {
                FeishuDocsBlocks(nodes: n.children, spacing: 4)
                    .font(slot.header ? Theme.Font.secondaryStrong : Theme.Font.secondary)
            }
        } background: { r, slot in
            node(r, slot).flatMap { FeishuDocsColor.background($0.attr("backgroundColor")) }
        }
    }
}

/// A GFM table from the Markdown export, drawn like the docx tables (MarkdownUI's tables neither wrap nor fit).
struct FeishuDocsMarkdownTableView: View {
    var table: FeishuDocsMarkdownTable
    @Environment(\.feishuDocsContext) private var context

    var body: some View {
        let grid = table.grid
        let layout = FeishuDocsTableLayout(rows: grid.count, columns: table.columns, header: table.header != nil)
        let texts = grid.map { $0.map { Self.attributed(FeishuDocsMarkdownTable.cellMarkdown($0)) } }
        FeishuDocsTableGrid(layout: layout, natural: layout.naturalWidths { r, slot in String(texts[r][slot.column].characters) }) { r, slot in
            FeishuDocsText(text: texts[r][slot.column])
                .font(slot.header ? Theme.Font.secondaryStrong : Theme.Font.secondary)
        } background: { _, _ in nil }
    }

    static func attributed(_ markdown: String) -> AttributedString {
        let opts = AttributedString.MarkdownParsingOptions(interpretedSyntax: .inlineOnlyPreservingWhitespace, failurePolicy: .returnPartiallyParsedIfPossible)
        var s = (try? AttributedString(markdown: markdown, options: opts)) ?? AttributedString(markdown)
        for run in s.runs where run.link != nil { s[run.range].foregroundColor = LarkColor.blue }
        return s
    }
}

/// The grid both tables draw: rows of cells as tall as the row's tallest. When every column fits at a readable width
/// the table fits the width and wraps; otherwise it scrolls sideways with a fade on the side that has more.
struct FeishuDocsTableGrid<Cell: View>: View {
    var layout: FeishuDocsTableLayout
    var natural: [Double]
    @ViewBuilder var cell: (_ row: Int, _ slot: FeishuDocsTableLayout.Slot) -> Cell
    var background: (_ row: Int, _ slot: FeishuDocsTableLayout.Slot) -> Color?
    @State private var available: Double = 0
    @State private var moreLeft = false
    @State private var moreRight = false

    var body: some View {
        let fit = FeishuDocsTableSizing.fit(natural: natural, available: available > 0 ? available : 340)
        Group {
            if fit.scrolls {
                ScrollView(.horizontal) { grid(fit.widths) }
                    .defaultScrollAnchor(Self.debugEnd ? .trailing : .leading)
                    .onScrollGeometryChange(for: [Bool].self) { g in
                        [g.contentOffset.x + g.contentInsets.leading > 1,
                         g.contentSize.width - g.containerSize.width - g.contentOffset.x > 1]
                    } action: { _, v in
                        moreLeft = v[0]
                        moreRight = v[1]
                    }
                    .overlay(alignment: .leading) { if moreLeft { fade(.leading) } }
                    .overlay(alignment: .trailing) { if moreRight { fade(.trailing) } }
            } else {
                grid(fit.widths)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .onGeometryChange(for: Double.self) { $0.size.width } action: { available = $0 }
    }

    private func grid(_ widths: [Double]) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(layout.rows.enumerated()), id: \.offset) { r, slots in
                HStack(alignment: .top, spacing: 0) {
                    ForEach(Array(slots.enumerated()), id: \.offset) { _, slot in
                        let empty = slot.cell == nil
                        Group { if empty { Color.clear.frame(height: Theme.Size.line) } else { cell(r, slot) } }
                            .padding(.horizontal, Theme.Space.s)
                            .padding(.vertical, Theme.Space.sm)
                            .frame(width: FeishuDocsTableSizing.width(widths, column: slot.column, colspan: slot.colspan), alignment: .topLeading)
                            .frame(maxHeight: .infinity, alignment: .topLeading)
                            .background(background(r, slot) ?? (slot.header ? Color.primary.opacity(0.05) : .clear))
                            .overlay(Rectangle().strokeBorder(Color.primary.opacity(empty ? 0.06 : 0.12), lineWidth: 0.5))
                    }
                }
                .fixedSize(horizontal: false, vertical: true)
            }
        }
        .overlay(Rectangle().strokeBorder(Color.primary.opacity(0.14), lineWidth: 0.5))
        .clipShape(.rect(cornerRadius: Theme.Radius.badge))
    }

    private func fade(_ edge: HorizontalEdge) -> some View {
        LinearGradient(colors: [Theme.pageBackground, Theme.pageBackground.opacity(0)],
                       startPoint: edge == .leading ? .leading : .trailing, endPoint: edge == .leading ? .trailing : .leading)
            .scaledFrame(width: Theme.Size.iconS)
            .allowsHitTesting(false)
    }

    #if DEBUG
    /// Simulator automation: `-showFeishuDocTableEnd YES` starts wide tables scrolled to their right edge.
    static var debugEnd: Bool { UserDefaults.standard.bool(forKey: "showFeishuDocTableEnd") }
    #else
    static var debugEnd: Bool { false }
    #endif
}

struct FeishuDocsImage: View {
    var node: FeishuDocsNode
    @Environment(\.feishuDocsContext) private var context

    var body: some View {
        let src = node.attr("src") ?? ""
        let w = node.number("width") ?? node.number("displayWidth") ?? 0
        let h = node.number("height") ?? 0
        let ratio = (w > 0 && h > 0) ? w / h : nil
        VStack(spacing: Theme.Space.sm) {
            Button { context.openImage(src) } label: {
                LarkRemoteImage(url: src, maxPixel: 1600, contentMode: .fit)
                    .aspectRatio(ratio.map { CGFloat($0) }, contentMode: .fit)
                    .frame(maxWidth: w > 0 ? CGFloat(w) : .infinity, minHeight: ratio == nil ? 120 : nil)
                    .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
            }
            .buttonStyle(.plain)
            .accessibilityLabel(node.attr("alt").flatMap { $0.isEmpty ? nil : $0 } ?? String(localized: "images"))
            if let c = node.attr("caption"), !c.isEmpty {
                Text(c).font(Theme.Font.label).foregroundStyle(.secondary).multilineTextAlignment(.center)
            }
        }
        .frame(maxWidth: .infinity)
    }
}

struct FeishuDocsFileBlock: View {
    var node: FeishuDocsNode
    @State private var request: (url: String, name: String)?

    var body: some View {
        let name = node.attr("name") ?? String(localized: "Files")
        let icon = FeishuDocsFileKind.symbol(ext: node.attr("extension"), title: name)
        let size = node.number("size").flatMap { $0 > 0 ? Int64($0).storageText : nil }
        Button {
            if let token = node.attr("token") {
                request = ("/api/v1/lark/raw/documents/drive/files/\(token)/download", name)
            } else if let s = node.attr("sourceUrl") {
                FeishuDocsLinks.openInFeishu(s)
            }
        } label: {
            HStack(spacing: Theme.Space.m) {
                FeishuAppIcon(symbol: icon.symbol, hue: icon.hue, size: 36)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(name).font(Theme.Font.secondaryMedium).lineLimit(1)
                    Text(size ?? String(localized: "Attachment")).font(Theme.Font.label).foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
                Image(systemName: "arrow.down.circle").foregroundStyle(LarkColor.blue)
            }
            .padding(Theme.Space.m)
            .background(LarkColor.cardBackground, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(LarkColor.cardStroke, lineWidth: 0.5))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .modifier(LarkFileOpener(request: $request))
    }
}

/// A link card (bookmark, embedded sheet / Base / chat, unsupported block with its source).
struct FeishuDocsLinkCard: View {
    var symbol: String
    var hue: String
    var title: String
    var subtitle: String?
    var href: String?
    var quiet = false
    @Environment(\.openURL) private var openURL

    var body: some View {
        Button {
            if let href, let u = URL(string: href) { openURL(u) }
        } label: {
            HStack(spacing: Theme.Space.m) {
                Image(systemName: symbol)
                    .font(Theme.Font.secondaryStrong)
                    .foregroundStyle(quiet ? Color.secondary : LarkColor.hue(hue))
                    .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                    .background((quiet ? Color.secondary : LarkColor.hue(hue)).opacity(0.12), in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    Text(title).font(quiet ? Theme.Font.caption : Theme.Font.secondaryMedium)
                        .foregroundStyle(quiet ? .secondary : .primary).lineLimit(2).multilineTextAlignment(.leading)
                    if let subtitle, !subtitle.isEmpty {
                        Text(subtitle).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(2).multilineTextAlignment(.leading)
                    }
                }
                Spacer(minLength: 0)
                if href != nil { Image(systemName: Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary) }
            }
            .padding(Theme.Space.m)
            .background(quiet ? Color.primary.opacity(0.035) : LarkColor.cardBackground, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(quiet ? .clear : LarkColor.cardStroke, lineWidth: 0.5))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .disabled(href == nil)
    }
}
