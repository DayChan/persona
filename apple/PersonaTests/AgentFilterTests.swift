import Foundation
import Testing
@testable import Persona

/// The Agents list's quick filters and what a space header says when folded (AgentsListTests drives the screens).
@MainActor struct AgentFilterTests {
    private func session(_ id: String, state: String, updated: TimeInterval = 0, since: TimeInterval = 0) -> Session {
        let f = ISO8601DateFormatter()
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"/tmp/p","status":"idle","state":"\(state)",
         "space_id":"w1","space_name":"app","updated_at":"\(f.string(from: Date(timeIntervalSince1970: updated)))",
         "state_since":"\(f.string(from: Date(timeIntervalSince1970: since)))"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    @Test func todoSpaceUsesOnlyWorkspaceIDsAndTheHubProjectKey() {
        var s = session("s", state: "working")
        // One group either way, so the nils below are the group's, not a list with no group in it.
        s.spaceID = nil
        #expect(SpaceGroup.build([s]).count == 1)
        #expect(SpaceGroup.build([s]).first?.todoSpace == nil)
        s.spaceID = ""
        #expect(SpaceGroup.build([s]).count == 1)
        #expect(SpaceGroup.build([s]).first?.todoSpace == nil)
        s.spaceID = "workspace"
        s.spaceName = "Project"
        #expect(SpaceGroup.build([s]).first?.todoSpace == "Project")
        s.spaceName = nil
        #expect(SpaceGroup.build([s]).first?.todoSpace == "workspace")
        s.spaceName = ""
        #expect(SpaceGroup.build([s]).first?.todoSpace == "workspace")
    }

    @Test func eachFilterKeepsItsState() {
        let all = [session("a", state: "needs_you"), session("b", state: "unread"), session("c", state: "unread"),
                   session("d", state: "working"), session("e", state: "idle")]
        #expect(all.filter { AgentFilter.all.matches($0) }.count == 5)
        #expect(all.filter { AgentFilter.needsYou.matches($0) }.map(\.sessionID) == ["a"])
        #expect(all.filter { AgentFilter.unread.matches($0) }.map(\.sessionID) == ["b", "c"])
        #expect(all.filter { AgentFilter.working.matches($0) }.map(\.sessionID) == ["d"])
        #expect(AgentFilter.all.state == nil)
    }

    /// Lark Group is a botmux session, Terminal a herdr pane (#2526); other hosts and paneless sessions are neither.
    @Test func sourceFiltersSplitBotmuxFromHerdr() {
        var botmux = session("b", state: "idle"), herdr = session("h", state: "working"), orca = session("o", state: "idle")
        let bare = session("n", state: "idle")
        botmux.paneID = "botmux:abc"
        herdr.paneID = "w1:p1"
        orca.paneID = "orca:1"
        #expect(botmux.source == .feishu)
        #expect(herdr.source == .terminal)
        #expect(orca.source == nil && bare.source == nil)
        let all = [botmux, herdr, orca, bare]
        #expect(all.filter { AgentFilter.feishu.matches($0) }.map(\.sessionID) == ["b"])
        #expect(all.filter { AgentFilter.terminal.matches($0) }.map(\.sessionID) == ["h"])
        #expect(AgentFilter.feishu.state == nil && AgentFilter.feishu.title == "飞书群")
    }

    /// Muted agents sit under Muted: Unread and Working neither count nor list them; Needs You still does.
    @Test func mutedAgentsAreNotUnreadOrWorking() {
        let unread = session("b", state: "unread"), working = session("d", state: "working"), needs = session("a", state: "needs_you")
        #expect(!AgentFilter.unread.matches(unread, muted: true))
        #expect(!AgentFilter.working.matches(working, muted: true))
        #expect(AgentFilter.needsYou.matches(needs, muted: true))
        #expect(AgentFilter.all.matches(unread, muted: true))
        #expect(AgentFilter.unread.matches(unread, muted: false))
    }

    @Test func foldedHeaderSpeaksItsCounts() {
        let g = SpaceGroup.build([session("a", state: "needs_you"), session("b", state: "unread"), session("c", state: "unread"),
                                  session("d", state: "idle")]).first!
        #expect(StateCounts.spoken(g) == "1 个需要你，2 个完成未看，1 个空闲")
    }

    /// Spaces that changed at the same moment keep one order: they swapped between renders before.
    @Test func spacesWithTheSameTimeKeepTheirOrder() {
        let a = session("a", state: "unread", updated: 100)
        var b = session("b", state: "unread", updated: 100)
        b.spaceID = "w2"
        b.spaceName = "web"
        let ids = SpaceGroup.build([a, b]).map(\.id)
        for _ in 0..<20 { #expect(SpaceGroup.build([b, a]).map(\.id) == ids) }
    }

    /// Working spaces keep their order while their agents work: by when they started, not their latest line. The real
    /// hub pushes every two seconds; ordered by the latest update the spaces traded places under the finger (2026-11-01).
    @Test func workingSpacesKeepTheirOrderAsTheyWork() {
        func space(_ id: String, _ name: String, updated: TimeInterval, since: TimeInterval) -> Session {
            var s = session(id, state: "working", updated: updated, since: since)
            s.spaceID = name
            s.spaceName = name
            return s
        }
        let started = SpaceGroup.build([space("a", "api", updated: 500, since: 100), space("b", "web", updated: 300, since: 200)]).map(\.spaceID)
        #expect(started == ["web", "api"]) // the later start leads, whoever wrote last
        let later = SpaceGroup.build([space("a", "api", updated: 300, since: 100), space("b", "web", updated: 700, since: 200)]).map(\.spaceID)
        #expect(later == started)
        // Finished: by when they changed, as before.
        var docs = session("c", state: "unread", updated: 900, since: 400), site = session("d", state: "unread", updated: 800, since: 600)
        docs.spaceID = "docs"
        site.spaceID = "site"
        #expect(SpaceGroup.build([site, docs]).map(\.spaceID) == ["docs", "site"])
    }

    // What the list's structure reads (docs/apple/ios-state.md, step 1): a push that changes only what a row shows leaves
    // the list alone; the row follows its own agent.
    @Test func onlyRowsFollowWhatOnlyRowsShow() {
        let a = session("a", state: "working", updated: 600, since: 60)
        var b = a
        b.rev = 42
        b.lastText = "a new line"
        b.updatedAt = Date(timeIntervalSince1970: 630) // the same minute
        #expect(AgentsFeed.shape([a], searching: false) == AgentsFeed.shape([b], searching: false))
        // While searching the last lines are what it filters by.
        #expect(AgentsFeed.shape([a], searching: true) != AgentsFeed.shape([b], searching: true))
        var c = a
        c.updatedAt = Date(timeIntervalSince1970: 700) // the next minute: only time passed (step 3)
        #expect(AgentsFeed.shape([a], searching: false) == AgentsFeed.shape([c], searching: false))
        var d = a
        d.state = .needsYou
        #expect(AgentsFeed.shape([a], searching: false) != AgentsFeed.shape([d], searching: false))
    }

    // A machine's heartbeat (its last_seen, on every push of a busy hub) is not what the list shows: the list is not
    // drawn again for it (#1495); its name, being online and its error are.
    @Test func aHeartbeatLeavesTheListsMachinesAlone() throws {
        let m = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"m","name":"MacBook","kind":"local","online":true,"last_seen":"2026-11-04T10:00:00Z"}"#.utf8))
        var beat = m
        beat.lastSeen = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(beat != m)
        #expect(AgentsFeed.sameListing([beat], [m]))
        var down = m
        down.online = false
        #expect(!AgentsFeed.sameListing([down], [m]))
        #expect(!AgentsFeed.sameListing([down], [beat])) // a heartbeat with a change in it is a change
        var renamed = m
        renamed.name = "Mac Studio"
        #expect(!AgentsFeed.sameListing([renamed], [m]))
        #expect(!AgentsFeed.sameListing([m], [m, m]))
    }

    /// A running subagent's write time and last line tick with each of its steps (#5602): its session's chat page shows
    /// neither, so it is not built again; whether it runs is a change.
    @Test func aSubagentsTicksAreNoChangeForItsSessionsPage() {
        var a = session("a", state: "working")
        let start = Date(timeIntervalSince1970: 100)
        a.subagents = [Subagent(id: "x", sessionID: "a@x", type: "Explore", description: "看代码", toolUseID: "t", running: true,
                                startedAt: start, updatedAt: Date(timeIntervalSince1970: 200), lastText: "读 a.swift")]
        var b = a
        b.subagents?[0].updatedAt = Date(timeIntervalSince1970: 260)
        b.subagents?[0].lastText = "读 b.swift"
        #expect(Session.sameForItsPage(a, b))
        var c = b
        c.subagents?[0].running = false
        #expect(!Session.sameForItsPage(a, c))
        // No start time: its write time is what the sheet shows, so that is a change.
        var d = a, e = a
        d.subagents?[0].startedAt = nil
        e.subagents?[0].startedAt = nil
        e.subagents?[0].updatedAt = Date(timeIntervalSince1970: 260)
        #expect(!Session.sameForItsPage(d, e))
    }

    /// A working agent's steps tick `rev` and `updatedAt` only: views about the agent that show neither (its chat, its
    /// row apart from the time) take that as no change; anything they show is one.
    @Test func ticksAreNoChangeForWhatShowsNeither() {
        let a = session("a", state: "working", updated: 600)
        var b = a
        b.rev = 7
        b.updatedAt = Date(timeIntervalSince1970: 900)
        #expect(Session.sameApartFromTicks(a, b))
        var c = b
        c.lastText = "a new line"
        #expect(!Session.sameApartFromTicks(a, c))
        var d = a
        d.state = .needsYou
        #expect(!Session.sameApartFromTicks(a, d))
        #expect(Session.sameApartFromTicks(nil, nil))
        #expect(!Session.sameApartFromTicks(a, nil))
    }

    /// Views about one session (its chat, its top bar) show neither the ticks nor its last line: a new last line is
    /// no change for them; what they show is (docs/apple/ios-state.md「Step 3」).
    @Test func aNewLastLineIsNoChangeForItsPage() {
        let a = session("a", state: "working", updated: 600)
        var b = a
        b.rev = 7
        b.updatedAt = Date(timeIntervalSince1970: 900)
        b.lastText = "a new line"
        b.activity = "在跑测试"
        b.activityAt = Date(timeIntervalSince1970: 880)
        #expect(Session.sameForItsPage(a, b))
        #expect(!Session.sameApartFromTicks(a, b)) // the task cards (sessionWithLine) do draw a new step
        var c = b
        c.permissionMode = "plan"
        #expect(!Session.sameForItsPage(a, c))
        var d = a
        d.state = .needsYou
        #expect(!Session.sameForItsPage(a, d))
        #expect(!Session.sameForItsPage(a, nil))
    }

    /// Time changes the list only through the order it puts agents in (rows, and spaces by their agents): a minute
    /// passing is no change, an agent moving ahead of another is (docs/apple/ios-state.md「Step 3」).
    @Test func timeIsStructureOnlyThroughTheOrder() {
        let a = session("a", state: "unread", updated: 600), b = session("b", state: "unread", updated: 500)
        var a2 = a
        a2.updatedAt = Date(timeIntervalSince1970: 660) // later, still ahead of b
        #expect(AgentsFeed.shape([a, b], searching: false) == AgentsFeed.shape([a2, b], searching: false))
        var b2 = b
        b2.updatedAt = Date(timeIntervalSince1970: 700) // b moves ahead of a
        #expect(AgentsFeed.shape([a, b], searching: false) != AgentsFeed.shape([a, b2], searching: false))
        // A working agent sorts by when it started working: its steps never reorder.
        let w = session("w", state: "working", updated: 800, since: 100)
        var w2 = w
        w2.updatedAt = Date(timeIntervalSince1970: 400)
        #expect(AgentsFeed.shape([w, a], searching: false) == AgentsFeed.shape([w2, a], searching: false))
    }

    @Test func recentFeedReordersOnlyWhenActivityPassesAnotherRow() {
        let a = session("a", state: "working", updated: 600, since: 100)
        let b = session("b", state: "working", updated: 500, since: 200)
        var newer = b
        newer.updatedAt = Date(timeIntervalSince1970: 700)
        #expect(AgentsFeed.shape([a, b], searching: false) == AgentsFeed.shape([a, newer], searching: false))
        #expect(AgentsFeed.shape([a, b], searching: false, recent: true) != AgentsFeed.shape([a, newer], searching: false, recent: true))
        newer.updatedAt = Date(timeIntervalSince1970: 550)
        #expect(AgentsFeed.shape([a, b], searching: false, recent: true) == AgentsFeed.shape([a, newer], searching: false, recent: true))
    }
}
