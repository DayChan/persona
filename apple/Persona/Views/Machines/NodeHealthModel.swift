import Foundation
import Observation

// What This Computer Can Do (#3018, the web's #2429, docs/server/machines.md「Doctor: what this computer can do」): what `personad node doctor` finds on a computer, read
// as features. The node reports individual checks (server/internal/node/doctor.go, DoctorReport as it is); a person
// wants to know whether phone approvals work, so each feature is a set of those checks and takes the worst state among
// them. The grouping, the states and the words are the web's (web/src/lib/node-health.ts): change both together.

/// One line of the node's report; `state` is current / stale / missing for a generated file, "" for the others.
nonisolated struct DoctorCheck: Decodable, Hashable, Sendable {
    enum Status: String, Sendable { case ok, warn, fail }

    var name: String
    var status: Status
    var state: String
    var detail: String
    var hint: String
    var items: [String]

    init(name: String, status: Status, state: String = "", detail: String = "", hint: String = "", items: [String] = []) {
        self.name = name
        self.status = status
        self.state = state
        self.detail = detail
        self.hint = hint
        self.items = items
    }

    private enum CodingKeys: String, CodingKey { case name, status, state, detail, hint, items }

    init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        // A status a newer node adds reads as a warning: shown, not hidden, and not called broken.
        status = Status(rawValue: try c.decodeIfPresent(String.self, forKey: .status) ?? "") ?? .warn
        state = try c.decodeIfPresent(String.self, forKey: .state) ?? ""
        detail = try c.decodeIfPresent(String.self, forKey: .detail) ?? ""
        hint = try c.decodeIfPresent(String.self, forKey: .hint) ?? ""
        items = try c.decodeIfPresent([String].self, forKey: .items) ?? []
    }

    var statusLabel: String {
        switch status {
        case .ok: String(localized: "OK [Node Health Model 296de0]")
        case .warn: String(localized: "Warning")
        case .fail: String(localized: "Problem")
        }
    }

    /// Persona's own generated file is out of date or gone: Repair writes it again.
    var fixable: Bool { state == "stale" || state == "missing" }
}

nonisolated struct DoctorReport: Decodable, Sendable {
    var ok: Bool
    var checks: [DoctorCheck]
    /// The generated files `fix` wrote again.
    var fixed: [String]

    init(ok: Bool = true, checks: [DoctorCheck], fixed: [String] = []) {
        self.ok = ok
        self.checks = checks
        self.fixed = fixed
    }

    private enum CodingKeys: String, CodingKey { case ok, checks, fixed }

    init(from decoder: any Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok) ?? false
        checks = try c.decodeIfPresent([DoctorCheck].self, forKey: .checks) ?? []
        fixed = try c.decodeIfPresent([String].self, forKey: .fixed) ?? []
    }
}

nonisolated enum NodeHealth {
    enum FeatureState: Sendable {
        case ready, partial, broken, off

        var label: String {
            switch self {
            case .ready: String(localized: "Ready [Node Health Model b073db]")
            case .partial: String(localized: "Partly Working")
            case .broken: String(localized: "Problem")
            case .off: String(localized: "Off [Machine Detail View f95ea7]")
            }
        }
    }

    struct FeatureSpec: Sendable {
        var id: String
        var name: String
        /// What it lets the user do, one line.
        var detail: String
        /// The checks it stands on (DoctorCheck.name).
        var checks: [String]
        /// Something a computer may well not use: a warning alone means Off.
        var optional = false
        /// For an optional one: whether this computer needs it (by its engine), which turns the warning into Problem.
        var needed: (@Sendable (MachineEngine) -> Bool)?
    }

    struct Feature: Identifiable, Sendable {
        var id: String
        var name: String
        var detail: String
        var state: FeatureState
        var checks: [DoctorCheck]
        /// Why it is not ready, from its worst check: what to do, else what was found. "" when ready.
        var note: String
    }

    /// The checks the node reports and no feature stands on: the user's own settings, shown as information.
    static let infoCheckNames = ["global-permission-hooks"]

    static let features: [FeatureSpec] = [
        FeatureSpec(id: "approvals", name: String(localized: "Approval Cards on Phone"), detail: String(localized: "Answer questions and approve tools and plans on your phone"),
                    checks: ["node.sock", "claude-settings.json", "claude-hooks", "assistant-settings.json", "assistant-hooks", "command-paths"]),
        FeatureSpec(id: "status", name: String(localized: "Status and Notifications"), detail: String(localized: "Agent status changes and completion notifications"), checks: ["node.sock", "command-paths"]),
        FeatureSpec(id: "terminal", name: String(localized: "Terminal and Engine"), detail: String(localized: "View and use this computer's terminal from your phone"), checks: ["herdr"], optional: true,
                    needed: { $0 == .herdr || $0 == .herdrBoth }),
        FeatureSpec(id: "mcp", name: String(localized: "MCP Tools"), detail: String(localized: "Persona's tools available inside agents"), checks: ["node.sock", "mcp.json", "command-paths"]),
        FeatureSpec(id: "skills", name: String(localized: "Skill"), detail: String(localized: "Skills that agents started by Persona come with"), checks: ["agent-plugin", "claude-version"]),
        FeatureSpec(id: "runtimes", name: String(localized: "Runtime Connections"), detail: String(localized: "Runtimes like DSH and pi connect to this computer"), checks: ["runtime.sock"], optional: true),
    ]

    private static func rank(_ s: DoctorCheck.Status) -> Int { s == .ok ? 0 : s == .warn ? 1 : 2 }

    private static func worst(_ checks: [DoctorCheck]) -> DoctorCheck? {
        var w: DoctorCheck?
        for c in checks where c.status != .ok && (w == nil || rank(c.status) > rank(w!.status)) { w = c }
        return w
    }

    /// A stale or missing generated file has Repair here, so the node's hint (a command line) is not repeated.
    private static func note(_ c: DoctorCheck?) -> String {
        guard let c else { return "" }
        if c.state == "stale" { return String(localized: "Files Persona generated are out of date. Tap Repair to regenerate them.") }
        if c.state == "missing" { return String(localized: "Files Persona generated are missing. Tap Repair to regenerate them.") }
        return c.hint.isEmpty ? c.detail : c.hint
    }

    /// The report as features, in a fixed order. A feature none of whose checks the report has (an older node reports
    /// fewer) is left out; a check no feature covers and that is not fine makes Other Checks so nothing hides.
    static func derive(_ report: DoctorReport, engine: MachineEngine = .herdr) -> [Feature] {
        var byName: [String: DoctorCheck] = [:]
        for c in report.checks where byName[c.name] == nil { byName[c.name] = c }
        var out: [Feature] = []
        for spec in features {
            let checks = spec.checks.compactMap { byName[$0] }
            if checks.isEmpty { continue }
            let w = worst(checks)
            var state = FeatureState.ready
            if w?.status == .fail {
                state = .broken
            } else if w?.status == .warn {
                state = spec.optional ? (spec.needed?(engine) == true ? .broken : .off) : .partial
            }
            out.append(Feature(id: spec.id, name: spec.name, detail: spec.detail, state: state, checks: checks, note: note(w)))
        }
        let covered = Set(features.flatMap(\.checks) + infoCheckNames)
        let rest = report.checks.filter { !covered.contains($0.name) }
        if let w = worst(rest) {
            out.append(Feature(id: "other", name: String(localized: "Other Checks"), detail: String(localized: "Extra items this node version reports"),
                               state: w.status == .fail ? .broken : .partial, checks: rest, note: note(w)))
        }
        return out
    }

    struct Summary: Equatable, Sendable {
        /// Features that are broken or partial.
        var attention: Int
        var title: String
        /// Not set up: … ("" when every feature is in use).
        var off: String
    }

    static func summarize(_ features: [Feature]) -> Summary {
        let attention = features.filter { $0.state == .broken || $0.state == .partial }.count
        let off = features.filter { $0.state == .off }.map(\.name)
        return Summary(attention: attention, title: attention == 0 ? String(localized: "All Good") : String(localized: "\(attention) features need attention"),
                       off: off.isEmpty ? "" : String(localized: "Not set up: \(off.joined(separator: "、"))"))
    }

    /// The checks Repair would put right: Persona's own generated files that are stale or missing.
    static func fixable(_ report: DoctorReport) -> [DoctorCheck] { report.checks.filter(\.fixable) }

    /// The user's own settings the node only looks at: a row of information, not a feature.
    static func info(_ report: DoctorReport) -> [DoctorCheck] {
        report.checks.filter { infoCheckNames.contains($0.name) && $0.status != .ok }
    }

    /// What the toast says after Repair: the node re-checks after writing, so the report is already the new one.
    static func fixResult(_ report: DoctorReport) -> (text: String, failed: Bool) {
        if let failed = report.checks.first(where: { $0.name == "fix" && $0.status == .fail }) { return (failed.detail, true) }
        return report.fixed.isEmpty ? (String(localized: "No files needed regenerating"), false) : (String(localized: "Regenerated \(report.fixed.count) items"), false)
    }

    enum Unavailable: Equatable, Sendable { case oldNode, oldHub, offline, forbidden }

    /// Why a self-check could not be had, from the hub's answer. An old node answers "unknown method"; an old hub has no
    /// such route (a plain-text 404, not the JSON of "unknown machine").
    static func unavailable(_ error: any Error) -> Unavailable? {
        guard let e = error as? APIError else { return nil }
        if e.message.range(of: "unknown method", options: .caseInsensitive) != nil { return .oldNode }
        if e.status == 404 && e.details == nil { return .oldHub }
        if e.status == 503 { return .offline }
        if e.status == 403 { return .forbidden }
        return nil
    }

    /// Whether the hub can install its own build there (Redeploy): the hub's own answer (newer hubs), else not the hub's
    /// own machine and not one that dials in.
    static func canDeploy(_ m: Machine) -> Bool {
        if let can = m.canDeploy { return can }
        return !["local", "link", "service"].contains(m.kind)
    }

    /// Why an old node cannot be updated from here, when it cannot: the hub's own words (newer hubs), else ours.
    static func deployReason(_ m: Machine) -> String? {
        if let reason = m.deployReason?.trimmingCharacters(in: .whitespacesAndNewlines), !reason.isEmpty { return reason }
        return switch m.kind {
        case "local": String(localized: "This is the hub machine. Upgrade the hub and node on it directly.")
        case "link", "service": String(localized: "First upgrade node locally on this computer, and upgrade the hub to a version that supports updates over the link.")
        default: nil
        }
    }

    /// Why a computer is not connected, read from the error the hub keeps (the same kinds and patterns as the web's
    /// `classifyConnection`); nil when the error says nothing these know. Reconnect's toast and the offline card say it
    /// in plain words.
    enum ConnectionFailure: Sendable, Equatable {
        case auth, version, timeout, unreachable
    }

    static func connectionFailure(_ m: Machine) -> ConnectionFailure? {
        let text = m.error ?? ""
        func has(_ pattern: String) -> Bool { text.range(of: pattern, options: [.regularExpression, .caseInsensitive]) != nil }
        // Order matters: "connection timed out" is a timeout, not "connection …".
        if has(#"permission denied|publickey|authentication|unauthori[sz]ed|invalid[ _]token|forbidden|host key verification|access denied|\b40[13]\b|配对|认证"#) { // i18n-ignore: matches hub and ssh error text
            return .auth
        }
        if m.nodeMismatch == true || has(#"version mismatch|protocol version|unknown method|incompatible|不是同一版|版本"#) { // i18n-ignore: matches hub and ssh error text
            return .version
        }
        if has(#"timed out|timeout|deadline exceeded|no pong"#) { return .timeout }
        if has(#"refused|no route|unreachable|could not resolve|name or service|no such host|network is|connection (reset|closed|lost)|broken pipe|\beof\b|node exited|waiting for|lookup |dial "#) {
            return .unreachable
        }
        return nil
    }

    /// An offline computer's card line: what failed, then what to do ("Connection timed out: …"); nil when its error is
    /// one of none of these kinds, empty, or it is being deployed (its deploy row says what is going on).
    static func offlineReason(_ m: Machine) -> String? {
        guard !m.online, m.deploying != true, !(m.error ?? "").isEmpty, let f = connectionFailure(m) else { return nil }
        let link = m.kind == "link" || m.kind == "service"
        let (title, next): (String, String) = switch f {
        case .auth: (String(localized: "Authentication failed"),
                     link ? String(localized: "Pair Persona Link again on this computer, then reconnect")
                         : String(localized: "Check that this computer's SSH key and account are still valid, then reconnect"))
        case .version: (String(localized: "Version mismatch"),
                        canDeploy(m) ? String(localized: "Redeploy to update the node to the same version as the hub")
                            : String(localized: "Upgrade the node on this computer so it matches the hub"))
        case .timeout: (String(localized: "Connection timed out"),
                        String(localized: "The network is unstable or the computer is asleep. Wait for it to wake up or switch networks, then reconnect"))
        case .unreachable: (String(localized: "Can't reach this computer"),
                            link ? String(localized: "Make sure it's on and Persona Link is running, then reconnect")
                                : String(localized: "Make sure it's on, the network is up and the node is running, then reconnect"))
        }
        return String(localized: "\(title): \(next)")
    }
}

/// A Redeploy's stage as the hub reports it (`deploy_phase`): what the machine card says while it runs.
nonisolated enum MachineDeployPhase: String, Sendable {
    case waitingIdle = "waiting_idle"
    case transferring
    case restarting
    case confirming

    /// The card's line for this stage.
    var message: String {
        switch self {
        case .waitingIdle: String(localized: "Waiting for the agents on this computer to go idle…")
        case .transferring: String(localized: "Sending Persona to this computer…")
        case .restarting: String(localized: "Restarting Persona on this computer…")
        case .confirming: String(localized: "Waiting for Persona to reconnect and confirm its version…")
        }
    }

    /// The stage a machine's `deploy_phase` names; nil for an older hub or a deploy with no stage yet.
    static func of(_ m: Machine) -> MachineDeployPhase? {
        m.deployPhase.flatMap(MachineDeployPhase.init(rawValue:))
    }
}

/// The section's state: checks once when shown (and on Check Again); Repair writes and comes back with the new report.
@MainActor @Observable final class NodeHealthModel {
    struct Deps {
        var check: () async throws -> DoctorReport
        var fix: () async throws -> DoctorReport
    }

    enum Load {
        case idle
        case loading
        case report(DoctorReport)
        case unavailable(NodeHealth.Unavailable)
        case error(String)
    }

    private(set) var load: Load = .idle
    /// A re-check or a fix is running over the report that is shown.
    private(set) var busy = false
    private(set) var fixing = false
    @ObservationIgnored private let deps: Deps
    /// Answers to an older request are dropped (a re-check while a fix is on its way).
    @ObservationIgnored private var seq = 0

    init(deps: Deps) { self.deps = deps }

    var report: DoctorReport? {
        if case .report(let r) = load { return r }
        return nil
    }

    /// An offline computer: nothing to ask, and nothing older to show.
    func reset() {
        seq += 1
        load = .idle
        busy = false
    }

    func check() async {
        seq += 1
        let mine = seq
        if report == nil { load = .loading }
        busy = true
        defer { if mine == seq { busy = false } }
        do {
            let r = try await deps.check()
            guard mine == seq else { return }
            load = .report(r)
        } catch {
            guard mine == seq, !error.isCancellation else { return }
            if let why = NodeHealth.unavailable(error) {
                load = .unavailable(why)
            } else {
                load = .error(error.shown ?? error.localizedDescription)
            }
        }
    }

    /// Returns what to tell the user (a toast), failed when the fix did not go through.
    func fix() async -> (text: String, failed: Bool) {
        seq += 1
        let mine = seq
        fixing = true
        busy = true
        defer {
            fixing = false
            if mine == seq { busy = false }
        }
        do {
            let r = try await deps.fix()
            if mine == seq { load = .report(r) }
            return NodeHealth.fixResult(r)
        } catch {
            return (String(localized: "Repair failed: \(error.shown ?? error.localizedDescription)"), true)
        }
    }
}

extension APIClient {
    /// What This Computer Can Do (docs/server/machines.md「Doctor: what this computer can do」): the node's self-check, read only. Full devices only (403 otherwise);
    /// an old node answers "unknown method", an old hub a plain 404. The hub gives the node 45 s.
    func nodeDoctor(machine: String) async throws -> DoctorReport {
        try JSON.decoder.decode(DoctorReport.self, from: await request("GET", "/api/v1/machines/\(machine)/doctor", timeout: 50))
    }

    /// Repair: the node writes Persona's own generated files again (never the user's settings) and re-checks.
    func fixNodeDoctor(machine: String) async throws -> DoctorReport {
        try await post("/api/v1/machines/\(machine)/doctor/fix", timeout: 65, as: DoctorReport.self)
    }
}
