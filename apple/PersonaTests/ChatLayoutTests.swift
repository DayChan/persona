import Foundation
import SwiftUI
import Testing
@testable import Persona

/// The fixed conversation layout of Persona's main conversation (#2477, `ChatLayout`): always on with three-line exchanges,
/// only in Persona's conversations; supervision and scheduled runs are Persona writing to itself; the right-edge
/// navigator's ticks are the messages the user typed, placed to fit; links in an exchange open; the boxes read well.
@MainActor
struct ChatLayoutTests {
    private func defaults() -> UserDefaults {
        let name = "ChatLayoutTests-\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        return d
    }

    @Test func fixedPolicyShowsThreeLines() {
        let settings = ChatLayoutSettings(defaults: defaults())
        #expect(settings.on)
        #expect(settings.fold == .lines)
        #expect(settings.lines == 3)
        #expect(ChatLayout(on: true).shownLines == 3)
    }

    @Test(arguments: ["folded", "lines", "expanded", "invalid"], [0, 1, 5, 10, 40])
    func oldPreferencesAreIgnored(fold: String, lines: Int) {
        let d = defaults()
        d.set(false, forKey: "ui.newChatLayout")
        d.set(fold, forKey: "ui.newChatLayout.fold")
        d.set(lines, forKey: "ui.newChatLayout.lines")
        for _ in 0..<2 {
            let settings = ChatLayoutSettings(defaults: d)
            #expect(settings.on)
            #expect(settings.fold == .lines)
            #expect(settings.lines == 3)
            #if os(iOS)
            #expect(settings.layout(assistant: true) == ChatLayout(on: true))
            #expect(settings.layout(assistant: true).shownLines == 3)
            #endif
        }
        // Ignoring the old keys must not rewrite preferences used by an older app version.
        #expect(d.bool(forKey: "ui.newChatLayout") == false)
        #expect(d.string(forKey: "ui.newChatLayout.fold") == fold)
        #expect(d.integer(forKey: "ui.newChatLayout.lines") == lines)
    }

    @Test func oldLaunchArgumentsAreIgnored() {
        let d = defaults()
        d.setVolatileDomain(["ui.newChatLayout": "NO", "ui.newChatLayout.fold": "expanded",
                             "ui.newChatLayout.lines": "9"], forName: UserDefaults.argumentDomain)
        let settings = ChatLayoutSettings(defaults: d)
        #expect(settings.on && settings.fold == .lines && settings.lines == 3)
    }

    @Test func otherConversationsKeepTheirPlatformPresentation() {
        let settings = ChatLayoutSettings(defaults: defaults())
        #expect(settings.layout(assistant: false) == .off)
        #if os(iOS)
        #expect(settings.layout(assistant: true) == ChatLayout(on: true))
        #else
        #expect(settings.layout(assistant: true) == .off)
        #endif
        #expect(!ChatLayout.off.on)
    }

    @Test func supervisionAndScheduledRunsArePersonaToItself() {
        #expect(Injected(kind: "system", state: "supervise", line: "督办：2 个派出去的任务要处理").isPatrol) // i18n-ignore
        #expect(Injected(kind: "followup", state: "daily", title: "午间整理", line: "x").isPatrol) // i18n-ignore
        #expect(Injected(kind: "followup", state: "wake", line: "x").isPatrol)
        #expect(!Injected(kind: "task_notice", state: "done", line: "x").isPatrol)
        #expect(!Injected(kind: "system", state: "lost_tasks", line: "x").isPatrol)
        #expect(!Injected(kind: "system", state: "inbox", line: "x").isPatrol)
    }

    // MARK: The right-edge navigator

    private func user(_ seq: Int, _ text: String, clientMsgID: String? = "c\(UUID().uuidString)", from: String? = nil) -> ChatLine {
        var it = Item(id: "\(seq)#0", seq: seq, ts: nil, role: "user", kind: .text, text: text, toolUseID: nil, toolName: nil,
                      toolInput: nil, isError: nil, image: nil)
        it.clientMsgID = clientMsgID
        it.from = from
        return .row(.user(it))
    }

    @Test func ticksAreTheMessagesTheUserTyped() {
        let answer = Item(id: "2#0", seq: 2, ts: nil, role: "assistant", kind: .text, text: "好的", toolUseID: nil, toolName: nil, // i18n-ignore
                          toolInput: nil, isError: nil, image: nil)
        var supervise = Item(id: "4#0", seq: 4, ts: nil, role: "user", kind: .text, text: "📋 督办", toolUseID: nil, toolName: nil, // i18n-ignore
                             toolInput: nil, isError: nil, image: nil)
        supervise.injected = Injected(kind: "system", state: "supervise", line: "x")
        let lines: [ChatLine] = [
            user(1, "帮我修一下小组件"), // i18n-ignore
            .row(.assistant(answer)),
            user(3, "/loop 5m check"),
            .row(.user(supervise)),
            user(5, "另一段对话发来的", clientMsgID: nil, from: "conversation"), // i18n-ignore
            user(6, "  补测试  "), // i18n-ignore
        ]
        let marks = EdgeScrubState.marks(lines)
        #expect(marks.map(\.id) == ["1#0", "6#0"])
        #expect(marks.map(\.text) == ["帮我修一下小组件", "补测试"]) // i18n-ignore
    }

    @Test func aFewTicksSpreadAtMostTheWidestGapAndCenter() throws {
        let c = try #require(EdgeScrubColumn.fit(marks: 3, height: 600, top: 100, bottom: 40))
        #expect(c.first == 0 && c.count == 3)
        #expect(c.gap == Theme.Size.edgeTickGapMax)
        // Centered in the room between the margins: 100 + (460 - 28) / 2.
        #expect(abs(c.top - (100 + (460 - 2 * Theme.Size.edgeTickGapMax) / 2)) < 0.001)
        #expect(c.index(at: c.y(of: 1)) == 1)
        #expect(c.index(at: -1000) == 0)
        #expect(c.index(at: 1000) == 2)
    }

    @Test func manyTicksKeepTheNewestThatFit() throws {
        let c = try #require(EdgeScrubColumn.fit(marks: 500, height: 300, top: 0, bottom: 0))
        let fits = Int(300 / Theme.Size.edgeTickGapMin) + 1
        #expect(c.count == fits)
        #expect(c.first == 500 - fits)
        #expect(c.gap >= Theme.Size.edgeTickGapMin)
        #expect(c.index(at: 10_000) == 499) // the bottom tick is the newest message
        #expect(EdgeScrubColumn.fit(marks: 0, height: 300, top: 0, bottom: 0) == nil)
        #expect(EdgeScrubColumn.fit(marks: 4, height: 50, top: 40, bottom: 20) == nil)
    }

    @Test func movingPicksAndClosingHandsBackThePick() {
        let state = EdgeScrubState()
        let marks = (0..<5).map { EdgeMark(id: "m\($0)", text: "\($0)") }
        #expect(state.open(marks, height: 600, top: 60, at: 0))
        #expect(state.isOpen)
        #expect(state.pickedMark?.id == "m0")
        let column = state.column!
        #expect(state.move(to: column.y(of: 3)))
        #expect(!state.move(to: column.y(of: 3) + 1)) // same tick: no second haptic
        #expect(state.close()?.id == "m3")
        #expect(!state.isOpen)
        #expect(!state.open([], height: 600, top: 60, at: 0))
    }

    /// Letting go is a choice only once the finger has gone past the slop to another tick (#2615): the tick the
    /// navigator opens on is just where the finger happened to be.
    @Test func onlyAMoveToAnotherTickIsAChoice() throws {
        let state = EdgeScrubState()
        let marks = (0..<5).map { EdgeMark(id: "m\($0)", text: "\($0)") }
        #expect(state.open(marks, height: 600, top: 60, at: 0))
        #expect(!state.moved)
        let column = try #require(state.column)
        #expect(!state.move(to: column.y(of: 0) + 1)) // still the tick it opened on
        #expect(!state.moved)
        #expect(state.move(to: column.y(of: 2)))
        #expect(state.moved)
        #expect(state.move(to: column.y(of: 0))) // back to where it opened, on purpose: still a choice
        #expect(state.moved)
        #expect(state.close()?.id == "m0")
        #expect(!state.moved)
        // A finger held still trembles: over the line between two ticks it picks the next one (a haptic tick), but
        // within the slop that is no choice; going on past the slop is, even without another tick.
        let line = column.y(of: 2) + column.gap / 2 - 1
        #expect(state.open(marks, height: 600, top: 60, at: line))
        #expect(state.pickedMark?.id == "m2")
        #expect(state.move(to: line + 2))
        #expect(state.pickedMark?.id == "m3")
        #expect(!state.moved)
        #expect(!state.move(to: line + Theme.Space.s + 2))
        #expect(state.pickedMark?.id == "m3")
        #expect(state.moved)
        state.close()
        // The next slide starts without a choice, even on another tick.
        #expect(state.open(marks, height: 600, top: 60, at: 10_000))
        #expect(state.pickedMark?.id == "m4")
        #expect(!state.moved)
    }

    // MARK: Exchanges and boxes

    @Test func linksInAnExchangeOpen() {
        let text = "PR 在 https://github.com/example/weather/pull/45 。" // i18n-ignore
        let linked = ExchangeText.linked(text)
        let links = linked.runs.compactMap(\.link)
        #expect(links == [URL(string: "https://github.com/example/weather/pull/45")!])
        #expect(ExchangeText.linked("没有链接").runs.allSatisfy { $0.link == nil }) // i18n-ignore
    }

    /// An Agent's words show their Markdown, not its marks (#2363): bold without `**`, code without backticks, a
    /// heading without `#`s, lines kept, a Markdown link and a bare link both links.
    @Test func anExchangeRendersInlineMarkdown() {
        let text = "## Result\n**Status**: memory capped at `180 MB`.\n- see [the PR](https://github.com/example/weather/pull/45)\n- log https://example.com/log"
        let linked = ExchangeText.linked(text)
        let plain = String(linked.characters)
        #expect(plain == "Result\nStatus: memory capped at 180 MB.\n- see the PR\n- log https://example.com/log")
        #expect(linked.runs.contains { $0.inlinePresentationIntent?.contains(.stronglyEmphasized) == true })
        #expect(linked.runs.contains { $0.inlinePresentationIntent?.contains(.code) == true })
        #expect(linked.runs.compactMap(\.link) == [URL(string: "https://github.com/example/weather/pull/45")!, URL(string: "https://example.com/log")!])
        #expect(ExchangeText.withoutHeadingMarks("#123 is a PR, not a heading") == "#123 is a PR, not a heading")
        // A folded line's header: the words without the marks.
        #expect(ExchangeText.plain("**Fix** the `cache` first") == "Fix the cache first")
        // A report's heading after its kind's prefix ("Says it is done: ## Result") loses its marks too.
        let report = ReviewText.reportLine(kind: "done", text: ExchangeText.withoutHeadingMarks("## Result\n**Done**"))
        #expect(!String(ExchangeText.linked(report).characters).contains("#"))
        #expect(String(ExchangeText.linked(report).characters).hasSuffix("Result\nDone"))
    }

    /// The boxes' words are the primary color: black on the light boxes, white on the dark ones, both well past 4.5:1;
    /// and each box stands apart from the page (white / black) and from the other box.
    @Test func boxesReadWellInBothModes() {
        for (box, name) in [(Theme.ChatBox.userHex, "user"), (Theme.ChatBox.personaHex, "persona")] {
            #expect(PersonaPalette.contrast(box.light, 0x000000) >= 7, "\(name) light")
            #expect(PersonaPalette.contrast(box.dark, 0xFFFFFF) >= 7, "\(name) dark")
            #expect(PersonaPalette.contrast(box.light, 0xFFFFFF) > 1.05, "\(name) apart from a white page")
            #expect(PersonaPalette.contrast(box.dark, 0x000000) > 1.2, "\(name) apart from a black page")
        }
        #expect(Theme.ChatBox.userHex.light != Theme.ChatBox.personaHex.light)
        #expect(Theme.ChatBox.userHex.dark != Theme.ChatBox.personaHex.dark)
    }
}
