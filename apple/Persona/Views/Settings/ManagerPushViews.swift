import Observation
import SwiftUI

/// This device's push switch for one manager (#1863): the device prefs' `muted_managers` (GET / PUT
/// /api/v1/devices/{id}/prefs). The list goes alone, so the hub merges nothing else from this page; a hub whose prefs
/// answer has no `muted_managers` has no switch to show.
@MainActor @Observable final class ManagerPushModel {
    struct Deps {
        var api: () -> APIClient?
        var device: () -> String
        /// Where the rest of the app reads the prefs (the Mac's banners, MacAttention).
        var shared: (NotifyPrefs) -> Void
        static var live: Deps {
            .init(api: { AppModel.shared.api }, device: { PushRegistrar.deviceID }, shared: { AppModel.shared.savedNotifyPrefs($0) })
        }
    }

    let id: String
    private(set) var prefs: NotifyPrefs?
    private(set) var saving = false
    private(set) var error: String?
    @ObservationIgnored private let deps: Deps

    init(id: String, deps: Deps = .live) {
        self.id = id
        self.deps = deps
    }

    /// The hub has per-manager push switches.
    var available: Bool { prefs?.mutedManagers != nil }
    /// This device gets the manager's pushes.
    var on: Bool { !(prefs?.mutedManagers ?? []).contains(id) }

    func load() async {
        guard let api = deps.api() else { return }
        do {
            prefs = try await api.notifyPrefs(device: deps.device())
            error = nil
        } catch {
            if !error.isCancellation { self.error = error.shown }
        }
    }

    /// Shown at once, put back if the hub says no.
    func set(_ on: Bool) async {
        guard let api = deps.api(), var next = prefs, !saving else { return }
        let before = prefs
        var muted = (next.mutedManagers ?? []).filter { $0 != id }
        if !on { muted.append(id) }
        next.mutedManagers = muted
        prefs = next
        saving = true
        defer { saving = false }
        do {
            let saved = try await api.setMutedManagers(device: deps.device(), muted)
            prefs = saved
            deps.shared(saved)
            error = nil
        } catch {
            prefs = before
            self.error = String(localized: "Couldn't change notifications: \(error.shown ?? error.localizedDescription)")
        }
    }
}

/// The switch's section on a manager's page; nothing on a hub without per-manager switches. The page loads `push`
/// (an empty section has no view to run a task on).
struct ManagerPushSection: View {
    var push: ManagerPushModel

    var body: some View {
        if push.available {
            Section {
                SettingsSwitchRow(title: String(localized: "Notifications [Manager Push Views 4c1e7a]"),
                                  symbol: Theme.Icon.notifications, accessibilityID: "manager.push",
                                  isOn: Binding(get: { push.on }, set: { on in Task { await push.set(on) } }))
                    .disabled(push.saving)
            } footer: {
                Text(push.error ?? String(localized: "When off, this device gets no notifications from this manager"))
            }
        }
    }
}
