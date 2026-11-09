import SwiftUI

/// The Inbox menu (docs/apple/mac.md「菜单和快捷键」): the toolbar's Clear All (⇧⌘E) for the open items in view, and its
/// Rules, while the inbox is the section on screen (the list presents their sheets). Many items ask first, in the list
/// (`confirming`).
struct MacInboxMenu: Commands {
    let model: AppModel

    var body: some Commands {
        CommandMenu(String(localized: "Inbox")) {
            let state = MacInboxState.shared
            let onScreen = MacSection.shown(override: MacShellState.shared.section, tab: model.selectedTab) == .inbox
            let off = !onScreen || state.openInView.isEmpty
            Button("Clean up all") { state.clean() }
                .keyboardShortcut("e", modifiers: [.command, .shift])
                .disabled(off)
            Divider()
            Button("Rules [Inbox Policy]") { state.showRules = true }
                .disabled(!onScreen)
        }
    }
}
