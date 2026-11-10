import Foundation

/// The block of a streaming reply still being written (`StreamChunks.split`'s tail), made whole for the moment before
/// it is parsed (#2916; a port of the web's `repairStreamingMarkdown`, web/src/ui/stream/repair.ts). Half a delimiter
/// showed raw and then flipped the structure when its other half came (`**bold` to bold, `[link](` to a link), so:
/// - an open code fence is closed with its own marker, indented as its opener; its opening line is held back until its
///   info string is whole, and a half-written closing run until it closes;
/// - a table's half-written last row is held back until its line ends, and a header row gets its delimiter row
///   (indented as the header) before it arrives;
/// - a last line of nothing but block markers (`-`, `1.`, `#`, `>`, `---`, `**`) is held back: it would draw as an
///   empty item or heading, or turn the line above into a heading, and then snap;
/// - on the last line, `**`, `*`, `~~` and a code span left open are closed (not `_`: see `InlineRepair`), and a
///   half-written link or image shows as its text.
/// Only the last line is cut or added to (a closing fence goes after it), so every line before it, and every block
/// already finished, stays byte for byte what it was. Inside an open `$$` block nothing changes; where it cannot be
/// sure, it changes nothing. Only while streaming: the finished text is drawn as written.
public nonisolated enum StreamRepair {
    /// Where a streaming text ends: in prose, or inside a block that a caret must not be written into.
    public enum End: Sendable, Equatable {
        case text
        /// In a code fence, its opening line still being written included, or on the line that closes it.
        case code
        /// In a table (a row being written included), or on its header row once that line has ended.
        case table
        /// In a `$$` / `\[` block, on the line that closes one, or on a line that is one formula.
        case math
    }

    /// `md` made whole for the moment (see the type).
    public static func repair(_ md: String) -> String {
        guard !md.isEmpty else { return md }
        if let shorter = withoutIndentSoFar(md) { return repair(shorter) }
        let x = Scan(md)
        guard let lastLine = x.lines.last else { return md }
        let last = x.lines.count - 1
        if let open = x.fence {
            // Indented as its opener: a fence in a list item is closed in that item. At column 0 the closer ended the
            // item and opened an empty fence under the list.
            let marker = x.indent(x.lines[open.line]) + String(repeating: open.fence.character, count: open.fence.count)
            if !x.endsWithNewline {
                // A language is not guessed from half its name; a half-written closing run is not a line of code.
                if open.line == last { return x.string(0..<lastLine.lowerBound) }
                let t = x.trimmed(lastLine)
                if !t.isEmpty, t.allSatisfy({ $0 == open.fence.character }) { return x.string(0..<lastLine.lowerBound) + marker }
            }
            return md + (x.endsWithNewline ? "" : "\n") + marker
        }
        // A formula being written is TeX, not Markdown; a line that just closed a block is whole.
        if x.mathOpen || x.closedLast != nil { return md }
        if let table = x.repairTable() { return table }
        if x.table != nil { return md }
        // A line ended by a newline is finished: what it left open is most likely meant literally.
        guard !x.endsWithNewline else { return md }
        if x.markersOnly(lastLine) { return x.string(0..<lastLine.lowerBound) }
        return x.repairInlines() ?? md
    }

    /// Where `md` (as it came, not repaired) ends.
    public static func end(of md: String) -> End {
        guard !md.isEmpty else { return .text }
        if let shorter = withoutIndentSoFar(md) { return end(of: shorter) }
        let x = Scan(md)
        if x.fence != nil { return .code }
        if x.mathOpen { return .math }
        if !x.endsWithNewline, let closed = x.closedLast { return closed }
        if !x.endsWithNewline, let last = x.lines.last, MathMarkdown.display(x.trimmed(last)) != nil { return .math }
        if x.table != nil { return .table }
        return .text
    }

    /// `md` without its last line when that line, not ended, is only the indentation of the next one so far. It draws as
    /// nothing; read as a blank line it ended the block above, and in a list item's table the caret after it was a row.
    private static func withoutIndentSoFar(_ md: String) -> String? {
        let u = md.unicodeScalars
        var i = u.endIndex
        while i > u.startIndex, u[u.index(before: i)] == " " || u[u.index(before: i)] == "\t" { i = u.index(before: i) }
        guard i < u.endIndex, i == u.startIndex || u[u.index(before: i)] == "\n" else { return nil }
        return String(u[..<i])
    }

    /// `md` with the info string of the code fence still open at its end taken off when `drawn` says that fence is
    /// drawn as a picture (```mermaid, ```math): it is drawn as plain code until it closes, not sent half-written to
    /// the renderer at every draw. Only the open fence's opening line changes; fences already closed keep theirs.
    public static func plainOpenFence(_ md: String, when drawn: (String) -> Bool) -> String {
        guard md.contains("```") || md.contains("~~~") else { return md }
        let x = Scan(md)
        guard let open = x.fence else { return md }
        let line = x.lines[open.line]
        var run = line.lowerBound
        while run < line.upperBound, x.s[run].isSpaceLike { run += 1 }
        while run < line.upperBound, Character(x.s[run]) == open.fence.character { run += 1 }
        let info = x.string(run..<line.upperBound).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !info.isEmpty, drawn(info) else { return md }
        return x.string(0..<run) + x.string(line.upperBound..<x.s.count)
    }
}

/// The lines of a streaming text and where its end sits: in a fence, in a `$$` block, in which block.
private nonisolated struct Scan {
    let s: [Unicode.Scalar]
    /// Each line without its "\n"; a text ending with "\n" has no empty line after it.
    let lines: [Range<Int>]
    let endsWithNewline: Bool

    struct OpenFence {
        let fence: MarkdownFence
        /// Its opening line.
        let line: Int
    }

    /// The code fence open at the end.
    private(set) var fence: OpenFence?
    /// Inside a `$$` / `\[` block on lines of their own (`MathMarkdown`'s rule).
    private(set) var mathOpen = false
    /// The last line closed a fence (`.code`) or a math block (`.math`).
    private(set) var closedLast: StreamRepair.End?
    /// The first line of the last block: after the last blank line, closed fence or closed math block.
    private(set) var blockStart = 0

    init(_ md: String) {
        s = Array(md.unicodeScalars)
        var lines: [Range<Int>] = []
        var start = 0
        for (i, c) in s.enumerated() where c == "\n" {
            lines.append(start..<i)
            start = i + 1
        }
        endsWithNewline = s.last == "\n"
        if start < s.count { lines.append(start..<s.count) }
        self.lines = lines

        var mathEnd: String?
        for (i, line) in lines.enumerated() {
            let t = trimmed(line)
            let isLast = i == lines.count - 1
            if let open = fence {
                if open.fence.isClosed(by: t) {
                    fence = nil
                    blockStart = i + 1
                    if isLast { closedLast = .code }
                }
                continue
            }
            if let end = mathEnd {
                if t == end {
                    mathEnd = nil
                    blockStart = i + 1
                    if isLast { closedLast = .math }
                }
                continue
            }
            if let f = MarkdownFence.opening(t) {
                fence = OpenFence(fence: f, line: i)
                blockStart = i
                continue
            }
            if columns(line) < 4, t == "$$" || t == "\\[" {
                mathEnd = t == "$$" ? "$$" : "\\]"
                blockStart = i
                continue
            }
            if t.isEmpty { blockStart = i + 1 }
        }
        mathOpen = mathEnd != nil
    }

    func string(_ r: Range<Int>) -> String {
        var v = String.UnicodeScalarView()
        v.append(contentsOf: s[r])
        return String(v)
    }

    /// The line without the spaces, tabs and carriage return around it.
    func trimmed(_ r: Range<Int>) -> String {
        var a = r.lowerBound, b = r.upperBound
        while a < b, s[a].isLineSpace { a += 1 }
        while b > a, s[b - 1].isLineSpace { b -= 1 }
        return string(a..<b)
    }

    /// Leading indentation in columns, a tab to the next multiple of four.
    func columns(_ r: Range<Int>) -> Int {
        var n = 0
        for i in r {
            switch s[i] {
            case " ": n += 1
            case "\t": n += 4 - n % 4
            default: return n
            }
        }
        return n
    }

    /// The spaces and tabs a line starts with.
    func indent(_ r: Range<Int>) -> String {
        var i = r.lowerBound
        while i < r.upperBound, s[i] == " " || s[i] == "\t" { i += 1 }
        return string(r.lowerBound..<i)
    }

    // MARK: Tables

    struct Table {
        /// Its header row.
        let header: Int
        /// Whether a whole delimiter row is under it. If not, the end is on the header row, its line ended, or on the
        /// delimiter row being written, and `repairTable` writes that row.
        let divided: Bool
    }

    /// The table the end is in, read close enough to cmark-gfm: a delimiter row with a pipe under a header row of as
    /// many cells, the header starting the last block or ending a paragraph (not as a lazy line of a list item or a
    /// quote), and every line after it a row until one starts another block. Without a delimiter row yet, a header row
    /// starting with a pipe, not under another pipe row (prose with pipes in it), is taken as one.
    var table: Table? {
        let last = lines.count - 1
        guard blockStart <= last else { return nil }
        // Up from the end through the rows to a delimiter row under its header.
        var i = last
        while i > blockStart, !Self.startsNonTableBlock(trimmed(lines[i])) {
            let t = trimmed(lines[i]), head = trimmed(lines[i - 1])
            if t.contains("|"), Self.isDivider(t), !head.isEmpty, !Self.isDelimiterish(head), !Self.startsNonTableBlock(head),
               Self.cells(head) == Self.cells(t), !continuesLazily(i - 1) {
                return Table(header: i - 1, divided: true)
            }
            i -= 1
        }
        let h = endsWithNewline ? last : last - 1
        guard h >= blockStart else { return nil }
        let head = trimmed(lines[h])
        guard head.hasPrefix("|"), !Self.isDelimiterish(head), !continuesLazily(h),
              h == blockStart || !trimmed(lines[h - 1]).hasPrefix("|") else { return nil }
        let ok = h == last ? Self.isClosedPipeRow(head) : Self.isDelimiterish(trimmed(lines[last]))
        return ok ? Table(header: h, divided: false) : nil
    }

    /// A table's half-written last row held back (as the web, #5602), or a header row given its delimiter row early
    /// (indented as the header, so a table in a list item stays in it).
    func repairTable() -> String? {
        guard let table else { return nil }
        let last = lines.count - 1
        if table.divided {
            guard !endsWithNewline, last > table.header + 1, trimmed(lines[last]).hasPrefix("|") else { return nil }
            return string(0..<lines[last].lowerBound)
        }
        let head = lines[table.header]
        let row = indent(head) + "|" + String(repeating: " --- |", count: Self.cells(trimmed(head)))
        if table.header == last { return string(0..<s.count) + row }
        return string(0..<lines[last].lowerBound) + row
    }

    /// Whether line `i`, with no blank line above it in its block, only continues the paragraph of a list item or a
    /// quote above it (a lazy line): it is less indented than that item's text, or under a quote.
    private func continuesLazily(_ i: Int) -> Bool {
        for j in stride(from: i - 1, through: blockStart, by: -1) {
            let t = trimmed(lines[j])
            if Self.isHeading(t) { return false }
            if t.hasPrefix(">") { return true }
            if Self.startsListItem(t) { return columns(lines[i]) < contentColumn(lines[j]) }
        }
        return false
    }

    /// The column a list item's text starts at: after its marker and the one to four spaces after it.
    private func contentColumn(_ r: Range<Int>) -> Int {
        let t = trimmed(r)
        let marker = t.first.map { "-+*".contains($0) } == true ? 1 : t.prefix(while: { $0.isASCII && $0.isNumber }).count + 1
        let gap = t.dropFirst(marker).prefix(while: { $0 == " " }).count
        return columns(r) + marker + ((1...4).contains(gap) ? gap : 1)
    }

    /// A row that starts and ends with a pipe: likely a table's header (a pipe at the start alone may be prose).
    static func isClosedPipeRow(_ t: String) -> Bool {
        t.count >= 2 && t.hasPrefix("|") && t.hasSuffix("|") && cells(t) > 0
    }

    /// The cells of a pipe row: between unescaped pipes, without the empty ones outside a leading or trailing pipe.
    static func cells(_ t: String) -> Int { cellTexts(t).count }

    private static func cellTexts(_ t: String) -> [Substring] {
        var parts: [Substring] = []
        var start = t.startIndex
        var i = t.startIndex
        while i < t.endIndex {
            if t[i] == "\\" {
                i = t.index(after: i)
                if i < t.endIndex { i = t.index(after: i) }
                continue
            }
            if t[i] == "|" {
                parts.append(t[start..<i])
                start = t.index(after: i)
            }
            i = t.index(after: i)
        }
        parts.append(t[start...])
        if t.hasPrefix("|"), !parts.isEmpty { parts.removeFirst() }
        if t.hasSuffix("|"), t.count > 1, !parts.isEmpty { parts.removeLast() }
        return parts
    }

    /// A whole delimiter row (GFM): cells of dashes, a colon at either end.
    static func isDivider(_ t: String) -> Bool {
        let cells = cellTexts(t)
        return !cells.isEmpty && cells.allSatisfy { cell in
            var c = cell.trimmingCharacters(in: .whitespaces)[...]
            if c.hasPrefix(":") { c = c.dropFirst() }
            if c.hasSuffix(":") { c = c.dropLast() }
            return !c.isEmpty && c.allSatisfy { $0 == "-" }
        }
    }

    /// Only what a delimiter row is made of, so far.
    static func isDelimiterish(_ t: String) -> Bool {
        !t.isEmpty && t.allSatisfy { "|-: \t".contains($0) } && (t.contains("-") || t.contains("|"))
    }

    static func isHeading(_ t: String) -> Bool {
        let n = t.prefix(while: { $0 == "#" }).count
        guard (1...6).contains(n) else { return false }
        let rest = t.dropFirst(n)
        return rest.isEmpty || rest.first == " " || rest.first == "\t"
    }

    // MARK: Block markers

    /// A list item's marker at the start of `t`: `-`, `+`, `*` or `1.` / `1)`, then a space or the end.
    static func startsListItem(_ t: String) -> Bool {
        guard let c = t.first else { return false }
        if "-+*".contains(c) { return t.count == 1 || t.dropFirst().first == " " || t.dropFirst().first == "\t" }
        let digits = t.prefix(while: { $0.isASCII && $0.isNumber })
        guard (1...9).contains(digits.count) else { return false }
        let rest = t.dropFirst(digits.count)
        guard let mark = rest.first, mark == "." || mark == ")" else { return false }
        let after = rest.dropFirst()
        return after.isEmpty || after.first == " " || after.first == "\t"
    }

    /// Whether the line begins a block of its own: a list item, a quote, a heading or a table row.
    static func startsBlock(_ t: String) -> Bool { startsNonTableBlock(t) || t.hasPrefix("|") }

    /// A list item, a quote or a heading: what ends a table, and what is not one's header.
    static func startsNonTableBlock(_ t: String) -> Bool { startsListItem(t) || t.hasPrefix(">") || isHeading(t) }

    private static let markers: Set<Unicode.Scalar> = Set("#-=*+_>|`~:[]".unicodeScalars)

    /// A line of nothing but block markers — `-`, `1.`, `#`, `>`, `---`, `==`, `**`, a lone `|`, `- [ ]` — would draw as
    /// an empty item or heading, or turn the line above into a heading, then snap (MarkdownView's streaming tail rule).
    func markersOnly(_ r: Range<Int>) -> Bool {
        guard let first = r.first(where: { !s[$0].isLineSpace }) else { return false }
        var i = r.lowerBound
        var saw = false
        while i < r.upperBound {
            let c = s[i]
            if c.isLineSpace {
                i += 1
            } else if Self.markers.contains(c) {
                saw = true
                i += 1
            } else if c == "x" || c == "X", i > r.lowerBound, s[i - 1] == "[" {
                i += 1 // a task box's x
            } else if c.isASCIIDigit {
                // `1.` starts an ordered item; a bare number only at the start of the line, where it may still become one.
                var e = i
                while e < r.upperBound, s[e].isASCIIDigit { e += 1 }
                guard e - i <= 9 else { return false }
                if e == r.upperBound {
                    guard i == first else { return false }
                } else if s[e] != ".", s[e] != ")" {
                    return false
                }
                saw = true
                i = min(e + 1, r.upperBound)
            } else {
                return false
            }
        }
        return saw
    }

    // MARK: Inlines

    /// The last line's inlines closed: the emphasis and code span it opened, a link or image it began. Nil when nothing
    /// needs it, or when it cannot be sure.
    func repairInlines() -> String? {
        let last = lines.count - 1
        guard blockStart <= last else { return nil }
        // The last leaf block: from the last line that starts a block of its own (a heading is one line).
        var first = blockStart
        for i in stride(from: last, through: blockStart, by: -1) where Self.startsBlock(trimmed(lines[i])) {
            first = Self.isHeading(trimmed(lines[i])) && i < last ? i + 1 : i
            break
        }
        // Indented code (not a list's continuation) is code.
        if columns(lines[blockStart]) >= 4, !lines[..<blockStart].contains(where: { Self.startsListItem(trimmed($0)) }) { return nil }
        let lo = lines[first].lowerBound
        let lastLo = lines[last].lowerBound
        var end = lines[last].upperBound
        while end > lastLo, s[end - 1].isSpaceLike { end -= 1 }
        guard end > lastLo else { return nil }
        return InlineRepair(s: s, lo: lo, lastLo: lastLo, end: end).repaired()
    }
}

/// Reads the inlines of the last leaf block (`lo..<end`) and closes what its last line (from `lastLo`) left open.
/// Close enough to CommonMark (flanking, code spans, escapes) to agree with cmark on what is open; links' destinations,
/// autolinks, bare URLs and formulas are skipped, so an underscore in them opens nothing.
private nonisolated struct InlineRepair {
    let s: [Unicode.Scalar]
    let lo: Int
    let lastLo: Int
    /// The end of the text, without trailing spaces (a closer after a space would not close).
    let end: Int

    private struct Opener {
        var c: Unicode.Scalar
        var n: Int
        var at: Int
    }

    func repaired() -> String? {
        var openers: [Opener] = []
        var brackets: [(at: Int, image: Bool)] = []
        var mixed = Set<Unicode.Scalar>()
        /// An open code span on the last line: where it starts, and its closer.
        var code: (at: Int, closer: String)?
        /// A half-written link or image: from where it is cut, and its text.
        var link: (cut: Int, text: Range<Int>)?
        var stopped = false
        /// Where the shown text ends: before a half-written closer held back.
        var cut = end
        var i = lo
        scan: while i < end {
            let c = s[i]
            switch c {
            case "\\":
                guard i + 1 < end else { i += 1; continue }
                // `\(` opens inline math: its source is not Markdown.
                if s[i + 1] == "(" {
                    if let j = find("\\", ")", from: i + 2) { i = j + 2; continue }
                    if i >= lastLo { stopped = true; break scan }
                }
                i += 2
            case "`":
                let n = run(of: c, at: i)
                if let j = findRun(of: c, count: n, from: i + n) { i = j + n; continue }
                // An unmatched run is a code span being written only on the last line, with code right after it.
                if i >= lastLo, i + n < end, !s[i + n].isSpaceLike {
                    code = (i, String(repeating: "`", count: n))
                    stopped = true
                    break scan
                }
                i += n
            case "$":
                if i + 1 < end, s[i + 1] == "$" {
                    if let j = find("$", "$", from: i + 2) { i = j + 2; continue }
                    if i >= lastLo { stopped = true; break scan }
                    i += 2
                    continue
                }
                // An inline formula opens before a non-space; "$5" is money.
                if i + 1 < end, !s[i + 1].isSpaceLike, !s[i + 1].isASCIIDigit {
                    if let j = closingDollar(from: i + 1) { i = j + 1; continue }
                    if i >= lastLo { stopped = true; break scan }
                }
                i += 1
            case "h", "H":
                if i == lo || !s[i - 1].isASCIIAlphanumeric, let j = urlEnd(at: i) { i = j; continue }
                i += 1
            case "<":
                // An autolink or a tag: nothing in it is emphasis.
                if let j = find(">", from: i + 1), !s[(i + 1)..<j].contains(where: \.isSpaceLike) { i = j + 1; continue }
                i += 1
            case "[":
                brackets.append((i, i > lo && s[i - 1] == "!"))
                i += 1
            case "]":
                guard let b = brackets.popLast() else { i += 1; continue }
                if i + 1 < end, s[i + 1] == "(" {
                    if let j = find(")", from: i + 2) { i = j + 1; continue }
                    // The destination is still being written: the link shows as its text until it is whole.
                    if b.at >= lastLo {
                        link = (b.image ? b.at - 1 : b.at, (b.at + 1)..<i)
                        stopped = true
                        break scan
                    }
                    i += 2
                    continue
                }
                if b.image, i + 1 == end, b.at >= lastLo {
                    link = (b.at - 1, (b.at + 1)..<i)
                    stopped = true
                    break scan
                }
                i += 1
            case "*", "_", "~":
                let n = run(of: c, at: i)
                defer { i += n }
                // Strikethrough is `~~` only: a single `~` is "about" in prose.
                if c == "~", n != 2 {
                    // Half a `~~` closer at the end: held back, the closer is added whole.
                    if n == 1, i + n == end, i >= lastLo, openers.contains(where: { $0.c == "~" }) { cut = i }
                    continue
                }
                // A *** run pairs differently by context: then nothing of it is repaired (as the web).
                if n >= 3 { mixed.insert(c); continue }
                let (open, close) = flanking(c, at: i, count: n)
                var left = n
                if close {
                    while left > 0, let k = openers.lastIndex(where: { $0.c == c }) {
                        let used = left >= 2 && openers[k].n >= 2 ? 2 : 1
                        openers[k].n -= used
                        left -= used
                        openers.removeSubrange((k + 1)...)
                        if openers[k].n == 0 { openers.remove(at: k) }
                    }
                }
                if left > 0, open { openers.append(Opener(c: c, n: left, at: i + n - left)) }
            default:
                i += 1
            }
        }
        // An image whose alt text is still being written.
        if !stopped, let b = brackets.last(where: { $0.image && $0.at >= lastLo }) {
            link = (b.at - 1, (b.at + 1)..<end)
        }

        var closers = code?.closer ?? ""
        // Innermost first, only the last line's, each only when it is the one opener of its kind (two `**` openers
        // are a `**注意：**后文` that cmark does not pair either), and only when text follows it. Never `_` (as the web):
        // in a coding reply it is far more often `src/_app.tsx` or `_private` than emphasis, and closing it set the rest
        // of the line in italics until the line ended. It is still read, so what it pairs with stays paired.
        for o in openers.reversed() where o.c != "_" {
            guard o.at >= lastLo, !mixed.contains(o.c),
                  openers.count(where: { $0.c == o.c && $0.n == o.n }) == 1,
                  followedByText(o.at + o.n, before: cut, code: code?.at, link: link) else { break }
            closers += String(repeating: String(o.c), count: o.n)
        }
        if let link { return slice(0, link.cut) + slice(link.text.lowerBound, link.text.upperBound) + closers }
        guard !closers.isEmpty else { return nil }
        return slice(0, cut) + closers
    }

    private func slice(_ a: Int, _ b: Int) -> String {
        var v = String.UnicodeScalarView()
        v.append(contentsOf: s[a..<b])
        return String(v)
    }

    /// Whether something other than spaces and markers follows `p` in what is shown.
    private func followedByText(_ p: Int, before cut: Int, code: Int?, link: (cut: Int, text: Range<Int>)?) -> Bool {
        // An open code span starts with code (not a space).
        if code != nil { return true }
        func text(_ a: Int, _ b: Int) -> Bool { a < b && s[a..<b].contains { !$0.isSpaceLike && !"*_~`".unicodeScalars.contains($0) } }
        if let link { return text(p, link.cut) || text(max(p, link.text.lowerBound), link.text.upperBound) }
        return text(p, cut)
    }

    /// CommonMark's left- and right-flanking rules, and what they let `_` do inside a word.
    private func flanking(_ c: Unicode.Scalar, at i: Int, count n: Int) -> (open: Bool, close: Bool) {
        let prev: Unicode.Scalar? = i > lo ? s[i - 1] : nil
        let next: Unicode.Scalar? = i + n < end ? s[i + n] : nil
        let prevSpace = prev?.isSpaceLike ?? true, nextSpace = next?.isSpaceLike ?? true
        let prevPunct = prev?.isMarkdownPunctuation ?? false, nextPunct = next?.isMarkdownPunctuation ?? false
        let left = !nextSpace && (!nextPunct || prevSpace || prevPunct)
        let right = !prevSpace && (!prevPunct || nextSpace || nextPunct)
        if c == "_" { return (left && (!right || prevPunct), right && (!left || nextPunct)) }
        return (left, right)
    }

    private func run(of c: Unicode.Scalar, at i: Int) -> Int {
        var j = i
        while j < end, s[j] == c { j += 1 }
        return j - i
    }

    /// The next run of exactly `count` of `c` (a code span's closer), anywhere in the block.
    private func findRun(of c: Unicode.Scalar, count: Int, from start: Int) -> Int? {
        var j = start
        while j < end {
            guard s[j] == c else { j += 1; continue }
            let n = run(of: c, at: j)
            if n == count { return j }
            j += n
        }
        return nil
    }

    /// The end of the line `i` is on.
    private func lineEnd(_ i: Int) -> Int {
        var j = i
        while j < end, s[j] != "\n" { j += 1 }
        return j
    }

    /// `a` (then `b`) further on the same line.
    private func find(_ a: Unicode.Scalar, _ b: Unicode.Scalar? = nil, from start: Int) -> Int? {
        let stop = lineEnd(start)
        var j = start
        while j < stop {
            if s[j] == a, b == nil || (j + 1 < stop && s[j + 1] == b) { return j }
            j += 1
        }
        return nil
    }

    /// The `$` that closes an inline formula opened before `start` (`MathMarkdown.inlineSpans`' rule): the first one
    /// after, when it follows a non-space and comes before no digit.
    private func closingDollar(from start: Int) -> Int? {
        let stop = lineEnd(start)
        var j = start
        while j < stop {
            if s[j] == "\\" { j += 2; continue }
            if s[j] == "$" {
                let beforeDigit = j + 1 < stop && s[j + 1].isASCIIDigit
                return !s[j - 1].isSpaceLike && !beforeDigit ? j : nil
            }
            j += 1
        }
        return nil
    }

    /// Where a bare `http(s)://` URL at `i` ends: `Links` makes it an autolink, so what is in it is not emphasis.
    private func urlEnd(at i: Int) -> Int? {
        let scheme = ["https://", "http://"].first { p in
            let u = Array(p.unicodeScalars)
            return i + u.count <= end && zip(s[i..<(i + u.count)], u).allSatisfy { a, b in
                a.properties.lowercaseMapping == String(b)
            }
        }
        guard let scheme else { return nil }
        var j = i + scheme.unicodeScalars.count
        while j < end, !s[j].isSpaceLike, s[j].isASCII, s[j] != "<", s[j] != ">", s[j] != "]", s[j] != "`" { j += 1 }
        return j
    }
}

private extension Unicode.Scalar {
    /// A space, tab or carriage return: what trims a line.
    var isLineSpace: Bool { self == " " || self == "\t" || self == "\r" }

    /// Unicode whitespace as CommonMark counts it for flanking.
    var isSpaceLike: Bool { isLineSpace || self == "\n" || properties.generalCategory == .spaceSeparator }

    var isASCIIDigit: Bool { ("0"..."9").contains(self) }

    var isASCIIAlphanumeric: Bool { isASCIIDigit || ("a"..."z").contains(self) || ("A"..."Z").contains(self) }

    /// ASCII punctuation or a Unicode punctuation character (cmark-gfm's rule: `：` counts, a CJK letter does not).
    var isMarkdownPunctuation: Bool {
        if isASCII { return "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~".unicodeScalars.contains(self) }
        switch properties.generalCategory {
        case .connectorPunctuation, .dashPunctuation, .openPunctuation, .closePunctuation, .initialPunctuation,
             .finalPunctuation, .otherPunctuation: return true
        default: return false
        }
    }
}
