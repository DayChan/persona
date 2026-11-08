import Foundation
import UserNotifications

/// Buttons on interaction pushes. The hub sends `category: INTERACTION_<KIND>` (server/internal/hub/push.go), and
/// `SESSION_DONE` on a finished turn; these are the categories it names. Anything that says yes to the agent needs the
/// phone unlocked; saying no does not.
nonisolated enum NotificationActions {
    static let allow = "IX_ALLOW"
    static let deny = "IX_DENY"
    static let approvePlan = "IX_APPROVE_PLAN"
    static let rejectPlan = "IX_REJECT_PLAN"
    /// A finished turn's push: a message to the session, typed on the notification (#3449).
    static let reply = "SESSION_REPLY"
    static let doneCategory = "SESSION_DONE"

    static func category(_ kind: InteractionKind) -> String { "INTERACTION_" + kind.rawValue.uppercased() }

    static var categories: Set<UNNotificationCategory> {
        let permission = UNNotificationCategory(
            identifier: category(.permission),
            actions: [
                UNNotificationAction(identifier: allow, title: String(localized: "Allow [Notification Actions ce7ef2]"), options: [.authenticationRequired],
                                     icon: UNNotificationActionIcon(systemImageName: "checkmark")),
                UNNotificationAction(identifier: deny, title: String(localized: "Deny"), options: [.destructive],
                                     icon: UNNotificationActionIcon(systemImageName: "xmark")),
            ],
            intentIdentifiers: [], options: [])
        let plan = UNNotificationCategory(
            identifier: category(.plan),
            actions: [
                UNNotificationAction(identifier: approvePlan, title: String(localized: "Approve [Notification Actions 8cbe69]"), options: [.authenticationRequired],
                                     icon: UNNotificationActionIcon(systemImageName: "checkmark")),
                UNTextInputNotificationAction(identifier: rejectPlan, title: String(localized: "Send Back [Notification Actions cd3150]"), options: [.authenticationRequired],
                                              icon: UNNotificationActionIcon(systemImageName: Theme.Icon.undo),
                                              textInputButtonTitle: String(localized: "Send Back [Notification Actions a20f13]"), textInputPlaceholder: String(localized: "Tell the agent what to change…")),
            ],
            intentIdentifiers: [], options: [])
        // Questions and terminal prompts need the screen: no buttons, a tap opens the session. Registered anyway so
        // the system knows the identifiers the hub sends.
        let question = UNNotificationCategory(identifier: category(.question), actions: [], intentIdentifiers: [], options: [])
        let screen = UNNotificationCategory(identifier: category(.screen), actions: [], intentIdentifiers: [], options: [])
        // A message is an instruction to the agent: unlocked, like approving a plan.
        let done = UNNotificationCategory(
            identifier: doneCategory,
            actions: [
                UNTextInputNotificationAction(identifier: reply, title: String(localized: "Reply"), options: [.authenticationRequired],
                                              icon: UNNotificationActionIcon(systemImageName: Theme.Icon.reply),
                                              textInputButtonTitle: String(localized: "Send"), textInputPlaceholder: String(localized: "What next…")),
            ],
            intentIdentifiers: [], options: [])
        // Pairing receipts (#4199): Not you? Revoke, It's Mine / Not Mine (PairReceiptActions).
        return Set([permission, plan, question, screen, done] + PairReceiptActions.categories)
    }

    /// What a button sends to the hub, or nil for a plain tap (and anything we don't know).
    /// Plans approve in acceptEdits: from a notification you have not read the plan in full, so edits go through but
    /// commands still ask. "Approve & Auto-Run" stays an in-app choice.
    static func resolution(action: String, text: String? = nil) -> Resolution? {
        switch action {
        case allow: Resolution(allow: true)
        case deny: Resolution(allow: false)
        case approvePlan: Resolution(approve: true, mode: "acceptEdits")
        case rejectPlan: Resolution(approve: false, feedback: text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "")
        default: nil
        }
    }

    /// The message a Reply on a finished turn's push sends, nil for anything else or an empty one.
    static func replyText(action: String, text: String?) -> String? {
        guard action == reply, let t = text?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
        return t
    }

    /// Sends a reply typed on the notification to its session, as the composer would (a new client_msg_id, so a
    /// retry by the system is typed once); a failure comes back as a local notification.
    static func sendReply(_ key: SessionKey, text: String) async {
        let id = UUID()
        do {
            try await HubStore.client(group: key.group).send(key, text: text, clientMsgID: id.uuidString, opID: id)
        } catch {
            await reportFailure(id: id.uuidString, title: failureTitle(action: reply), error: error)
        }
    }

    /// Posts "Couldn't allow · This was already handled." and the like: buttons run while nobody looks at the app.
    static func reportFailure(id: String, title: String, error: Error) async {
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = IntentFailure.sentence(for: error, host: HubStore.load().hubs.first?.config.url.host())

        try? await UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: "resolve-failed-\(id)", content: content, trigger: nil))
    }

    /// Takes the session's notifications out of Notification Center once it is on screen (#3449): its pushes carry
    /// `thread-id` = the session id, and `machine_id` tells two machines' sessions apart.
    static func clearDelivered(_ key: SessionKey) {
        // Called back on the center's own queue: nothing in it needs the main actor.
        UNUserNotificationCenter.current().getDeliveredNotifications { @Sendable delivered in
            let ids = delivered.filter {
                $0.request.content.threadIdentifier == key.sessionID
                    && ($0.request.content.userInfo["machine_id"] as? String).map { $0 == key.machineID } != false
            }.map(\.request.identifier)
            if !ids.isEmpty { UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: ids) }
        }
    }

    /// Shown (as a local notification) when a button could not be carried out: the phone was locked in a drawer
    /// while the agent moved on, the hub was unreachable, and so on.
    static func failureTitle(action: String) -> String {
        switch action {
        case allow: String(localized: "Couldn't allow")
        case deny: String(localized: "Couldn't deny")
        case approvePlan: String(localized: "Couldn't approve the plan")
        case rejectPlan: String(localized: "Couldn't send back the plan")
        case reply: String(localized: "Couldn't send the reply")
        default: String(localized: "Couldn't complete the action")
        }
    }
}
