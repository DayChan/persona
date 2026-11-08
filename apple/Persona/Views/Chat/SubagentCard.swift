import PersonaKit
import SwiftUI

/// The subagents of the chat's session by the call that launched them (`tool_use_id`), for their cards
/// (`SubagentCard`): set by the chat, each running or not as its own lines tell it (`Subagent.running(in:)`).
extension EnvironmentValues {
    @Entry var subagentsByCall: [String: Subagent] = [:]
}

/// How a subagent is doing, as its card says it (#5602, the web's `AgentStatus`): by its call's result, a background
/// one by its notice.
nonisolated enum SubagentCardStatus: Equatable, Sendable {
    case running, done, failed, stopped

    /// From its Agent call, the chat's background tasks and the hub's word on it.
    static func of(_ call: ToolCall, tasks: TaskIndex, agent: Subagent?) -> SubagentCardStatus {
        if let id = call.use.toolUseID, tasks.started.contains(id) {
            // Sent to the background: its receipt is the result; how it went is its notice.
            guard let last = tasks.notes[id]?.last(where: { $0.status != "event" }) else {
                return agent?.running == false ? .stopped : .running
            }
            switch last.status {
            case "failed", "error": return .failed
            case "killed", "stopped": return .stopped
            default: return .done
            }
        }
        guard let result = call.result else { return .running }
        return result.isError == true ? .failed : .done
    }

    /// What a background subagent reported: its last notice's detail (nil before it is over).
    static func report(_ call: String, tasks: TaskIndex) -> String? {
        tasks.notes[call]?.last(where: { $0.status != "event" })?.detail.flatMap { $0.isEmpty ? nil : $0 }
    }
}

/// What a subagent's card says under its title: what it is doing while it runs ("Running the tests…", or
/// "Starting…" before its first call), then how it went and how many steps it took ("Done · 7 steps · 1 min 20 s").
enum SubagentCardText {
    static func line(_ status: SubagentCardStatus, agent: Subagent?, took: Int?) -> String {
        let steps = agent?.steps.flatMap { $0 > 0 ? String(localized: "\($0) steps [Subagent Card]") : nil }
        switch status {
        case .running:
            if let t = agent?.lastTool, let what = (t.summary?.isEmpty == false ? t.summary : t.name) {
                return [what + "…", steps].compactMap { $0 }.joined(separator: " · ")
            }
            return String(localized: "Starting… [Subagent Card]")
        case .done, .failed, .stopped:
            let word = switch status {
            case .failed: String(localized: "Failed [Subagent Card]")
            case .stopped: String(localized: "Stopped [Subagent Card]")
            default: String(localized: "Done [Subagent Card]")
            }
            return [word, steps, took.map { TaskNoteText.took($0) }].compactMap { $0 }.joined(separator: " · ")
        }
    }
}

/// A subagent the agent launched (an Agent / Task call), in its work line where it launched it (#5602, as the web's
/// `SubagentCard` and Claude Desktop): its title, what it is doing or how it went with its steps; open, what it was
/// asked, what it reported, and its conversation (read-only, "View Subagent").
struct SubagentCard: View {
    @Environment(\.taskIndex) private var tasks
    @Environment(\.subagentsByCall) private var agents
    @Environment(\.openSubagent) private var openSubagent
    @Environment(\.openTools) private var openTools
    @Environment(\.chatPin) private var pin
    var call: ToolCall
    @State private var openHere = false

    /// An Agent / Task call: drawn as its subagent's card.
    nonisolated static func isAgent(_ call: ToolCall) -> Bool { call.name == "Agent" || call.name == "Task" }
    @State private var unfold = Unfolding()

    private var key: String { "agent-" + call.id }
    private var open: Bool { openTools?.isOpen(key) ?? openHere }
    private var agent: Subagent? { call.use.toolUseID.flatMap { agents[$0] } }
    private var status: SubagentCardStatus { .of(call, tasks: tasks, agent: agent) }

    private var title: String {
        if let d = call.input["description"]?.string, !d.isEmpty { return d }
        return agent?.title ?? String(localized: "Subagent [Task Note Row 70da69]")
    }

    /// How long it ran: from its start to its result (a background one: to its last line, its receipt is written at
    /// once); nil while it runs.
    private var took: Int? {
        guard status != .running, let start = agent?.startedAt ?? call.use.ts else { return nil }
        let id = call.use.toolUseID ?? ""
        let end = tasks.started.contains(id) ? agent?.updatedAt : call.result?.ts
        guard let end, end > start else { return nil }
        return Int(end.timeIntervalSince(start))
    }

    private var tint: Color {
        switch status {
        case .running: Theme.busy
        case .done: Theme.positive
        case .failed: Theme.danger
        case .stopped: .secondary
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                Haptics.tap()
                toggleInPlace(pin, key, animation: Theme.Motion.state, unfold: $unfold, closing: open) {
                    if let openTools { openTools.set(key, !open) } else { openHere.toggle() }
                }
            } label: { header }
                .buttonStyle(.plain)
                .chatPinned(key)
            if open { details.unfolding(unfold).transition(.opacity.combined(with: .move(edge: .top))) }
        }
        .accessibilityIdentifier("chat.subagent-card")
    }

    private var header: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            ZStack {
                if WorkCardSlice.timeline { Circle().fill(Theme.pageBackground).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon) }
                Circle().fill(tint.opacity(0.14)).scaledFrame(width: Theme.Size.icon, height: Theme.Size.icon)
                if status == .running {
                    ProgressView().controlSize(.mini).tint(tint)
                } else {
                    ThemedIcon(ToolPresentation.symbol(call.name))
                        .font(Theme.Font.labelStrong).dynamicTypeSize(...DynamicTypeSize.xLarge)
                        .foregroundStyle(tint)
                }
            }
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title).font(Theme.Font.secondaryStrong).lineLimit(1)
                Text(SubagentCardText.line(status, agent: agent, took: took))
                    .font(Theme.Font.label)
                    .foregroundStyle(status == .failed ? Theme.danger : Color.secondary)
                    .lineLimit(1)
                    .contentTransition(.opacity)
            }
            Spacer(minLength: Theme.Space.xs)
            ThemedIcon(Theme.Icon.disclosure)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(.tertiary)
                .rotationEffect(.degrees(open ? 90 : 0))
                .padding(.top, Theme.Space.xxs)
        }
        .padding(.vertical, WorkCardSlice.timeline ? Theme.Space.s : Theme.Space.m)
        .padding(.horizontal, WorkCardSlice.inset)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
        .accessibilityHint(open ? String(localized: "Collapses the details") : String(localized: "Shows the details"))
    }

    @ViewBuilder private var details: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if let prompt = call.input["prompt"]?.string, !prompt.isEmpty {
                Text(prompt).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(6)
            }
            if let id = call.use.toolUseID, tasks.started.contains(id) {
                // Sent to the background: its result is the receipt; its report is its notice's (#5886).
                if let report = SubagentCardStatus.report(id, tasks: tasks) {
                    ResultBlock(text: report, isError: status == .failed, cut: nil)
                }
            } else if let result = call.result {
                ResultBlock(text: result.cutText, isError: result.isError ?? false, cut: result.fullSize.map { (result.id, $0) })
            }
            if let open = openSubagent, let id = call.use.toolUseID {
                Button { open(id) } label: {
                    Label("View Subagent", systemImage: Theme.Icon.open).font(Theme.Font.labelStrong).lineLimit(1)
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
            }
        }
        .padding(.leading, WorkCardSlice.textColumn)
        .padding(.trailing, WorkCardSlice.inset)
        .padding(.bottom, Theme.Space.m)
    }
}
