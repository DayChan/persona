import PersonaKit
import SwiftUI

/// The machine pages are pushed by value (MachineListRoute, MachineDetailRoute) and so are the chats they open
/// (SessionKey): a page pushed with `NavigationLink { … }` that pushes by value puts the new page *under* itself, so a
/// tap seemed to do nothing (Settings › Machines › a card; Machine Details › an agent). Every stack that shows them registers both.
struct MachineListRoute: Hashable {}
struct MachineDetailRoute: Hashable {
    var ref: MachineRef
    init(_ ref: MachineRef) { self.ref = ref }
    /// A machine of the default group, or "group|id" (launch arguments).
    init(id: String) { ref = MachineRef(path: id) }
}

/// What brings a machine to the top of Machines (docs/server/machines.md「App」), most pressing first: it is offline, a value is
/// too high, an agent there needs you, its personad is not the hub's build.
enum MachineConcern: Int, Comparable {
    case pending, offline, critical, warning, needsYou, mismatch

    static func < (a: Self, b: Self) -> Bool { a.rawValue < b.rawValue }

    /// `alert`: the worst of the machine's latest values; `waiting`: its agents that need you.
    static func of(_ m: Machine, alert: MetricLevel, waiting: Int) -> MachineConcern? {
        if m.isPending { return .pending } // waits for It's Mine: first of all
        if !m.online { return .offline }
        if alert == .critical { return .critical }
        if alert == .warning { return .warning }
        if waiting > 0 { return .needsYou }
        if m.nodeMismatch == true || m.deploying == true { return .mismatch }
        return nil
    }

    /// The machines with a concern first, most pressing first; the same concern and the rest keep the hub's order, so a
    /// card moves only when what it shows changes.
    static func ordered(_ machines: [Machine], by concern: (Machine) -> MachineConcern?) -> [Machine] {
        machines.enumerated()
            .map { (rank: concern($0.element)?.rawValue ?? Int.max, index: $0.offset, machine: $0.element) }
            .sorted { ($0.rank, $0.index) < ($1.rank, $1.index) }
            .map(\.machine)
    }
}

/// A machine's live agents by what they need: Needs You, Working, and Idle for the rest (finished, seen, unknown, and
/// waiting in the background: `Session.countedState`).
struct AgentTally: Equatable {
    var needsYou = 0
    var working = 0
    var idle = 0

    init(_ sessions: [Session]) {
        for s in sessions {
            switch s.countedState {
            case .needsYou: needsYou += 1
            case .working: working += 1
            default: idle += 1
            }
        }
    }
}

/// Machines (docs/server/machines.md「App」): every machine on one screen, what needs a look first. A card each: online or not,
/// the system, its agents (Needs You / Working / Idle), CPU / memory / network now and over the last hour, what is too high,
/// a build unlike the hub's with Redeploy. The machine list is live; the metrics refresh every 10 s while the page is
/// on screen and the app is in front. A tap opens the machine.
struct MachinesView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.scenePhase) private var phase
    @Environment(\.tabIsActive) private var inFront
    @State private var metrics: [MachineRef: MachineMetrics] = [:]
    /// The groups whose hub keeps no metrics (404: before machine monitoring): their cards have no CPU / memory / network.
    @State private var unmonitoredGroups = Set<String>()
    /// Add Computer is up.
    @State private var adding: AddComputerRequest?
    private var unmonitored: Bool { !unmonitoredGroups.isEmpty && unmonitoredGroups.count >= model.groupIDs.count }

    var body: some View {
        // Behind another section (the Mac keeps the page, `MacPageStack`): as they are now, not followed push by push.
        let machines = model.machines(inFront: inFront)
        let agents = MachineDetailView.agentsByMachine(model, inFront: inFront)
        let tallies = Dictionary(machines.map { ($0.ref, AgentTally(agents[$0.ref] ?? [])) }, uniquingKeysWith: { a, _ in a })
        let alerts = Dictionary(machines.map { ($0.ref, alertLevel($0)) }, uniquingKeysWith: { a, _ in a })
        ScrollView {
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                if !machines.isEmpty {
                    summary(machines, alerts: alerts.values.filter { $0 > .normal }, waiting: tallies.values.reduce(0) { $0 + $1.needsYou })
                }
                // Today's tokens across the computers, from their cards' usage (#4973).
                let shares = machines.compactMap { m in metrics[m.ref]?.usage.map { (name: m.name, usage: $0) } }
                if !shares.isEmpty { TodayTokensCard(shares: shares) }
                if model.isMultiHub {
                    // Several groups of hubs: one block each, under its name (docs/protocol/multi-hub.md).
                    ForEach(model.groupIDs, id: \.self) { g in
                        let mine = machines.filter { $0.group == g }
                        if !mine.isEmpty {
                            groupHeader(g, machines: mine)
                            grid(mine, tallies: tallies, alerts: alerts)
                        }
                    }
                } else {
                    grid(machines, tallies: tallies, alerts: alerts)
                }
                #if !os(macOS)
                UsageWindowsCard() // the Mac has it at the foot of its sections column
                #endif
                if machines.isEmpty, model.canAddComputer {
                    EmptyStateView(symbol: Theme.Icon.machines, title: String(localized: "No computers yet"),
                                   message: String(localized: "Add one and Persona can work on it for you."),
                                   action: (String(localized: "Add Computer"), { adding = .add }))
                } else if machines.isEmpty {
                    // A hub without machines_add: computers are added in its admin page, as before.
                    EmptyStateView(symbol: Theme.Icon.machines, title: String(localized: "No machines yet"), message: String(localized: "Once the hub is connected, the hub machine and the computers it connects to appear here."))
                } else if !unmonitored {
                    Text("CPU, memory and network update every 10 seconds. Charts show the last hour.")
                        .font(Theme.Font.label).foregroundStyle(.secondary)
                        .padding(.horizontal, Theme.Space.xs)
                }
            }
            .padding(Theme.Space.l)
        }
        #if os(macOS)
        .modifier(MacBelowTitleRowIfAsked()) // under Machines' title in the window: the cards start below it (#1243)
        #endif
        .themedGroupedPage()
        .navigationTitle("Machines")
        // Large wherever it is pushed (the Agents list's stack would make it inline, and the principal item below would
        // then hide the title); the bar's middle is only for Connecting… and the like.
        .navigationBarTitleDisplayMode(.large)
        .pageToolbar {
            #if os(macOS)
            // Empty while connected: its glass drew a thin line in the middle of the title bar's row (#830).
            ToolbarItem(placement: .principal) { ConnectionTitle() }.sharedBackgroundVisibility(.hidden)
            #else
            ToolbarItem(placement: .principal) { ConnectionTitle() }
            #endif
            if model.canAddComputer {
                ToolbarItem(placement: .primaryAction) {
                    Button { adding = .add } label: { Label("Add Computer", systemImage: Theme.Icon.add) }
                        .help("Add Computer")
                        .accessibilityIdentifier("machines.add")
                }
            }
        }
        .addComputerSheet($adding)
        .refreshable {
            // The requests hit different endpoints and write separate state, so start them together: one slow
            // hub must not keep the others waiting (nor the refresh spinner).
            async let refreshed: Void = model.refresh()
            async let metricsLoaded: Void = loadMetrics()
            #if !os(macOS)
            async let usageLoaded: Void = UsageWindowsStore.shared.load(model)
            #endif
            await refreshed
            await metricsLoaded
            #if !os(macOS)
            await usageLoaded
            #endif
        }
        #if os(macOS)
        .macRefreshes { // ⌘R: no pull to refresh on the Mac
            async let refreshed: Void = model.refresh()
            async let metricsLoaded: Void = loadMetrics()
            await refreshed
            await metricsLoaded
        }
        #endif
        .tabActiveTask(id: phase == .active) {
            guard phase == .active else { return } // in the background or another tab: nothing to draw, nothing to fetch
            while !Task.isCancelled {
                await loadMetrics()
                try? await Task.sleep(for: .seconds(10))
            }
        }
        #if !os(macOS)
        .tabActiveTask(id: phase == .active ? "usage" : "") {
            guard phase == .active else { return }
            await UsageWindowsStore.shared.poll(model)
        }
        #endif
    }

    /// The cards, what needs a look first.
    private func grid(_ machines: [Machine], tallies: [MachineRef: AgentTally], alerts: [MachineRef: MetricLevel]) -> some View {
        let ordered = MachineConcern.ordered(machines) { m in
            MachineConcern.of(m, alert: alerts[m.ref] ?? .normal, waiting: tallies[m.ref]?.needsYou ?? 0)
        }
        // The cards of a row are as tall as its tallest (`fillsRow`): a card with more alert chips or a two-line
        // subtitle left its neighbours short and their charts at another height (#905).
        return LazyVGrid(columns: [GridItem(.adaptive(minimum: Theme.Size.cardColumn), spacing: Theme.Space.m, alignment: .top)],
                         spacing: Theme.Space.m) {
            ForEach(ordered, id: \.ref) { m in
                NavigationLink(value: MachineDetailRoute(m.ref)) {
                    MachineCard(machine: m, tally: tallies[m.ref], metrics: metrics[m.ref], showsMetrics: !unmonitoredGroups.contains(m.group),
                                fillsRow: true)
                }
                .buttonStyle(.plain)
            }
        }
        .animation(Theme.Motion.layout, value: ordered.map(\.ref))
    }

    /// A group's name over its cards, with how its link is and how many of its machines are online.
    private func groupHeader(_ group: String, machines: [Machine]) -> some View {
        HStack(spacing: Theme.Space.sm) {
            if let live = model.socket(group) { HubStatusDot(live: live) }
            Text(model.groupName(group)).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).lineLimit(1)
            Text("\(machines.filter(\.online).count)/\(machines.count)").font(Theme.Font.label).foregroundStyle(.tertiary)
            Spacer(minLength: 0)
        }
        .padding(.horizontal, Theme.Space.xs)
        .padding(.top, Theme.Space.s)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("machines.group.\(group)")
    }

    /// The worst of a machine's latest values; normal while it is offline or has sent nothing lately.
    private func alertLevel(_ m: Machine) -> MetricLevel {
        guard m.online, let mm = metrics[m.ref], let s = mm.latest else { return .normal }
        return MetricAlert.of(s, cores: (mm.system ?? m.system)?.cores).first?.level ?? .normal
    }

    private func summary(_ machines: [Machine], alerts: [MetricLevel], waiting: Int) -> some View {
        let online = machines.filter(\.online).count
        // Wraps to a second line rather than cutting the last one off (Japanese did: #5648).
        return LarkFlowLayout(spacing: Theme.Space.l, lineSpacing: Theme.Space.xs) {
            Label("\(online)/\(machines.count) online", systemImage: Theme.Icon.dot)
                .labelStyle(DotLabel(color: online == machines.count ? Theme.positive : .secondary))
            if let worst = alerts.max() {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.errorFilled).foregroundStyle(MetricColor.level(worst))
                    Text("\(alerts.count) machines running high")
                }
            }
            if waiting > 0 {
                Label("\(waiting) agents need you", systemImage: Theme.Icon.dot)
                    .labelStyle(DotLabel(color: StatusPalette.needsYou.dot))
            }
        }
        .font(Theme.Font.captionMedium)
        .lineLimit(1)
        .minimumScaleFactor(0.8)
        .padding(.horizontal, Theme.Space.xs)
    }

    /// Every group's metrics (each hub keeps its own machines').
    private func loadMetrics() async {
        // Every group at once, and only those whose link is up: a hub out of reach held the others' numbers back by its
        // requests' 30 s every round (#4705). One that is not asked keeps its last numbers.
        let apis = Dictionary(model.groupIDs.compactMap { g in model.api(g).map { (g, $0) } }, uniquingKeysWith: { a, _ in a })
        let asked = HubFanOut.asked(model.groupIDs.filter { apis[$0] != nil }) { model.socket($0)?.looksConnected == true }
        await HubFanOut.run(asked, fetch: { g in try await apis[g]!.metricsOverview() }) { g, result in
            switch result {
            case .success(let fresh):
                var all = metrics.filter { $0.key.group != g }
                for m in fresh where all[MachineRef(group: g, id: m.machineID)] == nil { all[MachineRef(group: g, id: m.machineID)] = m }
                metrics = all
                unmonitoredGroups.remove(g)
            case .failure(let error):
                // A hub before machine monitoring: the cards go without numbers. Other errors keep the last numbers (the
                // title says when the hub is out of reach).
                if let e = error as? APIError, e.status == 404 { unmonitoredGroups.insert(g) }
            }
        }
    }

}

struct DotLabel: LabelStyle {
    var color: Color
    func makeBody(configuration: Configuration) -> some View {
        HStack(spacing: Theme.Space.sm) {
            Circle().fill(color).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
            configuration.title
        }
    }
}

/// One machine: on Machines with its numbers, and at the top of its page without them (the page has the charts).
struct MachineCard: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dynamicTypeSize) private var typeSize
    var machine: Machine
    /// Its agents counted; nil counts them here.
    var tally: AgentTally?
    /// Its latest values (and for Machines the last hour); nil until they come, or from a hub without machine monitoring.
    var metrics: MachineMetrics?
    var showsMetrics = true
    /// Off at the top of its own page, whose title is the name already.
    var showsName = true
    /// In Machines' grid: as tall as the row, an online card's counts and charts at the bottom so they line up across it
    /// (an offline card keeps its words at the top). At the top of a machine's page it is as tall as its content.
    var fillsRow = false

    var body: some View {
        let system = machine.system ?? metrics?.system
        let sample = machine.online ? metrics?.latest : nil
        let alerts = sample.map { MetricAlert.of($0, cores: system?.cores) } ?? []
        let worst = alerts.first?.level ?? .normal
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                HStack(spacing: Theme.Space.m) {
                    OSBadge(system: system, fallback: machine.symbol, size: Theme.Size.avatarM, online: machine.online)
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        // Color.primary / Color.secondary, not .primary / .secondary: the hierarchical styles come out a step
                        // lighter where the card sits in a section header.
                        if showsName {
                            Text(machine.name).font(Theme.Font.heading).foregroundStyle(Color.primary).lineLimit(1)
                        }
                        // One line: "4 cores" no longer breaks in the middle on a narrow card.
                        Text(Self.subtitle(machine, system: system)).font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1)
                    }
                    Spacer(minLength: Theme.Space.sm)
                    status
                }
                if !alerts.isEmpty {
                    FlowLayoutRow {
                        ForEach(alerts, id: \.label) { a in AlertChip(alert: a) }
                    }
                }
            }
            .frame(maxHeight: fillsRow && machine.online ? .infinity : nil, alignment: .top)
            if machine.online {
                VStack(alignment: .leading, spacing: Theme.Space.m) {
                    AgentCounts(tally: tally ?? AgentTally(MachineDetailView.agents(model, machine.ref)))
                    if showsMetrics { metricsRow(sample) }
                }
                // Sized before the stretching part above: second in line, the values (which may shrink to fit) came
                // out smaller than in a card of their own.
                .layoutPriority(1)
            } else if let manager = model.offlineManager(of: machine, group: machine.group) {
                // Offline with its manager on a hub with several (#1919): the hub's words would blame the computer.
                Text("Its manager “\(manager.name)” is offline. It comes back online once the manager is back.")
                    .font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(3)
                    .accessibilityIdentifier("machine.managerOffline")
            } else if let lines = AddComputerText.machineLines(machine) {
                // A known error_code in words (docs/assistant/onboarding.md 5.3), or the hub's error read in plain words with
                // the raw one under it; the whole of it is on its page (Details).
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    // Three lines for why and what to do (English and Japanese need them for the step); a raw error keeps two.
                    Text(lines.line).font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(lines.detail == nil ? 2 : 3)
                    if let detail = lines.detail {
                        Text(detail).font(Theme.Font.caption).foregroundStyle(.tertiary).lineLimit(1)
                    }
                }
            }
            if machine.isPending, model.canConfirmComputer(machine.group) {
                PendingRow(machine: machine)
            }
            if machine.nodeMismatch == true || machine.deploying == true {
                DeployRow(machine: machine)
            }
        }
        .frame(maxHeight: fillsRow ? .infinity : nil, alignment: .top)
        .card(padding: 14)
        .overlay {
            if worst > .normal {
                RoundedRectangle(cornerRadius: Theme.Radius.card, style: .continuous)
                    .stroke(MetricColor.level(worst).opacity(0.7), lineWidth: Theme.Size.lineMedium)
            }
        }
        .contentShape(.rect)
    }

    /// "Ubuntu 24.04.1 LTS · 16 cores · 64.0 GB"; the host name when the machine never said what it runs.
    static func subtitle(_ m: Machine, system: MachineSystem?) -> String {
        guard let sys = system else { return m.hostname ?? m.sshHost ?? m.id }
        var parts = [sys.name]
        if sys.cores > 0 { parts.append(String(localized: "\(sys.cores) cores")) }
        if sys.memory > 0 { parts.append(MetricFormat.bytes(sys.memory)) }
        return parts.joined(separator: " · ")
    }

    @ViewBuilder private var status: some View {
        if machine.isPending {
            Text("Waiting for you [Chat View 99e510]").font(Theme.Font.labelStrong).foregroundStyle(Theme.cautionText).lineLimit(1).fixedSize()
                .accessibilityIdentifier("machine.pending")
        } else if machine.neverConnected {
            Text("Not connected yet").font(Theme.Font.labelStrong).foregroundStyle(Color.secondary).lineLimit(1).fixedSize()
        } else if machine.online {
            HStack(spacing: Theme.Space.sm) {
                Circle().fill(Theme.positive).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                Text("Online").font(Theme.Font.labelStrong).foregroundStyle(Theme.positive)
            }
            .fixedSize()
        } else {
            VStack(alignment: .trailing, spacing: Theme.Space.xxs) {
                Text("Offline [Machine Entity be1b4f]").font(Theme.Font.labelStrong).foregroundStyle(Color.secondary)
                if let seen = machine.lastSeen, seen.timeIntervalSince1970 > 0 {
                    Text(Fmt.relative(seen)).font(Theme.Font.label).foregroundStyle(.tertiary)
                }
            }
            .fixedSize()
        }
    }

    /// Side by side; at the accessibility text sizes one under another, where four in a row left each a quarter of the
    /// card and broke its title a character a line (#1659).
    private func metricsRow(_ s: MetricSample?) -> some View {
        Group {
            if typeSize.isAccessibilitySize {
                VStack(alignment: .leading, spacing: Theme.Space.m) { metricCells(s) }
            } else {
                HStack(alignment: .top, spacing: Theme.Space.m) { metricCells(s) }
            }
        }
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private func metricCells(_ s: MetricSample?) -> some View {
        let points = metrics?.points ?? []
        let end = Date.now
        let domain = end.addingTimeInterval(-3600)...end
        MetricCell(title: "CPU", value: MetricFormat.percent(s?.cpu), detail: s?.load.map { String(localized: "Load \(MetricFormat.load($0))") },
                   level: MetricLevel.of(s?.cpu, warn: 75, crit: 90)) {
            Sparkline(points: points, series: [ChartSeries(name: "CPU", color: MetricColor.cpu) { $0.cpu }], domain: domain, yMax: 100)
        }
        MetricCell(title: String(localized: "Memory [Machine Detail View 7d8f8c]"), value: MetricFormat.percent(s?.memPercent),
                   detail: s.map { MetricFormat.bytes($0.memUsed) + " / " + MetricFormat.bytes($0.memTotal) },
                   level: MetricLevel.of(s?.memPercent, warn: 80, crit: 90)) {
            let total = Double(s?.memTotal ?? metrics?.system?.memory ?? 0)
            Sparkline(points: points, series: [ChartSeries(name: String(localized: "Memory [Machine Detail View 7d8f8c]"), color: MetricColor.mem) { p in
                p.mem.map { total > 0 ? 100 * $0 / total : 0 }
            }], domain: domain, yMax: 100)
        }
        MetricCell(title: String(localized: "Network"), value: s == nil ? "—" : "↓ " + MetricFormat.rate(s?.netRx),
                   detail: s == nil ? nil : "↑ " + MetricFormat.rate(s?.netTx), level: .normal) {
            Sparkline(points: points, series: [
                ChartSeries(name: String(localized: "Down"), color: MetricColor.rx) { $0.rx },
                ChartSeries(name: String(localized: "Up"), color: MetricColor.tx) { $0.tx },
            ], domain: domain, yMax: nil)
        }
        if let usage = metrics?.usage {
            MetricCell(title: String(localized: "Tokens"), value: TokenFormat.perMinute(usage.perMinute),
                       detail: String(localized: "Today \(TokenFormat.count(usage.today.total))"), level: .normal) {
                let (minutes, value) = Self.usageSparkline(usage, now: end)
                Sparkline(points: minutes, series: [ChartSeries(name: String(localized: "Tokens"), color: UsageColor.total) { value[$0.t] }],
                          domain: domain, yMax: nil)
            }
        }
    }

    /// The last hour of all tokens by minute as points for a sparkline, and each minute's value.
    static func usageSparkline(_ usage: UsageCard, now: Date) -> ([MetricPoint], [Int64: Double]) {
        let minute = Int64(now.timeIntervalSince1970) / 60 * 60
        var values: [Int64: Double] = [:]
        let points = usage.recent.enumerated().map { i, n in
            let t = minute - Int64(usage.recent.count - 1 - i) * 60
            values[t] = Double(n)
            return MetricPoint(t: t)
        }
        return (points, values)
    }
}

/// Today's tokens across the computers (#4973): the total, every token per minute now, the cache hit rate, and each
/// computer's share as a bar in shades of the text ink (the four kinds' colors are the charts').
struct TodayTokensCard: View {
    var shares: [(name: String, usage: UsageCard)]
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let today = shares.reduce(UsageTokens()) { $0 + $1.usage.today }
        let perMinute = shares.reduce(Int64(0)) { $0 + $1.usage.perMinute }
        let ranked = shares.filter { $0.usage.today.total > 0 }.sorted { $0.usage.today.total > $1.usage.today.total }
        VStack(alignment: .leading, spacing: Theme.Space.m) {
            Text("Today's Tokens").font(Theme.Font.heading)
            // Side by side; at the accessibility text sizes one under another, where a third of the card each cut them
            // to a character (#1929).
            let cells = Group {
                cell(String(localized: "Total [Token Usage]"), TokenFormat.count(today.total))
                cell(String(localized: "Now [Token Usage]"), TokenFormat.perMinute(perMinute))
                cell(String(localized: "Cache Hit Rate"), today.hitRate.map { $0.formatted(.percent.precision(.fractionLength(0))) } ?? "—")
            }
            if typeSize.isAccessibilitySize {
                VStack(alignment: .leading, spacing: Theme.Space.s) { cells }
            } else {
                HStack(alignment: .top, spacing: Theme.Space.m) { cells }
            }
            if today.total > 0 {
                GeometryReader { g in
                    // A small share's minimum width comes out of the large ones, so the bar ends at the card's edge.
                    let widths = ShareBar.widths(ranked.map { Double($0.usage.today.total) }, width: g.size.width,
                                                 gap: Theme.Space.xxs, minimum: Theme.Size.dotXS)
                    HStack(spacing: Theme.Space.xxs) {
                        ForEach(Array(widths.enumerated()), id: \.offset) { i, w in
                            Capsule().fill(Color.primary.opacity(Self.shade(i))).frame(width: w)
                        }
                    }
                }
                .scaledFrame(height: Theme.Size.dotS)
                .accessibilityHidden(true)
                FlowLayoutRow {
                    ForEach(Array(ranked.enumerated()), id: \.offset) { i, s in
                        HStack(spacing: Theme.Space.xs) {
                            Circle().fill(Color.primary.opacity(Self.shade(i))).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                            Text(s.name).font(Theme.Font.label).lineLimit(1)
                            Text(TokenFormat.count(s.usage.today.total)).font(Theme.Font.label.monospacedDigit()).foregroundStyle(.secondary)
                        }
                        .accessibilityElement(children: .combine)
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: 14)
        .accessibilityIdentifier("machines.tokens")
    }

    /// The share bar's shade for the i-th largest: darkest first.
    static func shade(_ i: Int) -> Double { max(0.85 - Double(i) * 0.2, 0.2) }

    private func cell(_ title: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(title).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            Text(value).font(Theme.Font.bodyStrong.monospacedDigit()).lineLimit(1).minimumScaleFactor(0.7)
                .contentTransition(.numericText())
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }
}

/// Needs You / Working / Idle side by side, a dot and a count each; a state with none stays gray.
struct AgentCounts: View {
    var tally: AgentTally
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        // Side by side; at the accessibility text sizes one under another, where three in a row kept a character of
        // each state's word (#1929).
        let items = Group {
            item(tally.needsYou, StateName.needsYou.text, tone: StatusPalette.needsYou, ink: StatusPalette.needsYou.text)
            item(tally.working, StateName.working.text, tone: StatusPalette.working, ink: StatusPalette.working.text)
            item(tally.idle, String(localized: "Idle"), tone: StatusPalette.idle, ink: Color.primary)
        }
        Group {
            if typeSize.isAccessibilitySize {
                VStack(alignment: .leading, spacing: Theme.Space.xs) { items }
            } else {
                HStack(spacing: Theme.Space.s) { items }
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        .background(Color.primary.opacity(0.04), in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }

    private func item(_ n: Int, _ label: String, tone: StatusTone, ink: Color) -> some View {
        HStack(spacing: Theme.Space.sm) {
            Circle().fill(n > 0 ? tone.dot : StatusPalette.offline.dot).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
            Text("\(n)").font(Theme.Font.bodyStrong.monospacedDigit()).foregroundStyle(n > 0 ? ink : Color.secondary)
                .contentTransition(.numericText(value: Double(n)))
            Text(label).font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(n) agents \(label)")
    }
}

/// CPU / Memory / Network on a card: the value now, a detail under it, the last hour under that.
struct MetricCell<Chart: View>: View {
    var title: String
    var value: String
    var detail: String?
    var level: MetricLevel
    @ViewBuilder var chart: Chart

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            HStack(spacing: Theme.Space.xs) {
                Text(title).font(Theme.Font.label).foregroundStyle(Color.secondary)
                if level > .normal {
                    ThemedIcon(Theme.Icon.errorFilled).font(Theme.Font.label).imageScale(.small).foregroundStyle(MetricColor.level(level))
                }
            }
            Text(value).font(Theme.Font.bodyStrong.monospacedDigit()).foregroundStyle(Color.primary).lineLimit(1).minimumScaleFactor(0.7)
            Text(detail ?? " ").font(Theme.Font.label.monospacedDigit()).foregroundStyle(Color.secondary).lineLimit(1).minimumScaleFactor(0.7)
            chart.scaledFrame(height: Theme.Size.wellS).padding(.top, Theme.Space.xxs)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// What is too high: the icon carries the level's color, the words stay in text ink (warning yellow is unreadable
/// as text on a light card).
struct AlertChip: View {
    var alert: MetricAlert

    var body: some View {
        let color = MetricColor.level(alert.level)
        HStack(spacing: Theme.Space.xs) {
            ThemedIcon(alert.level == .critical ? Theme.Icon.critical : Theme.Icon.errorFilled)
                .foregroundStyle(color)
            Text(alert.label).foregroundStyle(Color.primary)
        }
        .font(Theme.Font.labelStrong)
        .lineLimit(1)
        .padding(.horizontal, Theme.Space.s)
        .padding(.vertical, Theme.Space.xs)
        .background(color.opacity(0.16), in: .capsule)
        .accessibilityLabel(alert.level == .critical ? String(localized: "Critical: \(alert.label)") : String(localized: "Warning: \(alert.label)"))
    }
}

/// A machine whose personad is not the hub's build: says so, and Redeploy has the hub install its own build there.
struct DeployRow: View {
    @Environment(AppModel.self) private var model
    var machine: Machine
    @State private var starting = false

    var body: some View {
        let busy = machine.deploying == true || starting
        HStack(spacing: Theme.Space.m) {
            Label(text(busy), systemImage: busy ? Theme.Icon.retry : Theme.Icon.errorFilled)
                .font(Theme.Font.label)
                .foregroundStyle(Theme.cautionText)
                .frame(maxWidth: .infinity, alignment: .leading)
            Button { Task { await deploy() } } label: {
                if busy {
                    ProgressView().controlSize(.small)
                } else {
                    Text("Redeploy").font(Theme.Font.labelStrong).lineLimit(1)
                }
            }
            .themedButton()
            .disabled(busy)
            .accessibilityLabel(busy ? String(localized: "Deploying") : String(localized: "Redeploy"))
        }
    }

    private func text(_ busy: Bool) -> String {
        if busy { return String(localized: "Installing the hub's Persona build on this computer…") }
        if let e = machine.deployError, !e.isEmpty { return String(localized: "Last deploy failed: \(e)") }
        return String(localized: "Persona on this computer isn't from the same deploy as the hub")
    }

    private func deploy() async {
        starting = true
        defer { starting = false }
        do {
            try await model.api?.deploy(machine: machine.id)
            Haptics.tap()
            await model.refresh()
        } catch {
            if let m = error.shown { model.toast = Toast(String(localized: "Deploy failed: \(m)"), style: .error) }
        }
    }
}

/// A computer waiting for the user (feature machine_trust): It's Mine / Not Mine once it is online (it can be looked at),
/// Remove while it is not.
struct PendingRow: View {
    @Environment(AppModel.self) private var model
    var machine: Machine
    @State private var busy = false

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            if machine.online {
                Button { Task { await run(confirm: true) } } label: { Text("It's Mine").font(Theme.Font.labelStrong).lineLimit(1) }
                    .themedButton(prominent: true)
                    .accessibilityIdentifier("machine.confirm")
                Button(role: .destructive) { Task { await run(confirm: false) } } label: { Text("Not Mine").font(Theme.Font.labelStrong).lineLimit(1) }
                    .themedButton()
                    .accessibilityIdentifier("machine.reject")
            } else {
                Button(role: .destructive) { Task { await run(confirm: false) } } label: { Text("Remove [Add Computer View c94330]").font(Theme.Font.labelStrong).lineLimit(1) }
                    .themedButton()
                    .accessibilityIdentifier("machine.reject")
            }
            if busy { ProgressView().controlSize(.small) }
            Spacer(minLength: 0)
        }
        .disabled(busy)
    }

    private func run(confirm: Bool) async {
        guard let api = model.api(machine.group) else { return }
        busy = true
        defer { busy = false }
        do {
            if confirm { try await api.confirmMachine(machine.id) } else { try await api.rejectMachine(machine.id) }
            confirm ? Haptics.success() : Haptics.tap()
            model.toast = Toast(confirm ? String(localized: "Added: \(machine.name)") : String(localized: "Removed “\(machine.name)”"), style: .success)
            await model.refresh()
        } catch {
            model.toast = Toast(AddComputerText.failure(error).message, style: .error)
            await model.refresh()
        }
    }
}
