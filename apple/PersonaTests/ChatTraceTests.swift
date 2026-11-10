import Foundation
import Testing
@testable import Persona

/// Copy Diagnostics (#3783): the chat's trace says where a reply went in structure only, and never carries a word of it.
@MainActor
struct ChatTraceTests {
    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, tool: String? = nil, id: String? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: id, toolName: tool, toolInput: nil, isError: nil, image: nil)
    }

    private let secret = "不该出现在诊断里的话"

    @Test func theEndOfTheChatTellsTheAnswerFromItsExecutionSummary() {
        var wake = item(4, .notice, role: "system", text: "后台任务完成")
        wake.wake = true
        let long = String(repeating: secret, count: 20)
        // The reply after the wake is the answer (the newest words, #3783), the longer words before it in the work.
        let items = [item(1, .text, role: "user", text: secret), item(2, .toolUse, tool: "Bash", id: "a"), item(3, .text, text: long),
                     wake, item(5, .toolUse, tool: "Bash", id: "b"), item(6, .text, text: "好了")]
        let lines = ChatLines(openWork: []).build(ChatBlock.build(ChatStore.buildRows(items)), live: nil)
        let screen = ChatTrace.screen(lines, live: 0, streaming: false, running: false, frozen: false, withheld: 0)
        #expect(screen.contains("a6"))
        #expect(screen.contains("work(4)"))
        #expect(!screen.contains("a3"))
        #expect(!screen.contains(secret) && !screen.contains("好了"))

        let batch = ChatTrace.batch(items, span: ItemSpan(history: "h", from: 0, to: 6), backlog: false, gap: .none, resets: 0, covered: 6)
        #expect(batch == "items n=6 seq=1-6 [a2,tool_use2,u1,wake1] span=0..6 gap=none covered=6 resets=0")
        #expect(!batch.contains(secret))
    }

    @Test func keepsTheNewestLinesOfAFewChats() {
        var now = Date(timeIntervalSince1970: 0)
        let trace = ChatTrace(load: false) { now }
        let a = SessionKey(machineID: "m", sessionID: "a")
        trace.note(a, "open")
        trace.note(a, "open") // the same again: one line
        #expect(trace.lines(a).count == 1)
        for n in 0..<(ChatTrace.limit + 10) {
            now += 1
            trace.note(a, "line \(n)")
        }
        #expect(trace.lines(a).count == ChatTrace.limit)
        #expect(trace.lines(a).last?.hasSuffix("line \(ChatTrace.limit + 9)") == true)
        // A fifth chat pushes out the one not written to for longest.
        for s in ["b", "c", "d", "e"] { trace.note(SessionKey(machineID: "m", sessionID: s), "open") }
        #expect(trace.lines(a).isEmpty)
        #expect(trace.text(SessionKey(machineID: "m", sessionID: "e")).hasPrefix("Persona 对话诊断"))
    }
}
