import Foundation

/// The arithmetic of filing many inbox items at once (Clear All, Mark All Seen / Clear All, and Undo), kept apart from
/// `InboxStore` so it can be tested without a hub (docs/assistant/inbox.md「全部清理」).
nonisolated enum InboxBulk {
    /// How items are filed at once (`POST /api/v1/inbox/bulk`'s `action`, and the `done_by` shown meanwhile):
    /// `done` for the Done request (sections, Mac's multi-select, Undo), `dismiss` for Clear All — the view-wide
    /// clean-up, recorded as dismissed (looked at, nothing to do) rather than handled.
    enum Action: String, Sendable, CaseIterable {
        case done
        case dismiss

        /// The `done_by` the item shows before the hub answers.
        var doneBy: String { self == .done ? "user" : "dismissed" }
    }

    /// From this many items on, the clean-up asks first (fewer are done at once, with Undo after); the web's and
    /// Android's threshold too (docs/assistant/inbox.md「全部清理」).
    static let confirmFrom = 10

    static func needsConfirm(_ count: Int) -> Bool { count >= confirmFrom }

    /// What Undo sends after a clean-up: the hub's `undo` (only what that request changed) to the manager that answered
    /// it, or — on a hub from before `/bulk`, or after several managers' — the Done request bringing back what moved.
    enum Undo: Sendable, Equatable {
        case bulk(InboxBulkReply.Undo, manager: String?)
        case legacy([InboxItem])
    }

    /// What a filing came back with: the answers together, its Undo, and — when some managers failed and others did
    /// not (#1850) — the failed ones' items, put back, and the first failure in words, which the toast says.
    struct Sent: Sendable {
        var result: InboxBulkResult
        var undo: Undo?
        var lost: [InboxItem] = []
        var failure: String?
    }

    /// The bulk request's answer as the arithmetic below reads it.
    static func result(_ reply: InboxBulkReply) -> InboxBulkResult {
        InboxBulkResult(changed: reply.changed, unchanged: reply.unchanged, skipped: reply.skipped)
    }

    /// Sends a clean-up to one manager (`manager`: the one asked, nil on a hub before managers): the bulk request, or on
    /// a hub from before it (404 / 405) the Done one. That request cannot say dismissed, so an old hub records Clear All
    /// as `done_by=user` — acceptable there, better than refusing it. The requests come in so it can be tested without a
    /// hub.
    static func perform(_ action: Action, _ targets: [InboxItem], manager: String? = nil,
                        bulk: (Action, [InboxItem]) async throws -> InboxBulkReply,
                        legacy: ([InboxItem]) async throws -> InboxBulkResult) async throws -> (result: InboxBulkResult, undo: Undo?) {
        do {
            let reply = try await bulk(action, targets)
            let r = result(reply).stamped(manager)
            return (r, reply.undo.map { .bulk($0, manager: manager) } ?? (r.changed.isEmpty ? nil : .legacy(r.changed)))
        } catch let e as APIError where e.status == 404 || e.status == 405 {
            let r = try await legacy(targets).stamped(manager)
            return (r, r.changed.isEmpty ? nil : .legacy(r.changed))
        }
    }

    /// `each` for every manager's share of `targets` (`InboxStore.managers`), the answers together and stamped with the
    /// manager asked. A manager that fails keeps its items and the others' answers still count; only when every one
    /// fails does this throw (the first failure).
    static func byManager(_ groups: [(key: String?, items: [InboxItem])],
                          _ each: (String?, [InboxItem]) async throws -> InboxBulkResult) async throws -> Sent {
        var sent = Sent(result: .none)
        var first: (any Error)?
        for g in groups {
            do {
                sent.result.add(try await each(g.key, g.items).stamped(g.key))
            } catch {
                sent.lost += g.items
                if first == nil {
                    first = error
                    sent.failure = error.shown ?? error.localizedDescription
                }
            }
        }
        if let first, sent.lost.count == groups.reduce(0, { $0 + $1.items.count }) { throw first }
        return sent
    }

    /// The list with what a failed request had moved put back as it was (`before`: by `InboxItem.key`).
    static func rollback(_ items: [InboxItem], before: [String: InboxItem]) -> [InboxItem] {
        items.map { before[$0.key] ?? $0 }
    }

    /// What a bulk request moves: the items not in that state yet.
    static func targets(_ list: [InboxItem], done: Bool) -> [InboxItem] {
        var seen = Set<String>()
        return list.filter { $0.isDone != done && seen.insert($0.key).inserted }
    }

    /// The list with the items of `keys` (`InboxItem.key`) filed (or brought back) at `now`, as it shows before the hub
    /// answers.
    static func marking(_ items: [InboxItem], keys: Set<String>, done: Bool, now: Date, by action: Action = .done) -> [InboxItem] {
        items.map { it in
            guard keys.contains(it.key) else { return it }
            var it = it
            it.doneAt = done ? now : nil
            it.doneBy = done ? action.doneBy : nil
            return it
        }
    }

    /// The list after the hub answered: what it moved as it has them now; what it left (new messages since the list,
    /// gone from the hub) as it was before; what was already so as shown.
    static func settle(_ items: [InboxItem], before: [String: InboxItem], result: InboxBulkResult) -> [InboxItem] {
        let fresh = Dictionary(result.changed.map { ($0.key, $0) }, uniquingKeysWith: { _, b in b })
        let left = Set(result.skipped.map(\.key))
        return items.map { it in
            if let f = fresh[it.key] { return f }
            if left.contains(it.key), let b = before[it.key] { return b }
            return it
        }
    }

    /// The items left open because something new came in after the list was drawn.
    static func changedSince(_ result: InboxBulkResult) -> Int { result.skipped.filter { $0.reason == "changed" }.count }

    /// The toast after the hub answered.
    static func message(done: Bool, result: InboxBulkResult, action: Action = .done) -> String {
        let moved = result.changed.count
        let fresh = changedSince(result)
        let head: String
        switch (done, action) {
        case (true, .done): head = moved > 0 ? String(localized: "Marked \(moved) items as done") : String(localized: "Nothing to mark")
        case (true, .dismiss): head = moved > 0 ? String(localized: "Cleaned up \(moved) items") : String(localized: "Nothing to clean up")
        case (false, _): head = moved > 0 ? String(localized: "Moved back \(moved) items") : String(localized: "Nothing to move back")
        }
        return fresh > 0 ? head + String(localized: ". \(fresh) with new messages stayed in the Inbox.") : head
    }

    /// The toast when some managers' share failed (#1850): what the others did, then how many stayed as they were and why.
    static func partly(_ head: String, lost: Int, error: String) -> String {
        let head = head.hasSuffix(".") || head.hasSuffix("。") ? String(head.dropLast()) : head
        return String(localized: "\(head). \(lost) items weren't changed: \(error)")
    }

    /// The question before a large Clear All (the web's and Android's words).
    static var confirmTitle: String { String(localized: "Clean up all?") }

    /// The dialog's button: Clear and how many.
    static func confirmButton(count: Int) -> String { String(localized: "Clean up \(count) items") }

    static func confirmMessage(count: Int, filters: [String], query: String) -> String {
        String(localized: "This cleans up \(count) items (\(scope(filters: filters, query: query))). New messages that arrive later aren't affected, and you can undo it afterward.")
    }

    /// What Clear All covers, for the menu: the filters' names and the search, or All.
    static func scope(filters: [String], query: String) -> String {
        var parts = filters.filter { !$0.isEmpty }
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        if !q.isEmpty { parts.append(String(localized: "Search “\(q)”")) }
        return parts.isEmpty ? String(localized: "All") : parts.joined(separator: " · ")
    }

    /// The menu's line under Clear All: what it covers and how many.
    static func scopeLine(filters: [String], query: String, count: Int) -> String {
        count == 0 ? String(localized: "Nothing left to handle") : String(localized: "\(scope(filters: filters, query: query)) · \(count) items")
    }
}
