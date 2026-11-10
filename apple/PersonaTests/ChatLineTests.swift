import Testing
@testable import Persona

@MainActor
struct ChatLineTests {
    @Test func workExpansionKeepsExistingIdentities() {
        let user = RebuildTests.item(1, "user", .text, text: "question")
        let tool = RebuildTests.item(2, "assistant", .toolUse, tool: "call")
        let answer = RebuildTests.item(3, "assistant", .text, text: "answer")
        let step = WorkItem.step(.call(ToolCall(use: tool)))
        let header = ChatLine.work(id: "w-1", items: [step], live: false, start: nil, end: nil)
        let closed = [ChatLine.row(.user(user)), header, .row(.assistant(answer))]
        let open = [closed[0], header, .workStep(group: header.id, item: step, first: true),
                    .workEnd(group: header.id, count: 1), closed[2]]
        #expect(open.map(\.id) == [user.id, "w-1", "w-1/" + tool.id, "w-1-end", answer.id])
        #expect(Array(open.reversed()).reversed().map(\.id) == open.map(\.id))
        #expect(Set(open.map(\.id)).count == open.count)
        #expect([open[0].id, open[1].id, open[4].id] == closed.map(\.id))
    }

    @Test func messageMenuStillBelongsToItsOriginalRow() {
        let user = RebuildTests.item(1, "user", .text, text: "first")
        let line = ChatLine.row(.user(user))
        #expect(line.isInput)
        #expect(line.holds(user.id))
        #expect(!line.holds(nil))
    }
}
