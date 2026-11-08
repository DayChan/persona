import Foundation

/// The Agents list's order, folds and sections (By Status / By Machine), as plain functions of the sessions and the user's pins
/// and mutes, so they are unit-tested (`AgentListOrderTests`) apart from the view.
nonisolated struct AgentListOrder {
    var isPinned: (SessionKey) -> Bool
    var isMuted: (Session) -> Bool
    /// Under a quick filter, or sorted by Recently Updated (`AgentSort`), rows are ordered by time only: an agent seen from a
    /// quick filter keeps its place.
    var byRank = true
    /// The open agent that was Unread when it was opened (`AppModel.heldInList`): until it is left it keeps that place
    /// and stays out of the idle folds, so it does not go from under the pointer while it is read (#2213).
    var held: SessionKey? = nil

    /// The sections of By Status, in rank order.
    static let stateKeys = ["needs", "unread", "awaiting", "working", "background", "waiting", "muted", "idle"]

    /// Awaiting Reply (#5096): seen, stopped on a question nobody answered yet, and not muted (a muted one folds with
    /// the idle ones, as it did before). The one rule is `Session.awaitsYourReply(muted:)`, which the rows use too.
    func awaitsReply(_ s: Session) -> Bool { s.awaitsYourReply(muted: isMuted(s)) }

    /// Rows that stay out of the idle fold: busy, pinned, waiting to be woken (`Session.isWaiting`: looping, with
    /// background work, or the hub waiting it out), or waiting on the user's reply (`awaitsReply`).
    func stays(_ s: Session) -> Bool {
        !s.isQuiet || s.key == held || isPinned(s.key) || s.isWaiting || awaitsReply(s)
    }

    /// One order for every grouping (inside a space, a machine, and the sections of By Status):
    /// Needs You → Unread → Awaiting Reply → Working → Waiting in the Background (`Session.waitsInBackground`, #6045) →
    /// Waiting → Muted → Seen. Muted agents that are working, finished-unread or waiting rank as Muted; one that needs
    /// you stays first. The index into `stateKeys`.
    func rank(_ s: Session) -> Int {
        if s.state == .needsYou { return 0 }
        let unread = s.state == .unread || (s.key == held && s.isQuiet)
        let live = s.state == .working || unread || s.isWaiting
        if live && isMuted(s) { return 6 }
        if unread { return 1 }
        if awaitsReply(s) { return 2 }
        switch s.state {
        case .unread: return 1
        case .working: return s.waitsInBackground ? 4 : 3
        default: return s.isWaiting ? 5 : 7
        }
    }

    /// By rank, then the most recently updated (working: most recently started) first, then by id so equal times
    /// never swap between renders.
    func ordered(_ list: [Session]) -> [Session] {
        list.map { session -> (session: Session, rank: Int, sortDate: Date) in
            (session: session, rank: byRank ? rank(session) : 0, sortDate: session.sortDate)
        }.sorted { a, b in
            if a.rank != b.rank { return a.rank < b.rank }
            return a.sortDate != b.sortDate ? a.sortDate > b.sortDate : a.session.sessionID < b.session.sessionID
        }
        .map(\.session)
    }

    /// A flat activity timeline, including idle and offline history. Reading a reply does not move its row.
    nonisolated static func recent(_ list: [Session]) -> [Session] {
        list.sorted { a, b in
            let lhs = a.updatedAt ?? .distantPast, rhs = b.updatedAt ?? .distantPast
            return lhs != rhs ? lhs > rhs : a.id < b.id
        }
    }

    /// The rows of a space or a working tree (`ordered`), each tab's agents together where its first one ranks
    /// (`TabPanes.entries`, #2320): those that stay out of the idle fold (a tab when any of its agents does) and the
    /// idle ones; with `all` off (a quick filter), every row stays.
    func tabEntries(_ list: [Session], all: Bool = true) -> (busy: [TabPanes.Entry], idle: [TabPanes.Entry]) {
        let entries = TabPanes.entries(ordered(list))
        guard all else { return (entries, []) }
        let stays = { (e: TabPanes.Entry) in e.sessions.contains(where: self.stays) }
        return (entries.filter(stays), entries.filter { !stays($0) })
    }

    /// A space that folds into Idle Spaces: every agent in it quiet, none awaiting the user's reply, and not the held
    /// one's.
    func isQuiet(_ g: SpaceGroup) -> Bool { g.isQuiet && !g.sessions.contains { $0.key == held || awaitsReply($0) } }
    /// Newest first by `date`, then by `id` so equal times never swap between renders: spaces under a quick filter,
    /// spaces and machines under Recently Updated. `date` is a `sortDate`, so a working agent's every step does not move them.
    static func latestFirst<T>(_ items: [T], date: (T) -> Date, id: (T) -> String) -> [T] {
        items.map { (item: $0, at: date($0), id: id($0)) }
            .sorted { $0.at != $1.at ? $0.at > $1.at : $0.id < $1.id }
            .map(\.item)
    }

    /// The fold ids of the sections shown now (Collapse All): one per machine with live agents, or one per rank in use.
    func sectionIDs(_ sessions: [Session], byMachine: Bool) -> [String] {
        let live = sessions.filter { $0.state.isLive }
        if byMachine { return Set(live.map(\.machineRef.path)).map { "machine/" + $0 } }
        let ranks = Set(live.map(rank))
        return Self.stateKeys.enumerated().filter { ranks.contains($0.offset) }.map { "state/" + $0.element }
    }
}

/// Limits the iPhone list's initially eligible rows before its "Show All N" row (#5766). The List remains lazy; this
/// presentation budget does not eliminate full filtering or grouping work. Other clients keep their existing list
/// behavior, including the Mac's ↑ / ↓ navigation (`AgentListRows`).
nonisolated enum AgentRowCap {
    #if os(iOS)
    static let rows = 20
    #else
    static let rows = Int.max
    #endif
    /// How many to show of `count`; nil: all of them. A run only a few over the cap shows them all (no "Show All 22").
    static func capped(_ count: Int, cap: Int = rows) -> Int? {
        cap == .max || count <= cap + 5 ? nil : cap
    }
}
