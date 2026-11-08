import SwiftUI

/// Settings › hub (docs/protocol/multi-hub.md「设置」): every hub this device paired with, by group. Hubs that know each other (their
/// hellos list each other) are one group, reached over one connection; separate groups are shown side by side
/// everywhere. Here: add a hub, rename a group, pick the group Persona / Inbox / the hub's settings show, disconnect a
/// hub or a whole group.
struct HubsView: View {
    @Environment(AppModel.self) private var model
    @State private var showPair = false
    @State private var renaming: String?
    @State private var newName = ""
    @State private var removingGroup: String?
    @State private var removingHub: PairedHub?

    var body: some View {
        SettingsForm {
            ForEach(model.groupIDs, id: \.self) { g in
                Section {
                    groupRows(g)
                } header: {
                    header(g)
                } footer: {
                    Text(footer(g))
                }
            }
            Section {
                #if os(macOS)
                HStack {
                    Spacer(minLength: 0)
                    Button("Add Hub…") { showPair = true }.lineLimit(1).formButton().accessibilityIdentifier("hubs.add")
                }
                #else
                Button { showPair = true } label: {
                    SettingsRow(title: String(localized: "Add Hub"), symbol: Theme.Icon.add, tint: Theme.accent)
                }
                .modifier(SettingsButtonLook())
                .accessibilityIdentifier("hubs.add")
                .bareListRow()
                #endif
            } footer: {
                Text("A hub you add joins an existing group automatically when it's linked with a hub in it; otherwise it forms its own group. Everything shows in one list, labeled with the group it comes from.")
            }
        }
        .themedPage()
        .detailTitle("hub")
        .sheet(isPresented: $showPair) { PairView() }
        .alert("Name This Group", isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
            TextField(String(localized: "Name [Add Computer View 677331]"), text: $newName)
            Button("Save") { if let g = renaming { model.renameGroup(g, to: newName) }; renaming = nil }
            Button("Cancel", role: .cancel) { renaming = nil }
        } message: {
            Text("Only changes the name on this device. Leave empty to use the hub's own name.")
        }
        .confirmationDialog(removingGroup.map { String(localized: "Disconnect “\(model.groupName($0))”?") } ?? "", isPresented: Binding(
            get: { removingGroup != nil }, set: { if !$0 { removingGroup = nil } }
        ), titleVisibility: .visible, presenting: removingGroup) { g in
            Button("Disconnect This Group", role: .destructive) { model.removeGroup(g); removingGroup = nil }
        } message: { g in
            Text(model.groupIDs.count > 1 ? String(localized: "This group's machines, agents, and cached chats are removed from this device; other groups aren't affected. To use it again, pair again.")
                                         : String(localized: "After disconnecting, you'll need to scan the code to pair again."))
        }
        .confirmationDialog(removingHub.map { String(localized: "Disconnect “\($0.displayName)”?") } ?? "", isPresented: Binding(
            get: { removingHub != nil }, set: { if !$0 { removingHub = nil } }
        ), titleVisibility: .visible, presenting: removingHub) { h in
            Button("Disconnect This Hub", role: .destructive) { model.removeHub(h.id); removingHub = nil }
        } message: { _ in
            Text("Only this hub is disconnected; the group stays connected through its other hubs.")
        }
    }

    @ViewBuilder private func groupRows(_ g: String) -> some View {
        let members = model.registry.members(g)
        let on = model.socket(g)?.currentConfig
        ForEach(members) { h in
            hubRow(h, inUse: h.config == on && model.isLive(g))
                .contextMenu {
                    if members.count > 1 {
                        Button(role: .destructive) { removingHub = h } label: { Label("Disconnect This Hub", systemImage: Theme.Icon.end) }
                    }
                }
        }
        // The group's other hubs, reached through it (listed in its mesh, not paired here).
        ForEach(meshOnly(g), id: \.hubID) { m in
            meshRow(m, inUse: model.isLive(g) && on.map { cfg in m.endpoints?.contains { $0.hasPrefix(cfg.url.absoluteString) } == true } == true)
        }
        Group {
            // Each paired hub's SSH tunnel (docs/operations/network.md「经 SSH 隧道连 hub」). Keyed apart from the hub rows above: the
            // same ids twice in one section, and the Mac showed only one of the two sets of rows (#1221).
            ForEach(members, id: \.sshRowID) { h in
                NavigationLink(value: SettingsPage.sshTunnel(h.id)) {
                    SettingsRow(title: members.count > 1 ? String(localized: "SSH Tunnel · \(h.displayName)") : String(localized: "SSH Tunnel"), symbol: Theme.Icon.sshTunnel,
                                tint: Theme.Hue.green, value: sshValue(h), look: .oneLine)
                }
                .accessibilityIdentifier("hubs.ssh.\(h.id)")
            }
            // Each paired hub's local SOCKS5 proxy (docs/operations/network.md "Connecting to hub through a local SOCKS5
            // proxy"), keyed apart like the SSH rows.
            ForEach(members, id: \.localProxyRowID) { h in
                NavigationLink(value: SettingsPage.localProxy(h.id)) {
                    SettingsRow(title: members.count > 1 ? String(localized: "Local Proxy · \(h.displayName)") : String(localized: "Local Proxy"),
                                symbol: Theme.Icon.localProxy, tint: Theme.Hue.indigo, value: localProxyValue(h), look: .oneLine)
                }
                .accessibilityIdentifier("hubs.proxy.\(h.id)")
            }
            // Each paired hub's in-app tunnel (docs/operations/network.md「经 App 内隧道连 hub」): only a hub whose extension
            // contributes one has it, titled with the tunnel's name. Keyed apart like the SSH rows.
            ForEach(members.filter { $0.inAppTunnel != nil }, id: \.inAppTunnelRowID) { h in
                let name = h.inAppTunnel?.title ?? ""
                NavigationLink(value: SettingsPage.inAppTunnel(h.id)) {
                    SettingsRow(title: members.count > 1 ? String(localized: "\(name) · \(h.displayName)") : name,
                                symbol: Theme.Icon.inAppTunnel, tint: Theme.Hue.blue, value: inAppTunnelValue(h), look: .oneLine)
                }
                .accessibilityIdentifier("hubs.tunnel.\(h.id)")
            }
            #if os(macOS)
            // Standard buttons side by side at the group's trailing end, as a Mac form has them, not rows the width of
            // the form (#1221).
            HStack(spacing: Theme.Space.s) {
                Spacer(minLength: 0)
                if model.isMultiHub, model.hub?.group != g {
                    Button("Make Current Hub") { model.selectHub(g) }.lineLimit(1).formButton()
                }
                Button("Rename… [Hubs View 5f6ecf]") { newName = model.registry.groupNames[g] ?? ""; renaming = g }
                    .lineLimit(1).formButton()
                    .accessibilityIdentifier("hubs.rename.\(g)")
                Button(model.groupIDs.count > 1 ? String(localized: "Disconnect This Group…") : String(localized: "Disconnect and Forget Server…"), role: .destructive) { removingGroup = g }
                    .lineLimit(1).formButton()
                    .accessibilityIdentifier("hubs.remove.\(g)")
            }
            #else
            if model.isMultiHub, model.hub?.group != g {
                Button { model.selectHub(g) } label: {
                    SettingsRow(title: String(localized: "Make Current Hub"), symbol: Theme.Icon.successFilled, tint: Theme.positive)
                }
                .modifier(SettingsButtonLook())
            }
            Button { newName = model.registry.groupNames[g] ?? ""; renaming = g } label: {
                SettingsRow(title: String(localized: "Rename [Hubs View 83c37e]"), symbol: Theme.Icon.edit, tint: Theme.Hue.gray)
            }
            .modifier(SettingsButtonLook())
            .accessibilityIdentifier("hubs.rename.\(g)")
            Button(role: .destructive) { removingGroup = g } label: {
                SettingsRow(title: model.groupIDs.count > 1 ? String(localized: "Disconnect This Group") : String(localized: "Disconnect and Forget Server"), symbol: Theme.Icon.end, tint: Theme.danger)
            }
            .modifier(SettingsButtonLook())
            .accessibilityIdentifier("hubs.remove.\(g)")
            #endif
        }
        .bareListRow()
    }

    private func header(_ g: String) -> some View {
        HStack(spacing: Theme.Space.sm) {
            if let live = model.socket(g) { HubStatusDot(live: live) }
            SettingsHeading(model.groupName(g))
            if model.isMultiHub, model.hub?.group == g { Chip(text: String(localized: "Current"), look: .outline) }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("hubs.group.\(g)")
    }

    /// What the SSH Tunnel row says: not set, used now, or there as the fallback / for every connection.
    private func sshValue(_ h: PairedHub) -> String {
        guard let route = h.ssh, route.isUsable else { return String(localized: "Not set") }
        if SSHTunnels.shared.routed.contains(h.id) { return String(localized: "In Use [Hubs View fa48e8]") }
        return route.always ? String(localized: "Always [Hubs View 1d325d]") : String(localized: "Fallback")
    }

    /// What the Local Proxy row says, as the SSH one does.
    private func localProxyValue(_ h: PairedHub) -> String {
        guard let route = h.localProxy, route.isUsable else { return String(localized: "Not set") }
        if LocalProxies.shared.routed.contains(h.id) { return String(localized: "In Use [Hubs View fa48e8]") }
        return route.always ? String(localized: "Always [Hubs View 1d325d]") : String(localized: "Fallback")
    }

    /// What the in-app tunnel's row says, as the SSH one does.
    private func inAppTunnelValue(_ h: PairedHub) -> String {
        guard let route = h.inAppTunnel, route.isUsable else { return String(localized: "Not set") }
        guard InAppTunnel.available else { return String(localized: "Not in this build") }
        if InAppTunnels.shared.routed.contains(h.id) { return String(localized: "In Use [Hubs View fa48e8]") }
        return route.always ? String(localized: "Always [Hubs View 1d325d]") : String(localized: "Fallback")
    }

    private func footer(_ g: String) -> String {
        let n = model.registry.members(g).count + meshOnly(g).count
        let inApp = model.inAppTunnelState(g).flatMap { $0.routed ? $0.route.title : nil }
            ?? (model.localProxyState(g)?.routed == true ? String(localized: "Local Proxy") : nil)
        let routed = model.tunnel(g)?.routed == true
        let state = model.isLive(g)
            ? (inApp.map { String(localized: "Connected (via \($0))") } ?? (routed ? String(localized: "Connected (via SSH tunnel)") : String(localized: "Connected [V N C Page 5be032]")))
            : model.socket(g)?.state == .connecting
            ? (inApp.map { String(localized: "Connecting (via \($0))") } ?? (routed ? String(localized: "Connecting (via SSH tunnel)") : String(localized: "Connecting")))
            : String(localized: "Not Connected")
        let group = n > 1 ? String(localized: "\(n) linked hubs in this group; you see the whole group's machines. If the connected one drops, another in the group takes over. ") : ""
        let current = model.isMultiHub && model.hub?.group == g ? String(localized: "Persona, the Inbox, and hub settings use this group.") : ""
        return String(localized: "\(state)。", comment: "Sentence end after the connection state; more sentences may follow") + group + current
    }

    private func meshOnly(_ g: String) -> [MeshHub] {
        let members = model.registry.members(g)
        let paired = Set(members.compactMap(\.hubID))
        var seen = Set<String>(), out: [MeshHub] = []
        // Not a paired hub, by id or by address (a paired hub whose hub_id the app has not learned yet).
        let urls = Set(members.map { $0.config.url.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/")) })
        for h in members {
            for m in h.mesh ?? [] where !paired.contains(m.hubID) && seen.insert(m.hubID).inserted {
                if m.endpoints?.contains(where: { urls.contains($0.trimmingCharacters(in: CharacterSet(charactersIn: "/"))) }) == true { continue }
                out.append(m)
            }
        }
        return out
    }

    private func hubRow(_ h: PairedHub, inUse: Bool) -> some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.server)
                .scaledFrame(width: Theme.Size.icon)
                .foregroundStyle(Theme.accent)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(spacing: Theme.Space.sm) {
                    Text(h.displayName).lineLimit(1)
                    if inUse { Chip(text: String(localized: "In Use [Hubs View 564c83]"), look: .outline) }
                    if SSHTunnels.shared.routed.contains(h.id) { Chip(text: "SSH", look: .neutral) }
                    if LocalProxies.shared.routed.contains(h.id) { Chip(text: String(localized: "Proxy"), look: .neutral) }
                    if InAppTunnels.shared.routed.contains(h.id) { Chip(text: String(localized: "Tunnel [Hubs View 55d2d3]"), look: .neutral) }
                }
                Text(h.config.url.absoluteString).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("hubs.hub.\(h.id)")
    }

    private func meshRow(_ m: MeshHub, inUse: Bool) -> some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.hubGroup)
                .scaledFrame(width: Theme.Size.icon)
                .foregroundStyle(m.online == false ? Color.secondary : Theme.accent)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                HStack(spacing: Theme.Space.sm) {
                    Text(m.name ?? m.hubID).lineLimit(1)
                    Chip(text: String(localized: "Linked"), look: .neutral)
                    if inUse { Chip(text: String(localized: "In Use [Hubs View 564c83]"), look: .outline) }
                }
                Text(m.online == false ? String(localized: "Offline [Machine Entity be1b4f]") : (m.endpoints?.first ?? "")).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 0)
        }
        .accessibilityElement(children: .combine)
    }
}

private extension PairedHub {
    /// The SSH tunnel row's identity, apart from the hub's own row in the same section.
    var sshRowID: String { "ssh." + id }
    /// The in-app tunnel row's identity, apart from the hub's own row and its SSH row.
    var inAppTunnelRowID: String { "tunnel." + id }
    /// The Local Proxy row's identity, apart from the hub's other rows.
    var localProxyRowID: String { "proxy." + id }
}
