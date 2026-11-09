import SwiftUI

#if os(iOS) || os(visionOS)
/// "Persona is signed in on your Mac" over the Agents list, like WeChat's line over its chats (docs/apple/ios.md "Mac signed in"): one
/// quiet row while a desktop app is connected or you're at a computer with Persona Link, Phone Muted beside it when on. A tap
/// opens `DesktopPresenceSheet`.
struct DesktopPresenceRow: View {
    @Environment(AppModel.self) private var model
    @State private var showSheet = false

    var body: some View {
        Button { showSheet = true } label: {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.laptop)
                    .foregroundStyle(.secondary)
                    .scaledFrame(width: Theme.Size.iconS)
                Text(DesktopDevice.bannerTitle(model.desktops))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Spacer(minLength: Theme.Space.s)
                if model.muteWhileDesktop {
                    Label("Phone Muted", systemImage: Theme.Icon.muted)
                        .labelStyle(.titleAndIcon)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }
            }
            .font(Theme.Font.secondary)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("agents.desktops")
        .sheet(isPresented: $showSheet) { DesktopPresenceSheet() }
        #if DEBUG
        .task { // `-openDesktopSheet YES`: the sheet open, for screenshots
            if UserDefaults.standard.bool(forKey: "openDesktopSheet") { try? await Task.sleep(for: .seconds(1)); showSheet = true }
        }
        #endif
    }
}

/// What the banner opens, after WeChat's: which Macs are signed in and since when, Phone Muted, and Sign Out for each (it
/// revokes that Mac's device token; the hub closes its connection and it has to pair again). Persona Link entries say since
/// when you've been at that computer and have no Sign Out: they end when you lock it or quit Persona Link (said once, under
/// the first of them).
struct DesktopPresenceSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var signingOut: DesktopDevice?

    var body: some View {
        NavigationStack {
            List {
                Section {
                    SettingsSwitchRow(title: String(localized: "Phone Muted"), symbol: Theme.Icon.muted, accessibilityID: "desktops.mute",
                                      isOn: Binding(get: { model.muteWhileDesktop }, set: { on in Task { await model.setMuteWhileDesktop(on) } }))
                } footer: {
                    Text(DesktopDevice.muteNote(model.desktops))
                }

                let firstLink = model.desktops.first(where: \.isLink)?.id
                ForEach(model.desktops) { d in
                    Section {
                        LabeledContent {
                            if let since = d.since {
                                Text(Self.sinceLine(d, since)).lineLimit(1)
                            }
                        } label: {
                            Label(d.name, systemImage: Theme.Icon.laptop).lineLimit(1)
                        }
                        if d.canSignOut {
                            Button(role: .destructive) { signingOut = d } label: {
                                Text("Sign Out Persona on This Mac").lineLimit(1)
                            }
                        }
                    } footer: {
                        if d.id == firstLink {
                            Text("Persona Link tells the hub you're at this computer while it's unlocked and in use. This stops when you lock the computer or quit Persona Link.")
                        } else if d.isApp && d.device.isEmpty {
                            Text("This Mac connects to the hub with its own pairing credentials and can't be signed out here.")
                        }
                    }
                }
            }
            .navigationTitle(model.desktops.contains(where: \.isLink) ? String(localized: "Computers in Use") : String(localized: "Signed-In Macs"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() }.lineLimit(1) }
            }
            .confirmationDialog(signingOut.map { String(localized: "Sign out Persona on “\($0.name)”?") } ?? "",
                                isPresented: Binding(get: { signingOut != nil }, set: { if !$0 { signingOut = nil } }),
                                titleVisibility: .visible) {
                Button("Sign Out [Desktop Presence 498e1d]", role: .destructive) {
                    if let d = signingOut { Task { await model.signOutDesktop(d) } }
                }
            } message: {
                Text("That Mac will need to pair again to use Persona.")
            }
            .onChange(of: model.desktops.isEmpty) { _, none in if none { dismiss() } }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    /// A Mac app's row says when it signed in; a Persona Link row since when you've been at it, with the date when not today.
    private static func sinceLine(_ d: DesktopDevice, _ since: Date) -> String {
        guard d.isLink else { return String(localized: "Signed in \(since.formatted(.relative(presentation: .named)))") }
        let at = since.formatted(date: Calendar.current.isDateInToday(since) ? .omitted : .abbreviated, time: .shortened)
        return String(localized: "Persona Link · since \(at)")
    }
}
#endif
