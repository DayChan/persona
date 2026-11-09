import Foundation
import Testing
@testable import Persona

/// Pages of long sessions come with the agent's work folded (`Item.fold`, docs/protocol/protocol.md「折叠的工作」): the work
/// line counts what a fold stands for, a fold replaces the steps it covers, and opening it puts the steps back.
@MainActor
struct FoldedWorkTests {
    private func item(_ seq: Int, _ kind: ItemKind, role: String = "assistant", text: String? = nil, tool: String? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: Date(timeIntervalSince1970: TimeInterval(seq)), role: role, kind: kind, text: text,
             toolUseID: tool, toolName: tool == nil ? nil : "Bash", toolInput: nil, isError: nil, image: nil)
    }

    private func fold(_ from: Int, _ through: Int, tools: Int, thoughts: Int = 0) -> Item {
        var f = item(from, .fold)
        f.id = "fold:\(from)#0"
        f.fold = Fold(items: through - from + 1, tools: tools, thoughts: thoughts, until: Date(timeIntervalSince1970: TimeInterval(through)),
                      throughSeq: through, throughID: "\(through)#0")
        return f
    }

    /// Steps 2…7: a thought, then three calls with their results (the result of the last one at 8 is outside).
    private func work() -> [Item] {
        [item(2, .thinking, text: "plan"),
         item(3, .toolUse, tool: "a"), item(4, .toolResult, role: "user", text: "ok", tool: "a"),
         item(5, .toolUse, tool: "b"), item(6, .toolResult, role: "user", text: "ok", tool: "b"),
         item(7, .toolUse, tool: "c"), item(8, .toolResult, role: "user", text: "ok", tool: "c")]
    }

    private func store() -> ChatStore { ChatStore(key: SessionKey(machineID: "test", sessionID: "fold-\(UUID())")) }

    /// Rows are rebuilt off the main actor: wait for the first exchange to show up (at most 2 s).
    private func rebuilt(_ s: ChatStore) async {
        for _ in 0..<200 {
            if case .turn = s.blocks.first { return }
            try? await Task.sleep(for: .milliseconds(10))
        }
    }

    @Test func aFoldCountsWhatItStandsFor() {
        let items = [item(1, .text, role: "user", text: "go"), fold(2, 8, tools: 3, thoughts: 1), item(9, .text, text: "done")]
        let blocks = ChatBlock.build(ChatStore.buildRows(items))
        guard case .turn(let t) = blocks.first else { Issue.record("no turn: \(blocks)"); return }
        #expect(t.toolCount == 3 && t.thoughtCount == 1)
        if case .assistant(let a) = t.answer { #expect(a.text == "done") } else { Issue.record("answer") }
        // The work ends where its last step did.
        #expect(t.process.last?.lastTS == Date(timeIntervalSince1970: 8))
    }

    @Test func aFoldReplacesTheStepsItCoversAndOpeningItPutsThemBack() async {
        let s = store()
        let prompt = item(1, .text, role: "user", text: "go"), answer = item(20, .text, text: "done")
        s.merge([prompt] + work() + [answer], backlog: true, hasMore: false)
        // A page with the same work folded (the latest page again): the steps give way to the fold.
        let f = fold(2, 8, tools: 3, thoughts: 1)
        s.merge([prompt, f, answer], backlog: true, hasMore: false)
        #expect(s.items.map(\.id) == ["1#0", "fold:2#0", "20#0"])
        s.merge([f]) // the same fold again changes nothing
        #expect(s.items.map(\.id) == ["1#0", "fold:2#0", "20#0"])
        // The turn went on (pushed steps), then a page folds the longer run: it replaces the old fold and the steps.
        let more = [item(9, .toolUse, tool: "d"), item(10, .toolResult, role: "user", text: "x", tool: "d")]
        s.merge(more)
        let longer = fold(2, 10, tools: 4, thoughts: 1)
        s.merge([prompt, longer, answer], backlog: true, hasMore: false)
        #expect(s.items.map(\.id) == ["1#0", "fold:2#0", "20#0"])
        #expect(s.items[1].fold?.tools == 4)

        // Opening it: the steps take the fold's place, in order.
        s.unfold(longer, into: work() + more)
        #expect(s.items.map(\.seq) == [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 20])
        await rebuilt(s)
        guard case .turn(let t) = s.blocks.first else { Issue.record("no turn"); return }
        #expect(t.toolCount == 4)
    }

    /// Work opened here stays open when a page folds it again. As a chat opens, the latest page can come twice (over
    /// HTTP and in the socket's backlog); the fold put back over its steps had already had its row fetch them, and the
    /// row stayed on "Loading N steps…" (#6038). A fold that reaches further covers steps not fetched here, and a page
    /// that starts over (a reload) has none of them: those fold the work again.
    @Test func workOpenedHereStaysOpenWhenAPageFoldsItAgain() {
        let s = store()
        let prompt = item(1, .text, role: "user", text: "go"), answer = item(20, .text, text: "done")
        let f = fold(2, 8, tools: 3, thoughts: 1), page = ItemSpan(history: "h", from: nil, to: 20)
        s.apply([prompt, f, answer], span: page, backlog: true, hasMore: false)
        s.unfold(f, into: work())
        s.apply([prompt, f, answer], span: page, backlog: true, hasMore: false)
        #expect(s.items.map(\.seq) == [1, 2, 3, 4, 5, 6, 7, 8, 20])
        s.apply([prompt, fold(2, 10, tools: 4, thoughts: 1), answer], span: page, backlog: true, hasMore: false)
        #expect(s.items.map(\.id) == ["1#0", "fold:2#0", "20#0"])
        s.replace(with: [prompt, f, answer], hasMore: false, span: page)
        #expect(s.items.map(\.id) == ["1#0", "fold:2#0", "20#0"] && s.items[1].fold?.throughSeq == 8)
    }

    /// A tool call outside the fold (still running when the page was cut) keeps its result when the result comes.
    @Test func stepsOutsideAFoldStay() {
        let s = store()
        s.merge([item(1, .text, role: "user", text: "go"), fold(2, 6, tools: 2, thoughts: 1), item(7, .toolUse, tool: "c")], backlog: true)
        s.merge([item(8, .toolResult, role: "user", text: "ok", tool: "c")])
        #expect(s.items.map(\.seq) == [1, 2, 7, 8])
        let rows = ChatStore.buildRows(s.items)
        guard case .tools(let steps) = rows.last else { Issue.record("rows \(rows)"); return }
        #expect(steps.count == 2)
        if case .call(let c) = steps.last { #expect(c.result?.text == "ok") } else { Issue.record("call") }
    }

    @Test func partialOverlapRetainsPromotionAfterRemovingTheFold() throws {
        let s = store()
        var f = fold(2, 8, tools: 3, thoughts: 1)
        f.fold?.supervisionUser = true
        s.merge([item(1, .text, role: "user", text: "Fictional input"), f, item(10, .text, text: "Fictional answer")])
        // Only a thought arrives: the actual visibility tool may still be on an unloaded part of the fold.
        let thought = item(2, .thinking, text: "Fictional plan")
        s.merge([thought])
        #expect(!s.items.contains { $0.kind == .fold })
        #expect(s.items.first { $0.id == thought.id }?.supervisionUser == true)
        #expect(Supervision.visible(s.items, results: [:], live: false))
        s.merge([thought]) // An unmarked duplicate cannot erase the transferred evidence.
        #expect(s.items.first { $0.id == thought.id }?.supervisionUser == true)
        var finish = item(7, .toolUse, tool: "finish")
        finish.toolName = "supervision_finish"
        let silent = item(8, .toolResult, role: "user", text: #"{"visibility":"silent"}"#, tool: "finish")
        s.merge([finish, silent])
        #expect(Supervision.visible(s.items, results: ["finish": silent], live: false))
        let persisted = try JSON.decoder.decode([Item].self, from: JSON.encoder.encode(s.items))
        #expect(Supervision.visible(persisted, results: ["finish": silent], live: false))
        #expect(persisted.first { $0.id == thought.id }?.text == thought.text)
    }

    @Test func duplicateFoldPromotionUpdatesAndNeverDowngrades() {
        let s = store()
        let quiet = fold(2, 8, tools: 3)
        s.merge([quiet])
        #expect(!Supervision.visible(s.items, results: [:], live: false))
        var promoted = quiet
        promoted.fold?.supervisionUser = true
        s.merge([promoted])
        #expect(s.items.count == 1 && s.items[0].fold?.supervisionUser == true)
        s.merge([quiet])
        #expect(s.items.count == 1 && s.items[0].fold?.supervisionUser == true)
        #expect(Supervision.visible(s.items, results: [:], live: false))
    }

    @Test func promotionTransfersBackIntoAReplacementFold() {
        let s = store()
        var thought = item(2, .thinking, text: "Fictional plan")
        thought.supervisionUser = true
        s.merge([thought, item(3, .toolUse, tool: "read")])
        let quiet = fold(2, 8, tools: 3)
        s.merge([quiet])
        #expect(s.items.count == 1 && s.items[0].fold?.supervisionUser == true)
        s.merge([quiet])
        #expect(Supervision.visible(s.items, results: [:], live: false))
        s.unfold(quiet, into: [item(2, .thinking, text: "Fictional plan")]) // The caller's stale fold has no mark.
        #expect(s.items[0].supervisionUser == true)
    }

    @Test func aRepeatedPromotedFoldUpdatesWorkThatStaysOpen() {
        let s = store()
        let quiet = fold(2, 8, tools: 3)
        s.merge([quiet])
        s.unfold(quiet, into: work())
        var promoted = quiet
        promoted.fold?.supervisionUser = true
        s.merge([promoted])
        #expect(!s.items.contains { $0.kind == .fold })
        #expect(s.items.allSatisfy { $0.supervisionUser == true })
        s.merge([quiet])
        #expect(Supervision.visible(s.items, results: [:], live: false))
        s.merge([fold(2, 10, tools: 4)]) // A longer page may fold the work again, but its promotion stays.
        #expect(s.items.count == 1 && s.items[0].fold?.supervisionUser == true)
    }

    @Test func promotionUsesBlockBoundsAndIncludesToolImagesNotUserImages() {
        let s = store()
        var f = fold(2, 3, tools: 1)
        f.id = "fold:2#1"
        f.fold?.throughID = "3#1"
        f.fold?.supervisionUser = true
        s.merge([f])
        let before = item(2, .thinking) // Same seq, outside the fold's opening block.
        var covered = item(3, .image, role: "user", tool: "read")
        covered.id = "3#1"
        var outside = covered
        outside.id = "3#2"
        var own = item(2, .image, role: "user")
        own.id = "2#2" // Inside the seq range, but user input is not folded work.
        s.merge([before, covered, outside, own])
        #expect(!s.items.contains { $0.kind == .fold })
        #expect(s.items.first { $0.id == covered.id }?.supervisionUser == true)
        #expect(s.items.first { $0.id == outside.id }?.supervisionUser != true)
        #expect(s.items.first { $0.id == before.id }?.supervisionUser != true)
        #expect(s.items.first { $0.id == own.id }?.supervisionUser != true)
    }

    @Test func ordinaryPartialOverlapDoesNotInventPromotion() {
        let s = store()
        s.merge([fold(2, 8, tools: 3)])
        s.merge([item(3, .toolUse, tool: "read")])
        #expect(s.items.map(\.seq) == [3])
        #expect(!Supervision.visible(s.items, results: [:], live: false))
    }

    @Test func aMixedBatchKeepsRealWorkInsteadOfItsOverlappingPromotedFold() {
        let s = store()
        var f = fold(2, 8, tools: 3)
        f.fold?.supervisionUser = true
        let read = item(3, .toolUse, tool: "read")
        s.merge([f, read])
        #expect(s.items.map(\.id) == [read.id])
        #expect(s.items[0].supervisionUser == true)
    }

    @Test func reverseOrderPagesAndDuplicateReceiptsKeepTheirOriginalTool() {
        let s = store()
        var notify = item(2, .toolUse, tool: "notify")
        notify.toolName = "notify_user"
        var receipt = item(3, .toolResult, role: "user", text: "failed", tool: "notify")
        receipt.isError = true
        s.merge([receipt, item(6, .text, text: "Fictional answer")])
        s.merge([item(1, .text, role: "user", text: "Fictional input"), notify])
        #expect(!Supervision.visible(s.items, results: ["notify": receipt], live: false))
        receipt.isError = false
        receipt.text = "ok"
        s.merge([receipt])
        let results = Dictionary(uniqueKeysWithValues: s.items.filter { $0.kind == .toolResult }.map { ($0.toolUseID!, $0) })
        #expect(Supervision.visible(s.items, results: results, live: false))
        #expect(s.items.map(\.seq) == [1, 2, 3, 6])
        #expect(s.items.filter { $0.id == receipt.id }.count == 1)
    }

    @Test func aNewHistoryDropsOldPromotionAndOpenedFoldEvidence() {
        let s = store()
        var promoted = fold(2, 8, tools: 3)
        promoted.fold?.supervisionUser = true
        s.apply([promoted], span: ItemSpan(history: "old", to: 8))
        s.unfold(promoted, into: [item(3, .toolUse, tool: "read")])
        #expect(s.items[0].supervisionUser == true)
        s.apply([fold(2, 8, tools: 3)], span: ItemSpan(history: "new", to: 8))
        #expect(s.items.count == 1 && s.items[0].kind == .fold)
        #expect(!Supervision.visible(s.items, results: [:], live: false))
    }
}
