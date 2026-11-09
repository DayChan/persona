import Foundation
import Testing
@testable import Persona

/// #4369: with several hub groups on the watch, one group's failed refresh keeps that group's last cards and agents,
/// and the same card does not buzz again when the group recovers.
struct WatchGroupRefreshTests {
    private func interaction(_ id: String, group: String) -> Interaction {
        var ix = Interaction(id: id, machineID: "m", sessionID: "s", kind: .permission)
        ix.group = group
        return ix
    }

    private func machine(_ id: String, group: String) -> Machine {
        var m = Machine(id: id, name: id, kind: "ssh", online: true)
        m.group = group
        return m
    }

    private func data(group: String, interaction id: String) -> WatchGroupData {
        WatchGroupData(sessions: [], interactions: [interaction(id, group: group)],
                       machines: [machine("m-\(group)", group: group)])
    }

    @Test func failingGroupKeepsItsLastSuccessfulData() {
        let old = ["g1": data(group: "g1", interaction: "a"), "g2": data(group: "g2", interaction: "b")]
        let newer = ["g1": data(group: "g1", interaction: "a2")]
        let out = WatchGroupRefresh.mergeGroups(old: old, new: newer, failures: ["g2"])
        #expect(out.staleFailed == ["g2"])
        // The failed group is not replaced with an empty answer; the group that answered is.
        #expect(out.data["g2"] == old["g2"])
        #expect(out.data["g1"] == newer["g1"])
    }

    @Test func recoveredGroupReplacesOldDataAndLeavesTheBannerList() {
        let old = ["g2": data(group: "g2", interaction: "b")]
        let out = WatchGroupRefresh.mergeGroups(old: old, new: ["g2": data(group: "g2", interaction: "b2")], failures: [])
        #expect(out.staleFailed.isEmpty)
        #expect(out.data["g2"]?.interactions.first?.id == "b2")
    }

    @Test func everyGroupFailingBeforeItsFirstSuccessHasNothingStaleToShow() {
        let out = WatchGroupRefresh.mergeGroups(old: [:], new: [:], failures: ["", "g2"])
        #expect(out.data.isEmpty)
        #expect(out.staleFailed.isEmpty) // the watch shows its whole-page offline state instead
    }

    @Test func groupNotAttemptedThisRoundKeepsItsDataWithoutABanner() {
        let old = ["g1": data(group: "g1", interaction: "a"), "g2": data(group: "g2", interaction: "b")]
        // g2 has no credential this round, so it is neither fetched nor in failures.
        let out = WatchGroupRefresh.mergeGroups(old: old, new: ["g1": data(group: "g1", interaction: "a2")], failures: [])
        #expect(out.data["g2"] == old["g2"])
        #expect(out.staleFailed.isEmpty)
    }

    @Test func cardBuzzesOnceAcrossFailureAndRecovery() {
        let card = interaction("card", group: "g2")
        var remembered: [String] = []

        // First success: the new card buzzes and is remembered.
        let first = WatchGroupRefresh.newHapticUids(seen: Set(remembered), current: [card])
        #expect(first == ["g2|card"])
        remembered = WatchGroupRefresh.rememberSeen(remembered, adding: first)

        // g2 fails: its old card is kept and marked stale, applying it again buzzes for nothing.
        let stored = ["g2": WatchGroupData(sessions: [], interactions: [card], machines: [])]
        let failed = WatchGroupRefresh.mergeGroups(old: stored, new: [:], failures: ["g2"])
        #expect(failed.staleFailed == ["g2"])
        // The coalescing stays outside #expect: its property-access macro treats the whole expression as the
        // optional root and generates a chain on the already non-optional [String].
        let failedCards = failed.data["g2"]?.interactions ?? []
        #expect(WatchGroupRefresh.newHapticUids(seen: Set(remembered), current: failedCards).isEmpty)

        // g2 recovers with the same card: replaced with fresh data, still no buzz.
        let recovered = WatchGroupRefresh.mergeGroups(old: failed.data, new: stored, failures: [])
        #expect(recovered.staleFailed.isEmpty)
        let recoveredCards = recovered.data["g2"]?.interactions ?? []
        #expect(WatchGroupRefresh.newHapticUids(seen: Set(remembered), current: recoveredCards).isEmpty)

        // A genuinely new card buzzes; the one already seen does not.
        let next = interaction("card2", group: "g2")
        #expect(WatchGroupRefresh.newHapticUids(seen: Set(remembered), current: [card, next]) == ["g2|card2"])
    }

    @Test func rememberedUidsDedupAndStayBounded() {
        #expect(WatchGroupRefresh.rememberSeen(["a", "b"], adding: ["b", "c"]) == ["a", "b", "c"])
        // Past the limit the oldest first-seen uids drop, so the persisted record can not grow without end.
        #expect(WatchGroupRefresh.rememberSeen([], adding: (0..<5).map { "u\($0)" }, limit: 3) == ["u2", "u3", "u4"])
    }

    // #6257: what a phone summary carries, and whether it is still news.

    private func session(_ id: String, _ state: String, updated: TimeInterval) throws -> Session {
        let o: [String: Any] = ["machine_id": "watchsummarybox", "session_id": id, "title": id, "cwd": "/code/\(id)",
                                "status": "idle", "state": state, "started_by_persona": false, "assistant": false,
                                "pending_interactions": 0,
                                "updated_at": ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated))]
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
    }

    @Test func aSummaryOlderThanWhatTheWatchShowsIsDropped() {
        let shown = Date(timeIntervalSince1970: 1000)
        #expect(WatchGroupRefresh.acceptsSummary(at: shown.addingTimeInterval(-1), lastUpdate: shown) == false)
        #expect(WatchGroupRefresh.acceptsSummary(at: shown, lastUpdate: shown) == false)
        #expect(WatchGroupRefresh.acceptsSummary(at: shown.addingTimeInterval(1), lastUpdate: shown))
    }

    @Test func theFirstSummaryKeepsItsOwnAgeAndAFutureOneStopsAtNow() {
        let now = Date(timeIntervalSince1970: 10_000)
        let old = now.addingTimeInterval(-3600)
        #expect(WatchGroupRefresh.acceptsSummary(at: old, lastUpdate: nil))
        #expect(WatchGroupRefresh.dataDate(ofSummaryAt: old, now: now) == old)
        #expect(WatchGroupRefresh.dataDate(ofSummaryAt: now.addingTimeInterval(5), now: now) == now)
    }

    @Test func busyAgentsOutlastTheSummaryLimit() throws {
        let idle = try (0..<3).map { try session("idle\($0)", "idle", updated: 100 + Double($0)) }
        let busy = [try session("asking", "needs_you", updated: 1), try session("working", "working", updated: 2)]
        #expect(WatchGroupRefresh.summarySessions(idle + busy, limit: 3).map(\.title) == ["idle2", "working", "asking"])
    }

    @Test func busyAgentsPastTheLimitAreAllKept() throws {
        let busy = try (0..<4).map { try session("b\($0)", "working", updated: Double($0)) }
        let idle = try session("idle", "idle", updated: 100)
        #expect(WatchGroupRefresh.summarySessions(busy + [idle], limit: 3).count == 4)
    }

    @Test func aFewAgentsAreAllKeptNewestFirst() throws {
        let all = try [session("a", "idle", updated: 1), session("b", "idle", updated: 3), session("c", "idle", updated: 2)]
        #expect(WatchGroupRefresh.summarySessions(all).map(\.title) == ["b", "c", "a"])
    }

    @MainActor @Test func thePhoneSummaryKeepsAnOldWorkingAgent() throws {
        let model = AppModel()
        model.forgetRestoredForTests() // only the agents below, not what the simulator's pairing saved (#1638)
        model.registry = HubRegistry(hubs: [PairedHub(id: "watchsummarybox",
                                                      config: ServerConfig(url: URL(string: "http://hub.example.com")!, token: "t"))])
        model.buildLinks()
        // AppModel restores paired snapshots; only this fixture belongs in the summary under test.
        model.applySessions(Dictionary(grouping: model.allSessions, by: \.machineRef).mapValues { _ in [Session]() })
        #expect(model.allSessions.isEmpty)
        try (0..<25).forEach { model.adopt(try session("idle\($0)", "idle", updated: 1000 + Double($0))) }
        model.adopt(try session("old", "working", updated: 1))
        let titles = model.watchSummary().sessions.map(\.title)
        #expect(titles.count == 20)
        #expect(titles.contains("old"))
    }
}
