import Foundation
import SwiftUI
import Testing
@testable import Persona

/// The new conversation layout of Persona's main conversation (#1666, `ChatLayout`): on by default and off on request,
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

    @Test func onByDefaultAndRemembered() {
        let d = defaults()
        let s = ChatLayoutSettings(defaults: d)
        #expect(s.on)
        #expect(s.fold == .lines)
        #expect(s.lines == 3)
        s.on = false
        s.fold = .folded
        s.lines = 5
        let again = ChatLayoutSettings(defaults: d)
        #expect(!again.on)
        #expect(again.fold == .folded)
        #expect(again.lines == 5)
    }

    /// `-ui.newChatLayout NO` as a launch argument reaches UserDefaults as a string.
    @Test func aLaunchArgumentTurnsItOff() {
        let d = defaults()
        d.set("NO", forKey: ChatLayoutSettings.key)
        #expect(!ChatLayoutSettings(defaults: d).on)
        d.set("YES", forKey: ChatLayoutSettings.key)
        #expect(ChatLayoutSettings(defaults: d).on)
    }

    /// Off, or anywhere but Persona's conversations, every chat draws as before (`ChatLayout.off`).
    @Test func offRestoresTheOldLookEverywhere() {
        let s = ChatLayoutSettings(defaults: defaults())
        #expect(s.layout(assistant: false) == .off)
        #if os(iOS)
        #expect(s.layout(assistant: true) == ChatLayout(on: true, fold: .lines, lines: 3))
        #endif
        s.on = false
        #expect(s.layout(assistant: true) == .off)
        #expect(!ChatLayout.off.on)
    }

    @Test func foldsToTheChosenLines() {
        #expect(ChatLayout(on: true, fold: .folded).shownLines == 0)
        #expect(ChatLayout(on: true, fold: .lines, lines: 4).shownLines == 4)
        #expect(ChatLayout(on: true, fold: .expanded).shownLines == nil)
    }

    @Test func outOfRangeLinesFallBackToThree() {
        let d = defaults()
        d.set(40, forKey: ChatLayoutSettings.linesKey)
        #expect(ChatLayoutSettings(defaults: d).lines == ChatLayoutSettings.defaultLines)
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

    @Test func manyTicksKeepEveryFetchedMessageReachable() throws {
        let c = try #require(EdgeScrubColumn.fit(marks: 500, height: 300, top: 0, bottom: 0))
        #expect(c.count == 500)
        #expect(c.first == 0)
        #expect(c.gap < Theme.Size.edgeTickGapMin)
        #expect(c.index(at: -1000) == 0)
        #expect(c.index(at: 10_000) == 499)
        #expect(c.drawnIndices(picked: 257).contains(257))
        #expect(c.drawnIndices(picked: nil).count <= Int(300 / Theme.Size.edgeTickGapMin) + 2)
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

    // MARK: Exchanges and boxes

    @Test func linksInAnExchangeOpen() {
        let text = "PR 在 https://github.com/example/weather/pull/45 。" // i18n-ignore
        let linked = ExchangeText.linked(text)
        let links = linked.runs.compactMap(\.link)
        #expect(links == [URL(string: "https://github.com/example/weather/pull/45")!])
        #expect(ExchangeText.linked("没有链接").runs.allSatisfy { $0.link == nil }) // i18n-ignore
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
