import SwiftUI

/// Settings › Session Triage (`TriageSettingsModel`): the triage and its model, then the three things it may do, then the
/// reminder for a question seen and not answered (apart from the triage). Each switch shows under it what it changes, off
/// against on or as steps in time (`TriageExampleView`, #1413).
struct TriageSettingsView: View {
    @Environment(AppModel.self) private var app
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var model: TriageSettingsModel?

    var body: some View {
        SettingsForm {
            if let error = model?.error {
                Section {
                    Label(error, systemImage: Theme.Icon.error).font(Theme.Font.caption).foregroundStyle(Theme.danger)
                }
            }
            if model?.unsupported == true {
                Section {
                    Label("This hub doesn't support session triage yet. Update the hub, then turn it on here.", systemImage: Theme.Icon.info)
                        .font(Theme.Font.secondary).foregroundStyle(.secondary)
                }
            } else {
                Section {
                    toggle(\.enabled, String(localized: "Session Triage"), String(localized: "Each agent's row says what it is waiting for"), Theme.Icon.triage, id: "triage-enabled") {
                        TriageExampleView(kind: .triage, replyMinutes: replyMinutes)
                    }
                    ModelConfigurationLink(title: String(localized: "Triage Model"), purposeID: "agent.triage")
                } footer: {
                    Text("Reads the agent's last reply with the triage model, once per turn.")
                }
                Section {
                    toggle(\.decide, String(localized: "Cards for Your Decisions"), String(localized: "A question waiting on you becomes a card in Needs You"), Theme.Icon.askFirst, id: "triage-decide") {
                        TriageExampleView(kind: .decide, replyMinutes: replyMinutes)
                    }
                    toggle(\.autoContinue, String(localized: "Continue After Errors"), String(localized: "Sends “continue” after a temporary model error, up to 3 times"), Theme.Icon.retry, id: "triage-continue") {
                        TriageExampleView(kind: .autoContinue, replyMinutes: replyMinutes)
                    }
                    toggle(\.wake, String(localized: "Wake on Time"), String(localized: "Wakes an agent waiting on a deploy, CI or another agent to check again"), Theme.Icon.wakeUp, id: "triage-wake") {
                        TriageExampleView(kind: .wake, replyMinutes: replyMinutes)
                    }
                } header: {
                    Text("After Triage")
                } footer: {
                    Text("Need Session Triage on. Continuing and waking run the agent on its own model; pushing, deploying and messaging people always wait for you.")
                }
                Section {
                    toggle(\.replyReminder, String(localized: "Remind If Seen but Unanswered"), String(localized: "One reminder after \(replyMinutes) min without your reply. Uses no model."), Theme.Icon.replyReminder, id: "triage-reply-reminder") {
                        TriageExampleView(kind: .replyReminder, replyMinutes: replyMinutes)
                    }
                }
            }
        }
        .overlay { if model?.switches == nil && model?.error == nil && model?.unsupported != true { ProgressView() } }
        .themedGroupedPage()
        .detailTitle(String(localized: "Session Triage"))
        .navigationBarTitleDisplayMode(.inline)
        .task {
            if model == nil {
                let api = { [app] in app.api }
                model = TriageSettingsModel(deps: .init(
                    load: {
                        guard let api = api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
                        return try await api.agentTriage()
                    },
                    save: { next in
                        guard let api = api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
                        return try await api.setAgentTriage(next)
                    }))
            }
            await model?.load()
        }
        .refreshable { await model?.load() }
    }

    /// The hub's wait before Remind When Seen, Unanswered fires (`reply_reminder_minutes`, 30 when it names none).
    private var replyMinutes: Int { model?.switches?.replyReminderAfter ?? TriageSwitches.defaultReplyReminderMinutes }

    /// A switch with its example under it, in one row.
    private func toggle<Example: View>(_ key: WritableKeyPath<TriageSwitches, Bool>, _ title: String, _ hint: String, _ symbol: String,
                                       id: String, @ViewBuilder example: () -> Example) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            switchRow(key, title, hint, symbol, id: id)
            example()
        }
    }

    private func switchRow(_ key: WritableKeyPath<TriageSwitches, Bool>, _ title: String, _ hint: String, _ symbol: String,
                           id: String) -> some View {
        // One line, as its description is under it; at the accessibility sizes as many as it takes, where one line cut
        // it to its first two characters (#1796).
        SettingsSwitchRow(title: title, detail: hint, symbol: symbol, titleLines: typeSize.isAccessibilitySize ? nil : 1,
                          accessibilityID: id, isOn: Binding {
            model?.switches?[keyPath: key] ?? false
        } set: { on in
            Task { await model?.set(key, on) }
        })
        .disabled(model?.canChange(key) != true)
    }
}
