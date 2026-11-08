#if canImport(ActivityKit)
import ActivityKit
#endif
import SwiftUI
import UserNotifications

/// Settings › Notifications: which pushes this iPhone gets. The hub keeps the choice per device (notify.go), so a second phone or
/// an iPad can choose differently; the system switch (allowed at all, sounds, lock screen) stays in iOS Settings.
struct NotificationSettingsView: View {
    @Environment(AppModel.self) private var model
    @State private var prefs: NotifyPrefs?
    @State private var system: UNAuthorizationStatus?
    #if os(iOS)
    @State private var activitiesAllowed = ActivityAuthorizationInfo().areActivitiesEnabled
    #endif
    @State private var debugIsland = false

    var body: some View {
        SettingsForm {
            if systemOff {
                Section {
                    Label {
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Text("Notifications are off").font(Theme.Font.heading)
                            Text("The settings below take effect only after you allow Persona to send notifications in Settings.").font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    } icon: {
                        ThemedIcon(Theme.Icon.muted).foregroundStyle(Theme.caution)
                    }
                    openSystemSettings
                }
            }

            // Whether any of what follows can reach this device while Persona is closed (#5998).
            BackgroundPushSection(status: pushStatus)

            Section {
                Toggle(isOn: binding(\.input)) {
                    Label {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text("When You're Needed")
                            Text("Questions, plan approvals, tool permissions, a terminal waiting for input").font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    } icon: {
                        ThemedIcon("exclamationmark.bubble").foregroundStyle(StatusPalette.needsYou.dot)
                    }
                }
            } footer: {
                #if os(macOS)
                Text("On permission and plan notifications you can Allow/Approve or Deny/Reject directly (hover over the notification and click Options).")
                #else
                Text("On permission and plan notifications you can Allow/Approve (after unlocking) or Deny/Reject directly.")
                #endif
            }

            Section {
                Picker(selection: binding(\.done)) {
                    Text("Off [Notification Settings View b3253d]").tag(NotifyPrefs.Done.off)
                    Text("Long Tasks Only").tag(NotifyPrefs.Done.long)
                    Text("Always [Notification Settings View 30ec51]").tag(NotifyPrefs.Done.always)
                } label: {
                    Label("When an Agent Finishes", systemImage: Theme.Icon.success)
                }
                #if os(macOS)
                .pickerStyle(.radioGroup) // a Mac form's one-of-several (#1221)
                #else
                .pickerStyle(.inline)
                #endif
                .labelsHidden() // the section header says it
            } header: {
                Text("When an Agent Finishes")
            } footer: {
                Text(doneFooter)
            }

            // Live Activities (Lock Screen and Dynamic Island) are an iPhone's: a Mac has neither (docs/apple/mac.md).
            #if os(iOS)
            Section {
                Toggle(isOn: binding(\.liveActivity)) {
                    Label {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text("Lock Screen and Dynamic Island")
                            Text("One Live Activity sums up agents that need you, are working, or are done").font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    } icon: {
                        ThemedIcon(Theme.Icon.persona).foregroundStyle(Theme.brand)
                    }
                }
                Toggle(isOn: binding(\.lockDetails)) {
                    Label {
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            Text("Show Details on Lock Screen")
                            Text("When off, the Lock Screen shows only counts and “A permission is waiting”, not agent names or content").font(Theme.Font.caption).foregroundStyle(.secondary)
                        }
                    } icon: {
                        ThemedIcon(Theme.Icon.view).foregroundStyle(.secondary)
                    }
                }
                .disabled(prefs?.liveActivity == false)
                NavigationLink {
                    IslandSettingsView(island: binding(\.island))
                } label: {
                    LabeledContent {
                        Text((prefs?.island ?? IslandPrefs()).preset.title).lineLimit(1)
                    } label: {
                        Label("Dynamic Island", systemImage: "capsule.fill")
                    }
                }
                .disabled(prefs?.liveActivity == false)
                if !activitiesAllowed {
                    Label {
                        Text("Live Activities for Persona are turned off in Settings").font(Theme.Font.caption)
                    } icon: {
                        ThemedIcon(Theme.Icon.errorFilled).foregroundStyle(Theme.caution)
                    }
                    Button {
                        UIApplication.shared.open(URL(string: UIApplication.openSettingsURLString)!)
                    } label: {
                        Label("Open Settings", systemImage: "gear").lineLimit(1)
                    }
                }
            } header: {
                Text("Live Activities")
            } footer: {
                Text("On the Lock Screen you can Allow/Approve (after unlocking) or Deny directly. The Dynamic Island appears only when unlocked and always shows details.")
            }
            #endif

            // The Mac had the button twice while notifications were off (#1224 A5): the warning above has it then.
            #if os(macOS)
            if !systemOff { systemSettingsSection }
            #else
            systemSettingsSection
            #endif
        }
        .themedPage()
        .detailTitle(String(localized: "Notifications"))
        .navigationBarTitleDisplayMode(.inline)
        .disabled(prefs == nil)
        .overlay { if prefs == nil { ProgressView() } }
        .task { await load() }
        #if DEBUG && os(iOS)
        // `-openIslandSettings YES` (with `-openNotificationSettings YES -tab settings`): Dynamic Island.
        .navigationDestination(isPresented: $debugIsland) { IslandSettingsView(island: binding(\.island)) }
        .task {
            if UserDefaults.standard.bool(forKey: "openIslandSettings") {
                try? await Task.sleep(for: .milliseconds(600))
                debugIsland = true
            }
        }
        #endif
        .onReceive(NotificationCenter.default.publisher(for: UIApplication.didBecomeActiveNotification)) { _ in
            Task { system = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus }
            #if os(iOS)
            activitiesAllowed = ActivityAuthorizationInfo().areActivitiesEnabled
            #endif
        }
    }

    /// Allowed for a while (App Clips): iOS only.
    private static func ephemeral(_ status: UNAuthorizationStatus) -> Bool {
        #if os(iOS) || os(visionOS)
        status == .ephemeral
        #else
        false
        #endif
    }

    /// Notifications are not allowed for the app (the system's switch): the page says so first.
    private var systemOff: Bool {
        guard let system else { return false }
        return system != .authorized && system != .provisional && !Self.ephemeral(system)
    }

    /// Background push at the hub this page's settings go to (#5998).
    private var pushStatus: PushStatus {
        var health = PushHealthStore.shared.health
        #if DEBUG
        // `-showPushHealth refused | moved | unreachable | relay | direct | failed | unknown`: a sample, for screenshots.
        if let sample = UserDefaults.standard.string(forKey: "showPushHealth") { health = .sample(sample, group: model.hub?.group ?? "") }
        #endif
        return health.status(group: model.hub?.group ?? "", allowed: system.map { _ in !systemOff })
    }

    private var systemSettingsSection: some View {
        Section {
            openSystemSettings
        } footer: {
            Text("Change sounds, Lock Screen display, and Focus in Settings.")
        }
    }

    private var openSystemSettings: some View {
        Button {
            UIApplication.shared.open(URL(string: UIApplication.openNotificationSettingsURLString)!)
        } label: {
            Label("Notification Settings", systemImage: "gear").lineLimit(1)
        }
    }

    private var doneFooter: String {
        let delay = NotifyPrefs.duration(prefs?.doneDelaySeconds)
        let long = NotifyPrefs.duration(prefs?.longTurnSeconds)
        return String(localized: "After finishing, Persona waits \(delay) and skips the notification if you've already looked on your computer or in the app. It notifies once, after background tasks and subagents are all done. A “long task” is a turn that ran \(long) or longer; quick answers are best read where you asked.")
    }

    private func binding<V>(_ path: WritableKeyPath<NotifyPrefs, V>) -> Binding<V> {
        Binding {
            (prefs ?? NotifyPrefs())[keyPath: path]
        } set: { value in
            guard var next = prefs else { return }
            next[keyPath: path] = value
            save(next)
        }
    }

    private func load() async {
        system = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
        guard let api = model.api else { return }
        do {
            let p = try await api.notifyPrefs(device: PushRegistrar.deviceID)
            prefs = p
            LiveActivityController.shared.settings(p)
        } catch { model.show(error) }
    }

    /// Optimistic: the control moves at once and goes back if the hub says no.
    private func save(_ next: NotifyPrefs) {
        let before = prefs
        prefs = next
        LiveActivityController.shared.settings(next)
        guard let api = model.api else { return }
        Task {
            do {
                prefs = try await api.setNotifyPrefs(device: PushRegistrar.deviceID, next)
                Haptics.success()
            } catch {
                prefs = before
                if let before { LiveActivityController.shared.settings(before) }
                model.show(error)
            }
        }
    }
}
