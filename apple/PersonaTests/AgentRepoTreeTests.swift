import Foundation
import Testing
@testable import Persona

/// By Repository (`RepoPlace`, `RepoTree`): the repository and working tree a working directory names, and the machines →
/// repositories → working trees the Agents list draws. The path rules are the web's (`web/src/lib/agent-repos.test.ts`).
struct AgentRepoTreeTests {
    private func session(_ id: String, _ cwd: String, machine: String = "machine-a", group: String = "", repoRoot: String? = nil,
                         worktree: String? = nil, branch: String? = nil) -> Session {
        let hub = group.isEmpty ? "" : #","app_hub_group":"\#(group)""#
        let repo = repoRoot.map { #","repo_root":"\#($0)""# } ?? ""
        let tree = worktree.map { #","worktree":"\#($0)""# } ?? ""
        let head = branch.map { #","branch":"\#($0)""# } ?? ""
        let json = """
        {"machine_id":"\(machine)","session_id":"\(id)","title":"\(id)","cwd":"\(cwd)","status":"idle","state":"idle"\(hub)\(repo)\(tree)\(head)}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private let machines = [MachineRef(id: "machine-b"), MachineRef(id: "machine-a")]

    // MARK: Where a working directory sits

    @Test func aMainCheckoutIsTheRepositoryItself() {
        #expect(RepoPlace(cwd: "/code/app") == RepoPlace(repo: "/code/app", name: "app", worktree: "/code/app", main: true))
        #expect(RepoPlace(cwd: "/code/app/") == RepoPlace(cwd: "/code/app"), "a trailing slash is the same directory")
    }

    @Test func theUsualWorkingTreeFoldersBelongToTheRepositoryAboveThem() {
        for cwd in ["/code/app/.worktrees/fix-login", "/code/app/.claude/worktrees/fix-login", "/code/app/.herdr/worktrees/fix-login"] {
            #expect(RepoPlace(cwd: cwd) == RepoPlace(repo: "/code/app", name: "app", worktree: cwd, main: false), "\(cwd)")
        }
    }

    @Test func aHomeLevelCollectionNamesTheRepositoryAfterTheFolder() {
        #expect(RepoPlace(cwd: "/home/me/.worktrees/persona/fix-login")
            == RepoPlace(repo: "/home/me/.worktrees/persona", name: "persona", worktree: "/home/me/.worktrees/persona/fix-login", main: false))
    }

    @Test func withoutAWorkingTreeFolderTheDirectoryIsItsOwnRepository() {
        #expect(RepoPlace(cwd: "/code/app/server/internal")
            == RepoPlace(repo: "/code/app/server/internal", name: "internal", worktree: "/code/app/server/internal", main: true))
        #expect(RepoPlace(cwd: "") == RepoPlace(repo: "", name: "", worktree: "", main: true))
    }

    /// Paseo keeps its worktrees under its own folder, whose paths name no repository: its host names it (#2840).
    @Test func aRepositoryTheHostNamesComesBeforeThePath() {
        #expect(RepoPlace(cwd: "/home/me/.paseo/worktrees/1a2b/fix-login", repoRoot: "/code/app")
            == RepoPlace(repo: "/code/app", name: "app", worktree: "/home/me/.paseo/worktrees/1a2b/fix-login", main: false))
        #expect(RepoPlace(cwd: "/code/app/server/", repoRoot: "/code/app/")
            == RepoPlace(repo: "/code/app", name: "app", worktree: "/code/app", main: true), "a directory inside it is its main checkout")
        #expect(RepoPlace(cwd: "/code/app-old", repoRoot: "/code/app")
            == RepoPlace(repo: "/code/app", name: "app", worktree: "/code/app-old", main: false), "a shared prefix is not inside it")
        #expect(RepoPlace(cwd: "/code/app/.worktrees/fix", repoRoot: "") == RepoPlace(cwd: "/code/app/.worktrees/fix"), "an empty one names none")
    }

    // MARK: The tree

    @Test func theMainCheckoutComesFirstThenTreesByName() throws {
        let tree = RepoTree.build([session("main", "/code/app"), session("wt-b", "/code/app/.worktrees/fix-b"),
                                   session("wt-a", "/code/app/.worktrees/fix-a"), session("wt-a2", "/code/app/.worktrees/fix-a")],
                                  machines: machines)
        let repo = try #require(tree.first?.repos.first)
        #expect(tree.count == 1 && tree[0].repos.count == 1)
        #expect(repo.name == "app" && !repo.flat)
        #expect(repo.worktrees.map { $0.name + ($0.main ? "*" : "") } == ["app*", "fix-a", "fix-b"])
        #expect(repo.worktrees[1].sessions.map(\.sessionID) == ["wt-a", "wt-a2"])
        #expect(repo.sessions.count == 4)
    }

    @Test func worktreesElsewhereJoinTheRepositoryTheirHostNames() throws {
        let tree = RepoTree.build([session("main", "/code/app", repoRoot: "/code/app"),
                                   session("wt-b", "/home/me/.paseo/worktrees/3c4d/fix-b", repoRoot: "/code/app"),
                                   session("wt-a", "/home/me/.paseo/worktrees/1a2b/fix-a", repoRoot: "/code/app"),
                                   session("herdr", "/code/app/.worktrees/fix-c")], machines: machines)
        let repo = try #require(tree.first?.repos.first)
        #expect(tree.count == 1 && tree[0].repos.count == 1)
        #expect(repo.name == "app" && repo.path == "/code/app" && repo.id == "repo/machine-a//code/app")
        #expect(repo.worktrees.map { $0.name + ($0.main ? "*" : "") } == ["app*", "fix-a", "fix-b", "fix-c"])
        #expect(repo.worktrees[1].id == "tree/machine-a//home/me/.paseo/worktrees/1a2b/fix-a")
    }

    @Test func aRepositoryWithOnlyItsMainCheckoutIsFlat() throws {
        let repo = try #require(RepoTree.build([session("a", "/code/app"), session("b", "/code/app")], machines: machines).first?.repos.first)
        #expect(repo.flat)
        #expect(repo.worktrees[0].sessions.map(\.sessionID) == ["a", "b"])
    }

    @Test func repositoriesGoByNameAsFinderSortsThem() {
        let tree = RepoTree.build([session("z", "/code/zeta"), session("10", "/code/app10"), session("2", "/code/app2"),
                                   session("a", "/code/Alpha")], machines: machines)
        #expect(tree[0].repos.map(\.name) == ["Alpha", "app2", "app10", "zeta"])
    }

    @Test func machinesGoInTheHubsOrderAndOneItDoesNotListGoesLast() {
        let tree = RepoTree.build([session("a", "/code/app"), session("x", "/code/app", machine: "machine-x"),
                                   session("b", "/code/app", machine: "machine-b")], machines: machines)
        #expect(tree.map(\.id) == ["machine-b", "machine-a", "machine-x"])
    }

    @Test func sameNamedRepositoriesOnTwoMachinesStayApart() {
        let tree = RepoTree.build([session("a", "/code/app"), session("b", "/code/app", machine: "machine-b")], machines: machines)
        #expect(tree.map { $0.id + ":" + $0.repos.map(\.name).joined() } == ["machine-b:app", "machine-a:app"])
        #expect(Set(tree.flatMap(\.repos).map(\.id)).count == 2, "their fold ids differ")
    }

    @Test func theSameMachineIdInAnotherGroupOfHubsIsAnotherMachine() {
        let tree = RepoTree.build([session("a", "/code/app"), session("b", "/code/app", group: "work")], machines: machines)
        #expect(tree.map(\.id) == ["machine-a", "work|machine-a"])
    }

    @Test func anAgentWithoutAWorkingDirectoryGoesUnderOther() {
        let tree = RepoTree.build([session("a", "")], machines: machines)
        #expect(tree[0].repos.map(\.name) == [RepoTree.unnamed])
    }

    @Test func foldIdsAreStableAndApart() {
        let tree = RepoTree.build([session("a", "/code/app"), session("b", "/code/app/.worktrees/fix")], machines: machines)
        let repo = tree[0].repos[0]
        #expect(repo.id == "repo/machine-a//code/app")
        #expect(repo.worktrees.map(\.id) == ["tree/machine-a//code/app", "tree/machine-a//code/app/.worktrees/fix"])
    }

    // MARK: Spaces nested under another (Orca's child worktrees, #3075)

    private func orca(_ id: String, _ cwd: String, space: String, parent: String? = nil, index: Int = 0,
                      kind: String = "orca", machine: String = "machine-a") -> Session {
        let parentField = parent.map { #","parent_space_id":"\#($0)""# } ?? ""
        let json = """
        {"machine_id":"\(machine)","session_id":"\(id)","title":"\(id)","cwd":"\(cwd)","status":"idle","state":"idle",
         "space_kind":"\(kind)","space_id":"\(space)","space_name":"\(space)","space_index":\(index)\(parentField)}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    @Test func childSpacesGoUnderTheirParentsWorkingTreeDepthFirst() throws {
        // Orca keeps worktrees outside the repository: by the path alone each would be a repository of its own.
        let tree = RepoTree.build([
            orca("p", "/code/app", space: "w-main", index: 1),
            orca("c2", "/orca/app/metrics", space: "w-metrics", parent: "w-main", index: 5),
            orca("c1", "/orca/app/refresh", space: "w-refresh", parent: "w-main", index: 3),
            orca("g1", "/orca/app/refresh-docs", space: "w-docs", parent: "w-refresh", index: 4),
        ], machines: machines)
        let repos = try #require(tree.first?.repos)
        #expect(repos.map(\.name) == ["app"], "the children are not repositories of their own")
        let repo = repos[0]
        #expect(!repo.flat, "a main checkout with children shows its working-tree level")
        #expect(repo.worktrees.count == 1)
        let main = repo.worktrees[0]
        #expect(main.sessions.map(\.sessionID) == ["p"])
        #expect(main.children.map(\.name) == ["w-refresh", "w-metrics"], "only direct children, in host order")
        #expect(main.children[0].children.map(\.name) == ["w-docs"])
        #expect(main.children.map(\.id) == ["tree/space/machine-a/orca:w-refresh", "tree/space/machine-a/orca:w-metrics"])
        #expect(main.childrenFoldID == "tree/machine-a//code/app" + RepoTree.childFoldSuffix)
        #expect(repo.sessions.map(\.sessionID).sorted() == ["c1", "c2", "g1", "p"])
        #expect(main.treeSessions.count == 4)
    }

    @Test func aChildWhoseParentIsNotListedStaysAtTheTop() {
        let tree = RepoTree.build([orca("c", "/orca/app/login", space: "w-login", parent: "w-gone")], machines: machines)
        #expect(tree[0].repos.map(\.name) == ["login"])
        #expect(tree[0].repos[0].worktrees[0].children.isEmpty)
    }

    @Test func loopsSelfParentsAndOtherHostsDoNotNest() {
        let tree = RepoTree.build([
            orca("a", "/code/a", space: "w2", parent: "w3"),
            orca("b", "/code/b", space: "w3", parent: "w2"),
            orca("self", "/code/self", space: "w6", parent: "w6"),
            orca("h", "/code/h", space: "w4", parent: "w5", kind: "herdr"),
            orca("o", "/code/o", space: "w5"),
            orca("m", "/code/m", space: "w7", parent: "w5", machine: "machine-b"),
        ], machines: machines)
        #expect(tree.flatMap(\.repos).flatMap(\.worktrees).allSatisfy { $0.children.isEmpty })
        #expect(tree.flatMap(\.repos).count == 6)
        #expect(RepoTree.parents(tree.flatMap(\.repos).flatMap(\.sessions)).isEmpty)
    }

    @Test func parentSpaceIdIsReadAndOptional() throws {
        #expect(orca("c", "/x", space: "w1", parent: "w0").parentSpaceID == "w0")
        #expect(orca("p", "/x", space: "w0").parentSpaceID == nil)
    }

    /// An app that never chose a grouping shows By Repository (#2108), on a hub with todos or without.
    @Test @MainActor func byRepositoryIsTheDefault() {
        #expect(DevGrouping.standard == .repo)
        #expect(DevGrouping.resolved(.standard, supportsTodos: false) == .repo)
        #expect(DevGrouping.offered(supportsTodos: false).first == .repo)
    }

    // MARK: The node's git place (#1805): machine → project → branch → agents

    @Test func linkedWorktreesGoUnderTheirMainRepositoryByBranch() {
        let root = "/home/u/Code/app", fix = "/home/u/orca/workspaces/app/fix-login"
        let tree = RepoTree.build([session("fix", fix, repoRoot: root, worktree: fix, branch: "fix/login"),
                                   session("main", root, repoRoot: root, worktree: root, branch: "main"),
                                   session("sub", fix + "/web", repoRoot: root, worktree: fix, branch: "fix/login"),
                                   session("notes", "/home/u/Code/notes", repoRoot: "/home/u/Code/notes",
                                           worktree: "/home/u/Code/notes", branch: "trunk")], machines: machines)
        #expect(tree.count == 1)
        let repos = tree[0].repos
        #expect(repos.map(\.name) == ["app", "notes"])
        #expect(repos[0].worktrees.map(\.name) == ["main", "fix/login"])
        #expect(repos[0].worktrees.map(\.main) == [true, false])
        #expect(Set(repos[0].worktrees[1].sessions.map(\.sessionID)) == ["fix", "sub"])
    }

    @Test func aNamedBranchKeepsItsLevelUnderALoneMainCheckout() {
        let named = RepoTree.build([session("main", "/code/app", repoRoot: "/code/app", worktree: "/code/app", branch: "main")],
                                   machines: machines)
        #expect(named[0].repos[0].flat == false)
        #expect(named[0].repos[0].worktrees.map(\.name) == ["main"])
        let older = RepoTree.build([session("main", "/code/app")], machines: machines)
        #expect(older[0].repos[0].flat, "an older node's lone main checkout stays flat")
    }

    @Test func projectsSortByNameInsideEachMachineInTheHubsOrder() {
        let tree = RepoTree.build([session("a", "/code/zeta", repoRoot: "/code/zeta", worktree: "/code/zeta", branch: "main"),
                                   session("b", "/code/alpha", machine: "machine-b", repoRoot: "/code/alpha", worktree: "/code/alpha", branch: "main"),
                                   session("c", "/code/beta", repoRoot: "/code/beta", worktree: "/code/beta", branch: "main")],
                                  machines: machines)
        #expect(tree.map(\.ref.id) == ["machine-b", "machine-a"])
        #expect(tree.map { $0.repos.map(\.name) } == [["alpha"], ["beta", "zeta"]])
    }

    @Test func theNodesPlaceWinsOverPathInference() {
        let s = session("w", "/home/u/orca/workspaces/app/fix/web", repoRoot: "/home/u/Code/app",
                        worktree: "/home/u/orca/workspaces/app/fix/", branch: "fix")
        #expect(RepoPlace.of(s) == RepoPlace(repo: "/home/u/Code/app", name: "app", worktree: "/home/u/orca/workspaces/app/fix", main: false))
        #expect(RepoPlace.git(session("old", "/code/app")) == nil)
    }

    private func workspace(_ id: String, parent: String? = nil, machine: String = "machine-a", group: String = "",
                           path: String? = nil) -> HostWorkspace {
        HostWorkspace(machineID: machine, source: "orca", sourceID: id, title: id, cwd: path ?? "/work/" + id,
                      parentSourceID: parent, index: 1, group: group)
    }

    @Test func catalogRetainsExistingEmptyAncestorsAndIndependentEmptyWorkspaces() throws {
        let catalog = [workspace("root"), workspace("child", parent: "root"), workspace("leaf", parent: "child"), workspace("empty")]
        let tree = RepoTree.build([orca("agent", "/work/leaf", space: "leaf")], machines: machines, workspaces: catalog)
        let repos = try #require(tree.first?.repos)
        #expect(repos.map(\.name) == ["empty", "root"])
        let root = try #require(repos.first { $0.name == "root" }?.worktrees.first)
        #expect(root.sessions.isEmpty)
        #expect(root.children[0].sessions.isEmpty)
        #expect(root.children[0].children[0].sessions.map(\.sessionID) == ["agent"])
        #expect(root.treeSessions.count == 1)
        #expect(!repos[0].flat, "an empty real workspace keeps its container row")
    }

    @Test func catalogSearchAndStateFiltersKeepOnlyMatchingAncestorPaths() {
        let catalog = [workspace("root"), workspace("middle", parent: "root"), workspace("leaf", parent: "middle"), workspace("unrelated")]
        let session = orca("agent", "/work/leaf", space: "leaf")
        let tree = RepoTree.build([session], machines: machines, workspaces: catalog, includeEmpty: false, search: "agent")
        #expect(tree[0].repos.map(\.name) == ["root"])
        #expect(tree[0].repos[0].worktrees[0].children[0].children[0].sessions.count == 1)
        #expect(RepoTree.build([], machines: machines, workspaces: catalog, includeEmpty: false).isEmpty)
        #expect(RepoTree.build([], machines: machines, workspaces: catalog, includeEmpty: false, search: "leaf").isEmpty)
        let byName = RepoTree.build([], machines: machines, workspaces: catalog, search: "leaf")
        #expect(byName[0].repos.map(\.name) == ["root"])
        #expect(byName[0].repos[0].worktrees[0].children[0].children[0].name == "leaf")
    }

    @Test func catalogIdentityIsNotTheDirectoryAndParentsNeverCrossMachineOrHub() {
        let catalog = [workspace("root", path: "/same"), workspace("other", path: "/same"),
                       workspace("child", parent: "root", machine: "machine-b"),
                       workspace("work-child", parent: "root", group: "work")]
        let tree = RepoTree.build([], machines: machines, workspaces: catalog)
        #expect(tree.flatMap(\.repos).count == 4)
        #expect(Set(tree.flatMap(\.repos).map(\.id)).count == 4)
        #expect(tree.flatMap(\.repos).flatMap(\.worktrees).allSatisfy { $0.children.isEmpty })
    }

    @Test func catalogCyclesSelfParentsAndMissingParentsStayVisible() {
        let catalog = [workspace("a", parent: "b"), workspace("b", parent: "a"), workspace("self", parent: "self"),
                       workspace("orphan", parent: "gone")]
        let repos = RepoTree.build([], machines: machines, workspaces: catalog).flatMap(\.repos)
        #expect(repos.count == 4)
        #expect(repos.flatMap(\.worktrees).allSatisfy { $0.children.isEmpty })
    }

    @Test func oldCatalogWithoutParentMetadataUsesSessionLineage() {
        var child = workspace("child")
        child.index = nil
        let tree = RepoTree.build([orca("c", "/work/child", space: "child", parent: "root")],
                                  machines: machines, workspaces: [workspace("root"), child])
        #expect(tree[0].repos.count == 1)
        #expect(tree[0].repos[0].worktrees[0].children[0].sessions.count == 1)
    }

    @Test func independentRootWorkspacesShareTheirOrcaProject() throws {
        var a = workspace("research", path: "/external/research")
        var b = workspace("review", path: "/another/review")
        var c = workspace("ready", path: "/code/app/.worktrees/ready")
        a.index = 3; b.index = 1; c.index = 2
        var catalog = [a, b, c]
        for i in catalog.indices {
            catalog[i].projectID = "project-app"
            catalog[i].projectName = "app"
            catalog[i].projectPath = "/code/app"
            catalog[i].isMain = false
        }
        catalog[1].projectName = nil; catalog[1].projectPath = nil // a retained older snapshot comes first
        var child = workspace("nested", parent: "research")
        child.projectID = "other-project"; child.projectName = "other"
        child.isMain = true
        catalog.append(child)
        let repos = RepoTree.build([orca("live", child.cwd, space: "nested")], machines: machines, workspaces: catalog)[0].repos
        #expect(repos.count == 1)
        let repo = try #require(repos.first)
        #expect(repo.name == "app" && repo.path == "/code/app")
        #expect(repo.worktrees.map(\.name) == ["review", "ready", "research"])
        #expect(repo.worktrees.allSatisfy { !$0.main })
        #expect(repo.worktrees[2].children.map(\.name) == ["nested"], "cross-project lineage still follows the chosen parent")
        #expect(repo.worktrees[2].children[0].main, "a nested project's original checkout keeps its Primary badge")
        #expect(repo.sessions.map(\.sessionID) == ["live"])
    }

    @Test func projectIdentityIsScopedAndDoesNotDependOnItsDisplayName() {
        var a = workspace("a"), b = workspace("b"), remote = workspace("remote", machine: "machine-b")
        var otherHub = workspace("other-hub", group: "other")
        a.projectID = "one"; b.projectID = "two"; remote.projectID = "one"; otherHub.projectID = "one"
        a.projectName = "app"; b.projectName = "app"; remote.projectName = "app"; otherHub.projectName = "app"
        let catalog = [a, b, remote, otherHub]
        let repos = RepoTree.build([], machines: machines, workspaces: catalog).flatMap(\.repos)
        #expect(repos.count == 4 && Set(repos.map(\.id)).count == 4)
        var renamed = catalog
        renamed[0].projectName = "renamed"
        #expect(Set(RepoTree.build([], machines: machines, workspaces: renamed).flatMap(\.repos).map(\.id)) == Set(repos.map(\.id)))
    }

    @Test func olderCatalogCanGroupTheDocumentedOrcaProjectAddresses() {
        let a = workspace("repo-id::/code/app/.worktrees/a", path: "/code/app/.worktrees/a")
        let b = workspace("repo-id::/code/app/.worktrees/b", path: "/code/app/.worktrees/b")
        let repos = RepoTree.build([], machines: machines, workspaces: [a, b])[0].repos
        #expect(repos.count == 1 && repos[0].name == "app")
        #expect(repos[0].worktrees.count == 2)
    }

    private func badge(_ workspace: HostWorkspace, parent: HostWorkspace?, inheritedName: String = "root") -> String? {
        RepoTree.Worktree(id: workspace.key, name: workspace.title, path: workspace.cwd, main: false, sessions: [],
                          catalog: true, workspace: workspace).repositoryBadge(parent: parent, inheritedName: inheritedName)
    }

    @Test func repositoryBadgesAppearOnlyAtDirectParentRepositoryChanges() {
        var chain = (0..<5).map { workspace("tree-\($0)") }
        for i in chain.indices {
            chain[i].projectID = ["root", "service", "service", "frontend", "frontend"][i]
            chain[i].projectName = chain[i].projectID
        }
        let labels = chain.indices.map { i in badge(chain[i], parent: i == 0 ? nil : chain[i - 1]) }
        #expect(labels == [nil, "service", nil, "frontend", nil])
        #expect(badge(chain[1], parent: chain[4]) == "service", "Switching back to an ancestor's repository is another boundary")
    }

    @Test func repositoryBadgesUseRepositoryIDsRatherThanDisplayNames() {
        var parent = workspace("parent"), child = workspace("child", parent: "parent")
        parent.projectID = "one"; child.projectID = "two"
        parent.projectName = "app"; child.projectName = "app"
        #expect(badge(child, parent: parent) == "app", "Different repositories can have the same display name")
        child.projectID = parent.projectID
        child.projectName = "renamed app"
        #expect(badge(child, parent: parent) == nil, "A newer name in a snapshot does not change repository identity")
    }

    @Test func olderRepositoryMetadataUsesPathsThenTheInheritedName() {
        var parent = workspace("parent"), child = workspace("child", parent: "parent")
        parent.projectPath = "/code/app/"; child.projectPath = "/code/app"
        parent.projectName = "app"; child.projectName = "app"
        #expect(badge(child, parent: parent) == nil)
        child.projectPath = "/other/app"
        #expect(badge(child, parent: parent) == "app")
        parent.projectPath = nil; child.projectPath = nil; parent.projectName = nil
        #expect(badge(child, parent: parent, inheritedName: "app") == nil)
        #expect(badge(child, parent: parent, inheritedName: "other") == "app")
        #expect(badge(child, parent: nil) == nil, "A grouped root already has its repository heading")
    }

}
