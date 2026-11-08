import Foundation
import Testing
@testable import Persona

/// A stopped session's work state as the lists show it (`Session.work`, docs/assistant/agent-work.md; the web's lib/work.ts).
@MainActor
struct SessionWorkTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func session(state: String = "idle", lastText: String = "最后一句", work: String?) throws -> Session {
        let w = work.map { #","work":\#($0)"# } ?? ""
        let json = #"{"session_id":"s","status":"done","state":"\#(state)","last_text":"\#(lastText)"\#(w)}"#
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    private func at(_ minutes: Double) -> String {
        ISO8601DateFormatter().string(from: now.addingTimeInterval(minutes * 60))
    }

    @Test func decodesTheWorkState() throws {
        let s = try session(work: #"{"phase":"blocked","on":"agent","line":"等登录页那个 Agent","wait":"它合并","wake_at":"\#(at(10))","attempts":1,"max":6,"agent":{"machine_id":"m","session_id":"x","label":"登录页"},"by":"model"}"#)
        let w = try #require(s.work)
        #expect(w.phase == "blocked")
        #expect(w.on == "agent")
        #expect(w.wait == "它合并")
        #expect(w.wakeAt == now.addingTimeInterval(600))
        #expect(w.attempts == 1 && w.max == 6)
        #expect(w.agent?.sessionID == "x" && w.agent?.label == "登录页")
    }

    @Test func anOddWorkStateNeverDropsTheSession() throws {
        let s = try session(work: #"{"line":"没有 phase"}"#)
        #expect(s.work == nil)
        #expect(s.listPreview(muted: false) == "最后一句")
    }

    @Test func doneShowsItsConclusion() throws {
        let s = try session(work: #"{"phase":"done","line":"已合并 PR #12，测试全过"}"#)
        #expect(s.workLine(now: now) == "✓ 已合并 PR #12，测试全过")
        #expect(s.listPreview(muted: false) == "✓ 已合并 PR #12，测试全过")
    }

    @Test func blockedSaysWhatItWaitsForAndWhenTheHubActs() throws {
        #expect(try session(work: #"{"phase":"blocked","on":"user","line":"等你选 D2 / D4"}"#).workLine(now: now) == "等你选 D2 / D4")
        #expect(try session(work: #"{"phase":"blocked","on":"external","line":"等部署","wake_at":"\#(at(9))"}"#).workLine(now: now) == "等部署（9 分钟后叫醒）")
        #expect(try session(work: #"{"phase":"blocked","on":"external","line":"等部署","wake_at":"\#(at(65))"}"#).workLine(now: now) == "等部署（1 小时 5 分后叫醒）")
        #expect(try session(work: #"{"phase":"blocked","on":"external","line":"等部署","wake_at":"\#(at(120))"}"#).workLine(now: now) == "等部署（2 小时后叫醒）")
        #expect(try session(work: #"{"phase":"blocked","on":"transient","line":"模型出错","wake_at":"\#(at(0.4))"}"#).workLine(now: now) == "模型出错（马上重试）")
        #expect(try session(work: #"{"phase":"blocked","on":"transient","line":"模型出错","wake_at":"\#(at(3))"}"#).workLine(now: now) == "模型出错（3 分钟后重试）")
        // A wake already due reads as now, never as minus minutes.
        #expect(try session(work: #"{"phase":"blocked","on":"external","line":"等部署","wake_at":"\#(at(-2))"}"#).workLine(now: now) == "等部署（马上叫醒）")
    }

    @Test func noLineWhileItWorksOrWithoutOne() throws {
        #expect(try session(work: #"{"phase":"working"}"#).workLine(now: now) == nil)
        #expect(try session(state: "working", work: #"{"phase":"done","line":"旧的结论"}"#).workLine(now: now) == nil)
        #expect(try session(work: #"{"phase":"done","line":""}"#).workLine(now: now) == nil)
        #expect(try session(work: nil).listPreview(muted: false) == "最后一句")
        #expect(try session(state: "working", work: #"{"phase":"done","line":"旧的结论"}"#).listPreview(muted: false) == "最后一句")
    }

    /// The host's own reason for waiting still comes first (botmux's attention).
    @Test func theHostsReasonStaysFirst() throws {
        let json = #"{"session_id":"s","status":"blocked","state":"needs_you","attention_reason":"要你确认","work":{"phase":"blocked","on":"user","line":"等你确认"}}"#
        let s = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        #expect(s.listPreview(muted: false) == "要你确认")
    }

    @Test func theHubWaitsOutRetriesAndWakesButNotTheUser() throws {
        let wake = try session(work: #"{"phase":"blocked","on":"external","line":"等部署","wake_at":"\#(at(9))"}"#)
        #expect(wake.work?.hubWaits == true)
        #expect(wake.isWaiting)
        #expect(try session(work: #"{"phase":"blocked","on":"external","line":"等它自己的后台命令"}"#).work?.hubWaits == true)
        #expect(try session(work: #"{"phase":"blocked","on":"external","line":"等 CI","wait":"CI 跑完"}"#).work?.hubWaits == false)
        #expect(try session(work: #"{"phase":"blocked","on":"user","line":"等你","wake_at":"\#(at(9))"}"#).work?.hubWaits == false)
        #expect(try session(work: #"{"phase":"done","line":"做完了"}"#).isWaiting == false)
        // Unread stays unread: Waiting is only for one with nothing to look at.
        #expect(try session(state: "unread", work: #"{"phase":"blocked","on":"external","line":"等部署","wake_at":"\#(at(9))"}"#).isWaiting == false)
    }

    @Test func aSessionTheHubWaitsOutRanksAsWaitingAndStaysOutOfTheIdleFold() throws {
        let order = AgentListOrder(isPinned: { _ in false }, isMuted: { _ in false })
        let wake = try session(work: #"{"phase":"blocked","on":"transient","line":"模型出错","wake_at":"\#(at(1))"}"#)
        let done = try session(work: #"{"phase":"done","line":"做完了"}"#)
        #expect(order.rank(wake) == AgentListOrder.stateKeys.firstIndex(of: "waiting"))
        #expect(order.stays(wake))
        #expect(order.rank(done) == AgentListOrder.stateKeys.firstIndex(of: "idle"))
        #expect(!order.stays(done))
    }
}
