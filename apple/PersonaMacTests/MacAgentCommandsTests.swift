import Foundation
import Testing
@testable import Persona

/// The Agent menu (docs/apple/mac.md「菜单和快捷键」): acts on the chat on screen.
@MainActor
struct MacAgentCommandsTests {
    private func ix(_ json: String) throws -> Interaction { try JSON.decoder.decode(Interaction.self, from: Data(json.utf8)) }

    private func session(_ id: String, _ state: AgentState, ago: TimeInterval = 0, assistant: Bool = false) -> Session {
        var s = try! JSON.decoder.decode(Session.self, from: Data("""
        {"machine_id":"m","session_id":"\(id)","title":"t","cwd":"/w","status":"idle","state":"\(state.rawValue)",
         "started_by_persona":false,"assistant":\(assistant),"pending_interactions":0}
        """.utf8))
        s.stateSince = Date(timeIntervalSince1970: 1_000_000 - ago)
        return s
    }

    @Test func approveAllowsAToolOrAPlan() throws {
        let p = try ix(#"{"id":"1","machine_id":"m","session_id":"a","kind":"permission"}"#)
        #expect(MacAgentCommands.approve(p)?.allow == true)
        let plan = try ix(#"{"id":"2","machine_id":"m","session_id":"a","kind":"plan","plan":"x"}"#)
        #expect(MacAgentCommands.approve(plan)?.approve == true)
        #expect(MacAgentCommands.approve(plan)?.mode == "acceptEdits")
        // A question or a terminal dialog has no single "yes".
        let q = try ix(#"{"id":"3","machine_id":"m","session_id":"a","kind":"question","questions":[]}"#)
        #expect(MacAgentCommands.approve(q) == nil)
        // A password card has nothing to allow without its password: only its card can.
        let fill = try ix(#"{"id":"4","machine_id":"m","session_id":"a","kind":"permission","tool_name":"secret_run","fields":[{"key":"password","label":"Password","secret":true}]}"#)
        #expect(MacAgentCommands.approve(fill) == nil)
        #expect(MacAgentCommands.reject(fill)?.allow == false)
    }

    @Test func rejectOnlyDeniesATool() throws {
        let p = try ix(#"{"id":"1","machine_id":"m","session_id":"a","kind":"permission"}"#)
        #expect(MacAgentCommands.reject(p)?.allow == false)
        // Sending a plan back needs words: that is the chat's Send Back.
        let plan = try ix(#"{"id":"2","machine_id":"m","session_id":"a","kind":"plan","plan":"x"}"#)
        #expect(MacAgentCommands.reject(plan) == nil)
    }

    /// ⌥⌘1…9 answer a single question by its options, in order, at most nine.
    @Test func choicesAreASingleQuestionsOptions() throws {
        let labels = (1...11).map { #"{"label":"选项\#($0)"}"# }.joined(separator: ",")
        let q = try ix(#"{"id":"q","machine_id":"m","session_id":"a","kind":"question","questions":[{"question":"用哪个?","multi_select":false,"options":[\#(labels)]}]}"#)
        let c = MacAgentCommands.choices(q)
        #expect(c.count == 9)
        #expect(c.first?.title == "选项1")
        #expect(c.dropFirst(2).first?.resolution.answers == ["用哪个?": "选项3"])
    }

    /// Claude's "(Recommended)" reads as the card's Recommended in the menu; the answer sent back keeps the label as it came.
    @Test func aRecommendedChoiceSaysSo() throws {
        let q = try ix(#"{"id":"q","machine_id":"m","session_id":"a","kind":"question","questions":[{"question":"框架?","multi_select":false,"options":[{"label":"Swift Testing (Recommended)"},{"label":"XCTest"}]}]}"#)
        let c = MacAgentCommands.choices(q)
        #expect(c.map(\.title) == ["Swift Testing（推荐）", "XCTest"])
        #expect(c.first?.resolution.answers == ["框架?": "Swift Testing (Recommended)"])
    }

    /// Several questions or a multi-select need the card; a tool or a plan has Approve / Deny.
    @Test func noChoicesWhenOneKeyCannotAnswer() throws {
        let multi = try ix(#"{"id":"q","machine_id":"m","session_id":"a","kind":"question","questions":[{"question":"哪些?","multi_select":true,"options":[{"label":"A"}]}]}"#)
        #expect(MacAgentCommands.choices(multi).isEmpty)
        let two = try ix(#"{"id":"q","machine_id":"m","session_id":"a","kind":"question","questions":[{"question":"一","multi_select":false,"options":[{"label":"A"}]},{"question":"二","multi_select":false,"options":[{"label":"B"}]}]}"#)
        #expect(MacAgentCommands.choices(two).isEmpty)
        let p = try ix(#"{"id":"1","machine_id":"m","session_id":"a","kind":"permission"}"#)
        #expect(MacAgentCommands.choices(p).isEmpty)
    }

    @Test func nextNeedsYouCyclesLongestWaitingFirst() {
        let list = [session("a", .needsYou, ago: 10), session("b", .needsYou, ago: 300), session("c", .working),
                    session("d", .needsYou, ago: 60), session("j", .needsYou, ago: 999, assistant: true)]
        let k = { SessionKey(machineID: "m", sessionID: $0) }
        #expect(MacAgentCommands.nextNeedsYou(after: nil, in: list, muted: { _ in false }) == k("j"))
        #expect(MacAgentCommands.nextNeedsYou(after: k("j"), in: list, muted: { _ in false }) == k("b"))
        #expect(MacAgentCommands.nextNeedsYou(after: k("b"), in: list, muted: { _ in false }) == k("d"))
        #expect(MacAgentCommands.nextNeedsYou(after: k("a"), in: list, muted: { _ in false }) == k("j"))
        #expect(MacAgentCommands.nextNeedsYou(after: nil, in: [session("c", .working)], muted: { _ in false }) == nil)
    }

    /// ⌃⌘N passes `isMuted || isQuiet` as the muted closure, so an agent of a quiet task (#1731) is never a target:
    /// the longest-waiting quiet agent is skipped, cycling wraps within the visible agents, and a quiet agent that is
    /// the only one waiting leaves nothing to jump to (the menu item stays disabled).
    @Test func nextNeedsYouSkipsAgentsHiddenAsQuiet() {
        let list = [session("a", .needsYou, ago: 10), session("q", .needsYou, ago: 300),
                    session("d", .needsYou, ago: 60)]
        let k = { SessionKey(machineID: "m", sessionID: $0) }
        let hidden: (Session) -> Bool = { $0.sessionID == "q" }
        #expect(MacAgentCommands.nextNeedsYou(after: nil, in: list, muted: hidden) == k("d"))
        #expect(MacAgentCommands.nextNeedsYou(after: k("d"), in: list, muted: hidden) == k("a"))
        #expect(MacAgentCommands.nextNeedsYou(after: k("a"), in: list, muted: hidden) == k("d"))
        #expect(MacAgentCommands.nextNeedsYou(after: k("q"), in: list, muted: hidden) == k("d"))
        #expect(MacAgentCommands.nextNeedsYou(after: nil, in: [session("q", .needsYou, ago: 1)], muted: hidden) == nil)
    }
}

/// Which chat the Agent menu acts on: the key window's (a chat can be open in the main window and in a window of its
/// own at once), not just the last one that appeared.
struct MacFocusedChatTests {
    let a = SessionKey(machineID: "m", sessionID: "a"), b = SessionKey(machineID: "m", sessionID: "b")

    @Test func theKeyWindowsChat() {
        #expect(MacShellState.focusedChat(keyWindow: 1, chats: [1: a, 2: b], fallback: b) == a)
    }

    /// The key window shows no chat (Knowledge, settings): nothing to act on, not the other window's chat.
    @Test func aKeyWindowWithoutAChat() {
        #expect(MacShellState.focusedChat(keyWindow: 3, chats: [1: a], fallback: a) == nil)
    }

    /// No key window (the app is in the background, a menu bar menu is open): the last chat shown.
    @Test func noKeyWindow() {
        #expect(MacShellState.focusedChat(keyWindow: nil, chats: [1: a], fallback: b) == b)
    }
}
