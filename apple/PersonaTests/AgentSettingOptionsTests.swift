import Testing
@testable import Persona

/// Settings › Claude Code / Codex options read in words, not their raw values (#3835).
struct AgentSettingOptionsTests {
    // Every enum option the node lists (claudeconfig.go, codexconfig.go, codex_effort.go).
    private let options: [String: [String]] = [
        "effortLevel": ["low", "medium", "high", "xhigh", "max"],
        "permissions.defaultMode": ["default", "acceptEdits", "plan", "auto", "bypassPermissions"],
        "autoUpdatesChannel": ["latest", "stable"],
        "model_reasoning_effort": ["minimal", "low", "medium", "high", "xhigh", "max", "ultra"],
        "model_reasoning_summary": ["auto", "concise", "detailed", "none"],
        "model_verbosity": ["low", "medium", "high"],
        "approval_policy": ["untrusted", "on-failure", "on-request", "never"],
        "sandbox_mode": ["read-only", "workspace-write", "danger-full-access"],
    ]

    @Test func everyOptionHasAName() {
        for (key, list) in options {
            for o in list { #expect(AgentSettingOptions.name(o, key: key) != o, "\(key)=\(o)") }
        }
    }

    @Test func namesMatchTheSessionMenuAndTheWeb() {
        #expect(AgentSettingOptions.name("xhigh", key: "effortLevel") == "很高")
        #expect(AgentSettingOptions.name("ultra", key: "model_reasoning_effort") == "极高")
        #expect(AgentSettingOptions.name("latest", key: "autoUpdatesChannel") == "最新版")
        #expect(AgentSettingOptions.name("auto", key: "permissions.defaultMode") == "自动")
        #expect(AgentSettingOptions.name("default", key: "permissions.defaultMode") == "手动确认")
        #expect(AgentSettingOptions.name("on-request", key: "approval_policy") == "它觉得需要时问")
        #expect(AgentSettingOptions.name("danger-full-access", key: "sandbox_mode") == "不设限（危险）")
    }

    @Test func unknownStaysAsWritten() {
        #expect(AgentSettingOptions.name("turbo", key: "effortLevel") == "turbo")
        #expect(AgentSettingOptions.name("high", key: "someNewKey") == "high")
    }
}
