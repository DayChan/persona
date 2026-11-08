import Foundation

/// How Persona's own MCP tools (server `persona`: `mcp__persona__agent_read`…) are named in a run of steps.
///
/// The server's wire name is `persona`. Common tools get a verb of their own ("Read agent chat"); the rest read like the web's rows,
/// "Call Persona · agent read", never the raw `agent_read`.
enum PersonaToolNames {
    /// The MCP server name for Persona itself.
    nonisolated static let servers: Set<String> = ["persona", LegacyNames.product, "plugin_persona", "plugin_" + LegacyNames.product]

    /// The server of an MCP tool's full name ("mcp__persona__agent_read" → "persona"), nil for any other tool.
    nonisolated static func server(_ name: String) -> String? {
        guard name.hasPrefix("mcp__") else { return nil }
        let rest = name.dropFirst(5)
        guard let r = rest.range(of: "__", options: .backwards), r.lowerBound > rest.startIndex else { return nil }
        return String(rest[..<r.lowerBound])
    }

    nonisolated static func isPersona(_ name: String) -> Bool {
        server(name).map { servers.contains($0) } ?? false
    }

    /// The verb for one of Persona's tools, nil for any other tool and for the ones `ToolPresentation.verb` already
    /// names (conversations, computers, browsers, phone, mail).
    static func verb(_ name: String) -> String? {
        guard isPersona(name) else { return nil }
        let n = ToolPresentation.baseName(name)
        if let own = verbs(n) { return own }
        // Already named by the generic table: its bare name comes back as something other than itself.
        if ToolPresentation.verb(n) != n { return nil }
        return fallback(n)
    }

    /// "Call Persona · agent read".
    static func fallback(_ base: String) -> String {
        let label = base.replacingOccurrences(of: "_", with: " ")
        return String(localized: "Call Persona · \(label)")
    }

    private static func verbs(_ n: String) -> String? {
        switch n {
        // Agents on the user's computers (docs/assistant/assistant.md「Agents 与飞书工具」).
        case "agent_read": String(localized: "Read Agent Chat")
        case "agent_send", "send_to_agent": String(localized: "Message Agent")
        case "agents_list", "list_agents": String(localized: "List Agents")
        case "agent_start", "task_plan": String(localized: "Assign Task")
        // Commands on one of the user's computers with a password typed into a card (SecretRun).
        case SecretRun.tool: String(localized: "Run With Password")
        case "interaction_answer": String(localized: "Answer for You")
        case "ask_persona", "ask_" + LegacyNames.product: String(localized: "Hand to Persona")
        // The knowledge base (docs/assistant/knowledge.md).
        case "kb_get", "kb_list", "kb_search", "search_knowledge": String(localized: "Search Knowledge")
        case "kb_update": String(localized: "Update Knowledge")
        // Talking to the user.
        case "ask_user": String(localized: "Ask You")
        case "notify_user": String(localized: "Notify You")
        case "schedule_followup": String(localized: "Reminder [Add Todo Intent 8f29b3]")
        case "list_followups": String(localized: "List Reminders")
        case "cancel_followup": String(localized: "Cancel Reminder")
        case "schedule_routine": String(localized: "Schedule Task")
        case "delete_routine": String(localized: "Delete Scheduled Task [Persona Tool Names 24bbfb]")
        case "inbox_draft": String(localized: "Draft Reply")
        case "persona_settings", LegacyNames.product + "_settings": String(localized: "Persona Settings")
        // Live cards (docs/assistant/assistant-views.md).
        case "view_show": String(localized: "Draw Card")
        case "view_get": String(localized: "Read Card")
        case "view_update": String(localized: "Update Card")
        default: nil
        }
    }
}
