import Foundation

/// The done marks the app shows before the hub has them (#2201). Marking an item done (or back) applies at once and its
/// request goes in the background, while lists keep coming: every `inbox` push reloads, and a reload may already be out.
/// A list the hub made before it took a mark still has the item as it was, and replacing the whole list with it brought
/// a swiped-away item back for a moment (swipe A, then B: the list after A's push has B open again).
///
/// So each mark is laid over the lists that may not have it yet: while its request is out, over every list; once the
/// hub took it, over the lists that started before then. It goes when none of those is still out, or when its request
/// failed (the item is then as the hub has it). The web keeps its marks the same way (`withPending` in lib/inbox.ts).
nonisolated struct InboxMarks {
    /// What a mark sets on its item.
    struct Mark: Equatable, Sendable {
        var doneAt: Date?
        var doneBy: String?
        /// A reason the mark gives (a mute's "Muted: …"); nil leaves the item's own.
        var reason: String?
    }

    private struct Entry {
        var mark: Mark
        let token: Int
        /// The newest list started when the hub took the mark; nil while its request is out.
        var tookAt: Int?
    }

    private var entries: [String: Entry] = [:]
    private var listSeq = 0
    private var listsOut: Set<Int> = []
    private var tokens = 0

    var isEmpty: Bool { entries.isEmpty }

    /// Sets a mark for an item, replacing an older one; the token settles it.
    mutating func put(_ id: String, _ mark: Mark) -> Int {
        tokens += 1
        entries[id] = Entry(mark: mark, token: tokens)
        return tokens
    }

    /// A list request starts: its number, for `lay` and `endList`.
    mutating func beginList() -> Int {
        listSeq += 1
        listsOut.insert(listSeq)
        return listSeq
    }

    /// A list request ended, answered or not: the marks no list still out can undo go.
    mutating func endList(_ seq: Int) {
        listsOut.remove(seq)
        prune()
    }

    /// The hub answered a mark's request, unless a newer mark of the same item replaced it meanwhile. Taken: the mark
    /// stays over the lists that started before now (with the hub's done fields when its answer had the item). Not
    /// taken: it goes.
    mutating func settle(_ id: String, token: Int, taken: Bool, hub: InboxItem? = nil) {
        guard var e = entries[id], e.token == token else { return }
        if taken {
            e.tookAt = listSeq
            if let hub {
                e.mark.doneAt = hub.doneAt
                e.mark.doneBy = hub.doneBy
            }
            entries[id] = e
        } else {
            entries[id] = nil
        }
        prune()
    }

    /// Whether the item has a mark whose request is still out: a failed older request must not put the item back then.
    func out(_ id: String) -> Bool { entries[id].map { $0.tookAt == nil } ?? false }

    /// A list from the hub with the marks laid over it. `seq` is its request's number; nil (an answer about items, not a
    /// list) lays only the marks still out.
    func lay(_ items: [InboxItem], seq: Int?) -> [InboxItem] {
        guard !entries.isEmpty else { return items }
        return items.map { it in
            guard let e = entries[it.id] else { return it }
            let stale = e.tookAt.map { took in seq.map { $0 <= took } ?? false } ?? true
            guard stale else { return it }
            var it = it
            it.doneAt = e.mark.doneAt
            it.doneBy = e.mark.doneBy
            if let reason = e.mark.reason { it.reason = reason }
            return it
        }
    }

    private mutating func prune() {
        for (id, e) in entries {
            guard let took = e.tookAt, !listsOut.contains(where: { $0 <= took }) else { continue }
            entries[id] = nil
        }
    }
}
