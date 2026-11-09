import SwiftUI

/// One of a row's extra actions (an extension row's `actions`, docs/extensions.md「UI version 6」): its words, its icon,
/// whether it deletes or undoes something (red, last, asked first), whether a swipe shows it too.
struct RowActionItem: Identifiable {
    var id: String
    var title: String
    var symbol: String?
    var destructive = false
    var swipe = false
    /// Asked first (a confirmation dialog), with this question.
    var confirm: String?
    var run: () -> Void
}

extension View {
    /// A row's extra actions, the platform's way: a long press's menu and (in a list) a trailing swipe for the
    /// `swipe` ones on iPhone; a right click's menu and a … button at the row's end (shown on hover, always there for
    /// the keyboard and VoiceOver) on the Mac. Destructive ones are red and last; a full swipe never runs one (a tap
    /// does); one with `confirm` asks first. VoiceOver also has them as the row's actions.
    func rowActions(_ actions: [RowActionItem]) -> some View { modifier(RowActionsModifier(actions: actions)) }
}

private struct RowActionsModifier: ViewModifier {
    var actions: [RowActionItem]
    @State private var asking: RowActionItem?
    @State private var hovering = false

    private var ordered: [RowActionItem] { actions.filter { !$0.destructive } + actions.filter(\.destructive) }

    func body(content: Content) -> some View {
        if actions.isEmpty {
            content
        } else {
            decorated(content)
                .contextMenu { menu }
                .accessibilityActions {
                    ForEach(ordered) { a in Button(a.title) { start(a) } }
                }
                .confirmationDialog(asking?.confirm ?? "", isPresented: Binding(get: { asking != nil }, set: { if !$0 { asking = nil } }),
                                    titleVisibility: .visible) {
                    if let a = asking {
                        Button(role: a.destructive ? .destructive : nil) { a.run() } label: { Text(a.title).lineLimit(1) }
                            .accessibilityIdentifier("plugin-ui-confirm")
                    }
                    Button(String(localized: "取消"), role: .cancel) {}
                }
        }
    }

    @ViewBuilder private func decorated(_ content: Content) -> some View {
        #if os(macOS)
        HStack(spacing: Theme.Space.s) {
            content
            Menu { menu } label: { Image(systemName: Theme.Icon.more) }
                .menuStyle(.borderlessButton)
                .menuIndicator(.hidden)
                .fixedSize()
                .opacity(hovering ? 1 : 0.001)
                .help(String(localized: "更多操作"))
                .accessibilityLabel(String(localized: "更多操作"))
                .accessibilityIdentifier("plugin-ui-row-actions")
        }
        .onHover { hovering = $0 }
        #else
        content
            .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                // The first in the swipe sits at the edge: the destructive one there, as Mail puts Delete.
                ForEach(ordered.filter(\.swipe).reversed()) { a in
                    Button(role: a.destructive && a.confirm == nil ? .destructive : nil) { start(a) } label: {
                        Label { Text(a.title).lineLimit(1) } icon: { if let s = a.symbol { Image(systemName: s) } }
                    }
                    .tint(a.destructive ? Theme.danger : Theme.Hue.gray)
                }
            }
        #endif
    }

    @ViewBuilder private var menu: some View {
        ForEach(ordered) { a in
            Button(role: a.destructive ? .destructive : nil) { start(a) } label: {
                Label { Text(a.title).lineLimit(1) } icon: { if let s = a.symbol { Image(systemName: s) } }
            }
        }
    }

    private func start(_ a: RowActionItem) {
        if let c = a.confirm, !c.isEmpty { asking = a } else { a.run() }
    }
}
