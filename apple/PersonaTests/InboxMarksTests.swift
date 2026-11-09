import XCTest
@testable import Persona

/// The marks laid over lists that may not have them yet (InboxMarks, #2201): a list the hub made before it took a
/// mark must not bring a swiped-away item back.
final class InboxMarksTests: XCTestCase {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func item(_ id: String, done: Bool = false) throws -> InboxItem {
        let json = """
        {"id":"\(id)","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"\(id)","preview":"","count":1\
        \(done ? ",\"done_at\":\"2026-11-09T08:00:00Z\",\"done_by\":\"user\"" : "")}
        """
        return try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    private func done(_ items: [InboxItem]) -> [String] { items.filter(\.isDone).map(\.id) }

    /// The report: A swiped to Done, then B; A's answer comes after B's swipe, and the reload its push brings has the
    /// list from before the hub had B. Before #2201 that list replaced everything and B came back until its own answer.
    func testAListFromBeforeTheSecondMarkKeepsItDone() throws {
        var marks = InboxMarks()
        let a = marks.put("a", .init(doneAt: now, doneBy: "user"))
        let b = marks.put("b", .init(doneAt: now, doneBy: "user"))
        marks.settle("a", token: a, taken: true)
        // the hub's push for A: a reload, answered with A done and B still open
        let reload = marks.beginList()
        let stale = [try item("a", done: true), try item("b"), try item("c")]
        XCTAssertEqual(done(marks.lay(stale, seq: reload)), ["a", "b"], "B came back while its request was out")
        marks.endList(reload)
        // B's answer, then the list its push brings: the hub has both, and nothing is left to lay
        marks.settle("b", token: b, taken: true)
        XCTAssertTrue(marks.isEmpty, "a mark taken with no list out that started before it stayed")
        let fresh = [try item("a", done: true), try item("b", done: true), try item("c")]
        XCTAssertEqual(done(marks.lay(fresh, seq: marks.beginList())), ["a", "b"])
    }

    /// A reload already out when the hub took the mark answers with the item still open: the mark stays over it, and
    /// goes once that reload is in.
    func testAListThatStartedBeforeTheHubTookTheMark() throws {
        var marks = InboxMarks()
        let early = marks.beginList()
        let a = marks.put("a", .init(doneAt: now, doneBy: "user"))
        marks.settle("a", token: a, taken: true)
        XCTAssertFalse(marks.isEmpty, "the mark went while a list from before it was still out")
        XCTAssertEqual(done(marks.lay([try item("a")], seq: early)), ["a"])
        marks.endList(early)
        XCTAssertTrue(marks.isEmpty)
        // a list that started after the hub took it is the hub's word: here someone moved it back meanwhile
        XCTAssertEqual(done(marks.lay([try item("a")], seq: marks.beginList())), [])
    }

    /// A list that started after the hub took the mark is laid with nothing, even while an older list is still out.
    func testListsAfterTheHubTookItAreTheHubs() throws {
        var marks = InboxMarks()
        let early = marks.beginList()
        let a = marks.put("a", .init(doneAt: now, doneBy: "user"))
        marks.settle("a", token: a, taken: true)
        let late = marks.beginList()
        XCTAssertEqual(done(marks.lay([try item("a")], seq: late)), [], "a list from after the hub took it was overridden")
        XCTAssertEqual(done(marks.lay([try item("a")], seq: early)), ["a"])
    }

    func testAFailedMarkGoes() throws {
        var marks = InboxMarks()
        let a = marks.put("a", .init(doneAt: now, doneBy: "user"))
        XCTAssertTrue(marks.out("a"))
        marks.settle("a", token: a, taken: false)
        XCTAssertFalse(marks.out("a"))
        XCTAssertTrue(marks.isEmpty)
        XCTAssertEqual(done(marks.lay([try item("a")], seq: marks.beginList())), [])
    }

    /// Done then Move Back before the first answer: the older request's answer settles nothing, the newer mark stays.
    func testANewerMarkOfTheSameItemWins() throws {
        var marks = InboxMarks()
        let first = marks.put("a", .init(doneAt: now, doneBy: "user"))
        _ = marks.put("a", .init(doneAt: nil, doneBy: nil))
        marks.settle("a", token: first, taken: false)
        XCTAssertTrue(marks.out("a"), "the older request's failure dropped the newer mark")
        XCTAssertEqual(done(marks.lay([try item("a", done: true)], seq: marks.beginList())), [], "the newer mark (back) was not laid")
    }

    /// A mute's mark sets its reason; the hub's answer sets the done fields the mark keeps laying.
    func testReasonAndTheHubsFields() throws {
        var marks = InboxMarks()
        let early = marks.beginList()
        let a = marks.put("a", .init(doneAt: now, doneBy: "read", reason: "Muted: Weekly"))
        var hub = try item("a", done: true)
        hub.doneBy = "read"
        marks.settle("a", token: a, taken: true, hub: hub)
        let laid = marks.lay([try item("a")], seq: early)
        XCTAssertEqual(laid.first?.reason, "Muted: Weekly")
        XCTAssertEqual(laid.first?.doneAt, hub.doneAt, "the hub's done time was not kept")
    }

    /// An answer about items (not a list, seq nil) lays only the marks still out.
    func testAnswersWithoutAListNumber() throws {
        var marks = InboxMarks()
        let early = marks.beginList()
        let a = marks.put("a", .init(doneAt: now, doneBy: "user"))
        _ = marks.put("b", .init(doneAt: now, doneBy: "user"))
        marks.settle("a", token: a, taken: true)
        XCTAssertEqual(done(marks.lay([try item("a"), try item("b")], seq: nil)), ["b"])
        marks.endList(early)
    }
}
