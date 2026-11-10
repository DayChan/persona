import SwiftUI
import Testing
@testable import Persona

/// The phone's action bar above the assistant's composer (#2879): what its three trays list and count, and its entries
/// drawn exactly as low as the dock's capsules they replace.
@MainActor
struct AssistantActionBarTests {
    private func agent(_ id: String, _ state: AgentState, since: TimeInterval = 0) -> DockAgent {
        DockAgent(key: SessionKey(group: "", machineID: "box", sessionID: id), title: "agent \(id)", state: state,
                  since: Date(timeIntervalSince1970: since))
    }

    private func task(_ id: String, _ state: TaskState, at: TimeInterval) -> ActionTrayContent.TaskRow {
        .init(id: id, state: state, createdAt: Date(timeIntervalSince1970: at))
    }

    private func ids(_ rows: [ActionTrayContent.Row]) -> [String] { rows.map(\.id) }

    @Test func progressListsGoingTasksFirstThenFinished() {
        let content = ActionTrayContent(tasks: [
            task("done-new", .done, at: 50),
            task("working-old", .working, at: 10),
            task("asks", .needsYou, at: 5),
            task("working-new", .working, at: 40),
            task("failed", .failed, at: 30),
            task("follow", .followup, at: 20),
        ], own: [], taskAgents: [])
        // a task stopped between rounds (followup) is still open: listed with the open ones and counted
        #expect(ids(content.progress) == ["task:asks", "task:working-new", "task:working-old", "task:follow", "task:done-new", "task:failed"])
        #expect(content.count(.progress) == 4, "every open task is counted, not only the running ones")
    }

    private func plan(_ id: String, _ state: TaskPlan.State, tasks: Int, at: TimeInterval) -> TaskPlan {
        TaskPlan(id: id, conversation: "c", tasks: (0..<tasks).map { PlanTask(title: "task \($0)", prompt: "p", machine: "box", cwd: "/demo") },
                 state: state, createdAt: Date(timeIntervalSince1970: at))
    }

    @Test func plansWaitingForStartHaveTheirOwnEntry() {
        let content = ActionTrayContent(tasks: [], own: [], taskAgents: [], plans: [
            plan("later", .open, tasks: 1, at: 20), plan("first", .open, tasks: 2, at: 10), plan("started", .started, tasks: 3, at: 5),
        ])
        #expect(content.plans.map(\.id) == ["first", "later"])
        #expect(content.count(.start) == 3)
        #expect(content.entries == [.pending, .start, .progress, .agents])
        // not counted twice
        #expect(content.count(.pending) == 0)
        #expect(ActionTrayContent(tasks: [], own: [], taskAgents: [], plans: [plan("gone", .dismissed, tasks: 1, at: 1)]).entries == [.pending, .progress, .agents])
    }

    @Test func pendingIsWaitingTasksThenOwnWaitingAgents() {
        let content = ActionTrayContent(tasks: [task("asks", .needsYou, at: 1), task("works", .working, at: 2)],
                                        own: [agent("own-asks", .needsYou), agent("own-works", .working)],
                                        taskAgents: [agent("task-asks", .needsYou)])
        // A task's waiting agent is its task's row already; only the agents of their own are listed besides.
        #expect(ids(content.pending) == ["task:asks", "agent:box/own-asks"])
        #expect(content.count(.pending) == 2)
    }

    @Test func agentsAreOwnAndTasksOnceWaitingFirstNewestFirst() {
        let shared = agent("both", .working, since: 30)
        let content = ActionTrayContent(tasks: [],
                                        own: [agent("old", .working, since: 10), shared],
                                        taskAgents: [shared, agent("asks", .needsYou, since: 1), agent("new", .working, since: 40)])
        #expect(ids(content.agents) == ["agent:box/asks", "agent:box/new", "agent:box/both", "agent:box/old"])
        #expect(content.count(.agents) == 4)
    }

    @Test func emptyConversationCountsNothing() {
        let content = ActionTrayContent(tasks: [], own: [], taskAgents: [])
        for tray in ActionTray.allCases {
            #expect(content.rows(tray).isEmpty)
            #expect(content.count(tray) == 0)
        }
    }

    private func height(_ view: some View, _ size: DynamicTypeSize) -> CGFloat {
        let r = ImageRenderer(content: view.fixedSize().environment(\.dynamicTypeSize, size))
        r.scale = 1
        return r.cgImage.map { CGFloat($0.height) } ?? 0
    }

    /// Every look of the bar's entries is exactly as tall as the dock's All and its pills, at every text size.
    @Test(arguments: [DynamicTypeSize.large, .xxxLarge, .accessibility2])
    func entriesAreAsLowAsTheDock(size: DynamicTypeSize) {
        let all = height(Text("All 7").dockCapsule(), size)
        let pill = height(DockPillLabel(title: "Fix the widget", state: .working, waiting: false) {
            StatusDot(state: .working, size: Theme.Size.dot)
        }, size)
        #expect(all > 0)
        #expect(abs(all - pill) <= 1, "All \(all) pt, a pill \(pill) pt")
        for look in [DockCapsule.Look.plain, .selected, .waiting(.needsYou)] {
            let entry = height(HStack(spacing: Theme.Space.xs) { Text("Progress"); Text("3").monospacedDigit() }.dockCapsule(look), size)
            #expect(abs(entry - all) <= 1, "\(look) is \(entry) pt, All \(all) pt at \(size)")
        }
    }

    /// At the default text size the capsule is the caption's line (footnote, 13 pt) plus 6 pt above and below: 28 pt
    /// for Latin text, nowhere near the 44 pt tap-target buttons the bar must not become.
    @Test func defaultCapsuleIsTheCaptionLineAndItsPadding() {
        let h = height(Text("Pending").dockCapsule(), .large)
        let line = UIFont.preferredFont(forTextStyle: .footnote, compatibleWith: UITraitCollection(preferredContentSizeCategory: .large)).lineHeight
        #expect(abs(h - (line + 2 * Theme.Space.sm)) <= 1, "\(h) pt for a \(line) pt line")
        #expect(h < Theme.Size.tapTarget)
    }
}
