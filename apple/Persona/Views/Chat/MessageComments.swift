import Foundation

/// A comment that leads a message as a `> ` block (docs/protocol/protocol.md「Quote and comment blocks at the start of
/// messages」, the web's `lib/comments.ts` and `lib/drafts.ts`): the words picked in a message, lines of a file or a diff
/// (a review, #151), or an element of a dev server's page (#150), each with the reader's note. The blocks are text the
/// agent reads; written and read back here exactly as the web does, checked by both against spec/fixtures/comments.
nonisolated struct MessageComment: Codable, Sendable, Identifiable {
    /// What a code comment's diff shows (`Diff:`): the staged or unstaged changes, one commit, or what a review compares
    /// the files on disk with.
    enum Diff: Codable, Hashable, Sendable {
        case staged, unstaged, uncommitted
        /// A commit, by its hash (written as its first 8 characters).
        case commit(String)
        /// A review of the branch since it left the default branch, or of everything since a commit.
        case branchSince(String), since(String)
    }

    /// The lines a code comment is about.
    struct Code: Codable, Hashable, Sendable {
        /// The path the agent reads: relative to the session's folder when inside it.
        var file: String
        var from: Int
        var to: Int
        /// The numbers are the old file's (only removed lines picked).
        var old = false
        var diff: Diff?
        /// The fence's language: the file's extension, `diff` for a diff's lines.
        var lang: String?
        /// Lines left out after the first `MessageComments.maxCodeLines`.
        var omitted = 0
    }

    /// The element of a page an element comment is about.
    struct Element: Codable, Hashable, Sendable {
        var url: String
        var selector: String
        /// The component that drew it and where its code is, when the dev build says.
        var component: String?
        var source: String?
        /// Its visible text.
        var text: String?
        var omitted = 0
    }

    var id = UUID()
    /// The picked words, the code lines, or the element's HTML.
    var quoted: String
    var note: String
    var code: Code?
    var element: Element?
}

nonisolated extension MessageComment: Hashable {
    /// The same comment whatever card it sits on.
    static func == (a: Self, b: Self) -> Bool {
        a.quoted == b.quoted && a.note == b.note && a.code == b.code && a.element == b.element
    }

    func hash(into h: inout Hasher) {
        h.combine(quoted)
        h.combine(note)
        h.combine(code)
        h.combine(element)
    }
}

nonisolated enum MessageComments {
    /// A note is cut at this many UTF-16 units (the web's characters).
    static let maxNote = 4000
    /// At most this many lines of code go with a code comment; the rest is counted, not sent.
    static let maxCodeLines = 60

    // MARK: Writing

    /// The message as sent with its comments: their blocks, a blank line, then what was typed (just the blocks when
    /// nothing was). The web's `withQuoteBlock` for comments.
    static func message(_ comments: [MessageComment], text: String) -> String {
        let blocks = quoted(comments)
        guard !blocks.isEmpty else { return text }
        let lead = blocks.joined(separator: "\n\n")
        return text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? lead : lead + "\n\n" + text
    }

    /// Each comment's block with `> ` in front of every line, numbered from 1.
    static func quoted(_ comments: [MessageComment]) -> [String] {
        comments.enumerated().map { i, c in
            let lines = block(c, number: i + 1).components(separatedBy: "\n").map { "> " + $0 }
            return trimEnd(lines.joined(separator: "\n"))
        }
    }

    /// The comment as the block the agent reads (before the `> ` marks): its number, where, the words or code, the note.
    static func block(_ c: MessageComment, number n: Int) -> String {
        let note = clamp(c.note).trimmingCharacters(in: .whitespacesAndNewlines)
        var out: [String] = []
        let shown = c.quoted.components(separatedBy: "\n").count
        if let e = c.element {
            out += ["Element comment \(n)", "Page: \(e.url)", "Element: \(e.selector)"] // i18n-ignore: read by the agent
            if let component = e.component, !component.isEmpty {
                out.append("Component: \(component)" + (e.source.map { $0.isEmpty ? "" : " (\($0))" } ?? ""))
            } else if let source = e.source, !source.isEmpty {
                out.append("Source: \(source)")
            }
            if let text = e.text, !text.isEmpty { out.append("Text: " + text.replacing(/\s+/, with: " ")) }
            if e.omitted > 0 { out.append("Truncated: the first \(shown) of \(shown + e.omitted) lines") }
            let f = fence(for: c.quoted)
            out += [f + "html", c.quoted, f]
        } else if let s = c.code {
            out += ["Code comment \(n)", "File: \(s.file):\(range(s.from, s.to))"]
            if s.old { out.append("Side: old") }
            if let d = s.diff { out.append("Diff: \(label(d))") }
            if s.omitted > 0 { out.append("Truncated: the first \(shown) of \(shown + s.omitted) lines") }
            let f = fence(for: c.quoted)
            out += [f + (s.lang ?? ""), c.quoted, f]
        } else {
            out += ["Comment \(n)", "Selected text:"] + c.quoted.components(separatedBy: "\n").map { "> " + $0 }
        }
        if !note.isEmpty { out += ["", note] }
        return out.joined(separator: "\n")
    }

    /// A fence the code cannot close: longer than any run of backticks in it, at least three.
    static func fence(for code: String) -> String {
        let run = code.matches(of: /`+/).map(\.output.count).max() ?? 0
        return String(repeating: "`", count: max(3, run + 1))
    }

    /// The note cut to `maxNote` UTF-16 units, never in the middle of a surrogate pair.
    static func clamp(_ s: String) -> String {
        let units = Array(s.utf16)
        guard units.count > maxNote else { return s }
        var cut = Array(units.prefix(maxNote))
        if let last = cut.last, UTF16.isLeadSurrogate(last) { cut.removeLast() }
        return String(decoding: cut, as: UTF16.self)
    }

    /// `12` or `12-14`.
    static func range(_ from: Int, _ to: Int) -> String { from == to ? "\(from)" : "\(from)-\(to)" }

    /// A `Diff:` line's value.
    static func label(_ d: MessageComment.Diff) -> String {
        switch d {
        case .staged: "staged"
        case .unstaged: "unstaged"
        case .uncommitted: "uncommitted"
        case .commit(let sha): "commit \(sha.prefix(8))"
        case .branchSince(let base): "branch since \(base)"
        case .since(let base): "since \(base)"
        }
    }

    /// A `Diff:` line's value read back.
    static func diff(_ v: String) -> MessageComment.Diff? {
        guard let m = v.trimmingCharacters(in: .whitespacesAndNewlines)
            .wholeMatch(of: /(staged|unstaged|uncommitted|commit ([0-9a-f]+)|(branch since|since) ([0-9a-f]+))/) else { return nil }
        if let sha = m.output.2 { return .commit(String(sha)) }
        if let kind = m.output.3, let base = m.output.4 { return kind == "since" ? .since(String(base)) : .branchSince(String(base)) }
        switch m.output.1 {
        case "staged": return .staged
        case "unstaged": return .unstaged
        default: return .uncommitted
        }
    }

    // MARK: Reading back

    /// A message's leading blocks: the first `> ` block when it is not a comment (a plain quote), the comment blocks,
    /// and what follows them; nil when the message does not start with a `> ` block. The web's `splitQuotes` and
    /// `parseQuotes`: a block after the first is taken only when it is a comment.
    struct Split: Equatable {
        var quote: String?
        var comments: [MessageComment]
        var rest: String
    }

    static func split(_ text: String) -> Split? {
        guard let first = splitQuote(text) else { return nil }
        var blocks = [first.quote]
        var rest = first.rest
        while let next = splitQuote(rest), isHead(next.quote.components(separatedBy: "\n")[0]) {
            blocks.append(next.quote)
            rest = next.rest
        }
        var quote: String?
        var comments: [MessageComment] = []
        for (i, b) in blocks.enumerated() {
            if isHead(b.components(separatedBy: "\n")[0]), let c = parse(b) {
                comments.append(c)
            } else if i == 0 {
                quote = b
            }
        }
        return Split(quote: quote, comments: comments, rest: rest)
    }

    /// A message that starts with a `> ` block: the block's words (marks taken off) and what follows.
    static func splitQuote(_ text: String) -> (quote: String, rest: String)? {
        var t = Substring(text)
        if let lead = t.prefixMatch(of: /\s*\n/) { t = t[lead.range.upperBound...] }
        let lines = t.components(separatedBy: "\n")
        var n = 0
        while n < lines.count, lines[n].prefixMatch(of: /\ {0,3}>/) != nil { n += 1 }
        guard n > 0 else { return nil }
        let quote = lines[..<n].map { $0.replacing(/^\ {0,3}>\ ?/, with: "") }.joined(separator: "\n")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard !quote.isEmpty else { return nil }
        return (quote, lines[n...].joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines))
    }

    /// `Comment 2`, `Code comment 1`, `Element comment 3`.
    static func isHead(_ line: String) -> Bool { head(line) != nil }

    private static func head(_ line: String) -> (kind: Substring, number: Substring)? {
        guard let m = line.trimmingCharacters(in: .whitespacesAndNewlines).wholeMatch(of: /(Code comment|Element comment|Comment) (\d+)/) else { return nil }
        return (m.output.1, m.output.2)
    }

    /// A comment block read back (an edited message, a sent bubble); nil when it is not one.
    static func parse(_ block: String) -> MessageComment? {
        let lines = block.components(separatedBy: "\n")
        guard let h = head(lines[0]) else { return nil }
        var i = 1
        if h.kind == "Comment" {
            if i < lines.count, lines[i].trimmingCharacters(in: .whitespaces) == "Selected text:" { i += 1 }
            var words: [String] = []
            while i < lines.count, lines[i].prefixMatch(of: /\ {0,3}>/) != nil {
                words.append(lines[i].replacing(/^\ {0,3}>\ ?/, with: ""))
                i += 1
            }
            return MessageComment(quoted: words.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines),
                                  note: note(lines, from: i))
        }
        if h.kind == "Element comment" { return element(Array(lines.dropFirst())) }
        var file: String?
        var code = MessageComment.Code(file: "", from: 0, to: 0)
        while i < lines.count, lines[i].prefixMatch(of: /```/) == nil {
            if let m = lines[i].wholeMatch(of: /(File|Side|Diff|Truncated):\ ?(.*)/) {
                let v = String(m.output.2)
                switch m.output.1 {
                case "File":
                    if let at = v.wholeMatch(of: /(.*):(\d+)(?:-(\d+))?/), let from = Int(at.output.2) {
                        file = String(at.output.1)
                        code.from = from
                        code.to = at.output.3.flatMap { Int($0) } ?? from
                    }
                case "Side": code.old = v == "old"
                case "Diff": if let d = diff(v) { code.diff = d }
                default: if let o = omitted(v) { code.omitted = o }
                }
            }
            i += 1
        }
        let (body, lang, after) = fenced(lines, at: i)
        guard let file else { return nil }
        code.file = file
        if let lang, !lang.isEmpty { code.lang = lang }
        return MessageComment(quoted: body, note: note(lines, from: after), code: code)
    }

    /// The rest of an `Element comment N` block (after its head).
    private static func element(_ lines: [String]) -> MessageComment? {
        var url: String?, selector: String?
        var e = MessageComment.Element(url: "", selector: "")
        var i = 0
        while i < lines.count, lines[i].prefixMatch(of: /```/) == nil {
            if let m = lines[i].wholeMatch(of: /(Page|Element|Component|Source|Text|Truncated):\ ?(.*)/) {
                let v = String(m.output.2)
                switch m.output.1 {
                case "Page": url = v
                case "Element": selector = v
                case "Component":
                    if let c = v.wholeMatch(of: /(.*?)(?:\ \((.*)\))?/) {
                        e.component = String(c.output.1)
                        if let s = c.output.2, !s.isEmpty { e.source = String(s) }
                    }
                case "Source": e.source = v
                case "Text": e.text = v
                default: if let o = omitted(v) { e.omitted = o }
                }
            }
            i += 1
        }
        let (body, _, after) = fenced(lines, at: i)
        guard let url, let selector else { return nil }
        e.url = url
        e.selector = selector
        return MessageComment(quoted: body, note: note(lines, from: after), element: e)
    }

    /// The fenced code at `i` (its lines and language) and the line after its closing fence.
    private static func fenced(_ lines: [String], at i: Int) -> (String, String?, Int) {
        guard i < lines.count, let open = lines[i].wholeMatch(of: /(`{3,})(.*)/) else { return ("", nil, i) }
        let f = String(open.output.1)
        var j = i + 1
        var code: [String] = []
        while j < lines.count, lines[j] != f {
            code.append(lines[j])
            j += 1
        }
        return (code.joined(separator: "\n"), String(open.output.2), j + 1)
    }

    /// `the first 60 of 80 lines` → 20.
    private static func omitted(_ v: String) -> Int? {
        guard let t = v.wholeMatch(of: /the first (\d+) of (\d+) lines/), let shown = Int(t.output.1), let all = Int(t.output.2) else { return nil }
        return all - shown
    }

    private static func note(_ lines: [String], from i: Int) -> String {
        i < lines.count ? lines[i...].joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines) : ""
    }

    private static func trimEnd(_ s: String) -> String {
        var t = Substring(s)
        while let last = t.last, last.isWhitespace { t = t.dropLast() }
        return String(t)
    }
}
