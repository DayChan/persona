import SwiftUI

/// Settings › Session Triage (#1413): what each switch changes, with made-up agents. Look only: drawn in an
/// `ExampleFrame`, never tapped, never sent anywhere.
enum TriageExample {
    /// A turn that ended and nobody read: Unread, as every stopped agent is with the triage off.
    static func unread(_ id: String, title: String, now: Date = Date()) -> Session? {
        session(id, title: title, state: "unread", now: now, extra: [:])
    }

    /// The same agent once the triage read its turn: what it waits for.
    static func triaged(now: Date = Date()) -> Session? {
        session("triage", title: String(localized: "Fix the sign-in test"), state: "idle", now: now,
                extra: ["work": ["phase": "blocked", "on": "external", "line": String(localized: "Waiting for CI"),
                                 "wait": String(localized: "CI")]])
    }

    static var decideTitle: String { String(localized: "Release 2.4") }

    /// The decision that agent waits on, as its card in Needs You.
    static func decision(now: Date = Date()) -> Interaction {
        Interaction(id: "triage-example-decide", machineID: "example", sessionID: "decide",
                    sessionTitle: decideTitle, kind: .question, createdAt: now,
                    questions: [Question(question: String(localized: "Push the fix to beta?"), header: nil, multiSelect: false,
                                         options: [QuestionOption(label: String(localized: "Push")),
                                                   QuestionOption(label: String(localized: "Not Yet"))])])
    }

    static var continueSteps: [ExampleTimeline.Step] {
        [.init(symbol: Theme.Icon.error, text: String(localized: "Model 503"), look: .tint(Theme.danger)),
         .init(symbol: Theme.Icon.send, text: String(localized: "Sends “continue” in 1 min")),
         .init(symbol: Theme.Icon.working, text: String(localized: "Keeps going"), look: .tone(StatusPalette.working))]
    }

    static var wakeSteps: [ExampleTimeline.Step] {
        [.init(symbol: Theme.Icon.time, text: String(localized: "Waiting for the deploy")),
         .init(symbol: Theme.Icon.schedule, text: String(localized: "In 10 min")),
         .init(symbol: Theme.Icon.search, text: String(localized: "Checks for itself"), look: .tone(StatusPalette.working))]
    }

    /// The reminder push, worded as the hub words it (`reply_title`; its body is what the agent asked).
    static var reminderTitle: String { String(localized: "\(String(localized: "Clean up old branches")) is still waiting for your answer") }
    static var reminderMessage: String { String(localized: "Delete these 12 merged branches?") }

    private static func session(_ id: String, title: String, state: String, now: Date, extra: [String: Any]) -> Session? {
        var fields: [String: Any] = ["machine_id": "example", "session_id": id, "title": title, "cwd": "/example/app",
                                     "status": "idle", "state": state, "agent": "claude", "pending_interactions": 0,
                                     "updated_at": ISO8601DateFormatter().string(from: now.addingTimeInterval(-120))]
        fields.merge(extra) { $1 }
        guard let data = try? JSONSerialization.data(withJSONObject: fields) else { return nil }
        return try? JSON.decoder.decode(Session.self, from: data)
    }
}

/// Each switch's example under it on Settings › Session Triage.
struct TriageExampleView: View {
    enum Kind { case triage, decide, autoContinue, wake, replyReminder }
    var kind: Kind
    var replyMinutes: Int

    var body: some View {
        ExampleFrame {
            switch kind {
            case .triage:
                if let off = TriageExample.unread("triage", title: String(localized: "Fix the sign-in test")),
                   let on = TriageExample.triaged() {
                    ExampleChange { ExampleAgentLine(session: off) } on: { ExampleAgentLine(session: on) }
                }
            case .decide:
                if let off = TriageExample.unread("decide", title: TriageExample.decideTitle) {
                    ExampleChange {
                        ExampleAgentLine(session: off)
                    } on: {
                        InteractionPreviewCard(interaction: TriageExample.decision(), inCell: true) {}.card()
                    }
                }
            case .autoContinue:
                ExampleTimeline(steps: TriageExample.continueSteps)
            case .wake:
                ExampleTimeline(steps: TriageExample.wakeSteps)
            case .replyReminder:
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    Chip(text: String(localized: "Seen, no reply for \(replyMinutes) min"), symbol: Theme.Icon.replyReminder)
                    ThemedIcon(Theme.Icon.down).font(Theme.Font.label).foregroundStyle(.tertiary)
                    PushBannerSample(title: TriageExample.reminderTitle, message: TriageExample.reminderMessage)
                }
            }
        }
    }
}
