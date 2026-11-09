import Foundation

/// Immutable derived data for one structural list revision; views and actor-owned model closures stay out of it.
nonisolated struct AgentListProjection: Sendable {
    struct Preferences: Equatable, Sendable {
        var pins = Pins()
        var mutes = Pins()
    }

    struct Settings: Equatable, Sendable {
        var grouping = "repo"
        var sort = "state"
        var filter = AgentFilter.all
        var query = ""
        var machine = ""
        var hub = ""
        var multiHub = false
        var kept = Set<SessionKey>()
        var held: SessionKey?
        var preferences: [String: Preferences] = [:]
        var pagedGroups = Set<String>()
        var listQuery = SessionListQuery()

        func pinned(_ key: SessionKey) -> Bool {
            preferences[key.group]?.pins.sessions.contains("\(key.machineID)/\(key.sessionID)") == true
        }

        func muted(_ session: Session) -> Bool {
            preferences[session.group]?.mutes.mutes(session: "\(session.machineID)/\(session.sessionID)",
                space: session.spaceName.map { "\(session.machineID)/\($0)" }) == true
        }

        func pinned(_ space: SpaceGroup) -> Bool {
            preferences[space.group]?.pins.spaces.contains("\(space.machineID)/\(space.name)") == true
        }

        func muted(_ space: SpaceGroup) -> Bool {
            preferences[space.group]?.mutes.spaces.contains("\(space.machineID)/\(space.name)") == true
        }

        var order: AgentListOrder {
            AgentListOrder(isPinned: pinned, isMuted: muted, byRank: filter == .all && sort == "state", held: held)
        }

        func scope(machine: String, hub: String, space: String = "", project: String = "") -> RuntimeSessionLists.Scope {
            var query = listQuery
            query.machine = machine
            query.group = space
            query.project = project
            return RuntimeSessionLists.Scope(hub: hub, query: query)
        }
    }

    struct Input: Sendable {
        var sessions: [Session]
        var machines: [Machine]
        var settings: Settings
        var workspaces: [HostWorkspace] = []
    }

    struct Rows: Sendable {
        var all: [TabPanes.Entry]
        var busy: [TabPanes.Entry]
        var idle: [TabPanes.Entry]
    }

    struct Repository: Sendable {
        var sessions: [Session]
        var space: SpaceGroup?
        var counts: SpaceGroup
        var unread: [SessionKey]
        var quiet: Bool
    }

    let identity = UUID()
    var base: [Session] = []
    var filtered: [Session] = []
    var matching = Set<SessionKey>()
    var unpinned: [Session] = []
    var pinnedAgents: [Session] = []
    var unread: [SessionKey] = []
    var active: [SpaceGroup] = []
    var quiet: [SpaceGroup] = []
    var tree: [RepoTree.Host] = []
    var byHost = false
    var filterCounts: [AgentFilter: Int] = [:]
    var sessionsByKey: [SessionKey: Session] = [:]
    var machinesByRef: [MachineRef: Machine] = [:]
    var scopes: [String: RuntimeSessionLists.Scope] = [:]
    var repositories: [String: Repository] = [:]
    var busyRepositories: [String: [RepoTree.Repo]] = [:]
    var quietRepositories: [String: [SpaceGroup]] = [:]
    var rows: [String: Rows] = [:]
    var quietWorktrees: [String: Bool] = [:]
    var menuSections: [String] = []
    /// Searching can match an Agent-free worktree even when no session matches the query.
    var hasSearchResults: Bool { !base.isEmpty || !tree.isEmpty }

    static func build(_ input: Input) -> Self {
        let settings = input.settings
        let order = settings.order
        var result = Self()
        result.sessionsByKey = Dictionary(input.sessions.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        result.machinesByRef = Dictionary(input.machines.map { ($0.ref, $0) }, uniquingKeysWith: { a, _ in a })
        result.base = input.sessions.filter { session in
            !session.assistant && (settings.machine.isEmpty || session.machineRef.path == settings.machine) &&
                (!settings.multiHub || HubFilter.matches(settings.hub, session.group)) &&
                (settings.query.isEmpty || settings.pagedGroups.contains(session.group) ||
                    [session.title, session.cwd, session.lastText ?? "", session.spaceName ?? "", session.slotName ?? ""]
                        .contains { $0.localizedCaseInsensitiveContains(settings.query) })
        }
        for filter in AgentFilter.allCases where filter != .all {
            result.filterCounts[filter] = result.base.count { filter.matches($0, muted: settings.muted($0)) }
        }
        if settings.filter != .all {
            result.matching = Set(result.base.filter { settings.filter.matches($0, muted: settings.muted($0)) }.map(\.key))
        }
        result.filtered = settings.filter == .all ? result.base : result.base.filter {
            result.matching.contains($0.key) || settings.kept.contains($0.key)
        }
        result.unpinned = result.filtered.filter { !settings.pinned($0.key) }
        result.pinnedAgents = result.filtered.filter { settings.pinned($0.key) }
            .map { (session: $0, rank: settings.preferences[$0.group]?.pins.sessions.firstIndex(of: "\($0.machineID)/\($0.sessionID)") ?? 0) }
            .sorted { $0.rank < $1.rank }.map(\.session)
        result.unread = result.filtered.filter { $0.state == .unread }.map(\.key)
        result.byHost = settings.machine.isEmpty && Set(result.base.lazy.filter { $0.state.isLive }.map(\.machineRef)).count > 1
        for machine in input.machines {
            result.scopes["machine/" + machine.ref.path] = settings.scope(machine: machine.id, hub: machine.group)
        }

        // A selected grouping builds its own structure, not every alternative on each body evaluation.
        if settings.grouping == "space" {
            let groups = SpaceGroup.build(result.unpinned).map { group in
                var group = group
                let scope = settings.scope(machine: group.machineID, hub: group.group, space: group.spaceID == "-" ? "" : group.spaceID)
                result.scopes[group.id] = scope
                group.bounded = settings.pagedGroups.contains(group.group)
                return group
            }
            let pinned = groups.filter { settings.pinned($0) && !settings.muted($0) }
            let spaces = settings.sort == "updated" ? latestFirst(groups) : groups
            let busy = spaces.filter { !order.isQuiet($0) }
            result.active = settings.filter != .all
                ? pinned + latestFirst(groups.filter { !(settings.pinned($0) && !settings.muted($0)) })
                : pinned + busy.filter { !settings.pinned($0) && !settings.muted($0) } + busy.filter { settings.muted($0) }
            result.quiet = settings.filter != .all ? [] : groups.filter { order.isQuiet($0) && !(settings.pinned($0) && !settings.muted($0)) }
            for group in groups { result.rows[group.id] = partition(group.sessions, settings: settings) }
            result.menuSections = result.active.map(\.id)
        } else if settings.grouping == "repo" {
            let catalog = input.workspaces.filter { workspace in
                input.machines.contains { $0.ref == workspace.machineRef } &&
                    (settings.machine.isEmpty || workspace.machineRef.path == settings.machine) &&
                    (!settings.multiHub || HubFilter.matches(settings.hub, workspace.group))
            }
            result.tree = RepoTree.build(result.unpinned.filter { $0.state.isLive }, machines: input.machines.map(\.ref),
                                         workspaces: catalog, includeEmpty: settings.filter == .all, search: settings.query)
            result.byHost = settings.machine.isEmpty && result.tree.count > 1
            for host in result.tree {
                for repo in host.repos {
                    let sessions = repo.sessions
                    let scope = settings.scope(machine: repo.machine.id, hub: repo.machine.group, project: repo.path)
                    result.scopes[repo.id] = scope
                    let spaces = SpaceGroup.build(sessions)
                    let counts = SpaceGroup(id: repo.id, machineID: repo.machine.id, spaceID: "-", group: repo.machine.group,
                        name: repo.name, index: 0, sessions: sessions,
                        bounded: settings.pagedGroups.contains(repo.machine.group))
                    let quiet = !repo.structured && settings.filter == .all && sessions.allSatisfy { !order.stays($0) }
                    result.repositories[repo.id] = Repository(sessions: sessions,
                        space: spaces.count == 1 && spaces[0].spaceID != "-" ? spaces[0] : nil,
                        counts: counts, unread: sessions.filter { $0.state == .unread }.map(\.key), quiet: quiet)
                    for worktree in repo.worktrees {
                        func prepare(_ item: RepoTree.Worktree) {
                            result.rows[item.id] = partition(item.sessions, settings: settings)
                            result.quietWorktrees[item.id] = settings.filter == .all && item.treeSessions.allSatisfy { !order.stays($0) }
                            for child in item.children { prepare(child) }
                        }
                        prepare(worktree)
                    }
                }
            }
            if settings.sort == "updated" {
                result.tree = result.tree.map { host in
                    var host = host
                    host.repos = AgentListOrder.latestFirst(host.repos,
                        date: { result.repositories[$0.id]?.sessions.map(\.sortDate).max() ?? .distantPast }, id: \.id)
                    return host
                }
            }
            for host in result.tree {
                result.busyRepositories[host.id] = host.repos.filter { result.repositories[$0.id]?.quiet != true }
                result.quietRepositories[host.id] = host.repos.filter { result.repositories[$0.id]?.quiet == true }.map { repo in
                    SpaceGroup(id: repo.id, machineID: repo.machine.id, spaceID: "-", group: repo.machine.group,
                               name: repo.name, index: 0, sessions: order.ordered(result.repositories[repo.id]!.sessions))
                }
            }
            result.menuSections = result.tree.flatMap(\.repos).filter { result.repositories[$0.id]?.quiet != true }.map(\.id)
        } else if settings.grouping != "recent" && settings.grouping != "todo" {
            result.menuSections = order.sectionIDs(result.unpinned, byMachine: settings.grouping == "machine")
        }
        return result
    }

    private static func latestFirst(_ groups: [SpaceGroup]) -> [SpaceGroup] {
        AgentListOrder.latestFirst(groups, date: \.sortDate, id: \.id)
    }

    private static func partition(_ sessions: [Session], settings: Settings) -> Rows {
        let order = settings.order
        let entries = TabPanes.entries(order.ordered(sessions))
        guard settings.filter == .all else { return Rows(all: entries, busy: entries, idle: []) }
        let stays = { (entry: TabPanes.Entry) in entry.sessions.contains(where: order.stays) }
        return Rows(all: entries, busy: entries.filter(stays), idle: entries.filter { !stays($0) })
    }
}
