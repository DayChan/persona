import Foundation

/// The agents of one herdr tab as one group (#2320, docs/protocol/agent-states.md「一个 tab 里的几个 pane」): an agent splits its
/// tab and starts more agents in the new panes to work on the same thing. They sit in the order of their panes, the
/// starter (the top left pane, the others split from it) first. The same rules as the web's (`web/src/lib/herdr-tree.ts`),
/// as plain functions so they are unit-tested (`TabPanesTests`) apart from the views.
nonisolated enum TabPanes {
    /// The order herdr opened the pane in, from its id: herdr counts its panes up in base 36 ("w4R:pW", "w4R:p2E"),
    /// Herdr App numbers its terminals in decimal ("herdr-core:t1"). Ids that carry no order ("botmux:<uuid>") go last.
    static func paneNumber(_ s: Session) -> Int {
        guard let id = s.paneID, !id.isEmpty else { return Int.max }
        if let colon = id.lastIndex(of: ":") {
            let tail = id[id.index(after: colon)...]
            if tail.first == "p", tail.count > 1, tail.dropFirst().allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber) }),
               let n = Int(tail.dropFirst(), radix: 36) {
                return n
            }
        }
        let digits = id.reversed().prefix { $0.isASCII && $0.isNumber }
        return digits.isEmpty ? Int.max : Int(String(digits.reversed())) ?? Int.max
    }

    /// A tab's panes in their order: their place in the tab (`pane_index`; panes without one after), then the order
    /// herdr opened them in, then the session id.
    static func inPaneOrder(_ a: Session, _ b: Session) -> Bool {
        let pa = a.paneIndex ?? Int.max, pb = b.paneIndex ?? Int.max
        if pa != pb { return pa < pb }
        let na = paneNumber(a), nb = paneNumber(b)
        if na != nb { return na < nb }
        return a.sessionID < b.sessionID
    }

    /// Tab order, as in herdr (`slot_index`, then the tab's id); a tab's agents together, in pane order.
    static func inTabOrder(_ a: Session, _ b: Session) -> Bool {
        let sa = a.slotIndex ?? 0, sb = b.slotIndex ?? 0
        if sa != sb { return sa < sb }
        if (a.slotID ?? "") != (b.slotID ?? "") { return (a.slotID ?? "") < (b.slotID ?? "") }
        return inPaneOrder(a, b)
    }

    /// The tab's starter: the agent in its first pane (`pane_index` 0). None when the tab has one agent, or that pane is a
    /// shell or not placed: the agents are then peers, and none is marked.
    static func launcher(of panes: [Session]) -> Session? {
        panes.count > 1 ? panes.first { $0.paneIndex == 0 } : nil
    }

    /// Which tab it is in (same group of hubs, machine, space and slot); nil for a host without tabs.
    static func tabKey(_ s: Session) -> String? {
        guard let slot = s.slotID, !slot.isEmpty, let space = s.spaceID, !space.isEmpty else { return nil }
        return "\(s.machineRef.path)/\(s.spaceKind ?? ""):\(space)/\(slot)"
    }

    /// Both in one tab (and so one group).
    static func sameSlot(_ a: Session, _ b: Session) -> Bool {
        guard let k = tabKey(a) else { return false }
        return k == tabKey(b)
    }

    /// Where a tab's fold is kept: by machine, space name and the tab's number, not its name (two tabs may share one,
    /// tabs are renamed, the hub names a `#2` after its task) nor herdr's ids (they change when it restarts); a host
    /// without numbers by the tab's id. The web keeps the same key.
    static func foldID(_ s: Session) -> String {
        let space = s.spaceName.flatMap { $0.isEmpty ? nil : $0 } ?? s.spaceID ?? ""
        return "tab/\(s.machineRef.path)/\(space)/\(s.slotIndex.map(String.init) ?? s.slotID ?? "")"
    }

    /// An agent's place in a tab of several: what it is called there and whether it is the starter.
    nonisolated struct Role: Equatable, Hashable, Sendable {
        var name: String
        var launcher: Bool
        /// Hangs off the starter (set a level in under it); with no starter the agents are peers.
        var branch = false
    }

    /// A tab holding more than one agent.
    struct Group: Identifiable, Equatable, Sendable {
        var id: String
        /// In pane order.
        var sessions: [Session]
        var launcher: Session?
        /// The one the tab's row speaks for: the most urgent (needs you, done unseen, working, seen), the first in pane
        /// order among equals.
        var lead: Session
        var foldID: String
        /// `#N` or the tab's name; nil when the host gives neither.
        var label: String? { sessions.first?.slotLabel }
        var state: AgentState { lead.state }

        func isLauncher(_ s: Session) -> Bool { launcher?.key == s.key }
        /// What its row says about its place in the tab.
        func role(of s: Session) -> Role { Role(name: paneName(s, among: sessions), launcher: isLauncher(s), branch: isBranch(s)) }
        /// Hangs off the starter (indented under it); with no starter the agents are peers.
        func isBranch(_ s: Session) -> Bool { launcher != nil && !isLauncher(s) }
    }

    /// A row of a list: one agent alone, or a tab's agents together.
    enum Entry: Identifiable, Equatable, Sendable {
        case agent(Session)
        case tab(Group)

        var id: String {
            switch self {
            case .agent(let s): s.id
            case .tab(let g): "tab|" + g.id
            }
        }
        var sessions: [Session] {
            switch self {
            case .agent(let s): [s]
            case .tab(let g): g.sessions
            }
        }
    }

    /// The most urgent of `panes`, the first among equals.
    static func mostUrgent(_ panes: [Session]) -> Session? {
        panes.reduce(nil) { best, s in best.map { s.state.priority < $0.state.priority ? s : $0 } ?? s }
    }

    /// `list` (in the list's order) with each tab's agents folded into one entry where the first of them was, its agents
    /// in pane order; a tab with one agent here, and an agent outside any tab, is an entry of its own.
    static func entries(_ list: [Session]) -> [Entry] {
        var byTab: [String: [Session]] = [:]
        for s in list { if let k = tabKey(s) { byTab[k, default: []].append(s) } }
        var placed = Set<String>()
        var out: [Entry] = []
        for s in list {
            guard let k = tabKey(s), let panes = byTab[k], panes.count > 1 else { out.append(.agent(s)); continue }
            guard placed.insert(k).inserted else { continue }
            let sorted = panes.sorted(by: inPaneOrder)
            out.append(.tab(Group(id: k, sessions: sorted, launcher: launcher(of: sorted), lead: mostUrgent(sorted) ?? sorted[0],
                                  foldID: foldID(sorted[0]))))
        }
        return out
    }

    /// Each agent of a tab with several in `list`: its place there (`Group.role(of:)`).
    static func roles(_ list: [Session]) -> [SessionKey: Role] {
        var out: [SessionKey: Role] = [:]
        for case .tab(let g) in entries(list) { for s in g.sessions { out[s.key] = g.role(of: s) } }
        return out
    }

    /// `list` in the list's order, each tab's agents together in pane order (`entries`, flattened).
    static func grouped(_ list: [Session]) -> [Session] { entries(list).flatMap(\.sessions) }

    /// What a tab's agent is called beside its tab mates: its agent ("Claude", "Codex"). Two of its other agents
    /// running the same one are numbered in pane order ("Claude 2"); the starter is told apart by its mark, so it is not.
    static func paneName(_ s: Session, among panes: [Session]) -> String {
        let label = s.agentLabel
        let first = launcher(of: panes)
        guard s.key != first?.key else { return label }
        let same = panes.filter { $0.key != first?.key && $0.agentLabel == label }
        guard same.count > 1, let i = same.firstIndex(where: { $0.key == s.key }) else { return label }
        return "\(label) \(i + 1)"
    }

    /// A tab's capsule over the chat, or its line in a list of tabs: the tab's name, and the agent's beside its tab
    /// mates (`role`) — the tab alone would be the same for all of them.
    static func tabLabel(_ s: Session, role: Role?) -> String {
        guard let role else { return s.slotLabel ?? s.displayTitle }
        return [s.slotLabel, role.name].compactMap { $0 }.joined(separator: " · ")
    }

    /// The agents of `list` in the same tab as `s` (itself included), in pane order; just `[s]` outside a tab.
    static func tabMates(of s: Session, in list: [Session]) -> [Session] {
        guard tabKey(s) != nil else { return [s] }
        var mates = list.filter { sameSlot($0, s) }
        if !mates.contains(where: { $0.key == s.key }) { mates.append(s) }
        return mates.sorted(by: inPaneOrder)
    }
}
