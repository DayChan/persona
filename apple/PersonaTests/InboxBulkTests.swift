import XCTest
@testable import Persona

/// Bulk filing's arithmetic (InboxBulk: Clear All, a section's Mark All Seen / Clear All, Undo): what a request moves, how the list shows it before and after the hub's
/// answer, the toast, and the scope line in the menu.
final class InboxBulkTests: XCTestCase {
    private func item(_ id: String, done: Bool = false, version: String = "v1") throws -> InboxItem {
        let json = """
        {"id":"\(id)","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"\(id)","preview":"","count":1,
         "version":"\(version)"\(done ? ",\"done_at\":\"2026-11-04T08:00:00Z\",\"done_by\":\"read\"" : "")}
        """
        return try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    func testTargetsAreTheOnesNotInThatStateOnce() throws {
        let a = try item("a"), b = try item("b", done: true), c = try item("c")
        XCTAssertEqual(InboxBulk.targets([a, b, c, a], done: true).map(\.id), ["a", "c"])
        XCTAssertEqual(InboxBulk.targets([a, b, c], done: false).map(\.id), ["b"])
        XCTAssertEqual(try JSON.decoder.decode(InboxItem.self, from: JSONEncoder().encode(a)).version, "v1", "the version is kept in the cache")
    }

    func testMarkingThenSettling() throws {
        let a = try item("a"), b = try item("b"), c = try item("c"), d = try item("d")
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let shown = InboxBulk.marking([a, b, c, d], keys: ["a", "b", "c"], done: true, now: now)
        XCTAssertEqual(shown.filter(\.isDone).map(\.id), ["a", "b", "c"])
        XCTAssertEqual(shown.first?.doneBy, "user")
        XCTAssertFalse(shown[3].isDone, "an item outside the scope was filed")

        // The hub filed a, b took a new message meanwhile, c is gone.
        var filed = a
        filed.doneAt = now
        filed.doneBy = "user"
        filed.version = "v2"
        let result = InboxBulkResult(changed: [filed], unchanged: [], skipped: [.init(id: "b", reason: "changed"), .init(id: "c", reason: "missing")])
        let settled = InboxBulk.settle(shown, before: ["a": a, "b": b, "c": c], result: result)
        XCTAssertEqual(settled.map(\.id), ["a", "b", "c", "d"])
        XCTAssertEqual(settled[0].version, "v2", "the hub's copy replaces what was shown")
        XCTAssertFalse(settled[1].isDone, "an item with new messages stays open")
        XCTAssertFalse(settled[2].isDone, "an item the hub did not have goes back as it was")
        XCTAssertEqual(InboxBulk.changedSince(result), 1)
        XCTAssertEqual(InboxBulk.message(done: true, result: result), "已将 1 件标为已处理，1 件有新消息，留在收件箱")
    }

    func testMessages() {
        let one = InboxBulkResult(changed: [], unchanged: ["a"], skipped: [])
        XCTAssertEqual(InboxBulk.message(done: true, result: one), "没有要标的")
        XCTAssertEqual(InboxBulk.message(done: false, result: one), "没有要移回的")
    }

    func testScopeLine() {
        XCTAssertEqual(InboxBulk.scopeLine(filters: [], query: "", count: 12), "全部 · 12 件")
        XCTAssertEqual(InboxBulk.scopeLine(filters: ["飞书", "@我"], query: "", count: 3), "飞书 · @我 · 3 件")
        XCTAssertEqual(InboxBulk.scopeLine(filters: [], query: " 周报 ", count: 2), "搜索「周报」 · 2 件")
        XCTAssertEqual(InboxBulk.scopeLine(filters: ["飞书"], query: "", count: 0), "没有未处理的")
    }

    // MARK: Clear All (POST /api/v1/inbox/bulk)

    func testDismissShowsAsDismissed() throws {
        let a = try item("a"), b = try item("b")
        let shown = InboxBulk.marking([a, b], keys: ["a"], done: true, now: .now, by: .dismiss)
        XCTAssertTrue(shown[0].isDone)
        XCTAssertEqual(shown[0].doneBy, "dismissed")
        XCTAssertFalse(shown[1].isDone)
        XCTAssertNil(InboxBulk.marking(shown, keys: ["a"], done: false, now: .now)[0].doneBy, "brought back has no done_by")
    }

    func testReplyDecodes() throws {
        let reply = try JSON.decoder.decode(InboxBulkReply.self, from: Data("""
        {"action":"dismiss","changed":[{"id":"a","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"a",
          "preview":"","count":1,"done_at":"2026-11-05T08:00:00Z","done_by":"dismissed"}],"ids":["a"],"unchanged":["c"],
         "skipped":[{"id":"b","reason":"changed"}],"undo":{"action":"reopen","ids":["a"]}}
        """.utf8))
        XCTAssertEqual(reply.ids, ["a"])
        XCTAssertEqual(reply.changed.first?.doneBy, "dismissed", "done_by=dismissed reads")
        XCTAssertTrue(reply.changed.first?.isDone == true)
        XCTAssertEqual(reply.undo, .init(action: "reopen", ids: ["a"]))
        let result = InboxBulk.result(reply)
        XCTAssertEqual(result.unchanged, ["c"])
        XCTAssertEqual(InboxBulk.message(done: true, result: result, action: .dismiss), "已清理 1 件，1 件有新消息，留在收件箱")
        XCTAssertEqual(InboxBulk.message(done: true, result: InboxBulkResult(changed: [], unchanged: [], skipped: []), action: .dismiss), "没有要清理的")

        let reopen = try JSON.decoder.decode(InboxBulkReply.self, from: Data(#"{"action":"reopen","changed":[],"ids":[],"unchanged":[],"skipped":[]}"#.utf8))
        XCTAssertNil(reopen.undo, "a reopen has no undo")
        // The body the undo is sent back as: the reply's own, unchanged.
        let sent = try JSONSerialization.jsonObject(with: JSONEncoder().encode(reply.undo!)) as? [String: Any]
        XCTAssertEqual(sent?["action"] as? String, "reopen")
        XCTAssertEqual(sent?["ids"] as? [String], ["a"])
    }

    func testPerformSendsTheBulkRequestAndKeepsItsUndo() async throws {
        let a = try item("a"), b = try item("b")
        var sent: [(InboxBulk.Action, [String])] = []
        var legacyCalls = 0
        var filed = a
        filed.doneAt = .now
        filed.doneBy = "dismissed"
        let (result, undo) = try await InboxBulk.perform(.dismiss, [a, b],
            bulk: { sent.append(($0, $1.map(\.id))); return InboxBulkReply(action: "dismiss", changed: [filed], unchanged: ["b"], undo: .init(action: "reopen", ids: ["a"])) },
            legacy: { _ in legacyCalls += 1; return InboxBulkResult(changed: [], unchanged: [], skipped: []) })
        XCTAssertEqual(sent.count, 1)
        XCTAssertEqual(sent.first?.0, .dismiss)
        XCTAssertEqual(sent.first?.1, ["a", "b"])
        XCTAssertEqual(legacyCalls, 0)
        XCTAssertEqual(result.changed.map(\.id), ["a"])
        XCTAssertEqual(undo, .bulk(.init(action: "reopen", ids: ["a"]), manager: nil), "撤销 sends the hub's undo")
    }

    func testOlderHub() async throws {
        let a = try item("a")
        for status in [404, 405] {
            var legacy: [String] = []
            let (result, undo) = try await InboxBulk.perform(.done, [a],
                bulk: { _, _ in throw APIError(status: status, message: "not found") },
                legacy: { legacy = $0.map(\.id); return InboxBulkResult(changed: $0, unchanged: [], skipped: []) })
            XCTAssertEqual(legacy, ["a"], "\(status): 已处理 goes to the old request")
            XCTAssertEqual(result.changed.map(\.id), ["a"])
            XCTAssertEqual(undo, .legacy([a]), "and 撤销 the old way")

            // Clear All on an old hub falls back to the Done request too (recorded there as done_by=user), not refused.
            legacy = []
            let (cleaned, cleanUndo) = try await InboxBulk.perform(.dismiss, [a],
                bulk: { _, _ in throw APIError(status: status, message: "not found") },
                legacy: { legacy = $0.map(\.id); return InboxBulkResult(changed: $0, unchanged: [], skipped: []) })
            XCTAssertEqual(legacy, ["a"], "\(status): 全部清理 goes to the old request")
            XCTAssertEqual(cleaned.changed.map(\.id), ["a"])
            XCTAssertEqual(cleanUndo, .legacy([a]), "and 撤销 the old way")
        }
    }

    func testPerformFailureRollsBack() async throws {
        let a = try item("a"), b = try item("b")
        let before = ["a": a]
        let shown = InboxBulk.marking([a, b], keys: ["a"], done: true, now: .now, by: .dismiss)
        do {
            _ = try await InboxBulk.perform(.dismiss, [a],
                bulk: { _, _ in throw APIError(status: 500, message: "boom") },
                legacy: { _ in XCTFail("a server error is no older hub"); return InboxBulkResult(changed: [], unchanged: [], skipped: []) })
            XCTFail("the error went missing")
        } catch let e as APIError {
            XCTAssertEqual(e.status, 500)
        }
        XCTAssertEqual(InboxBulk.rollback(shown, before: before), [a, b], "the failure puts the list back as it was")
    }

    func testConfirmsFromTen() {
        XCTAssertFalse(InboxBulk.needsConfirm(9))
        XCTAssertTrue(InboxBulk.needsConfirm(10))
        XCTAssertEqual(InboxBulk.confirmTitle, "全部清理？")
        XCTAssertEqual(InboxBulk.confirmButton(count: 12), "清理 12 件")
        XCTAssertEqual(InboxBulk.confirmMessage(count: 12, filters: ["飞书"], query: ""), "将清理 12 件（飞书）。之后进来的新消息不受影响，完成后可以撤销。")
    }
}
