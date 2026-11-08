import Charts
import SwiftUI

/// One element of a Feishu interactive card as the Lark gateway projects it (`CardNode`): a tag, text, style,
/// children and the per-tag extras (image, options, items, persons…). Kept as raw JSON.
nonisolated struct LarkCardNode {
    var raw: JSONValue

    init(_ raw: JSONValue) { self.raw = raw }
    init?(_ raw: JSONValue?) {
        guard let raw, raw.object != nil else { return nil }
        self.raw = raw
    }

    var tag: String { raw["tag"]?.string ?? "" }
    var text: String? { raw["text"]?.string }
    var url: String? { raw["url"]?.string }
    var children: [LarkCardNode] { (raw["children"]?.array ?? []).compactMap { LarkCardNode($0) } }
    var style: JSONValue? { raw["style"] }
    func style(_ k: String) -> JSONValue? { style?[k] }
    func styleString(_ k: String) -> String? { style?[k]?.string }
    var disabled: Bool { raw["disabled"]?.bool ?? false }
    var token: String? { raw["token"]?.string }
    var imageURL: String? { raw["image"]?["url"]?.string }
    var imageSize: CGSize { CGSize(width: raw["image"]?["width"]?.double ?? 0, height: raw["image"]?["height"]?.double ?? 0) }
    var actionID: String? { raw["actionId"]?.string }
    var actionMethod: Int? { raw["actionMethod"]?.int }
    func node(_ k: String) -> LarkCardNode? { LarkCardNode(raw[k]) }

    /// All the text this node and its children show.
    var plainText: String {
        if let t = text, !t.isEmpty, children.isEmpty { return t }
        let inner = children.map(\.plainText).joined()
        return inner.isEmpty ? (text ?? "") : inner
    }

    var textAttributes: Set<String> { Set((style("textAttributes")?.array ?? []).compactMap(\.string)) }

    static let inlineTags: Set<String> = ["plain_text", "link", "at", "at_all", "person", "person_v1", "person_list", "code_span", "emoji", "br",
                                          "local_datetime", "markdown_emphasis", "markdown_font", "markdown_fallback", "text_tag", "number_tag",
                                          "standard_icon", "custom_icon", "ud_icon"]
    var isInline: Bool { Self.inlineTags.contains(tag) }
    var isIcon: Bool { tag == "standard_icon" || tag == "custom_icon" || tag == "ud_icon" }
}

// MARK: - Card

/// A Feishu card message: header in the card's theme color, then the element tree (port of `CardRenderer`).
struct LarkCardView: View {
    var part: LarkPart

    var body: some View {
        let meta = part.metadata ?? .null
        if meta["variant"]?.string == MessagingViewCard.variant {
            MessagingViewCard(meta: meta, fallback: part.text ?? "")
        } else if meta["variant"]?.string != nil {
            LarkSummaryCardView(meta: meta, fallbackTitle: part.text ?? "")
        } else {
            let theme = meta["theme"]?.string ?? "blue"
            VStack(alignment: .leading, spacing: 0) {
                if let header = LarkCardNode(meta["header"]) {
                    LarkCardHeader(node: header, theme: theme)
                } else if meta["hideHeader"]?.bool != true, let title = meta["title"]?.string ?? part.text, !title.isEmpty {
                    LarkCardHeader(node: LarkCardNode(.object(["tag": .string("card_header"), "children": .array([.object(["tag": .string("plain_text"), "text": .string(title)])])])), theme: theme)
                }
                if let body = LarkCardNode(meta["body"]) {
                    LarkCardFlow(nodes: body.children, spacing: Self.spacing(body) ?? 10)
                        .padding(.horizontal, Theme.Space.m)
                        .padding(.vertical, Theme.Space.m)
                } else if let fallback = meta["fallback"]?.array, !fallback.isEmpty {
                    VStack(alignment: .leading, spacing: Theme.Space.sm) {
                        ForEach(Array(fallback.compactMap(\.string).enumerated()), id: \.offset) { _, t in Text(t).font(Theme.Font.secondary) }
                    }
                    .padding(Theme.Space.m)
                }
            }
            .environment(\.larkTextBase, LarkTextBase(size: Theme.Level.secondary.size))
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(LarkColor.cardBackground)
            .clipShape(.rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(LarkColor.cardStroke))
        }
    }

    static func spacing(_ n: LarkCardNode) -> CGFloat? {
        (n.style("verticalSpacingValue") ?? n.style("horizontalSpacingValue")).flatMap(LarkCardStyle.unit)
    }
}

/// Title row (leading icons, title, tags), optional subtitle, tinted with the card's template color.
struct LarkCardHeader: View {
    var node: LarkCardNode
    var theme: String

    var body: some View {
        let kids = node.children
        let texts = kids.filter { $0.tag == "plain_text" }
        let tags = kids.filter { $0.tag == "text_tag" }
        let extras = kids.filter { $0.tag != "plain_text" && $0.tag != "text_tag" }
        let tinted = !(theme.isEmpty || theme == "default" || theme == "white")
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                ForEach(Array(extras.enumerated()), id: \.offset) { _, e in LarkCardNodeView(node: e) }
                if let title = texts.first {
                    LarkInlineText(runs: LarkCardRuns.runs(title, inMarkdown: false), base: LarkTextBase(size: Theme.Level.heading.size, bold: true))
                }
                Spacer(minLength: 0)
            }
            if !tags.isEmpty {
                LarkFlowLayout(spacing: 6, lineSpacing: 6) { ForEach(Array(tags.enumerated()), id: \.offset) { _, t in LarkCardTag(node: t) } }
            }
            if texts.count > 1 {
                LarkInlineText(runs: LarkCardRuns.runs(texts[1], inMarkdown: false), base: LarkTextBase(size: Theme.Level.caption.size, color: .secondary))
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.m)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(tinted ? LarkColor.hue(theme).opacity(0.13) : Color.clear)
        .overlay(alignment: .bottom) { if !tinted { Divider() } }
    }
}

// MARK: - Flow of nodes

/// A list of card elements: consecutive inline elements wrap together as one paragraph; blocks stack.
struct LarkCardFlow: View {
    var nodes: [LarkCardNode]
    var inMarkdown = false
    var spacing: CGFloat = 6
    var alignment: HorizontalAlignment = .leading

    private enum Chunk {
        case inline([LarkCardNode])
        case block(LarkCardNode)
    }

    private var chunks: [Chunk] {
        var out: [Chunk] = []
        var run: [LarkCardNode] = []
        for n in nodes {
            if n.tag == "collapse_divider" { continue }
            if n.isInline { run.append(n) } else {
                if !run.isEmpty { out.append(.inline(run)); run = [] }
                out.append(.block(n))
            }
        }
        if !run.isEmpty { out.append(.inline(run)) }
        return out
    }

    var body: some View {
        VStack(alignment: alignment, spacing: spacing) {
            ForEach(Array(chunks.enumerated()), id: \.offset) { _, chunk in
                switch chunk {
                case .inline(let run): LarkCardInlineRun(nodes: run, inMarkdown: inMarkdown)
                case .block(let n): LarkCardNodeView(node: n)
                }
            }
        }
    }
}

/// Inline elements as text; a run made only of tags / icons lays out as chips instead.
struct LarkCardInlineRun: View {
    var nodes: [LarkCardNode]
    var inMarkdown: Bool
    @Environment(\.larkTextBase) private var base

    var body: some View {
        let chipsOnly = nodes.allSatisfy { $0.tag == "text_tag" || $0.isIcon || ($0.tag == "plain_text" && ($0.text ?? "").trimmingCharacters(in: .whitespaces).isEmpty) }
        if chipsOnly && nodes.contains(where: { $0.tag == "text_tag" }) {
            LarkFlowLayout(spacing: 6, lineSpacing: 6) {
                ForEach(Array(nodes.enumerated()), id: \.offset) { _, n in
                    if n.tag == "text_tag" { LarkCardTag(node: n) } else if n.isIcon { LarkCardIcon(node: n, size: base.size) }
                }
            }
        } else {
            let align = nodes.first?.styleString("textAlign")
            let lines = nodes.count == 1 ? nodes[0].style("lines")?.int : nil
            let _ = LarkMedia.shared.emojiVersion
            LarkInlineText(runs: nodes.flatMap { LarkCardRuns.runs($0, inMarkdown: inMarkdown) }, base: base,
                           lineLimit: (lines ?? 0) > 0 ? lines : nil,
                           alignment: align == "center" ? .center : align == "right" ? .trailing : .leading)
                .frame(maxWidth: align == "center" || align == "right" ? .infinity : nil,
                       alignment: align == "center" ? .center : align == "right" ? .trailing : .leading)
                .task { await LarkCardRuns.preloadIcons(nodes) }
        }
    }
}

nonisolated enum LarkCardRuns {
    static func style(_ n: LarkCardNode, _ inherited: LarkRunStyle = LarkRunStyle()) -> LarkRunStyle {
        var s = inherited
        let attrs = n.textAttributes
        if attrs.contains("bold") { s.bold = true }
        if attrs.contains("italic") { s.italic = true }
        if attrs.contains("strikethrough") { s.strike = true }
        if attrs.contains("underline") { s.underline = true }
        if let c = LarkColor.custom(n.style("customTextColor")) ?? LarkColor.text(n.styleString("textColor") ?? n.styleString("color")) { s.color = c }
        if let size = LarkCardStyle.textSize(n.styleString("textSize")) { s.size = size }
        return s
    }

    static func runs(_ n: LarkCardNode, inMarkdown: Bool, inherited: LarkRunStyle = LarkRunStyle()) -> [LarkInline] {
        let s = style(n, inherited)
        let kids = n.children
        func inner(_ st: LarkRunStyle) -> [LarkInline] { kids.flatMap { runs($0, inMarkdown: inMarkdown, inherited: st) } }
        switch n.tag {
        case "plain_text":
            let t = n.text ?? ""
            return inMarkdown ? LarkFormat.cardMarkdown(t, style: s) : [.text(t, s)]
        case "link":
            var l = s
            l.link = n.url.flatMap(LarkPartRenderer.safeURL)
            return kids.isEmpty ? [.text(n.text ?? n.url ?? "", l)] : inner(l)
        case "at", "person", "person_v1":
            var p = s
            p.color = LarkColor.blue
            let name = n.text ?? ""
            return [.text(n.tag == "at" && !name.hasPrefix("@") ? "@" + name : name, p)]
        case "at_all":
            var p = s
            p.color = LarkColor.blue
            return [.text(n.text ?? String(localized: "@Everyone"), p)]
        case "person_list":
            var p = s
            p.color = LarkColor.blue
            let names = (n.raw["persons"]?.array ?? []).compactMap { $0["text"]?.string }
            return [.text(names.isEmpty ? (n.text ?? "") : names.joined(separator: "、"), p)]
        case "code_span":
            var c = s
            c.code = true
            return [.text(inMarkdown ? LarkFormat.entities(n.text ?? "") : (n.text ?? ""), c)]
        case "emoji":
            if let key = n.token ?? n.text, !key.isEmpty { return [.emoji(key: key, fallback: n.text ?? LarkMedia.labelFallback(key, text: nil))] }
            return []
        case "br":
            return [.text("\n", s)]
        case "local_datetime":
            return [.text(datetime(ms: n.raw["milliseconds"]?.double, format: n.raw["formatType"]?.string) ?? n.text ?? "", s)]
        case "markdown_emphasis":
            var e = s
            e.italic = true
            return kids.isEmpty ? [.text(n.text ?? "", e)] : inner(e)
        case "markdown_font", "markdown_fallback":
            return kids.isEmpty ? [.text(n.text ?? "", s)] : inner(s)
        case "text_tag":
            var t = s
            let hue = LarkColor.hue(n.styleString("color"))
            t.color = hue
            t.background = hue.opacity(0.14)
            t.size = 12
            return [.text("\u{2009}" + n.plainText + "\u{2009}", t)]
        case "number_tag":
            var t = s
            t.color = LarkColor.blue
            t.size = 11
            t.link = n.url.flatMap(LarkPartRenderer.safeURL)
            return [.text("[\(n.plainText)]", t)]
        case "standard_icon", "custom_icon", "ud_icon":
            if let sym = LarkCardIcon.symbol(n.token) { return [.symbol(sym.0, LarkColor.text(n.styleString("color")) ?? sym.1)] }
            if let url = n.imageURL { return [.image(url: url)] }
            return []
        default:
            return kids.isEmpty ? [.text(n.text ?? "", s)] : inner(s)
        }
    }

    /// `local_datetime` in the reader's own time zone.
    static func datetime(ms: Double?, format: String?) -> String? {
        guard let ms else { return nil }
        let d = Date(timeIntervalSince1970: ms / 1000)
        switch format {
        case "date_num": return d.formatted(.dateTime.year().month(.twoDigits).day(.twoDigits))
        case "date_short": return d.formatted(.dateTime.month().day())
        case "date": return d.formatted(date: .long, time: .omitted)
        case "time": return d.formatted(date: .omitted, time: .shortened)
        case "time_sec": return d.formatted(.dateTime.hour().minute().second())
        default: return d.formatted(date: .abbreviated, time: .shortened)
        }
    }

    /// Custom icons drawn as images need their pixels before the text can embed them.
    @MainActor static func preloadIcons(_ nodes: [LarkCardNode]) async {
        for n in nodes where n.isIcon && LarkCardIcon.symbol(n.token) == nil {
            if let url = n.imageURL, LarkMedia.shared.cached(url, size: larkThumbEdge(points: 20)) == nil {
                _ = await LarkMedia.shared.image(url, size: larkThumbEdge(points: 20), maxPixel: 96)
            }
        }
    }
}

// MARK: - Elements

/// Renders one block-level card element (port of `CardNodeView`).
struct LarkCardNodeView: View {
    @Environment(\.dynamicTypeSize) private var typeSize
    var node: LarkCardNode
    @Environment(\.larkTextBase) private var base

    var body: some View { content.modifier(LarkCardBox(style: node.style)) }

    private var content: AnyView {
        let n = node
        switch n.tag {
        case "body", "form":
            return AnyView(LarkCardFlow(nodes: n.children, spacing: LarkCardView.spacing(n) ?? 10))
        case "card_header":
            return AnyView(LarkCardHeader(node: n, theme: ""))
        case "div":
            return AnyView(VStack(alignment: .leading, spacing: Theme.Space.s) {
                if !n.children.isEmpty { LarkCardFlow(nodes: n.children, alignment: Self.alignment(n)) }
                let fields = n.raw["fields"]?.array ?? []
                if !fields.isEmpty { LarkCardFields(fields: fields) }
            })
        case "markdown", "lark_md":
            let kids = n.children
            if let first = kids.first, first.isIcon {
                return AnyView(HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                    LarkCardIcon(node: first, size: base.size)
                    LarkCardFlow(nodes: Array(kids.dropFirst()), inMarkdown: true)
                })
            }
            if kids.isEmpty, let t = n.text { return AnyView(LarkInlineText(runs: LarkFormat.cardMarkdown(t, style: LarkRunStyle()))) }
            return AnyView(LarkCardFlow(nodes: kids, inMarkdown: true, alignment: Self.alignment(n))
                .frame(maxWidth: Self.alignment(n) == .leading ? nil : .infinity, alignment: Self.frameAlignment(n)))
        case "plain_text", "link", "at", "person", "person_v1", "code_span", "local_datetime":
            return AnyView(LarkCardInlineRun(nodes: [n], inMarkdown: false))
        case "column_set":
            return AnyView(LarkCardColumns(node: n))
        case "column":
            return AnyView(LarkCardColumn(node: n))
        case "interactive_container":
            return AnyView(LarkCardContainer(node: n))
        case "hr":
            return AnyView(Divider().padding(.vertical, Theme.Space.xxs))
        case "img":
            return AnyView(LarkCardImage(node: n))
        case "img_combination":
            let imgs = n.children
            return AnyView(LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 4), count: imgs.count >= 3 ? 3 : max(imgs.count, 1)), spacing: 4) {
                ForEach(Array(imgs.enumerated()), id: \.offset) { _, i in LarkCardImage(node: i, square: true) }
            })
        case "action":
            return AnyView(LarkFlowLayout(spacing: 8, lineSpacing: 8) {
                ForEach(Array(n.children.enumerated()), id: \.offset) { _, c in LarkCardNodeView(node: c) }
            })
        case "button":
            return AnyView(LarkCardButton(node: n))
        case "list":
            return AnyView(LarkCardList(node: n))
        case "blockquote":
            return AnyView(LarkCardFlow(nodes: n.children, inMarkdown: true)
                .environment(\.larkTextBase, LarkTextBase(size: base.size, color: .secondary))
                .padding(.leading, Theme.Space.m)
                .overlay(alignment: .leading) { Capsule().fill(Color.primary.opacity(0.18)).frame(width: Theme.Size.bar) })
        case "code_block":
            return AnyView(LarkCodeBlock(code: n.text ?? n.plainText, language: n.raw["language"]?.string))
        case "heading":
            return AnyView(LarkCardFlow(nodes: n.children, inMarkdown: true).environment(\.larkTextBase, LarkTextBase(size: base.size + 2, bold: true)))
        case "note":
            return AnyView(LarkCardNote(node: n))
        case "text_tag":
            return AnyView(LarkCardTag(node: n))
        case "standard_icon", "custom_icon", "ud_icon":
            return AnyView(LarkCardIcon(node: n, size: base.size))
        case "select_static", "multi_select_static", "select_person", "multi_select_person", "date_picker", "picker_datetime", "picker_time", "input":
            return AnyView(LarkCardField(node: n))
        case "select_img":
            return AnyView(LarkFlowLayout(spacing: 6) {
                ForEach(Array((n.raw["options"]?.array ?? []).enumerated()), id: \.offset) { _, o in
                    if let u = o["image"]?["url"]?.string {
                        LarkRemoteImage(url: u, maxPixel: 300).scaledFrame(width: Theme.Size.box72, height: Theme.Size.box72).clipShape(.rect(cornerRadius: Theme.Radius.control))
                            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.control).strokeBorder(o["selected"]?.bool == true ? LarkColor.blue : .clear, lineWidth: 2))
                    }
                }
            })
        case "checker":
            return AnyView(HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                Image(systemName: n.raw["checked"]?.bool == true ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                    .foregroundStyle(n.raw["checked"]?.bool == true ? LarkColor.blue : .secondary)
                LarkCardFlow(nodes: n.children)
                    .opacity(n.raw["checked"]?.bool == true ? 0.6 : 1)
            })
        case "overflow":
            return AnyView(LarkCardOverflow(node: n))
        case "collapsible_panel":
            return AnyView(LarkCardPanel(node: n))
        case "table", "markdown_table":
            return AnyView(LarkCardTable(node: n))
        case "markdown_table_row", "markdown_table_cell", "markdown_table_header":
            return AnyView(LarkCardFlow(nodes: n.children, inMarkdown: true))
        case "chart":
            return AnyView(LarkCardChart(spec: n.raw["chart"] ?? .null))
        case "avatar":
            return AnyView(LarkAvatarImage(name: n.text ?? "?", url: n.imageURL, size: 24))
        case "audio", "markdown_audio", "video":
            return AnyView(Label(n.text ?? (n.tag == "video" ? String(localized: "Video") : String(localized: "Audio")), systemImage: Theme.Icon.run)
                .font(Theme.Font.secondary)
                .padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.s)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color.primary.opacity(0.05), in: .rect(cornerRadius: Theme.Radius.control)))
        case "fallback_text":
            return AnyView(Text(n.plainText).font(Theme.Font.secondary).foregroundStyle(.secondary))
        case "collapse_divider":
            return AnyView(EmptyView())
        default:
            if !n.children.isEmpty { return AnyView(LarkCardFlow(nodes: n.children)) }
            if let t = n.text, !t.isEmpty { return AnyView(Text(t).font(.system(size: base.size * Theme.textScale(typeSize)))) }
            return AnyView(EmptyView())
        }
    }

    static func alignment(_ n: LarkCardNode) -> HorizontalAlignment {
        switch n.styleString("textAlign") { case "center": .center; case "right": .trailing; default: .leading }
    }

    static func frameAlignment(_ n: LarkCardNode) -> Alignment {
        switch n.styleString("textAlign") { case "center": .center; case "right": .trailing; default: .leading }
    }
}

/// Margin, padding, background, border and corner radius from a card element's style (port of `cardStyle`).
struct LarkCardBox: ViewModifier {
    var style: JSONValue?

    func body(content: Content) -> some View {
        let pad = LarkCardStyle.insets(style?["padding"])
        let margin = LarkCardStyle.insets(style?["margin"])
        let bg = LarkColor.custom(style?["customBackgroundStyle"]) ?? LarkColor.background(style?["backgroundStyle"]?.string)
        let radius = style?["cornerRadiusValue"].flatMap(LarkCardStyle.unit) ?? (bg != nil ? 8 : 0)
        let border = style?["hasBorder"]?.bool == true
        let borderColor = LarkColor.text(style?["borderColor"]?.string) ?? LarkColor.cardStroke
        content
            .padding(pad)
            .background(bg ?? .clear, in: .rect(cornerRadius: radius, style: .continuous))
            .overlay { if border { RoundedRectangle(cornerRadius: max(radius, 6), style: .continuous).strokeBorder(borderColor) } }
            .padding(margin)
    }
}

nonisolated enum LarkCardStyle {
    static func unit(_ v: JSONValue) -> CGFloat? {
        if let d = v.double { return CGFloat(max(0, min(64, d))) }
        if let d = v["value"]?.double { return CGFloat(max(0, min(64, d))) }
        switch v["value"]?.string ?? v.string {
        case "small": return 4
        case "medium": return 8
        case "large": return 12
        case "extra_large": return 16
        case "default": return 8
        default: return nil
        }
    }

    static func insets(_ v: JSONValue?) -> EdgeInsets {
        guard let v, v.object != nil else { return EdgeInsets() }
        func f(_ k: String) -> CGFloat { CGFloat(max(0, min(48, v[k]?.double ?? 0))) }
        return EdgeInsets(top: f("top"), leading: f("left"), bottom: f("bottom"), trailing: f("right"))
    }

    static func textSize(_ s: String?) -> CGFloat? {
        switch s {
        case "notation", "small": 12
        case "normal": 15
        case "large": 17
        case "heading", "heading-4": 18
        case "heading-3": 20
        case "heading-2": 22
        case "heading-1", "heading-0": 24
        default: nil
        }
    }
}

/// `column_set`: columns side by side, by weight / fixed width / content width (port of the CSS flex rules).
struct LarkCardColumns: View {
    var node: LarkCardNode

    var body: some View {
        let cols = node.children.filter { $0.tag == "column" }
        let spacing = node.style("horizontalSpacingValue").flatMap(LarkCardStyle.unit) ?? 8
        let mode = node.styleString("flexMode") ?? "none"
        if mode == "bisect" || mode == "trisect" || mode == "stretch" {
            // Narrow-screen modes: at most 2 / 3 per row (stretch: one per row), fewer when a column would
            // get under 64 pt, as Feishu's own client re-flows them on phones.
            LarkGridColumnsLayout(perRow: mode == "bisect" ? 2 : mode == "trisect" ? 3 : 1, minWidth: 64, spacing: spacing) {
                ForEach(Array(cols.enumerated()), id: \.offset) { _, c in LarkCardNodeView(node: c) }
            }
        } else if mode == "flow" {
            LarkFlowLayout(spacing: spacing, lineSpacing: spacing) {
                ForEach(Array(cols.enumerated()), id: \.offset) { _, c in LarkCardNodeView(node: c) }
            }
        } else {
            LarkColumnsLayout(specs: cols.map(Self.spec), spacing: spacing, vertical: node.styleString("verticalAlign") ?? "top") {
                ForEach(Array(cols.enumerated()), id: \.offset) { _, c in LarkCardNodeView(node: c) }
            }
        }
    }

    static func spec(_ c: LarkCardNode) -> LarkColumnsLayout.Spec {
        let w = c.style("widthValue")
        if w?["type"]?.string == "pixels", let px = w?["value"]?.double { return .fixed(CGFloat(min(px, 720))) }
        if w?["type"]?.string == "builtin_column_width", w?["value"]?.string == "auto" { return .auto }
        if c.styleString("width") == "auto" { return .auto }
        return .weight(max(c.style("weight")?.double ?? w?["value"]?.double ?? 1, 0.1))
    }
}

/// Places columns in one row: fixed widths first, content-sized (`auto`) columns get what they need up to an
/// equal share, weighted columns split the rest; every column is as tall as the tallest.
struct LarkColumnsLayout: Layout {
    enum Spec { case fixed(CGFloat), auto, weight(Double) }
    var specs: [Spec]
    var spacing: CGFloat
    var vertical: String

    private func widths(_ total: CGFloat, _ subviews: Subviews) -> [CGFloat] {
        let n = min(specs.count, subviews.count)
        guard n > 0 else { return [] }
        var w = [CGFloat](repeating: 0, count: n)
        var avail = total - spacing * CGFloat(n - 1)
        for i in 0..<n { if case .fixed(let px) = specs[i] { w[i] = min(px, max(avail, 0)); avail -= w[i] } }
        avail = max(avail, 0)
        let flexible = (0..<n).filter { if case .fixed = specs[$0] { return false } else { return true } }
        guard !flexible.isEmpty else { return w }
        let weighted = flexible.filter { if case .weight = specs[$0] { return true } else { return false } }
        let autos = flexible.filter { if case .auto = specs[$0] { return true } else { return false } }
        // Water-fill the auto columns: each takes min(ideal, fair share), leftovers go round again.
        var remaining = avail
        var open = autos
        let ideal = Dictionary(uniqueKeysWithValues: autos.map { ($0, subviews[$0].sizeThatFits(.unspecified).width) })
        let shareCount = CGFloat(autos.count + weighted.count)
        while !open.isEmpty {
            let share = remaining / max(CGFloat(open.count) + (weighted.isEmpty ? 0 : CGFloat(weighted.count)), 1)
            let fitting = open.filter { ideal[$0]! <= share }
            if fitting.isEmpty {
                for i in open { w[i] = weighted.isEmpty ? share : min(ideal[i]!, avail / shareCount) }
                remaining -= open.reduce(0) { $0 + w[$1] }
                open = []
            } else {
                for i in fitting { w[i] = ideal[i]!; remaining -= w[i] }
                open.removeAll { fitting.contains($0) }
            }
        }
        let totalWeight = weighted.reduce(0.0) { if case .weight(let x) = specs[$1] { $0 + x } else { $0 } }
        for i in weighted { if case .weight(let x) = specs[i] { w[i] = max(remaining, 0) * CGFloat(x / max(totalWeight, 0.1)) } }
        return w
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let total = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? 320
        let w = widths(total, subviews)
        let h = zip(subviews, w).map { $0.sizeThatFits(ProposedViewSize(width: $1, height: nil)).height }.max() ?? 0
        return CGSize(width: total, height: h)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let w = widths(bounds.width, subviews)
        var x = bounds.minX
        for (i, s) in subviews.enumerated() {
            guard i < w.count else { s.place(at: .zero, proposal: .zero); continue }
            let h = s.sizeThatFits(ProposedViewSize(width: w[i], height: nil)).height
            let y: CGFloat = switch vertical {
            case "center", "middle": bounds.minY + (bounds.height - h) / 2
            case "bottom": bounds.maxY - h
            default: bounds.minY
            }
            s.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(width: w[i], height: h))
            x += w[i] + spacing
        }
    }
}

/// Equal-width columns, `perRow` per row at most, fewer when they would be narrower than `minWidth`.
struct LarkGridColumnsLayout: Layout {
    var perRow: Int
    var minWidth: CGFloat
    var spacing: CGFloat

    private func columns(_ width: CGFloat) -> Int {
        guard width.isFinite else { return perRow }
        return max(1, min(perRow, Int((width + spacing) / (minWidth + spacing))))
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? 320
        let n = columns(width)
        let w = (width - spacing * CGFloat(n - 1)) / CGFloat(n)
        var height: CGFloat = 0
        for start in stride(from: 0, to: subviews.count, by: n) {
            let row = subviews[start..<min(start + n, subviews.count)].map { $0.sizeThatFits(ProposedViewSize(width: w, height: nil)).height }.max() ?? 0
            height += row + (start > 0 ? spacing : 0)
        }
        return CGSize(width: width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let n = columns(bounds.width)
        let w = (bounds.width - spacing * CGFloat(n - 1)) / CGFloat(n)
        var y = bounds.minY
        for start in stride(from: 0, to: subviews.count, by: n) {
            let row = subviews[start..<min(start + n, subviews.count)]
            let h = row.map { $0.sizeThatFits(ProposedViewSize(width: w, height: nil)).height }.max() ?? 0
            for (i, s) in row.enumerated() {
                s.place(at: CGPoint(x: bounds.minX + CGFloat(i) * (w + spacing), y: y), proposal: ProposedViewSize(width: w, height: h))
            }
            y += h + spacing
        }
    }
}

struct LarkCardColumn: View {
    var node: LarkCardNode

    var body: some View {
        let spacing = LarkCardView.spacing(node) ?? 6
        Group {
            if node.styleString("direction") == "horizontal" {
                LarkFlowLayout(spacing: spacing, lineSpacing: spacing) {
                    ForEach(Array(node.children.enumerated()), id: \.offset) { _, c in LarkCardNodeView(node: c) }
                }
            } else {
                LarkCardFlow(nodes: node.children, spacing: spacing, alignment: LarkCardNodeView.alignment(node))
            }
        }
        .frame(maxWidth: .infinity, alignment: LarkCardNodeView.frameAlignment(node))
        .padding(LarkColor.background(node.styleString("backgroundStyle")) != nil && node.style("padding") == nil ? 8 : 0)
    }
}

/// `interactive_container`: a framed group; tapping it opens its link.
struct LarkCardContainer: View {
    var node: LarkCardNode
    @Environment(\.openURL) private var openURL

    var body: some View {
        let link = node.url.flatMap(LarkPartRenderer.safeURL) ?? node.raw["behaviors"]?.array?.first?["default_url"]?.string.flatMap(LarkPartRenderer.safeURL)
        LarkCardFlow(nodes: node.children, spacing: LarkCardView.spacing(node) ?? 6)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(.rect)
            .onTapGesture { if let link { openURL(link) } }
    }
}

struct LarkCardFields: View {
    var fields: [JSONValue]

    var body: some View {
        let items = fields.map { (short: $0["short"]?.bool ?? false, nodes: ($0["children"]?.array ?? []).compactMap { LarkCardNode($0) }) }
        Grid(alignment: .topLeading, horizontalSpacing: 12, verticalSpacing: 8) {
            ForEach(Array(Self.rows(items).enumerated()), id: \.offset) { _, row in
                GridRow {
                    ForEach(Array(row.enumerated()), id: \.offset) { _, f in
                        LarkCardFlow(nodes: f.nodes).frame(maxWidth: .infinity, alignment: .leading)
                            .gridCellColumns(row.count == 1 ? 2 : 1)
                    }
                }
            }
        }
    }

    /// Two short fields share a row; a long one takes the whole row.
    static func rows(_ items: [(short: Bool, nodes: [LarkCardNode])]) -> [[(short: Bool, nodes: [LarkCardNode])]] {
        var out: [[(short: Bool, nodes: [LarkCardNode])]] = []
        var pending: (short: Bool, nodes: [LarkCardNode])?
        for f in items {
            if f.short {
                if let p = pending { out.append([p, f]); pending = nil } else { pending = f }
            } else {
                if let p = pending { out.append([p]); pending = nil }
                out.append([f])
            }
        }
        if let p = pending { out.append([p]) }
        return out
    }
}

/// A card picture: full width at its own aspect ratio (cropped past 300 pt), small icons at their size.
struct LarkCardImage: View {
    var node: LarkCardNode
    var square = false
    @State private var viewing = false
    @Namespace private var zoom

    var body: some View {
        if let url = node.imageURL {
            let size = node.imageSize
            let small = size.width > 0 && size.width <= 96 && !square
            Group {
                if small {
                    LarkRemoteImage(url: url, maxPixel: 300, contentMode: .fit).frame(width: size.width / 2, height: size.height / 2)
                } else if square {
                    Color.clear.aspectRatio(1, contentMode: .fit).overlay { LarkRemoteImage(url: url, maxPixel: 600) }.clipped()
                } else {
                    let ratio = size.width > 0 && size.height > 0 ? size.width / size.height : 16 / 9
                    Color.clear
                        .aspectRatio(max(ratio, 0.75), contentMode: .fit)
                        .frame(maxWidth: .infinity)
                        .frame(maxHeight: 320)
                        .overlay { LarkRemoteImage(url: url, maxPixel: 1200) }
                        .clipped()
                }
            }
            .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
            .contentShape(.rect)
            .matchedTransitionSource(id: url, in: zoom)
            .onTapGesture { if node.raw["preview"]?.bool != false { viewing = true } }
            .fullScreenCover(isPresented: $viewing) { ImageViewer(urls: [url], index: 0).navigationTransition(.zoom(sourceID: url, in: zoom)) }
            .accessibilityLabel(node.text ?? String(localized: "images"))
        }
    }
}

/// A button: a link opens in Safari, a callback button presses the card action as the user.
struct LarkCardButton: View {
    var node: LarkCardNode
    @Environment(\.openURL) private var openURL
    @Environment(\.larkContext) private var context
    @Environment(\.larkRefresh) private var refresh
    @Environment(\.larkAccount) private var larkAccount
    @State private var pending = false

    var body: some View {
        let kind = node.styleString("type") ?? "default"
        let link = node.url.flatMap(LarkPartRenderer.safeURL)
        let callback = node.actionID != nil && node.actionMethod == 2 && context.messageID != nil
        let enabled = !node.disabled && (link != nil || callback)
        let fill = node.style("widthValue")?["value"]?.string == "fill" || node.styleString("width") == "fill"
        let label = node.plainText.trimmingCharacters(in: .whitespacesAndNewlines)
        let icon = node.children.first(where: \.isIcon)
        Button {
            if let link { openURL(link); return }
            guard callback, let mid = context.messageID, let aid = node.actionID else { return }
            pending = true
            Task {
                defer { pending = false }
                do {
                    try await AppModel.shared.api?.lark(larkAccount).larkCardAction(message: mid, action: aid)
                    Haptics.success()
                    AppModel.shared.toast = Toast(String(localized: "Submitted “\(label)”"), style: .success)
                    try? await Task.sleep(for: .seconds(1.2))
                    refresh?()
                } catch {
                    AppModel.shared.show(error)
                }
            }
        } label: {
            HStack(spacing: Theme.Space.xs) {
                if pending { ProgressView().controlSize(.mini) } else if let icon { LarkCardIcon(node: icon, size: 14) }
                if !label.isEmpty { Text(label).lineLimit(1).minimumScaleFactor(0.7) }
            }
            .font(Theme.Font.secondaryMedium)
            .padding(.horizontal, fill ? 6 : 14)
            .scaledFrame(minHeight: Theme.Size.well)
            .frame(maxWidth: fill ? .infinity : nil)
            .foregroundStyle(Self.foreground(kind))
            .background(Self.background(kind), in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
            .overlay {
                if Self.bordered(kind) {
                    RoundedRectangle(cornerRadius: Theme.Radius.badge, style: .continuous).strokeBorder(kind.hasPrefix("danger") ? Color.red.opacity(0.5) : LarkColor.cardStroke)
                }
            }
        }
        .buttonStyle(.plain)
        .disabled(!enabled || pending)
        .opacity(enabled ? 1 : 0.5)
        .accessibilityLabel(label.isEmpty ? (node.raw["tooltip"]?.string ?? String(localized: "Button")) : label)
    }

    static func foreground(_ kind: String) -> Color {
        switch kind {
        case "primary", "primary_filled", "danger_filled": .white
        case "danger", "danger_text": LarkColor.text("red")!
        case "text", "primary_text", "laser": LarkColor.blue
        default: .primary
        }
    }

    static func background(_ kind: String) -> Color {
        switch kind {
        case "primary", "primary_filled": LarkColor.blue
        case "danger_filled": LarkColor.text("red")!
        case "text", "primary_text", "danger_text", "laser": .clear
        default: LarkColor.cardBackground
        }
    }

    static func bordered(_ kind: String) -> Bool { !["primary", "primary_filled", "danger_filled", "text", "primary_text", "danger_text", "laser"].contains(kind) }
}

struct LarkCardList: View {
    @Environment(\.dynamicTypeSize) private var typeSize
    var node: LarkCardNode
    @Environment(\.larkTextBase) private var base

    var body: some View {
        let items = node.raw["items"]?.array ?? []
        let markers = Self.markers(items)
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            ForEach(Array(items.enumerated()), id: \.offset) { i, item in
                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                    Text(markers[i]).font(.system(size: base.size * Theme.textScale(typeSize)).monospacedDigit()).foregroundStyle(.secondary).fixedSize()
                    LarkCardFlow(nodes: (item["children"]?.array ?? []).compactMap { LarkCardNode($0) }, inMarkdown: true)
                }
                .padding(.leading, CGFloat(min(4, item["level"]?.int ?? 0)) * 16)
            }
        }
    }

    /// Numbering restarts per nesting level, like the official renderer's nested lists.
    static func markers(_ items: [JSONValue]) -> [String] {
        var counters: [Int: Int] = [:]
        return items.map { item in
            let level = item["level"]?.int ?? 0
            counters = counters.filter { $0.key <= level }
            guard item["type"]?.string == "ol" else { return ["•", "◦", "▪︎"][min(level, 2)] }
            counters[level, default: 0] += 1
            return "\(counters[level]!)."
        }
    }
}

struct LarkCardNote: View {
    var node: LarkCardNode
    @Environment(\.larkTextBase) private var base

    var body: some View {
        let kids = node.children
        HStack(alignment: .center, spacing: Theme.Space.sm) {
            ForEach(Array(kids.enumerated()), id: \.offset) { _, k in
                if k.tag == "img", let u = k.imageURL {
                    LarkRemoteImage(url: u, size: larkThumbEdge(points: 16), maxPixel: 64).scaledFrame(width: Theme.Size.markM, height: Theme.Size.markM).clipShape(.rect(cornerRadius: 3))
                } else if k.isIcon {
                    LarkCardIcon(node: k, size: 12)
                } else {
                    LarkCardFlow(nodes: [k])
                }
            }
        }
        .environment(\.larkTextBase, LarkTextBase(size: Theme.Level.label.size, color: .secondary))
    }
}

struct LarkCardTag: View {
    var node: LarkCardNode

    var body: some View {
        let hue = LarkColor.hue(node.styleString("color"))
        Text(node.plainText)
            .font(Theme.Font.labelMedium)
            .lineLimit(1)
            .foregroundStyle(node.styleString("color") == "neutral" || node.styleString("color") == "grey" ? .secondary : hue)
            .padding(.horizontal, Theme.Space.sm)
            .padding(.vertical, Theme.Space.xxs)
            .background(hue.opacity(0.14), in: .rect(cornerRadius: 4, style: .continuous))
    }
}

struct LarkCardIcon: View {
    var node: LarkCardNode
    var size: CGFloat

    var body: some View {
        if let sym = Self.symbol(node.token) {
            Image(systemName: sym.0).font(.system(size: size * 0.95)).foregroundStyle(LarkColor.text(node.styleString("color")) ?? sym.1 ?? .secondary)
        } else if let url = node.imageURL {
            LarkRemoteImage(url: url, size: larkThumbEdge(points: size), maxPixel: 96, contentMode: .fit).frame(width: size, height: size)
        } else {
            Image(systemName: Theme.Icon.dot).font(.system(size: size * 0.4)).foregroundStyle(.secondary)
        }
    }

    /// UD icon tokens drawn with the closest SF Symbol (warning, info, success, time, people…).
    nonisolated static func symbol(_ token: String?) -> (String, Color?)? {
        guard let t = token?.lowercased(), !t.isEmpty else { return nil }
        let table: [(String, String, Color?)] = [
            ("warning", "exclamationmark.triangle.fill", .orange), ("alert", "exclamationmark.triangle.fill", .orange), // icon: Lark card icon tokens
            ("error", "xmark.octagon.fill", .red), ("close", "xmark.circle.fill", .red), ("cancel", "xmark.circle.fill", .red), // icon: as Lark draws its card icons // icon: as Lark draws its card icons
            ("info", "info.circle", nil), ("maybe", "questionmark.circle", nil), ("help", "questionmark.circle", nil), ("question", "questionmark.circle", nil), // icon: Lark card icon tokens
            ("succeed", "checkmark.circle.fill", .green), ("success", "checkmark.circle.fill", .green), ("done", "checkmark.circle.fill", .green), ("check", "checkmark.circle.fill", .green), ("yes", "checkmark.circle.fill", .green), // icon: Lark card icon tokens
            ("time", "clock", nil), ("clock", "clock", nil), ("calendar", "calendar", nil), ("alarm", "alarm", nil), // icon: Lark card icon tokens
            ("member", "person.2", nil), ("group", "person.2", nil), ("people", "person", nil), ("user", "person", nil), ("contacts", "person.crop.circle", nil), // icon: Lark card icon tokens
            ("link", "link", nil), ("external", "arrow.up.right.square", nil), ("doc", "doc.text", nil), ("file", "doc", nil), ("folder", "folder", nil), // icon: Lark card icon tokens
            ("bell", "bell", nil), ("notice", "bell", nil), ("fire", "flame.fill", .orange), ("star", "star.fill", .yellow), ("like", "hand.thumbsup", nil), // icon: Lark card icon tokens
            ("chat", "bubble.left", nil), ("message", "bubble.left", nil), ("mail", "envelope", nil), ("search", "magnifyingglass", nil), // icon: Lark card icon tokens
            ("setting", "gearshape", nil), ("edit", "pencil", nil), ("delete", "trash", nil), ("add", "plus.circle", nil), ("refresh", "arrow.clockwise", nil), // icon: Lark card icon tokens
            ("down", "chevron.down", nil), ("up", "chevron.up", nil), ("right", "chevron.right", nil), ("left", "chevron.left", nil), // icon: Lark card icon tokens
            ("robot", "sparkles", nil), ("ai", "sparkles", .purple), ("lock", "lock", nil), ("location", "mappin.and.ellipse", nil), // icon: Lark card icon tokens
            ("chart", "chart.bar", nil), ("data", "chart.bar", nil), ("code", "chevron.left.forwardslash.chevron.right", nil), ("bug", "ladybug", .red), // icon: Lark card icon tokens
            ("video", "video", nil), ("phone", "phone", nil), ("image", "photo", nil), ("tag", "tag", nil), ("flag", "flag", nil), ("pin", "pin", nil), // icon: Lark card icon tokens
        ]
        for (k, s, c) in table where t.contains(k) { return (s, c) }
        return nil
    }
}

/// Selects, pickers and inputs, shown as read-only fields with their current value.
struct LarkCardField: View {
    var node: LarkCardNode

    var body: some View {
        let options = node.raw["options"]?.array ?? []
        let selected = options.filter { $0["selected"]?.bool == true }.compactMap { $0["text"]?.string }
        let value = node.raw["value"]?.string
        let shown = !selected.isEmpty ? selected.joined(separator: "、") : (value?.isEmpty == false ? value! : nil)
        let symbol = switch node.tag {
        case "input": "character.cursor.ibeam"
        case "date_picker", "picker_datetime": "calendar"
        case "picker_time": "clock"
        case "select_person", "multi_select_person": "person.crop.circle.badge.plus"
        default: Theme.Icon.choices
        }
        Menu {
            ForEach(Array(options.enumerated()), id: \.offset) { _, o in
                Label(o["text"]?.string ?? o["value"]?.string ?? "", systemImage: o["selected"]?.bool == true ? Theme.Icon.done : "")
            }
        } label: {
            HStack(spacing: Theme.Space.sm) {
                Text(shown ?? (node.text ?? String(localized: "Select [Lark Card da590a]"))).foregroundStyle(shown == nil ? Color(.placeholderText) : Color.primary).lineLimit(1)
                Spacer(minLength: 4)
                Image(systemName: symbol).font(Theme.Font.label).foregroundStyle(Color.secondary)
            }
            .font(Theme.Font.secondary)
            .padding(.horizontal, Theme.Space.m)
            .scaledFrame(minHeight: Theme.Size.wellM)
            .frame(maxWidth: .infinity)
            .background(Color.primary.opacity(0.03), in: .rect(cornerRadius: Theme.Radius.badge))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.badge).strokeBorder(LarkColor.cardStroke))
        }
        .tint(.primary)
        .disabled(options.isEmpty)
        .opacity(node.disabled ? 0.6 : 1)
    }
}

/// `overflow`: the "•••" menu; link options open, callback options press the card action.
struct LarkCardOverflow: View {
    var node: LarkCardNode
    @Environment(\.openURL) private var openURL
    @Environment(\.larkContext) private var context
    @Environment(\.larkRefresh) private var refresh
    @Environment(\.larkAccount) private var larkAccount

    var body: some View {
        let options = (node.raw["options"]?.array ?? []).filter { $0["disabled"]?.bool != true }
        Menu {
            ForEach(Array(options.enumerated()), id: \.offset) { _, o in
                let text = o["text"]?.string ?? o["value"]?.string ?? ""
                if let u = o["url"]?.string.flatMap(LarkPartRenderer.safeURL) {
                    Button { openURL(u) } label: { Label(text, systemImage: Theme.Icon.open) }
                } else if let aid = o["actionId"]?.string, o["actionMethod"]?.int == 2, let mid = context.messageID {
                    Button {
                        Task {
                            do {
                                try await AppModel.shared.api?.lark(larkAccount).larkCardAction(message: mid, action: aid)
                                Haptics.success()
                                AppModel.shared.toast = Toast(String(localized: "Submitted “\(text)”"), style: .success)
                                try? await Task.sleep(for: .seconds(1.2))
                                refresh?()
                            } catch { AppModel.shared.show(error) }
                        }
                    } label: { Text(text) }
                } else {
                    Text(text)
                }
            }
        } label: {
            Image(systemName: Theme.Icon.more)
                .font(Theme.Font.secondaryStrong)
                .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.badge).strokeBorder(LarkColor.cardStroke))
        }
        .foregroundStyle(.primary)
        .accessibilityLabel("More")
    }
}

/// `collapsible_panel`: header toggles the body; children before a `collapse_divider` always show.
struct LarkCardPanel: View {
    var node: LarkCardNode
    @State private var open: Bool

    init(node: LarkCardNode) {
        self.node = node
        _open = State(initialValue: node.raw["expanded"]?.bool ?? false)
    }

    var body: some View {
        let kids = node.children
        let divider = kids.firstIndex { $0.tag == "collapse_divider" }
        let always = divider.map { Array(kids[..<$0]) } ?? []
        let folded = divider.map { Array(kids[($0 + 1)...]) } ?? kids
        let title = (open ? node.node("expandedHeader") : nil) ?? node.node("header")
        let bottom = node.raw["headerPosition"]?.string == "bottom"
        let iconLeft = node.raw["headerIconPosition"]?.string == "left"
        let header = Button {
            withAnimation(Theme.Motion.state) { open.toggle() }
        } label: {
            HStack(spacing: Theme.Space.sm) {
                if iconLeft { chevron }
                Group {
                    if let title { LarkCardFlow(nodes: [title]) } else { Text(node.text ?? String(localized: "Details [Readiness Card 979a33]")).font(Theme.Font.secondary) }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                if !iconLeft { chevron }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .modifier(LarkCardBox(style: node.raw["headerStyle"]))
        VStack(alignment: .leading, spacing: LarkCardView.spacing(node) ?? 8) {
            if !bottom { header }
            if !always.isEmpty { LarkCardFlow(nodes: always) }
            if open && !folded.isEmpty { LarkCardFlow(nodes: folded).transition(.opacity) }
            if bottom { header }
        }
        .clipped()
    }

    private var chevron: some View {
        Image(systemName: Theme.Icon.expand)
            .font(Theme.Font.labelStrong)
            .foregroundStyle(.secondary)
            .rotationEffect(.degrees(open ? 180 : 0))
    }
}

/// Card tables (`table`, markdown tables): header row tinted, cells wrap and every cell in a row is as tall as
/// the tallest; up to three columns share the card width, wider tables scroll sideways.
struct LarkCardTable: View {
    var node: LarkCardNode

    var body: some View {
        let rows = node.children.filter { $0.tag == "markdown_table_row" }
        let columns = rows.map(\.children.count).max() ?? 0
        if columns <= 3 {
            grid(rows, columns: columns, cellWidth: nil)
        } else {
            ScrollView(.horizontal, showsIndicators: false) { grid(rows, columns: columns, cellWidth: 132) }
        }
    }

    private func grid(_ rows: [LarkCardNode], columns: Int, cellWidth: CGFloat?) -> some View {
        VStack(spacing: 0) {
            ForEach(Array(rows.enumerated()), id: \.offset) { ri, row in
                if ri > 0 { Rectangle().fill(LarkColor.cardStroke).frame(height: Theme.Size.line) }
                HStack(spacing: 0) {
                    ForEach(0..<columns, id: \.self) { ci in
                        if ci > 0 { Rectangle().fill(LarkColor.cardStroke).frame(width: Theme.Size.line) }
                        let cell = ci < row.children.count ? row.children[ci] : nil
                        let header = cell?.tag == "markdown_table_header"
                        Group {
                            if let cell { LarkCardFlow(nodes: cell.children.isEmpty ? [cell] : cell.children, inMarkdown: true) } else { Color.clear }
                        }
                        .environment(\.larkTextBase, LarkTextBase(size: Theme.Level.caption.size, bold: header))
                        .padding(.horizontal, Theme.Space.s)
                        .padding(.vertical, Theme.Space.sm)
                        .frame(width: cellWidth)
                        .frame(maxWidth: cellWidth == nil ? .infinity : nil, maxHeight: .infinity, alignment: .topLeading)
                        .background(header ? Color.primary.opacity(0.05) : .clear)
                    }
                }
                .fixedSize(horizontal: false, vertical: true)
            }
        }
        .clipShape(.rect(cornerRadius: Theme.Radius.badge))
        .overlay(RoundedRectangle(cornerRadius: Theme.Radius.badge).strokeBorder(LarkColor.cardStroke))
    }
}

/// Charts in cards (VChart specs): line / bar / area / pie through Swift Charts, a placeholder otherwise.
struct LarkCardChart: View {
    var spec: JSONValue

    private struct Point: Identifiable { let id: Int; let x: String; let y: Double; let series: String }

    private var points: [Point] {
        let data = spec["data"]
        let values = data?.array?.first?["values"]?.array ?? data?["values"]?.array ?? []
        let xf = spec["xField"]?.string ?? spec["categoryField"]?.string ?? "x"
        let yf = spec["yField"]?.string ?? spec["valueField"]?.string ?? "y"
        let sf = spec["seriesField"]?.string
        return values.enumerated().compactMap { i, v in
            guard let y = v[yf]?.double else { return nil }
            return Point(id: i, x: v[xf]?.string ?? "\(i)", y: y, series: sf.flatMap { v[$0]?.string } ?? "")
        }
    }

    var body: some View {
        let type = spec["type"]?.string ?? ""
        let pts = points
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            if let title = spec["title"]?["text"]?.string { Text(title).font(Theme.Font.secondaryStrong) }
            if pts.isEmpty {
                Label("Chart", systemImage: "chart.bar.xaxis").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    .scaledFrame(maxWidth: .infinity, minHeight: Theme.Size.box80).background(Color.primary.opacity(0.04), in: .rect(cornerRadius: Theme.Radius.control))
            } else {
                Chart(pts) { p in
                    switch type {
                    case "pie": SectorMark(angle: .value(String(localized: "Value [Data Chart cda1d5]"), p.y), innerRadius: .ratio(0.5)).foregroundStyle(by: .value(String(localized: "Category"), p.x))
                    case "line": LineMark(x: .value("X", p.x), y: .value("Y", p.y)).foregroundStyle(by: .value(String(localized: "Series"), p.series))
                    case "area": AreaMark(x: .value("X", p.x), y: .value("Y", p.y)).foregroundStyle(by: .value(String(localized: "Series"), p.series))
                    default: BarMark(x: .value("X", p.x), y: .value("Y", p.y)).foregroundStyle(by: .value(String(localized: "Series"), p.series))
                    }
                }
                .scaledFrame(height: Theme.Size.box180)
            }
        }
    }
}
