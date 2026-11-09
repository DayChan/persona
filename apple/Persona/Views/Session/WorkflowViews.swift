import SwiftUI

// Multi-agent workflows an agent runs in the background (Claude Code's Workflow tool, `WorkflowRun`, #2935): one line
// for the Agents list's rows and task cards; a section in the chat's header, a card per running one and a line per
// ended one, the whole a capped share of the page that scrolls inside (#4532); and in the chat, where the Workflow call
// that launched it was, the run's own card (#3436, `WorkflowCallCard`). The cards share their parts: a head (name,
// status, done / started, time), a progress bar in parts and one block per phase with its agents (`WorkflowRunBody`).
// Things move only when a value changes, never on the first draw, not at all with Reduce Motion, and nothing repeats:
// counts roll (`numericText`), the lit phase moves on (`matchedGeometryEffect`), a new agent fades in, a finished
// run's chip and a finished agent's dot change color. A running run's time is the system's clock
// (`Text(timerInterval:)`), which ticks without drawing the card again; its agents' times move on once a minute.

/// "Background workflow “parity-audit” · Verify 2/2 · 169/185+ · 14 min" ("and N more" when more run): under a row's last line.
struct WorkflowLine: View {
    var runs: [WorkflowRun]
    var font: Font = Theme.Font.caption

    var body: some View {
        if let run = runs.first {
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
                ThemedIcon(Theme.Icon.workflow)
                    .font(font)
                    .foregroundStyle(Theme.ai)
                // Every minute: many rows tick at once.
                TimelineView(.periodic(from: .now, by: 60)) { ctx in
                    // The counts and the time are what the line is for, and are never cut (cut in the middle of the
                    // whole line, 169/185+ read 9/185+). Where it does not fit whole, the words the mark before it
                    // already says go first, then the phase, and only then is the name cut.
                    ViewThatFits(in: .horizontal) {
                        Text(run.lineName + tail(run, now: ctx.date))
                        Text(run.title + tail(run, now: ctx.date))
                        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.flush) {
                            Text(run.title)
                                .truncationMode(.tail)
                            Text(tail(run, now: ctx.date, phase: false))
                                .layoutPriority(1)
                        }
                    }
                    .lineLimit(1)
                    .font(font)
                    .foregroundStyle(.secondary)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(run.lineName + tail(run, now: ctx.date))
                }
            }
            .accessibilityElement(children: .combine)
        }
    }

    /// 「 · Verify 2/2 · 169/185+ · 14 分钟」 after the name (the phase left out when `phase` is false), and how many
    /// more run.
    private func tail(_ run: WorkflowRun, now: Date, phase: Bool = true) -> String {
        var parts = run.lineDetails(phase: phase)
        if let t = run.elapsed(now: now) { parts.append(WorkflowRun.duration(t, seconds: false)) }
        var s = parts.map { " · " + $0 }.joined()
        if runs.count > 1 { s += String(localized: " · and \(runs.count - 1) more") }
        return s
    }
}

/// The chat header's 「后台 workflow」 section, above the subagents (#4532): the running ones as cards, the ended ones
/// one line each under them. At most `maxHeight` tall (a share of the page, `Theme.Size.workflowShare`); past that it
/// scrolls inside, so open agents never push the conversation off the screen.
struct WorkflowSection: View {
    var runs: [WorkflowRun]
    /// The most it may take; nil: as tall as it is.
    var maxHeight: CGFloat?
    /// The running cards are their heads until tapped (`WorkflowCard.compact`).
    var compact = false
    /// Opens one of a workflow's agents in the chat (its transcript, read as a subagent).
    var open: (WorkflowAgent) -> Void
    @State private var contentHeight: CGFloat = 0

    var body: some View {
        ScrollView(.vertical) {
            content
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { contentHeight = $0 }
        }
        .scrollBounceBehavior(.basedOnSize)
        .frame(height: height)
        .accessibilityIdentifier("workflow.section")
    }

    /// Its content's height up to the cap; nil until measured.
    private var height: CGFloat? {
        guard contentHeight > 0 else { return nil }
        guard let maxHeight, maxHeight > 0 else { return contentHeight }
        return min(contentHeight, maxHeight)
    }

    private var content: some View {
        let live = runs.filter(\.isRunning)
        let ended = runs.filter { !$0.isRunning }
        return VStack(alignment: .leading, spacing: Theme.Space.xs) {
            ForEach(live) { run in
                WorkflowCard(run: run, compact: compact, open: open)
            }
            if !ended.isEmpty {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(ended) { run in
                        WorkflowEndedRow(run: run, open: open)
                        if run.id != ended.last?.id { Divider() }
                    }
                }
                .padding(.horizontal, Theme.Space.m)
                .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
            }
        }
    }
}

/// A running workflow in the chat's header: its head, where it is now and its progress bar; a tap opens its phases with
/// their agents (the same blocks as its card in the chat, `WorkflowRunBody`).
struct WorkflowCard: View {
    var run: WorkflowRun
    /// Only its head until tapped, then all of it: a header that never folds (iPhone Duo's side bars) on a short screen,
    /// where two full cards left the chat a third of it (#2161).
    var compact = false
    var open: (WorkflowAgent) -> Void
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var expanded = false

    /// Shows where it is now and its progress bar.
    private var showsBody: Bool { !compact || expanded }

    var body: some View {
        let opens = !run.phaseBlocks.isEmpty || (compact && (run.now != nil || run.tally.started > 0))
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if opens {
                Button {
                    Haptics.tap()
                    withAnimation(reduceMotion ? nil : Theme.Motion.reveal) { expanded.toggle() }
                } label: { top(opens: true).contentShape(.rect) }
                .buttonStyle(.plain)
                .accessibilityLabel([WorkflowLook.spoken(run), expanded ? String(localized: "Hide Agents") : String(localized: "Show Agents (\(run.tally.started))")]
                    .joined(separator: String(localized: ", ", comment: "List separator")))
                .accessibilityIdentifier("workflow.agents")
            } else {
                top(opens: false)
            }
            // Under the name, as in its card in the chat.
            if showsBody {
                WorkflowRunBody(run: run, open: open, scope: "header-" + run.id, phases: expanded)
                    .padding(.leading, WorkflowMark.textLeading - Theme.Space.s)
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Space.s)
        // A plain fill as GoalBar's: the folded header fades the open one out, and glass does not fade with it.
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(WorkflowLook.spoken(run))
    }

    private func top(opens: Bool) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            HStack(spacing: Theme.Space.s) {
                WorkflowHead(run: run)
                if opens {
                    ThemedIcon(Theme.Icon.disclosure)
                        .font(Theme.Font.labelStrong)
                        .foregroundStyle(.tertiary)
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                }
            }
            if showsBody {
                WorkflowSubline(run: run, whole: expanded)
                    .padding(.leading, WorkflowMark.textLeading)
            }
        }
    }
}

/// A workflow that ended (completed, stopped, failed, gone quiet): one line, its name, done / started, how long it ran
/// and how it ended, with a stopped or failed one's error under it; a tap opens its phases when it has any.
struct WorkflowEndedRow: View {
    var run: WorkflowRun
    var open: (WorkflowAgent) -> Void
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var expanded = false

    var body: some View {
        let opens = !run.phaseBlocks.isEmpty
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            if !opens {
                line(opens: false)
            } else {
                Button {
                    Haptics.tap()
                    withAnimation(reduceMotion ? nil : Theme.Motion.reveal) { expanded.toggle() }
                } label: { line(opens: true).contentShape(.rect) }
                .buttonStyle(.plain)
                .accessibilityIdentifier("workflow.ended.agents")
            }
            if let e = run.shownError {
                Text(e).font(Theme.Font.label).foregroundStyle(Theme.danger).lineLimit(expanded ? nil : 1)
            }
            if expanded, opens {
                WorkflowRunBody(run: run, open: open, scope: "header-" + run.id)
                    .padding(.top, Theme.Space.xs)
            }
        }
        .padding(.vertical, Theme.Space.s)
        .accessibilityElement(children: .contain)
        .accessibilityLabel(WorkflowLook.spoken(run))
    }

    private func line(opens: Bool) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.workflow)
                .font(Theme.Font.label)
                .foregroundStyle(WorkflowLook.tint(run))
            Text(run.title)
                .font(Theme.Font.labelMedium)
                .foregroundStyle(.primary)
                .lineLimit(1)
                .frame(maxWidth: .infinity, alignment: .leading)
            if let meta { Text(meta).font(Theme.Font.label).foregroundStyle(.secondary).monospacedDigit().lineLimit(1).fixedSize() }
            Text(WorkflowLook.word(run))
                .font(Theme.Font.labelStrong)
                .foregroundStyle(WorkflowLook.tint(run))
                .lineLimit(1)
                .fixedSize()
            if opens {
                // The same › as the running card's, the phases' and the chat card's: turned down while open.
                ThemedIcon(Theme.Icon.disclosure)
                    .font(Theme.Font.label)
                    .foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(expanded ? 90 : 0))
            }
        }
    }

    /// 「3/6 · 14 分钟」.
    private var meta: String? {
        let parts = [run.counts, run.elapsed().map { WorkflowRun.duration($0, seconds: false) }].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

// MARK: - The parts the cards share

/// The workflow's mark in a soft disc of the AI color (everything about workflows is drawn in it, as on the web and
/// Android; how a run went is its chip's color).
struct WorkflowMark: View {
    /// On the chat's timeline the disc sits on the rail: the page under it hides the line behind it.
    var onRail = false

    /// Where the words after the mark start: the disc and the gap after it (a line under the head lines up there), as
    /// a tool row's words after its icon.
    static var textLeading: CGFloat { Theme.Size.icon + Theme.Space.m }

    var body: some View {
        ZStack {
            if onRail { Circle().fill(Theme.pageBackground) }
            Circle().fill(Theme.aiTint)
            ThemedIcon(Theme.Icon.workflow)
                .font(Theme.Font.labelStrong).dynamicTypeSize(...DynamicTypeSize.xLarge)
                .foregroundStyle(Theme.ai)
        }
        .scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
        .accessibilityHidden(true)
    }
}

/// A run's head: its mark, its name, its status chip, 「169/185+」 and how long it has run (a live clock while it runs).
struct WorkflowHead: View {
    var run: WorkflowRun
    var onRail = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            WorkflowMark(onRail: onRail)
            HStack(spacing: Theme.Space.s) {
                Text(run.title)
                    .font(Theme.Font.secondaryStrong)
                    .foregroundStyle(.primary)
                    .lineLimit(1)
                    .frame(maxWidth: .infinity, alignment: .leading)
                Chip(text: WorkflowLook.word(run), look: .tint(WorkflowLook.tint(run)))
                    .contentTransition(.opacity)
                    .fixedSize()
                    .accessibilityIdentifier("workflow.status")
                if let counts = run.counts {
                    Text(counts)
                        .font(Theme.Font.labelStrong)
                        .foregroundStyle(.secondary)
                        .monospacedDigit()
                        .lineLimit(1)
                        .fixedSize()
                        .contentTransition(.numericText(value: Double(run.progress?.done ?? 0)))
                        .accessibilityIdentifier("workflow.counts")
                }
                WorkflowElapsed(run: run)
            }
        }
        // A change of its counts or of how it stands: rolled and recolored at a state change's pace (none on the first
        // draw: these only run when the value changes).
        .animation(reduceMotion ? nil : Theme.Motion.state, value: run.counts)
        .animation(reduceMotion ? nil : Theme.Motion.state, value: run.outcome)
    }
}

/// How long a run has run: the system's clock while it runs (it ticks without drawing the card again), how long it
/// took once it has ended, written as the header's ended rows and the agents' rows write it (「14 分钟」).
struct WorkflowElapsed: View {
    var run: WorkflowRun

    var body: some View {
        Group {
            if run.isRunning, let start = run.startedAt {
                Text(timerInterval: start...Date.distantFuture, countsDown: false)
            } else if let t = run.elapsed() {
                Text(WorkflowRun.duration(t, seconds: false))
            }
        }
        .font(Theme.Font.label)
        // Not tertiary: how long it has run is what the reader looks for here (tertiary read 1.9:1 on white).
        .foregroundStyle(.secondary)
        .monospacedDigit()
        .lineLimit(1)
        .fixedSize()
    }
}

/// The line under a run's head: where it is now while it runs (「当前 Verify：verify:chat-rich」); once ended, what it
/// was for and why it stopped. One line each, unless `whole`.
struct WorkflowSubline: View {
    var run: WorkflowRun
    var whole = false

    var body: some View {
        if let now = run.now {
            Text(Self.nowText(now))
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .contentTransition(.opacity)
                .accessibilityIdentifier("workflow.now")
        } else if run.summary?.isEmpty == false || run.shownError != nil {
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                if let s = run.summary, !s.isEmpty {
                    Text(s).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(whole ? nil : 1)
                }
                if let e = run.shownError {
                    Text(e).font(Theme.Font.caption).foregroundStyle(Theme.danger).lineLimit(whole ? nil : 1)
                }
            }
        }
    }

    static func nowText(_ now: (phase: String, agent: String)) -> String {
        if !now.phase.isEmpty, !now.agent.isEmpty { return String(localized: "Now \(now.phase): \(now.agent) [Workflow Card]") }
        if !now.phase.isEmpty { return String(localized: "Now \(now.phase) [Workflow Card]") }
        return String(localized: "Running \(now.agent) [Workflow Card]")
    }
}

/// What a run's card opens to: its progress bar with what each part counts, and (`phases`) one block per phase.
struct WorkflowRunBody: View {
    var run: WorkflowRun
    var open: (WorkflowAgent) -> Void
    /// Where its blocks' open / folded choices are kept (`WorkflowPhaseView`).
    var scope: String
    var phases = true

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            // In line with the phases' marks (their heads have this inset, for the lit background around them).
            WorkflowProgress(tally: run.tally).padding(.horizontal, Theme.Space.s)
            if phases { WorkflowPhaseList(run: run, open: open, scope: scope) }
        }
    }
}

/// A run's agents as a bar in parts, done first (done, running, failed, stopped), and under it what each part counts.
struct WorkflowProgress: View {
    var tally: WorkflowTally
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        if tally.started > 0 {
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                MeterBar(parts: tally.shares.map { MeterBar.Part(share: $0.share, color: Self.color($0.part)) }, thickness: Theme.Size.bar)
                HStack(spacing: Theme.Space.m) {
                    ForEach(WorkflowTally.Part.allCases.filter { tally.count($0) > 0 }, id: \.self) { p in
                        HStack(spacing: Theme.Space.xs) {
                            Circle().fill(Self.color(p)).scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                            Text(Self.word(p, tally.count(p)))
                                .contentTransition(.numericText(value: Double(tally.count(p))))
                        }
                        .fixedSize()
                        .transition(.opacity)
                    }
                }
                .font(Theme.Font.label)
                .foregroundStyle(.secondary)
                .monospacedDigit()
                .lineLimit(1)
            }
            .animation(reduceMotion ? nil : Theme.Motion.state, value: tally)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(String(localized: "\(tally.done)/\(tally.started) agents done"))
            .accessibilityIdentifier("workflow.progress")
        }
    }

    static func color(_ p: WorkflowTally.Part) -> Color {
        switch p {
        case .done: Theme.ai
        case .running: AgentState.working.tone.dot
        case .failed: Theme.danger
        case .stopped: AgentState.offline.tone.dot
        }
    }

    static func word(_ p: WorkflowTally.Part, _ n: Int) -> String {
        switch p {
        case .done: String(localized: "\(n) done [Workflow Card]")
        case .running: String(localized: "\(n) running [Workflow Card]")
        case .failed: String(localized: "\(n) failed [Workflow Card]")
        case .stopped: String(localized: "\(n) stopped [Workflow Card]")
        }
    }
}

/// A run's phases, one block each, in the script's order: the one it is in lit, and the light moving on with it.
struct WorkflowPhaseList: View {
    var run: WorkflowRun
    var open: (WorkflowAgent) -> Void
    var scope: String
    @Namespace private var lit
    @Environment(\.horizontalSizeClass) private var widthClass
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let blocks = run.phaseBlocks
        if !blocks.isEmpty {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(blocks) { b in
                    WorkflowPhaseView(block: b, lit: lit, key: Self.key(scope, b), wide: widthClass == .regular, open: open)
                }
            }
            // The run moving on to the next phase: the light goes over, the one before gets its mark.
            .animation(reduceMotion ? nil : Theme.Motion.layout, value: blocks.firstIndex(where: \.current))
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("workflow.phases")
        }
    }

    /// Where a block's open / folded choice is kept, and its head's place for the chat's pin: by its place as well as
    /// its title, so two blocks with one title (an odd hub's stats) fold apart.
    nonisolated static func key(_ scope: String, _ block: WorkflowPhaseBlock) -> String { "workflow-\(scope)-phase-\(block.id)" }
}

/// One phase: its head (lit while the run is in it) with done / started, and its agents in the order they started, at
/// most `WorkflowPhaseBlock.rowLimit` quiet rows with the quiet rest folded into 「还有 n 个」. Open by itself while the
/// run is in it or any of its agents runs, folded once all are back; a phase the reader opened or folded stays so.
struct WorkflowPhaseView: View {
    var block: WorkflowPhaseBlock
    var lit: Namespace.ID
    /// Where the reader's choice is kept: the chat's `OpenTools` (rows come and go as the list scrolls), else here. In
    /// the chat it is also the head's place for the pin (`chatPinned`).
    var key: String
    /// iPad and Mac: each agent's model and latest tool too.
    var wide: Bool
    var open: (WorkflowAgent) -> Void
    @Environment(\.openTools) private var openTools
    @Environment(\.chatPin) private var pin
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var openHere: Bool?
    @State private var all = false
    @State private var unfold = Unfolding()

    private var opens: Bool { !block.agents.isEmpty }
    private var isOpen: Bool { opens && ((openTools?.isOpen(key) ?? openHere) ?? block.opensByItself) }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if opens {
                Button {
                    let next = !isOpen
                    change(closing: !next, unfolds: true) {
                        if let openTools { openTools.set(key, next) } else { openHere = next }
                    }
                } label: { head }
                .buttonStyle(.plain)
                .chatPinned(key)
                .accessibilityLabel(spoken)
                .accessibilityIdentifier("workflow.phase")
            } else {
                head
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel(spoken)
                    .accessibilityIdentifier("workflow.phase")
            }
            if isOpen { rows.unfolding(unfold).transition(.opacity) }
        }
    }

    /// Opens or closes what is under the head, or shows more or fewer of its rows. In the chat the change is made at
    /// once with the head held where it is (`toggleInPlace`): the list is upside down, and up to 12 rows opening under
    /// it (or 48 more after 「还有 n 个」) pushed the head and all above it up, under the top bar. What opens reveals
    /// itself (`unfolds`, the head's own toggle; the rows' block is already there for more or fewer). In the chat's
    /// header (no pin) it animates as before, and nothing moves with Reduce Motion.
    private func change(closing: Bool, unfolds: Bool, _ apply: @escaping () -> Void) {
        Haptics.tap()
        guard pin != nil else {
            withAnimation(reduceMotion ? nil : Theme.Motion.reveal, apply)
            return
        }
        toggleInPlace(pin, key, animation: Theme.Motion.reveal, unfold: unfolds ? $unfold : nil, closing: closing, apply)
    }

    private var head: some View {
        HStack(spacing: Theme.Space.s) {
            WorkflowPhaseMark(state: block.state, current: block.current, failed: block.failed > 0)
            Text(title)
                .font(block.state == .next ? Theme.Font.caption : Theme.Font.captionMedium)
                .foregroundStyle(block.current ? AnyShapeStyle(Theme.ai) : block.state == .next ? AnyShapeStyle(.tertiary) : AnyShapeStyle(.primary))
                .lineLimit(1)
            if block.failed > 0 {
                Text(failedText)
                    .font(Theme.Font.label)
                    .foregroundStyle(Theme.danger)
                    .lineLimit(1)
                    .fixedSize()
            }
            Spacer(minLength: Theme.Space.xs)
            if block.started > 0 {
                Text(block.count)
                    .font(Theme.Font.label)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
                    .lineLimit(1)
                    .fixedSize()
                    .contentTransition(.numericText(value: Double(block.done)))
                    .animation(reduceMotion ? nil : Theme.Motion.state, value: block.count)
            }
            if opens {
                ThemedIcon(Theme.Icon.disclosure)
                    .font(Theme.Font.label)
                    .foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(isOpen ? 90 : 0))
            }
        }
        .padding(.horizontal, Theme.Space.s)
        .padding(.vertical, Theme.Space.sm)
        .background {
            if block.current {
                RoundedRectangle(cornerRadius: Theme.Radius.badge, style: .continuous)
                    .fill(Theme.aiTint)
                    .matchedGeometryEffect(id: "lit", in: lit)
            }
        }
        .contentShape(.rect)
    }

    private var title: String { block.title.isEmpty ? String(localized: "No phase [Workflow Phase]") : block.title }
    private var failedText: String { String(localized: "\(block.failed) failed [Workflow Phase]") }

    /// VoiceOver: 「Verify，当前阶段，5/8+」.
    private var spoken: String {
        [title, WorkflowPhaseMark.word(block.state, current: block.current, failed: block.failed > 0),
         block.started > 0 ? block.count : nil, block.failed > 0 ? failedText : nil]
            .compactMap { $0 }.joined(separator: String(localized: ", ", comment: "List separator"))
    }

    private var rows: some View {
        let r = block.rows(all: all)
        let fewer = all && block.rows().folded > 0
        return VStack(alignment: .leading, spacing: 0) {
            if r.unlisted > 0 {
                note(String(localized: "\(r.unlisted) earlier ones not listed [Workflow Phase]"))
            }
            ForEach(r.rows.prefix(r.breakAt)) { a in row(a) }
            if r.folded > 0 {
                more(String(localized: "\(r.folded) more [Workflow Phase]")) { all = true }
                    .accessibilityIdentifier("workflow.phase.more")
            }
            ForEach(r.rows.dropFirst(r.breakAt)) { a in row(a) }
            if fewer {
                more(String(localized: "Show Fewer [Workflow Phase]")) { all = false }
                    .accessibilityIdentifier("workflow.phase.fewer")
            }
        }
        .padding(.bottom, Theme.Space.xs)
        // A new agent fades in where it started (in start order: nothing above it moves).
        .animation(reduceMotion ? nil : Theme.Motion.reveal, value: r.rows.map(\.id))
    }

    private func row(_ a: WorkflowAgent) -> some View {
        WorkflowAgentRow(agent: a, wide: wide, open: a.sessionID == nil ? nil : { open(a) })
            .transition(.opacity)
    }

    private func note(_ text: String) -> some View {
        Text(text)
            .font(Theme.Font.label)
            .foregroundStyle(.secondary)
            .lineLimit(1)
            .padding(.leading, WorkflowAgentRow.labelLeading)
            .padding(.vertical, Theme.Space.xs)
    }

    /// 「还有 n 个」 and Show Fewer: the rows change with the phase's head held in place (`change`).
    private func more(_ text: String, _ action: @escaping () -> Void) -> some View {
        Button {
            change(closing: false, unfolds: false, action)
        } label: {
            Text(text)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(Theme.accent)
                .lineLimit(1)
                .fixedSize()
                .padding(.leading, WorkflowAgentRow.labelLeading)
                .padding(.vertical, Theme.Space.xs)
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
    }
}

/// Where a phase stands, before its name: ✓ done (a red ⚠ instead when some of its agents failed), a dot in the AI color
/// while the run is in it (a softer one for one with agents running that the run moved past: still at work, not "not
/// started", as the web's mark), a ring before it starts, a square when the run ended with some of its agents not back.
struct WorkflowPhaseMark: View {
    var state: WorkflowPhaseBlock.State
    var current: Bool
    var failed: Bool

    var body: some View {
        ZStack {
            switch state {
            case .done:
                ThemedIcon(failed ? Theme.Icon.error : Theme.Icon.done)
                    .font(Theme.Font.labelHeavy).dynamicTypeSize(...DynamicTypeSize.xLarge)
                    .foregroundStyle(failed ? Theme.danger : Theme.positive)
                    .transition(.opacity)
            case .current where current:
                Circle().fill(Theme.ai).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
            case .current:
                Circle().fill(Theme.aiMuted).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
            case .next:
                Circle().strokeBorder(.tertiary, lineWidth: Theme.Size.lineMedium).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
            case .stopped:
                Rectangle().fill(.tertiary).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
            }
        }
        .scaledFrame(width: Theme.Size.markM, height: Theme.Size.markM)
        .accessibilityHidden(true)
    }

    static func word(_ state: WorkflowPhaseBlock.State, current: Bool, failed: Bool) -> String {
        if current { return String(localized: "Current phase [Workflow Phase]") }
        switch state {
        case .done: return failed ? String(localized: "Finished with failures [Workflow Phase]") : String(localized: "Finished phase [Workflow Phase]")
        case .current: return String(localized: "Phase with agents running [Workflow Phase]")
        case .next: return String(localized: "Phase not started [Workflow Phase]")
        case .stopped: return String(localized: "Phase that did not finish [Workflow Phase]")
        }
    }
}

/// An agent of a workflow: how it stands (a dot), its label, (iPad, Mac) its model and latest tool, how long it has run
/// (to the minute); a tap opens its conversation. On the Mac the pointer over it says 「打开」 in place of its time.
struct WorkflowAgentRow: View {
    var agent: WorkflowAgent
    var wide: Bool
    var open: (() -> Void)?
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var hovering = false

    /// Where an agent's label starts: under its phase's name (the phase's inset, its mark and the gap after it).
    static var labelLeading: CGFloat { Theme.Space.s + Theme.Size.markM + Theme.Space.s }

    var body: some View {
        let content = HStack(spacing: Theme.Space.s) {
            Circle()
                .fill(Self.dot(agent.runState))
                .scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                .scaledFrame(width: Theme.Size.markM)
                .animation(reduceMotion ? nil : Theme.Motion.state, value: agent.runState)
            Text(agent.title)
                .font(Theme.Font.caption)
                .foregroundStyle(agent.running ? AnyShapeStyle(.primary) : AnyShapeStyle(.secondary))
                .lineLimit(1)
                .layoutPriority(1)
            if wide, let d = agent.detail {
                Text(d)
                    .font(Theme.Font.label)
                    .foregroundStyle(.tertiary)
                    .lineLimit(1)
                    .truncationMode(.middle)
            }
            Spacer(minLength: Theme.Space.xs)
            if hovering || MacHover.showAll, open != nil {
                Text("Open [Workflow Agent]")
                    .font(Theme.Font.labelStrong)
                    .foregroundStyle(Theme.accent)
                    .lineLimit(1)
                    .fixedSize()
            } else {
                elapsed
            }
        }
        .padding(.horizontal, Theme.Space.s)
        .padding(.vertical, Theme.Space.sm)
        .contentShape(.rect)

        if let open {
            Button { Haptics.tap(); open() } label: { content }
                .buttonStyle(.plain)
                #if os(macOS)
                .macHoverShade(in: .rect(cornerRadius: Theme.Radius.badge, style: .continuous))
                .onHover { hovering = $0 }
                .help(agent.lastText ?? agent.title)
                #endif
                .accessibilityLabel(String(localized: "Open \(agent.title)'s conversation [Workflow Agent]"))
                .accessibilityValue(Self.word(agent.runState))
                .accessibilityIdentifier("workflow.agent")
        } else {
            content
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("workflow.agent")
        }
    }

    /// How long it has run, to the minute: a running one's moves on once a minute (a second-by-second clock on each of a
    /// run's 60 rows kept the Mac's CPU busy while nothing happened, #3436; the run's head has the one that ticks). All
    /// rows move on at the same moment, on the clock's minute (`minuteTicks`): each at its own minute woke the app 60
    /// times a minute.
    private var elapsed: some View {
        Group {
            if agent.running, let start = agent.startedAt {
                TimelineView(.periodic(from: Self.minuteTicks, by: 60)) { ctx in
                    Text(WorkflowRun.duration(ctx.date.timeIntervalSince(start), seconds: false))
                }
            } else if let t = agent.took {
                Text(WorkflowRun.duration(t, seconds: false))
            }
        }
        .font(Theme.Font.label)
        // Not tertiary: each agent's time is what the rows are read for (tertiary read 1.7:1 on white).
        .foregroundStyle(.secondary)
        .monospacedDigit()
        .lineLimit(1)
        .fixedSize()
    }

    /// Where the rows' minutes are counted from: one moment for all of them, so their minutes turn together.
    static let minuteTicks = Date(timeIntervalSinceReferenceDate: 0)

    static func dot(_ s: WorkflowAgent.RunState) -> Color {
        switch s {
        case .running: AgentState.working.tone.dot
        case .done: AgentState.idle.tone.dot
        case .failed: Theme.danger
        case .stopped: AgentState.offline.tone.dot
        }
    }

    static func word(_ s: WorkflowAgent.RunState) -> String {
        switch s {
        case .running: String(localized: "Running [Workflow Views 828e7c]")
        case .done: String(localized: "Done [Extension System Presentation c0b3fb]")
        case .failed: String(localized: "Failed")
        case .stopped: String(localized: "Stopped [Port Chips f00645]")
        }
    }
}

/// One word and tint per workflow outcome.
enum WorkflowLook {
    static func tint(_ run: WorkflowRun) -> Color {
        switch run.outcome {
        case .running: Theme.ai
        case .completed: Theme.positive
        case .failed, .stopped: Theme.danger
        case .stalled: Theme.cautionText
        }
    }

    static func word(_ run: WorkflowRun) -> String {
        switch run.outcome {
        case .running: String(localized: "Running [Workflow Views 828e7c]")
        case .completed: String(localized: "Done [Extension System Presentation c0b3fb]")
        case .failed: String(localized: "Failed")
        case .stopped: String(localized: "Stopped [Port Chips f00645]")
        case .stalled: String(localized: "Stalled")
        }
    }

    /// VoiceOver: 「后台 workflow parity-audit，在跑，阶段 Verify 2/2，169/185 个 agent 完成」.
    static func spoken(_ run: WorkflowRun) -> String {
        var s = String(localized: "Background workflow \(run.title), \(word(run))")
        if let p = run.phaseLabel { s += String(localized: ", phase \(p)") }
        if let pr = run.progress { s += String(localized: ", \(pr.done)/\(pr.started) agents done") }
        if let e = run.shownError { s += String(localized: ", ") + e }
        return s
    }
}

extension WorkflowRun {
    /// A failed or stopped run's error; nil for the others.
    var shownError: String? {
        guard let e = error, !e.isEmpty, outcome == .failed || outcome == .stopped else { return nil }
        return e
    }
}
