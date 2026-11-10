import XCTest
@testable import Persona

/// The inbox's groups (InboxGroups, #2059): the time boundaries (calendar days; "just now" only in Done; the last
/// week by day, then 7+ days and 1+ month), the source order (the chips'), the groups open at first. The web's
/// lib/inbox-groups.test.ts checks the same rules.
final class InboxGroupsTests: XCTestCase {
    private var calendar: Calendar = {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "Asia/Shanghai")!
        return c
    }()

    /// Mon 9 Nov 2026, 15:00 in the calendar's zone.
    private var now: Date { calendar.date(from: DateComponents(year: 2026, month: 11, day: 9, hour: 15))! }

    private func on(_ day: Int, _ hour: Int = 9, month: Int = 11) -> Date {
        calendar.date(from: DateComponents(year: 2026, month: month, day: day, hour: hour))!
    }

    private func item(_ id: String, source: String = "lark", account: String = "feishu", accountName: String = "Lark",
                      connector: Bool = false, doneAt: Date? = nil) throws -> InboxItem {
        let done = doneAt.map { ",\"done_at\":\"\(ISO8601DateFormatter().string(from: $0))\",\"done_by\":\"user\"" } ?? ""
        let conn = connector ? ",\"connector\":{\"thread\":\"t\"}" : ""
        let json = """
        {"id":"\(id)","source":"\(source)","kind":"dm","account":"\(account)","account_name":"\(accountName)","title":"\(id)","preview":"","count":1\(done)\(conn)}
        """
        return try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    func testCalendarDaysNotTwentyFourHours() {
        let key = { InboxGroups.timeKey($0, now: self.now, calendar: self.calendar) }
        XCTAssertEqual(key(on(9, 0)), "today")
        XCTAssertEqual(key(calendar.date(byAdding: .minute, value: -10, to: on(9, 0))!), "yesterday")
        XCTAssertEqual(key(on(7)), "d:2026-11-07")
        XCTAssertEqual(key(on(3)), "d:2026-11-03", "six days back is still its own day")
        XCTAssertEqual(key(on(2)), "week")
        XCTAssertEqual(key(on(11, month: 10)), "week", "29 days")
        XCTAssertEqual(key(on(10, month: 10)), "month", "30 days")
        XCTAssertEqual(key(nil), "month")
        XCTAssertEqual(key(now.addingTimeInterval(3600)), "today", "the future is today")
    }

    func testJustNowOnlyInDoneAndWithinTheHour() {
        let half = now.addingTimeInterval(-1800)
        XCTAssertEqual(InboxGroups.timeKey(half, now: now, calendar: calendar), "today")
        XCTAssertEqual(InboxGroups.timeKey(half, now: now, recent: true, calendar: calendar), "just")
        XCTAssertEqual(InboxGroups.timeKey(now.addingTimeInterval(-3900), now: now, recent: true, calendar: calendar), "today")
    }

    func testDoneNewestFirst() throws {
        let items = try [("m", on(1, month: 10)), ("d5", on(4)), ("j", now.addingTimeInterval(-600)), ("w", on(31, month: 10)),
                         ("y", on(8)), ("t", on(9, 11)), ("d2", on(7))].map { try item($0.0, doneAt: $0.1) }
        let groups = InboxGroups.done(items, now: now, calendar: calendar)
        XCTAssertEqual(groups.map(\.key), ["just", "today", "yesterday", "d:2026-11-07", "d:2026-11-04", "week", "month"])
        XCTAssertEqual(groups.map { $0.items.map(\.id) }, [["j"], ["t"], ["y"], ["d2"], ["d5"], ["w"], ["m"]])
    }

    func testBySourceInTheChipsOrder() throws {
        let items = try [
            item("g1", source: "github", account: "github", accountName: "GitHub", connector: true),
            item("m1", source: "mail", account: "acc1", accountName: "me@example.com"),
            item("l1"),
            item("x1", source: "moments", account: "x", accountName: "Moments"),
            item("l2"),
        ]
        let groups = InboxGroups.bySource(items, order: ["lark", "mail:acc1", "c:github"]) { $0 == "acc1" ? "me@example.com" : $0 }
        XCTAssertEqual(groups.map(\.key), ["lark", "mail:acc1", "c:github", "moments"])
        XCTAssertEqual(groups.map { $0.items.map(\.id) }, [["l1", "l2"], ["m1"], ["g1"], ["x1"]])
        XCTAssertEqual(groups[2].title, "GitHub")
        XCTAssertEqual(groups[3].title, "Moments")
    }

    /// A Done group's range holds exactly its own moments (#2969): its start and last moment are in it, its end is not.
    func testTimeRangeMatchesTheGroups() {
        let key = { InboxGroups.timeKey($0, now: self.now, calendar: self.calendar) }
        for k in ["yesterday", "d:2026-11-07", "d:2026-11-03", "week"] {
            let r = InboxGroups.timeRange(k, now: now, calendar: calendar)
            guard let start = r.start, let end = r.end else { return XCTFail("\(k) has no bounds") }
            XCTAssertEqual(key(start), k)
            XCTAssertEqual(key(end.addingTimeInterval(-0.001)), k)
            XCTAssertNotEqual(key(end), k)
        }
        XCTAssertEqual(InboxGroups.timeRange("today", now: now, calendar: calendar).start, on(9, 0))
        XCTAssertNil(InboxGroups.timeRange("today", now: now, calendar: calendar).end)
        let month = InboxGroups.timeRange("month", now: now, calendar: calendar)
        XCTAssertNil(month.start)
        XCTAssertEqual(month.end, on(11, 0, month: 10))
    }

    /// Each day's count goes to its group; Just Now (the list's) comes off Today; 11 Oct is 29 days back (7+ days),
    /// 10 Oct 30 (1+ month). The web's dayGroupCounts test has the same numbers.
    func testDayCountsByGroup() {
        let days = ["2026-11-09": 5, "2026-11-08": 3, "2026-11-04": 2, "2026-11-02": 7, "2026-10-12": 1, "2026-10-11": 1, "2026-10-10": 4, "2026-09-01": 6]
        XCTAssertEqual(InboxGroups.dayCounts(days, now: now, just: 2, calendar: calendar),
                       ["just": 2, "today": 3, "yesterday": 3, "d:2026-11-04": 2, "week": 9, "month": 10])
        XCTAssertEqual(InboxGroups.dayCounts(["2026-11-09": 2], now: now, just: 2, calendar: calendar), ["just": 2])
        XCTAssertEqual(InboxGroups.sortedKeys(["month", "d:2026-11-04", "just", "week", "yesterday", "today", "week"], now: now, calendar: calendar),
                       ["just", "today", "yesterday", "d:2026-11-04", "week", "month"])
    }

    func testOpenAtFirst() {
        XCTAssertTrue(InboxGroups.openAtFirst(section: "action", key: "lark"))
        XCTAssertTrue(InboxGroups.openAtFirst(section: "noise", key: "mail:acc1"))
        XCTAssertEqual(["just", "today", "yesterday", "week"].map { InboxGroups.openAtFirst(section: "done", key: $0) }, [true, true, false, false])
    }
}
