import Foundation
import Testing
@testable import Persona

/// A task receipt says what happened to the task, not just that a turn ended (#2362; the web since #975): "done" only
/// once its to-do is done, "stopped, not done yet" while it is open, and a turn the model service cut off is interrupted.
struct ReceiptOutcomeTests {
    private func receipt(state: String = "done", summary: String? = nil) -> FeedItem {
        var body: [String: JSONValue] = ["title": .string("Example task"), "state": .string(state)]
        if let summary { body["summary"] = .string(summary) }
        return FeedItem(id: "r1", conversation: "c1", at: .distantPast, kind: .receipt, ref: "todo_example", body: .object(body))
    }

    private func todo(done: Bool) -> TodoItem {
        TodoItem(id: "todo_example", title: "Example task", doneAt: done ? Date(timeIntervalSince1970: 100) : nil)
    }

    /// A receipt names the task as it is called now (#3036): renamed after the receipt was written, the receipt follows;
    /// with the to-do gone from the list it keeps the name it was written with.
    @Test func aReceiptUsesTheTodosTitleNow() {
        var renamed = todo(done: false)
        renamed.title = "Renamed task"
        #expect(ReceiptCard.title(receipt(), todo: renamed) == "Renamed task")
        #expect(ReceiptCard.title(receipt(), todo: nil) == "Example task")
        renamed.title = "  "
        #expect(ReceiptCard.title(receipt(), todo: renamed) == "Example task")
    }

    @Test func doneOnlyOnceTheTodoIsDone() {
        #expect(ReceiptCard.outcome(receipt(summary: "Merged the fix"), todo: todo(done: true)) == .done)
        #expect(ReceiptCard.outcome(receipt(summary: "Waiting for the soak test"), todo: todo(done: false)) == .stopped)
    }

    @Test func aReceiptWhoseTodoIsGoneStandsOnItsOwn() {
        #expect(ReceiptCard.outcome(receipt(), todo: nil) == .done)
    }

    @Test func failedStaysFailed() {
        #expect(ReceiptCard.outcome(receipt(state: "failed"), todo: todo(done: false)) == .failed)
    }

    @Test func anInterruptedTurnIsNotDone() {
        #expect(ReceiptCard.outcome(receipt(state: "interrupted"), todo: todo(done: true)) == .interrupted(reason: nil))
        // The hub keeps the raw API error in `error`: the reason comes from it, not from the words in `summary`.
        var cut = receipt(state: "interrupted", summary: "Interrupted")
        if case .object(var body) = cut.body { body["error"] = .string("API Error: 529 overloaded"); cut.body = .object(body) }
        guard case .interrupted(let why) = ReceiptCard.outcome(cut, todo: todo(done: false)), let why, !why.isEmpty else {
            Issue.record("the reason comes from the receipt's error")
            return
        }
        // An older hub wrote `done` with the API error as the summary.
        guard case .interrupted(let reason) = ReceiptCard.outcome(receipt(summary: "API Error: 529 overloaded"), todo: todo(done: false)) else {
            Issue.record("an API error summary is an interrupted turn")
            return
        }
        #expect(reason?.isEmpty == false)
    }

    @Test func theInterruptedLineSaysWhyInWords() {
        let line = ReceiptCard.detail(.interrupted(reason: "overloaded"), summary: "API Error: 529 overloaded")
        #expect(line?.contains("529") == false)
        #expect(ReceiptCard.detail(.stopped, summary: "Waiting for the soak test") == "Waiting for the soak test")
        #expect(ReceiptCard.detail(.done, summary: "") == nil)
    }
}
