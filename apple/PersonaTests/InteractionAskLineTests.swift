import Foundation
import Testing
@testable import Persona

/// The needs-you row's one-liner (#4393): a plan shows its first heading, a terminal dialog its question, so the
/// inbox row says what each card asks instead of only Plan Awaiting Approval / Terminal Needs You.
struct InteractionAskLineTests {
    private func interaction(
        kind: InteractionKind,
        toolName: String? = nil,
        summary: String? = nil,
        plan: String? = nil,
        questions: [Question]? = nil,
        dialog: ScreenDialog? = nil,
        screen: String? = nil
    ) -> Interaction {
        Interaction(
            id: "ix-\(kind.rawValue)", machineID: "m1", sessionID: "s1", sessionTitle: nil, cwd: nil,
            kind: kind, createdAt: nil, expiresAt: nil, toolName: toolName, toolUseID: nil, questions: questions,
            plan: plan, toolInput: nil, summary: summary, screen: screen, dialog: dialog
        )
    }

    private func question(_ text: String) -> Question {
        Question(question: text, header: nil, multiSelect: false, options: [])
    }

    @Test func planShowsItsFirstHeadingLine() {
        let ix = interaction(kind: .plan, plan: "\n## 重构登录流程\n- 先加埋点\n- 再改 UI\n")
        #expect(ix.askLine(includeToolName: false) == "重构登录流程")
        #expect(ix.askLine(includeToolName: true) == "重构登录流程")
    }

    @Test func emptyPlanFallsBackToThePlanWord() {
        let ix = interaction(kind: .plan, plan: "  \n## \n")
        #expect(ix.askLine(includeToolName: false) == PlanText.title(""))
        #expect(ix.askLine(includeToolName: false)?.isEmpty == false)
    }

    @Test func screenShowsTheDialogQuestion() {
        let dialog = ScreenDialog(question: "Do you want to proceed?", options: [], rev: "r1")
        #expect(interaction(kind: .screen, dialog: dialog).askLine(includeToolName: false) == "Do you want to proceed?")
        // No dialog the hub or the app could read: the row keeps its generic Terminal Needs You title.
        #expect(interaction(kind: .screen).askLine(includeToolName: false) == nil)
    }

    @Test func questionShowsItsFirstQuestion() {
        #expect(interaction(kind: .question, questions: [question("先做哪一步？"), question("第二个问题？")])
            .askLine(includeToolName: false) == "先做哪一步？")
        #expect(interaction(kind: .question).askLine(includeToolName: false) == nil)
    }

    @Test func permissionInboxShowsSummaryThenToolName() {
        #expect(interaction(kind: .permission, toolName: "Bash", summary: "npm test")
            .askLine(includeToolName: false) == "npm test")
        #expect(interaction(kind: .permission, toolName: "Bash", summary: nil)
            .askLine(includeToolName: false) == "Bash")
        #expect(interaction(kind: .permission, toolName: "Bash", summary: "  ")
            .askLine(includeToolName: false) == "Bash")
        #expect(interaction(kind: .permission).askLine(includeToolName: false) == nil)
    }

    @Test func permissionElsewherePrefixesTheToolName() {
        // The chat card and Mac menu bar show both, joined with the localized separator.
        let separator = String(localized: "：", comment: "Separator between a tool name and what it runs, e.g. Bash：npm test")
        #expect(interaction(kind: .permission, toolName: "Bash", summary: "npm test")
            .askLine(includeToolName: true) == "Bash" + separator + "npm test")
        #expect(interaction(kind: .permission, summary: "npm test").askLine(includeToolName: true) == "npm test")
        #expect(interaction(kind: .permission, toolName: "Bash").askLine(includeToolName: true) == "Bash")
        #expect(interaction(kind: .permission).askLine(includeToolName: true) == nil)
    }

    /// The password card (secret_run) never shows its raw tool name: the chat card's title already names it, and its
    /// summary says where and what it runs. Without a summary it has no ask line (callers fall back to its title).
    @Test func thePasswordCardShowsItsSummaryAlone() {
        let summary = "Run on Mac Studio: install the backup job\n$ sudo -S -k -p '' launchctl bootstrap system backup.plist"
        let card = interaction(kind: .permission, toolName: SecretRun.tool, summary: summary + "\n")
        #expect(card.askLine(includeToolName: true) == summary)
        #expect(card.askLine(includeToolName: false) == summary)
        let bare = interaction(kind: .permission, toolName: SecretRun.tool)
        #expect(bare.askLine(includeToolName: true) == nil)
        #expect(bare.askLine(includeToolName: false) == nil)
    }
}
