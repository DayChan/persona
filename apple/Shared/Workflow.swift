import Foundation

/// A multi-agent workflow an agent runs in the background (Claude Code's Workflow tool): its turn ends while the
/// workflow's agents work, and the workflow's completion wakes it again. The hub's `workflows` on a session
/// (docs/protocol/protocol.md); older hubs send none, and an odd entry never drops the session.
nonisolated struct WorkflowRun: Codable, Identifiable, Hashable, Sendable {
    var taskID: String
    var runID: String?
    var name: String?
    var summary: String?
    /// running / completed / failed / killed / stalled (no end notice and 30 minutes without a move: not running).
    var status: String?
    var running: Bool?
    /// The script's declared phases; `phase` is the one the latest running agent is in, `phaseIndex` from 1 (0: unknown).
    var phases: [String]?
    var phase: String?
    var phaseIndex: Int?
    var phaseCount: Int?
    /// Each phase's counts over all its agents (not only the 60 listed), in `phases` order, then undeclared ones; a
    /// title "" is the agents whose phase is not known. Older hubs send none: `phaseBlocks` counts the listed agents.
    var phaseStats: [WorkflowPhaseStats]?
    /// The Workflow call that launched it: its card goes in the chat in that call's place (#3436).
    var toolUseID: String?
    /// The total is only known once it ends (the script launches as it goes): shown as done / started.
    var agentsStarted: Int?
    var agentsDone: Int?
    var agentsRunning: Int?
    /// Agents that failed, apart from done (#5511); older hubs count them as done and leave this out.
    var agentsFailed: Int?
    /// Sums over all its agents: each one's latest usage (not what it spent) and its tool calls.
    var tokens: Int?
    var calls: Int?
    var startedAt: Date?
    var endedAt: Date?
    var updatedAt: Date?
    /// A failed / killed one's first line.
    var error: String?
    /// Its agents in the order they started (at most 60, the newest kept).
    var agents: [WorkflowAgent]?

    var id: String { runID ?? taskID }

    enum CodingKeys: String, CodingKey {
        case name, summary, status, running, phases, phase, error, agents, tokens, calls
        case taskID = "task_id", runID = "run_id", phaseIndex = "phase_index", phaseCount = "phase_count",
             phaseStats = "phase_stats", toolUseID = "tool_use_id", agentsStarted = "agents_started",
             agentsDone = "agents_done", agentsRunning = "agents_running", agentsFailed = "agents_failed",
             startedAt = "started_at", endedAt = "ended_at", updatedAt = "updated_at"
    }

    init(taskID: String, runID: String? = nil, name: String? = nil, summary: String? = nil, status: String? = "running",
         running: Bool? = true, phases: [String]? = nil, phase: String? = nil, phaseIndex: Int? = nil,
         phaseStats: [WorkflowPhaseStats]? = nil, toolUseID: String? = nil, agentsStarted: Int? = nil,
         agentsDone: Int? = nil, agentsRunning: Int? = nil, agentsFailed: Int? = nil, startedAt: Date? = nil,
         endedAt: Date? = nil, updatedAt: Date? = nil, error: String? = nil, agents: [WorkflowAgent]? = nil) {
        self.taskID = taskID
        self.runID = runID
        self.name = name
        self.summary = summary
        self.status = status
        self.running = running
        self.phases = phases
        self.phase = phase
        self.phaseIndex = phaseIndex
        self.phaseStats = phaseStats
        self.toolUseID = toolUseID
        self.agentsStarted = agentsStarted
        self.agentsDone = agentsDone
        self.agentsRunning = agentsRunning
        self.agentsFailed = agentsFailed
        self.startedAt = startedAt
        self.endedAt = endedAt
        self.updatedAt = updatedAt
        self.error = error
        self.agents = agents
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        runID = try? c.decodeIfPresent(String.self, forKey: .runID)
        // One with neither id is not one the app can tell apart: it is left out (`Lossy`).
        taskID = try (try? c.decodeIfPresent(String.self, forKey: .taskID)) ?? runID ?? { throw DecodingError.keyNotFound(CodingKeys.taskID, .init(codingPath: c.codingPath, debugDescription: "no task_id or run_id")) }()
        name = try? c.decodeIfPresent(String.self, forKey: .name)
        summary = try? c.decodeIfPresent(String.self, forKey: .summary)
        status = try? c.decodeIfPresent(String.self, forKey: .status)
        running = try? c.decodeIfPresent(Bool.self, forKey: .running)
        phases = try? c.decodeIfPresent([String].self, forKey: .phases)
        phase = try? c.decodeIfPresent(String.self, forKey: .phase)
        phaseIndex = try? c.decodeIfPresent(Int.self, forKey: .phaseIndex)
        phaseCount = try? c.decodeIfPresent(Int.self, forKey: .phaseCount)
        // One phase the app cannot read leaves the others (and a run with none counts its listed agents).
        phaseStats = (try? c.decodeIfPresent([Lossy<WorkflowPhaseStats>].self, forKey: .phaseStats))?.compactMap(\.value)
        toolUseID = (try? c.decodeIfPresent(String.self, forKey: .toolUseID)).flatMap { $0.isEmpty ? nil : $0 }
        agentsStarted = try? c.decodeIfPresent(Int.self, forKey: .agentsStarted)
        agentsDone = try? c.decodeIfPresent(Int.self, forKey: .agentsDone)
        agentsRunning = try? c.decodeIfPresent(Int.self, forKey: .agentsRunning)
        agentsFailed = try? c.decodeIfPresent(Int.self, forKey: .agentsFailed)
        tokens = try? c.decodeIfPresent(Int.self, forKey: .tokens)
        calls = try? c.decodeIfPresent(Int.self, forKey: .calls)
        startedAt = try? c.decodeIfPresent(Date.self, forKey: .startedAt)
        endedAt = try? c.decodeIfPresent(Date.self, forKey: .endedAt)
        updatedAt = try? c.decodeIfPresent(Date.self, forKey: .updatedAt)
        error = try? c.decodeIfPresent(String.self, forKey: .error)
        // One agent the app cannot read leaves the others.
        agents = (try? c.decodeIfPresent([Lossy<WorkflowAgent>].self, forKey: .agents))?.compactMap(\.value)
    }

    /// The hub's `running`; from one without it, status running. An unknown status reads as completed.
    var isRunning: Bool { running ?? (status == "running") }

    enum Outcome: Sendable { case running, completed, failed, stopped, stalled }

    var outcome: Outcome {
        if isRunning { return .running }
        switch status {
        case "failed": return .failed
        case "killed": return .stopped
        case "stalled": return .stalled
        default: return .completed
        }
    }

    /// What to call it: its name, else its summary, else 「Workflow」.
    var title: String {
        if let n = name, !n.isEmpty { return n }
        if let d = summary, !d.isEmpty { return d }
        return "Workflow"
    }

    /// Done / started (the total is not known while it runs); counted from the agents when the hub sends no counts.
    var progress: (done: Int, started: Int)? {
        let listed = agents ?? []
        let done = agentsDone ?? listed.filter { $0.runState == .done }.count
        let started = max(agentsStarted ?? listed.count, done)
        return started > 0 ? (done, started) : nil
    }

    var runningAgents: Int { agentsRunning ?? (agents ?? []).filter(\.running).count }

    /// 「169/185+」: done of those started so far, with "+" while it runs (the script starts more as it goes); without it
    /// once it ended. Nil when nothing started (as far as the hub says).
    var counts: String? { progress.map { "\($0.done)/\($0.started)" + (isRunning ? "+" : "") } }

    /// The phase, and where it sits among the declared ones: 「Verify 2/2」.
    var phaseLabel: String? {
        guard let p = phase, !p.isEmpty else { return nil }
        let n = phases?.count ?? 0
        if let i = phaseIndex, i >= 1, n > 1 { return "\(p) \(i)/\(n)" }
        return p
    }

    /// One line (list rows, task cards): "Background workflow “parity-audit” · Verify 2/2 · 169/185+"; the time apart.
    var line: String { ([lineName] + lineDetails()).joined(separator: " · ") }

    /// The line's name part, "Background workflow “parity-audit”": the part a row cuts short (`WorkflowLine`).
    var lineName: String {
        isRunning ? String(localized: "Background workflow “\(title)”") : String(localized: "workflow「\(title)」")
    }

    /// What the line says after its name: the phase (unless `phase` is false: a row too narrow for it), done / started
    /// and how it ended ("Verify 2/2", "169/185+").
    func lineDetails(phase: Bool = true) -> [String] {
        var parts: [String] = []
        if phase, let p = phaseLabel { parts.append(p) }
        if let c = counts { parts.append(c) }
        switch outcome {
        case .running, .completed: break
        case .failed: parts.append(String(localized: "Failed"))
        case .stopped: parts.append(String(localized: "Stopped [Port Chips f00645]"))
        case .stalled: parts.append(String(localized: "Stalled"))
        }
        return parts
    }

    /// Its agents by how they stand (the hub's counts; from the listed agents when it sends none). While it runs, the
    /// rest is not known yet: only once it ended are the ones not back "stopped" (a hub before #5511 counts failures
    /// as done).
    var tally: WorkflowTally {
        let listed = agents ?? []
        let started = max(0, agentsStarted ?? listed.count)
        let done = min(started, max(0, progress?.done ?? 0))
        let running = isRunning ? min(started - done, max(0, runningAgents)) : 0
        let failedListed = agentsDone == nil ? listed.filter { $0.runState == .failed }.count : 0
        let failed = min(started - done - running, max(0, agentsFailed ?? failedListed))
        let stopped = isRunning ? 0 : started - done - running - failed
        return WorkflowTally(started: started, done: done, running: running, failed: failed, stopped: stopped)
    }

    /// While it runs, the phase it is in and its newest agent running there: 「当前 Verify：verify:chat-rich」.
    var now: (phase: String, agent: String)? {
        guard isRunning else { return nil }
        let listed = (agents ?? []).reversed()
        let p = phase ?? ""
        let lead = listed.first { $0.running && (p.isEmpty || $0.phase == p) } ?? listed.first(where: \.running)
        let agent = lead?.title ?? ""
        return p.isEmpty && agent.isEmpty ? nil : (p, agent)
    }

    /// Its phases, one block each, in the script's order (then undeclared ones, then the agents without a phase); none
    /// for a run that names no phases and lists no agents. A hub without `phase_stats` (#3165) has them counted from
    /// the listed agents, which undercounts once a run has more agents than it lists.
    var phaseBlocks: [WorkflowPhaseBlock] {
        let listed = agents ?? []
        let stats = phaseStats?.isEmpty == false ? phaseStats! : Self.stats(phases: phases ?? [], agents: listed)
        if stats.count == 1, stats[0].title.isEmpty, stats[0].started == 0 { return [] }
        let titles = Set(stats.map(\.title))
        let currentAt: Int? = if let p = phase, !p.isEmpty, titles.contains(p) {
            stats.firstIndex { $0.title == p }
        } else {
            isRunning ? stats.firstIndex { $0.running > 0 } : nil
        }
        return stats.enumerated().map { i, st in
            let current = isRunning && i == currentAt
            let running = isRunning ? st.running : 0
            let state: WorkflowPhaseBlock.State
            if running > 0 || current {
                state = .current
            } else if st.started == 0 {
                // None started (as far as the hub says): passed when the run went on beyond it; the phase an ended run
                // stopped in (a hub that lists neither its agents nor phase_stats) did run, done or not.
                if let at = currentAt, i < at { state = .done }
                else if i == currentAt, !isRunning { state = outcome == .completed ? .done : .stopped }
                else { state = .next }
            } else if st.done + st.failed >= st.started {
                state = .done
            } else {
                state = isRunning ? .done : .stopped
            }
            let own = listed.filter { a in (a.phase ?? "") == st.title || (st.title.isEmpty && !titles.contains(a.phase ?? "")) }
            return WorkflowPhaseBlock(index: i, title: st.title, started: st.started, done: st.done, running: running,
                                      failed: st.failed, agents: own, state: state, current: current,
                                      growing: isRunning && (running > 0 || current))
        }
    }

    /// Counts from the listed agents, for a hub without `phase_stats`: the declared phases first, then any other an
    /// agent names, then the agents without one.
    static func stats(phases: [String], agents: [WorkflowAgent]) -> [WorkflowPhaseStats] {
        var titles = phases
        for a in agents where !titles.contains(a.phase ?? "") { titles.append(a.phase ?? "") }
        return titles.map { t in
            let own = agents.filter { ($0.phase ?? "") == t }
            return WorkflowPhaseStats(title: t, started: own.count, done: own.filter { $0.runState == .done }.count,
                                      running: own.filter(\.running).count, failed: own.filter { $0.runState == .failed }.count)
        }
    }

    /// 「12 秒」「4 分 12 秒」「1 时 3 分」; minutes only when `seconds` is false (rows that tick every minute): 「14 分钟」.
    static func duration(_ t: TimeInterval, seconds: Bool = true) -> String {
        let s = max(0, Int(t))
        if !seconds {
            if s < 60 { return String(localized: "Under 1 min") }
            return s < 3600 ? String(localized: "\(s / 60) min [Task Views 135d12]") : String(localized: "\(s / 3600)h \(s % 3600 / 60)m [Machine Metrics 204e89]")
        }
        if s < 60 { return String(localized: "\(s) s") }
        if s < 3600 { return String(localized: "\(s / 60)m \(s % 60)s") }
        return String(localized: "\(s / 3600)h \(s % 3600 / 60)m [Session Panes c667a6]")
    }

    /// How long it has run (to now while running).
    func elapsed(now: Date = .now) -> TimeInterval? {
        guard let s = startedAt else { return nil }
        return max(0, (endedAt ?? (isRunning ? now : updatedAt ?? now)).timeIntervalSince(s))
    }
}

/// One agent of a workflow, read as a subagent through `sessionID` ("<session>@<agent id>").
nonisolated struct WorkflowAgent: Codable, Identifiable, Hashable, Sendable {
    var id: String
    var sessionID: String?
    var label: String?
    var phase: String?
    var running: Bool
    /// running / done / failed / stopped (it had not come back when the run ended, #5511); older hubs send none.
    var status: String?
    var startedAt: Date?
    /// When it came back (absent while it runs); older hubs send none (`updatedAt` then).
    var endedAt: Date?
    var updatedAt: Date?
    var lastText: String?
    var model: String?
    /// Its latest tool call, kept after its last words: what it does now ("Reading example.go").
    var latestTool: WorkflowTool?

    enum CodingKeys: String, CodingKey {
        case id, label, phase, running, status, model
        case sessionID = "session_id", startedAt = "started_at", endedAt = "ended_at", updatedAt = "updated_at",
             lastText = "last_text", latestTool = "latest_tool"
    }

    init(id: String, sessionID: String? = nil, label: String? = nil, phase: String? = nil, running: Bool = false,
         status: String? = nil, startedAt: Date? = nil, endedAt: Date? = nil, updatedAt: Date? = nil, lastText: String? = nil,
         model: String? = nil, latestTool: WorkflowTool? = nil) {
        self.id = id
        self.sessionID = sessionID
        self.label = label
        self.phase = phase
        self.running = running
        self.status = status
        self.startedAt = startedAt
        self.endedAt = endedAt
        self.updatedAt = updatedAt
        self.lastText = lastText
        self.model = model
        self.latestTool = latestTool
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        sessionID = try? c.decodeIfPresent(String.self, forKey: .sessionID)
        label = try? c.decodeIfPresent(String.self, forKey: .label)
        phase = try? c.decodeIfPresent(String.self, forKey: .phase)
        running = ((try? c.decodeIfPresent(Bool.self, forKey: .running)) ?? nil) ?? false
        status = try? c.decodeIfPresent(String.self, forKey: .status)
        startedAt = try? c.decodeIfPresent(Date.self, forKey: .startedAt)
        endedAt = try? c.decodeIfPresent(Date.self, forKey: .endedAt)
        updatedAt = try? c.decodeIfPresent(Date.self, forKey: .updatedAt)
        lastText = try? c.decodeIfPresent(String.self, forKey: .lastText)
        model = try? c.decodeIfPresent(String.self, forKey: .model)
        latestTool = try? c.decodeIfPresent(WorkflowTool.self, forKey: .latestTool)
    }

    var title: String { (label?.isEmpty == false ? label : nil) ?? id }

    /// How it stands: running, back with its result, failed, or stopped by the run's end.
    enum RunState: Sendable { case running, done, failed, stopped }

    var runState: RunState {
        if running { return .running }
        switch status {
        case "failed": return .failed
        case "stopped": return .stopped
        default: return .done
        }
    }

    /// When it stopped running; nil while it runs or when the hub does not say.
    var end: Date? { running ? nil : endedAt ?? updatedAt }

    /// How long it ran, once it is back (a running one's time is a live clock, `Text(timerInterval:)`).
    var took: TimeInterval? {
        guard let s = startedAt, let e = end else { return nil }
        return max(0, e.timeIntervalSince(s))
    }

    /// Its model and latest tool, for a wide row: "claude-sonnet · Reading example.go"; nil when the hub says neither.
    var detail: String? {
        let tool = latestTool.map { t in t.summary.flatMap { $0.trimmingCharacters(in: .whitespaces).isEmpty ? nil : $0 } ?? t.name }
        let parts = [Fmt.model(model?.trimmingCharacters(in: .whitespaces)), tool].compactMap { $0 }.filter { !$0.isEmpty }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

/// A workflow agent's latest tool call (`latest_tool`).
nonisolated struct WorkflowTool: Codable, Hashable, Sendable {
    var name: String
    /// What the call does, in the request's language ("Reading example.go").
    var summary: String?
}

/// One phase's counts over all the run's agents (`phase_stats`); `failed` only from hubs with #5511.
nonisolated struct WorkflowPhaseStats: Codable, Hashable, Sendable {
    var title: String
    var started: Int
    var done: Int
    var running: Int
    var failed: Int

    enum CodingKeys: String, CodingKey { case title, started, done, running, failed }

    init(title: String, started: Int, done: Int, running: Int, failed: Int = 0) {
        self.title = title
        self.started = started
        self.done = done
        self.running = running
        self.failed = failed
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        title = (try? c.decodeIfPresent(String.self, forKey: .title)) ?? ""
        started = max(0, (try? c.decodeIfPresent(Int.self, forKey: .started)) ?? 0)
        done = max(0, (try? c.decodeIfPresent(Int.self, forKey: .done)) ?? 0)
        running = max(0, (try? c.decodeIfPresent(Int.self, forKey: .running)) ?? 0)
        failed = max(0, (try? c.decodeIfPresent(Int.self, forKey: .failed)) ?? 0)
    }
}

/// One phase of a run as its card draws it (#3436, the web's `PhaseBlock`): its counts, its listed agents in the order
/// they started, where it stands.
nonisolated struct WorkflowPhaseBlock: Identifiable, Hashable, Sendable {
    /// Where a block stands: its agents all back (done), some running or the run in it (current), none started yet
    /// (next), or the run ended with some of them not back (stopped).
    enum State: Sendable { case done, current, next, stopped }

    /// Its place among the run's blocks (titles may repeat across an odd hub's stats).
    var index: Int
    /// The phase as the script names it; "" is the agents whose phase is not known.
    var title: String
    var started: Int
    var done: Int
    var running: Int
    var failed: Int
    /// Its agents the hub lists (the run's newest 60), in the order they started.
    var agents: [WorkflowAgent]
    var state: State
    /// The run is in this phase now: its block is lit.
    var current: Bool
    /// More may still start in it: its count reads "5/8+".
    var growing: Bool

    var id: String { "\(index):\(title)" }

    /// "5/8+": done of started, "+" while more may start in it.
    var count: String { "\(done)/\(started)" + (growing ? "+" : "") }

    /// Open by itself while the run is in it or any of its agents runs (not folding between one agent and the next of a
    /// phase that runs them one at a time); folded once all of them are back.
    var opensByItself: Bool { running > 0 || current }

    /// At most this many quiet agents are rows in a block; the quiet ones after them fold into 「还有 n 个」. A running
    /// agent is a row besides, however many run (#3434's design: running rows never fold, as the web's `phaseRows`).
    static let rowLimit = 12

    /// The rows a block shows, in the order its agents started (never reordered, so nothing jumps as they finish), the
    /// running ones always. Its first `limit` normally, the quiet tail folded. Once the hub's cap (the run's newest 60)
    /// has cut into the block, its last `limit` instead: anchored to the end, the rows stay put as the cap moves on,
    /// and all that is hidden comes before them. `all`: every listed one (after 「还有 n 个」).
    func rows(all: Bool = false, limit: Int = rowLimit) -> WorkflowPhaseRows {
        let unlisted = max(0, started - agents.count)
        if all { return WorkflowPhaseRows(rows: agents, folded: 0, breakAt: agents.count, unlisted: unlisted) }
        let from = unlisted > 0 ? max(0, agents.count - limit) : 0
        let shown = agents.indices.map { i in agents[i].running || (i >= from && i < from + limit) }
        let rows = agents.indices.filter { shown[$0] }.map { agents[$0] }
        let firstHidden = shown.firstIndex(of: false)
        let breakAt = firstHidden.map { h in shown[..<h].filter { $0 }.count } ?? rows.count
        return WorkflowPhaseRows(rows: rows, folded: agents.count - rows.count, breakAt: breakAt, unlisted: unlisted)
    }
}

/// How a block's rows stand (`WorkflowPhaseBlock.rows`): `rows` in start order; `folded` the listed ones not shown
/// (「还有 n 个」 opens them), the first of them where `breakAt` rows have been shown; `unlisted` the block's earliest
/// agents the hub no longer lists (it keeps the run's newest 60), said above its first row.
nonisolated struct WorkflowPhaseRows: Equatable, Sendable {
    var rows: [WorkflowAgent]
    var folded: Int
    var breakAt: Int
    var unlisted: Int
}

/// A run's agents by how they stand, for its progress bar: back, still running, failed, and stopped by its end.
nonisolated struct WorkflowTally: Equatable, Sendable {
    var started = 0
    var done = 0
    var running = 0
    var failed = 0
    var stopped = 0

    enum Part: CaseIterable, Sendable { case done, running, failed, stopped }

    func count(_ p: Part) -> Int {
        switch p {
        case .done: done
        case .running: running
        case .failed: failed
        case .stopped: stopped
        }
    }

    /// Each part's share of the bar (0…1), in the bar's order: done, running, failed, stopped.
    var shares: [(part: Part, share: Double)] {
        Part.allCases.map { p in (p, started > 0 ? Double(count(p)) / Double(started) : 0) }
    }
}

/// Decodes to nil instead of failing the array around it.
nonisolated struct Lossy<T: Decodable & Sendable>: Decodable, Sendable {
    var value: T?
    init(from decoder: Decoder) throws { value = try? T(from: decoder) }
}

nonisolated extension Session {
    /// Its workflows still running.
    var runningWorkflows: [WorkflowRun] { (workflows ?? []).filter(\.isRunning) }

    /// The chat header's cards: the running ones, and those that ended in the last hour (what happened to it), running
    /// first; only while one runs or one just ended.
    func shownWorkflows(now: Date = .now) -> [WorkflowRun] {
        (workflows ?? []).filter { w in
            w.isRunning || (w.endedAt ?? w.updatedAt).map { now.timeIntervalSince($0) < 3600 } == true
        }
        .enumerated().sorted { a, b in
            a.element.isRunning != b.element.isRunning ? a.element.isRunning : a.offset < b.offset
        }.map(\.element)
    }

    /// The workflow a single line shows: the running one the hub lists first (the newest).
    var leadWorkflow: WorkflowRun? { runningWorkflows.first }

    /// Working while its turn has ended, because a workflow runs (#2935; the hub counts it as working): 「工作中（后台）」.
    var isBackgroundWorking: Bool { !assistant && state == .working && !runningWorkflows.isEmpty }

    /// Background tasks to count apart from the workflows (the hub also lists a running workflow in `background`, for
    /// older apps): 「后台 N」 is these plus the running workflows.
    var backgroundCount: Int { (background ?? []).filter { !$0.isWorkflow }.count + runningWorkflows.count }
}

extension Session {
    /// The state's word with the background marked: 「工作中（后台）」 (`AgentState.label` is the main actor's). Never
    /// Awaiting Reply: that needs to know whether it is muted (`stateLabel(muted:)`).
    var stateLabel: String {
        if waitsInBackground { return String(localized: "Waiting in the background") }
        return isBackgroundWorking ? String(localized: "Working (Background)") : state.label
    }
    /// `stateLabel`, and "Awaiting Reply" for a seen one waiting on the user's reply that is not muted (#5096).
    func stateLabel(muted: Bool) -> String { awaitsYourReply(muted: muted) ? StateName.awaitsReply.text : stateLabel }
    /// The colors of `stateLabel`: Waiting in the Background lighter (`StatusPalette.backgroundWaiting`), else the state's.
    var stateTone: StatusTone { waitsInBackground ? StatusPalette.backgroundWaiting : state.tone }
    /// The colors of `stateLabel(muted:)`: Awaiting Reply in its own tone (`StatusPalette.awaitingReply`), else `stateTone`.
    func stateTone(muted: Bool) -> StatusTone { awaitsYourReply(muted: muted) ? StatusPalette.awaitingReply : stateTone }
}

nonisolated extension WorkflowRun {
    /// The folds the hub made of a turn's work (`fold_work`) that hold the Workflow call of a run it lists (#3436), to
    /// fetch so the call's card comes out: a chat opened while a run goes on has the turn that launched it folded, the
    /// call inside. A fold names no calls; its span `[ts, until]` holds the run's start, which the node takes from the
    /// call's own line. A call already among `items` needs none.
    static func foldsHoldingCalls(_ items: [Item], runs: [WorkflowRun]) -> [Item] {
        let wanted = runs.compactMap { r in r.toolUseID.flatMap { id in r.startedAt.map { (id, $0) } } }
        guard !wanted.isEmpty else { return [] }
        var present = Set<String>()
        var folds: [Item] = []
        for it in items {
            if it.kind == .toolUse, let id = it.toolUseID { present.insert(id) }
            if it.kind == .fold, it.fold != nil { folds.append(it) }
        }
        var out: [Item] = []
        for (call, at) in wanted where !present.contains(call) {
            for f in folds {
                guard let from = f.ts else { continue }
                let until = f.fold?.until ?? from
                if at >= from, at <= until, !out.contains(where: { $0.id == f.id }) { out.append(f) }
            }
        }
        return out
    }
}
