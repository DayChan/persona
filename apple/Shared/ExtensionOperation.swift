import Foundation

// An extension being installed, upgraded or removed on the hub, step by step
// (docs/protocol/protocol.md「扩展的安装、升级、卸载进度」, #3277, #3307): the hub runs it in the background and pushes the whole operation in an
// `extension_operation` event every time it moves on. Only with a hub that says `extension_operations`; an older hub
// answers the request itself when it is done. Unknown states and steps are kept as they come: a newer hub may add some.

/// One step of an operation (download, verify, unpack, check, consent, tools, install, start, nodes; stop, remove).
nonisolated struct ExtensionOperationStep: Codable, Hashable, Sendable, Identifiable {
    enum State: String, Codable, Sendable {
        case pending, running, done, failed, skipped

        /// A state this app does not know reads as still going.
        init(from decoder: Decoder) throws {
            self = State(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .running
        }
    }

    /// The computers the `nodes` step sends the extension's program to: done of total, and why some failed.
    struct Machines: Codable, Hashable, Sendable {
        var done: Int
        var total: Int
        var errors: [String: String]?

        init(done: Int, total: Int, errors: [String: String]? = nil) {
            self.done = done
            self.total = total
            self.errors = errors
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            done = try c.decodeIfPresent(Int.self, forKey: .done) ?? 0
            total = try c.decodeIfPresent(Int.self, forKey: .total) ?? 0
            errors = try? c.decodeIfPresent([String: String].self, forKey: .errors)
        }
    }

    /// One command-line program of the `tools` step (#4507): pending, running, done or failed; `reused` when it is the
    /// hub machine's own copy (nothing downloaded).
    struct Tool: Codable, Hashable, Sendable, Identifiable {
        var name: String
        var state: State
        var reused: Bool?
        var id: String { name }

        init(name: String, state: State, reused: Bool? = nil) {
            self.name = name
            self.state = state
            self.reused = reused
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
            state = try c.decodeIfPresent(State.self, forKey: .state) ?? .pending
            reused = try? c.decodeIfPresent(Bool.self, forKey: .reused)
        }
    }

    var id: String
    var state: State
    /// The download's bytes so far and of the whole (the total can be unknown); for `tools`, of the program downloading.
    var bytes: Int64?
    var total: Int64?
    /// The package came from the hub's cache: nothing downloaded.
    var cached: Bool?
    var machines: Machines?
    /// The `tools` step's programs, in the order they install.
    var tools: [Tool]?
    var startedAt: Date?
    var endedAt: Date?

    /// How far the step is, 0…1; nil: no bar, or one that cannot say how far.
    var fraction: Double? {
        guard let bytes, let total, total > 0 else { return nil }
        return min(max(Double(bytes) / Double(total), 0), 1)
    }

    enum CodingKeys: String, CodingKey {
        case id, state, bytes, total, cached, machines, tools
        case startedAt = "started_at", endedAt = "ended_at"
    }

    init(id: String, state: State, bytes: Int64? = nil, total: Int64? = nil, cached: Bool? = nil, machines: Machines? = nil,
         tools: [Tool]? = nil) {
        self.tools = tools
        self.id = id
        self.state = state
        self.bytes = bytes
        self.total = total
        self.cached = cached
        self.machines = machines
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        state = try c.decodeIfPresent(State.self, forKey: .state) ?? .pending
        bytes = try? c.decodeIfPresent(Int64.self, forKey: .bytes)
        total = try? c.decodeIfPresent(Int64.self, forKey: .total)
        cached = try? c.decodeIfPresent(Bool.self, forKey: .cached)
        machines = try? c.decodeIfPresent(Machines.self, forKey: .machines)
        tools = try? c.decodeIfPresent([Tool].self, forKey: .tools)
        startedAt = try? c.decodeIfPresent(Date.self, forKey: .startedAt)
        endedAt = try? c.decodeIfPresent(Date.self, forKey: .endedAt)
    }
}

/// An install, upgrade or removal and where it stands.
nonisolated struct ExtensionOperation: Codable, Hashable, Sendable, Identifiable {
    enum Kind: String, Codable, Sendable {
        case install, upgrade, uninstall

        init(from decoder: Decoder) throws {
            self = Kind(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .install
        }
    }

    enum State: String, Codable, Sendable {
        case running
        /// Downloaded and checked: the preview waits for the user's yes (`preview_id`).
        case needsConsent = "needs_consent"
        case succeeded, failed, canceled

        init(from decoder: Decoder) throws {
            self = State(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .running
        }
    }

    var id: String
    var kind: Kind
    /// The extension's id (before the preview, the market's id of it).
    var extensionID: String
    var name: String?
    var market: String?
    var version: String?
    /// The version an upgrade goes from.
    var from: String?
    /// Who started it: a device's name, personad extension, the hub (an automatic upgrade).
    var actor: String?
    var state: State
    /// The step it is at (or stopped at).
    var step: String?
    var steps: [ExtensionOperationStep]
    /// Why it failed, in the language of the request that started it.
    var error: String?
    var errorCode: String?
    /// The preview it waits on (needs_consent).
    var previewID: String?
    /// The hub can start it again (`POST …/-/operations/{id}/retry`).
    var retryable: Bool
    /// What the user should do once it succeeded: "auth", the extension signs in and is not signed in yet (#4507).
    var next: String?
    var startedAt: Date?
    var updatedAt: Date?
    var endedAt: Date?
    /// Whose operation it is when another manager's than the default (its push says so, docs/assistant/managers.md
    /// "Clients"); nil: the default's, as everything this app starts is. Set by the app, never decoded or sent.
    var manager: String?

    /// Going on: running, or waiting for the user's yes.
    var isActive: Bool { state == .running || state == .needsConsent }
    var isRunning: Bool { state == .running }
    /// Done, and the extension waits for the user to sign in (the app opens its sign-in).
    var needsSignIn: Bool { state == .succeeded && next == "auth" }
    var current: ExtensionOperationStep? {
        steps.first { $0.id == step } ?? steps.first { $0.state == .running || $0.state == .failed }
    }
    var failedStep: ExtensionOperationStep? {
        state == .failed ? (steps.first { $0.state == .failed } ?? current) : nil
    }
    /// The steps worth showing: the ones this kind skips are left out.
    var shownSteps: [ExtensionOperationStep] { steps.filter { $0.state != .skipped } }

    enum CodingKeys: String, CodingKey {
        case id, kind, name, market, version, from, actor, state, step, steps, error, retryable, next
        case extensionID = "extension", plugin
        case errorCode = "error_code", previewID = "preview_id"
        case startedAt = "started_at", updatedAt = "updated_at", endedAt = "ended_at"
    }

    init(id: String, kind: Kind, extensionID: String, name: String? = nil, version: String? = nil, from: String? = nil,
         market: String? = nil, state: State = .running, step: String? = nil, steps: [ExtensionOperationStep] = [],
         error: String? = nil, errorCode: String? = nil, retryable: Bool = false, startedAt: Date? = nil,
         updatedAt: Date? = nil, endedAt: Date? = nil) {
        self.id = id
        self.kind = kind
        self.extensionID = extensionID
        self.name = name
        self.version = version
        self.from = from
        self.market = market
        self.state = state
        self.step = step
        self.steps = steps
        self.error = error
        self.errorCode = errorCode
        self.retryable = retryable
        self.startedAt = startedAt
        self.updatedAt = updatedAt
        self.endedAt = endedAt
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        kind = try c.decodeIfPresent(Kind.self, forKey: .kind) ?? .install
        // `plugin` from a hub still on the old names.
        extensionID = try c.decodeIfPresent(String.self, forKey: .extensionID) ?? c.decodeIfPresent(String.self, forKey: .plugin) ?? ""
        name = try c.decodeIfPresent(String.self, forKey: .name)
        market = try c.decodeIfPresent(String.self, forKey: .market)
        version = try c.decodeIfPresent(String.self, forKey: .version)
        from = try c.decodeIfPresent(String.self, forKey: .from)
        actor = try c.decodeIfPresent(String.self, forKey: .actor)
        state = try c.decodeIfPresent(State.self, forKey: .state) ?? .running
        step = try c.decodeIfPresent(String.self, forKey: .step)
        steps = (try? c.decodeIfPresent([ExtensionOperationStep].self, forKey: .steps)) ?? []
        error = try c.decodeIfPresent(String.self, forKey: .error)
        errorCode = try c.decodeIfPresent(String.self, forKey: .errorCode)
        previewID = try c.decodeIfPresent(String.self, forKey: .previewID)
        retryable = try c.decodeIfPresent(Bool.self, forKey: .retryable) ?? false
        next = try? c.decodeIfPresent(String.self, forKey: .next)
        startedAt = try? c.decodeIfPresent(Date.self, forKey: .startedAt)
        updatedAt = try? c.decodeIfPresent(Date.self, forKey: .updatedAt)
        endedAt = try? c.decodeIfPresent(Date.self, forKey: .endedAt)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(id, forKey: .id)
        try c.encode(kind, forKey: .kind)
        try c.encode(extensionID, forKey: .extensionID)
        try c.encodeIfPresent(name, forKey: .name)
        try c.encodeIfPresent(market, forKey: .market)
        try c.encodeIfPresent(version, forKey: .version)
        try c.encodeIfPresent(from, forKey: .from)
        try c.encodeIfPresent(actor, forKey: .actor)
        try c.encode(state, forKey: .state)
        try c.encodeIfPresent(step, forKey: .step)
        try c.encode(steps, forKey: .steps)
        try c.encodeIfPresent(error, forKey: .error)
        try c.encodeIfPresent(errorCode, forKey: .errorCode)
        try c.encodeIfPresent(previewID, forKey: .previewID)
        try c.encode(retryable, forKey: .retryable)
        try c.encodeIfPresent(next, forKey: .next)
        try c.encodeIfPresent(startedAt, forKey: .startedAt)
        try c.encodeIfPresent(updatedAt, forKey: .updatedAt)
        try c.encodeIfPresent(endedAt, forKey: .endedAt)
    }
}
