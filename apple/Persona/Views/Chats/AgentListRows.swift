import Foundation

/// The agents the Agents list shows, top to bottom, as a plain function of what it is built from (#2213): ↑ / ↓ on the
/// Mac open the next one from it. It follows the list's own sections (`ChatsView`): Pinned, then the grouping, a folded
/// section or a closed "N more idle" hiding its rows. The Needs You cards are left out: their agents have rows below.
/// Unit-tested in `AgentListOrderTests`.
struct AgentListRows {
    var order: AgentListOrder
    var grouping: DevGrouping
    /// The quick filter is All: idle rows fold away (under one they are all listed).
    var all = true
    /// A search opens every fold.
    var searching = false
    var isFolded: (String) -> Bool
    /// Opened "N more idle" rows (`expandedIdle`) and opened quiet spaces, repositories and working trees (`expandedQuiet`).
    var expandedIdle: Set<String> = []
    var expandedQuiet: Set<String> = []

    private func folded(_ id: String) -> Bool { !searching && isFolded(id) }

    /// Busy rows, then the idle ones while their "N more idle" is open (`ChatsView.agentRows`); a tab's agents together,
    /// none of them while the tab is folded.
    func agentRows(_ list: [Session], key: String) -> [Session] {
        let rows = order.tabEntries(list, all: all)
        let shown = rows.busy + (rows.idle.isEmpty || !(searching || expandedIdle.contains(key)) ? [] : rows.idle)
        return entryRows(shown)
    }

    /// The agents of `entries`, a folded tab's left out.
    func entryRows(_ entries: [TabPanes.Entry]) -> [Session] {
        entries.flatMap { e -> [Session] in
            if case .tab(let g) = e, folded(g.foldID) { return [] }
            return e.sessions
        }
    }

    /// By Project: active spaces (each foldable), then the quiet ones that were opened; per machine when `byHost`.
    func spaces(active: [SpaceGroup], quiet: [SpaceGroup], hosts: [MachineRef]?) -> [Session] {
        func section(_ active: [SpaceGroup], _ quiet: [SpaceGroup]) -> [Session] {
            active.flatMap { g in folded(g.id) ? [] : agentRows(g.sessions, key: g.id) }
                + quiet.flatMap { g in searching || expandedQuiet.contains(g.id) ? TabPanes.grouped(order.ordered(g.sessions)) : [] }
        }
        guard let hosts else { return section(active, quiet) }
        return hosts.flatMap { ref in
            folded(Self.hostID(ref)) ? [] : section(active.filter { $0.machineRef == ref }, quiet.filter { $0.machineRef == ref })
        }
    }

    /// By Repository: per machine, the repositories with something going on (flat, or by working tree), then the quiet ones.
    func repos(_ tree: [RepoTree.Host], byHost: Bool) -> [Session] {
        tree.flatMap { host -> [Session] in
            if byHost && folded(Self.hostID(host.ref)) { return [] }
            let quiet = host.repos.filter(isQuiet)
            let busy = host.repos.filter { !isQuiet($0) }.flatMap { r -> [Session] in
                if folded(r.id) { return [] }
                if r.flat { return agentRows(r.worktrees[0].sessions, key: r.worktrees[0].id) }
                return r.worktrees.flatMap(worktree)
            }
            return busy + quiet.flatMap { r in searching || expandedQuiet.contains(r.id) ? TabPanes.grouped(order.ordered(r.sessions)) : [] }
        }
    }

    /// A repository with nothing going on: in Idle Repositories (`ChatsView.isQuiet`).
    func isQuiet(_ r: RepoTree.Repo) -> Bool { !r.structured && all && r.sessions.allSatisfy { !order.stays($0) } }

    /// A working tree's agents, then its nested spaces' while its「N 个子项」is open (`ChatsView.childWorktreeRows`).
    private func worktree(_ w: RepoTree.Worktree) -> [Session] {
        if w.catalog {
            let own = w.sessions.count > 1 && folded(w.agentsFoldID) ? [] : order.ordered(w.sessions)
            return own + (folded(w.childrenFoldID) ? [] : w.children.flatMap(worktree))
        }
        let quiet = all && w.treeSessions.allSatisfy { !order.stays($0) }
        let open = searching || (quiet ? expandedQuiet.contains(w.id) : !isFolded(w.id))
        guard open else { return [] }
        let own = quiet ? entryRows(TabPanes.entries(order.ordered(w.sessions))) : agentRows(w.sessions, key: w.id)
        guard !w.children.isEmpty, !folded(w.childrenFoldID) else { return own }
        return own + w.children.flatMap(worktree)
    }

    /// By Status: a section per rank, each foldable (Seen starts folded).
    func states(_ live: [Session]) -> [Session] {
        let byRank = Dictionary(grouping: live, by: order.rank)
        return AgentListOrder.stateKeys.indices.flatMap { i in
            folded("state/" + AgentListOrder.stateKeys[i]) ? [] : order.ordered(byRank[i] ?? [])
        }
    }

    /// By Machine: per machine, its busy rows and its idle ones while Idle is open.
    func machines(_ live: [Session], order refs: [MachineRef]) -> [Session] {
        refs.flatMap { ref -> [Session] in
            let rows = order.ordered(live.filter { $0.machineRef == ref })
            if folded("machine/" + ref.path) { return [] }
            guard all else { return rows }
            let idle = rows.filter { !order.stays($0) }
            return rows.filter(order.stays) + (searching || expandedIdle.contains("machine-" + ref.path) ? idle : [])
        }
    }

    /// By To-do: a section per todo, Unassigned last; `isFolded` per group (Unassigned has its own default, `TodoAgentGroup.folded`).
    func todos(_ groups: [TodoAgentGroup], isFolded: (TodoAgentGroup) -> Bool) -> [Session] {
        groups.flatMap { g in !searching && isFolded(g) ? [] : g.sessions }
    }

    static func hostID(_ ref: MachineRef) -> String { "machine/" + ref.path }

    /// The one after (`by` 1) or before (-1) `current` in `rows`; the first or the last when nothing listed is open.
    static func step(_ rows: [SessionKey], from current: SessionKey?, by: Int) -> SessionKey? {
        guard !rows.isEmpty else { return nil }
        guard let current, let i = rows.firstIndex(of: current) else { return by > 0 ? rows.first : rows.last }
        let j = i + by
        return rows.indices.contains(j) ? rows[j] : nil
    }
}
