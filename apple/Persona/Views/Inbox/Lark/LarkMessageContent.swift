import MarkdownUI
import PersonaKit
import SwiftUI

/// What a message renderer needs from the chat around it.
struct LarkRenderContext {
    var messageID: String?
    /// Every picture in the message, so tapping one pages through all of them.
    var images: [String] = []
}

extension EnvironmentValues {
    @Entry var larkContext = LarkRenderContext()
    /// Opens a Feishu file (url, name) in Quick Look; set by the chat view.
    @Entry var larkOpenFile: ((String, String) -> Void)? = nil
    /// Reloads the chat after a card button changed something.
    @Entry var larkRefresh: (() -> Void)? = nil
}

extension MarkdownUI.Theme {
    /// Markdown messages inside a chat bubble: body size, tight paragraphs.
    @MainActor static var larkMessage: MarkdownUI.Theme { larkMessageAtScale.value }
    @MainActor private static let larkMessageAtScale = PerTextScale {
        MarkdownUI.Theme.persona
            .text { FontSize(Theme.Level.body.size * Theme.Font.scale) } // the body level, as Lark messages are (LarkTextBase)
            .link { ForegroundColor(LarkColor.blue) }
            .paragraph { config in
                config.label.fixedSize(horizontal: false, vertical: true).relativeLineSpacing(.em(0.15)).markdownMargin(top: 0, bottom: 6)
            }
    }
}

// MARK: - Rich-text tree

/// The Lark gateway records each leaf's block ancestors; consecutive leaves sharing an ancestor share the block
/// (port of `rich-blocks.ts`).
indirect enum LarkRichNode {
    case leaf(LarkPart)
    case block(LarkRichBlock, [LarkRichNode])

    static func tree(_ parts: [LarkPart]) -> [LarkRichNode] {
        final class Box { let block: LarkRichBlock; var children: [Any] = []; init(_ b: LarkRichBlock) { block = b } }
        var root: [Any] = []
        var open: [Box] = []
        func add(_ x: Any) { if let top = open.last { top.children.append(x) } else { root.append(x) } }
        for part in parts {
            let path = part.blocks
            var shared = 0
            while shared < open.count, shared < path.count, open[shared].block.id == path[shared].id { shared += 1 }
            open.removeSubrange(shared...)
            for b in path.dropFirst(shared) {
                let box = Box(b)
                add(box)
                open.append(box)
            }
            add(part)
        }
        func convert(_ items: [Any]) -> [LarkRichNode] {
            items.map { item in
                if let box = item as? Box { return .block(box.block, convert(box.children)) }
                return .leaf(item as! LarkPart)
            }
        }
        return adoptNestedLists(convert(root))
    }

    /// Feishu nests a sub-list as a sibling of the items (`OL > LI, OL, LI`); move it into the item before it.
    static func adoptNestedLists(_ nodes: [LarkRichNode]) -> [LarkRichNode] {
        nodes.map { node in
            guard case .block(let b, let children) = node else { return node }
            let inner = adoptNestedLists(children)
            guard b.type.hasSuffix("-list") else { return .block(b, inner) }
            var out: [LarkRichNode] = []
            for child in inner {
                if case .block(let cb, _) = child, cb.type.hasSuffix("-list"), case .block(let pb, let pc)? = out.last, pb.type == "list-item" {
                    out[out.count - 1] = .block(pb, pc + [child])
                } else {
                    out.append(child)
                }
            }
            return .block(b, out)
        }
    }
}

// MARK: - Parts

/// The body of one Feishu message: rich text, pictures, stickers, files, video, code and cards.
struct LarkPartsView: View {
    var parts: [LarkPart]
    var base = LarkTextBase()

    var body: some View {
        if parts.contains(where: { !$0.blocks.isEmpty }) {
            LarkNodesView(nodes: LarkRichNode.tree(parts), structured: true, base: base)
        } else {
            LarkNodesView(nodes: parts.map { .leaf($0) }, structured: false, base: base)
        }
    }

    /// Parts that draw their own chrome (cards, pictures, stickers, files, video) and need no bubble.
    static func isBare(_ parts: [LarkPart]) -> Bool {
        let visible = parts.filter { !($0.kind == "text" && ($0.text ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty) }
        guard !visible.isEmpty else { return false }
        if visible.count == 1, let p = visible.first {
            return ["card", "image", "file", "media"].contains(p.kind) || (p.kind == "emotion" && p.url != nil)
                || (p.kind == "link" && LarkMoments.postID(p.url) != nil)
        }
        return visible.allSatisfy { $0.kind == "card" }
    }

    static func imageURLs(_ parts: [LarkPart]) -> [String] { parts.filter { $0.kind == "image" }.compactMap(\.url) }
}

/// Lays out rich nodes: consecutive inline leaves become one wrapped `Text`, everything else stacks.
struct LarkNodesView: View {
    var nodes: [LarkRichNode]
    var structured: Bool
    var base: LarkTextBase

    private enum Chunk {
        case inline([LarkInline])
        case node(LarkRichNode)
    }

    private var chunks: [Chunk] {
        var out: [Chunk] = []
        var runs: [LarkInline] = []
        func flush() {
            while case .text(let s, _)? = runs.last, s == "\n" { runs.removeLast() }
            if !runs.isEmpty { out.append(.inline(runs)) }
            runs = []
        }
        for node in nodes {
            if case .leaf(let p) = node, LarkPartRenderer.isInline(p) {
                if !structured, p.metaBool("blockStart"), p.metaString("block") == "list-item" { runs.append(.text("•  ", LarkRunStyle())) }
                runs += LarkPartRenderer.runs(p, structured: structured)
                if !structured, p.metaBool("blockEnd") { runs.append(.text("\n", LarkRunStyle())) }
            } else {
                flush()
                out.append(.node(node))
            }
        }
        flush()
        return out
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.sm) {
            ForEach(Array(chunks.enumerated()), id: \.offset) { _, chunk in
                switch chunk {
                case .inline(let runs): LarkInlineText(runs: runs, base: base)
                case .node(let n): LarkNodeView(node: n, base: base)
                }
            }
        }
    }
}

struct LarkNodeView: View {
    var node: LarkRichNode
    var base: LarkTextBase
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        switch node {
        case .leaf(let p): LarkBlockPartView(part: p)
        case .block(let b, let children): block(b, children)
        }
    }

    private func block(_ b: LarkRichBlock, _ children: [LarkRichNode]) -> AnyView {
        switch b.type {
        case "ordered-list", "unordered-list":
            let items = children
            return AnyView(VStack(alignment: .leading, spacing: Theme.Space.xs) {
                ForEach(Array(items.enumerated()), id: \.offset) { i, item in
                    HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                        Text(Self.marker(b, index: i)).font(.system(size: base.size * Theme.textScale(typeSize)).monospacedDigit()).foregroundStyle(.secondary)
                            .scaledFrame(minWidth: Theme.Size.markS, alignment: .trailing)
                        LarkNodeView(node: item, base: base)
                    }
                }
            })
        case "list-item", "paragraph":
            return AnyView(LarkNodesView(nodes: children, structured: true, base: base))
        case "quote":
            return AnyView(LarkNodesView(nodes: children, structured: true, base: LarkTextBase(size: base.size, color: .secondary))
                .padding(.leading, Theme.Space.m)
                .overlay(alignment: .leading) { Capsule().fill(Color.primary.opacity(0.18)).frame(width: Theme.Size.bar) })
        case let t where t.hasPrefix("heading-"):
            let level = Int(t.dropFirst(8)) ?? 3
            let size = level == 1 ? Theme.Level.title.size : Theme.Level.heading.size // as in a chat's Markdown
            return AnyView(LarkNodesView(nodes: children, structured: true, base: LarkTextBase(size: size, bold: true)))
        case "code-block":
            return AnyView(LarkNodesView(nodes: children, structured: true, base: base))
        default:
            return AnyView(LarkNodesView(nodes: children, structured: true, base: base))
        }
    }

    static func marker(_ b: LarkRichBlock, index: Int) -> String {
        guard b.type == "ordered-list" else { return ["•", "◦", "▪︎", ""][min(b.listType, 3)] }
        let n = b.start + index
        switch b.listType {
        case 1: return (n <= 26 ? String(UnicodeScalar(96 + n).map(Character.init) ?? "?") : "\(n)") + "."
        case 2: return (n <= 26 ? String(UnicodeScalar(64 + n).map(Character.init) ?? "?") : "\(n)") + "."
        case 3: return roman(n).lowercased() + "."
        case 4: return roman(n) + "."
        default: return "\(n)."
        }
    }

    static func roman(_ n: Int) -> String {
        let table = [(1000, "M"), (900, "CM"), (500, "D"), (400, "CD"), (100, "C"), (90, "XC"), (50, "L"), (40, "XL"), (10, "X"), (9, "IX"), (5, "V"), (4, "IV"), (1, "I")]
        var n = n, out = ""
        for (v, s) in table { while n >= v { out += s; n -= v } }
        return out
    }
}

nonisolated enum LarkPartRenderer {
    static func isInline(_ p: LarkPart) -> Bool {
        switch p.kind {
        case "text": return !p.metaBool("markdown") && !p.metaBool("system")
        case "link": return LarkMoments.postID(p.url) == nil
        case "at", "docs", "table": return true
        case "emotion": return p.url == nil
        case "interactive": return p.meta("richTag")?.int != 32
        default: return false
        }
    }

    static func marks(_ p: LarkPart) -> LarkRunStyle {
        var s = LarkRunStyle()
        let marks = Set((p.meta("marks")?.array ?? []).compactMap(\.string))
        let legacy = p.meta("style")?.int
        s.bold = marks.contains("bold") || legacy == 7
        s.italic = marks.contains("italic") || legacy == 8
        s.underline = marks.contains("underline") || legacy == 9
        s.strike = marks.contains("strikethrough")
        s.code = p.metaBool("inlineCode")
        if p.metaString("diff") == "added" { s.background = Color.green.opacity(0.15) }
        if p.metaString("diff") == "removed" { s.strike = true; s.color = .secondary }
        return s
    }

    static func runs(_ p: LarkPart, structured: Bool) -> [LarkInline] {
        let style = marks(p)
        switch p.kind {
        case "text":
            if structured && p.metaBool("emptyLine") { return [.text("\n", style)] }
            return lingoRuns(p.text ?? "", lingo: p.meta("lingo")?.array ?? [], style: style)
        case "at":
            var s = style
            s.color = LarkColor.blue
            return [.text(p.text ?? "@", s)]
        case "link":
            var s = style
            s.link = p.url.flatMap(safeURL)
            return [.text(p.text ?? p.url ?? "", s)]
        case "docs":
            var s = style
            s.link = p.url.flatMap(safeURL)
            return [.symbol(Theme.Icon.document, LarkColor.blue), .text(" " + (p.text ?? String(localized: "Document")), s)]
        case "emotion":
            let key = p.refId ?? p.text ?? ""
            let label = LarkMedia.labelFallback(key, text: p.text)
            return [.emoji(key: key, fallback: label)]
        default:
            return [.text(p.text ?? "", style)]
        }
    }

    /// Enterprise glossary terms (`lingo`, UTF-16 offsets) get a dotted underline, as in Feishu.
    static func lingoRuns(_ text: String, lingo: [JSONValue], style: LarkRunStyle) -> [LarkInline] {
        let ns = text as NSString
        let ranges = lingo.compactMap { l -> NSRange? in
            guard let a = l["start"]?.int, let b = l["end"]?.int, a >= 0, b > a, b <= ns.length else { return nil }
            return NSRange(location: a, length: b - a)
        }.sorted { $0.location < $1.location }
        guard !ranges.isEmpty else { return [.text(text, style)] }
        var out: [LarkInline] = []
        var last = 0
        var term = style
        term.dotted = true
        for r in ranges where r.location >= last {
            if r.location > last { out.append(.text(ns.substring(with: NSRange(location: last, length: r.location - last)), style)) }
            out.append(.text(ns.substring(with: r), term))
            last = r.location + r.length
        }
        if last < ns.length { out.append(.text(ns.substring(from: last), style)) }
        return out
    }

    static func safeURL(_ s: String) -> URL? {
        guard let u = URL(string: s), let scheme = u.scheme?.lowercased(), ["http", "https", "mailto"].contains(scheme) else { return nil }
        return u
    }
}

extension LarkMedia {
    /// `[Smile]`-style text for an emoji whose picture is not loaded.
    nonisolated static func labelFallback(_ key: String, text: String?) -> String {
        if let text, !text.isEmpty { return text }
        var k = key
        if k.hasPrefix("Lark_Emoji_") { k = String(k.dropFirst(11)) }
        if let us = k.lastIndex(of: "_") { k = String(k[..<us]) }
        return "[\(k)]"
    }
}

// MARK: - Block parts

/// A part that is not inline text: picture, sticker, file, video / voice, code, card, markdown, system line.
struct LarkBlockPartView: View {
    var part: LarkPart

    var body: some View {
        switch part.kind {
        case "image": LarkImagePart(part: part)
        case "emotion": LarkSticker(part: part)
        case "file": LarkFileCard(part: part)
        case "media":
            if (part.metaString("mime") ?? "").hasPrefix("audio/") { LarkVoicePart(part: part) } else { LarkVideoPart(part: part) }
        case "code": LarkCodeBlock(code: part.text ?? "", language: part.metaString("languageName") ?? part.metaString("language"))
        case "card": LarkCardView(part: part)
        case "link": LarkMomentsCard(part: part)
        case "interactive": Divider()
        case "text":
            if part.metaBool("system") {
                LarkSystemText(part: part)
            } else {
                Markdown(Links.markdown(part.text ?? "")).markdownTheme(.larkMessage).textSelection(.enabled)
            }
        default:
            Text(part.text ?? "[\(part.kind)]").foregroundStyle(.secondary)
        }
    }
}

/// Group events ("<name> created the topic group"): the people named in the template stand out.
struct LarkSystemText: View {
    var part: LarkPart

    var body: some View {
        LarkInlineText(runs: Self.runs(part), base: LarkTextBase(size: Theme.Level.caption.size, color: .secondary), alignment: .center)
    }

    static func runs(_ part: LarkPart) -> [LarkInline] {
        guard let template = part.metaString("systemTemplate"), template.contains("{") else { return [.text(part.text ?? "", LarkRunStyle())] }
        var holders: [String: String] = [:]
        for h in part.meta("systemHolders")?.array ?? [] {
            if let k = h["key"]?.string { holders[k] = h["text"]?.string ?? "" }
        }
        var out: [LarkInline] = []
        var rest = Substring(template)
        while let open = rest.firstIndex(of: "{"), let close = rest[open...].firstIndex(of: "}") {
            if open > rest.startIndex { out.append(.text(String(rest[..<open]), LarkRunStyle())) }
            let key = String(rest[rest.index(after: open)..<close])
            var s = LarkRunStyle()
            s.color = .primary.opacity(0.75)
            out.append(.text(holders[key] ?? "", s))
            rest = rest[rest.index(after: close)...]
        }
        if !rest.isEmpty { out.append(.text(String(rest), LarkRunStyle())) }
        return out
    }
}

/// A picture: its own aspect ratio inside 240 × 300 pt, tap for the full-screen gallery.
struct LarkImagePart: View {
    var part: LarkPart
    @Environment(\.larkContext) private var context
    @State private var viewing = false
    @Namespace private var zoom

    var body: some View {
        let w = part.meta("width")?.double ?? 0, h = part.meta("height")?.double ?? 0
        let size = Self.displaySize(width: w, height: h)
        if let url = part.url {
            LarkRemoteImage(url: url, maxPixel: 900)
                .frame(width: size.width, height: size.height)
                .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: Theme.Radius.control, style: .continuous).strokeBorder(Color.primary.opacity(0.06)))
                .contentShape(.rect)
                .matchedTransitionSource(id: url, in: zoom)
                .onTapGesture { viewing = true }
                .fullScreenCover(isPresented: $viewing) {
                    let all = context.images.isEmpty ? [url] : context.images
                    ImageViewer(urls: all, index: all.firstIndex(of: url) ?? 0)
                        .navigationTransition(.zoom(sourceID: url, in: zoom))
                }
                .accessibilityLabel("images")
        } else {
            Label("Image expired", systemImage: Theme.Icon.photo).font(Theme.Font.secondary).foregroundStyle(.secondary)
        }
    }

    static func displaySize(width: Double, height: Double, maxW: Double = 240, maxH: Double = 300) -> CGSize {
        guard width > 0, height > 0 else { return CGSize(width: 200, height: 150) }
        let s = min(maxW / width, maxH / height, 1)
        let out = CGSize(width: max(width * s, 60), height: max(height * s, 40))
        // Very small images (icons) keep a tappable minimum but are not blown up.
        return out
    }
}

/// A sticker at its own aspect ratio inside a 120 pt box (no blank band under wide ones).
struct LarkSticker: View {
    var part: LarkPart
    @State private var image: UIImage?

    var body: some View {
        if let url = part.url {
            Group {
                if let image {
                    let r = min(120 / max(image.size.width, 1), 120 / max(image.size.height, 1))
                    Image(uiImage: image).resizable().aspectRatio(contentMode: .fit)
                        .frame(width: image.size.width * r, height: image.size.height * r)
                } else {
                    RoundedRectangle(cornerRadius: Theme.Radius.control).fill(Color.primary.opacity(0.05)).scaledFrame(width: Theme.Size.box80, height: Theme.Size.box80)
                }
            }
            .accessibilityLabel(part.text ?? String(localized: "Sticker"))
            .task(id: url) { image = await LarkMedia.shared.image(url, maxPixel: 400) }
        }
    }
}

/// A file: type badge, name, size; tap downloads and opens it.
struct LarkFileCard: View {
    var part: LarkPart
    @Environment(\.larkOpenFile) private var openFile

    var body: some View {
        let name = part.text ?? String(localized: "Files")
        let ext = (part.metaString("extension") ?? (name as NSString).pathExtension).uppercased()
        Button {
            if let url = part.url { openFile?(url, name) }
        } label: {
            HStack(spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Text(name).font(Theme.Font.secondaryMedium).foregroundStyle(.primary).lineLimit(2).multilineTextAlignment(.leading)
                    Text(LarkFormat.fileSize(part.metaString("size"))).font(Theme.Font.label).foregroundStyle(.secondary)
                }
                Spacer(minLength: 8)
                Self.badge(ext)
            }
            .padding(Theme.Space.m)
            .scaledFrame(width: Theme.Size.box260)
            .background(LarkColor.cardBackground, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous).strokeBorder(LarkColor.cardStroke))
        }
        .buttonStyle(.plain)
        .disabled(part.url == nil || part.metaBool("destroyed"))
    }

    static func badge(_ ext: String) -> some View {
        let kind = FeishuDocsFileKind.symbol(ext: ext, title: "")
        let (symbol, color) = (kind.symbol, LarkColor.hue(kind.hue))
        return Image(systemName: symbol)
            .font(.system(size: 20)) // type-scale: glyph (in a 42 pt tile)
            .foregroundStyle(.white)
            .scaledFrame(width: Theme.Size.avatarM, height: Theme.Size.avatarM)
            .background(color.gradient, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }
}

/// A video: its poster with a play button and the duration; tap plays full screen.
struct LarkVideoPart: View {
    var part: LarkPart
    @State private var playing = false

    var body: some View {
        let size = LarkImagePart.displaySize(width: part.meta("posterWidth")?.double ?? 0, height: part.meta("posterHeight")?.double ?? 0)
        ZStack {
            if let poster = part.metaString("posterUrl") {
                LarkRemoteImage(url: poster, maxPixel: 900)
            } else {
                Rectangle().fill(Color.black.opacity(0.85))
            }
            Image(systemName: Theme.Icon.run)
                .font(.system(size: 20)) // type-scale: glyph (in a 48 pt play button)
                .foregroundStyle(.white)
                .scaledFrame(width: Theme.Size.avatarXL, height: Theme.Size.avatarXL)
                .background(.black.opacity(0.45), in: .circle)
        }
        .frame(width: size.width, height: size.height)
        .overlay(alignment: .bottomTrailing) {
            Text(LarkFormat.duration(ms: part.meta("duration")?.double))
                .font(Theme.Font.labelStrong.monospacedDigit())
                .foregroundStyle(.white)
                .padding(.horizontal, Theme.Space.sm).padding(.vertical, Theme.Space.xxs)
                .background(.black.opacity(0.5), in: .capsule)
                .padding(Theme.Space.sm)
        }
        .clipShape(.rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .contentShape(.rect)
        .onTapGesture { if part.url != nil { playing = true } }
        .fullScreenCover(isPresented: $playing) { if let url = part.url { LarkPlayerView(url: url) } }
        .accessibilityLabel("Video \(part.text ?? "")")
    }
}

/// A voice message: play glyph, waveform, duration.
struct LarkVoicePart: View {
    var part: LarkPart
    @State private var playing = false

    var body: some View {
        let ms = part.meta("duration")?.double ?? 0
        Button { playing = true } label: {
            HStack(spacing: Theme.Space.s) {
                Image(systemName: Theme.Icon.playMedia).font(Theme.Font.title).foregroundStyle(LarkColor.blue)
                HStack(spacing: Theme.Space.xxs) {
                    ForEach(0..<min(18, max(5, Int(ms / 1500))), id: \.self) { i in
                        Capsule().fill(Color.primary.opacity(0.35)).frame(width: Theme.Size.ruleWide, height: [8, 14, 10, 18, 12, 6][i % 6])
                    }
                }
                Text(LarkFormat.duration(ms: ms)).font(Theme.Font.secondary.monospacedDigit()).foregroundStyle(.secondary)
            }
        }
        .buttonStyle(.plain)
        .sheet(isPresented: $playing) { if let url = part.url { LarkPlayerView(url: url).presentationDetents([.medium]) } }
    }
}

/// A code block: language label, copy button, light syntax colors, horizontal scroll.
struct LarkCodeBlock: View {
    var code: String
    var language: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text(language?.isEmpty == false ? language! : String(localized: "Code")).font(Theme.Font.labelMedium).foregroundStyle(.secondary)
                Spacer()
                Button {
                    UIPasteboard.general.string = code
                    Haptics.success()
                    AppModel.shared.toast = Toast(String(localized: "Code copied"), style: .success)
                } label: { Image(systemName: Theme.Icon.copy).font(Theme.Font.label) }
                    .buttonStyle(.plain)
                    .foregroundStyle(.secondary)
                    .accessibilityLabel("Copy Code [Message Views fa2611]")
            }
            .padding(.horizontal, Theme.Space.m).padding(.top, Theme.Space.s).padding(.bottom, Theme.Space.xs)
            ScrollView(.horizontal, showsIndicators: false) {
                Text(LarkSyntax.highlight(code))
                    .font(Theme.Font.code)
                    .fixedSize(horizontal: true, vertical: true)
                    .textSelection(.enabled)
                    .padding(.horizontal, Theme.Space.m).padding(.bottom, Theme.Space.m)
            }
        }
        .background(Theme.codeBackground, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }
}

/// Just enough highlighting to read code at a glance: comments, strings, numbers, common keywords.
nonisolated enum LarkSyntax {
    static let keywords: Set<String> = ["func", "function", "def", "class", "struct", "enum", "interface", "type", "return", "if", "else", "elif",
        "for", "while", "switch", "case", "default", "break", "continue", "var", "let", "const", "import", "from", "package", "public",
        "private", "static", "async", "await", "try", "catch", "throw", "throws", "new", "nil", "null", "None", "true", "false", "True",
        "False", "self", "this", "in", "go", "defer", "select", "chan", "map", "range", "fn", "impl", "pub", "use", "mod", "match",
        "export", "extends", "implements", "guard", "where", "with", "as", "lambda", "yield", "echo", "then", "fi", "do", "done"]

    static func highlight(_ code: String) -> AttributedString {
        var out = AttributedString()
        let comment = Color(light: 0x8A8F98, dark: 0x7D8590), string = Color(light: 0x0A7B3E, dark: 0x7EE787)
        let number = Color(light: 0xB35900, dark: 0xFFA657), keyword = Color(light: 0xA626A4, dark: 0xFF7B9C)
        for (li, line) in code.components(separatedBy: "\n").enumerated() {
            if li > 0 { out += AttributedString("\n") }
            var i = line.startIndex
            while i < line.endIndex {
                let c = line[i]
                let rest = line[i...]
                if rest.hasPrefix("//") || (c == "#" && (i == line.startIndex || line[line.index(before: i)] == " ")) || rest.hasPrefix("--") {
                    var a = AttributedString(String(rest)); a.foregroundColor = comment; out += a; break
                }
                if c == "\"" || c == "'" || c == "`" {
                    var j = line.index(after: i)
                    while j < line.endIndex, line[j] != c { if line[j] == "\\" { j = line.index(after: j); if j == line.endIndex { break } }; j = line.index(after: j) }
                    let end = j < line.endIndex ? line.index(after: j) : j
                    var a = AttributedString(String(line[i..<end])); a.foregroundColor = string; out += a; i = end; continue
                }
                if c.isLetter || c == "_" {
                    var j = i
                    while j < line.endIndex, line[j].isLetter || line[j].isNumber || line[j] == "_" { j = line.index(after: j) }
                    let word = String(line[i..<j])
                    var a = AttributedString(word)
                    if keywords.contains(word) { a.foregroundColor = keyword }
                    out += a; i = j; continue
                }
                if c.isNumber {
                    var j = i
                    while j < line.endIndex, line[j].isNumber || line[j] == "." || line[j] == "x" || line[j].isHexDigit { j = line.index(after: j) }
                    var a = AttributedString(String(line[i..<j])); a.foregroundColor = number; out += a; i = j; continue
                }
                out += AttributedString(String(c))
                i = line.index(after: i)
            }
        }
        return out
    }
}
