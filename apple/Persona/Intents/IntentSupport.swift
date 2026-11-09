import AppIntents
import Foundation

// Siri / Shortcuts entry points. Every intent talks to the hubs directly (HubStore + APIClient), so the app does not
// have to be in the foreground; only OpenSessionIntent runs inside the app and moves its navigation. With several
// groups of hubs (docs/protocol/multi-hub.md「Siri」) they see every group: machines and sessions carry their group, the status
// adds the groups up, and whatever acts goes to the group of what it acts on.

/// Everything the intents say out loud. Pure functions, so the tests can pin the wording without a hub.
nonisolated enum IntentText {
    /// Keys the new-chat screen already writes (see NewChatView's AppStorage).
    static let machineKey = "newChat.machine"
    static func launcherKey(_ machine: String) -> String { "launcher.\(machine)" }
    static func cwdKey(_ machine: String) -> String { "cwd.\(machine)" }
    /// The hub group the app has selected (`AppModel.hubGroupKey`): where the todos page reads and writes.
    static let hubGroupKey = "hub.group"

    static func sent(to title: String) -> String {
        let name = title.trimmingCharacters(in: .whitespacesAndNewlines)
        // The untitled case is its own sentence, so 「会话」 is not translated as a bare noun.
        return name.isEmpty ? String(localized: "Sent to the session") : String(localized: "Sent to \(name)")
    }

    static func status(working: Int, waiting: Int, first: String?) -> String {
        let head = String(localized: "\(working) working, \(waiting) waiting for you")
        guard let first, !first.isEmpty else { return head }
        return String(localized: "\(head):\\n“\(excerpt(first, limit: 80))”")
    }

    /// One line describing what is waiting, for the status dialog.
    static func waitingPrompt(_ ix: Interaction) -> String {
        switch ix.kind {
        case .question:
            if let q = ix.questions?.first?.question, !q.isEmpty { return q }
            return ix.summary ?? String(localized: "A question is waiting for you")
        case .plan:
            // Plans are markdown documents; a dialog needs one line.
            if let plan = ix.plan ?? ix.summary, !plan.isEmpty { return String(localized: "Plan: \(excerpt(plan, limit: 120))") }
            return String(localized: "A plan is waiting for your approval")
        case .permission:
            if let s = ix.summary, !s.isEmpty { return s }
            if let tool = ix.toolName, !tool.isEmpty { return String(localized: "\(tool) is asking for permission") }
            return String(localized: "A permission request is waiting for you")
        case .screen:
            return String(localized: "The terminal is waiting for your input")
        }
    }

    static func approvedPermission(_ tool: String?) -> String {
        guard let tool, !tool.isEmpty else { return String(localized: "Allowed [Intent Support 75d889]") }
        return String(localized: "Allowed \(tool)")
    }

    static let approvedPlan = String(localized: "Plan approved. Starting now.")

    /// Only permission and plan can be settled without looking at the screen.
    static func needsApp(_ kind: InteractionKind) -> String {
        switch kind {
        case .question: String(localized: "This is a question. Open Persona to answer it.")
        case .screen: String(localized: "The terminal is waiting for your input. Open Persona.")
        case .plan, .permission: String(localized: "Open Persona to handle this.")
        }
    }

    /// Collapses whitespace and clips, so a plan or a question fits in a Siri dialog.
    static func excerpt(_ text: String, limit: Int = 60) -> String {
        let flat = text.split(whereSeparator: \.isWhitespace).joined(separator: " ")
        guard flat.count > limit else { return flat }
        return String(flat.prefix(limit)) + "…"
    }
}

/// Shortcut wording. The provider spells the same sentences out as `AppShortcutPhrase` interpolations (the system
/// needs the `\(.applicationName)` token there); these templates keep the readable version testable.
nonisolated enum IntentPhrases {
    static let askClaude = "用 %@ 问 Claude" // i18n-ignore
    static let status = "%@ 状态" // i18n-ignore
    static let approve = "用 %@ 批准第一个" // i18n-ignore
    static let addTodo = "用 %@ 新建待办" // i18n-ignore

    static func fill(_ template: String, appName: String) -> String {
        template.replacingOccurrences(of: "%@", with: appName)
    }
}

/// Failures an intent can explain in one Chinese sentence.
nonisolated enum IntentFailure: LocalizedError {
    case notPaired
    case noMachine
    case offline(String)
    case noSession(String)
    case noLauncher(String)
    case noPending

    var errorDescription: String? { sentence }

    var sentence: String {
        switch self {
        case .notPaired:
            String(localized: "Not paired yet. Open Persona and scan the QR code your hub shows.")
        case .noMachine:
            String(localized: "No computer is online right now. Check that your computer is on and Persona Link is running.")
        case .offline(let name):
            String(localized: "\(name) is offline: it may be asleep, or Persona Link isn't running on it.")
        case .noSession(let name):
            String(localized: "No sessions are running on \(name). Open Persona to start one, or say “new session”.")
        case .noLauncher(let name):
            String(localized: "You haven't started a new chat on \(name) yet. Start one in Persona once, and Siri will know which folder and launcher to use.")
        case .noPending:
            String(localized: "Nothing needs you right now.")
        }
    }

    /// Maps anything thrown while talking to the hub onto a sentence Siri can read out.
    static func sentence(for error: Error, host: String? = nil) -> String {
        if let failure = error as? IntentFailure { return failure.sentence }
        if let api = error as? APIError {
            switch api.status {
            case 401, 403: return String(localized: "The pairing has expired. Open Persona to pair again.")
            case 410: return String(localized: "This was already handled.")
            default: return api.message
            }
        }
        let whereHub = host.map { String(localized: " (\($0))", comment: "Hub host appended to a connection error") } ?? ""
        if let url = error as? URLError {
            switch url.code {
            case .timedOut: return String(localized: "The hub isn't responding\(whereHub). Try again later.")
            case .notConnectedToInternet, .networkConnectionLost: return String(localized: "Your phone is offline\(whereHub).")
            default: return String(localized: "Can't reach the hub\(whereHub). Make sure your phone can reach it (same network or VPN).")
            }
        }
        if (error as NSError).domain == NSURLErrorDomain {
            return String(localized: "Can't reach the hub\(whereHub). Make sure your phone can reach it (same network or VPN).")
        }
        return error.localizedDescription
    }
}

nonisolated enum IntentSupport {
    /// Fresh clients every call, one per group (its first hub): the pairing can change while the app is running.
    static func clients() throws -> [APIClient] {
        let registry = HubStore.load()
        let out = registry.groups.compactMap { g in registry.primary(g).map {
            var client = APIClient(config: $0.config, group: g)
            client.clientMetadata = RequestClientMetadata(surface: .intent)
            return client
        } }
        guard !out.isEmpty else { throw IntentFailure.notPaired }
        return out
    }

    /// The client of one group (a machine's, a session's); the first group's when it is gone.
    static func client(group: String = "") throws -> APIClient {
        let all = try clients()
        return all.first { $0.group == group } ?? all[0]
    }

    /// Every group's machines; with several groups each named with its group ("MacBook · Work").
    static func machines() async throws -> [Machine] {
        let all = try clients()
        let registry = HubStore.load()
        var out: [Machine] = []
        for api in all {
            let list = try await api.machines()
            out += all.count > 1 ? list.map { var m = $0; m.groupLabel = registry.name(api.group); return m } : list
        }
        return out
    }

    /// Every group's sessions, or one group's.
    static func sessions(group: String? = nil) async throws -> [Session] {
        var out: [Session] = []
        for api in try clients() where group == nil || api.group == group { out += try await api.sessions() }
        return out
    }

    /// Every group's pending interactions.
    static func interactions() async throws -> [Interaction] {
        var out: [Interaction] = []
        for api in try clients() { out += try await api.interactions() }
        return out
    }

    /// Every group's muted spaces and agents (`GET /api/v1/mutes`), by group. A hub that does not answer mutes nothing
    /// here; its cards still carry the hub's own `muted` stamp (#4360).
    static func mutes() async throws -> [String: Pins] {
        var out: [String: Pins] = [:]
        for api in try clients() { out[api.group] = try? await api.mutes() }
        return out
    }

    static func host() -> String? { HubStore.load().hubs.first?.config.url.host() }

    /// A dialog built from runtime text; `IntentDialog`'s literal initializer only takes compile-time strings.
    static func dialog(_ text: String) -> IntentDialog { IntentDialog("\(text)") }
}

/// Selection rules shared by the intents. Pure, so the tests can drive them with decoded models.
nonisolated enum IntentPicker {
    /// The remembered machine if it still exists, otherwise the first online one. `preferred` is what the new-chat
    /// sheet remembers: a machine id, or "group|id" for another group (`MachineRef.path`).
    static func machine(_ machines: [Machine], preferred: String?) -> Machine? {
        if let preferred, let match = machines.first(where: { $0.ref.path == preferred }) { return match }
        return machines.first(where: \.online) ?? machines.first
    }

    /// The most recently updated session on a machine that is not offline.
    static func session(_ sessions: [Session], machine: String, group: String = "") -> Session? {
        sessions
            .filter { $0.machineID == machine && $0.group == group && $0.status != .offline }
            .max { ($0.updatedAt ?? .distantPast) < ($1.updatedAt ?? .distantPast) }
    }

    /// Oldest first: the one that has been waiting longest.
    static func pending(_ interactions: [Interaction]) -> Interaction? {
        interactions.min { ($0.createdAt ?? .distantPast) < ($1.createdAt ?? .distantPast) }
    }

    /// The cards the user sees waiting, by the tab badges' rule (`TabBadges.place`): the Agents tab's and Persona's
    /// own. A quiet task's agent (the hub's `quiet`, #1731) is the main conversation's to tell, and a muted agent (it
    /// or its space, `mutes` by group, or the hub's `muted` stamp on the card) is not shown anywhere. 「Persona 状态」
    /// reads from these and 「批准第一个待处理」 settles from these, so Siri never approves a card it did not read
    /// out (#5257).
    static func visible(_ interactions: [Interaction], sessions: [Session], mutes: [String: Pins]) -> [Interaction] {
        let byKey = Dictionary(sessions.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        return interactions.filter { ix in
            let s = byKey[ix.sessionKey]
            switch TabBadges.place(ix.sessionKey, session: s, quiet: s?.quiet == true, mutes: mutes[ix.group]) {
            case .assistant: return true
            case .agents: return !ix.muted
            case .quiet, .muted: return false
            }
        }
    }

    /// How many agents 「Persona 状态」 says are working, as the island counts them (#180): the Agents tab's at work by
    /// the hub's `state` (not herdr's raw status). Persona's own conversations, quiet tasks' agents and muted ones are
    /// left out, by the same rule as the waiting cards (`TabBadges.place`).
    static func working(_ sessions: [Session], mutes: [String: Pins]) -> Int {
        sessions.filter { s in
            s.state == .working && TabBadges.place(s.key, session: s, quiet: s.quiet, mutes: mutes[s.group]) == .agents
        }.count
    }

    /// What Siri reads out as waiting and the first of it: 「Persona 状态」 says how many and reads `first`;
    /// 「批准第一个待处理」 settles that same `first` (#5257).
    static func waiting(_ interactions: [Interaction], sessions: [Session], mutes: [String: Pins])
        -> (cards: [Interaction], first: Interaction?) {
        let cards = visible(interactions, sessions: sessions, mutes: mutes)
        return (cards, pending(cards))
    }
}
