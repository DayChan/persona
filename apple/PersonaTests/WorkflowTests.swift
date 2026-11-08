import Foundation
import Testing
@testable import Persona

/// Background workflows on a session (#2935, docs/protocol/protocol.md `workflows`): read leniently, and what an older hub without
/// them looks like.
@MainActor struct WorkflowTests {
    private func session(_ extra: String = "", state: String = "working") throws -> Session {
        let json = #"{"machine_id":"box","session_id":"s1","title":"t","cwd":"/code/app","status":"done","state":"\#(state)""#
            + (extra.isEmpty ? "" : "," + extra) + "}"
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    /// The protocol's own example: a running parity-audit, and a killed one.
    private let full = #"""
    "background":[{"id":"wdp5zxh37","kind":"workflow","description":"parity-audit","run_id":"wf_c1d9daf8-cd2"},
                  {"id":"b1","kind":"shell","command":"sleep 60"},
                  {"id":"m1","kind":"monitor","description":"CI","expires_at":"2026-11-05T14:30:00Z"}],
    "workflows":[{"task_id":"wdp5zxh37","run_id":"wf_c1d9daf8-cd2","name":"parity-audit","summary":"…",
      "status":"running","running":true,"phases":["Audit","Verify"],"phase":"Verify","phase_index":2,
      "agents_started":185,"agents_done":169,"agents_running":16,
      "started_at":"2026-11-05T13:00:00Z","updated_at":"2026-11-05T13:14:00Z",
      "agents":[
        {"id":"a1","session_id":"s1@a1","label":"audit:chat-rich","phase":"Audit","running":false,"started_at":"2026-11-05T13:00:01Z","last_text":"ok"},
        {"id":"a2","session_id":"s1@a2","label":"verify:x","phase":"Verify","running":true,"started_at":"2026-11-05T13:10:00Z"},
        {"label":"no id: dropped"}
      ]},
     {"task_id":"wk","name":"docs-sweep","status":"killed","running":false,"agents_started":4,"agents_done":2,
      "started_at":"2026-11-05T12:00:00Z","ended_at":"2026-11-05T12:10:00Z","error":"Error: Workflow aborted"}]
    """#

    @Test func readsTheHubsWorkflows() throws {
        let s = try session(full)
        let runs = try #require(s.workflows)
        #expect(runs.count == 2)
        #expect(s.runningWorkflows.map(\.id) == ["wf_c1d9daf8-cd2"])
        #expect(s.isBackgroundWorking)
        #expect(s.stateLabel == "工作中（后台）")
        let run = try #require(s.leadWorkflow)
        #expect(run.taskID == "wdp5zxh37" && run.title == "parity-audit")
        #expect(run.outcome == .running)
        #expect(run.phaseLabel == "Verify 2/2")
        #expect(run.progress?.done == 169 && run.progress?.started == 185)
        #expect(run.runningAgents == 16)
        #expect(run.line == "后台 workflow「parity-audit」 · Verify 2/2 · 169/185+") // "+": more may start while it runs
        // The agent without an id is left out; the rest in the order they started (#3436: never reordered).
        #expect(run.agents?.map(\.id) == ["a1", "a2"])
        #expect(run.agents?.first?.sessionID == "s1@a1")
        #expect(run.elapsed(now: Date(timeIntervalSince1970: 1793883600 + 840)) == 840)
        let killed = runs[1]
        #expect(killed.id == "wk" && !killed.isRunning && killed.outcome == .stopped)
        #expect(killed.error == "Error: Workflow aborted")
        #expect(killed.elapsed() == 600)
        #expect(killed.line == "workflow「docs-sweep」 · 2/4 · 已停止")
        // Background N: the workflow's copy in `background` is not counted twice.
        #expect(s.background?.count == 3)
        #expect(s.backgroundCount == 3)
        #expect(s.background?.first?.isWorkflow == true && s.background?.first?.runID == "wf_c1d9daf8-cd2")
        #expect(s.background?.first?.title == "parity-audit")
        #expect(s.background?.last?.expiresAt != nil)
    }

    /// A hub before #2935 sends no `workflows`: nothing changes.
    @Test func anOlderHubSendsNoWorkflows() throws {
        let s = try session()
        #expect(s.workflows == nil)
        #expect(s.runningWorkflows.isEmpty && s.leadWorkflow == nil && s.shownWorkflows().isEmpty)
        #expect(!s.isBackgroundWorking)
        #expect(s.stateLabel == s.state.label)
        // #2228's background work alone: working, not marked as a workflow.
        let bg = try session(#""background":[{"id":"b1","kind":"shell","command":"sleep 60"}]"#)
        #expect(!bg.isBackgroundWorking && bg.stateLabel == "工作中" && bg.backgroundCount == 1)
    }

    /// The hub decides the state: a running workflow on an idle agent (an older hub) is not drawn as working, and the
    /// assistant's own conversation never reads as background.
    @Test func stateStaysTheHubs() throws {
        let wf = #""workflows":[{"task_id":"w","status":"running","running":true}]"#
        #expect(try session(wf).isBackgroundWorking)
        #expect(try !session(wf, state: "idle").isBackgroundWorking)
        #expect(try !session(wf + #","assistant":true"#).isBackgroundWorking)
        // A running workflow still keeps an idle agent out of the idle fold.
        #expect(try session(wf, state: "idle").isWaiting)
    }

    /// Optional everything: an unknown status reads as completed; an odd entry or a wrong type never drops the session,
    /// nor its other workflows.
    @Test func lenient() throws {
        let s = try session(#""workflows":[{"task_id":"ok","agents_done":"three","agents":"none","status":"teleported"},{"name":"no id"},42,{"run_id":"r"}]"#)
        #expect(s.workflows?.map(\.id) == ["ok", "r"])
        let ok = try #require(s.workflows?.first)
        #expect(ok.agentsDone == nil && ok.agents == nil)
        #expect(!ok.isRunning && ok.outcome == .completed)
        #expect(ok.title == "Workflow" && ok.progress == nil)
        // running missing: from the status.
        #expect(try session(#""workflows":[{"task_id":"x","status":"running"}]"#).workflows?.first?.isRunning == true)
        #expect(try session(#""workflows":[{"task_id":"x","status":"stalled","running":false}]"#).workflows?.first?.outcome == .stalled)
        #expect(try session(#""workflows":{"task_id":"x"}"#).workflows == nil)
    }

    @Test func progressFromTheAgentsWhenTheHubSendsNoCounts() {
        let run = WorkflowRun(taskID: "w", agents: [
            WorkflowAgent(id: "a"), WorkflowAgent(id: "b"), WorkflowAgent(id: "c", running: true),
        ])
        #expect(run.progress?.done == 2 && run.progress?.started == 3)
        #expect(run.runningAgents == 1)
        #expect(WorkflowRun(taskID: "w").progress == nil)
        #expect(WorkflowRun(taskID: "w", phases: ["A"], phase: "A", phaseIndex: 1).phaseLabel == "A")
        #expect(WorkflowRun(taskID: "w", phases: ["A", "B"], phase: "B", phaseIndex: 0).phaseLabel == "B")
        #expect(WorkflowRun(taskID: "w", status: "failed", running: false).line == "workflow「Workflow」 · 失败")
    }

    @Test func shownWorkflowsKeepARecentEnd() throws {
        let now = Date(timeIntervalSince1970: 1793883600)
        var s = try session()
        s.workflows = [
            WorkflowRun(taskID: "old", status: "completed", running: false, endedAt: now.addingTimeInterval(-7200)),
            WorkflowRun(taskID: "recent", status: "killed", running: false, endedAt: now.addingTimeInterval(-600)),
            WorkflowRun(taskID: "live"),
        ]
        #expect(s.shownWorkflows(now: now).map(\.id) == ["live", "recent"])
        #expect(s.leadWorkflow?.id == "live")
    }

    @Test func durations() {
        #expect(WorkflowRun.duration(12) == "12 秒")
        #expect(WorkflowRun.duration(252) == "4 分 12 秒")
        #expect(WorkflowRun.duration(840, seconds: false) == "14 分钟")
        #expect(WorkflowRun.duration(30, seconds: false) == "不到 1 分钟")
        #expect(WorkflowRun.duration(3780, seconds: false) == "1 小时 3 分钟")
    }

    /// The Agents list keeps an agent with a running workflow out of the idle fold.
    @Test func listOrderKeepsAWorkflowOutOfTheIdleFold() throws {
        let s = try session(#""workflows":[{"task_id":"wf","running":true}]"#, state: "idle")
        let order = AgentListOrder(isPinned: { _ in false }, isMuted: { _ in false })
        #expect(order.stays(s))
        #expect(order.rank(s) == AgentListOrder.stateKeys.firstIndex(of: "waiting"))
    }
}
