import Foundation

// Claude Code's ultracode switch on iOS, the Mac and the watch (#1307; the server is #3427, docs/protocol/protocol.md
// "Claude Code ultracode"; the web's lib/ultracode.ts does the same): with it on, Claude runs every substantive task as a
// multi-agent workflow. It is its own switch beside the thinking effort, only for the session's current process, off by
// default and never remembered. The pure rules are here (the watch compiles them for its rows' mark); the request and
// what this device asked are `AppModel.setUltracode`, the views `UltracodeViews.swift`.

nonisolated enum Ultracode {
    /// The hub takes the switch (hello `features`): an older hub never shows it.
    static let feature = "ultracode"
    /// Its name everywhere: a proper name, never translated (docs/protocol/i18n.md).
    static let name = "ultracode"
    /// The first Claude Code with the independent switch; known older ones are refused (the node's version gate).
    static let minVersion = [2, 1, 284]
    /// The Codex effort that does the same thing (several subagents, a lot of quota): its chip carries the same mark.
    static let codexUltra = "ultra"

    // MARK: Where it shows

    /// Whether a chat offers the switch: the hub has it, the agent is Claude Code (not TRAEX, Codex or a runtime's) whose
    /// thinking effort can change now (it takes messages, no read-only source, not offline), and it is not a Persona
    /// conversation (the main one included: the server refuses those).
    static func offered(_ s: Session?, features: Set<String>) -> Bool {
        guard let s, !s.assistant, features.contains(feature) else { return false }
        return s.isClaude && !s.hasSource && s.canSend && s.state != .offline
    }

    /// The node saw it on (unknown is not on), on a Claude Code agent of the user's.
    static func on(_ s: Session) -> Bool { s.ultracode == true && !s.assistant && s.isClaude }

    /// The mark beside a list row's title: on, and not on an offline row (the node keeps the last value it saw after the
    /// process is gone, and a resume starts it off).
    static func rowMark(_ s: Session) -> Bool { on(s) && s.state != .offline }

    /// The info card's row: a Claude Code agent on a hub that has the switch, whose process runs. Not on an offline one,
    /// as its row and chip (`rowMark`): the node keeps the last value it saw, and a resume starts it off.
    static func infoShown(_ s: Session?, features: Set<String>) -> Bool {
        guard let s, !s.assistant, features.contains(feature) else { return false }
        return s.isClaude && !s.hasSource && s.state != .offline
    }

    /// What the info card's row says: on, off, or unknown (no evidence yet, an older node).
    static func stateWord(_ value: Bool?) -> String {
        switch value {
        case true?: String(localized: "On [Ultracode 5a0c31]", comment: "ultracode in the session info: on")
        case false?: String(localized: "Off [Ultracode 5a0c31]", comment: "ultracode in the session info: off")
        case nil: String(localized: "Unknown [Session Modes 4d8c1c]")
        }
    }

    /// The workflow mark after a thinking-effort label (a chip, the composer's model button).
    enum Mark: Equatable, Sendable {
        /// Claude Code's ultracode is on.
        case ultracode
        /// A Codex thread on its "ultra" effort: the same thing, which the effort's own word already says.
        case codexUltra

        /// What VoiceOver adds after the label: 「… ultracode 开」; nothing for Codex, whose effort word says it.
        var spoken: String? {
            switch self {
            case .ultracode: Ultracode.onWords
            case .codexUltra: nil
            }
        }
    }

    /// The mark a session's effort label carries: ultracode on (not offline), or a Codex thread at "ultra".
    static func chipMark(_ s: Session?) -> Mark? {
        guard let s else { return nil }
        if rowMark(s) { return .ultracode }
        if s.setsEffortOnly, s.effort == codexUltra { return .codexUltra }
        return nil
    }

    /// 「ultracode 开」: the mark read out.
    static var onWords: String { String(localized: "ultracode on", comment: "VoiceOver for the workflow mark; ultracode is a proper name") }

    // MARK: New Agent

    /// Whether New Agent offers it: the hub has it and what starts is Claude Code.
    static func startOffered(features: Set<String>, agent: String) -> Bool {
        features.contains(feature) && (agent.isEmpty || agent == "claude")
    }

    /// A known Claude Code version older than the switch's (the node's own rule): an unknown one may be tried.
    static func versionTooOld(_ version: String?, min: [Int] = minVersion) -> Bool {
        let v = (version ?? "").trimmingCharacters(in: .whitespaces)
        guard let m = versionPattern.firstMatch(in: v, range: NSRange(v.startIndex..., in: v)) else { return false }
        let nums = (1...3).compactMap { i in Range(m.range(at: i), in: v).flatMap { Int(v[$0]) } }
        guard nums.count == 3 else { return false }
        for (n, least) in zip(nums, min) where n != least { return n < least }
        return false
    }

    /// "2.1.250", "v2.1.250", "2.1.284-beta": what the node reports as a machine's `claude_version`.
    private static let versionPattern = try! NSRegularExpression(pattern: #"^v?(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$"#)

    /// Why New Agent can't start with it on this computer (its Claude Code is too old); nil when it can.
    static func startBlocked(claudeVersion: String?) -> String? {
        guard versionTooOld(claudeVersion), let v = claudeVersion?.trimmingCharacters(in: .whitespaces) else { return nil }
        let min = minVersion.map(String.init).joined(separator: ".")
        return String(localized: "Claude Code on this computer is \(v); ultracode needs \(min) or later")
    }

    // MARK: What this device asked

    /// A switch this device asked for: the node answers by pushing the session, sometimes after the request returns, and a
    /// queued one (`ultracodePending`) says it waits, not what for. While either holds, the switch stands where it was
    /// put. A refusal is not kept: it is said once, and asking again gets the node's answer again.
    struct Ask: Equatable, Sendable {
        var on: Bool
        var sending: Bool
        /// The session as the page drew it when the request came back: the push with the result has not come while it
        /// still is.
        var answered: Session? = nil
    }

    /// Where the switch stands: what was asked while it is on its way, otherwise what the node saw.
    static func shownOn(_ s: Session, ask: Ask?) -> Bool {
        guard let ask else { return s.ultracode == true }
        if ask.sending || s.ultracodePending { return ask.on }
        if ask.answered == s, s.ultracode != ask.on, s.ultracodeNote == nil { return ask.on }
        return s.ultracode == true
    }

    /// An ask that no longer says anything: the session moved past it.
    static func askSpent(_ s: Session, ask: Ask) -> Bool {
        !ask.sending && !s.ultracodePending && ask.answered != s
    }

    /// The switch as drawn.
    struct Switch: Equatable, Sendable {
        var on: Bool
        var disabled: Bool
        var pending: Bool
        /// The line under it: what it does, that a change waits for the turn, or the node's reason.
        var line: String
        /// The line is the node's reason (`ultracode_note`): said in the caution color.
        var caution: Bool
    }

    /// The switch as drawn. Grayed only while its own request is out. The node's reason is said in place of the line but
    /// does not gray it: many are fixed elsewhere (dynamic workflows turned on in Claude Code's settings, a draft cleared
    /// on the computer) and only clear on new terminal evidence, and the ones that can't be the node refuses again at
    /// once, nothing typed. A change this device asked for says "Applies next turn" while it waits; one asked elsewhere
    /// (another device, the app reopened) says a change waits, without tying it to where the switch stands.
    static func switchState(_ s: Session, ask: Ask?) -> Switch {
        let on = shownOn(s, ask: ask)
        let pending = s.ultracodePending
        let note = (s.ultracodeNote ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        let disabled = ask?.sending == true
        if pending {
            let queued = queuedWords(ask: ask)
            return Switch(on: on, disabled: disabled, pending: true, line: note.isEmpty ? queued : "\(queued) · \(note)", caution: false)
        }
        if !note.isEmpty { return Switch(on: on, disabled: disabled, pending: false, line: note, caution: true) }
        return Switch(on: on, disabled: disabled, pending: false, line: subtitle, caution: false)
    }

    /// A change waits for the turn: "Applies next turn" for the one this device asked, otherwise that one waits, without
    /// tying it to where the switch stands.
    static func queuedWords(ask: Ask?) -> String {
        ask != nil ? pendingWords : String(localized: "A change is queued for the next turn")
    }

    /// What a menu says around the switch: its subtitle, and its section's header (the quota and its warning). A menu
    /// colors no words, so neither is in the caution color. `headerWraps`: an iOS menu cuts a subtitle after three lines
    /// but draws a header whole, line by line, so the node's reason goes up there, first, and the subtitle keeps what it
    /// does or that a change waits; a Mac menu wraps a subtitle and draws a header on one line, so the reason stays in the
    /// subtitle and the header's parts go on one line.
    struct MenuWords: Equatable, Sendable {
        var subtitle: String
        /// nil: no header (no reason up there and no quota to say).
        var header: String?
    }

    static func menuWords(_ s: Session, ask: Ask?, quota: Quota?, headerWraps: Bool) -> MenuWords {
        let sw = switchState(s, ask: ask)
        let note = (s.ultracodeNote ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        var subtitle = sw.line
        var header: [String] = []
        if headerWraps, !note.isEmpty {
            header.append(note)
            subtitle = sw.pending ? queuedWords(ask: ask) : Self.subtitle
        }
        if let quota {
            header.append(quotaLine(quota))
            if let age = quotaAge(quota) { header.append(age) }
            if quota.warn, quotaWarnShown(sw) { header.append(quotaWarning) }
        }
        return MenuWords(subtitle: subtitle, header: header.isEmpty ? nil : header.joined(separator: headerWraps ? "\n" : " — "))
    }

    /// The switch's description (zh-Hans: 「每个任务都编排多 Agent workflow · 很费额度 · 只对本会话」).
    static var subtitle: String { String(localized: "Runs every task as a multi-agent workflow · uses a lot of quota · this session only") }
    /// Shown while this device's change waits for the turn to end (zh-Hans: 「下一轮生效」).
    static var pendingWords: String { String(localized: "Applies next turn") }

    /// The quota warning goes with a switch that stands on, or that is off and can simply be turned on: not under the
    /// node's reason, not while turning it off is out or queued.
    static func quotaWarnShown(_ sw: Switch) -> Bool { sw.on || (!sw.caution && !sw.pending && !sw.disabled) }

    /// The resume notice is up: it was on before the agent resumed, and nothing turned it back on yet.
    static func lostShown(_ s: Session?, features: Set<String>) -> Bool {
        guard let s, offered(s, features: features), s.ultracodeLostAt != nil else { return false }
        return s.ultracode != true
    }

    // MARK: Refusals

    /// The hub refused it for this start or session (an old node, a session or CLI that can't take it): asking the same
    /// again gets the same answer.
    static func refused(_ error: Error) -> Bool {
        guard let code = (error as? APIError)?.code else { return false }
        return code == "node_outdated" || code == "ultracode_unsupported"
    }

    /// Why a request failed, in words: the hub's or node's own reason, or ours for a refusal that brought none.
    static func errorText(_ error: Error) -> String {
        if let e = error as? APIError {
            let words = (e.fields["error"] ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            switch e.code {
            case "node_outdated": return words.isEmpty ? String(localized: "personad on this computer is too old for ultracode. Update its node") : words
            case "ultracode_unsupported": return words.isEmpty ? String(localized: "This session can't use ultracode") : words
            default: break
            }
        }
        return error.localizedDescription
    }

    // MARK: The quota under the switch

    struct Quota: Equatable, Sendable {
        enum Kind: Sendable { case fiveHours, week }
        struct Window: Equatable, Sendable {
            var kind: Kind
            /// Used, 0-100.
            var percent: Double
        }
        var name: String
        /// The plan's 5-hour and weekly windows it has, in that order.
        var windows: [Window]
        /// One of them is past the caution line (80%, the usage bars'): the switch says it will run out sooner.
        var warn: Bool
        /// The numbers are out of date (the hub's mark, as on the usage bars): use only grows, so the real use may be
        /// higher, and the line says so with when they were read (#1906).
        var stale = false
        var sampledAt: Date?
    }

    /// The Claude subscription's 5-hour and weekly use (#2432's windows) for the line under the switch: a fresh sample
    /// before a stale one (a fresh account only a gateway reports says more about now than old numbers from a computer,
    /// #1906), then an account read on the computers before one only a gateway reports, then the fullest. nil without
    /// one: nothing is drawn. The hub merges accounts across computers, so this is the account's use, not one computer's.
    static func quota(_ accounts: [UsageAccount], now: Date = .now) -> Quota? {
        func rank(_ a: UsageAccount) -> Int { (a.stale ? 2 : 0) + (a.local ? 0 : 1) }
        let kinds: [(Int, Quota.Kind)] = [(300, .fiveHours), (10080, .week)]
        let claude = UsageWindows.live(accounts, now: now)
            .filter { $0.provider == "claude" }
            .map { a in
                (a, kinds.compactMap { mins, kind in
                    a.windows.first { $0.model == nil && UsageWindows.minutes($0) == mins }
                        .map { Quota.Window(kind: kind, percent: Swift.max(0, Swift.min(100, $0.usedPercent))) }
                })
            }
            .filter { !$0.1.isEmpty }
        let top = claude.enumerated().min { l, r in
            let (rl, rr) = (rank(l.element.0), rank(r.element.0))
            if rl != rr { return rl < rr }
            let (fl, fr) = (l.element.1.map(\.percent).max() ?? 0, r.element.1.map(\.percent).max() ?? 0)
            return fl != fr ? fl > fr : l.offset < r.offset
        }?.element
        guard let (account, windows) = top else { return nil }
        return Quota(name: account.name, windows: windows, warn: windows.contains { UsageWindows.isWarn($0.percent) },
                     stale: account.stale, sampledAt: account.sampledAt)
    }

    /// "Outdated · 2 hr ago" when the quota's numbers are out of date, as the usage bars say it (#1906); nil while they
    /// are current. A line of its own under the quota, so that neither is cut off.
    static func quotaAge(_ q: Quota, now: Date = .now) -> String? {
        q.stale ? UsageWindows.staleLabel(q.sampledAt, now: now) : nil
    }

    /// The quota line, for example "Claude quota: 5h 86% · weekly 41%" (zh-Hans: 「Claude 额度：5 小时 86% · 每周 41%」).
    static func quotaLine(_ q: Quota) -> String {
        let windows = q.windows.map { w in
            let p = UsageWindows.percentText(w.percent)
            return switch w.kind {
            case .fiveHours: String(localized: "5h \(p)", comment: "ultracode's quota line: the 5-hour window's use")
            case .week: String(localized: "weekly \(p)", comment: "ultracode's quota line: the weekly window's use")
            }
        }.joined(separator: " · ")
        return String(localized: "\(q.name) quota: \(windows)", comment: "ultracode's quota line: the account, then its windows")
    }

    /// The warning at 80 % or more (zh-Hans: 「额度快用完了，开着 ultracode 会用得更快」).
    static var quotaWarning: String { String(localized: "Quota is running low; ultracode uses it up faster") }
}
