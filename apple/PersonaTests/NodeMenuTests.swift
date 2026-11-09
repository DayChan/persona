import Foundation
import Testing
@testable import Persona

/// The Agents list's node menu (`NodeMenu`, docs/integrations/terminal.md「节点菜单」): what it offers and what it sends.
@MainActor struct NodeMenuTests {
    private func session(_ id: String, pane: String? = "w1:p1", state: String = "working", workspace: String? = "w1",
                         space: String = "w1", kind: String? = "herdr", extra: String = "") -> Session {
        let paneField = pane.map { #","pane_id":"\#($0)""# } ?? ""
        let wsField = workspace.map { #","workspace_id":"\#($0)""# } ?? ""
        let kindField = kind.map { #","space_kind":"\#($0)""# } ?? ""
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"/tmp/p","status":"idle","state":"\(state)",
         "space_id":"\(space)","space_name":"app","tab_id":"w1:t1"\(paneField)\(wsField)\(kindField)\(extra)}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    private func group(_ sessions: [Session], space: String = "w1") -> SpaceGroup {
        SpaceGroup(id: "m/\(space)", machineID: "m", spaceID: space, name: "app", index: 0, sessions: sessions)
    }

    @Test func paneItemsOnlyForALiveHerdrPane() {
        #expect(NodeMenu.offersPaneActions(session("a")))
        #expect(!NodeMenu.offersPaneActions(session("a", pane: nil)), "no pane: nothing to split")
        #expect(!NodeMenu.offersPaneActions(session("a", pane: "orca:x")), "Orca does not split or close")
        #expect(!NodeMenu.offersPaneActions(session("a", pane: "botmux:x")))
        #expect(!NodeMenu.offersPaneActions(session("a", state: "offline")))
        #expect(!NodeMenu.offersPaneActions(session("a", extra: #","source_kind":"runtime""#)), "a runtime's session has no terminal")
    }

    @Test func spaceItemsOnlyForAHerdrWorkspace() {
        #expect(NodeMenu.offersSpaceActions(group([session("a")])))
        #expect(!NodeMenu.offersSpaceActions(group([session("a", workspace: nil, space: "-")], space: "-")))
        #expect(!NodeMenu.offersSpaceActions(group([session("a", pane: "orca:x")])))
        #expect(NodeMenu.workspaceID(group([session("a", workspace: nil)])) == "w1", "falls back to the space id")
    }

    @Test func gitNamesThePaneOrTheWorkspaceAndNothingElse() {
        #expect(NodeMenu.gitBody(.pull, pane: "w1:p1") == ["op": "pull", "pane": "w1:p1"])
        #expect(NodeMenu.gitBody(.push, workspaceID: "w1", kind: "herdr") == ["op": "push", "workspace_id": "w1", "kind": "herdr"])
        #expect(NodeMenu.gitBody(.fetch, workspaceID: "w1", kind: nil) == ["op": "fetch", "workspace_id": "w1"])
        #expect(NodeMenu.GitOp.pull.done("已经是最新") == "拉取：已经是最新")
        #expect(NodeMenu.GitOp.fetch.done(nil) == "获取：完成")
        #expect(NodeMenu.GitOp.allCases.map(\.rawValue) == ["pull", "push", "fetch"])
    }

    @Test func splitAndCloseBodies() {
        #expect(NodeMenu.splitBody(pane: "w1:p1", .right) == ["pane": "w1:p1", "direction": "right"])
        #expect(NodeMenu.splitBody(pane: "w1:p1", .down) == ["pane": "w1:p1", "direction": "down"])
        #expect(NodeMenu.closeTabBody(tabID: "w1:t1", kind: "herdr") == ["tab_id": "w1:t1", "kind": "herdr"])
        #expect(NodeMenu.closeTabBody(tabID: "w1:t1", kind: "") == ["tab_id": "w1:t1"])
    }

    @Test func closeTabAsksFirst() {
        let actions = NodeActions()
        actions.askCloseTab(session("a", extra: #","slot_name":"2""#))
        #expect(actions.closingTab?.tabID == "w1:t1")
        #expect(actions.closingTab?.name == "#2", "a numbered tab reads as in the chat's header")
    }

    /// Push sends the branch off the computer: it waits for a yes, naming where it runs (#1883). Pull and Fetch don't ask.
    @Test func pushAsksFirst() {
        let actions = NodeActions()
        actions.git(.push, session: session("a"))
        #expect(actions.askingPush?.body == ["op": "push", "pane": "w1:p1"])
        #expect(actions.askingPush?.place == "p", "the pane's folder")
        actions.askingPush = nil
        actions.git(.push, space: group([session("a")]))
        #expect(actions.askingPush?.body == ["op": "push", "workspace_id": "w1", "kind": "herdr"])
        #expect(actions.askingPush?.place == "app")
        actions.askingPush = nil
        actions.git(.pull, session: session("a"))
        actions.git(.fetch, space: group([session("a")]))
        #expect(actions.askingPush == nil)
    }

    @Test func shellsAreTheWorkspacesPanesWithoutAnAgent() throws {
        let json = """
        {"hosts":[{"kind":"herdr","stream":true,"new_tab":true,"worktree":true,
          "workspaces":[{"id":"w1","label":"app"}],
          "tabs":[{"id":"w1:t1","workspace_id":"w1","label":"dev"},{"id":"w2:t1","workspace_id":"w2","label":"x"}],
          "panes":[{"pane":"w1:p1","terminal":"pane:w1:p1","tab_id":"w1:t1","workspace_id":"w1","agent":"claude","session_id":"s1"},
                   {"pane":"w1:p2","terminal":"pane:w1:p2","tab_id":"w1:t1","workspace_id":"w1","cwd":"/src/app"},
                   {"pane":"w1:p3","terminal":"pane:w1:p3","tab_id":"w1:t1","workspace_id":"w1","session_id":"s2"},
                   {"pane":"w2:p1","terminal":"pane:w2:p1","tab_id":"w2:t1","workspace_id":"w2"}]},
         {"kind":"orca","workspaces":[],"tabs":[],"panes":[{"pane":"orca:1","tab_id":"t","workspace_id":"w1"}]}]}
        """
        let panes = try JSON.decoder.decode(TerminalPanes.self, from: Data(json.utf8))
        let shells = NodeMenu.shells(in: panes, workspaceID: "w1")
        #expect(shells.map(\.pane) == ["w1:p2"])
        #expect(NodeMenu.shellTitle(shells[0], in: panes) == "dev · app")
        #expect(NodeMenu.herdrHost(panes)?.newTab == true)
        #expect(NodeMenu.herdrHost(panes)?.worktree == true)
    }

    @Test func anOldNodesLayoutDecodes() throws {
        let panes = try JSON.decoder.decode(TerminalPanes.self, from: Data(#"{"hosts":[{"kind":"herdr"}]}"#.utf8))
        #expect(NodeMenu.herdrHost(panes)?.worktree == false)
        #expect(NodeMenu.shells(in: panes, workspaceID: "w1").isEmpty)
    }

    @Test func onlyClaudeLaunchersRunInAShell() {
        #expect(NodeMenu.launchableHere(Launcher(name: "claude")))
        #expect(NodeMenu.launchableHere(Launcher(name: "c2", agent: "claude")))
        #expect(!NodeMenu.launchableHere(Launcher(name: "codex", agent: "codex")))
    }

    @Test func rowMenuIsNotEqualWhenItsPaneItemsChange() {
        let actions = AgentRowMenu.Actions()
        let key = session("a").key
        let a = AgentRowMenu(key: key, pinned: false, muted: false, looping: false, live: true, actions: actions)
        let b = AgentRowMenu(key: key, pinned: false, muted: false, looping: false, live: true, paneActions: true, actions: actions)
        #expect(a != b)
    }
}
