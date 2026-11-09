import AppKit
import Observation
import SwiftUI
import Testing
@testable import Persona

@MainActor struct MacWorkspaceSwitchingTests {
    @MainActor @Observable final class Route {
        var path = NavigationPath()
        var workspace: String?
        var displayed = ""
        let agent = SessionKey(machineID: "m", sessionID: "agent")
        func openAgent() { open(agent) }
        func open(_ key: SessionKey) { workspace = nil; path = NavigationPath([key]) }
        func selectEmpty(_ target: HostWorkspace) { workspace = target.key; path = NavigationPath() }
    }

    struct Destinations: ViewModifier {
        var route: Route
        func body(content: Content) -> some View {
            content
                .navigationDestination(for: SessionKey.self) { key in
                    Text(key.sessionID).onAppear { route.displayed = key.sessionID }.id(key)
                }
        }
    }

    struct Fixture: View {
        @Bindable var route: Route
        var tree: RepoTree.Worktree
        var body: some View {
            SplitNavigation(path: $route.path, destinations: Destinations(route: route)) {
                List {
                    WorkspaceSidebarControl(open: route.openAgent) {
                        Text("Agent").frame(maxWidth: .infinity, minHeight: 30).contentShape(.rect)
                    } actions: { Button("Agent Actions") {} }
                    .selectionDisabled()
                    WorkspaceSidebarCard(workspace: tree, project: "app", selectedSession: nil,
                                         selectedWorkspace: route.workspace, folded: { _ in false }, toggle: { _ in },
                                         open: route.selectEmpty, selectAgent: route.open, ordered: { $0.reversed() }) { session in
                        Text(session.title).frame(maxWidth: .infinity, minHeight: 30, alignment: .leading)
                    } actions: { _, _ in Button("Create Child") {} }
                        .selectionDisabled()
                }
                .padding(.vertical, 60)
            } empty: { Text("Choose Workspace") }
            .environment(\.horizontalSizeClass, .regular)
            .transaction { $0.disablesAnimations = true }
        }
    }

    @Test func titleAndBranchClicksReplaceAnOpenAgentAndEachOther() throws {
        func session(_ id: String, cwd: String) throws -> Session {
            try JSON.decoder.decode(Session.self, from: Data("""
            {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"\(cwd)","status":"idle","state":"idle"}
            """.utf8))
        }
        let a = HostWorkspace(machineID: "m", source: "orca", sourceID: "a", title: "Workspace A", cwd: "/code/a", branch: "feature/a")
        let b = HostWorkspace(machineID: "m", source: "orca", sourceID: "b", title: "Workspace B", cwd: "/code/b", branch: "feature/b", parentSourceID: "a")
        let c = HostWorkspace(machineID: "m", source: "orca", sourceID: "c", title: "Empty Worktree", cwd: "/code/c", parentSourceID: "a")
        let child = RepoTree.Worktree(id: "b", name: b.title, path: b.cwd, main: false,
                                     sessions: [try session("b-first", cwd: b.cwd)], catalog: true, workspace: b)
        let empty = RepoTree.Worktree(id: "c", name: c.title, path: c.cwd, main: false, sessions: [], catalog: true, workspace: c)
        let tree = RepoTree.Worktree(id: "a", name: a.title, path: a.cwd, main: false,
                                    sessions: [try session("a-first", cwd: a.cwd), try session("a-second", cwd: a.cwd)],
                                    children: [child, empty], catalog: true, workspace: a)
        let route = Route()
        let host = NSHostingView(rootView: Fixture(route: route, tree: tree))
        host.frame = NSRect(x: 0, y: 0, width: 1200, height: 900)
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
        var dispatching: NSEvent?
        func click(_ index: Int, fraction: CGFloat, shows expected: String) throws {
            RunLoop.main.run(until: Date().addingTimeInterval(0.05))
            host.layoutSubtreeIfNeeded()
            let controls = find(host).sorted { $0.convert(.zero, to: nil).y > $1.convert(.zero, to: nil).y }
            #expect(controls.count == 7)
            let view = try #require(controls.indices.contains(index) ? controls[index] : nil)
            view.scrollToVisible(view.bounds)
            RunLoop.main.run(until: Date().addingTimeInterval(0.05))
            host.layoutSubtreeIfNeeded()
            controls.forEach { $0.currentEvent = { dispatching } }
            let point = view.convert(NSPoint(x: view.bounds.midX, y: view.bounds.height * fraction), to: nil)
            #expect(host.bounds.contains(host.convert(point, from: nil)))
            for type in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
                dispatching = try #require(NSEvent.mouseEvent(with: type, location: point, modifierFlags: [], timestamp: 0,
                                                              windowNumber: window.windowNumber, context: nil, eventNumber: 0,
                                                              clickCount: 1, pressure: 1))
                window.sendEvent(try #require(dispatching))
            }
            dispatching = nil
            let deadline = Date().addingTimeInterval(1)
            func matches() -> Bool {
                expected == "empty" ? route.path.isEmpty && route.workspace == c.key : route.displayed == expected
            }
            repeat { RunLoop.main.run(until: Date().addingTimeInterval(0.02)) } while !matches() && Date() < deadline
            #expect(matches(), "Worktree identity opens its first displayed Agent; empty worktrees show the normal empty pane")
            var menuTarget: Int?
            for (target, control) in controls.enumerated() {
                control.makeMenu = { NSMenu() }
                control.presentMenu = { _, _, _ in menuTarget = target }
            }
            dispatching = try #require(NSEvent.mouseEvent(with: .rightMouseDown, location: point, modifierFlags: [], timestamp: 0,
                                                         windowNumber: window.windowNumber, context: nil, eventNumber: 0,
                                                         clickCount: 1, pressure: 1))
            window.sendEvent(try #require(dispatching))
            dispatching = nil
            #expect(menuTarget == index, "Title and branch must present the same workspace's menu")
        }
        try click(0, fraction: 0.5, shows: "agent")
        try click(1, fraction: 0.8, shows: "a-second")
        try click(4, fraction: 0.2, shows: "b-first")
        try click(3, fraction: 0.5, shows: "a-first")
        try click(0, fraction: 0.5, shows: "agent")
        #expect(route.workspace == nil)
        try click(1, fraction: 0.2, shows: "a-second")
        try click(4, fraction: 0.8, shows: "b-first")
        try click(6, fraction: 0.5, shows: "empty")
        #expect(route.workspace == c.key && route.path.isEmpty, "Empty worktrees stay selected without a new detail page")
    }
}
