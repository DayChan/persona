import SwiftUI

// Several groups of hubs on one device (docs/protocol/multi-hub.md): what the merged lists and the hub-level pages show of them.
// With one group none of this draws anything: every screen stays what it was.

/// The group filter of the merged lists (Agents, the ⋯ menu): "" every group, "=<group>" one of them (the default
/// group's id is "", hence the mark).
nonisolated enum HubFilter {
    static let key = "dev.hubFilter"
    static func value(_ group: String) -> String { "=" + group }
    static func matches(_ filter: String, _ group: String) -> Bool { filter.isEmpty || filter == "=" + group }
    static func group(_ filter: String) -> String? { filter.hasPrefix("=") ? String(filter.dropFirst()) : nil }
}

/// Which group of hubs a row or a page is about, as a small chip; nothing with one group.
struct HubTag: View {
    @Environment(AppModel.self) private var model
    var group: String

    var body: some View {
        if model.isMultiHub {
            Chip(text: model.groupName(group), symbol: Theme.Icon.hubGroup, shrinks: true)
                .accessibilityLabel("From \(model.groupName(group))")
        }
    }
}

/// The group a hub-level page (Persona, Inbox, the hub's settings) shows, and the menu to pick another; nothing with
/// one group. Each group has its own Persona, inbox and settings: they are not merged (docs/archive/operations/plan-hub-mesh.md 2.6).
struct HubSwitcher: View {
    @Environment(AppModel.self) private var model
    /// Only the group's icon and name, for a bar; the full label elsewhere.
    var compact = false

    var body: some View {
        if model.isMultiHub, let current = model.hub?.group {
            Menu {
                Picker("hub", selection: Binding(get: { current }, set: { model.selectHub($0) })) {
                    ForEach(model.groupIDs, id: \.self) { g in
                        Label(model.groupName(g), systemImage: Theme.Icon.hubGroup).tag(g)
                    }
                }
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.hubGroup)
                    Text(model.groupName(current)).lineLimit(1)
                    ThemedIcon(Theme.Icon.expand).font(Theme.Font.labelHeavy)
                }
                .font(compact ? Theme.Font.label : Theme.Font.secondaryStrong)
                .foregroundStyle(Color.secondary)
                .fixedSize()
            }
            .accessibilityIdentifier("hub.switcher")
            .accessibilityLabel("Current hub: \(model.groupName(current))")
        }
    }
}

/// A group's link to its hub as a dot: up, being opened, or down.
struct HubStatusDot: View {
    var live: LiveConnection

    var body: some View {
        Circle()
            .fill(color)
            .scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
            .accessibilityHidden(true)
    }

    private var color: Color {
        if live.inGrace { return Theme.positive } // dropped a moment ago and coming back (a hub restart): nothing to say
        return switch live.state {
        case .connected: live.stalled ? Theme.caution : Theme.positive
        case .connecting: Theme.caution
        case .idle, .failed: Color.secondary
        }
    }
}
