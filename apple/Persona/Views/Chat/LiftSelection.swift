import MarkdownUI
import SwiftUI
#if os(iOS) || os(visionOS)
import UIKit
#endif

// MARK: - A lifted message's words, selected where they are (#603)

// A second long press on a lifted message, or Select Text in its bar, selects its words where they are, the system's
// way: handles to drag, the edit menu with Copy, Quote and Look Up. The chat draws words with MarkdownUI, which cannot
// select a range on iOS, so while they are selected they are drawn again by a text view laid out like them
// (`ReplyText`, with the chat's spacing from `ReplyMetrics`): code on its background, quotes behind their bar, lists
// with their markers, and tables as the chat draws them (a picture that copies as its Markdown). The rest of the
// message (its bubble or card, what it quotes, its pictures) stays as it was. The bar steps aside for the edit menu;
// a tap beside the words, or one that clears the selection, brings it back; Copy and Quote close the whole thing, as
// the bar's own buttons do. The Mac selects in the chat itself and keeps its Select Text window.

/// The spacing of a reply's Markdown in the chat (`MarkdownUI.Theme.persona`), also used to lay out the same words for
/// selecting (`ReplyText`). em values are times the text size.
nonisolated enum ReplyMetrics {
    /// Between a paragraph's lines, em (the body's line height, `Theme.Level.body`).
    static let lineSpacing: CGFloat = 0.26
    /// Under a paragraph.
    static let paragraphBottom: CGFloat = 10
    /// The first heading level: its size (em) and the space above and below it.
    static let heading1 = (size: CGFloat(1.3), top: CGFloat(16), bottom: CGFloat(8))
    /// The second level, at the text's size.
    static let heading2 = (top: CGFloat(14), bottom: CGFloat(6))
    /// The rest.
    static let heading3 = (top: CGFloat(12), bottom: CGFloat(4))
    /// Above a list item, em.
    static let itemTop: CGFloat = 0.2
    /// Around a code block.
    static let codeTop: CGFloat = 4
    static let codeBottom: CGFloat = 12
    /// Between a code block's lines, em of its own size.
    static let codeLineSpacing: CGFloat = 0.2
    /// Inline code, and an agent's words between its steps (`QuietMarkdown`), em.
    static let inlineCode: CGFloat = 0.88
    static let quiet: CGFloat = 0.88
}

/// What starts selecting a lifted message's words: a long press on them at a point (in the window), or Select Text
/// (all of them).
struct InPlaceSelect: Equatable {
    var at: CGPoint?
    var id = UUID()
}

extension EnvironmentValues {
    /// Set on the lifted copy of a message while its words are selected (`MessageLiftLayer`); nil everywhere else.
    @Entry var liftSelect: InPlaceSelect? = nil
}

/// How a message's words are drawn in the chat, for the text view that stands in for them.
enum LiftWords: Equatable {
    /// Your own message, as typed, in its bubble's text color.
    case plain(Color)
    /// A reply's Markdown; quiet: an agent's words between its steps (smaller and gray).
    case reply(quiet: Bool)
}

extension View {
    /// Words a long press lifts (#603): in the lifted copy, while they are selected, a text view laid out like them in
    /// which any part can be selected and copied. Everywhere else they are drawn as they are.
    func selectableInLift(_ text: String, as words: LiftWords) -> some View {
        modifier(SelectableInLift(text: text, words: words))
    }
}

private struct SelectableInLift: ViewModifier {
    @Environment(\.liftSelect) private var select
    @Environment(MessageMenu.self) private var menu: MessageMenu?
    var text: String
    var words: LiftWords

    func body(content: Content) -> some View {
        #if os(iOS) || os(visionOS)
        if let select, let menu {
            LiftTextView(text: text, words: words, select: select, menu: menu)
        } else {
            content
        }
        #else
        content
        #endif
    }
}

#if os(iOS) || os(visionOS)
#if os(iOS)
/// A long press on the lifted message: where it landed, in the window, for its words to be selected there.
/// iOS only: `UIGestureRecognizerRepresentable` is not available on visionOS.
struct LiftPress: UIGestureRecognizerRepresentable {
    var enabled: Bool
    var action: (CGPoint) -> Void

    func makeUIGestureRecognizer(context: Context) -> UILongPressGestureRecognizer {
        let g = UILongPressGestureRecognizer()
        g.minimumPressDuration = 0.35
        return g
    }

    func updateUIGestureRecognizer(_ recognizer: UILongPressGestureRecognizer, context: Context) {
        recognizer.isEnabled = enabled
    }

    func handleUIGestureRecognizerAction(_ recognizer: UILongPressGestureRecognizer, context: Context) {
        if recognizer.state == .began { action(recognizer.location(in: nil)) }
    }
}
#endif

private struct LiftTextView: UIViewRepresentable {
    var text: String
    var words: LiftWords
    var select: InPlaceSelect
    var menu: MessageMenu

    func makeUIView(context: Context) -> LiftUITextView { LiftUITextView() }

    func updateUIView(_ v: LiftUITextView, context: Context) {
        let menu = menu
        v.show(text, words)
        if let bar = menu.lift?.bar,
           let quote = menu.selectionQuote(of: SelectableText(text: bar.text, plain: bar.isUser, msg: bar.id, quotable: !bar.pending)) {
            v.quote = { picked in
                quote(picked)
                menu.close()
            }
        } else {
            v.quote = nil
        }
        v.ended = { menu.endSelection() }
        v.copied = { menu.close() }
        v.begin(select)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: LiftUITextView, context: Context) -> CGSize? {
        guard let width = proposal.width, width.isFinite, width > 0 else { return nil }
        return CGSize(width: width, height: uiView.height(for: width))
    }
}

/// The words in a read-only text view, laid out by `ReplyLayout` (TextKit 1, which draws what is behind the blocks).
final class LiftUITextView: UITextView, UITextViewDelegate, UIEditMenuInteractionDelegate {
    private let storage = NSTextStorage()
    private let layout = ReplyLayout()
    /// What is behind the words' blocks, under the selection's highlight: drawn with the words, a code block's
    /// background covered the highlight and a selected block looked unselected.
    private let behind = ReplyBlocksView()
    /// The menu shown over the words a press picked. The text view's own menu only comes up on its own gestures, and
    /// its interaction is not among `interactions`; this one shows the same items.
    private lazy var menuInteraction = UIEditMenuInteraction(delegate: self)
    private var source: (text: String, words: LiftWords)?
    /// The width the words were laid out for (a table's picture and columns depend on it).
    private var laidOut: CGFloat = 0
    private var begun: UUID?
    /// Something is selected: when that goes, the selecting is over.
    private var picked = false
    var quote: ((String) -> Void)?
    var ended: () -> Void = {}
    var copied: () -> Void = {}

    init() {
        let container = NSTextContainer(size: CGSize(width: 0, height: CGFloat.greatestFiniteMagnitude))
        container.widthTracksTextView = true
        container.lineFragmentPadding = 0
        layout.addTextContainer(container)
        storage.addLayoutManager(layout)
        super.init(frame: .zero, textContainer: container)
        isEditable = false
        isSelectable = true
        isScrollEnabled = false
        textContainerInset = .zero
        backgroundColor = .clear
        delegate = self
        accessibilityIdentifier = "lift-text"
        behind.layout = layout
        behind.isUserInteractionEnabled = false
        behind.backgroundColor = .clear
        behind.layer.zPosition = -1 // under every other layer of the text view, the highlight's too
        insertSubview(behind, at: 0)
        addInteraction(menuInteraction)
        registerForTraitChanges([UITraitPreferredContentSizeCategory.self, UITraitUserInterfaceStyle.self]) { (v: LiftUITextView, _) in
            v.render(width: v.laidOut)
        }
    }

    required init?(coder: NSCoder) { fatalError("not from a nib") }

    func show(_ text: String, _ words: LiftWords) {
        guard source?.text != text || source?.words != words else { return }
        source = (text, words)
        render(width: laidOut)
    }

    private func render(width: CGFloat) {
        guard let source else { return }
        laidOut = width
        let look = ReplyText.Look(source.words, traits: traitCollection)
        attributedText = switch source.words {
        case .plain: ReplyText.plain(source.text, look: look)
        case .reply: ReplyText.attributed(source.text, look: look, width: width)
        }
        behind.setNeedsDisplay()
    }

    func height(for width: CGFloat) -> CGFloat {
        if abs(width - laidOut) > 0.5 { render(width: width) }
        return ceil(sizeThatFits(CGSize(width: width, height: .greatestFiniteMagnitude)).height)
    }

    override func layoutSubviews() {
        if bounds.width > 0, abs(bounds.width - laidOut) > 0.5, !picked { render(width: bounds.width) }
        super.layoutSubviews()
        let frame = CGRect(x: 0, y: 0, width: bounds.width, height: max(bounds.height, contentSize.height))
        if behind.frame != frame {
            behind.frame = frame
            behind.setNeedsDisplay()
        }
    }

    /// The selection's highlight, the same on every line: a line it covers to its end reaches across the whole width,
    /// a table's line and the last line as much as a paragraph's (the system's stopped at a table's edge and at the
    /// last word). Where the selection starts or ends inside a line, the highlight does too; the end handle stays after
    /// the last selected word.
    override func selectionRects(for range: UITextRange) -> [UITextSelectionRect] {
        let start = offset(from: beginningOfDocument, to: range.start), end = offset(from: beginningOfDocument, to: range.end)
        let text = storage.string as NSString
        guard end > start, end <= text.length, let container = layout.textContainers.first else { return super.selectionRects(for: range) }
        let inset = textContainerInset, right = inset.left + container.size.width
        var out: [LineSelectionRect] = []
        let glyphs = layout.glyphRange(forCharacterRange: NSRange(location: start, length: end - start), actualCharacterRange: nil)
        layout.enumerateLineFragments(forGlyphRange: glyphs) { line, used, _, lineGlyphs, _ in
            let chars = self.layout.characterRange(forGlyphRange: lineGlyphs, actualGlyphRange: nil)
            let lineEnd = NSMaxRange(chars)
            let from = max(chars.location, start), to = min(lineEnd, end)
            guard to > from else { return }
            // Where the words of this line end (before its line break), and where a character of it starts.
            let wordsEnd = lineEnd > chars.location && text.character(at: lineEnd - 1) == 0x0A ? lineEnd - 1 : lineEnd
            func x(_ at: Int) -> CGFloat {
                guard at < wordsEnd else { return inset.left + used.maxX }
                return inset.left + line.minX + self.layout.location(forGlyphAt: self.layout.glyphIndexForCharacter(at: at)).x
            }
            let top = inset.top + line.minY, height = line.height
            let left = from == chars.location ? inset.left : x(from)
            func add(_ a: CGFloat, _ b: CGFloat, start: Bool, end: Bool) {
                out.append(LineSelectionRect(CGRect(x: a, y: top, width: max(0, b - a), height: height), start: start, end: end))
            }
            if to < end {
                add(left, right, start: from == start, end: false) // it goes on below: the line to its end
            } else if to == lineEnd {
                // it ends with the line: across the whole width, the end handle after the line's last word
                let at = x(to)
                add(left, at, start: from == start, end: true)
                if at < right { add(at, right, start: false, end: false) }
            } else {
                add(left, x(to), start: from == start, end: true)
            }
        }
        return out.isEmpty ? super.selectionRects(for: range) : out
    }

    /// Selects the word at the press (or everything) once the view is in its window, and shows the edit menu.
    func begin(_ select: InPlaceSelect) {
        guard begun != select.id else { return }
        begun = select.id
        picked = false
        start(select.at, tries: 3)
    }

    private func start(_ at: CGPoint?, tries: Int) {
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            guard window != nil, bounds.width > 0 else {
                if tries > 0 { start(at, tries: tries - 1) }
                return
            }
            layoutIfNeeded()
            guard let range = at.flatMap(word(at:)) ?? textRange(from: beginningOfDocument, to: endOfDocument) else { return }
            _ = becomeFirstResponder()
            selectedTextRange = range
            picked = !range.isEmpty
            showMenu(for: range)
        }
    }

    /// The word under a point in the window; where there is none (a space, a mark), the character there.
    private func word(at point: CGPoint) -> UITextRange? {
        guard let pos = closestPosition(to: convert(point, from: nil)) else { return nil }
        for direction in [UITextStorageDirection.forward, .backward] {
            if let w = tokenizer.rangeEnclosingPosition(pos, with: .word, inDirection: UITextDirection(rawValue: direction.rawValue)) {
                return w
            }
        }
        guard let next = position(from: pos, offset: 1) else { return nil }
        return textRange(from: pos, to: next)
    }

    private func showMenu(for range: UITextRange) {
        let rects = selectionRects(for: range).map(\.rect).filter { !$0.isEmpty && $0.width > 0 }
        guard let first = rects.first else { return }
        let box = rects.dropFirst().reduce(first) { $0.union($1) }.intersection(bounds)
        menuInteraction.presentEditMenu(with: UIEditMenuConfiguration(identifier: nil, sourcePoint: CGPoint(x: box.midX, y: box.minY)))
    }

    func editMenuInteraction(_ interaction: UIEditMenuInteraction, menuFor configuration: UIEditMenuConfiguration,
                             suggestedActions: [UIMenuElement]) -> UIMenu? {
        textView(self, editMenuForTextIn: selectedRange, suggestedActions: suggestedActions)
    }

    func textViewDidChangeSelection(_ textView: UITextView) {
        if selectedRange.length > 0 {
            picked = true
        } else if picked {
            picked = false
            ended()
        }
    }

    func textView(_ textView: UITextView, editMenuForTextIn range: NSRange, suggestedActions: [UIMenuElement]) -> UIMenu? {
        let picked = ReplyText.copyText(attributedText.attributedSubstring(from: range)).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !picked.isEmpty, let quote else { return UIMenu(children: suggestedActions) }
        let ours = UIMenu(options: .displayInline, children: [
            UIAction(title: String(localized: "Quote"), image: UIImage(systemName: Theme.Icon.quote)) { _ in quote(picked) },
        ])
        return UIMenu(children: [ours] + suggestedActions)
    }

    /// The words as the chat shows them; a table in it as its Markdown.
    override func copy(_ sender: Any?) {
        guard selectedRange.length > 0 else { return super.copy(sender) }
        UIPasteboard.general.string = ReplyText.copyText(attributedText.attributedSubstring(from: selectedRange))
        Haptics.success()
        copied()
    }
}

extension NSAttributedString.Key {
    /// What `ReplyLayout` draws behind a run of lines (a `ReplyBlock`).
    nonisolated static let replyBlock = NSAttributedString.Key("PersonaReplyBlock")
    /// What a picture in the words copies as (a table's Markdown).
    nonisolated static let replySource = NSAttributedString.Key("PersonaReplySource")
}

/// Behind a run of lines: a code block's background, a quote's bar, a rule. One object per block, so a block's lines
/// are one run of the attribute.
final class ReplyBlock: NSObject {
    enum Kind { case code, quote, rule }
    let kind: Kind
    /// How far its background reaches above and below its lines.
    let pad: CGFloat

    init(_ kind: Kind, pad: CGFloat = 0) {
        self.kind = kind
        self.pad = pad
    }

    func draw(lines: CGRect, x: CGFloat, width: CGFloat) {
        switch kind {
        case .code:
            UIColor(Theme.codeBackground).setFill()
            let box = CGRect(x: x, y: lines.minY - pad, width: width, height: lines.height + 2 * pad)
            UIBezierPath(roundedRect: box, cornerRadius: Theme.Radius.control).fill()
        case .quote:
            UIColor(Theme.accent).withAlphaComponent(0.5).setFill()
            UIRectFill(CGRect(x: x, y: lines.minY, width: Theme.Size.bar, height: lines.height))
        case .rule:
            UIColor.separator.setFill()
            UIRectFill(CGRect(x: x, y: lines.midY, width: width, height: Theme.Size.hairline))
        }
    }
}

/// Lays out the words with TextKit 1; where their blocks (`ReplyBlock`) are is read from it.
nonisolated final class ReplyLayout: NSLayoutManager {
    /// Each block with the lines it spans, in the text container's coordinates.
    @MainActor func blocks() -> [(ReplyBlock, CGRect)] {
        guard let storage = textStorage else { return [] }
        var out: [(ReplyBlock, CGRect)] = []
        storage.enumerateAttribute(.replyBlock, in: NSRange(location: 0, length: storage.length)) { value, range, _ in
            guard let block = value as? ReplyBlock else { return }
            var lines = CGRect.null
            enumerateLineFragments(forGlyphRange: glyphRange(forCharacterRange: range, actualCharacterRange: nil)) { _, used, _, _, _ in
                lines = lines.union(used)
            }
            guard !lines.isNull else { return }
            if let i = out.firstIndex(where: { $0.0 === block }) { out[i].1 = out[i].1.union(lines) } else { out.append((block, lines)) }
        }
        return out
    }
}

/// What is behind the words' blocks (a code block's background, a quote's bar, a rule), under the text view's
/// selection highlight (`LiftUITextView.behind`).
final class ReplyBlocksView: UIView {
    weak var layout: ReplyLayout?

    override func draw(_ rect: CGRect) {
        guard let layout, let container = layout.textContainers.first else { return }
        for (block, lines) in layout.blocks() { block.draw(lines: lines, x: 0, width: container.size.width) }
    }
}

/// A line of the selection's highlight (`LiftUITextView.selectionRects(for:)`).
final class LineSelectionRect: UITextSelectionRect {
    private let box: CGRect
    private let start: Bool
    private let end: Bool

    init(_ box: CGRect, start: Bool, end: Bool) {
        self.box = box
        self.start = start
        self.end = end
    }

    override var rect: CGRect { box }
    override var writingDirection: NSWritingDirection { .leftToRight }
    override var containsStart: Bool { start }
    override var containsEnd: Bool { end }
    override var isVertical: Bool { false }
}

/// A reply's Markdown laid out as the chat draws it (`MarkdownUI.Theme.persona`, `ReplyMetrics`), for selecting: what
/// the chat shows, not its marks. Paragraphs keep their lines (a single line break is one, as in the chat), headings,
/// list markers, inline code and links keep their look, code blocks and quotes their background and bar, tables are
/// the chat's own table as a picture (copied as Markdown). Formulas, diagrams and cards stay as written.
enum ReplyText {
    /// The fonts and colors of the words where they were.
    struct Look {
        var body: UIFont
        var code: UIFont
        var inlineCode: UIFont
        var color: UIColor
        var link: UIColor
        var traits: UITraitCollection

        init(_ words: LiftWords, traits: UITraitCollection) {
            let base = UIFont.preferredFont(forTextStyle: .body, compatibleWith: traits)
            var quiet = false
            switch words {
            case .plain(let c): color = UIColor(c)
            case .reply(let q): quiet = q; color = q ? .secondaryLabel : .label
            }
            body = quiet ? base.withSize(base.pointSize * ReplyMetrics.quiet) : base
            code = .monospacedSystemFont(ofSize: body.pointSize * Theme.Level.code.size / Theme.Level.body.size, weight: .regular)
            inlineCode = .monospacedSystemFont(ofSize: body.pointSize * ReplyMetrics.inlineCode, weight: .regular)
            link = UIColor(Theme.accent)
            self.traits = traits
        }

        var size: CGFloat { body.pointSize }
    }

    /// Your own message, as its bubble shows it: no Markdown.
    static func plain(_ text: String, look: Look) -> NSAttributedString {
        let style = NSMutableParagraphStyle()
        style.lineSpacing = look.size * ReplyMetrics.lineSpacing
        return NSAttributedString(string: text, attributes: [.font: look.body, .foregroundColor: look.color, .paragraphStyle: style])
    }

    /// A reply laid out for `width` (what a table's picture is drawn at).
    static func attributed(_ markdown: String, look: Look, width: CGFloat) -> NSAttributedString {
        let b = Builder(look: look, width: width)
        b.read(markdown)
        return b.finish()
    }

    /// The text of a part of the words, a picture in it as what it copies as.
    static func copyText(_ part: NSAttributedString) -> String {
        var out = ""
        part.enumerateAttribute(.replySource, in: NSRange(location: 0, length: part.length)) { value, range, _ in
            out += (value as? String) ?? (part.string as NSString).substring(with: range)
        }
        return out
    }

    /// A block's spacing: above and below it (the larger of two neighbours' wins, as in MarkdownUI), and inside its
    /// background.
    private struct Block {
        var top: CGFloat = 0
        var bottom: CGFloat = ReplyMetrics.paragraphBottom
        var pad: CGFloat = 0
    }

    private struct Line {
        var text: NSMutableAttributedString
        /// Its block; nil for a blank line between blocks.
        var block: Int?
        var style = NSMutableParagraphStyle()
        /// The first line of a list item (the space above items).
        var item = false
        /// What its line break carries when it has no text of its own (an empty line of code: its font and background).
        var ending: [NSAttributedString.Key: Any]?
    }

    private enum Open {
        case paragraph, list, quote(ReplyBlock), code(ReplyBlock, fence: Character?, count: Int), math(close: String)
    }

    private final class Builder {
        let look: Look
        let width: CGFloat
        var lines: [Line] = []
        var blocks: [Block] = []
        var open: Open?
        /// The open list's items: the indent of each level's marker and where its text starts.
        var levels: [(marker: Int, text: CGFloat)] = []
        var afterBlank = false

        init(look: Look, width: CGFloat) {
            self.look = look
            self.width = width
        }

        private var markerWidth: CGFloat { look.size * 1.5 }
        private var markerGap: CGFloat { look.size * 0.35 }

        func read(_ markdown: String) {
            let source = markdown.components(separatedBy: "\n")
            var i = 0
            while i < source.count {
                let raw = source[i]
                i += 1
                let trimmed = raw.trimmingCharacters(in: .whitespaces)
                let indent = Self.indent(raw)
                let deep = indent >= 4
                switch open {
                case .code(let block, let fence?, let count):
                    if indent < 4, trimmed.count >= count, trimmed.allSatisfy({ $0 == fence }) {
                        open = nil
                    } else {
                        code(raw, block)
                    }
                    continue
                case .math(let close):
                    add(verbatim(raw, look.body), in: blocks.count - 1, lineSpacing: true)
                    if trimmed == close { open = nil }
                    continue
                default: break
                }
                if trimmed.isEmpty {
                    blank()
                    continue
                }
                defer { afterBlank = false }
                if case .code(let block, nil, _) = open {
                    if deep { code(String(raw.dropFirst(min(indent, 4))), block); continue }
                    open = nil
                }
                if !deep, let f = Self.fence(trimmed) {
                    let block = ReplyBlock(.code, pad: Theme.Space.m)
                    blocks.append(Block(top: ReplyMetrics.codeTop, bottom: ReplyMetrics.codeBottom, pad: Theme.Space.m))
                    open = .code(block, fence: f.0, count: f.1)
                    levels = []
                    continue
                }
                if trimmed == "$$" || trimmed == "\\[" {
                    blocks.append(Block())
                    open = .math(close: trimmed == "$$" ? "$$" : "\\]")
                    add(verbatim(raw, look.body), in: blocks.count - 1, lineSpacing: true)
                    continue
                }
                let inList: Bool = if case .list = open { true } else { false }
                // Four spaces in, after a blank line and not in a list: code, as written.
                if deep, !inList, open == nil || afterBlank {
                    let block = ReplyBlock(.code, pad: Theme.Space.m)
                    blocks.append(Block(top: ReplyMetrics.codeTop, bottom: ReplyMetrics.codeBottom, pad: Theme.Space.m))
                    open = .code(block, fence: nil, count: 0)
                    code(String(raw.dropFirst(min(indent, 4))), block)
                    continue
                }
                if !deep, trimmed.contains("|"), i < source.count, Self.isDelimiterRow(source[i]) {
                    var rows = [raw, source[i]]
                    i += 1
                    while i < source.count, source[i].contains("|"), !source[i].trimmingCharacters(in: .whitespaces).isEmpty {
                        rows.append(source[i])
                        i += 1
                    }
                    table(rows.joined(separator: "\n"))
                    continue
                }
                if !deep, Self.isRule(trimmed) {
                    blocks.append(Block(top: Theme.Space.m, bottom: Theme.Space.m))
                    let line = verbatim(trimmed, look.body)
                    line.addAttributes([.foregroundColor: UIColor.clear, .replyBlock: ReplyBlock(.rule)], range: NSRange(location: 0, length: line.length))
                    add(line, in: blocks.count - 1)
                    open = nil
                    levels = []
                    continue
                }
                if !deep, let (level, title) = Self.heading(trimmed) {
                    let m = level == 1 ? (top: ReplyMetrics.heading1.top, bottom: ReplyMetrics.heading1.bottom)
                        : level == 2 ? ReplyMetrics.heading2 : ReplyMetrics.heading3
                    blocks.append(Block(top: m.top, bottom: m.bottom))
                    let font = level == 1 ? Self.weight(look.body.withSize(look.size * ReplyMetrics.heading1.size), .bold)
                        : Self.weight(look.body, .semibold)
                    add(inline(title, font: font), in: blocks.count - 1)
                    open = nil
                    levels = []
                    continue
                }
                if !deep, trimmed.hasPrefix(">") {
                    var rest = trimmed.dropFirst()
                    if rest.first == " " { rest = rest.dropFirst() }
                    let block: ReplyBlock
                    if case .quote(let b) = open { block = b } else {
                        block = ReplyBlock(.quote)
                        blocks.append(Block())
                        open = .quote(block)
                        levels = []
                    }
                    let line = inline(String(rest), font: look.body, color: .secondaryLabel)
                    line.addAttribute(.replyBlock, value: block, range: NSRange(location: 0, length: line.length))
                    add(line, in: blocks.count - 1, lineSpacing: true) { s in
                        s.firstLineHeadIndent = Theme.Space.m
                        s.headIndent = Theme.Space.m
                    }
                    continue
                }
                if let (marker, text) = Self.item(trimmed), inList || !deep {
                    item(marker: marker, text: text, indent: indent)
                    continue
                }
                if inList, !afterBlank || indent > (levels.last?.marker ?? 0) {
                    // More of the item above: under its words.
                    let at = levels.last?.text ?? 0
                    add(inline(trimmed, font: look.body), in: blocks.count - 1, lineSpacing: true) { s in
                        s.firstLineHeadIndent = at
                        s.headIndent = at
                    }
                    continue
                }
                if case .paragraph = open, !afterBlank {} else {
                    blocks.append(Block())
                    open = .paragraph
                    levels = []
                }
                add(inline(trimmed, font: look.body), in: blocks.count - 1, lineSpacing: true)
            }
        }

        private func blank() {
            switch open {
            case .code(let block, _?, _):
                code("", block)
                return
            case .list:
                break // a blank line inside a list: the list goes on if an item or an indented line follows
            case .code(_, nil, _):
                break // indented code goes on after a blank line while lines stay indented
            default:
                open = nil
            }
            afterBlank = true
            lines.append(Line(text: NSMutableAttributedString(string: ""), block: nil))
        }

        private func code(_ text: String, _ block: ReplyBlock) {
            let attrs: [NSAttributedString.Key: Any] = [.font: look.code, .foregroundColor: look.color, .replyBlock: block]
            add(NSAttributedString(string: text, attributes: attrs), in: blocks.count - 1) { s in
                s.lineSpacing = self.look.code.pointSize * ReplyMetrics.codeLineSpacing
                s.firstLineHeadIndent = Theme.Space.m
                s.headIndent = Theme.Space.m
                s.tailIndent = -Theme.Space.m
            }
            lines[lines.count - 1].ending = attrs
        }

        private func item(marker: String, text: String, indent: Int) {
            if case .list = open {
                while let last = levels.last, indent < last.marker, levels.count > 1 { levels.removeLast() }
                if let last = levels.last, indent > last.marker + 1 {
                    levels.append((indent, last.text + markerWidth + markerGap))
                } else if levels.isEmpty {
                    levels = [(indent, markerWidth + markerGap)]
                }
            } else {
                blocks.append(Block())
                open = .list
                levels = [(indent, markerWidth + markerGap)]
            }
            let level = levels.count - 1
            let start = levels[level].text - markerWidth - markerGap
            let shown: String
            if marker.first?.isNumber == true {
                shown = marker
            } else {
                shown = ["•", "◦", "▪"][min(level, 2)]
            }
            let markerSize = (shown as NSString).size(withAttributes: [.font: look.body]).width
            let line = NSMutableAttributedString(string: shown + "\t", attributes: [.font: look.body, .foregroundColor: look.color])
            line.append(inline(text, font: look.body))
            let textAt = levels[level].text
            add(line, in: blocks.count - 1, lineSpacing: true, item: true) { s in
                s.firstLineHeadIndent = max(start + self.markerWidth - markerSize, start)
                s.headIndent = textAt
                s.tabStops = [NSTextTab(textAlignment: .left, location: textAt)]
                s.defaultTabInterval = textAt
            }
        }

        private func table(_ source: String) {
            blocks.append(Block())
            open = nil
            levels = []
            guard let image = ReplyTable.picture(source, width: width, traits: look.traits) else {
                add(verbatim(source, look.body), in: blocks.count - 1, lineSpacing: true)
                return
            }
            let a = NSTextAttachment()
            a.image = image
            let size = image.size.width > width && width > 0
                ? CGSize(width: width, height: image.size.height * width / image.size.width) : image.size
            a.bounds = CGRect(origin: .zero, size: size)
            let line = NSMutableAttributedString(attachment: a)
            line.addAttributes([.replySource: source, .font: look.body], range: NSRange(location: 0, length: line.length))
            add(line, in: blocks.count - 1)
        }

        private func add(_ text: NSAttributedString, in block: Int, lineSpacing: Bool = false, item: Bool = false,
                         _ configure: (NSMutableParagraphStyle) -> Void = { _ in }) {
            var line = Line(text: NSMutableAttributedString(attributedString: text), block: block, item: item)
            if lineSpacing { line.style.lineSpacing = look.size * ReplyMetrics.lineSpacing }
            configure(line.style)
            lines.append(line)
        }

        private func verbatim(_ s: String, _ font: UIFont) -> NSMutableAttributedString {
            NSMutableAttributedString(string: s, attributes: [.font: font, .foregroundColor: look.color])
        }

        /// The lines joined, each with its spacing: inside a block its own (list items 0.2 em apart), between two blocks
        /// the larger of their margins plus what each keeps inside its background.
        func finish() -> NSAttributedString {
            let real = lines.indices.filter { lines[$0].block != nil }
            for (k, i) in real.enumerated() {
                let line = lines[i], b = blocks[line.block!]
                if k == 0 { line.style.paragraphSpacingBefore = b.pad }
                guard k + 1 < real.count else {
                    line.style.paragraphSpacing = b.pad
                    continue
                }
                let next = lines[real[k + 1]], nb = blocks[next.block!]
                if next.block == line.block {
                    line.style.paragraphSpacing = next.item ? look.size * ReplyMetrics.itemTop : 0
                } else {
                    let gap = max(b.bottom, nb.top) + b.pad + nb.pad
                    line.style.paragraphSpacing = max(0, gap - line.style.lineSpacing)
                }
            }
            // A blank line between blocks takes no room: the spacing above is the blocks'. It stays a line break, so a
            // copied part keeps its paragraphs apart.
            let none = NSMutableParagraphStyle()
            none.minimumLineHeight = 0.01
            none.maximumLineHeight = 0.01
            let tiny = UIFont.systemFont(ofSize: 0.01)
            let out = NSMutableAttributedString()
            for (n, line) in lines.enumerated() {
                let text = line.text
                if line.block == nil {
                    text.setAttributes([.font: tiny, .paragraphStyle: none], range: NSRange(location: 0, length: text.length))
                }
                if n < lines.count - 1 {
                    var attrs = line.ending ?? (text.length > 0 ? text.attributes(at: text.length - 1, effectiveRange: nil)
                        : (line.block == nil ? [.font: tiny] : [.font: look.body]))
                    attrs[.attachment] = nil // a table's line break is not more of its picture
                    attrs[.replySource] = nil
                    text.append(NSAttributedString(string: "\n", attributes: attrs))
                }
                let style = line.block == nil ? none : line.style
                text.addAttribute(.paragraphStyle, value: style, range: NSRange(location: 0, length: text.length))
                out.append(text)
            }
            // Blank lines at the start or the end of the reply take no part.
            while out.length > 0, out.string.hasSuffix("\n") { out.deleteCharacters(in: NSRange(location: out.length - 1, length: 1)) }
            return out
        }

        // MARK: Inline

        /// Code spans (left to the Markdown parser) and formulas (kept as written) in a line.
        private static let spans = try! NSRegularExpression(pattern: #"(`+).+?\1|\$\$.+?\$\$|\$[^\s$](?:[^$\n]*?[^\s$])?\$|\\\(.+?\\\)"#)

        /// Bold, italics, inline code, strikethrough and links of one line; formulas as written.
        func inline(_ line: String, font: UIFont, color: UIColor? = nil) -> NSMutableAttributedString {
            let color = color ?? look.color
            let out = NSMutableAttributedString()
            let ns = line as NSString
            var pending = "", at = 0
            for m in Self.spans.matches(in: line, range: NSRange(location: 0, length: ns.length)) {
                pending += ns.substring(with: NSRange(location: at, length: m.range.location - at))
                let span = ns.substring(with: m.range)
                at = m.range.location + m.range.length
                if span.hasPrefix("`") { pending += span; continue }
                out.append(markdown(pending, font: font, color: color))
                pending = ""
                out.append(NSAttributedString(string: span, attributes: [.font: font, .foregroundColor: color]))
            }
            pending += ns.substring(from: at)
            out.append(markdown(pending, font: font, color: color))
            return out
        }

        private func markdown(_ text: String, font: UIFont, color: UIColor) -> NSAttributedString {
            guard !text.isEmpty else { return NSAttributedString() }
            guard let parsed = try? AttributedString(markdown: text, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)) else {
                return NSAttributedString(string: text, attributes: [.font: font, .foregroundColor: color])
            }
            let out = NSMutableAttributedString()
            for run in parsed.runs {
                let intent = run.inlinePresentationIntent ?? []
                var f = font
                var attrs: [NSAttributedString.Key: Any] = [.foregroundColor: color]
                if intent.contains(.code) {
                    f = look.inlineCode.withSize(font.pointSize * ReplyMetrics.inlineCode)
                    attrs[.backgroundColor] = UIColor(Theme.codeBackground)
                }
                if intent.contains(.stronglyEmphasized) { f = Self.weight(f, .semibold) }
                if intent.contains(.emphasized), let d = f.fontDescriptor.withSymbolicTraits(f.fontDescriptor.symbolicTraits.union(.traitItalic)) {
                    f = UIFont(descriptor: d, size: 0)
                }
                if intent.contains(.strikethrough) { attrs[.strikethroughStyle] = NSUnderlineStyle.single.rawValue }
                if run.link != nil { attrs[.foregroundColor] = look.link } // not followed: a tap here is about the selection
                attrs[.font] = f
                out.append(NSAttributedString(string: String(parsed[run.range].characters), attributes: attrs))
            }
            return out
        }

        // MARK: Lines

        /// Leading spaces, a tab counting as four.
        static func indent(_ line: String) -> Int {
            var n = 0
            for c in line {
                if c == " " { n += 1 } else if c == "\t" { n += 4 } else { break }
            }
            return n
        }

        /// An opening fence: three or more backticks or tildes (a backtick fence's info string has no backtick).
        static func fence(_ trimmed: String) -> (Character, Int)? {
            guard let c = trimmed.first, c == "`" || c == "~" else { return nil }
            let n = trimmed.prefix { $0 == c }.count
            guard n >= 3 else { return nil }
            if c == "`", trimmed.dropFirst(n).contains("`") { return nil }
            return (c, n)
        }

        /// `---`, `* * *`, `___`: a rule, not a list item.
        static func isRule(_ trimmed: String) -> Bool {
            let marks = trimmed.filter { $0 != " " && $0 != "\t" }
            guard let c = marks.first, "-*_".contains(c), marks.count >= 3 else { return false }
            return marks.allSatisfy { $0 == c }
        }

        /// `## Title`: its level and words.
        static func heading(_ trimmed: String) -> (Int, String)? {
            let hashes = trimmed.prefix { $0 == "#" }.count
            guard (1...6).contains(hashes) else { return nil }
            let rest = trimmed.dropFirst(hashes)
            guard rest.isEmpty || rest.first == " " || rest.first == "\t" else { return nil }
            return (hashes, rest.trimmingCharacters(in: .whitespaces))
        }

        /// `- words`, `* words`, `+ words`, `5. words`, `5) words`: the marker and the words.
        static func item(_ trimmed: String) -> (String, String)? {
            if let c = trimmed.first, "-*+".contains(c), trimmed.dropFirst().first == " " {
                return (String(c), String(trimmed.dropFirst(2)))
            }
            let digits = trimmed.prefix { $0.isASCII && $0.isNumber }
            guard (1...9).contains(digits.count) else { return nil }
            let rest = trimmed.dropFirst(digits.count)
            guard let mark = rest.first, mark == "." || mark == ")", rest.dropFirst().first == " " else { return nil }
            return (String(trimmed.prefix(digits.count + 1)), String(rest.dropFirst(2)))
        }

        /// A table's delimiter row: `| --- | :-: |`.
        static func isDelimiterRow(_ line: String) -> Bool {
            let t = line.trimmingCharacters(in: .whitespaces)
            guard t.contains("|"), t.contains("-") else { return false }
            return t.allSatisfy { "|-: \t".contains($0) }
        }

        static func weight(_ f: UIFont, _ w: UIFont.Weight) -> UIFont {
            f.fontDescriptor.symbolicTraits.contains(.traitMonoSpace)
                ? .monospacedSystemFont(ofSize: f.pointSize, weight: w) : .systemFont(ofSize: f.pointSize, weight: w)
        }
    }
}

/// A Markdown table drawn as the chat draws it (`FittedTable` at the width it gets), for the words being selected.
@MainActor enum ReplyTable {
    static func picture(_ source: String, width: CGFloat, traits: UITraitCollection) -> UIImage? {
        guard width > 0 else { return nil }
        let fontSize = UIFont.preferredFont(forTextStyle: .body, compatibleWith: traits).pointSize * ReplyMetrics.inlineCode
        let m = MarkdownTableText.measure(source, fontSize: fontSize)
        let n = CGFloat(m.natural.count)
        let plan = TablePlan.fit(natural: m.natural, minimum: m.minimum,
                                 available: width - n * (FittedTable.cellPadding + FittedTable.border) - FittedTable.border * 3)
        // The table's style inside the theme: the nearer one wins, and the theme's own table scrolls (a renderer draws no
        // scroll views at all).
        let table = Markdown(source)
            .markdownBlockStyle(\.table) { config in
                FittedTable.drawn(config.label).environment(\.tableColumnWidths, plan.widths)
            }
            .markdownTheme(.persona)
            // At the columns' planned widths: no clear space beside a narrow table (it would be selected as part of it);
            // one wider than the words is scaled down to them where it is attached.
            .fixedSize()
            .environment(\.colorScheme, traits.userInterfaceStyle == .dark ? .dark : .light)
            .environment(\.dynamicTypeSize, DynamicTypeSize(traits.preferredContentSizeCategory) ?? .large)
        let r = ImageRenderer(content: table)
        r.proposedSize = .unspecified
        r.scale = traits.displayScale > 0 ? traits.displayScale : 3
        return r.uiImage
    }
}
#endif
