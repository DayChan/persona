import Foundation
import Testing
@testable import Persona

/// The same whole-round renderer is compiled into iOS, iPadOS and macOS.
@MainActor struct ChatSupervisionLinesTests {
    private func item(_ seq: Int, role: String = "assistant", kind: ItemKind = .text, text: String = "Synthetic answer") -> Item {
        Item(id: "synthetic-\(seq)#0", seq: seq, role: role, kind: kind, text: text)
    }
    private func input(_ seq: Int = 1, level: String? = "triage") -> Item {
        var value = item(seq, role: "user", text: "Complete synthetic supervision input")
        value.injected = Injected(kind: "system", state: "supervise", line: "Synthetic supervision", level: level)
        return value
    }
    private func build(_ items: [Item], open: Set<String> = [], active: Bool = false, streaming: Bool = false) -> [ChatLine] {
        let blocks = ChatStore.rebuild(items, settled: nil).blocks
        let live = active ? blocks.last.flatMap { if case .turn(let t) = $0 { t.id } else { nil } } : nil
        return ChatLines(openWork: [], foldSupervision: true, openSupervision: open, active: active)
            .build(blocks, live: live, streaming: streaming)
    }
    private func quiet(_ lines: [ChatLine]) -> ChatLine? {
        lines.first { if case .supervision = $0.content { true } else { false } }
    }

    @Test func quietRoundIsOneLineAndExpansionRestoresOriginalInputAndAnswer() {
        let items = [input(), item(2)]
        let folded = build(items)
        #expect(folded.count == 1)
        guard let header = quiet(folded) else { Issue.record("missing supervision header"); return }
        let expanded = build(items, open: [header.id])
        #expect(expanded.count == 3)
        #expect(expanded.contains { if case .supervisionInput(let i) = $0.content { i.text == items[0].text } else { false } })
        #expect(expanded.contains { if case .row(.assistant(let i)) = $0.content { i.id == items[1].id } else { false } })
        #expect(build(items).count == 1)
    }

    @Test func recapsContinuationAndTrailingToolsRemainInsideTheSameRecord() {
        var call = item(3, kind: .toolUse)
        call.toolName = "Bash"
        call.toolUseID = "synthetic-call"
        var result = item(4, role: "user", kind: .toolResult, text: "Synthetic result")
        result.toolUseID = call.toolUseID
        let items = [input(), item(2), call, result, item(5, kind: .recap), item(6)]
        let folded = build(items)
        #expect(folded.count == 1)
        guard case .supervision(_, let trace, _)? = quiet(folded)?.content else { return }
        #expect(trace.contains { if case .work(_, let items, _, _, _) = $0.content { items.contains { if case .step(.call(let c)) = $0 { c.result?.text == "Synthetic result" } else { false } } } else { false } })
        #expect(trace.contains { if case .row(.recap) = $0.content { true } else { false } })
        #expect(trace.contains { if case .row(.assistant(let i)) = $0.content { i.seq == 6 } else { false } })
    }

    @Test func nextQueuedUserInputAndItsAnswerNeverJoinSupervision() {
        var next = item(3, role: "user", text: "Real input")
        next.queued = true
        next.clientMsgID = "synthetic-real-user"
        let lines = build([input(), item(2), next, item(4)])
        #expect(lines.count == 3)
        #expect(quiet(lines) != nil)
        #expect(lines.contains { if case .row(.user(let i)) = $0.content { i.id == next.id } else { false } })
        #expect(lines.last?.holds("synthetic-4#0") == true)
    }

    @Test func userLevelAndRetainedPromotionDisplayOrdinaryLines() {
        #expect(quiet(build([input(level: "user"), item(2)])) == nil)
        var answer = item(2)
        answer.supervisionUser = true
        #expect(quiet(build([input(), answer])) == nil)
    }

    @Test func successfulNotifyStaysOutsideOrdinaryWorkFold() {
        var call = item(2, kind: .toolUse)
        call.toolName = "mcp__persona__notify_user"
        call.toolUseID = "notify"
        var result = item(3, role: "user", kind: .toolResult, text: "sent")
        result.toolUseID = "notify"
        let lines = build([input(), call, result, item(4)])
        #expect(quiet(lines) == nil)
        #expect(lines.contains { if case .workStep(_, .step(.call(let c)), _) = $0.content { c.id == call.id } else { false } })
    }

    @Test func liveTriageHidesStreamingUntilOpenedAndPendingQuestionPromotes() {
        let items = [input(), item(2)]
        let folded = build(items, active: true, streaming: true)
        guard let header = quiet(folded) else { Issue.record("Expected a quiet supervision record"); return }
        #expect(ChatLines.hidesLiveSupervision(folded, open: []))
        #expect(!ChatLines.hidesLiveSupervision(folded, open: [header.id]))
        #expect(!ChatLines.hidesLiveSupervision(folded, open: [], openAll: true))
        var ask = item(3, kind: .toolUse)
        ask.toolName = "mcp__persona__ask_user"
        ask.toolUseID = "pending-ask"
        #expect(quiet(build(items + [ask], active: true)) == nil)
        #expect(quiet(build(items + [ask], active: false)) != nil)
    }

    @Test func resumedLiveTextAfterRecapRemainsQuiet() {
        let lines = build([input(), item(2), item(3, kind: .recap)], active: true, streaming: true)
        #expect(ChatLines.hidesLiveSupervision(lines, open: []))
    }

    @Test func stoppedQuietRoundStillHidesItsPendingStreamUntilTheNextInput() {
        let folded = build([input(), item(2)])
        guard let header = quiet(folded) else { Issue.record("Expected a quiet supervision record"); return }
        #expect(ChatLines.hidesLiveSupervision(folded, open: []))
        #expect(ChatLines.hidesLiveSupervision(folded + [.day(Date(timeIntervalSince1970: 0))], open: []))
        #expect(!ChatLines.hidesLiveSupervision(folded, open: [header.id]))
        let next = item(3, role: "user", text: "A genuine next input")
        #expect(!ChatLines.hidesLiveSupervision(build([input(), item(2), next, item(4)]), open: []))
    }

    @Test func splitInjectedInputPartsAreAllRawAuditContentRatherThanUserBubbles() {
        var extra = item(1, role: "user", text: "Additional synthetic model instructions")
        extra.id = "synthetic-1#1"
        let items = [input(), extra, item(2)]
        let folded = build(items)
        guard let header = quiet(folded) else { Issue.record("Expected a quiet supervision record"); return }
        let expanded = build(items, open: [header.id])
        #expect(!expanded.contains { if case .row(.user) = $0.content { true } else { false } })
        #expect(expanded.contains { if case .supervisionInput(let i) = $0.content { i.text?.contains("Additional synthetic") == true } else { false } })
    }

    @Test func automaticLiveWorkExpansionNeverOpensAQuietRecord() {
        var call = item(2, kind: .toolUse)
        call.toolName = "Bash"
        call.toolUseID = "synthetic-live-call"
        let blocks = ChatStore.rebuild([input(), call], settled: nil).blocks
        guard case .turn(let turn)? = blocks.last else { return }
        var renderer = ChatLines(openWork: [], opensLive: true, foldSupervision: true, active: true)
        let folded = renderer.build(blocks, live: turn.id)
        #expect(folded.count == 1)
        #expect(ChatLines.hidesLiveSupervision(folded, open: []))
        guard let header = quiet(folded) else { Issue.record("Expected a quiet supervision record"); return }
        renderer.openSupervision = [header.id]
        let expanded = renderer.build(blocks, live: turn.id)
        #expect(expanded.contains { if case .workStep(_, .step(.call(let c)), _) = $0.content { c.id == call.id } else { false } })
    }

    @Test func agentConversationDoesNotAdoptMainConversationFolding() {
        let blocks = ChatStore.rebuild([input(), item(2)], settled: nil).blocks
        let baseline = ChatLines(openWork: []).build(blocks, live: nil)
        #expect(quiet(baseline) == nil)
        #expect(baseline.count == 2)
    }
}
