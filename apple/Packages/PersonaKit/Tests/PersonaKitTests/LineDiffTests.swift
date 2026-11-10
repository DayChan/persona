import Foundation
import Testing
@testable import PersonaKit

struct LineDiffTests {
    /// The lines as "<sign><text>", the way a diff reads.
    private func rows(_ d: LineDiff) -> [String] {
        d.lines.map { ($0.kind == .add ? "+" : $0.kind == .del ? "-" : " ") + $0.text }
    }

    /// The changed words of a line, as text.
    private func changed(_ l: LineDiff.Line) -> [String] {
        LineDiff.pieces(l.text, changed: l.changed).filter(\.changed).map(\.text)
    }

    @Test func identicalTextsAreAllContext() {
        let text = "let a = 1\nlet b = 2\n"
        let d = LineDiff(old: text, new: text)
        #expect(rows(d) == [" let a = 1", " let b = 2"])
        #expect(d.adds == 0 && d.dels == 0 && !d.capped)
        #expect(d.lines.map(\.oldLine) == [1, 2] && d.lines.map(\.newLine) == [1, 2])
        #expect(LineDiff(old: "", new: "").lines.isEmpty)
    }

    @Test func oneWordInALongEditIsOneLineEach() {
        // The issue's case: one word in a 30-line old_string read as 60 changed lines.
        let old = (1...30).map { "line \($0) of the function" }
        var new = old
        new[14] = "line 15 of the method"
        let d = LineDiff(old: old.joined(separator: "\n"), new: new.joined(separator: "\n"))
        #expect(d.adds == 1 && d.dels == 1)
        #expect(d.lines.count == 31)
        let del = d.lines[14], add = d.lines[15]
        #expect(del.kind == .del && del.text == "line 15 of the function" && del.oldLine == 15 && del.newLine == nil)
        #expect(add.kind == .add && add.text == "line 15 of the method" && add.newLine == 15 && add.oldLine == nil)
        #expect(changed(del) == ["function"] && changed(add) == ["method"])
        #expect(d.lines[16].kind == .context && d.lines[16].oldLine == 16 && d.lines[16].newLine == 16)
    }

    @Test func insertOnly() {
        let d = LineDiff(old: "a\nc", new: "a\nb\nc")
        #expect(rows(d) == [" a", "+b", " c"])
        #expect(d.adds == 1 && d.dels == 0)
        #expect(d.lines[1].newLine == 2 && d.lines[2].oldLine == 2 && d.lines[2].newLine == 3)
        #expect(d.lines.allSatisfy { $0.changed.isEmpty })
    }

    @Test func deleteOnly() {
        let d = LineDiff(old: "a\nb\nc\nd", new: "a\nd")
        #expect(rows(d) == [" a", "-b", "-c", " d"])
        #expect(d.adds == 0 && d.dels == 2)
        #expect(LineDiff(old: "gone\n", new: "").dels == 1)
    }

    @Test func aMovedBlockIsTakenOutAndPutBack() {
        let d = LineDiff(old: "a\nb\nc\nd", new: "c\nd\na\nb")
        #expect(d.adds == 2 && d.dels == 2)
        #expect(d.lines.filter { $0.kind == .context }.count == 2)
        #expect(d.lines.filter { $0.kind == .del }.map(\.text).sorted() == d.lines.filter { $0.kind == .add }.map(\.text).sorted())
    }

    @Test func removedLinesComeBeforeAddedOnes() {
        let d = LineDiff(old: "x\nold 1\nold 2\ny", new: "x\nnew 1\nnew 2\nnew 3\ny")
        #expect(rows(d) == [" x", "-old 1", "-old 2", "+new 1", "+new 2", "+new 3", " y"])
        // Paired first with first, second with second; the third added line has no partner.
        #expect(changed(d.lines[1]) == ["old"] && changed(d.lines[3]) == ["new"])
        #expect(changed(d.lines[2]) == ["old"] && changed(d.lines[4]) == ["new"])
        #expect(d.lines[5].changed.isEmpty)
    }

    @Test func whitespaceOnlyChange() {
        let d = LineDiff(old: "if a {\n  run()\n}", new: "if a {\n    run()\n}")
        #expect(d.adds == 1 && d.dels == 1)
        #expect(d.lines[1].changed == [0..<2] && d.lines[2].changed == [0..<4])
        // A trailing space taken off.
        let t = LineDiff(old: "let x = 1 ", new: "let x = 1")
        #expect(t.lines[0].changed == [9..<10] && t.lines[1].changed.isEmpty)
    }

    @Test func chineseAndJapaneseCompareCharacterByCharacter() {
        let d = LineDiff(old: "今天天气很好", new: "今天天气不好")
        #expect(changed(d.lines[0]) == ["很"] && changed(d.lines[1]) == ["不"])
        let j = LineDiff(old: "ファイルを開く", new: "ファイルを閉じる")
        #expect(changed(j.lines[0]) == ["開く"] && changed(j.lines[1]) == ["閉じる"])
        // Mixed with code: the Latin word stays one word.
        let m = LineDiff(old: "title = \"保存\"", new: "title = \"保存草稿\"")
        #expect(changed(m.lines[0]).isEmpty && changed(m.lines[1]) == ["草稿"])
    }

    @Test func rangesAreUTF16AndNeverSplitACharacter() {
        let d = LineDiff(old: "say 👋🏽 hi", new: "say 👋🏿 hi")
        // The emoji with its skin tone is one character, two scalars, four UTF-16 units.
        #expect(d.lines[0].changed == [4..<8] && d.lines[1].changed == [4..<8])
        #expect(changed(d.lines[0]) == ["👋🏽"] && changed(d.lines[1]) == ["👋🏿"])
        let n = NSRange(d.lines[1].changed[0])
        #expect((d.lines[1].text as NSString).substring(with: n) == "👋🏿")
    }

    @Test func spacesBetweenChangedWordsJoinThem() {
        let d = LineDiff(old: "return cache value now", new: "return fresh result now")
        #expect(changed(d.lines[0]) == ["cache value"] && changed(d.lines[1]) == ["fresh result"])
    }

    @Test func wordsPutInOrTakenOut() {
        let d = LineDiff(old: "call(a)", new: "call(a, b)")
        #expect(d.lines[0].changed.isEmpty && changed(d.lines[1]) == [", b"])
    }

    @Test func rewrittenLinesGetNoWords() {
        let d = LineDiff(old: "import Foundation", new: "let total = items.count")
        #expect(d.adds == 1 && d.dels == 1)
        #expect(d.lines.allSatisfy { $0.changed.isEmpty })
        // A line against an empty one: the row says it all.
        let e = LineDiff(old: "a\n\nb", new: "a\ntext\nb")
        #expect(e.lines.allSatisfy { $0.changed.isEmpty })
    }

    @Test func longLinesGetNoWords() {
        let long = String(repeating: "word ", count: 250)
        let d = LineDiff(old: long + "old", new: long + "new")
        #expect(d.adds == 1 && d.dels == 1)
        #expect(d.lines.allSatisfy { $0.changed.isEmpty })
    }

    @Test func theFinalNewlineEndsTheLastLine() {
        // A Write of two lines is two lines, not three.
        let w = LineDiff(old: "", new: "first\nsecond\n")
        #expect(rows(w) == ["+first", "+second"])
        #expect(w.adds == 2 && w.dels == 0)
        #expect(LineDiff(old: "a\n", new: "a\nb\n").lines.map(\.kind) == [.context, .add])
        #expect(LineDiff(old: "", new: "\n").adds == 1) // one empty line
        // Only the newline differs: one line break taken out, the line before it unchanged.
        let n = LineDiff(old: "}\n", new: "}")
        #expect(rows(n) == [" }", "-"] && n.adds == 0 && n.dels == 1)
        #expect(rows(LineDiff(old: "a", new: "a\n")) == [" a", "+"])
        // An edit's text that ends mid-line, given another line.
        #expect(rows(LineDiff(old: "x", new: "x\ny")) == [" x", "+y"])
        // Deleting everything, newline and all: the lines it had.
        #expect(rows(LineDiff(old: "a\nb\n", new: "")) == ["-a", "-b"])
    }

    @Test func aNewlineAtTheEndOfOneSideIsALineBreak() {
        // An Edit's texts are pieces of a file: "x\na" followed by "\n…" became "y\na\n\n…", one more line.
        let d = LineDiff(old: "x\na", new: "y\na\n")
        #expect(rows(d) == ["-x", "+y", " a", "+"])
        #expect(d.adds == 2 && d.dels == 1)
        // A function put in after a closing brace: three lines, a blank one, the function, and the blank one its final
        // newline makes before the file's next line.
        let f = LineDiff(old: "}", new: "}\n\nfunc b() {}\n")
        #expect(rows(f) == [" }", "+", "+func b() {}", "+"] && f.adds == 3)
        // The other way round: the line break taken out.
        let g = LineDiff(old: "x\na\n", new: "y\na")
        #expect(rows(g) == ["-x", "+y", " a", "-"] && g.adds == 1 && g.dels == 2)
        // However they are split, different texts never come out the same.
        for (old, new) in [("a", "a\n"), ("a\n", "a"), ("", "\n"), ("\n", ""), ("a\n\n", "a\n"), ("\n", "\n\n")] {
            let d = LineDiff(old: old, new: new)
            #expect(d.adds + d.dels > 0, "\(old.debugDescription) → \(new.debugDescription)")
        }
    }

    @Test func countsAreTheDiffs() {
        let old = (1...40).map { "line \($0)" }.joined(separator: "\n")
        var lines = (1...40).map { "line \($0)" }
        lines[3] = "line four"
        lines.insert("new line", at: 20)
        lines.removeLast()
        let cases: [(String, String)] = [
            (old, lines.joined(separator: "\n")), ("", "a\nb\n"), ("a\nb\n", ""), ("x\na", "y\na\n"), ("}\n", "}"), ("same", "same"),
            ((0..<600).map { "a \($0)" }.joined(separator: "\n"), (0..<600).map { "b \($0)" }.joined(separator: "\n")), // capped
        ]
        for (old, new) in cases {
            let d = LineDiff(old: old, new: new), n = LineDiff.counts(old: old, new: new)
            #expect(n.adds == d.adds && n.dels == d.dels)
        }
    }

    @Test func emptySides() {
        let w = LineDiff(old: "", new: "a\nb")
        #expect(rows(w) == ["+a", "+b"] && w.lines.map(\.newLine) == [1, 2])
        let gone = LineDiff(old: "a\nb", new: "")
        #expect(rows(gone) == ["-a", "-b"] && gone.lines.map(\.oldLine) == [1, 2])
        #expect(!w.capped && !gone.capped)
    }

    @Test func pastTheLineBudgetTheMiddleIsAllRemovedThenAllAdded() {
        let old = ["head"] + (1...10).map { "old \($0)" } + ["tail"]
        let new = ["head"] + (1...10).map { "new \($0)" } + ["tail"]
        let d = LineDiff(old: old.joined(separator: "\n"), new: new.joined(separator: "\n"), maxChars: 1_000, maxChangedLines: 10)
        #expect(d.capped)
        let removed: [String] = (1...10).map { "-old \($0)" }, added: [String] = (1...10).map { "+new \($0)" }
        #expect(rows(d) == [" head"] + removed + added + [" tail"])
        #expect(d.adds == 10 && d.dels == 10)
        #expect(d.lines.allSatisfy { $0.changed.isEmpty })
        // Within the budget, the same texts pair up word by word.
        let full = LineDiff(old: old.joined(separator: "\n"), new: new.joined(separator: "\n"), maxChars: 1_000, maxChangedLines: 20)
        #expect(!full.capped && full.lines.contains { !$0.changed.isEmpty })
    }

    @Test func pastTheCharacterBudgetEverythingIsRemovedThenAdded() {
        let d = LineDiff(old: "same\nold", new: "same\nnew", maxChars: 5, maxChangedLines: 1_000)
        #expect(d.capped)
        #expect(rows(d) == ["-same", "-old", "+same", "+new"])
    }

    @Test func aBigEditStaysQuick() {
        // Twice the line budget, every line different: no diff is run at all.
        let old = (0..<LineDiff.maxChangedLines).map { "old line \($0)" }.joined(separator: "\n")
        let new = (0..<LineDiff.maxChangedLines).map { "new line \($0)" }.joined(separator: "\n")
        let start = Date()
        let d = LineDiff(old: old, new: new)
        #expect(d.capped && d.adds == LineDiff.maxChangedLines && d.dels == LineDiff.maxChangedLines)
        // Just within the budget, every line different: the diff runs and stays well under a second.
        let half = LineDiff.maxChangedLines / 2
        let a = (0..<half).map { "a \($0)" }.joined(separator: "\n"), b = (0..<half).map { "b \($0)" }.joined(separator: "\n")
        let e = LineDiff(old: a, new: b)
        #expect(!e.capped && e.adds == half && e.dels == half)
        #expect(Date().timeIntervalSince(start) < 2)
    }

    @Test func theWordBudgetRunsOut() {
        let old = (1...20).map { "value \($0) old" }.joined(separator: "\n")
        let new = (1...20).map { "value \($0) new" }.joined(separator: "\n")
        let d = LineDiff(old: old, new: new, maxChars: 10_000, maxChangedLines: 1_000, maxWordWork: 75)
        let marked = d.lines.filter { !$0.changed.isEmpty }.count
        #expect(marked > 0 && marked < 40)
    }

    @Test func longStretchesOfContextFold() {
        let old = (1...20).map { "line \($0)" }
        var new = old
        new[9] = "line ten"
        let d = LineDiff(old: old.joined(separator: "\n"), new: new.joined(separator: "\n"))
        // Lines 1–6 folded, 7–9 shown, the change, 11–13 shown, 14–20 folded.
        #expect(d.shown() == [.folded(0..<6), .line(6), .line(7), .line(8), .line(9), .line(10), .line(11), .line(12), .line(13), .folded(14..<21)])
        // Two changes close together keep the lines between them.
        new[13] = "line fourteen"
        let two = LineDiff(old: old.joined(separator: "\n"), new: new.joined(separator: "\n"))
        #expect(!two.shown().contains { if case .folded(let r) = $0 { r.contains(11) } else { false } })
        // One line folded would save nothing.
        let short = LineDiff(old: "a\nb\nc\nd\nx", new: "a\nb\nc\nd\ny")
        #expect(short.shown(context: 3) == (0..<6).map(LineDiff.Shown.line))
        // No change: shown whole.
        #expect(LineDiff(old: "a\nb", new: "a\nb").shown(context: 0) == [.line(0), .line(1)])
    }

    @Test func piecesCutAtTheRanges() {
        #expect(LineDiff.pieces("abc", changed: []) == [.init("abc", changed: false)])
        #expect(LineDiff.pieces("let a = 1", changed: [4..<5, 8..<9])
            == [.init("let ", changed: false), .init("a", changed: true), .init(" = ", changed: false), .init("1", changed: true)])
        // Out of order or past the end: left out.
        #expect(LineDiff.pieces("abc", changed: [1..<2, 0..<1, 2..<9]) == [.init("a", changed: false), .init("b", changed: true), .init("c", changed: false)])
    }

    @Test func tokens() {
        #expect(LineDiff.tokens("foo_bar(x, 42)") == ["foo_bar", "(", "x", ",", " ", "42", ")"])
        #expect(LineDiff.tokens("中文 text") == ["中", "文", " ", "text"])
        #expect(LineDiff.tokens("café  ok") == ["café", "  ", "ok"])
        #expect(LineDiff.tokens("") == [])
    }
}
