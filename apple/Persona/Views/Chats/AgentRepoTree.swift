import Foundation

/// Where an agent works: the repository and the working tree in it. The host may name the repository (`repo_root`,
/// #2840: Paseo keeps its worktrees under its own folder): a working directory inside it is its main checkout, any other
/// one of its trees. Otherwise from the working directory alone, by the web's rules (`web/src/lib/agent-repos.ts`): a
/// path under one of the usual working-tree folders belongs to the directory above that folder; any other path is its
/// own repository's main checkout.
nonisolated struct RepoPlace: Equatable, Sendable {
    /// The repository's path; "" without a working directory.
    var repo: String
    var name: String
    /// The working tree's path.
    var worktree: String
    /// The repository's main checkout, not a tree under it.
    var main: Bool

    /// Where herdr, Claude Code and a home-level collection keep working trees.
    static let markers = ["/.worktrees/", "/.claude/worktrees/", "/.herdr/worktrees/"]

    init(repo: String, name: String, worktree: String, main: Bool) {
        self.repo = repo
        self.name = name
        self.worktree = worktree
        self.main = main
    }

    init(cwd: String, repoRoot: String? = nil) {
        var path = Substring(cwd)
        while path.hasSuffix("/") { path = path.dropLast() }
        guard !path.isEmpty else {
            self.init(repo: "", name: "", worktree: "", main: true)
            return
        }
        var root = Substring(repoRoot ?? "")
        while root.hasSuffix("/") { root = root.dropLast() }
        if !root.isEmpty {
            let inside = path == root || path.hasPrefix(String(root) + "/")
            self.init(repo: String(root), name: Self.baseName(String(root)), worktree: String(inside ? root : path), main: inside)
            return
        }
        for marker in Self.markers {
            guard let at = path.range(of: marker), at.lowerBound > path.startIndex else { continue }
            let rest = path[at.upperBound...].split(separator: "/" as Character)
            let repo = String(path[..<at.lowerBound])
            // ~/.worktrees/<repo>/<tree>: the folder sits at the home level, so the repository is the first part after
            // it and the tree is what follows.
            if rest.count >= 2 {
                self.init(repo: repo + marker + rest[0], name: String(rest[0]), worktree: String(path), main: false)
            } else {
                self.init(repo: repo, name: Self.baseName(repo), worktree: String(path), main: false)
            }
            return
        }
        self.init(repo: String(path), name: Self.baseName(String(path)), worktree: String(path), main: true)
    }

    private static func baseName(_ path: String) -> String {
        path.split(separator: "/" as Character).last.map(String.init) ?? ""
    }
}

/// By Repository (docs/apple/ios.md「Agents 的分组」): every agent under its machine, its repository (`RepoPlace`) and the
/// working tree in it, as Orca's projects with the machine above them. Plain data, unit-tested apart from the list
/// (`AgentRepoTreeTests`); the list orders the agents inside a tree (`AgentListOrder`).
nonisolated struct RepoTree {
    struct Worktree: Identifiable, Sendable {
        /// Its fold id.
        var id: String
        var name: String
        var path: String
        var main: Bool
        var sessions: [Session]
        /// The spaces its host nests under it (Orca's child worktrees, `parent_space_id`, #3075), every level, depth
        /// first: listed behind one「N 个子项」row, as Orca's sidebar does.
        var children: [Worktree] = []
        /// The fold id of its「N 个子项」row; folded until opened (`childFoldSuffix`).
        var childrenFoldID: String { id + RepoTree.childFoldSuffix }
        /// Its agents and its children's.
        var treeSessions: [Session] { sessions + children.flatMap(\.sessions) }
    }

    /// One repository on one machine.
    struct Repo: Identifiable, Sendable {
        /// Its fold id.
        var id: String
        var machine: MachineRef
        var name: String
        var path: String
        /// The main checkout first, then by name.
        var worktrees: [Worktree]
        var sessions: [Session] { worktrees.flatMap(\.treeSessions) }
        /// Only its main checkout, nothing nested under it: its agents sit right under it, with no working-tree level.
        var flat: Bool { worktrees.count == 1 && worktrees[0].main && worktrees[0].children.isEmpty }
    }

    /// One machine's repositories, by name.
    struct Host: Identifiable, Sendable {
        var ref: MachineRef
        var repos: [Repo]
        var id: String { ref.path }
        var sessions: [Session] { repos.flatMap(\.sessions) }
    }

    /// The name a repository goes by when its agents have no working directory.
    static let unnamed = String(localized: "Other")
    /// A working tree's「N 个子项」fold id: its id and this.
    static let childFoldSuffix = String(localized: "/sub-items")

    /// A session's space on its machine, unique across hosts; nil outside a space.
    static func spaceKey(_ s: Session) -> String? {
        s.spaceID.map { s.machineRef.path + "/" + (s.spaceKind ?? "") + ":" + $0 }
    }

    /// The space each nested space hangs under (`parent_space_id`): only a parent that is listed (it has a live agent),
    /// on the same machine and host, and not itself; a space whose parents loop back stays at the top (#3075).
    static func parents(_ sessions: [Session]) -> [String: String] {
        var parentOf: [String: String] = [:]
        let listed = Set(sessions.compactMap(spaceKey))
        for s in sessions {
            guard let key = spaceKey(s), parentOf[key] == nil, let pid = s.parentSpaceID, !pid.isEmpty else { continue }
            let parent = s.machineRef.path + "/" + (s.spaceKind ?? "") + ":" + pid
            if parent != key && listed.contains(parent) { parentOf[key] = parent }
        }
        func loops(_ key: String) -> Bool {
            var seen: Set<String> = [key]
            var at = parentOf[key]
            while let p = at {
                if !seen.insert(p).inserted { return true }
                at = parentOf[p]
            }
            return false
        }
        return parentOf.filter { !loops($0.key) }
    }

    /// `sessions` (the live agents the list shows) under their machines in `machines`' order (the hub's); a machine it
    /// does not list goes last. A space nested under another (Orca's child worktrees) goes under its top space's working
    /// tree, whatever its own directory says.
    static func build(_ sessions: [Session], machines: [MachineRef]) -> [Host] {
        let parentOf = parents(sessions)
        let nested = sessions.filter { s in spaceKey(s).map { parentOf[$0] != nil } ?? false }
        let top = sessions.filter { s in spaceKey(s).map { parentOf[$0] == nil } ?? true }
        var repos: [String: Repo] = [:]
        var worktreeIndices: [String: [String: Int]] = [:]
        /// Where each top space's first agent went: its repository's key and working tree's path.
        var placeOf: [String: (repo: String, worktree: String)] = [:]
        for s in top {
            let place = RepoPlace(cwd: s.cwd, repoRoot: s.repoRoot)
            let key = s.machineRef.path + "/" + place.repo
            if repos[key] == nil {
                repos[key] = Repo(id: "repo/" + key, machine: s.machineRef,
                                  name: place.name.isEmpty ? unnamed : place.name, path: place.repo, worktrees: [])
            }
            // Mutate the stored value in place: a local Repo copy detaches its growing arrays on every append.
            if let i = worktreeIndices[key]?[place.worktree] {
                repos[key]!.worktrees[i].sessions.append(s)
            } else {
                let name = place.worktree == place.repo ? repos[key]!.name : (place.worktree.split(separator: "/" as Character).last.map(String.init) ?? "")
                let i = repos[key]!.worktrees.count
                repos[key]!.worktrees.append(Worktree(id: "tree/" + s.machineRef.path + "/" + place.worktree, name: name,
                                                    path: place.worktree, main: place.main, sessions: [s]))
                worktreeIndices[key, default: [:]][place.worktree] = i
            }
            if let space = spaceKey(s), placeOf[space] == nil { placeOf[space] = (key, place.worktree) }
        }
        // Each nested space a working tree of its own, under its top space's working tree: depth first, each level by
        // the host's order of spaces.
        let bySpace = Dictionary(grouping: nested) { spaceKey($0)! }
        var childrenOf: [String: [String]] = [:]
        for key in bySpace.keys { childrenOf[parentOf[key]!, default: []].append(key) }
        func index(_ key: String) -> Int { bySpace[key]?.first?.spaceIndex ?? Int.max }
        func descendants(_ key: String) -> [String] {
            (childrenOf[key] ?? []).sorted { index($0) != index($1) ? index($0) < index($1) : $0 < $1 }
                .flatMap { [$0] + descendants($0) }
        }
        for rootKey in placeOf.keys.sorted() where childrenOf[rootKey] != nil {
            let at = placeOf[rootKey]!
            guard let i = repos[at.repo]?.worktrees.firstIndex(where: { $0.path == at.worktree }) else { continue }
            repos[at.repo]!.worktrees[i].children += descendants(rootKey).map { key in
                let list = bySpace[key]!
                let first = list[0]
                let name = first.spaceName.flatMap { $0.isEmpty ? nil : $0 }
                    ?? first.cwd.split(separator: "/" as Character).last.map(String.init) ?? first.spaceID ?? ""
                return Worktree(id: "tree/space/" + key, name: name, path: first.cwd, main: false, sessions: list)
            }
        }
        var hosts: [MachineRef: [Repo]] = [:]
        for var repo in repos.values {
            repo.worktrees.sort { a, b in a.main != b.main ? a.main : before(a.name, a.path, b.name, b.path) }
            hosts[repo.machine, default: []].append(repo)
        }
        return hostOrder(Set(hosts.keys), machines: machines).map { ref in
            Host(ref: ref, repos: hosts[ref, default: []].sorted { before($0.name, $0.path, $1.name, $1.path) })
        }
    }

    /// `refs` in `machines`' order (the hub's); one it does not list goes last, by its path.
    static func hostOrder(_ refs: Set<MachineRef>, machines: [MachineRef]) -> [MachineRef] {
        let index = Dictionary(machines.enumerated().map { ($1, $0) }, uniquingKeysWith: { a, _ in a })
        return refs.sorted { a, b in
            let ia = index[a] ?? Int.max, ib = index[b] ?? Int.max
            return ia != ib ? ia < ib : a.path < b.path
        }
    }

    /// By name as Finder sorts them ("app2" before "app10"), then by path: one order on every render.
    private static func before(_ a: String, _ pathA: String, _ b: String, _ pathB: String) -> Bool {
        switch a.localizedStandardCompare(b) {
        case .orderedAscending: true
        case .orderedDescending: false
        case .orderedSame: pathA < pathB
        }
    }
}
