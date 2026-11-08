import Foundation
import Observation

/// GET / PUT /api/v1/bots/feishu (docs/extensions/bots.md): the Feishu bot the user talks to Persona through.
nonisolated struct FeishuBotStatus: Codable, Hashable, Sendable {
    var enabled: Bool
    var appID: String
    var domain: String
    var hasSecret: Bool
    var fromConfig: Bool
    var features: Features
    var connected: Bool
    var error: String?
    var lastEventAt: Int64?
    var botName: String?
    var owner: Owner?
    var pairing: Pairing?
    var target: String?
    var agents: [BoundAgent]?
    /// Minutes an unanswered card waits before the one nudge under it, 0 off (#6084); a hub from before leaves it out.
    var nudgeAfter: Int?

    /// What a hub from before the setting does: nudge after an hour.
    static let defaultNudgeAfter = 60
    /// The delays the picker offers; the one in force is added when it is not one of them (set through the API).
    static let nudgeChoices = [0, 15, 30, 60, 120, 240, 480]

    nonisolated struct Features: Codable, Hashable, Sendable {
        var main: Bool
        var cards: Bool
        var allCards: Bool
        var pushes: Bool
        var sync: Bool
        var agents: Bool
        /// The main conversation's tasks are told in the chat (#2768); a hub from before leaves it out: on.
        var reports: Bool = true
        /// /tasks keeps one standing "waiting for you" card (#6052). Off unless the hub says so.
        var board: Bool = false
        /// Whether the hub sent `board` at all: a hub from before doesn't know the switch, so the page hides it and the
        /// edit doesn't carry it.
        var hubKnowsBoard: Bool = false
        /// Task reports push only conclusions; a finished turn, waiting and interrupted stay in the to-do list and the
        /// briefing (#1226). Off unless the hub says so.
        var onlyConclusions: Bool = false
        /// Whether the hub sent `only_conclusions`: as with the board, a hub from before hides the switch and gets
        /// nothing of it.
        var hubKnowsOnlyConclusions: Bool = false
        /// Whether the hub sent `reports`. It came with the hub (0.1.12) that merges the switches it is sent over its own;
        /// a hub from before replaces the whole object, so an edit has to carry every switch to it.
        var hubMergesFeatures: Bool = false

        enum CodingKeys: String, CodingKey {
            case main, cards, pushes, sync, agents, reports, board
            case allCards = "all_cards"
            case onlyConclusions = "only_conclusions"
        }

        init(main: Bool, cards: Bool, allCards: Bool, pushes: Bool, sync: Bool, agents: Bool, reports: Bool = true, board: Bool? = nil,
             onlyConclusions: Bool? = nil) {
            (self.main, self.cards, self.allCards, self.pushes, self.sync, self.agents, self.reports) = (main, cards, allCards, pushes, sync, agents, reports)
            self.board = board ?? false
            hubKnowsBoard = board != nil
            self.onlyConclusions = onlyConclusions ?? false
            hubKnowsOnlyConclusions = onlyConclusions != nil
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            main = try c.decode(Bool.self, forKey: .main)
            cards = try c.decode(Bool.self, forKey: .cards)
            allCards = try c.decode(Bool.self, forKey: .allCards)
            pushes = try c.decode(Bool.self, forKey: .pushes)
            sync = try c.decode(Bool.self, forKey: .sync)
            agents = try c.decode(Bool.self, forKey: .agents)
            let reports = try c.decodeIfPresent(Bool.self, forKey: .reports)
            self.reports = reports ?? true
            hubMergesFeatures = reports != nil
            let board = try c.decodeIfPresent(Bool.self, forKey: .board)
            self.board = board ?? false
            hubKnowsBoard = board != nil
            let onlyConclusions = try c.decodeIfPresent(Bool.self, forKey: .onlyConclusions)
            self.onlyConclusions = onlyConclusions ?? false
            hubKnowsOnlyConclusions = onlyConclusions != nil
        }

        func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(main, forKey: .main)
            try c.encode(cards, forKey: .cards)
            try c.encode(allCards, forKey: .allCards)
            try c.encode(pushes, forKey: .pushes)
            try c.encode(sync, forKey: .sync)
            try c.encode(agents, forKey: .agents)
            try c.encode(reports, forKey: .reports)
            if hubKnowsBoard { try c.encode(board, forKey: .board) } // the hub merges what it is sent over what it has
            if hubKnowsOnlyConclusions { try c.encode(onlyConclusions, forKey: .onlyConclusions) }
        }

        /// The wire name of a switch.
        static func wireName(_ key: WritableKeyPath<Features, Bool>) -> String {
            if key == \.main { return "main" }
            if key == \.cards { return "cards" }
            if key == \.allCards { return "all_cards" }
            if key == \.pushes { return "pushes" }
            if key == \.sync { return "sync" }
            if key == \.agents { return "agents" }
            if key == \.reports { return "reports" }
            if key == \.onlyConclusions { return "only_conclusions" }
            return "board"
        }

        /// What turning one switch sends to a hub that merges per switch: that switch alone, so a switch changed elsewhere
        /// since this page loaded keeps its value. The two that go together name both: "all cards" brings "cards" along, and
        /// turning "cards" off takes "all cards" with it (the hub does not tie them).
        static func change(_ key: WritableKeyPath<Features, Bool>, _ on: Bool) -> [String: Bool] {
            var change = [wireName(key): on]
            if key == \.cards && !on { change["all_cards"] = false }
            if key == \.allCards && on { change["cards"] = true }
            return change
        }
    }

    nonisolated struct Owner: Codable, Hashable, Sendable {
        var name: String
        var pairedAt: Int64
        enum CodingKeys: String, CodingKey { case name; case pairedAt = "paired_at" }
    }

    nonisolated struct Pairing: Codable, Hashable, Sendable {
        var code: String
        var expiresAt: Int64
        enum CodingKeys: String, CodingKey { case code; case expiresAt = "expires_at" }
    }

    nonisolated struct BoundAgent: Codable, Hashable, Sendable {
        var title: String
        var machineID: String
        var sessionID: String
        enum CodingKeys: String, CodingKey { case title; case machineID = "machine_id", sessionID = "session_id" }
    }

    enum CodingKeys: String, CodingKey {
        case enabled, domain, features, connected, error, owner, pairing, target, agents
        case nudgeAfter = "nudge_after"
        case appID = "app_id", hasSecret = "has_secret", fromConfig = "from_config", lastEventAt = "last_event_at", botName = "bot_name"
    }
}

/// The edit sent to PUT /api/v1/bots/feishu: only what is set changes.
nonisolated struct FeishuBotUpdate: Encodable, Sendable {
    var enabled: Bool?
    var appID: String?
    var appSecret: String?
    var domain: String?
    var features: FeaturesEdit?
    var nudgeAfter: Int?

    /// The switches in an edit: the ones that changed, or (for a hub from before it merged them) all of them.
    nonisolated enum FeaturesEdit: Encodable, Sendable {
        case only([String: Bool])
        case whole(FeishuBotStatus.Features)

        func encode(to encoder: Encoder) throws {
            var c = encoder.singleValueContainer()
            switch self {
            case .only(let keys): try c.encode(keys)
            case .whole(let all): try c.encode(all)
            }
        }
    }

    enum CodingKeys: String, CodingKey {
        case enabled, domain, features
        case nudgeAfter = "nudge_after"
        case appID = "app_id", appSecret = "app_secret"
    }
}

extension APIClient {
    func feishuBot() async throws -> FeishuBotStatus { try await get("/api/v1/bots/feishu") }
    func updateFeishuBot(_ u: FeishuBotUpdate) async throws -> FeishuBotStatus {
        try JSON.decoder.decode(FeishuBotStatus.self, from: await request("PUT", "/api/v1/bots/feishu", body: u, timeout: 40))
    }
    func pairFeishuBot() async throws -> FeishuBotStatus { try await post("/api/v1/bots/feishu/pair") }
    func unpairFeishuBot() async throws -> FeishuBotStatus {
        try JSON.decoder.decode(FeishuBotStatus.self, from: await request("DELETE", "/api/v1/bots/feishu/owner"))
    }
    func testFeishuBot() async throws { try await post("/api/v1/bots/feishu/test") }
}

/// Settings › Connectors › Lark Bot: load the bot's state, edit the credentials and switches, pair. Each change goes to the hub
/// at once and the page shows what the hub answered; a failed change leaves the page as it was.
@Observable final class FeishuBotModel {
    struct Deps {
        var api: () -> APIClient?
        var toast: (Toast) -> Void

        static var live: Deps {
            let model = AppModel.shared
            return Deps(api: { model.api }, toast: { model.toast = $0 })
        }
    }

    private(set) var status: FeishuBotStatus?
    private(set) var loading = false
    private(set) var saving = false
    private(set) var error: String?
    /// The credentials being typed (the secret is never read back from the hub).
    var appID = ""
    var appSecret = ""
    var domain = "feishu"
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    var features: FeishuBotStatus.Features {
        status?.features ?? .init(main: true, cards: true, allCards: false, pushes: false, sync: false, agents: false)
    }

    /// The nudge delay in force: an hour for a hub that doesn't say.
    var nudgeAfter: Int { status?.nudgeAfter ?? FeishuBotStatus.defaultNudgeAfter }

    /// Whether the hub has the "nudge" setting (a hub from before the setting leaves it out of its status).
    var hubKnowsNudge: Bool { status?.nudgeAfter != nil }

    /// Whether the hub has the "board" switch.
    var hubKnowsBoard: Bool { status?.features.hubKnowsBoard == true }

    /// Whether the hub has the "only conclusions" switch.
    var hubKnowsOnlyConclusions: Bool { status?.features.hubKnowsOnlyConclusions == true }

    /// The delays to pick from, with the one in force among them.
    var nudgeChoices: [Int] {
        let usual = FeishuBotStatus.nudgeChoices
        return usual.contains(nudgeAfter) ? usual : (usual + [nudgeAfter]).sorted()
    }

    /// "Off", "30 min", "2 h".
    static func nudgeLabel(_ minutes: Int) -> String {
        if minutes <= 0 { return String(localized: "Off [Feishu Bot Model 3f9d21]") }
        return minutes % 60 == 0 ? String(localized: "\(minutes / 60) h") : String(localized: "\(minutes) min")
    }

    /// The domain picker's choice for what the hub resolved.
    static func domainChoice(_ resolved: String) -> String {
        if resolved.contains("larksuite") { return "lark" }
        if resolved.isEmpty || resolved.contains("open.feishu.cn") { return "feishu" }
        return resolved
    }

    var credentialsChanged: Bool {
        guard let st = status else { return !appID.isEmpty }
        return appID.trimmingCharacters(in: .whitespaces) != st.appID || !appSecret.isEmpty || domain != Self.domainChoice(st.domain)
    }

    func load() async {
        guard let api = deps.api() else { return }
        loading = true
        defer { loading = false }
        do {
            adopt(try await api.feishuBot())
            error = nil
        } catch {
            self.error = error.shown.map { String(localized: "Couldn't get the bot's status: \($0)") }
        }
    }

    private func adopt(_ st: FeishuBotStatus) {
        let firstOrSame = status == nil || !credentialsChanged
        status = st
        if firstOrSame { // don't overwrite what is being typed
            appID = st.appID
            domain = Self.domainChoice(st.domain)
            appSecret = ""
        }
    }

    @discardableResult
    private func run(_ what: String, _ call: (APIClient) async throws -> FeishuBotStatus) async -> Bool {
        guard let api = deps.api() else { return false }
        saving = true
        defer { saving = false }
        do {
            let st = try await call(api)
            status = nil // what the hub has now, credentials included
            adopt(st)
            error = nil
            return true
        } catch {
            self.error = error.shown.map { String(localized: "\(what): \($0)", comment: "What failed, and why") }
            return false
        }
    }

    func saveCredentials() async {
        let id = appID.trimmingCharacters(in: .whitespaces)
        let secret = appSecret.trimmingCharacters(in: .whitespaces)
        if await run(String(localized: "Couldn't save"), { try await $0.updateFeishuBot(.init(appID: id, appSecret: secret.isEmpty ? nil : secret, domain: domain)) }) {
            deps.toast(Toast(String(localized: "App credentials verified and saved"), style: .success))
        }
    }

    func setEnabled(_ on: Bool) async {
        await run(on ? String(localized: "Couldn't turn on") : String(localized: "Couldn't turn off")) { try await $0.updateFeishuBot(.init(enabled: on)) }
    }

    /// One switch. The hub merges the switches it is sent over its own, so only the changed one goes (this page may
    /// have been open while another device changed a different one). A hub from before that replaces the whole object
    /// gets every switch, as it always did.
    func set(_ key: WritableKeyPath<FeishuBotStatus.Features, Bool>, _ on: Bool) async {
        let edit: FeishuBotUpdate.FeaturesEdit
        if features.hubMergesFeatures {
            edit = .only(FeishuBotStatus.Features.change(key, on))
        } else {
            var f = features
            f[keyPath: key] = on
            if key == \.cards && !on { f.allCards = false }
            if key == \.allCards && on { f.cards = true }
            edit = .whole(f)
        }
        await run(String(localized: "Couldn't save")) { try await $0.updateFeishuBot(.init(features: edit)) }
    }

    func setNudgeAfter(_ minutes: Int) async {
        await run(String(localized: "Couldn't save")) { try await $0.updateFeishuBot(.init(nudgeAfter: minutes)) }
    }

    func pair() async { await run(String(localized: "Couldn't get a pairing code")) { try await $0.pairFeishuBot() } }

    func unpair() async {
        if await run(String(localized: "Couldn't unpair"), { try await $0.unpairFeishuBot() }) { deps.toast(Toast(String(localized: "Unpaired"), style: .success)) }
    }

    func test() async {
        guard let api = deps.api() else { return }
        saving = true
        defer { saving = false }
        do {
            try await api.testFeishuBot()
            deps.toast(Toast(String(localized: "Sent a test message to Lark"), style: .success))
            error = nil
        } catch {
            self.error = error.shown.map { String(localized: "Test message not sent: \($0)") }
        }
    }

    /// What the connection row says.
    var stateText: String {
        guard let st = status else { return loading ? String(localized: "Checking…") : String(localized: "Couldn't get status") }
        if !st.enabled { return String(localized: "Off [Connectors Views a0a4c7]") }
        if !st.hasSecret { return String(localized: "App credentials not entered yet") }
        if !st.connected { return st.error.map { String(localized: "Can't connect: \($0)") } ?? String(localized: "Connecting… [Feishu Bot Model 8be0ea]") }
        return st.owner == nil ? String(localized: "Connected; waiting for you to pair") : String(localized: "Connected [V N C Page 5be032]")
    }

    /// The settings list's one-word summary.
    static func summary(_ st: FeishuBotStatus?) -> String {
        guard let st else { return "" }
        if !st.enabled { return String(localized: "Off [Connectors Views a0a4c7]") }
        if !st.connected { return String(localized: "Can't connect") }
        return st.owner == nil ? String(localized: "Not paired") : String(localized: "Connected [V N C Page 5be032]")
    }
}
