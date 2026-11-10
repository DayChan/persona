import Foundation

/// Settings › Claude Code / Codex: an enum option's name, by the setting's key (node claudeconfig.go / codexconfig.go), the
/// same names as the web's (`web/src/lib/agent-settings.ts` `optionText`, #3835). Effort levels read as the session menu
/// calls them (`SessionModes`). An option with no name here (a new one) stays as the node wrote it. The names are in the
/// app's language (Localizable.xcstrings, the web's wording in en and ja, #4793).
nonisolated enum AgentSettingOptions {
    private static let names: [String: [String: String]] = [
        "effortLevel": Dictionary(uniqueKeysWithValues: SessionModes.effortLevels.map { ($0.id, $0.title) }),
        "permissions.defaultMode": Dictionary(uniqueKeysWithValues: SessionModes.permissionModes.map { ($0.id, $0.title) }),
        "autoUpdatesChannel": [
            "latest": String(localized: "Latest", comment: "Claude Code update channel"),
            "stable": String(localized: "Stable", comment: "Claude Code update channel"),
        ],
        "model_reasoning_effort": Dictionary(uniqueKeysWithValues: SessionModes.codexEffortLevels.map { ($0.id, $0.title) }),
        "model_reasoning_summary": [
            "auto": String(localized: "Auto", comment: "Codex reasoning summary"),
            "concise": String(localized: "Short", comment: "Codex reasoning summary; also a verbosity"),
            "detailed": String(localized: "Detailed", comment: "Codex reasoning summary; also a verbosity"),
            "none": String(localized: "None [Agent Setting Options fc3ffd]", comment: "Codex reasoning summary"),
        ],
        "model_verbosity": [
            "low": String(localized: "Short", comment: "Codex reasoning summary; also a verbosity"),
            "medium": String(localized: "Medium [Agent Setting Options fe4e7b]", comment: "Codex verbosity"),
            "high": String(localized: "Detailed", comment: "Codex reasoning summary; also a verbosity"),
        ],
        "approval_policy": [
            "untrusted": String(localized: "Ask unless it's clearly safe", comment: "Codex approval policy"),
            "on-failure": String(localized: "Ask when something fails", comment: "Codex approval policy"),
            "on-request": String(localized: "Ask when it needs to", comment: "Codex approval policy"),
            "never": String(localized: "Never ask", comment: "Codex approval policy"),
        ],
        "sandbox_mode": [
            "read-only": String(localized: "Read only", comment: "Codex sandbox mode"),
            "workspace-write": String(localized: "Can change the working folder", comment: "Codex sandbox mode"),
            "danger-full-access": String(localized: "No limits (risky)", comment: "Codex sandbox mode"),
        ],
    ]

    static func name(_ option: String, key: String) -> String {
        names[key]?[option] ?? option
    }
}
