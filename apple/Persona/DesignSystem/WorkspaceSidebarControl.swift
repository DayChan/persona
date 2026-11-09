import SwiftUI

/// A sidebar control opens its destination on a normal click and keeps secondary actions in its own bounds.
struct WorkspaceSidebarControl<Label: View, Actions: View>: View {
    var open: () -> Void
    @ViewBuilder var label: Label
    @ViewBuilder var actions: Actions

    var body: some View {
        #if os(macOS)
        Button(action: open) { label }
            .buttonStyle(.plain)
            .macBoundedContextMenu { actions }
        #else
        Menu { actions } label: { label } primaryAction: { open() }
            .menuStyle(.button).buttonStyle(.plain).menuIndicator(.hidden)
        #endif
    }
}
