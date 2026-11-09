import Foundation
import Testing
@testable import Persona

struct WorkspaceNavigationTests {
    @Test func compactActivityAgesReserveRoomForTheModel() {
        let now = Date(timeIntervalSince1970: 1000000)
        #expect(WorkspaceList.age(now.addingTimeInterval(10), now: now) == "now")
        #expect(WorkspaceList.age(now.addingTimeInterval(-3599), now: now) == "59m")
        #expect(WorkspaceList.age(now.addingTimeInterval(-3600), now: now) == "1h")
        #expect(WorkspaceList.age(now.addingTimeInterval(-86400), now: now) == "1d")
    }
    private func workspace(_ id: String, parent: String? = nil, group: String = "") -> HostWorkspace {
        HostWorkspace(machineID: "m", source: "orca", sourceID: id, title: id, cwd: "/work/shared", parentSourceID: parent, group: group)
    }

    private func session(kind: String = "codex_app", space: String = "provider-project", group: String = "") throws -> Session {
        var s = try JSON.decoder.decode(Session.self, from: Data("""
        {"machine_id":"m","session_id":"old","title":"Prior work","cwd":"/work/shared","status":"offline","state":"offline",
         "space_kind":"\(kind)","space_id":"\(space)","capabilities":{"read":true,"send":false}}
        """.utf8))
        s.group = group
        return s
    }

    @Test func oldProviderHistoryIsVisibleOnlyInAnUnambiguousWorkspace() throws {
        let w = workspace("chosen"), s = try session()
        #expect(w.contains(s, catalog: [w]))
        #expect(!w.contains(s, catalog: [w, workspace("same-directory")]))
        #expect(!w.contains(s, catalog: []), "an incomplete catalog cannot assign provider history by directory")
        #expect(!workspace("chosen", group: "other").contains(s, catalog: [w]))
        #expect(!w.contains(try session(kind: "herdr"), catalog: [w]))
    }

    @Test func explicitWorkspaceIdentityWinsOverItsDirectory() throws {
        let a = workspace("a"), b = workspace("b")
        let s = try session(kind: "orca", space: "b")
        #expect(!a.contains(s, catalog: [a, b]))
        #expect(b.contains(s, catalog: [a, b]))
        #expect(b.preset.workspaceID == "b")
        #expect(b.preset.machineID == "m")
        #expect(b.preset.host == "orca")
    }

    @MainActor @Test func pinnedAgentsRemainWorktreeDestinationsWhenTheirTreeRowsAreEmpty() throws {
        let root = workspace("root"), child = workspace("child", parent: root.sourceID)
        var first = try session(kind: "orca", space: root.sourceID), second = first
        first.sessionID = "first"; second.sessionID = "second"
        var elsewhere = try session(kind: "orca", space: child.sourceID)
        elsewhere.sessionID = "child-agent"
        var otherHub = first
        otherHub.group = "another-hub"
        var settings = AgentListProjection.Settings()
        var pins = Pins()
        pins.sessions = ["m/first", "m/second"]
        settings.preferences[""] = .init(pins: pins, mutes: Pins())
        let machine = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"m","name":"Fixture","kind":"local","online":true}"#.utf8))
        let projection = AgentListProjection.build(.init(sessions: [first, second], machines: [machine], settings: settings, workspaces: [root]))
        #expect(projection.pinnedAgents.count == 2)
        #expect(projection.tree.first?.repos.first?.worktrees.first?.sessions.isEmpty == true)
        let direct = WorkspaceNavigation.directSessions(root, inventory: [first, elsewhere, otherHub, second], catalog: [root, child])
        #expect(direct.map(\.key) == [first.key, second.key])
        let destination = WorkspaceNavigation.firstAgent(rendered: [], workspace: root, inventory: [first, elsewhere, otherHub, second],
                                                         catalog: [root, child], ordered: { $0.reversed() })
        #expect(destination == second.key, "Pinning removes tree rows, not the worktree's direct Agent destination")
        #expect(WorkspaceNavigation.firstAgent(rendered: [first], workspace: root, inventory: [first, second],
                                                catalog: [root, child], ordered: { $0.reversed() }) == first.key,
                "A displayed Agent retains the same title-click order as its rendered row")
        #expect(WorkspaceNavigation.firstAgent(rendered: [], workspace: child, inventory: [first, second],
                                                catalog: [root, child], ordered: { $0 }) == nil,
                "An empty worktree never opens its parent's Agent")
    }

    @Test func parentSelectionIsIndependentOfTheCodeSource() {
        #expect(WorktreeForm.body(workspaceID: "code", kind: "orca", parentWorkspaceID: "folder") ==
                ["workspace_id": "code", "kind": "orca", "parent_workspace_id": "folder"])
        let root = workspace("root"), child = workspace("child", parent: "root")
        #expect(child.breadcrumb(in: [root, child]) == "root › child")
        #expect(child.breadcrumb(in: [workspace("root", group: "other"), child]) == "child")
    }

    @Test func creationUsesTheAcknowledgedParentAcrossMixedHubAndNodeVersions() throws {
        let legacy = try JSON.decoder.decode(WorktreeCreated.self, from: Data(#"{"workspace_id":"new","path":"/new","branch":"b"}"#.utf8))
        #expect(legacy.resolvedParent(sourceID: "code") == "code")
        let acknowledged = try JSON.decoder.decode(WorktreeCreated.self, from: Data(#"{"workspace_id":"new","path":"/new","branch":"b","parent_workspace_id":"folder"}"#.utf8))
        #expect(acknowledged.resolvedParent(sourceID: "code") == "folder")
    }

    @MainActor @Test func childCreationFromAnEmptyNestedWorkspaceKeepsItsExactTarget() throws {
        let parent = workspace("nested-folder", parent: "root", group: "remote-hub")
        let group = parent.creationGroup
        #expect(group.machineRef == parent.machineRef)
        #expect(group.spaceID == parent.sourceID && group.sessions.isEmpty)
        let made = try JSON.decoder.decode(WorktreeCreated.self, from: Data(#"{"workspace_id":"new","path":"/work/new","branch":"test/child","parent_workspace_id":"nested-folder"}"#.utf8))
        let child = parent.createdWorkspace(made)
        #expect(child.machineRef == parent.machineRef && child.source == parent.source)
        #expect(child.parentKey == parent.key && child.sourceID == "new")
        #expect(child.branch == "test/child" && child.cwd == "/work/new")
    }

    @Test func childDestinationUsesTheServersParentWhenTheUserChangesIt() throws {
        let origin = workspace("clicked")
        let made = try JSON.decoder.decode(WorktreeCreated.self, from: Data(#"{"workspace_id":"new","path":"/work/new","branch":"test/child","parent_workspace_id":"chosen-elsewhere"}"#.utf8))
        #expect(origin.createdWorkspace(made).parentSourceID == "chosen-elsewhere")
    }

    @Test func breadcrumbsIncludeTheRootProjectRatherThanAChildsGitProject() throws {
        var root = workspace("research"), child = workspace("nested", parent: "research")
        root.projectName = "app"; child.projectName = "other"
        #expect(child.breadcrumb(in: [root, child]) == "app › research › nested")
        root.projectName = "research"
        #expect(child.breadcrumb(in: [root, child]) == "research › nested")
        let decoded = try JSON.decoder.decode(HostWorkspace.self, from: Data(#"{"machine_id":"m","source":"orca","source_id":"w","title":"Review","cwd":"/external/tree","branch":"feature/review","project_id":"p","project_name":"app","project_path":"/code/app","is_main":false}"#.utf8))
        #expect(decoded.projectID == "p" && decoded.projectName == "app" && decoded.projectPath == "/code/app" && decoded.isMain == false)
        #expect(decoded.branch == "feature/review")
    }
}
