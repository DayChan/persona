import AppIntents
import Foundation

/// "Approve First Pending": settles the oldest interaction 「Persona 状态」 reads out (`IntentPicker.waiting`) when
/// it is a permission or a plan. Questions and terminal prompts need the screen, so those answer with a hint to open
/// the app. Saying yes needs the phone unlocked, as on the notification and Live Activity buttons: Siri runs it on a
/// locked phone too (#1878).
struct ApproveFirstIntent: AppIntent {
    nonisolated static let title: LocalizedStringResource = "Approve First Pending"
    nonisolated static let description = IntentDescription(
        "Allows the oldest tool permission request, or approves a plan with edits accepted automatically (commands still ask). Asks you to unlock first when locked. Questions and terminal prompts need the app.",
        categoryName: "Status",
        searchKeywords: ["Approve [Notification Actions 8cbe69]", "Allow [Notification Actions ce7ef2]", "Permission", "plan"]
    )
    nonisolated static var supportedModes: IntentModes { .background }
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .requiresAuthentication }

    nonisolated init() {}

    nonisolated func perform() async throws -> some IntentResult & ProvidesDialog & ReturnsValue<String> {
        do {
            // The oldest of every group's cards 「Persona 状态」 reads out (#5257): never a muted agent's or a quiet
            // task's, settled at its own group's hub.
            async let sessionsRequest = IntentSupport.sessions()
            async let interactionsRequest = IntentSupport.interactions()
            async let mutesRequest = IntentSupport.mutes()
            let sessions = try await sessionsRequest
            guard let first = Self.target(try await interactionsRequest, sessions: sessions,
                                          mutes: try await mutesRequest) else {
                throw IntentFailure.noPending
            }
            guard let resolution = Self.resolution(for: first) else {
                let hint = IntentText.needsApp(first.kind)
                return .result(value: hint, dialog: IntentSupport.dialog(hint))
            }
            let api = try IntentSupport.client(group: first.group)
            let session = sessions.first { $0.key == first.sessionKey }
            guard first.canAnswer(session: session) else {
                let hint = String(localized: "This session can't be approved from here. Open the app to check it.")
                return .result(value: hint, dialog: IntentSupport.dialog(hint))
            }
            try await api.resolve(first.id, resolution)
            let text = Self.doneText(for: first)
            return .result(value: text, dialog: IntentSupport.dialog(text))
        } catch {
            throw IntentFailureSentence(IntentFailure.sentence(for: error, host: IntentSupport.host()))
        }
    }

    /// The card to settle: the first of what 「Persona 状态」 reads out (`IntentPicker.waiting`, #5257).
    static func target(_ interactions: [Interaction], sessions: [Session], mutes: [String: Pins]) -> Interaction? {
        IntentPicker.waiting(interactions, sessions: sessions, mutes: mutes).first
    }

    /// What to send, or nil when this kind cannot be settled from Siri. Plans approve in acceptEdits, like the
    /// notification button: Siri never shows the plan in full, so commands still ask. "Approve & Auto-Run" stays in the app.
    static func resolution(for ix: Interaction) -> Resolution? {
        guard !ix.requiresInput else { return nil }
        return switch ix.kind {
        case .permission: Resolution(allow: true)
        case .plan: Resolution(approve: true, mode: "acceptEdits")
        case .question, .screen: nil
        }
    }

    static func doneText(for ix: Interaction) -> String {
        switch ix.kind {
        case .permission: IntentText.approvedPermission(ix.toolName)
        case .plan: IntentText.approvedPlan
        case .question, .screen: IntentText.needsApp(ix.kind)
        }
    }
}
