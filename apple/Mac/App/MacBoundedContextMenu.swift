import AppKit
import SwiftUI

extension View {
    /// A secondary-click menu whose scope is this control, not the containing SwiftUI List cell.
    func macBoundedContextMenu<Items: View>(@ViewBuilder items: () -> Items) -> some View {
        overlay(MacBoundedContextMenu(items: items()))
    }
}

struct MacBoundedContextMenu<Items: View>: NSViewRepresentable {
    var items: Items

    func makeNSView(context: Context) -> MacContextMenuCatcher { MacContextMenuCatcher() }
    func updateNSView(_ view: MacContextMenuCatcher, context: Context) {
        let environment = context.environment
        let items = items
        view.makeMenu = { NSHostingMenu(rootView: items.environment(\.self, environment)) }
    }
}

final class MacContextMenuCatcher: NSView {
    nonisolated static func catches(_ event: NSEvent?) -> Bool {
        guard let event else { return false }
        if [.rightMouseDown, .rightMouseDragged, .rightMouseUp].contains(event.type) { return true }
        return [.leftMouseDown, .leftMouseDragged, .leftMouseUp].contains(event.type) && event.modifierFlags.contains(.control)
    }

    var makeMenu: () -> NSMenu = { NSMenu() }
    var currentEvent: () -> NSEvent? = { NSApp.currentEvent }
    var presentMenu: (NSMenu, NSEvent, NSView) -> Void = { NSMenu.popUpContextMenu($0, with: $1, for: $2) }

    init() {
        super.init(frame: .zero)
        setAccessibilityElement(false)
    }
    required init?(coder: NSCoder) { nil }

    override func hitTest(_ point: NSPoint) -> NSView? {
        Self.catches(currentEvent()) ? super.hitTest(point) : nil
    }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
    override func rightMouseDown(with event: NSEvent) { presentMenu(makeMenu(), event, self) }
    override func mouseDown(with event: NSEvent) {
        if Self.catches(event) { presentMenu(makeMenu(), event, self) }
        else { super.mouseDown(with: event) }
    }
    override func rightMouseDragged(with event: NSEvent) {}
    override func rightMouseUp(with event: NSEvent) {}
    override func mouseDragged(with event: NSEvent) {}
    override func mouseUp(with event: NSEvent) {}
}
