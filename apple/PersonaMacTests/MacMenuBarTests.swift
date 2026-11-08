import Foundation
import Testing
@testable import Persona

/// The menu bar item (docs/apple/mac.md: Dynamic Island / Lock Screen Live Activity → menu bar): what needs you, what's working, what finished.
@MainActor
struct MacMenuBarTests {
    private func session(_ id: String, _ state: AgentState, assistant: Bool = false, ago: TimeInterval = 0) -> Session {
        var s = try! JSON.decoder.decode(Session.self, from: Data("""
        {"machine_id":"m","session_id":"\(id)","title":"t-\(id)","cwd":"/w/proj","status":"idle","state":"\(state.rawValue)",
         "started_by_persona":false,"assistant":\(assistant),"pending_interactions":0}
        """.utf8))
        s.stateSince = Date(timeIntervalSince1970: 1_000_000 - ago)
        return s
    }

    @Test func groupsByWhatTheyWantFromYou() {
        let list = [session("w", .working), session("n", .needsYou), session("u", .unread), session("i", .idle),
                    session("j", .needsYou, assistant: true)]
        let s = MacMenuBar.sections(list, muted: { _ in false })
        #expect(s.needsYou.map(\.sessionID) == ["n", "j"])
        #expect(s.working.map(\.sessionID) == ["w"])
        #expect(s.unread.map(\.sessionID) == ["u"])
        #expect(s.isEmpty == false)
        #expect(MacMenuBar.sections([session("i", .idle)], muted: { _ in false }).isEmpty)
    }

    @Test func longestWaitingFirstAndMutedLeftOut() {
        let list = [session("a", .needsYou, ago: 10), session("b", .needsYou, ago: 300), session("c", .needsYou, ago: 60)]
        #expect(MacMenuBar.sections(list, muted: { $0.sessionID == "c" }).needsYou.map(\.sessionID) == ["b", "a"])
    }

    /// Agents of a quiet task (#1731) are the main conversation's to tell: the shared menu filter drops them in
    /// every state before the menu bar or the Dock menu groups sessions into sections.
    @Test func quietTaskSessionsAreFilteredBeforeSections() {
        let list = [session("qn", .needsYou), session("w", .working), session("qu", .unread), session("n", .needsYou)]
        let visible = MacMenuBar.menuSessions(list, quiet: { $0.sessionID.hasPrefix("q") })
        #expect(visible.map(\.sessionID) == ["w", "n"])
    }

    @Test func finishedListIsShort() {
        let list = (0..<12).map { session("u\($0)", .unread, ago: TimeInterval($0)) }
        #expect(MacMenuBar.sections(list, muted: { _ in false }).unread.count == MacMenuBar.unreadLimit)
    }

    @Test func quickAnswersAreTheCardsOneTapOnes() throws {
        func ix(_ json: String) throws -> Interaction { try JSON.decoder.decode(Interaction.self, from: Data(json.utf8)) }
        let permission = try ix(#"{"id":"p","machine_id":"m","session_id":"a","kind":"permission","tool_name":"Bash"}"#)
        #expect(MacMenuBar.quickAnswers(permission)?.map(\.title) == ["允许", "拒绝"])
        #expect(MacMenuBar.quickAnswers(permission)?.first?.resolution.allow == true)
        // Something to fill in first (a password card): Deny only, the password goes in from the card.
        let fill = try ix(#"{"id":"f","machine_id":"m","session_id":"a","kind":"permission","tool_name":"secret_run","fields":[{"key":"password","label":"Password","secret":true}]}"#)
        #expect(MacMenuBar.quickAnswers(fill)?.map(\.resolution.allow) == [false])
        let plan = try ix(#"{"id":"p","machine_id":"m","session_id":"a","kind":"plan","plan":"改登录"}"#)
        #expect(MacMenuBar.quickAnswers(plan)?.first?.resolution.mode == "acceptEdits")
        let one = try ix(#"{"id":"q","machine_id":"m","session_id":"a","kind":"question","questions":[{"question":"用哪个?","multi_select":false,"options":[{"label":"A"},{"label":"B"}]}]}"#)
        #expect(MacMenuBar.quickAnswers(one)?.map(\.title) == ["A", "B"])
        #expect(MacMenuBar.quickAnswers(one)?.last?.resolution.answers == ["用哪个?": "B"])
        let multi = try ix(#"{"id":"q","machine_id":"m","session_id":"a","kind":"question","questions":[{"question":"哪些?","multi_select":true,"options":[{"label":"A"}]}]}"#)
        #expect(MacMenuBar.quickAnswers(multi) == nil)
        // "(Recommended)" reads as Recommended; the answer keeps the label as it came.
        let rec = try ix(#"{"id":"q","machine_id":"m","session_id":"a","kind":"question","questions":[{"question":"框架?","multi_select":false,"options":[{"label":"Swift Testing (Recommended)"},{"label":"XCTest"}]}]}"#)
        #expect(MacMenuBar.quickAnswers(rec)?.first?.title == "Swift Testing（推荐）")
        #expect(MacMenuBar.quickAnswers(rec)?.first?.resolution.answers == ["框架?": "Swift Testing (Recommended)"])
    }

    // What the icon and the menu draw (#1495): a working agent's new last line or time leaves it equal, so neither is
    // drawn again for it; a new title, a state change or a new question is a change.
    @Test func theMenuChangesOnlyWithWhatItShows() throws {
        var w = session("w", .working), n = session("n", .needsYou)
        func shown(_ list: [Session], _ ix: [Interaction] = []) -> MacMenuBar.Shown {
            MacMenuBar.shown(MacMenuBar.sections(list, muted: { _ in false }), paired: true, interactions: ix, machineName: { "Mac-" + $0 })
        }
        let before = shown([w, n])
        #expect(before.needsYou.map(\.label) == ["t-n · Mac-m"])
        #expect(before.working.map(\.label) == ["t-w · Mac-m"])
        w.lastText = "a new line"
        w.updatedAt = Date(timeIntervalSince1970: 2_000_000)
        #expect(shown([w, n]) == before)
        w.title = "renamed"
        #expect(shown([w, n]) != before)
        let ix = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"p","machine_id":"m","session_id":"n","kind":"permission","tool_name":"Bash"}"#.utf8))
        let asking = shown([session("w", .working), n], [ix])
        #expect(asking != before)
        #expect(asking.needsYou.first?.interaction?.id == "p")
        n.state = .unread
        #expect(shown([session("w", .working), n]).needsYou.isEmpty)
    }

    @Test func titleShowsTheCount() {
        #expect(MacMenuBar.count(needsYou: 0) == nil)
        #expect(MacMenuBar.count(needsYou: 3) == "3")
    }

    /// The icon's number counts waiting cards of the menu's Needs You sessions, not sessions (#4081): one agent on a
    /// second card shows 2, matching the Dock badge and the iPhone's Agents tab.
    @Test func iconCountsCardsNotSessions() throws {
        func ix(_ id: String, _ session: String) throws -> Interaction {
            try JSON.decoder.decode(Interaction.self, from: Data(
                #"{"id":"\#(id)","machine_id":"m","session_id":"\#(session)","kind":"permission"}"#.utf8))
        }
        let sections = MacMenuBar.sections([session("a", .needsYou), session("w", .working)], muted: { _ in false })
        #expect(MacMenuBar.needsYouCount(sections, interactions: []) == 0)
        let cards = try [ix("a1", "a"), ix("a2", "a"), ix("w1", "w")]
        // Both of a's cards count; w is working, so its (out-of-sync) card is not on the menu's number.
        #expect(MacMenuBar.needsYouCount(sections, interactions: cards) == 2)
    }
}
