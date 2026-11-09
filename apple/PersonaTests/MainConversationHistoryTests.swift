import Foundation
import Testing
@testable import Persona

@MainActor
struct MainConversationHistoryTests {
    private func item(_ seq: Int, role: String, text: String, kind: ItemKind = .text) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text,
             toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
    }

    @Test func supervisionAndCompactionNeverEvictRealUserQuestions() {
        var items: [Item] = []
        for question in 1...3 {
            var user = item(question * 10, role: "user", text: "Question \(question)")
            user.clientMsgID = "client-\(question)"
            if question == 2 {
                var image = user
                image.id = "\(user.seq)#image"
                image.kind = .image
                image.text = nil
                image.image = ImagePayload(mime: "image/png", data: "iVBORw0KGgo=")
                items.append(image)
            }
            items.append(user)
            items.append(item(question * 10 + 1, role: "assistant", text: "An answer"))
            if question < 3 {
                var boundary = item(question * 10 + 2, role: "system", text: ChatStore.compactedText, kind: .notice)
                boundary.compact = Compaction(trigger: "auto", preTokens: 170_000)
                items.append(boundary)
                items.append(item(question * 10 + 3, role: "system", text: ChatStore.continuedText, kind: .notice))
            }
            for n in 0..<3 {
                var supervision = item(question * 10 + 4 + n, role: "user", text: "Automatic task supervision")
                supervision.injected = Injected(kind: "system", state: "supervise", line: "Review a synthetic task")
                items.append(supervision)
            }
        }
        let tail = ChatCache.tail(items, turns: 2)
        #expect(tail.filter { $0.kind == .text && $0.clientMsgID != nil }.compactMap(\.text) == ["Question 2", "Question 3"])
        #expect(tail.filter { $0.kind == .image }.count == 1)
        #expect(tail.filter { $0.injected != nil }.count == 6)
        #expect(ChatCache.tail(items, turns: 3).map(\.id) == items.map(\.id))

        let blocks = ChatStore.rebuild(items, settled: nil).blocks
        let lines = ChatLines(openWork: []).build(blocks, live: nil)
        let realUsers = lines.compactMap { line -> Item? in
            guard case .row(.user(let user)) = line.content, user.clientMsgID != nil else { return nil }
            return user
        }
        #expect(realUsers.compactMap(\.text) == ["Question 1", "Question 2", "Question 3"])
    }
}
