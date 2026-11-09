import AppKit
import SwiftUI
import Testing
@testable import Persona

@MainActor struct MacBoundedContextMenuTests {
    private func mouse(_ type: NSEvent.EventType, flags: NSEvent.ModifierFlags = [], at point: NSPoint = .zero,
                       window: NSWindow? = nil) throws -> NSEvent {
        try #require(NSEvent.mouseEvent(with: type, location: point, modifierFlags: flags, timestamp: 0,
                                        windowNumber: window?.windowNumber ?? 0, context: nil, eventNumber: 0, clickCount: 1, pressure: 1))
    }

    @Test func onlySecondaryClicksAreIntercepted() throws {
        #expect(MacContextMenuCatcher.catches(try mouse(.rightMouseDown)))
        #expect(MacContextMenuCatcher.catches(try mouse(.rightMouseUp)))
        #expect(MacContextMenuCatcher.catches(try mouse(.leftMouseDown, flags: .control)))
        #expect(!MacContextMenuCatcher.catches(try mouse(.leftMouseDown)))
        #expect(!MacContextMenuCatcher.catches(try mouse(.leftMouseDown, flags: .option)))
        #expect(!MacContextMenuCatcher.catches(nil))
    }

    @Test func theMenuIsBoundedAndLeavesOrdinaryClicksAlone() throws {
        let view = MacContextMenuCatcher()
        view.frame = NSRect(x: 0, y: 0, width: 100, height: 30)
        view.currentEvent = { try? self.mouse(.rightMouseDown) }
        #expect(view.hitTest(NSPoint(x: 50, y: 15)) === view)
        #expect(view.hitTest(NSPoint(x: 50, y: 40)) == nil)
        view.currentEvent = { try? self.mouse(.leftMouseDown) }
        #expect(view.hitTest(NSPoint(x: 50, y: 15)) == nil)
    }

    @Test func nestedListControlsReceiveTheirOwnMenus() throws {
        let root = List {
            VStack(alignment: .leading) {
                Button("Workspace") {}.buttonStyle(.plain).frame(width: 180, height: 30)
                    .macBoundedContextMenu { Button("Create Child") {} }
                Button("Agent") {}.buttonStyle(.plain).frame(width: 180, height: 30)
                    .macBoundedContextMenu { Button("Agent Actions") {} }
                Button("Child Workspace") {}.buttonStyle(.plain).frame(width: 160, height: 30).padding(.leading, 20)
                    .macBoundedContextMenu { Button("Create Grandchild") {} }
            }
        }.frame(width: 260, height: 240)
        let host = NSHostingView(rootView: root)
        host.frame = NSRect(x: 0, y: 0, width: 260, height: 240)
        let window = NSWindow(contentRect: host.frame, styleMask: [.borderless], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = host
        window.orderFrontRegardless()
        defer { window.close() }
        host.layoutSubtreeIfNeeded()
        RunLoop.main.run(until: Date().addingTimeInterval(0.05))
        func find(_ view: NSView) -> [MacContextMenuCatcher] {
            (view as? MacContextMenuCatcher).map { [$0] } ?? view.subviews.flatMap(find)
        }
        let controls = find(host).sorted { $0.convert(.zero, to: nil).y > $1.convert(.zero, to: nil).y }
        #expect(controls.count == 3)
        var dispatching: NSEvent?
        var chosen: [Int] = []
        for (index, view) in controls.enumerated() {
            view.currentEvent = { dispatching }
            view.makeMenu = { NSMenu(title: String(index)) }
            view.presentMenu = { menu, _, _ in if let value = Int(menu.title) { chosen.append(value) } }
        }
        for view in controls {
            let point = view.convert(NSPoint(x: view.bounds.midX, y: view.bounds.midY), to: nil)
            dispatching = try mouse(.rightMouseDown, at: point, window: window)
            window.sendEvent(try #require(dispatching))
        }
        dispatching = nil
        #expect(chosen == [0, 1, 2], "A child or Agent menu must not become the enclosing List cell's menu")
    }
}
