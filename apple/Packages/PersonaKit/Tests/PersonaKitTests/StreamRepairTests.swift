import Testing
@testable import PersonaKit

/// The block of a streaming reply still being written, made whole for the moment (`StreamRepair`). The first cases are
/// the web's (web/src/ui/stream/repair.test.ts), kept as they are there except where noted.
struct StreamRepairTests {
    private func repair(_ md: String) -> String { StreamRepair.repair(md) }

    // MARK: The web's cases

    @Test func closesAnOpenCodeFenceWithItsOwnMarker() {
        #expect(repair("text\n```ts\nconst a") == "text\n```ts\nconst a\n```")
        #expect(repair("~~~~\nx\n") == "~~~~\nx\n~~~~")
        #expect(repair("```\na\n```\ndone") == "```\na\n```\ndone")
    }

    @Test func closesWhatTheGrowingLineLeavesOpen() {
        #expect(repair("这是 **重要") == "这是 **重要**")
        #expect(repair("an *emph") == "an *emph*")
        #expect(repair("**done** and *also*") == "**done** and *also*")
    }

    @Test func leavesWhatItCannotBeSureOf() {
        #expect(repair("trailing **") == "trailing **") // nothing after the opener yet
        #expect(repair("** spaced") == "** spaced") // not an opener
        #expect(repair("**a\nb") == "**a\nb") // another line
        #expect(repair("* item\n* other") == "* item\n* other") // list markers
        #expect(repair("***both") == "***both")
        #expect(repair("a \\*literal") == "a \\*literal")
    }

    @Test func closesHalfACodeSpanAndLeavesWhatIsInIt() {
        // The web leaves the whole line when a code span is open; here the span is closed, and the `**` in it stays code.
        #expect(repair("`code **x") == "`code **x`")
    }

    @Test func showsAHalfWrittenImageOrLinkAsItsText() {
        #expect(repair("see ![chart](https://exa") == "see chart")
        #expect(repair("see ![cha") == "see cha")
        #expect(repair("read [the docs](https://exa") == "read the docs")
        #expect(repair("read [the docs](https://example.com) ok") == "read [the docs](https://example.com) ok")
    }

    @Test func holdsATablesHalfWrittenLastRowBack() {
        let head = "| 文件 | 改动 |\n| --- | --- |\n| a.swift | 刷新 |\n"
        #expect(repair(head + "| b.swi") == head)
        #expect(repair(head + "| b.swift | 超时 |\n") == head + "| b.swift | 超时 |\n")
        // the header itself, while its line is written, and a pipe in prose are left alone
        #expect(repair("| 文件 | 改") == "| 文件 | 改")
        #expect(repair("Use a | b\n| not a table") == "Use a | b\n| not a table")
    }

    // MARK: Fences

    @Test func holdsAFencesOpeningLineBackUntilItsInfoStringIsWhole() {
        #expect(repair("看这段：\n```swi") == "看这段：\n")
        #expect(repair("```mermaid") == "")
        #expect(repair("看这段：\n```swift\n") == "看这段：\n```swift\n```")
    }

    @Test func holdsAHalfWrittenClosingRunBack() {
        #expect(repair("```\nlet a = 1\n``") == "```\nlet a = 1\n```")
        #expect(repair("````\nx\n```") == "````\nx\n````") // shorter than the fence so far: it may grow into its closer
        #expect(repair("````\nx\n```\n") == "````\nx\n```\n````") // a line of code once it has ended
    }

    @Test func changesNothingInsideAFenceButItsEnd() {
        #expect(repair("```\n**x [a](b") == "```\n**x [a](b\n```")
        #expect(repair("```\n-") == "```\n-\n```")
    }

    @Test func closesAFenceInTheListItemItOpenedIn() {
        // At column 0 the closer would end the item and open an empty fence under the list.
        #expect(repair("1. 安装依赖：\n\n   ```bash\n   npm install") == "1. 安装依赖：\n\n   ```bash\n   npm install\n   ```")
        #expect(repair("1. 安装依赖：\n   ```bash\n   npm install\n") == "1. 安装依赖：\n   ```bash\n   npm install\n   ```")
        #expect(repair("- 运行：\n  ~~~\n  make") == "- 运行：\n  ~~~\n  make\n  ~~~")
        // a half-written closing run too
        #expect(repair("- 运行：\n  ```\n  make\n  ``") == "- 运行：\n  ```\n  make\n  ```")
    }

    // MARK: Tables

    @Test func givesATableHeaderItsDelimiterRowEarly() {
        #expect(repair("| 文件 | 改动 |\n") == "| 文件 | 改动 |\n| --- | --- |")
        #expect(repair("| a | b |\n| --") == "| a | b |\n| --- | --- |")
        #expect(repair("| a | b |\n|") == "| a | b |\n| --- | --- |")
        #expect(repair("## 结果\n| a | b | c |\n") == "## 结果\n| a | b | c |\n| --- | --- | --- |")
        // a whole delimiter row is kept as written (its alignment)
        #expect(repair("| a | b |\n| :-- | --: |") == "| a | b |\n| :-- | --: |")
        // a pipe at the start of a line of prose is not a header, nor is a pipe row under another
        #expect(repair("| 不是表格\n") == "| 不是表格\n")
        #expect(repair("| x |\n| a | b |\n") == "| x |\n| a | b |\n")
    }

    @Test func findsATableRightUnderAParagraphLine() {
        // cmark-gfm ends the paragraph at a header row with its delimiter row under it.
        #expect(repair("**结果：**\n| a | b |\n") == "**结果：**\n| a | b |\n| --- | --- |")
        #expect(repair("**结果：**\n| a | b |\n| --") == "**结果：**\n| a | b |\n| --- | --- |")
        #expect(repair("**结果：**\n| a | b |\n| --- | --- |\n| 1") == "**结果：**\n| a | b |\n| --- | --- |\n")
        #expect(StreamRepair.end(of: "**结果：**\n| a | b |\n| --- | --- |\n| 1") == .table)
        #expect(StreamRepair.end(of: "**结果：**\n| a | b |\n| --- | --- |\n| 1 | 2 |\n") == .table)
        // a row without pipes is a row too, until a line starts another block
        #expect(StreamRepair.end(of: "| a | b |\n| --- | --- |\nfoo") == .table)
        #expect(StreamRepair.end(of: "| a | b |\n| --- | --- |\n- 项") == .text)
    }

    @Test func keepsATableInTheListItemItIsIn() {
        #expect(repair("- 项\n\n  | a | b |\n") == "- 项\n\n  | a | b |\n  | --- | --- |")
        #expect(repair("1. 项\n   | a | b |\n   | -") == "1. 项\n   | a | b |\n   | --- | --- |")
        #expect(repair("- 项\n  | a | b |\n  | --- | --- |\n  | 1") == "- 项\n  | a | b |\n  | --- | --- |\n")
        #expect(StreamRepair.end(of: "- 项\n  | a | b |\n  | --- | --- |\n  | 1") == .table)
        // the next row's indentation, written before the row, is not a blank line ending the table
        #expect(repair("- 项\n\n  | a | b |\n  | --- | --- |\n  ") == "- 项\n\n  | a | b |\n  | --- | --- |\n")
        #expect(StreamRepair.end(of: "- 项\n\n  | a | b |\n  | --- | --- |\n  ") == .table)
        #expect(StreamRepair.end(of: "- 项\n\n  | a | b |\n") == .table)
        #expect(StreamRepair.end(of: "- 项\n\n  | a | b |\n  ") == .table)
    }

    @Test func findsNoTableInALazyLine() {
        // Less indented than the item's text, or under a quote, a pipe row only continues that paragraph (cmark-gfm).
        #expect(repair("- 项\n| a | b |\n") == "- 项\n| a | b |\n")
        #expect(repair("- a\n  - b\n  | x |\n") == "- a\n  - b\n  | x |\n")
        #expect(repair("> 引用\n| a | b |\n") == "> 引用\n| a | b |\n")
        #expect(repair("- 项\n| a | b |\n| --- | --- |\n| 1") == "- 项\n| a | b |\n| --- | --- |\n| 1")
        #expect(StreamRepair.end(of: "- 项\n| a | b |\n| --- | --- |\n| 1") == .text)
    }

    // MARK: Block markers

    @Test func holdsALastLineOfOnlyMarkersBack() {
        #expect(repair("- 第一项\n-") == "- 第一项\n")
        #expect(repair("- 第一项\n- ") == "- 第一项\n")
        #expect(repair("1. 第一步\n2.") == "1. 第一步\n")
        #expect(repair("1. 第一步\n12") == "1. 第一步\n")
        #expect(repair("正文\n##") == "正文\n")
        #expect(repair("正文\n>") == "正文\n")
        #expect(repair("标题\n---") == "标题\n") // not a setext heading for a moment
        #expect(repair("- [ ]") == "")
        #expect(repair("**") == "")
        // text after the marker shows
        #expect(repair("- 第") == "- 第")
        #expect(repair("2026年") == "2026年")
        #expect(repair("1.5 倍") == "1.5 倍")
    }

    // MARK: Inlines

    @Test func closesStrikethroughAndCodeSpans() {
        #expect(repair("这 ~~删除") == "这 ~~删除~~")
        #expect(repair("这 ~~删除~") == "这 ~~删除~~") // half its closer
        #expect(repair("请运行 `swift te") == "请运行 `swift te`")
        #expect(repair("``a ` b") == "``a ` b``")
        #expect(repair("**粗体里的 `代码") == "**粗体里的 `代码`**")
    }

    @Test func closesNestedOpenersInnermostFirst() {
        #expect(repair("**bold *ital") == "**bold *ital***")
        #expect(repair("**粗 ~~删") == "**粗 ~~删~~**")
    }

    @Test func cutsTrailingSpacesBeforeAnAddedCloser() {
        // `**重要 **` would leave the closer unclosing.
        #expect(repair("这是 **重要 ") == "这是 **重要**")
    }

    @Test func neverClosesAnUnderscore() {
        // Closed, it set the rest of the line in italics until the line ended; the finished text has none (as the web).
        #expect(repair("修改 src/_app.tsx 后重启") == "修改 src/_app.tsx 后重启")
        #expect(repair("改了 _utils.py 里的函数") == "改了 _utils.py 里的函数")
        #expect(repair("an _emph") == "an _emph")
        // what it pairs with still pairs, and what is open around it is still closed
        #expect(repair("**粗 _x") == "**粗 _x**")
        #expect(repair("_a_ 和 *b") == "_a_ 和 *b*")
    }

    @Test func leavesWordsAndURLsWithMarkersInThem() {
        #expect(repair("set snake_case_name") == "set snake_case_name")
        #expect(repair("大约 ~5 分钟") == "大约 ~5 分钟")
        #expect(repair("see https://example.com/_next/static") == "see https://example.com/_next/static")
        #expect(repair("<https://example.com/_a") == "<https://example.com/_a")
        #expect(repair("read [x](https://example.com/_a) and *more") == "read [x](https://example.com/_a) and *more*")
        #expect(repair("2 * 3") == "2 * 3")
    }

    @Test func leavesCJKEmphasisCmarkDoesNotPair() {
        // cmark does not close `**` after `：` before a letter: the finished text shows the markers, so does this.
        #expect(repair("**注意：**后") == "**注意：**后")
        #expect(repair("**注意：**后文 *斜") == "**注意：**后文 *斜*")
    }

    @Test func leavesFormulas() {
        #expect(repair("$$\nx^2 + **y") == "$$\nx^2 + **y")
        #expect(repair("其中 $a_1 *b") == "其中 $a_1 *b")
        #expect(repair("其中 $a_1$ 和 *b") == "其中 $a_1$ 和 *b*")
        #expect(repair("费用 $5 和 *b") == "费用 $5 和 *b*")
    }

    @Test func showsCJKLinksAndImagesAsTheirText() {
        #expect(repair("参见[文档](https://exa") == "参见文档")
        #expect(repair("**参见[文档](https://exa") == "**参见文档**")
        #expect(repair("图：![架构图]") == "图：架构图")
        #expect(repair("[WIP]") == "[WIP]")
    }

    @Test func leavesIndentedCode() {
        #expect(repair("正文\n\n    let a = **b") == "正文\n\n    let a = **b")
        #expect(repair("1. 第一项\n\n    接着 **重要") == "1. 第一项\n\n    接着 **重要**")
    }

    @Test func leavesALineThatHasEnded() {
        #expect(repair("这是 **重要\n") == "这是 **重要\n")
        #expect(repair("-\n") == "-\n")
    }

    @Test func leavesOutALineOfOnlyIndentationSoFar() {
        #expect(repair("```\na\n  ") == "```\na\n```")
        #expect(repair("正文\n\t") == "正文\n")
        #expect(repair("   ") == "")
        #expect(repair("a  b") == "a  b")
    }

    // MARK: Only the end changes

    /// A reply with every kind of block, written a few characters at a time.
    private static let reply = """
        ## 看完了

        改动没问题，**两处**要注意：`StreamChunks` 的 *缝隙* 和 [文档](https://example.com/docs_v2)。

        - 第一项 ~~删掉~~
        - 第二项
          1. 嵌套 `code`
        > 引用 **粗体**

        | 文件 | 改动 |
        | --- | :-: |
        | a.swift | 刷新 |

        1. 安装依赖：

           ```bash
           npm install
           ```
        **结果：**
        | 项 | 值 |
        | --- | --- |
        | b | 2 |

        ```swift
        let a = "**"
        ```

        $$
        x^2
        $$

        ![图](https://example.com/a.png) 结束 $a_1$。
        """

    @Test func everythingBeforeTheLastLineStaysAsItWas() {
        let scalars = Array(Self.reply.unicodeScalars)
        for n in 0...scalars.count {
            var v = String.UnicodeScalarView()
            v.append(contentsOf: scalars[..<n])
            let text = String(v)
            let tail = StreamChunks.split(text).tail
            let lastLine = tail.unicodeScalars.lastIndex(of: "\n").map { tail.unicodeScalars.index(after: $0) } ?? tail.unicodeScalars.startIndex
            let kept = tail.unicodeScalars[..<lastLine]
            let repaired = StreamRepair.repair(tail)
            #expect(repaired.unicodeScalars.starts(with: kept), "lines before the last changed at \(n): \(tail.debugDescription) → \(repaired.debugDescription)")
            _ = StreamRepair.end(of: tail)
            _ = StreamRepair.plainOpenFence(tail) { _ in true }
        }
        #expect(StreamRepair.repair(Self.reply) == Self.reply)
    }

    @Test func nothingChangesInAFinishedReply() {
        #expect(repair(Self.reply + "\n") == Self.reply + "\n")
    }

    // MARK: Where the text ends

    @Test func tellsWhereTheTextEnds() {
        #expect(StreamRepair.end(of: "") == .text)
        #expect(StreamRepair.end(of: "这是 **重要") == .text)
        #expect(StreamRepair.end(of: "看这段：\n```swi") == .code)
        #expect(StreamRepair.end(of: "```swift\nlet a") == .code)
        #expect(StreamRepair.end(of: "```swift\nlet a\n```") == .code) // the closing line has not ended yet
        #expect(StreamRepair.end(of: "```swift\nlet a\n```\n") == .text)
        #expect(StreamRepair.end(of: "| a | b |\n| --- | --- |\n| 1") == .table)
        #expect(StreamRepair.end(of: "| a | b |\n| --- | --- |\n| 1 | 2 |\n") == .table)
        #expect(StreamRepair.end(of: "| a | b |\n") == .table)
        #expect(StreamRepair.end(of: "| a | b |\n| -") == .table)
        #expect(StreamRepair.end(of: "| a | b") == .text)
        #expect(StreamRepair.end(of: "| a | b |\n| --- | --- |\n\n后文") == .text)
        #expect(StreamRepair.end(of: "$$\nx^2") == .math)
        #expect(StreamRepair.end(of: "$$\nx^2\n$$") == .math)
        #expect(StreamRepair.end(of: "$$\nx^2\n$$\n") == .text)
        #expect(StreamRepair.end(of: "\\[\nx") == .math)
        #expect(StreamRepair.end(of: "$$E=mc^2$$") == .math)
        #expect(StreamRepair.end(of: "质能 $E=mc^2$") == .text)
    }

    // MARK: Fences drawn as pictures

    @Test func drawsAnOpenPictureFenceAsPlainCode() {
        let drawn: (String) -> Bool = { $0 == "mermaid" || $0 == "math" }
        #expect(StreamRepair.plainOpenFence("看图：\n```mermaid\ngraph TD", when: drawn) == "看图：\n```\ngraph TD")
        #expect(StreamRepair.plainOpenFence("  ~~~ math \nx^2\n", when: drawn) == "  ~~~\nx^2\n")
        // a closed one keeps its language; so does a fence of another language
        #expect(StreamRepair.plainOpenFence("```mermaid\ngraph TD\n```\n", when: drawn) == "```mermaid\ngraph TD\n```\n")
        #expect(StreamRepair.plainOpenFence("```mermaid\na\n```\n```swift\nlet", when: drawn) == "```mermaid\na\n```\n```swift\nlet")
        #expect(StreamRepair.plainOpenFence("```swift\nlet", when: drawn) == "```swift\nlet")
    }
}
