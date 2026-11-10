import Foundation

/// An edit's text before and after, compared line by line (#2915): the lines that stayed as context, the ones taken
/// out and put in, and inside a line that was changed rather than rewritten, the words that changed. Claude's Edit,
/// MultiEdit and Write carry the old and new text, not a patch; drawn as all of the old removed and all of the new
/// added, a one-word change in a 30-line edit read as 60 changed lines, and every +N −N with it. The web compares the
/// same items the same way (web/src/lib/compare.ts for lines, web/src/lib/code-review.ts for words), but for a newline
/// at the very end, which it takes as one more, empty line (`split`).
///
/// Lines are compared with the standard library's `difference(from:)`, a Myers diff, after the lines both texts start
/// and end with are set aside. Devices on iOS 26.0–26.3 run the older version of it, whose memory grows with the
/// square of the changes (docs/lessons/ios.md), so past `maxChangedLines` lines in between (or `maxChars` on either side, well above the
/// 12 KB the hub keeps of a tool's input) the lines in between come back as before: all removed, then all added
/// (`capped`).
public nonisolated struct LineDiff: Sendable, Equatable {
    public enum Kind: Sendable, Equatable { case context, add, del }

    public struct Line: Sendable, Equatable {
        public var kind: Kind
        public var text: String
        /// The line's number in the old text (1-based), for context and removed lines.
        public var oldLine: Int?
        /// The line's number in the new text (1-based), for context and added lines.
        public var newLine: Int?
        /// The words that changed, as UTF-16 offsets into `text` (`NSRange`'s units), in order and apart; empty when
        /// the line is not paired with one it was changed from or to, or the two are too unlike for words to help.
        public var changed: [Range<Int>]

        public init(kind: Kind, text: String, oldLine: Int? = nil, newLine: Int? = nil, changed: [Range<Int>] = []) {
            self.kind = kind
            self.text = text
            self.oldLine = oldLine
            self.newLine = newLine
            self.changed = changed
        }
    }

    public var lines: [Line]
    public var adds: Int
    public var dels: Int
    /// Too big to compare: the lines between the common start and end (all of them past `maxChars`) are removed, then
    /// added, and no words are marked.
    public var capped: Bool

    /// Lines of both texts compared at most, once the lines they start and end with are set aside.
    public static let maxChangedLines = 1_000
    /// UTF-16 units of either text compared at most.
    public static let maxChars = 100_000

    public init(old: String, new: String) {
        self.init(old: old, new: new, maxChars: Self.maxChars, maxChangedLines: Self.maxChangedLines)
    }

    init(old: String, new: String, maxChars: Int, maxChangedLines: Int, maxWordWork: Int = LineDiff.maxWordWork) {
        let (a, b) = Self.split(old, new)
        let (removed, inserted, capped) = Self.compare(a, b, old: old, new: new, maxChars: maxChars, maxChangedLines: maxChangedLines)
        // Both walked together: within a change the removed lines come first, then the added ones.
        var lines: [Line] = []
        lines.reserveCapacity(a.count + b.count)
        var i = 0, j = 0
        while i < a.count || j < b.count {
            if i < a.count, removed[i] || j == b.count {
                lines.append(Line(kind: .del, text: a[i], oldLine: i + 1)); i += 1
            } else if j < b.count, inserted[j] || i == a.count {
                lines.append(Line(kind: .add, text: b[j], newLine: j + 1)); j += 1
            } else {
                lines.append(Line(kind: .context, text: b[j], oldLine: i + 1, newLine: j + 1)); i += 1; j += 1
            }
        }
        if !capped { Self.markWords(&lines, budget: maxWordWork) }
        self.lines = lines
        adds = lines.reduce(0) { $0 + ($1.kind == .add ? 1 : 0) }
        dels = lines.reduce(0) { $0 + ($1.kind == .del ? 1 : 0) }
        self.capped = capped
    }

    /// The lines added and removed, as `LineDiff(old:new:)` counts them, without its lines or words: the +N −N of an
    /// edit that is not on screen.
    public static func counts(old: String, new: String) -> (adds: Int, dels: Int) {
        let (a, b) = split(old, new)
        let m = compare(a, b, old: old, new: new, maxChars: maxChars, maxChangedLines: maxChangedLines)
        return (m.inserted.reduce(0) { $0 + ($1 ? 1 : 0) }, m.removed.reduce(0) { $0 + ($1 ? 1 : 0) })
    }

    /// Which lines of `a` were removed and which of `b` inserted; `capped` past the budgets.
    private static func compare(_ a: [String], _ b: [String], old: String, new: String, maxChars: Int,
                                maxChangedLines: Int) -> (removed: [Bool], inserted: [Bool], capped: Bool) {
        guard old.utf16.count <= maxChars, new.utf16.count <= maxChars else {
            return ([Bool](repeating: true, count: a.count), [Bool](repeating: true, count: b.count), true)
        }
        var removed = [Bool](repeating: false, count: a.count), inserted = [Bool](repeating: false, count: b.count)
        // Equal lines share a number, so the diff compares integers.
        var ids: [String: Int] = [:]
        func id(_ s: String) -> Int {
            if let n = ids[s] { return n }
            ids[s] = ids.count
            return ids.count - 1
        }
        let x = a.map(id), y = b.map(id)
        var head = 0
        while head < x.count, head < y.count, x[head] == y[head] { head += 1 }
        var tail = 0
        while tail < x.count - head, tail < y.count - head, x[x.count - 1 - tail] == y[y.count - 1 - tail] { tail += 1 }
        let midX = Array(x[head..<(x.count - tail)]), midY = Array(y[head..<(y.count - tail)])
        if midX.isEmpty || midY.isEmpty || midX.count + midY.count > maxChangedLines {
            for k in midX.indices { removed[head + k] = true }
            for k in midY.indices { inserted[head + k] = true }
            return (removed, inserted, !midX.isEmpty && !midY.isEmpty)
        }
        for change in midY.difference(from: midX) {
            switch change {
            case .remove(let offset, _, _): removed[head + offset] = true
            case .insert(let offset, _, _): inserted[head + offset] = true
            }
        }
        return (removed, inserted, false)
    }

    /// Both texts' lines. A newline at the very end of both ends their last lines rather than starting an empty one,
    /// and so does a newline at the end of either when the other is empty: a file written with one (a Write's content,
    /// nearly always) counts the lines it has, as git does. An Edit's texts are pieces of a file, though: a newline at
    /// the end of only one of them puts in or takes out a line break, and the empty line after it stays to show that
    /// (`"x"` to `"x\n"` adds one). Split the same way, two texts have the same lines only when they are the same.
    static func split(_ old: String, _ new: String) -> (old: [String], new: [String]) {
        let newline = UInt8(ascii: "\n")
        let oldEnds = old.utf8.last == newline, newEnds = new.utf8.last == newline
        let ends = (oldEnds && newEnds) || old.isEmpty || new.isEmpty
        func lines(_ text: String, _ endsWithNewline: Bool) -> [String] {
            guard !text.isEmpty else { return [] }
            var lines = text.components(separatedBy: "\n")
            if ends, endsWithNewline { lines.removeLast() }
            return lines
        }
        return (lines(old, oldEnds), lines(new, newEnds))
    }

    // MARK: Folding

    /// What to show of `lines`: each line by its index, or in place of a long stretch of context, the lines left out.
    public enum Shown: Sendable, Equatable {
        case line(Int)
        case folded(Range<Int>)
    }

    /// The changes with `context` lines around each; a longer stretch of unchanged lines folds into one row. A diff
    /// with no change is shown whole.
    public func shown(context: Int = 3) -> [Shown] {
        guard lines.contains(where: { $0.kind != .context }) else { return lines.indices.map(Shown.line) }
        var near = [Bool](repeating: false, count: lines.count)
        for (k, line) in lines.enumerated() where line.kind != .context {
            for n in max(0, k - context)...min(lines.count - 1, k + context) { near[n] = true }
        }
        var out: [Shown] = []
        var k = 0
        while k < lines.count {
            guard !near[k] else { out.append(.line(k)); k += 1; continue }
            var end = k
            while end < lines.count, !near[end] { end += 1 }
            // One line folded into a row of its own saves nothing.
            if end - k > 1 { out.append(.folded(k..<end)) } else { out.append(.line(k)) }
            k = end
        }
        return out
    }

    // MARK: Words

    /// A run of a line's text, changed or not (`pieces`).
    public struct Piece: Sendable, Equatable {
        public var text: String
        public var changed: Bool

        public init(_ text: String, changed: Bool) {
            self.text = text
            self.changed = changed
        }
    }

    /// `text` cut where its `changed` ranges (UTF-16 offsets, as `Line.changed`) begin and end; ranges out of order or
    /// past the end are left out.
    public static func pieces(_ text: String, changed: [Range<Int>]) -> [Piece] {
        guard !changed.isEmpty else { return [Piece(text, changed: false)] }
        let u = text.utf16
        var out: [Piece] = []
        var at = u.startIndex, offset = 0
        for r in changed where r.lowerBound >= offset && r.upperBound <= u.count && !r.isEmpty {
            let lo = u.index(at, offsetBy: r.lowerBound - offset), hi = u.index(lo, offsetBy: r.count)
            if lo > at { out.append(Piece(String(text[at..<lo]), changed: false)) }
            out.append(Piece(String(text[lo..<hi]), changed: true))
            at = hi
            offset = r.upperBound
        }
        if at < u.endIndex { out.append(Piece(String(text[at...]), changed: false)) }
        return out
    }

    /// Lines longer than this (UTF-16 units), or with more words, are not compared word by word: too slow, too noisy.
    static let maxLineChars = 1_000
    static let maxLineWords = 500
    /// Under this share of characters in common (of both lines'), two lines read as rewritten: the row's color says
    /// enough. Unless one is nearly all in the other (`containedLeast`): words put in or taken out.
    static let alikeLeast = 0.4
    static let containedLeast = 0.7
    /// Word comparisons at most over the whole diff, each pair of lines counted as one's words times the other's (what
    /// comparing them can cost), so a long stretch of long changed lines stays quick: a few of the worst lines that
    /// pass the checks above (tens of milliseconds on a Mac), hundreds of ordinary ones.
    static let maxWordWork = 1_000_000

    /// In each change, the first removed line is paired with the first added one, the second with the second…, and
    /// each pair compared word by word.
    private static func markWords(_ lines: inout [Line], budget: Int) {
        var budget = budget
        var k = 0
        while k < lines.count, budget > 0 {
            guard lines[k].kind == .del else { k += 1; continue }
            var dels: [Int] = [], adds: [Int] = []
            while k < lines.count, lines[k].kind == .del { dels.append(k); k += 1 }
            while k < lines.count, lines[k].kind == .add { adds.append(k); k += 1 }
            for (d, a) in zip(dels, adds) where budget > 0 {
                guard let spans = words(lines[d].text, lines[a].text, budget: &budget) else { continue }
                lines[d].changed = spans.old
                lines[a].changed = spans.new
            }
        }
    }

    /// The ranges that changed between an old line and the new one it became, word by word; nil when the lines are
    /// too long or too unlike for it to help. Whitespace between two changed words joins them into one range.
    static func words(_ a: String, _ b: String, budget: inout Int) -> (old: [Range<Int>], new: [Range<Int>])? {
        if a == b { return ([], []) }
        let la = a.utf16.count, lb = b.utf16.count
        // A line against an empty one is all changed: the row's color says so.
        guard la > 0, lb > 0, la <= maxLineChars, lb <= maxLineChars else { return nil }
        let x = tokens(a), y = tokens(b)
        guard x.count <= maxLineWords, y.count <= maxLineWords else { return nil }
        func alike(_ common: Int) -> Bool {
            Double(2 * common) / Double(la + lb) >= alikeLeast || Double(common) / Double(min(la, lb)) >= containedLeast
        }
        // At most the words both have, counted once each: when even that is too little, the diff is not needed.
        var count: [Substring: Int] = [:]
        for t in x { count[t, default: 0] += 1 }
        var most = 0
        for t in y where (count[t] ?? 0) > 0 {
            count[t]! -= 1
            most += t.utf16.count
        }
        guard alike(most) else { return nil }
        budget -= x.count * y.count
        var keptX = [Bool](repeating: true, count: x.count), keptY = [Bool](repeating: true, count: y.count)
        for change in y.difference(from: x) {
            switch change {
            case .remove(let offset, _, _): keptX[offset] = false
            case .insert(let offset, _, _): keptY[offset] = false
            }
        }
        let common = zip(x, keptX).reduce(0) { $0 + ($1.1 ? $1.0.utf16.count : 0) }
        guard alike(common) else { return nil }
        return (spans(x, keptX), spans(y, keptY))
    }

    private static func spans(_ words: [Substring], _ kept: [Bool]) -> [Range<Int>] {
        var out: [Range<Int>] = []
        var at = 0
        for (n, w) in words.enumerated() {
            let len = w.utf16.count
            if !kept[n] {
                if let last = out.last, last.upperBound == at { out[out.count - 1] = last.lowerBound..<(at + len) }
                else { out.append(at..<(at + len)) }
            } else if w.allSatisfy(\.isWhitespace), let last = out.last, last.upperBound == at, n + 1 < words.count, !kept[n + 1] {
                // A space between two changed words: one range.
                out[out.count - 1] = last.lowerBound..<(at + len)
            }
            at += len
        }
        return out
    }

    private enum TokenClass { case word, space, alone }

    /// A line as words: runs of letters, digits and _, runs of whitespace, and every other character on its own. Han
    /// characters and kana are words of their own too: Chinese and Japanese put no spaces between words, and a whole
    /// sentence as one word would never match. Cut at characters (grapheme clusters), so a range never splits one.
    static func tokens(_ s: String) -> [Substring] {
        var out: [Substring] = []
        var start = s.startIndex, previous: TokenClass?
        var i = s.startIndex
        while i < s.endIndex {
            let c = tokenClass(s[i])
            if i > start, c == .alone || c != previous {
                out.append(s[start..<i])
                start = i
            }
            previous = c
            i = s.index(after: i)
        }
        if start < s.endIndex { out.append(s[start...]) }
        return out
    }

    private static func tokenClass(_ c: Character) -> TokenClass {
        if c.isWhitespace { return .space }
        if let scalar = c.unicodeScalars.first, scalar.properties.isIdeographic || isKana(scalar.value) { return .alone }
        if c.isLetter || c.isNumber || c == "_" { return .word }
        return .alone
    }

    private static func isKana(_ v: UInt32) -> Bool {
        (0x3040...0x30FF).contains(v) || (0x31F0...0x31FF).contains(v) || (0xFF66...0xFF9F).contains(v)
    }
}
