import AppIntents

/// Siri / Spotlight shortcuts. Every phrase has to include `\(.applicationName)`; only AppEntity/AppEnum
/// parameters may be interpolated, which is why the message text is asked for after the phrase matches.
struct PersonaShortcuts: AppShortcutsProvider {
    nonisolated static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: TalkToPersonaIntent(),
            phrases: [
                "Have a chat with \(.applicationName)",
                "Start a chat with \(.applicationName)",
                "Talk to \(.applicationName)",
                "Chat with \(.applicationName)",
            ],
            shortTitle: "Talk to Persona",
            systemImageName: "waveform.badge.mic" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AskPersonaIntent(),
            phrases: [
                "Ask \(.applicationName) something",
                "Quick question for \(.applicationName)",
                "Ask \(.applicationName)",
                "Ask \(.applicationName) a question",
            ],
            shortTitle: "Ask Persona",
            systemImageName: "sparkles" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AgentTaskIntent(),
            phrases: [
                "Give an agent a task in \(.applicationName)",
                "Ask \(\.$agent) in \(.applicationName) to do something",
                "Hand off to an agent in \(.applicationName)",
                "Have \(.applicationName) agent do something",
                "Send a task to \(\.$agent) with \(.applicationName)",
            ],
            shortTitle: "Give an Agent a Task",
            systemImageName: "person.crop.circle.badge.checkmark" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AgentsStatusIntent(),
            phrases: [
                "Unread from \(.applicationName) personal agents",
                "What's new from \(.applicationName) agents",
                "Check \(.applicationName) agents",
            ],
            shortTitle: "Agent Unread",
            systemImageName: "tray.full" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AskClaudeIntent(),
            phrases: [
                "Ask Claude in \(.applicationName)",
                "Ask Claude with \(.applicationName)",
                "\(.applicationName), ask Claude",
                "Quick question for Claude in \(.applicationName)",
            ],
            shortTitle: "Ask Claude [Ask Claude Intent 946979]",
            systemImageName: "bubble.left.and.text.bubble.right" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: PersonaStatusIntent(),
            phrases: [
                "Status of \(.applicationName)",
                "\(.applicationName) status",
                "How's \(.applicationName) doing",
                "Check progress in \(.applicationName)",
            ],
            shortTitle: "Status",
            systemImageName: "gauge.with.dots.needle.33percent" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: ApproveFirstIntent(),
            phrases: [
                "Approve the first one in \(.applicationName)",
                "Approve pending in \(.applicationName)",
            ],
            shortTitle: "Approve First",
            systemImageName: "checkmark.shield" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: RunExtensionActionIntent(),
            phrases: ["Run an extension action in \(.applicationName)"],
            shortTitle: "Run Extension Action",
            // AppShortcuts metadata requires a literal; mirrored from ExtensionSurfaceTokens.actionIcon.
            systemImageName: "puzzlepiece.extension" // icon: App Shortcuts take only a literal
        )
        AppShortcut(
            intent: AddTodoIntent(),
            phrases: [
                "Create a to-do in \(.applicationName)",
                "Note a to-do in \(.applicationName)",
                "Add to-do to \(.applicationName)",
                "Add a to-do in \(.applicationName)",
                "New \(.applicationName) to-do",
            ],
            shortTitle: "New To-Do",
            systemImageName: "checklist" // icon: App Shortcuts take only a literal
        )
        #if os(iOS)
        AppShortcut(
            intent: StartMeetingRecordingIntent(),
            phrases: [
                "Record a meeting with \(.applicationName)",
                "Record this meeting with \(.applicationName)",
                "Start a meeting recording in \(.applicationName)",
                "\(.applicationName), record the meeting",
            ],
            shortTitle: "Record a Meeting",
            systemImageName: "waveform.circle" // icon: App Shortcuts take only a literal (Theme.Icon.recording)
        )
        #endif
    }

    nonisolated static var shortcutTileColor: ShortcutTileColor { .tangerine }
}
