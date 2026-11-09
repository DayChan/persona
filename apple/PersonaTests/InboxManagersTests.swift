import Foundation
import Testing
@testable import Persona

/// The inbox with several managers (#1850): two managers' items with one id are two items (`InboxItem.key`), a bulk
/// filing where one manager fails keeps the others' filed and puts back only the failed one's (the toast says how many
/// and why), and the list groups under each manager's name, its sections folding apart. Made-up values.
@MainActor
struct InboxManagersTests {
    private func item(_ id: String, _ manager: String?, tier: String = "action", done: Bool = false) throws -> InboxItem {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        let filed = done ? #","done_at":"2026-11-04T08:00:00Z","done_by":"read""# : ""
        return try JSON.decoder.decode(InboxItem.self, from: Data(#"""
        {"id":"\#(id)","source":"mail","kind":"mail","account":"a1","account_name":"me@example.com","title":"\#(id)","preview":"",
         "count":1,"version":"v1","tier":"\#(tier)"\#(stamp)\#(filed)}
        """#.utf8))
    }

    /// What a manager answers: the items filed, with no `manager` (an answer is no merged list).
    private func filed(_ items: [InboxItem]) -> InboxBulkResult {
        InboxBulkResult(changed: items.map { var it = $0; it.manager = nil; it.doneAt = .now; it.doneBy = "user"; return it },
                        unchanged: [], skipped: [])
    }

    private static let roster = ManagerRoster(managers: [
        HubManager(id: "assistant", name: "助手", online: true, isDefault: true),
        HubManager(id: "m-1", name: "工作", online: true),
        HubManager(id: "m-2", name: "家里", online: false),
    ], defaultID: "assistant")

    @Test func oneIdUnderTwoManagersIsTwoItems() throws {
        let a = try item("x", "m-1"), b = try item("x", "m-2"), old = try item("x", nil)
        #expect(a.key != b.key && old.key == "x")
        #expect(InboxBulk.targets([a, b, a], done: true).map(\.key) == [a.key, b.key])
        let shown = InboxBulk.marking([a, b], keys: [a.key], done: true, now: .now)
        #expect(shown[0].isDone && !shown[1].isDone, "filing one manager's leaves the other's")

        // The hub answered for m-1 only; m-2 left its "x" (new messages since).
        var result = filed([a]).stamped("m-1")
        result.skipped = [.init(id: "x", reason: "changed", manager: "m-2")]
        let both = InboxBulk.marking([a, b], keys: [a.key, b.key], done: true, now: .now)
        let settled = InboxBulk.settle(both, before: [a.key: a, b.key: b], result: result)
        #expect(settled.map(\.manager) == ["m-1", "m-2"], "an answer's item keeps the manager asked")
        #expect(settled[0].isDone && !settled[1].isDone)
    }

    @Test func aManagerThatFailsKeepsItsItemsAndTheOthersStayFiled() async throws {
        let a = try item("x", "m-1"), b = try item("x", "m-2"), c = try item("y", "m-1")
        var asked: [String?] = []
        let sent = try await InboxBulk.byManager(InboxStore.managers([a, b, c])) { manager, mine in
            asked.append(manager)
            if manager == "m-2" { throw APIError(status: 500, message: "down") }
            return self.filed(mine)
        }
        #expect(asked == ["m-1", "m-2"], "each manager its own request")
        #expect(sent.result.changed.map(\.key) == [a.key, c.key])
        #expect(sent.lost.map(\.key) == [b.key] && sent.failure == "down")

        // As the store shows it: m-1's stay filed, m-2's goes back as it was.
        let before = [a.key: a, b.key: b, c.key: c]
        let shown = InboxBulk.marking([a, b, c], keys: Set(before.keys), done: true, now: .now)
        let lost = Set(sent.lost.map(\.key))
        let after = InboxBulk.rollback(InboxBulk.settle(shown, before: before, result: sent.result),
                                       before: before.filter { lost.contains($0.key) })
        #expect(after.map(\.isDone) == [true, false, true])
        #expect(after[1] == b)
        let head = InboxBulk.message(done: true, result: sent.result)
        #expect(InboxBulk.partly(head, lost: 1, error: "down") == "已将 2 件标为已处理；1 件没改成：down")
        #expect(InboxBulk.partly(head + "。", lost: 1, error: "down") == "已将 2 件标为已处理；1 件没改成：down", "one stop, not two")
    }

    @Test func whenEveryManagerFailsTheFilingFails() async throws {
        let a = try item("x", "m-1"), b = try item("x", "m-2")
        await #expect(throws: APIError.self) {
            _ = try await InboxBulk.byManager(InboxStore.managers([a, b])) { _, _ in throw APIError(status: 500, message: "down") }
        }
    }

    @Test func anUndoGoesToTheManagerThatAnswered() async throws {
        let a = try item("x", "m-2")
        let (_, undo) = try await InboxBulk.perform(.dismiss, [a], manager: "m-2",
            bulk: { _, _ in InboxBulkReply(action: "dismiss", changed: [], unchanged: [], undo: .init(action: "reopen", ids: ["x"])) },
            legacy: { _ in .none })
        #expect(undo == .bulk(.init(action: "reopen", ids: ["x"]), manager: "m-2"))
    }

    @Test func theInboxGroupsUnderEachManager() throws {
        let mine = try item("a", "assistant"), bare = try item("b", nil, tier: "fyi"), home = try item("c", "m-2", tier: "fyi")
        let workDone = try item("d", "m-1", done: true), gone = try item("e", "gone")
        var waiting = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix","machine_id":"m","session_id":"s","kind":"permission"}"#.utf8))
        waiting.manager = "m-2"
        let byTier = Dictionary(grouping: [mine, bare, home, gone], by: \.triage)
        let shares = InboxShare.make(byTier: byTier, done: [workDone], agents: [waiting], roster: Self.roster)
        #expect(shares.map(\.id) == ["assistant", "m-1", "m-2"], "the default first, then the roster's order")
        #expect(shares[0].byTier[.action]?.map(\.id) == ["a", "e"] && shares[0].byTier[.fyi]?.map(\.id) == ["b"],
                "no stamp, or one the roster lacks: the default's")
        #expect(shares[1].openCount == 0 && shares[1].done.map(\.id) == ["d"], "a group with only done items still shows")
        #expect(shares[2].agents.map(\.id) == ["ix"] && shares[2].openCount == 2)
        #expect(shares[2].mine([workDone, home], Self.roster).map(\.id) == ["c"])

        let one = ManagerRoster(managers: [Self.roster.managers[0]], defaultID: "assistant")
        let flat = InboxShare.make(byTier: byTier, done: [workDone], agents: [waiting], roster: one)
        #expect(flat.count == 1 && flat[0].manager == nil && flat[0].openCount == 5, "one manager: the page as before")
        #expect(InboxShare.make(byTier: [:], done: [], agents: [], roster: nil).count == 1)
    }

    @Test func eachGroupFoldsApart() {
        var folds = InboxFolds()
        #expect(folds.isOpen("action") && folds.isOpen("fyi") && !folds.isOpen("noise") && !folds.isOpen("done"))
        folds.toggle("fyi", manager: "m-1")
        #expect(!folds.isOpen("fyi", manager: "m-1") && folds.isOpen("fyi", manager: "m-2") && folds.isOpen("fyi"))
        folds.toggle("done", manager: "m-2")
        #expect(folds.isOpen("done", manager: "m-2") && !folds.isOpen("done"))
        folds.toggle("working")
        #expect(folds.isOpen("working"), "In Progress stays open")
        folds.toggle("fyi", manager: "m-1")
        #expect(folds.isOpen("fyi", manager: "m-1"))
    }
}
