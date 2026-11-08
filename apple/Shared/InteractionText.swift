import Foundation

/// The first heading line of an agent's plan: the first non-empty line without Markdown markers
/// ("### Plan" -> "Plan"), what every card and the Mac menu bar title shows. An empty plan is Plan.
nonisolated enum PlanText {
    static func title(_ plan: String) -> String {
        for line in plan.split(separator: "\n") {
            let t = line.trimmingCharacters(in: .whitespaces).trimmingCharacters(in: CharacterSet(charactersIn: "#* "))
            if !t.isEmpty { return t }
        }
        return String(localized: "Plan [Interaction Text 2619dd]")
    }
}

/// The assistant's password card (`secret_run`, docs/assistant/assistant.md): commands the Agent proposes for one of the
/// user's computers, run by that computer's node with a password typed into the card on the iPhone.
nonisolated enum SecretRun {
    /// The tool's name on the card (`tool_name`), and the base name of its call in the chat (`mcp__persona__secret_run`).
    static let tool = "secret_run"
}

nonisolated extension Interaction {
    /// The password card. Its raw tool name is never shown: its title names it, its summary says what it runs.
    var isSecretRun: Bool { kind == .permission && toolName == SecretRun.tool }
}

nonisolated extension Interaction {
    /// One line on what a needs-you card asks, chosen by its kind — the same wording on the chat card, the inbox's
    /// Needs You row and the Mac menu bar: a plan's first heading, a terminal dialog's question, a question card's
    /// first question, and a permission card's tool with what it runs.
    /// - Parameter includeToolName: a permission card shows `toolName: summary`; pass false to show the summary
    ///   only and fall back to the tool name, as the inbox row does (its next line already names the card kind).
    ///   The password card (`isSecretRun`) shows its summary alone either way.
    /// - Returns: nil when the card carries no such text; the caller falls back to its generic title.
    func askLine(includeToolName: Bool) -> String? {
        switch kind {
        case .question:
            return questions?.first?.question
        case .plan:
            return PlanText.title(plan ?? "")
        case .permission:
            if includeToolName, !isSecretRun {
                let line = [toolName, summary].compactMap { $0 }.joined(separator: permissionSeparator)
                return line.isEmpty ? nil : line
            }
            // The inbox row: trim whitespace-only summaries so it never draws a blank asks line.
            if let s = summary?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty { return s }
            if let t = toolName?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty, !isSecretRun { return t }
            return nil
        case .screen:
            return screenDialog?.question
        }
    }

    private var permissionSeparator: String {
        // Same separator on the chat card, the inbox row and the Mac menu bar.
        String(localized: ": ", comment: "Separator between a tool name and what it runs, e.g. Bash：npm test")
    }
}

nonisolated extension Interaction {
    /// A card on no session (#4059): `personad ask` run outside herdr, or before the pane's agent hooked in, has no
    /// session the node could tie it to (docs/integrations/script-cli.md「归属」).
    var isSessionless: Bool { sessionID.isEmpty }

    /// Whether the app may answer it: a sessionless card always (no session source says no); else as its session's source
    /// allows (`canApprove`; a read-only Codex history does not), and not while that session is unknown.
    func canAnswer(session: Session?) -> Bool { isSessionless || session?.canApprove == true }

    /// Where a sessionless card comes from, for its list row: the title the node gave it, else its directory's name.
    var sessionlessPlace: String? {
        if let t = sessionTitle?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty { return t }
        let dir = cwd.map { URL(fileURLWithPath: $0).lastPathComponent } ?? ""
        return dir.isEmpty || dir == "/" ? nil : dir
    }
}
