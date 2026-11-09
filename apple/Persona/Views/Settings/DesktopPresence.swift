import SwiftUI

#if os(iOS) || os(visionOS)
/// 「Mac 上的 Persona 已登录」 over the Agents list, like WeChat's line over its chats (docs/apple/ios.md「Mac 已登录」): one
/// quiet row while a desktop app is connected, Phone Muted beside it when on. A tap opens `DesktopPresenceSheet`.
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
/// revokes that Mac's device token; the hub closes its connection and it has to pair again).
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
                    Text("While Persona is online on a Mac, this iPhone stops sending “Needs you” and “Agent done” notifications and the Mac alerts you instead. Reminders you asked Persona to set still come through.")
                }

                ForEach(model.desktops) { d in
                    Section {
                        LabeledContent {
                            if let since = d.since {
                                Text(String(localized: "Signed in \(since.formatted(.relative(presentation: .named)))")).lineLimit(1)
                            }
                        } label: {
                            Label(d.name, systemImage: Theme.Icon.laptop).lineLimit(1)
                        }
                        if !d.device.isEmpty {
                            Button(role: .destructive) { signingOut = d } label: {
                                Text("Sign Out Persona on This Mac").lineLimit(1)
                            }
                        }
                    } footer: {
                        if d.device.isEmpty {
                            Text("This Mac connects to the hub with its own pairing credentials and can't be signed out here.")
                        }
                    }
                }
            }
            .navigationTitle("Signed-In Macs")
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
}
#endif
