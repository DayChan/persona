import Foundation

// Subscription rate-limit windows (#2432, #2595, docs/server/usage-windows.md): GET /api/v1/usage/windows lists the accounts the
// machines' agents are signed in to, and those an account-pool gateway keeps, with how much of each window (5h, 7d …)
// is used. The types and the pure helpers (pace, countdown, sample age, which accounts the bars show) are here, as the
// web's lib/usage-windows.ts has them; the views are UsageWindowsViews.swift.

nonisolated struct UsageWindow: Decodable, Hashable, Sendable {
    var label: String
    var windowMinutes: Int
    var usedPercent: Double
    var resetsAt: Date?
    /// An allowance a provider keeps for one model next to the plan's own (#2595); nil for the plan's window.
    var model: String?

    enum CodingKeys: String, CodingKey {
        case label, model
        case windowMinutes = "window_minutes", usedPercent = "used_percent", resetsAt = "resets_at"
    }

    init(label: String, windowMinutes: Int, usedPercent: Double, resetsAt: Date?, model: String? = nil) {
        self.label = label
        self.windowMinutes = windowMinutes
        self.usedPercent = usedPercent
        self.resetsAt = resetsAt
        self.model = model
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        label = try c.decodeIfPresent(String.self, forKey: .label) ?? ""
        windowMinutes = try c.decodeIfPresent(Int.self, forKey: .windowMinutes) ?? 0
        usedPercent = try c.decodeIfPresent(Double.self, forKey: .usedPercent) ?? 0
        resetsAt = (try? c.decodeIfPresent(String.self, forKey: .resetsAt)).flatMap { $0.flatMap(JSON.parseDate) }
        let m = try c.decodeIfPresent(String.self, forKey: .model)?.trimmingCharacters(in: .whitespaces)
        model = m?.isEmpty == false ? m : nil
    }

    /// Unique within an account (the same length may come twice: the plan's and one model's).
    var key: String { "\(label):\(windowMinutes):\(model ?? "")" }
}

nonisolated struct UsageAccount: Decodable, Hashable, Sendable, Identifiable {
    var id: String
    var provider: String
    var name: String
    var plan: String?
    var windows: [UsageWindow]
    var sampledAt: Date?
    var stale: Bool
    /// Where the numbers came from (#2595): an agent's files on one of the computers, and/or gateways by name.
    var local: Bool
    var sources: [String]
    /// Why a gateway's numbers are old: "error" (it could not read this account just now) or, when its own last read
    /// failed, "rejected" (the key was turned away), "unreachable" or "failed" (docs/server/usage-windows.md).
    var problem: String?

    enum CodingKeys: String, CodingKey {
        case id, provider, name, plan, windows, stale, local, sources, problem
        case sampledAt = "sampled_at"
    }

    init(id: String, provider: String, name: String, plan: String? = nil, windows: [UsageWindow], sampledAt: Date?,
         stale: Bool = false, local: Bool = false, sources: [String] = [], problem: String? = nil) {
        self.id = id
        self.provider = provider
        self.name = name
        self.plan = plan
        self.windows = windows
        self.sampledAt = sampledAt
        self.stale = stale
        self.local = local
        self.sources = sources
        self.problem = problem
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decodeIfPresent(String.self, forKey: .id) ?? ""
        provider = try c.decodeIfPresent(String.self, forKey: .provider) ?? ""
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        plan = try c.decodeIfPresent(String.self, forKey: .plan)
        windows = try c.decodeIfPresent([UsageWindow].self, forKey: .windows) ?? []
        sampledAt = (try? c.decodeIfPresent(String.self, forKey: .sampledAt)).flatMap { $0.flatMap(JSON.parseDate) }
        stale = try c.decodeIfPresent(Bool.self, forKey: .stale) ?? false
        local = try c.decodeIfPresent(Bool.self, forKey: .local) ?? false
        sources = try c.decodeIfPresent([String].self, forKey: .sources) ?? []
        problem = try c.decodeIfPresent(String.self, forKey: .problem)
    }
}

/// The pure part of the subscription windows: what to show and how it reads (the web's lib/usage-windows.ts).
nonisolated enum UsageWindows {
    /// Samples older than this are dropped (a source that went quiet for days says nothing about now).
    static let sampleMaxAge: TimeInterval = 3 * 24 * 3600
    /// From this used percentage a bar turns to the caution colour.
    static let warnPercent: Double = 80
    /// The pace is not judged in the first part of a window: a few minutes in, any use is "ahead".
    static let paceQuietFraction: Double = 0.05
    /// How far (percentage points) the actual use may be from the expected before it is over / under.
    static let paceTolerance: Double = 5
    /// How often the bars ask the hub again while they are on screen.
    static let pollInterval: Duration = .seconds(60)

    /// The window's length in minutes from its label ("5h", "7d", "45m"); the given minutes when the label is not one.
    static func minutes(_ w: UsageWindow) -> Int {
        let s = w.label.trimmingCharacters(in: .whitespaces).lowercased()
        if let unit = s.last, let mult = ["m": 1.0, "h": 60, "d": 1440, "w": 10080][String(unit)],
           let n = Double(s.dropLast().trimmingCharacters(in: .whitespaces)), n > 0 {
            return Int((n * mult).rounded())
        }
        return max(0, w.windowMinutes)
    }

    enum PaceState: Equatable, Sendable { case over, under, on }

    struct Pace: Equatable, Sendable {
        /// Where use would be if it were spread evenly over the window, 0-100.
        var expected: Double
        var state: PaceState
    }

    /// How use compares with an even spread: expected% = elapsed / length; over when use is more than 5 points above it,
    /// under when more than 5 below. nil with no reset time or length, and in the first 5% of the window.
    static func pace(_ w: UsageWindow, now: Date = .now) -> Pace? {
        let total = Double(minutes(w)) * 60
        guard total > 0, let resets = w.resetsAt else { return nil }
        let elapsed = total - resets.timeIntervalSince(now)
        guard elapsed >= 0, elapsed <= total, elapsed / total >= paceQuietFraction else { return nil }
        let expected = elapsed / total * 100
        let diff = w.usedPercent - expected
        return Pace(expected: expected, state: diff > paceTolerance ? .over : diff < -paceTolerance ? .under : .on)
    }

    static func paceLabel(_ s: PaceState) -> String {
        switch s {
        case .over: String(localized: "Ahead of pace")
        case .under: String(localized: "Behind pace")
        case .on: String(localized: "On pace")
        }
    }

    /// When a window starts over, as a phrase: "resets in 45m" within the hour, "resets 13:05" when it is today, otherwise
    /// "resets in 2d 3h"; "resets soon" when it is due, "" without a time.
    static func resetLabel(_ resetsAt: Date?, now: Date = .now, calendar: Calendar = .current) -> String {
        guard let at = resetsAt else { return "" }
        let secs = at.timeIntervalSince(now)
        if secs <= 0 { return String(localized: "resets soon") }
        let mins = Int((secs / 60).rounded(.up))
        if mins < 60 { return String(localized: "resets in \(mins)m") }
        if calendar.isDate(at, inSameDayAs: now) {
            let time = at.formatted(Date.FormatStyle(date: .omitted, time: .shortened, calendar: calendar, timeZone: calendar.timeZone))
            return String(localized: "Resets \(time)")
        }
        let hours = mins / 60
        let d = hours / 24
        return d > 0 ? String(localized: "resets in \(d)d \(hours % 24)h") : String(localized: "resets in \(hours)h")
    }

    /// How long ago a sample was taken ("12 s ago"), coarser past a minute; "" without a time.
    static func sampleAge(_ at: Date?, now: Date = .now) -> String {
        guard let at, at.isMeaningful else { return "" }
        let s = max(0, Int(now.timeIntervalSince(at).rounded()))
        if s < 60 { return String(localized: "\(s) seconds ago") }
        if s < 3600 { return String(localized: "\(s / 60) min ago") }
        if s < 86400 { return String(localized: "\(s / 3600)h ago") }
        return String(localized: "\(s / 86400)d ago")
    }

    /// The age as the account's header says it: "Outdated · 3 hr ago" when stale.
    static func ageLabel(_ a: UsageAccount, now: Date = .now) -> String {
        let age = sampleAge(a.sampledAt, now: now)
        return a.stale ? (age.isEmpty ? String(localized: "Outdated [Usage Windows 2fe0e3]") : String(localized: "Outdated · \(age)")) : age
    }

    /// The accounts worth showing: samples within three days, windows that have not reset, accounts that still have one.
    static func live(_ accounts: [UsageAccount], now: Date = .now) -> [UsageAccount] {
        accounts.compactMap { a in
            if let t = a.sampledAt, t.isMeaningful, now.timeIntervalSince(t) > sampleMaxAge { return nil }
            var a = a
            a.windows = a.windows.filter { $0.resetsAt.map { $0 > now } ?? true }
            return a.windows.isEmpty ? nil : a
        }
    }

    /// The used percentage of an account's fullest window. A per-model allowance does not count while the plan's own
    /// windows are there: one model running out is not the account running out.
    static func fullest(_ a: UsageAccount) -> Double {
        let plan = a.windows.filter { $0.model == nil }
        return (plan.isEmpty ? a.windows : plan).map(\.usedPercent).max() ?? 0
    }

    /// Where an account was seen, for people: "This Device", then the gateways' names.
    static func sourceNames(_ a: UsageAccount) -> [String] {
        (a.local ? [String(localized: "usage.source.local", defaultValue: "Local", comment: "Usage numbers read from an agent's own files on a computer")] : []) + a.sources
    }

    /// The plan to write beside the name, unless the name already says it ("Codex Pro" and plan "pro").
    static func planNote(_ a: UsageAccount) -> String {
        let plan = (a.plan ?? "").trimmingCharacters(in: .whitespaces)
        return !plan.isEmpty && !a.name.lowercased().contains(plan.lowercased()) ? plan : ""
    }

    /// What the gateway could not do, in words; "" when nothing went wrong or the hub sends a reason this build doesn't
    /// know. A key the gateway turned away says where to fix it: it is not a network problem (#1902).
    static func problemText(_ a: UsageAccount) -> String {
        switch a.problem {
        case "rejected": String(localized: "The gateway turned the key away; update it in Admin → Usage sources. Showing the last numbers")
        case "unreachable": String(localized: "Can't reach the gateway right now; showing the last numbers")
        case "failed": String(localized: "Couldn't read the gateway; showing the last numbers")
        case "error": String(localized: "The gateway couldn't read this account; showing the numbers it saved")
        default: ""
        }
    }

    /// The accounts the bars are drawn for: the `n` with the fullest window, stale ones last. Stable for equal ones.
    static func top(_ accounts: [UsageAccount], n: Int = 2) -> [UsageAccount] {
        accounts.enumerated()
            .sorted { l, r in
                if l.element.stale != r.element.stale { return !l.element.stale }
                let (fl, fr) = (fullest(l.element), fullest(r.element))
                return fl != fr ? fl > fr : l.offset < r.offset
            }
            .prefix(n)
            .map(\.element)
    }

    static func isWarn(_ percent: Double) -> Bool { percent >= warnPercent }

    /// "86%".
    static func percentText(_ p: Double) -> String { "\(Int(p.rounded()))%" }

    /// 0…1 for a bar.
    static func fraction(_ p: Double) -> Double { min(100, max(0, p)) / 100 }
}
