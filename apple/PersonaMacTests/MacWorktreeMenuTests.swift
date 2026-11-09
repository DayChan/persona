import AppKit
import SwiftUI
import Testing
@testable import Persona

@MainActor struct MacWorktreeMenuTests {
    final class Actions {
        var parents: [String] = []
        var renamed: [String] = []
    }

    private func session(_ id: String) throws -> Session {
        let data = Data("""
        {"machine_id":"m","session_id":"\(id)","title":"Agent \(id)","cwd":"/code/b","status":"idle","state":"idle","agent":"codex"}
        """.utf8)
        return try JSON.decoder.decode(Session.self, from: data)
    }

    private func agentMenu(_ session: Session) -> AgentRowMenu {
        AgentRowMenu(key: session.key, pinned: false, muted: false, looping: false, live: true,
                     terminal: true, deletable: true, actions: AgentRowMenu.Actions())
    }

    private func items(_ menu: NSMenu) -> [NSMenuItem] {
        menu.update()
        return menu.items.flatMap { [$0] + ($0.submenu.map(items) ?? []) }
    }

    @Test func transcriptAndStatusUpdatesKeepMenuIdentityWhileCommandsStayCurrent() throws {
        let workspace = HostWorkspace(machineID: "m", source: "orca", sourceID: "b", title: "Child", cwd: "/code/b")
        let session = try session("one")
        let menu = agentMenu(session)
        func value(_ workspace: HostWorkspace, _ session: Session, _ menu: AgentRowMenu) -> WorktreeSidebarMenu {
            WorktreeSidebarMenu(workspace: workspace, parent: nil, sessions: [session], agent: session, menus: [menu],
                                open: { _ in }, newSession: { _ in }, newChild: { _ in })
        }
        let initial = value(workspace, session, menu)
        var updated = workspace
        updated.unread = true
        updated.sidebarStatus = "working"
        var reply = session
        reply.title = session.title
        reply.updatedAt = (session.updatedAt ?? Date(timeIntervalSince1970: 0)).addingTimeInterval(1)
        #expect(initial == value(updated, reply, menu))
        updated.available = false
        #expect(initial != value(updated, reply, menu))
        updated.available = true
        reply.title = "Renamed Agent"
        #expect(initial != value(updated, reply, menu))
    }

    @Test func titleBranchAndAgentMenusCreateUnderTheSameNestedWorktree() throws {
        let a = HostWorkspace(machineID: "m", source: "orca", sourceID: "a", title: "Root", cwd: "/code/a", branch: "main")
        let b = HostWorkspace(machineID: "m", source: "orca", sourceID: "b", title: "Child", cwd: "/code/b", branch: "feature/b", parentSourceID: "a")
        let sessions = try [session("one"), session("two")]
        let child = RepoTree.Worktree(id: "b", name: b.title, path: b.cwd, main: false, sessions: sessions, catalog: true, workspace: b)
        let root = RepoTree.Worktree(id: "a", name: a.title, path: a.cwd, main: true, sessions: [], children: [child], catalog: true, workspace: a)
        let actions = Actions()
        let model = AppModel()
        let card = WorkspaceSidebarCard(workspace: root, project: "app", selectedSession: nil, selectedWorkspace: nil,
                                        folded: { _ in false }, toggle: { _ in }, open: { _ in }, ordered: { $0 }) { session in
            Text(session.title).frame(maxWidth: .infinity, minHeight: 30, alignment: .leading)
        } actions: { tree, agent in
            if let workspace = tree.workspace {
                WorktreeSidebarMenu(workspace: workspace, parent: workspace.parentKey == a.key ? a : nil,
                                    sessions: tree.sessions, agent: agent, menus: tree.sessions.map(agentMenu),
                                    open: { _ in }, newSession: { _ in }, newChild: { actions.parents.append($0.key) },
                                    rename: { actions.renamed.append($0.key) }).equatable()
            }
        }
        let host = NSHostingView(rootView: List { card.selectionDisabled() }.padding(.vertical, 60).environment(model))
        host.frame = NSRect(x: 0, y: 0, width: 600, height: 650)
        let window = NSWindow(contentRect: host.frame, styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = host
        window.orderFrontRegardless()
        defer { window.close() }
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.1))
        func find(_ view: NSView) -> [MacContextMenuCatcher] {
            (view as? MacContextMenuCatcher).map { [$0] } ?? view.subviews.flatMap(find)
        }
        let controls = find(host).sorted { $0.convert(.zero, to: nil).y > $1.convert(.zero, to: nil).y }
        #expect(controls.count == 4)
        var event: NSEvent?
        controls.forEach { $0.currentEvent = { event } }
        // Both parts of the child's identity and both actual Agent controls use the production menu factory.
        for (index, fraction) in [(1, 0.8), (1, 0.2), (2, 0.5), (3, 0.5)] {
            let control = try #require(controls.indices.contains(index) ? controls[index] : nil)
            var menu: NSMenu?
            control.presentMenu = { value, _, _ in menu = value }
            let point = control.convert(NSPoint(x: control.bounds.midX, y: control.bounds.height * fraction), to: nil)
            event = try #require(NSEvent.mouseEvent(with: .rightMouseDown, location: point, modifierFlags: [], timestamp: 0,
                                                   windowNumber: window.windowNumber, context: nil, eventNumber: 0,
                                                   clickCount: 1, pressure: 1))
            window.sendEvent(try #require(event))
            event = nil
            let shown = try #require(menu)
            let contents = items(shown)
            let createTitle = String(localized: "workspace.newChild", defaultValue: "New Child Worktree…")
            #expect(contents.filter { $0.title == createTitle }.count == 1)
            let renameTitle = String(localized: "worktree.rename", defaultValue: "Rename Worktree…")
            #expect(shown.items.filter { $0.title == renameTitle }.count == 1)
            #expect(contents.contains { $0.title == String(localized: "agent.rename", defaultValue: "Rename Agent…") })
            #expect(contents.contains { $0.title == String(localized: "worktree.copyName", defaultValue: "Copy Worktree Name") })
            #expect(contents.contains { $0.title == String(localized: "worktree.openParent", defaultValue: "Open Parent Worktree") })
            #expect(contents.contains { $0.title == String(localized: "Copy Resume Command") })
            if index == 1 {
                #expect(contents.contains { $0.title == sessions[0].title && $0.submenu != nil })
                #expect(contents.contains { $0.title == sessions[1].title && $0.submenu != nil })
                for session in sessions {
                    let item = try #require(shown.items.first { $0.title == session.title && $0.submenu != nil })
                    let image = try #require(item.image)
                    #expect(image.size.width <= Theme.Size.markS && image.size.height <= Theme.Size.markS,
                            "Native menu icons must not use the brand asset's full intrinsic size")
                    #expect(item.view == nil, "Agent submenus use standard native menu rows")
                }
            } else {
                #expect(shown.items.contains { $0.title == String(localized: "Pin [Personal Agent List View 173f88]") })
            }
            let create = try #require(shown.items.firstIndex { $0.title == createTitle })
            #expect(shown.items[create].isEnabled)
            shown.performActionForItem(at: create)
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
            #expect(actions.parents.last == b.key, "Creating from an Agent must use its owning child, never the root or Agent ID")
            shown.performActionForItem(at: try #require(shown.items.firstIndex { $0.title == renameTitle }))
            RunLoop.main.run(until: Date().addingTimeInterval(0.02))
            #expect(actions.renamed.last == b.key, "A worktree rename never targets its first Agent")
        }
        #expect(actions.parents == Array(repeating: b.key, count: 4))
        #expect(actions.renamed == Array(repeating: b.key, count: 4))
    }
}
