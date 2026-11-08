import Foundation
import Testing
@testable import Persona

/// The Agents list's order and sections (`AgentListOrder`), without the list.
@MainActor struct AgentListOrderTests {
    private func session(_ id: String, _ state: String, updated: TimeInterval = 0, machine: String = "m") -> Session {
        let json = """
        {"machine_id":"\(machine)","session_id":"\(id)","title":"\(id)","cwd":"/tmp/p","status":"idle","state":"\(state)",
         "space_id":"w1","space_name":"app","updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated)))"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private func order(pinned: Set<String> = [], muted: Set<String> = [], byRank: Bool = true) -> AgentListOrder {
        AgentListOrder(isPinned: { pinned.contains($0.sessionID) }, isMuted: { muted.contains($0.sessionID) }, byRank: byRank)
    }

    @Test func rowMenuForwardsEachActionOnceAndUsesUpdatedCallbacks() {
        let actions = AgentRowMenu.Actions()
        let first = session("first", "working")
        let second = session("second", "working")
        actions.close(first)
        actions.rename(first)
        var events: [String] = []
        actions.update(rename: { events.append("rename:" + $0.sessionID) },
                       close: { events.append("close:" + $0.sessionID) })
        actions.rename(first)
        actions.close(second)
        #expect(events == ["rename:first", "close:second"])
        actions.update(rename: { events.append("new-rename:" + $0.sessionID) },
                       close: { events.append("new-close:" + $0.sessionID) })
        actions.close(first)
        actions.rename(second)
        #expect(events == ["rename:first", "close:second", "new-close:first", "new-rename:second"])
    }

    @Test func statesComeInOneOrder() {
        let list = [session("idle", "idle", updated: 50), session("work", "working", updated: 10),
                    session("unread", "unread", updated: 20), session("needs", "needs_you", updated: 1)]
        #expect(order().ordered(list).map(\.sessionID) == ["needs", "unread", "work", "idle"])
    }

    @Test func mutedLiveAgentsGoBelowTheRestButNeedsYouStaysFirst() {
        let list = [session("a", "unread"), session("b", "working"), session("c", "needs_you"), session("d", "idle")]
        let o = order(muted: ["a", "c"])
        #expect(o.rank(list[0]) == AgentListOrder.stateKeys.firstIndex(of: "muted"), "muted and unread ranks as 已屏蔽")
        #expect(o.rank(list[2]) == 0, "muted but needs you stays first")
        #expect(o.ordered(list).map(\.sessionID) == ["c", "b", "a", "d"])
    }

    @Test func sameRankNewestFirstThenById() {
        let list = [session("b", "unread", updated: 5), session("a", "unread", updated: 5), session("c", "unread", updated: 9)]
        #expect(order().ordered(list).map(\.sessionID) == ["c", "a", "b"])
    }

    @Test func quietSpaceRowsFollowActivityWhileTheSpacesTabsKeepTheirOrder() throws {
        var first = session("first", "idle", updated: 19), second = session("second", "idle", updated: 8)
        var latest = session("latest", "idle", updated: 59)
        first.slotIndex = 0
        second.slotIndex = 1
        latest.slotIndex = 2
        let group = try #require(SpaceGroup.build([latest, second, first]).first)
        #expect(group.isQuiet)
        #expect(group.sessions.map(\.sessionID) == ["first", "second", "latest"])
        #expect(order().ordered(group.sessions).map(\.sessionID) == ["latest", "first", "second"])
        let rows = AgentListRows(order: order(), grouping: .space, isFolded: { _ in false }, expandedQuiet: [group.id])
        let visible = rows.spaces(active: [], quiet: [group], hosts: nil)
        #expect(visible.map(\.sessionID) == ["latest", "first", "second"], "navigation follows the displayed rows")
        #expect(AgentListRows.step(visible.map(\.key), from: latest.key, by: 1) == first.key)
        #expect(group.sessions.map(\.slotIndex) == [0, 1, 2], "list order must not rewrite the original tabs")
    }

    @Test func underAQuickFilterOnlyTimeCounts() {
        let list = [session("needs", "needs_you", updated: 1), session("idle", "idle", updated: 9)]
        #expect(order(byRank: false).ordered(list).map(\.sessionID) == ["idle", "needs"])
    }

    @Test func sortedByLatestUpdateOnlyTimeCounts() {
        // Recently Updated (`AgentSort.updated`) orders rows the way a quick filter does: the state does not lift a row.
        let list = [session("needs", "needs_you", updated: 1), session("unread", "unread", updated: 5),
                    session("idle", "idle", updated: 9)]
        #expect(order(byRank: false).ordered(list).map(\.sessionID) == ["idle", "unread", "needs"])
    }

    @Test func latestFirstOrdersGroupsByTimeThenId() {
        let items: [(id: String, at: TimeInterval)] = [("b", 5), ("c", 1), ("a", 5), ("d", 9)]
        let sorted = AgentListOrder.latestFirst(items, date: { Date(timeIntervalSince1970: $0.at) }, id: { $0.id })
        #expect(sorted.map { $0.id } == ["d", "a", "b", "c"])
    }

    @Test func pinnedIdleAgentsStayOutOfTheFold() {
        let o = order(pinned: ["p"])
        #expect(o.stays(session("p", "idle")))
        #expect(!o.stays(session("q", "idle")))
        #expect(o.stays(session("w", "working")))
    }

    @Test func sectionsAreTheRanksAndMachinesInUse() {
        let list = [session("a", "needs_you", machine: "x"), session("b", "idle", machine: "y"), session("c", "offline", machine: "z")]
        #expect(order().sectionIDs(list, byMachine: false) == ["state/needs", "state/idle"])
        #expect(Set(order().sectionIDs(list, byMachine: true)) == ["machine/x", "machine/y"], "offline agents make no section")
    }

    @Test func recentActivityMixesProjectsStatesAndOfflineHistory() {
        var seen = session("seen", "idle", updated: 30, machine: "laptop")
        seen.spaceID = "docs"
        var working = session("work", "working", updated: 40, machine: "server")
        working.stateSince = Date(timeIntervalSince1970: 1)
        let list = [session("needs", "needs_you", updated: 10), seen,
                    session("history", "offline", updated: 50), working]
        #expect(AgentListOrder.recent(list).map(\.sessionID) == ["history", "work", "seen", "needs"])
    }

    @Test func readingAReplyKeepsItsRecentPosition() {
        var latest = session("latest", "unread", updated: 20)
        let older = session("older", "working", updated: 10)
        latest.state = .idle
        latest.seenAt = Date(timeIntervalSince1970: 100)
        #expect(AgentListOrder.recent([older, latest]).map(\.sessionID) == ["latest", "older"])
    }

    @Test func recentTiesUseTheWholeIdentityAndMissingTimesComeLast() {
        let a = session("same", "idle", updated: 20, machine: "a")
        let b = session("same", "idle", updated: 20, machine: "b")
        var unknown = session("unknown", "idle")
        unknown.updatedAt = nil
        #expect(AgentListOrder.recent([unknown, b, a]).map(\.id) == ["a/same", "b/same", "m/unknown"])
        #expect(AgentListOrder.recent([a, unknown, b]).map(\.id) == ["a/same", "b/same", "m/unknown"])
    }

    // MARK: Awaiting Reply (#5096)

    private func awaiting(_ id: String, _ extra: String = "", state: String = "idle", updated: TimeInterval = 0) -> Session {
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"/tmp/p","status":"idle","state":"\(state)",
         "space_id":"w1","space_name":"app","awaits_reply":true,"asks":"要不要推到 beta？","last_text":"PR 看完了"\(extra),
         "updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated)))"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    @Test func awaitingAReplyDecodesAndOlderHubsSayNothing() {
        let a = awaiting("a")
        #expect(a.awaitsReply && a.asks == "要不要推到 beta？" && a.awaitsYourReply(muted: false))
        let plain = session("p", "idle")
        #expect(!plain.awaitsReply && plain.asks == nil && !plain.awaitsYourReply(muted: false))
        let odd = try? JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"x","awaits_reply":"yes","asks":3}"#.utf8))
        #expect(odd?.awaitsReply == false && odd?.asks == nil, "an odd value never drops the session")
        #expect(!awaiting("u", state: "unread").awaitsYourReply(muted: false), "完成未看 is its own state")
    }

    @Test func awaitingAReplyRanksBetweenUnreadAndWorking() {
        let list = [session("idle", "idle", updated: 50), session("work", "working", updated: 40), awaiting("ask", updated: 1),
                    session("unread", "unread", updated: 20), session("needs", "needs_you", updated: 1)]
        #expect(order().rank(list[2]) == AgentListOrder.stateKeys.firstIndex(of: "awaiting"))
        #expect(order().ordered(list).map(\.sessionID) == ["needs", "unread", "ask", "work", "idle"])
        #expect(order(byRank: false).ordered(list).map(\.sessionID) == ["idle", "work", "unread", "ask", "needs"],
                "最近更新 keeps time-only ordering")
        #expect(order().sectionIDs(list, byMachine: false) == ["state/needs", "state/unread", "state/awaiting", "state/working", "state/idle"])
    }

    @Test func awaitingAReplyStaysOutOfTheIdleFoldUnlessMuted() {
        let ask = awaiting("ask")
        #expect(order().stays(ask))
        #expect(!order(muted: ["ask"]).stays(ask) && order(muted: ["ask"]).rank(ask) == AgentListOrder.stateKeys.firstIndex(of: "idle"), "a muted one folds as 已看")
        let g = SpaceGroup(id: "s", machineID: "m", spaceID: "w1", group: "", name: "app", index: 0, sessions: [ask, session("q", "idle")])
        #expect(!order().isQuiet(g), "a space with one is not in 空闲的空间")
        #expect(order(muted: ["ask"]).isQuiet(g))
        #expect(rows(.space).spaces(active: [g], quiet: [], hosts: nil).map(\.sessionID) == ["ask"])
        #expect(rows(.state, folded: ["state/idle"]).states([ask, session("q", "idle")]).map(\.sessionID) == ["ask"])
    }

    @Test func awaitingAReplyShowsTheQuestion() {
        let ask = awaiting("ask")
        #expect(ask.stateLabel(muted: false) == "待你回复" && ask.listPreview(muted: false) == "要不要推到 beta？")
        let blocked = awaiting("b", #","work":{"phase":"blocked","on":"user","line":"等你决定发不发 beta"}"#)
        #expect(blocked.listPreview(muted: false) == "等你决定发不发 beta", "the triage's line is the better one")
        var seen = session("s", "idle")
        seen.asks = "要不要推到 beta？"
        #expect(seen.stateLabel(muted: false) == "已看" && seen.replyLine(muted: false) == nil, "asks alone is not 待你回复")
    }

    /// One rule: a muted agent that asks is plain Seen in every word, color and line, as it ranks (`AgentListOrder.rank`).
    @Test func aMutedAgentThatAsksIsPlainSeenEverywhere() {
        let ask = awaiting("ask")
        #expect(!ask.awaitsYourReply(muted: true))
        #expect(ask.stateLabel(muted: true) == "已看" && ask.stateLabel == "已看", "the plain label never says it")
        #expect(ask.stateTone(muted: true).text == AgentState.idle.tone.text)
        #expect(ask.stateTone(muted: false).text == StatusPalette.awaitingReply.text)
        #expect(ask.replyLine(muted: true) == nil)
        #expect(ask.listPreview(muted: true) == "PR 看完了", "its last words, not the question")
        #expect(order(muted: ["ask"]).awaitsReply(ask) == ask.awaitsYourReply(muted: true))
    }

    // MARK: Waiting in the Background (#6045)

    private func waiting(_ id: String, state: String = "working", updated: TimeInterval = 0) -> Session {
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"/tmp/p","status":"done","state":"\(state)",
         "space_id":"w1","space_name":"app","background_working":true,"background_waiting":true,
         "background":[{"id":"b1","kind":"shell","description":"等 CI"}],
         "updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated)))"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    @Test func waitingInTheBackgroundDecodesAndOlderHubsSayNothing() {
        let w = waiting("w")
        #expect(w.backgroundWaiting && w.waitsInBackground && w.state == .working, "still working for whatever acts on it")
        #expect(!session("p", "working").waitsInBackground, "an older hub never sends it: Working as before")
        #expect(!waiting("i", state: "idle").waitsInBackground, "only with Working")
        let odd = try? JSON.decoder.decode(Session.self, from: Data(#"{"session_id":"x","background_waiting":"yes"}"#.utf8))
        #expect(odd?.backgroundWaiting == false, "an odd value never drops the session")
    }

    @Test func waitingInTheBackgroundIsNotCountedOrFilteredAsWorking() {
        let w = waiting("w"), work = session("work", "working")
        #expect(!AgentFilter.working.matches(w) && AgentFilter.working.matches(work))
        #expect(AgentFilter.all.matches(w))
        #expect(AgentFilter.backgroundWaiting.matches(w) && !AgentFilter.backgroundWaiting.matches(work))
        #expect(!AgentFilter.backgroundWaiting.matches(w, muted: true), "a muted one is under Muted, as Working's are")
        let g = SpaceGroup(id: "s", machineID: "m", spaceID: "w1", group: "", name: "app", index: 0, sessions: [w, work])
        #expect(g.count(.working) == 1 && g.count(.idle) == 1 && g.waitingInBackground == 1)
        #expect(StateCounts.spoken(g).hasSuffix("1 " + String(localized: "Waiting in the background")), "said apart, not as idle")
        #expect(SpaceGroup(id: "s", machineID: "m", spaceID: "w1", group: "", name: "app", index: 0, sessions: [w]).state == .idle)
        #expect(AgentTally([w, work]) == { var t = AgentTally([]); t.working = 1; t.idle = 1; return t }())
    }

    @Test func waitingInTheBackgroundHasItsOwnSectionAndSaysSo() {
        let list = [session("idle", "idle", updated: 50), waiting("bg", updated: 40), session("work", "working", updated: 1)]
        #expect(order().rank(list[1]) == AgentListOrder.stateKeys.firstIndex(of: "background"))
        #expect(order().ordered(list).map(\.sessionID) == ["work", "bg", "idle"])
        #expect(order().stays(list[1]), "it stays out of the idle fold")
        #expect(order(muted: ["bg"]).rank(list[1]) == AgentListOrder.stateKeys.firstIndex(of: "muted"))
        let w = waiting("w")
        #expect(w.stateLabel == "后台等待中" && w.stateLabel(muted: false) == "后台等待中")
        #expect(w.stateTone.text == StatusPalette.backgroundWaiting.text && w.stateTone(muted: true).text == StatusPalette.backgroundWaiting.text)
        #expect(session("work", "working").stateTone.text == StatusPalette.working.text)
    }

    // MARK: The open agent held in place (#2213)

    @Test func theOpenAgentSeenByOpeningKeepsItsPlaceUntilLeft() {
        let read = session("read", "idle", updated: 30)
        let list = [session("work", "working", updated: 10), read, session("old", "idle", updated: 40)]
        let held = AgentListOrder(isPinned: { _ in false }, isMuted: { _ in false }, held: read.key)
        #expect(held.stays(read), "out of 还有 N 个空闲 while it is open")
        #expect(held.rank(read) == 1, "still 完成未看's place")
        #expect(held.ordered(list).map(\.sessionID) == ["read", "work", "old"])
        let left = order()
        #expect(!left.stays(read) && left.rank(read) == AgentListOrder.stateKeys.firstIndex(of: "idle"), "folds as soon as it is left")
    }

    @Test func aQuietSpaceWithTheHeldAgentIsNotQuiet() {
        let read = session("read", "idle")
        let g = SpaceGroup(id: "s", machineID: "m", spaceID: "w1", group: "", name: "app", index: 0, sessions: [read])
        #expect(order().isQuiet(g))
        #expect(!AgentListOrder(isPinned: { _ in false }, isMuted: { _ in false }, held: read.key).isQuiet(g))
    }

    @Test func aHeldAgentThatIsWorkingAgainRanksAsWorking() {
        let s = session("again", "working")
        #expect(AgentListOrder(isPinned: { _ in false }, isMuted: { _ in false }, held: s.key).rank(s) == 3)
    }

    // MARK: The rows in order (↑ / ↓, #2213)

    private func rows(_ grouping: DevGrouping, folded: Set<String> = [], expandedIdle: Set<String> = [],
                      held: SessionKey? = nil) -> AgentListRows {
        AgentListRows(order: AgentListOrder(isPinned: { _ in false }, isMuted: { _ in false }, held: held), grouping: grouping,
                      isFolded: { folded.contains($0) }, expandedIdle: expandedIdle)
    }

    @Test func aSpaceListsBusyRowsAndItsIdleOnesOnlyWhenOpened() {
        let list = [session("idle", "idle", updated: 50), session("work", "working", updated: 10), session("needs", "needs_you")]
        let g = SpaceGroup(id: "s", machineID: "m", spaceID: "w1", group: "", name: "app", index: 0, sessions: list)
        #expect(rows(.space).spaces(active: [g], quiet: [], hosts: nil).map(\.sessionID) == ["needs", "work"])
        #expect(rows(.space, expandedIdle: ["s"]).spaces(active: [g], quiet: [], hosts: nil).map(\.sessionID) == ["needs", "work", "idle"])
        #expect(rows(.space, folded: ["s"]).spaces(active: [g], quiet: [], hosts: nil).isEmpty, "a folded space hides its rows")
    }

    @Test func stateSectionsFollowTheRanksAndTheFolds() {
        let live = [session("idle", "idle"), session("work", "working"), session("unread", "unread"), session("needs", "needs_you")]
        #expect(rows(.state, folded: ["state/idle"]).states(live).map(\.sessionID) == ["needs", "unread", "work"])
        #expect(rows(.state).states(live).map(\.sessionID) == ["needs", "unread", "work", "idle"])
    }

    @Test func theHeldAgentStaysWhereTheArrowsFoundIt() {
        let read = session("read", "idle", updated: 30)
        let live = [session("work", "working", updated: 10), read]
        let g = SpaceGroup(id: "s", machineID: "m", spaceID: "w1", group: "", name: "app", index: 0, sessions: live)
        let shown = rows(.space, held: read.key).spaces(active: [g], quiet: [], hosts: nil).map(\.key)
        #expect(shown == [read.key, live[0].key])
        #expect(AgentListRows.step(shown, from: read.key, by: 1) == live[0].key)
        #expect(rows(.state, folded: ["state/idle"], held: read.key).states(live).map(\.sessionID) == ["read", "work"])
    }

    /// By Repository: a working tree's nested spaces (Orca's child worktrees, #3075) follow its agents while "N children" is open.
    @Test func nestedSpacesFollowTheirParentWhileOpened() {
        func orca(_ id: String, space: String, parent: String? = nil) -> Session {
            let parentField = parent.map { #","parent_space_id":"\#($0)""# } ?? ""
            let json = """
            {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"/code/\(space)","status":"working","state":"working",
             "space_kind":"orca","space_id":"\(space)","space_name":"\(space)"\(parentField)}
            """
            do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
            catch { fatalError("bad fixture: \(error)") }
        }
        let tree = RepoTree.build([orca("p", space: "main"), orca("c", space: "child", parent: "main")], machines: [])
        let main = tree[0].repos[0].worktrees[0]
        #expect(rows(.repo, folded: [main.childrenFoldID]).repos(tree, byHost: false).map(\.sessionID) == ["p"])
        #expect(rows(.repo).repos(tree, byHost: false).map(\.sessionID) == ["p", "c"])
        #expect(rows(.repo, folded: [main.id]).repos(tree, byHost: false).isEmpty, "a folded working tree hides its children too")
    }

    @Test func stepGoesToTheNeighbourAndStopsAtTheEnds() {
        let keys = ["a", "b", "c"].map { session($0, "working").key }
        #expect(AgentListRows.step(keys, from: keys[1], by: 1) == keys[2])
        #expect(AgentListRows.step(keys, from: keys[1], by: -1) == keys[0])
        #expect(AgentListRows.step(keys, from: keys[2], by: 1) == nil)
        #expect(AgentListRows.step(keys, from: nil, by: 1) == keys[0], "nothing open: ↓ opens the first")
        #expect(AgentListRows.step(keys, from: nil, by: -1) == keys[2])
        #expect(AgentListRows.step([], from: nil, by: 1) == nil)
    }

    @Test func machinesListTheirBusyRowsAndOpenedIdleOnes() {
        let a = session("a", "working", machine: "m1"), b = session("b", "idle", machine: "m1"), c = session("c", "unread", machine: "m2")
        let refs = [c.machineRef, a.machineRef]
        #expect(rows(.machine).machines([a, b, c], order: refs).map(\.sessionID) == ["c", "a"])
        #expect(rows(.machine, expandedIdle: ["machine-" + a.machineRef.path]).machines([a, b, c], order: refs).map(\.sessionID) == ["c", "a", "b"])
    }
}

/// The cap on a long run of rows (`AgentRowCap`, #5766).
struct AgentRowCapTests {
    @Test func shortRunsShowEveryRow() {
        #expect(AgentRowCap.capped(0, cap: 20) == nil)
        #expect(AgentRowCap.capped(20, cap: 20) == nil)
        #expect(AgentRowCap.capped(25, cap: 20) == nil) // a few over: all of them, no "Show All 22"
    }

    @Test func longRunsShowTheCap() {
        #expect(AgentRowCap.capped(26, cap: 20) == 20)
        #expect(AgentRowCap.capped(140, cap: 20) == 20)
    }

    @Test func noCapShowsEverything() {
        #expect(AgentRowCap.capped(10_000, cap: .max) == nil)
    }
}
