import Foundation
import Testing
@testable import Persona

/// UI versions 5 and 6 on Apple (#5473, docs/extensions.md「UI version 5」「UI version 6」): the schema's v6 examples read
/// with every component known; answers that replace parts of the page or bring hidden lines; a control shown at once,
/// sent once per change, put back when it fails; hidden-lines gaps, ignore whitespace, changed words, generated files,
/// suggestions and line links; the field values' formats; the hub's image paths that take credentials.
@MainActor
struct ExtensionUIv6Tests {
    private static let examples = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "schema/extension/examples")

    private static func view(_ name: String) throws -> ExtensionView {
        try JSONDecoder().decode(ExtensionView.self, from: Data(contentsOf: examples.appending(path: name)))
    }

    private static let path = "/api/v1/plugins/home/-/ui/main"

    private func page(_ hub: FakeHub) -> ExtensionViewModel {
        ExtensionViewModel(plugin: "home", ui: "main", context: ExtensionUIContext.here().forPage(), deps: .init(api: { hub.client }))
    }

    private func bodies(_ hub: FakeHub) throws -> [JSONValue] {
        try hub.sent("POST", Self.path).map { try JSON.decoder.decode(JSONValue.self, from: $0.body ?? Data()) }
    }

    // MARK: The examples

    @Test func everyV6ExampleReadsWithNothingUnknown() throws {
        for name in ["View.v6-charts.json", "View.v6-controls.json", "View.v6-inputs.json", "View.v6-media.json", "View.v6-tiles.json"] {
            let v = try Self.view(name)
            let unknown = v.sections.flatMap(\.items).filter { if case .unknown = $0 { true } else { false } }
            #expect(unknown.isEmpty, "\(name): \(unknown.map(\.type))")
        }
        let tiles = try Self.view("View.v6-tiles.json").sections.flatMap(\.items)
        #expect(tiles.allSatisfy(\.isTiles))
        let parts = ExtensionParts.of(try Self.view("View.v6-tiles.json"))
        #expect(parts.allSatisfy(\.bare) && parts.count == 3, "each grid and carousel is a part of its own, without the row's inset")
    }

    // MARK: Controls

    private func controls(_ hub: FakeHub, answer: @escaping (JSONValue) throws -> Data) {
        hub.on("POST", Self.path) { req in
            let body = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            if body["action"] == nil {
                return try Data(contentsOf: Self.examples.appending(path: "View.v6-controls.json")).wrappedAsView()
            }
            return try answer(body)
        }
    }

    private func light(_ v: ExtensionView?) -> ExtensionRowControl? {
        v?.sections.flatMap(\.items).compactMap(\.row).first { $0.key == "light.main" }?.control
    }

    @Test func aSwitchSendsItsNameAndValueAndTheActionsOwnValues() async throws {
        let hub = FakeHub()
        controls(hub) { _ in Data("{}".utf8) }
        let m = page(hub)
        await m.load()
        let c = try #require(light(m.view))
        _ = await m.change("light.main", c, to: "false")
        let sent = try bodies(hub).filter { $0["action"] != nil }
        #expect(sent.count == 1)
        #expect(sent.first?["action"] == .string("toggle"))
        #expect(sent.first?["values"] == .object(["light.main": .string("false"), "entity": .string("light.main")]))
        #expect(sent.first?["context"]?["ui_version"] == .number(6))
    }

    @Test func aFailedChangePutsBackTheExtensionsValue() async throws {
        let hub = FakeHub()
        controls(hub) { _ in throw APIError(status: 502, message: "灯没有回应") }
        let m = page(hub)
        await m.load()
        let c = try #require(light(m.view))
        let o = await m.change("light.main", c, to: "false")
        #expect(o?.error == "灯没有回应")
        #expect(m.controlValues["light.main"] == nil, "the extension's last value (on) shows again")
        #expect(c.wire == "true")
    }

    @Test func anAnswerWithItemsReplacesOnlyThatRowAndDoesNotDrawThePageAgain() async throws {
        let hub = FakeHub()
        controls(hub) { _ in
            Data(#"{"items":[{"type":"row","key":"light.main","title":"主灯","control":{"kind":"switch","name":"light.main","on":false,"action":{"id":"toggle"}}}]}"#.utf8)
        }
        let m = page(hub)
        await m.load()
        let c = try #require(light(m.view))
        _ = await m.change("light.main", c, to: "false")
        #expect(light(m.view)?.on == false)
        #expect(m.view?.sections.flatMap(\.items).count == 5, "the other rows stay")
        #expect(try bodies(hub).filter { $0["action"] == nil }.count == 1, "no second draw")
    }

    @Test func aStepperSendsOnceAfterTheTapsPause() async throws {
        let hub = FakeHub()
        controls(hub) { _ in Data("{}".utf8) }
        let m = page(hub)
        await m.load()
        let row = try #require(m.view?.sections.flatMap(\.items).compactMap(\.row).first { $0.key == "climate.living" })
        let c = try #require(row.control)
        async let a = m.change("climate.living", c, to: "23", after: .milliseconds(200))
        try await Task.sleep(for: .milliseconds(50))
        async let b = m.change("climate.living", c, to: "23.5", after: .milliseconds(200))
        #expect(m.controlValues["climate.living"] == "23.5", "shown at once")
        _ = await (a, b)
        let sent = try bodies(hub).filter { $0["action"] == .string("temperature") }
        #expect(sent.count == 1 && sent.first?["values"]?["temperature"] == .string("23.5"))
    }

    @Test func aControlsValueIsWrittenAsTheProtocolSays() {
        let a = ExtensionAction(id: "x")
        #expect(ExtensionRowControl(kind: "switch", name: "n", on: true, action: a).wire == "true")
        #expect(ExtensionRowControl(kind: "stepper", name: "n", value: 22.5, step: 0.5, action: a).wire == "22.5")
        #expect(ExtensionRowControl(kind: "slider", name: "n", value: 60, min: 0, max: 100, action: a).wire == "60")
        let k = ExtensionRowControl(kind: "slider", name: "n", value: 4000, min: 2700, max: 6500, step: 100, display: "4000 K", action: a)
        #expect(k.written(4000) == "4000 K" && k.written(4500) == "4,500")
        #expect(ExtensionRowControl(kind: "slider", name: "n", min: 0, max: 50, action: a).stepSize == 0.5)
    }

    // MARK: UI version 5's answers

    @Test func linesFromContextAreKeptAndJoined() {
        let hub = FakeHub()
        let m = page(hub)
        m.lines(ExtensionFileLines(path: "a.go", side: "new", start: 10, lines: ["10", "11", "12"]))
        m.lines(ExtensionFileLines(path: "a.go", side: "new", start: 13, lines: ["13", "14"], total: 20))
        let f = m.fileLines[ExtensionViewModel.fileKey("a.go", "new")]
        #expect(f?.start == 10 && f?.lines == ["10", "11", "12", "13", "14"] && f?.total == 20)
        #expect(f?.covers(11, 14) == true && f?.covers(9, 11) == false)
    }

    @Test func aDiffDrawnAgainWithoutHunksKeepsTheLoadedOnes() {
        let hunks = [ExtensionHunk(oldStart: 1, newStart: 1, lines: ["+a"])]
        let old = ExtensionComponent.diff(ExtensionDiffComponent(key: "d", path: "a.go", hunks: hunks))
        let new = ExtensionComponent.diff(ExtensionDiffComponent(key: "d", path: "a.go", collapsed: true, viewed: true))
        guard case .diff(let kept) = ExtensionViewModel.keepingHunks(new, old: old) else { Issue.record("not a diff"); return }
        #expect(kept.hunks == hunks && kept.viewed == true)
    }

    // MARK: Code review

    @Test func gapsAreBetweenAroundAndAfterTheHunks() {
        let h1 = CodeDiffModel.lines(oldStart: 5, newStart: 5, raw: [" a", "+b", " c"])
        let h2 = CodeDiffModel.lines(oldStart: 20, newStart: 21, raw: [" x"])
        let gaps = CodeGap.of([h1, h2], starts: [(5, 5), (20, 21)], total: nil)
        #expect(gaps.map(\.index) == [0, 1, 2])
        #expect(gaps[0].from == 1 && gaps[0].to == 4 && gaps[0].first)
        #expect(gaps[1].from == 8 && gaps[1].to == 20 && gaps[1].offset == -1)
        #expect(gaps[2].from == 22 && gaps[2].to == nil, "the end is not known until the file's length is")
        #expect(CodeGap.of([h1, h2], starts: [(5, 5), (20, 21)], total: 21).count == 2, "nothing after the end")
    }

    @Test func ignoreWhitespaceMergesPairsThatDifferOnlyInSpaces() {
        let lines = CodeDiffModel.lines(oldStart: 1, newStart: 1, raw: ["-if x {", "+if x  {", "-a", "+b"])
        let out = CodeReview.ignoringWhitespace(lines)
        #expect(out.map(\.kind) == [.context, .del, .add])
        #expect(out[0].old == 1 && out[0].new == 1)
    }

    @Test func changedWordsMarkWhatChangedInAPair() {
        let lines = CodeDiffModel.lines(oldStart: 1, newStart: 1, raw: ["-let total = price * count", "+let total = cost * count"])
        let marks = CodeReview.changedWords(lines)
        let old = Array(lines[0].text), new = Array(lines[1].text)
        #expect(marks[0].map { String(old[$0]) } == ["price"])
        #expect(marks[1].map { String(new[$0]) } == ["cost"])
        let unlike = CodeDiffModel.lines(oldStart: 1, newStart: 1, raw: ["-alpha beta gamma", "+one two three"])
        #expect(CodeReview.changedWords(unlike).isEmpty, "too unlike: the whole line says it")
    }

    @Test func generatedFilesAreKnownByTheirPaths() {
        for p in ["go.sum", "web/package-lock.json", "api/user.pb.go", "x/types_gen.go", "dist/app.js", "vendor/a/b.go"] {
            #expect(CodeReview.isGenerated(p), "\(p)")
        }
        for p in ["main.go", "src/vendors.ts", "README.md"] { #expect(!CodeReview.isGenerated(p), "\(p)") }
    }

    @Test func suggestionsAreCutOutOfAComment() {
        let parts = CodeReview.parts("Try this:\n```suggestion\nlet a = 1\n```\nthanks")
        #expect(parts == [.markdown("Try this:"), .suggestion(["let a = 1"]), .markdown("thanks")])
        #expect(CodeReview.suggestion(["x", "y"]) == "```suggestion\nx\ny\n```")
    }

    @Test func aLineLinkIsHttpsOnly() {
        #expect(CodeReview.lineURL("https://h.example/a.go#L{start}-L{end}", start: 3, end: 5)?.absoluteString == "https://h.example/a.go#L3-L5")
        #expect(CodeReview.lineURL("http://h.example/{start}", start: 1, end: 1) == nil)
    }

    @Test func aSecondLineTappedMakesACommentOnSeveralLines() {
        let s = CodePageState()
        s.compose(.init(diff: "d", side: "new", line: 7))
        s.compose(.init(diff: "d", side: "new", line: 4))
        #expect(s.composing == .init(diff: "d", side: "new", line: 7, start: 4))
    }

    @Test func aDiffWithContextHasGapBarsAndShowsTheLinesBrought() {
        let d = ExtensionDiffComponent(key: "d", path: "a.go", hunks: [ExtensionHunk(oldStart: 10, newStart: 10, lines: [" x", "+y"])],
                                       context: ExtensionAction(id: "context"))
        let v = ExtensionView(sections: [ExtensionSection(items: [.diff(d)])])
        let state = CodePageState()
        let before = CodePageBlocks.of(v, sections: [0], state: state, look: .unified, canComment: true)
        #expect(before.filter { if case .diffGap = $0 { true } else { false } }.count == 2)
        state.reveal("d", 0, bottom: 2)
        let files = ["a.go|new": ExtensionViewModel.FileLines(start: 1, lines: (1...9).map { "line \($0)" })]
        let after = CodePageBlocks.of(v, sections: [0], state: state, look: .unified, canComment: true, review: .init(files: files))
        let texts = after.compactMap { if case .diffLine(_, _, _, let l, _, _) = $0 { l.text } else { nil } }
        #expect(texts.prefix(2) == ["line 8", "line 9"])
    }

    // MARK: Fields

    @Test func dateAndTimeValuesRoundTripInTheAppsZone() throws {
        let tz = try #require(TimeZone(identifier: "Asia/Shanghai"))
        let d = try #require(ExtensionFieldValue.date("2026-11-06T07:30", kind: "datetime", in: tz))
        #expect(ExtensionFieldValue.write(d, kind: "datetime", in: tz) == "2026-11-06T07:30")
        #expect(ExtensionFieldValue.write(d, kind: "date", in: tz) == "2026-11-06")
        #expect(ExtensionFieldValue.date("07:30", kind: "time", in: tz).map { ExtensionFieldValue.write($0, kind: "time", in: tz) } == "07:30")
        #expect(ExtensionFieldValue.date("2026-02-30", kind: "date") == nil, "no rolling over")
        #expect(ExtensionFieldValue.date("25:00", kind: "time") == nil)
    }

    @Test func multichoiceValuesAreAJSONArray() {
        #expect(ExtensionFieldValue.chosen(#"["kitchen","bedroom"]"#) == ["kitchen", "bedroom"])
        #expect(ExtensionFieldValue.chosen("kitchen").isEmpty)
        #expect(ExtensionFieldValue.write(chosen: ["a", "b"]) == #"["a","b"]"#)
    }

    // MARK: Pictures

    @Test func onlyTheHubsOwnPathForThisExtensionTakesCredentials() {
        #expect(ExtensionHubImages.checked("/api/v1/extensions/home/-/image/abc.DEF_-1", owner: "home") != nil)
        #expect(ExtensionHubImages.checked("/api/v1/extensions/other/-/image/abc", owner: "home") == nil, "another extension's")
        #expect(ExtensionHubImages.checked("/api/v1/extensions/home/-/image/../../token", owner: "home") == nil)
        #expect(ExtensionHubImages.checked("https://evil.example/api/v1/extensions/home/-/image/abc", owner: "home") == nil)
        #expect(ExtensionHubImages.checked("/api/v1/extensions/home/-/image/a?x=1", owner: "home") == nil)
        #expect(ExtensionHubImages.width(points: 300, scale: 3) == 1280 && ExtensionHubImages.width(points: 100, scale: 2) == 320)
    }

    @Test func anImagesAspectIsItsFrameBeforeItLoads() {
        let src = ExtensionImageSource(url: "https://e.example/a.jpg", width: 1920, height: 1080)
        #expect(ExtensionImageItem.ratio("wide", source: src) == 16 / 9)
        #expect(ExtensionImageItem.ratio(nil, source: src) == 4 / 3)
        #expect(ExtensionImageItem.ratio("auto", source: src) == 1920.0 / 1080.0)
        #expect(ExtensionImageItem.ratio("auto", source: ExtensionImageSource(ref: "x")) == 4 / 3)
    }

    // MARK: Styles

    @Test func everyColorAndIconHasTheAppsOwn() {
        for c in ExtensionColor.all { #expect(ExtensionUIStyle.color(c) != nil, "\(c)") }
        #expect(ExtensionUIStyle.color("chartreuse") == nil)
        let fallback = ExtensionUIStyle.icon("no-such-icon")
        let own = ExtensionNavIcon.all.filter { $0 != "plugin" && $0 != "extension" }
        for i in own { #expect(ExtensionUIStyle.icon(i) != fallback, "\(i) draws as the default icon") }
        #expect(Set(own.map(ExtensionUIStyle.icon)).count == own.count, "one meaning, one symbol")
    }
}

private extension Data {
    /// A View file as the hub's answer to a draw.
    func wrappedAsView() throws -> Data {
        Data(#"{"view":"#.utf8) + self + Data("}".utf8)
    }
}
