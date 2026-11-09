import AppKit
import UserNotifications

/// Posts and takes back the "Needs you" notifications (`AttentionNotifications` decides which), asks for permission
/// once the hub sends a summary, and hands a clicked notification's route to `open`. Only the running app uses it: the
/// notification center needs a bundled app, so snapshots and tests never touch it.
@MainActor final class LinkNotifier: NSObject, UNUserNotificationCenterDelegate {
    static let shared = LinkNotifier()

    private var diff = AttentionNotifications()
    private var asked = false
    private var allowed = true
    /// The work of each change, in order: each waits for the one before, so a post never lands after a later take-back.
    private var last: Task<Void, Never>?
    private var open: (_ route: String, _ webURL: String?) -> Void = { _, _ in }
    private var report: (_ allowed: Bool) -> Void = { _ in }
    private var center: UNUserNotificationCenter { .current() }
    /// `UNUserNotificationCenter.current()` raises in a process outside an app bundle (`swift run`, the package's
    /// executable run from Xcode): there this does nothing.
    private let bundled = Bundle.main.bundleURL.pathExtension == "app"

    /// Called at launch, before any summary. `open`: a clicked notification's route and the hub's web address when it
    /// was posted. `allowed`: whether notifications are allowed, whenever that is checked.
    func start(open: @escaping (_ route: String, _ webURL: String?) -> Void, allowed: @escaping (_ allowed: Bool) -> Void) {
        guard bundled else { return }
        self.open = open
        report = allowed
        center.delegate = self
        // Notifications an earlier run left shown go when the summary no longer has their session.
        run {
            let delivered = await self.center.deliveredNotifications().map(\.request)
                .filter { $0.identifier.hasSuffix("#needs") }
                .map { (id: $0.identifier, card: $0.content.userInfo[AttentionNote.cardKey] as? String) }
            self.withdraw(self.diff.adopt(delivered))
            await self.refreshAuthorization()
        }
    }

    /// A status line's summary changed (nil: it went away).
    func apply(_ attention: LinkAttention?) {
        guard bundled else { return }
        let changes = diff.apply(attention)
        let notes = changes.post
        let connection = changes.connection
        let webURL = attention?.webURL
        // Asked once the hub sends summaries at all, not at launch: an older hub never sends one.
        let ask = attention != nil && !asked
        if ask { asked = true }
        run {
            self.withdraw(changes.withdrawNow)
            if ask {
                await self.requestAuthorization()
            } else if !notes.isEmpty {
                await self.refreshAuthorization() // System Settings may have turned them on or off since the last check
            }
            // While the permission prompt waited, a later summary may have answered or replaced the card or stopped it
            // notifying (muted), or the connection may have dropped.
            for item in notes {
                if self.allowed, self.diff.isCurrent(item, connection: connection),
                   await self.post(AttentionNote(item, webURL: webURL)) {
                    // Shown only now; the summary may have moved on while it went out.
                    self.withdraw(self.diff.adopt([(item.id, item.card)]))
                } else if changes.withdraw.contains(item.id) {
                    self.withdraw([item.id]) // the older notification it was to replace
                }
            }
        }
    }

    /// Whether notifications are allowed now, for the menu (System Settings may have changed it meanwhile).
    func refreshAuthorization() async {
        guard bundled else { return }
        allowed = await center.notificationSettings().authorizationStatus != .denied
        report(allowed)
    }

    private func run(_ work: @escaping @MainActor () async -> Void) {
        let previous = last
        last = Task {
            await previous?.value
            await work()
        }
    }

    private func withdraw(_ ids: [String]) {
        guard !ids.isEmpty else { return }
        center.removeDeliveredNotifications(withIdentifiers: ids)
        center.removePendingNotificationRequests(withIdentifiers: ids)
    }

    /// Prompts the first time only; later calls return what the user chose.
    private func requestAuthorization() async {
        do {
            _ = try await center.requestAuthorization(options: [.alert, .sound])
        } catch {
            NSLog("Persona Link: notification permission not asked: %@", error.localizedDescription)
        }
        await refreshAuthorization() // denied: the menu says so
    }

    /// Whether it went out.
    private func post(_ note: AttentionNote) async -> Bool {
        let content = UNMutableNotificationContent()
        content.title = note.title
        content.subtitle = note.subtitle
        content.body = note.body
        content.sound = .default
        content.userInfo = note.userInfo
        do {
            // The same identifier replaces a notification still shown for this session.
            try await center.add(UNNotificationRequest(identifier: note.id, content: content, trigger: nil))
            return true
        } catch {
            NSLog("Persona Link: notification not posted: %@", error.localizedDescription)
            return false
        }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                            withCompletionHandler completionHandler: @escaping () -> Void) {
        let info = response.notification.request.content.userInfo
        let route = info[AttentionNote.routeKey] as? String ?? ""
        let webURL = info[AttentionNote.webURLKey] as? String
        Task { @MainActor in LinkNotifier.shared.open(route, webURL) }
        completionHandler()
    }

    /// The app counts as in front while its menu or a window is: show the banner anyway.
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                            withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .list, .sound])
    }
}
