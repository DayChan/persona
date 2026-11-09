import Foundation
import Testing
@testable import Persona

/// UI version 4's code-hosting parts on Apple (#2961, docs/extensions/extensions.md「UI 版本 4」): the schema's examples read
/// with every component known, a diff's lines numbered and paired, a file tree built from paths, the page's blocks
/// laid out lazily (threads under their lines, the inline comment box where it was opened), the values a control
/// sends, and a View from an older hub drawn as before.
@MainActor
struct ExtensionCodeHostingTests {
    private static let examples = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "schema/extension/examples")

    private static func example(_ name: String) throws -> String {
        try String(contentsOf: examples.appending(path: name), encoding: .utf8)
    }

    private static func view(_ name: String) throws -> ExtensionView {
        try JSONDecoder().decode(ExtensionView.self, from: Data(try example(name).utf8))
    }

    // MARK: Reading the examples

    @Test(arguments: ["View.code-overview.json", "View.code-review.json", "View.code-browse.json"])
    func theExamplesAreReadWholly(_ name: String) throws {
        let v = try Self.view(name)
        let items = v.sections.flatMap(\.items)
        #expect(!items.contains { if case .unknown = $0 { true } else { false } }, "every component of \(name) is known")
        #expect(v.wantsCodePage, "\(name) is drawn by the code page")
        #expect(items.allSatisfy { !$0.isCodeHosting || $0.key != nil }, "the code components keep their keys")
    }

    @Test func theReviewPageIsASplitWithItsTreeAside() throws {
        let v = try Self.view("View.code-review.json")
        #expect(v.layout == ExtensionLayout.split)
        #expect(v.tabs?.items.map(\.value) == ["overview", "files", "commits", "checks"] && v.tabs?.value == "files")
        let side = v.sections.filter { $0.pane == "side" }
        #expect(side.count == 1)
        guard case .fileTree(let t)? = side.first?.items.first else { Issue.record("the side section is the tree"); return }
        #expect(t.selected == "src/upload/retry.go" && t.action?.id == "open_file" && t.entries.count == 3)
        let diffs = v.sections.flatMap(\.items).compactMap { c -> ExtensionDiffComponent? in if case .diff(let d) = c { d } else { nil } }
        #expect(diffs.map(\.path) == ["src/upload/retry.go", "src/upload/client.go", "docs/upload.md"])
        #expect(diffs[0].threads?.first?.reply?.values == ["thread": "t1"], "an action's own values")
        #expect(diffs[2].collapsed == true && diffs[2].load?.id == "load_diff" && diffs[2].hunks == nil)
    }

    // MARK: Diff lines

    @Test func aHunksLinesAreNumberedOnEachSide() {
        let lines = CodeDiffModel.lines(oldStart: 20, newStart: 20, raw: [" a", "-b", "-c", "+B", " d", "\\ No newline at end of file", "+e"])
        #expect(lines.map(\.kind) == [.context, .del, .del, .add, .context, .note, .add])
        #expect(lines.map(\.old) == [20, 21, 22, nil, 23, nil, nil])
        #expect(lines.map(\.new) == [20, nil, nil, 21, 22, nil, 23], "a \\ line takes no number")
        #expect(lines.map(\.text) == ["a", "b", "c", "B", "d", "No newline at end of file", "e"])
        #expect(lines[1].anchor! == ("old", 21) && lines[3].anchor! == ("new", 21) && lines[0].anchor! == ("new", 20))
        #expect(lines[5].anchor == nil)
        #expect(CodeDiffModel.counts(lines) == (2, 2))
    }

    @Test func aNewFileStartsAtLineOne() {
        let lines = CodeDiffModel.lines(oldStart: 0, newStart: 1, raw: ["+package upload", "+", "+import \"time\""])
        #expect(lines.map(\.new) == [1, 2, 3] && lines.allSatisfy { $0.old == nil })
        #expect(lines[1].text == "", "an empty added line")
    }

    @Test func aLineWithoutAPrefixReadsAsContext() {
        let lines = CodeDiffModel.lines(oldStart: 5, newStart: 7, raw: ["", "x"])
        #expect(lines.map(\.kind) == [.context, .context] && lines.map(\.old) == [5, 6] && lines.map(\.new) == [7, 8])
        #expect(lines[1].text == "x")
    }

    @Test func sideBySideRemovedLinesFaceTheAddedOnes() {
        let lines = CodeDiffModel.lines(oldStart: 1, newStart: 1, raw: [" a", "-b", "-c", "-d", "+B", " e", "+f"])
        let pairs = CodeDiffModel.pairs(lines)
        #expect(pairs.count == 6)
        #expect(pairs[0].left?.text == "a" && pairs[0].right?.text == "a")
        #expect(pairs[1].left?.text == "b" && pairs[1].right?.text == "B")
        #expect(pairs[2].left?.text == "c" && pairs[2].right == nil)
        #expect(pairs[3].left?.text == "d" && pairs[3].right == nil)
        #expect(pairs[5].left == nil && pairs[5].right?.text == "f")
    }

    // MARK: File tree

    @Test func theTreeIsBuiltFromPathsWithSingleChildChainsJoined() {
        let nodes = CodeTree.build([
            CodeTreeEntry(path: "src/upload/retry.go", status: "added", additions: 12),
            CodeTreeEntry(path: "src/upload/client.go", status: "modified"),
            CodeTreeEntry(path: "docs/upload.md"),
            CodeTreeEntry(path: "README.md"),
            CodeTreeEntry(path: "a.txt"),
        ])
        // Directories first, then files; each by name, case-insensitively.
        #expect(nodes.map(\.name) == ["docs", "src/upload", "a.txt", "README.md"])
        #expect(nodes[1].path == "src/upload" && nodes[1].children.map(\.name) == ["client.go", "retry.go"])
        #expect(nodes[1].children[1].entry?.additions == 12)
        #expect(nodes[1].fileCount == 2)
        let rows = CodeTree.rows(nodes, open: ["src/upload"])
        #expect(rows.map(\.node.path) == ["docs", "src/upload", "src/upload/client.go", "src/upload/retry.go", "a.txt", "README.md"])
        #expect(rows.map(\.depth) == [0, 0, 1, 1, 0, 0])
        #expect(CodeTree.parents(of: "src/upload/retry.go", in: nodes) == ["src/upload"])
        #expect(CodeTree.node("src/upload/retry.go", in: nodes)?.isDir == false)
    }

    @Test func aDirectoryNotLoadedYetStaysItsOwnRow() {
        let nodes = CodeTree.build([
            CodeTreeEntry(path: "src", kind: "dir"),
            CodeTreeEntry(path: "src/lib", kind: "dir"),
            CodeTreeEntry(path: "go.mod"),
        ])
        #expect(nodes.map(\.name) == ["src", "go.mod"], "src holds an unloaded directory: not joined into it")
        #expect(nodes[0].children.map(\.name) == ["lib"] && nodes[0].children[0].unloaded)
        #expect(!nodes[0].unloaded)
    }

    @Test func oddPathsAreTolerated() {
        let nodes = CodeTree.build([CodeTreeEntry(path: "/a//b.txt"), CodeTreeEntry(path: "a/b.txt", status: "deleted"), CodeTreeEntry(path: "")])
        #expect(nodes.count == 1 && nodes[0].children.count == 1, "empty parts dropped, a path given twice once")
        #expect(nodes[0].children[0].entry?.status == nil, "the first entry stays")
    }

    @Test func theExamplesTreeOpensAtTheSelectedFile() throws {
        let v = try Self.view("View.code-review.json")
        let state = CodePageState()
        let blocks = CodePageBlocks.of(v, sections: v.sections.indices.filter { v.sections[$0].pane == "side" }, state: state,
                                   look: .unified, canComment: true)
        let rows = blocks.compactMap { b -> String? in if case .treeRow(_, _, let r, _) = b { r.node.path } else { nil } }
        #expect(rows.contains("src/upload/retry.go"), "the selected file's directories start open: \(rows)")
    }

    // MARK: The page's blocks

    @Test func threadsGoUnderTheirLinesAndTheCommentBoxWhereItWasOpened() throws {
        let v = try Self.view("View.code-review.json")
        let state = CodePageState()
        let mains = v.sections.indices.filter { v.sections[$0].pane != "side" }
        var blocks = CodePageBlocks.of(v, sections: mains, state: state, look: .unified, canComment: true)
        let ids = blocks.map(\.id)
        #expect(Set(ids).count == ids.count, "block ids are unique")
        // retry.go's thread is on its new line 12, the last line of the hunk: right after it.
        guard let line12 = blocks.firstIndex(where: { if case .diffLine(_, _, let d, let l, _, _) = $0 { d.path == "src/upload/retry.go" && l.new == 12 } else { false } })
        else { Issue.record("line 12 of retry.go"); return }
        if case .inlineThread(_, _, let t) = blocks[line12 + 1] { #expect(t.id == "t1") } else { Issue.record("the thread follows its line") }
        // The collapsed file says it can be loaded.
        #expect(blocks.contains { if case .diffLoad(_, let d) = $0 { d.path == "docs/upload.md" } else { false } })
        // client.go has an expand action: a way to show the lines after the last hunk.
        #expect(blocks.contains { if case .diffMore(_, let d, 1) = $0 { d.path == "src/upload/client.go" } else { false } })

        state.compose(.init(diff: "diff:src/upload/client.go", side: "old", line: 24))
        blocks = CodePageBlocks.of(v, sections: mains, state: state, look: .unified, canComment: true)
        guard let removed = blocks.firstIndex(where: { if case .diffLine(_, _, let d, let l, _, _) = $0 { d.path == "src/upload/client.go" && l.old == 24 && l.kind == .del } else { false } })
        else { Issue.record("removed line 24 of client.go"); return }
        if case .inlineComposer(_, _, let a) = blocks[removed + 1] { #expect(a.side == "old" && a.line == 24) }
        else { Issue.record("the comment box follows the line it was opened on") }

        // Folding a file leaves its header only.
        state.toggle(diff: "diff:src/upload/retry.go", collapsed: false)
        blocks = CodePageBlocks.of(v, sections: mains, state: state, look: .unified, canComment: true)
        #expect(!blocks.contains { if case .diffLine(_, _, let d, _, _, _) = $0 { d.path == "src/upload/retry.go" } else { false } })
        #expect(blocks.contains { if case .diffHeader(_, _, let d, true) = $0 { d.path == "src/upload/retry.go" } else { false } })
    }

    @Test func sideBySideLaysTheDiffOutInPairs() throws {
        let v = try Self.view("View.code-review.json")
        let mains = v.sections.indices.filter { v.sections[$0].pane != "side" }
        let blocks = CodePageBlocks.of(v, sections: mains, state: CodePageState(), look: .split, canComment: true)
        // client.go (modified): its removed lines face the added ones.
        #expect(blocks.contains { if case .diffPair(_, _, let d, let l?, let r?, _, _) = $0 { d.path == "src/upload/client.go" && l.kind == .del && r.kind == .add } else { false } })
        #expect(!blocks.contains { if case .diffLine(_, _, let d, _, _, _) = $0 { d.path == "src/upload/client.go" } else { false } })
        // retry.go (added whole): one side only, so one column; side by side would leave half of it empty.
        #expect(blocks.contains { if case .diffLine(_, _, let d, _, _, _) = $0 { d.path == "src/upload/retry.go" } else { false } })
        #expect(!blocks.contains { if case .diffPair(_, _, let d, _, _, _, _) = $0 { d.path == "src/upload/retry.go" } else { false } })
    }

    @Test func aBigDiffIsOneBlockPerLine() {
        let raw = (0..<20_000).map { $0 % 3 == 0 ? "+line \($0)" : " line \($0)" }
        let d = ExtensionDiffComponent(key: "big", path: "big.go", hunks: [ExtensionHunk(oldStart: 1, newStart: 1, lines: raw)])
        let v = ExtensionView(sections: [ExtensionSection(items: [.diff(d)])])
        let started = Date()
        let blocks = CodePageBlocks.of(v, sections: [0], state: CodePageState(), look: .unified, canComment: true)
        #expect(blocks.count >= 20_000)
        #expect(Date().timeIntervalSince(started) < 2, "laying out 20 000 lines is quick (they are drawn lazily)")
    }

    // MARK: Values sent

    private func page(_ hub: FakeHub) -> ExtensionViewModel {
        ExtensionViewModel(plugin: "github", ui: "pr", context: ExtensionUIContext.here().forPage(), deps: .init(api: { hub.client }))
    }

    private func bodies(_ hub: FakeHub) throws -> [JSONValue] {
        try hub.sent("POST", "/api/v1/plugins/github/-/ui/pr").map { try JSON.decoder.decode(JSONValue.self, from: $0.body ?? Data()) }
    }

    @Test func anActionsOwnValuesWinOverTheComponentsAndTheAppSaysVersion6() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/github/-/ui/pr", json: #"{"view":\#(try Self.example("View.code-review.json"))}"#)
        let m = page(hub)
        await m.load()
        let reply = ExtensionAction(id: "reply", values: ["thread": "t1"])
        _ = await m.perform(reply, with: ["body": "ok", "thread": "other"])
        let sent = try bodies(hub)
        #expect(sent.first?["context"]?["ui_version"] == .number(6))
        #expect(sent.last?["action"] == .string("reply"))
        #expect(sent.last?["values"] == .object(["body": .string("ok"), "thread": .string("t1")]))
    }

    @Test func aWholePageAndACardBothSayVersion6() {
        #expect(ExtensionUIContext.here().forPage().uiVersion == ExtensionUIContext.pageVersion)
        #expect(ExtensionUIContext.here(size: "full").forPage().uiVersion == 6)
        #expect(ExtensionUIContext.here(size: "compact").forPage().uiVersion == ExtensionUIContext.version)
        #expect(ExtensionUIContext.version == 6, "cards draw version 6 too (#5473)")
        #expect(ExtensionUIContext.here().uiVersion == ExtensionUIContext.version)
    }

    @Test func aTabSendsItsValueAndTheSearch() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/github/-/ui/pr", json: #"{"view":\#(try Self.example("View.code-review.json"))}"#)
        let m = page(hub)
        await m.load()
        #expect(m.values["tab"] == "files", "the tabs' value is what the View says")
        _ = await m.pickTab("overview")
        #expect(try bodies(hub).last?["values"]?["tab"] == .string("overview"))
        #expect(try bodies(hub).last?["action"] == .string("tab"))
    }

    @Test func aComposerKeepsWhatIsTypedWhenDrawnAgain() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/github/-/ui/pr", json: #"{"view":\#(try Self.example("View.code-overview.json"))}"#)
        let m = page(hub)
        await m.load()
        let composer = m.view?.sections.flatMap(\.items).compactMap { c -> ExtensionComposerComponent? in if case .composer(let x) = c { x } else { nil } }.first
        guard let composer else { Issue.record("the overview has a composer"); return }
        m.values[composer.name] = "LGTM"
        await m.refresh()
        #expect(m.values[composer.name] == "LGTM")
    }

    // MARK: Older hubs and older views

    @Test func aVersion3ViewKeepsTheListPage() throws {
        let v = try Self.view("View.components-v3.json")
        #expect(!v.wantsCodePage, "a View without the new parts is drawn as before")
    }

    @Test func aComponentFromANewerHubIsLeftOut() throws {
        let json = #"{"sections":[{"items":[{"type":"hologram","key":"x"},{"type":"diff","path":"a.go","hunks":[{"old_start":1,"new_start":1,"lines":[" a"]}]}]}]}"#
        let v = try JSONDecoder().decode(ExtensionView.self, from: Data(json.utf8))
        #expect(v.wantsCodePage)
        let blocks = CodePageBlocks.of(v, sections: [0], state: CodePageState(), look: .unified, canComment: false)
        #expect(!blocks.contains { if case .legacy = $0 { true } else { false } }, "the unknown component is not drawn")
        #expect(blocks.contains { if case .diffLine = $0 { true } else { false } })
    }

    @Test func aDiffMissingItsPathIsLeftOut() throws {
        let json = #"{"sections":[{"items":[{"type":"diff","hunks":[]},{"type":"text","text":"still here"}]}]}"#
        let v = try JSONDecoder().decode(ExtensionView.self, from: Data(json.utf8))
        let blocks = CodePageBlocks.of(v, sections: [0], state: CodePageState(), look: .unified, canComment: false)
        #expect(!blocks.contains { if case .diffHeader = $0 { true } else { false } }, "nothing to anchor a diff without a path to")
        #expect(blocks.contains { if case .legacy = $0 { true } else { false } }, "the rest of the section is drawn")
    }

    @Test func languagesByName() {
        #expect(SyntaxLanguage.named("Go")?.id == SyntaxLanguage.go.id)
        #expect(SyntaxLanguage.named("TypeScript")?.id == SyntaxLanguage.typescript.id)
        #expect(SyntaxLanguage.named(".py")?.id == SyntaxLanguage.python.id)
        #expect(SyntaxLanguage.named("Objective-C")?.id == SyntaxLanguage.objc.id)
        #expect(SyntaxLanguage.named("Brainfuck") == nil && SyntaxLanguage.named("") == nil)
    }
}
