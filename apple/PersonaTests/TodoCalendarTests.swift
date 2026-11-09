import Foundation
import Testing
@testable import Persona

/// The to-dos' calendar (#2029): which to-dos it shows and on what day, the days of a day / week / month, side by side
/// where blocks overlap, what a month's day fits, where a tap on the axis lands, and the headings.
struct TodoCalendarTests {
    /// Monday-first, in a fixed time zone: Monday 2026-11-09 is "today" in these tests.
    private var cal: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "Asia/Shanghai")!
        c.firstWeekday = 2
        c.locale = Locale(identifier: "en_US")
        return c
    }

    private func at(_ day: Int, _ hour: Int, _ minute: Int = 0) -> Date {
        cal.date(from: DateComponents(year: 2026, month: 11, day: day, hour: hour, minute: minute))!
    }

    private func entry(_ id: String, _ start: Date, allDay: Bool = false, done: Bool = false) -> CalendarEntry {
        CalendarEntry(source: .todo(id), title: id, start: start, end: nil, allDay: allDay, state: .notStarted,
                      stateLabel: "", done: done, overdue: false, highPriority: false, reminding: false)
    }

    @MainActor @Test func toDosBecomeEntries() {
        let now = at(9, 14, 20)
        let timed = TodoItem(id: "timed", title: "Ship", dueAt: at(9, 18), remindAt: at(9, 17), priority: "high")
        let day = TodoItem(id: "day", title: "Rent", dueDate: "2026-11-11")
        let late = TodoItem(id: "late", title: "HEIC", dueAt: at(9, 11, 30))
        let done = TodoItem(id: "done", title: "PR", doneAt: at(9, 10), dueAt: at(9, 10), priority: "high")
        let pushed = TodoItem(id: "pushed", title: "Pushed", dueAt: at(10, 9), remindAt: at(9, 8), remindedAt: at(9, 8))
        let none = TodoItem(id: "none", title: "Someday")

        let e = TodoCalendar.entry(timed, state: .notStarted, calendar: cal, now: now)!
        #expect(!e.allDay && e.start == at(9, 18) && e.highPriority && e.reminding && !e.overdue && e.end == nil)
        let d = TodoCalendar.entry(day, state: .notStarted, calendar: cal, now: now)!
        #expect(d.allDay && d.start == at(11, 0))
        #expect(TodoCalendar.entry(late, state: .needsYou, calendar: cal, now: now)!.overdue)
        let gone = TodoCalendar.entry(done, state: .done, calendar: cal, now: now)!
        #expect(gone.done && !gone.overdue && !gone.highPriority)
        #expect(!TodoCalendar.entry(pushed, state: .notStarted, calendar: cal, now: now)!.reminding)
        #expect(TodoCalendar.entry(none, state: .notStarted, calendar: cal, now: now) == nil)

        // The source keeps what falls in the range, and leaves done ones out when asked.
        let week = TodoCalendar.range(.week, anchor: now, calendar: cal)
        let all = [timed, day, late, done, pushed, none]
        let shown = TodoEntrySource(todos: all, state: { _ in .notStarted }, showDone: true).entries(in: week, calendar: cal, now: now)
        #expect(Set(shown.map(\.id)) == ["todo:timed", "todo:day", "todo:late", "todo:done", "todo:pushed"])
        let open = TodoEntrySource(todos: all, state: { _ in .notStarted }, showDone: false).entries(in: week, calendar: cal, now: now)
        #expect(!open.contains { $0.id == "todo:done" })
        let nextWeek = TodoCalendar.range(.week, anchor: at(16, 9), calendar: cal)
        #expect(TodoEntrySource(todos: all, state: { _ in .notStarted }, showDone: true).entries(in: nextWeek, calendar: cal, now: now).isEmpty)

        // Those without a due date are counted under the calendar; done ones are not.
        let doneUndated = TodoItem(id: "x", title: "Old", doneAt: now)
        #expect(TodoCalendar.undatedCount(all + [doneUndated], calendar: cal) == 1)
    }

    @Test func daysOfEachMode() {
        let anchor = at(11, 15)
        #expect(TodoCalendar.days(.day, anchor: anchor, calendar: cal) == [at(11, 0)])
        let week = TodoCalendar.days(.week, anchor: anchor, calendar: cal)
        #expect(week.count == 7 && week.first == at(9, 0) && week.last == at(15, 0))
        // November 2026 starts on a Sunday: Monday-first, its weeks run from October 26 to December 6.
        let month = TodoCalendar.days(.month, anchor: anchor, calendar: cal)
        #expect(month.count == 42)
        #expect(month.first == cal.date(from: DateComponents(year: 2026, month: 10, day: 26)))
        #expect(month.last == cal.date(from: DateComponents(year: 2026, month: 12, day: 6)))
        // A Sunday-first region starts the same week a day earlier.
        var sunday = cal
        sunday.firstWeekday = 1
        #expect(TodoCalendar.days(.week, anchor: anchor, calendar: sunday).first == at(8, 0))
        // A month that fits in five weeks gets five.
        let february = cal.date(from: DateComponents(year: 2027, month: 2, day: 10))!
        #expect(TodoCalendar.days(.month, anchor: february, calendar: cal).count == 28)

        let range = TodoCalendar.range(.week, anchor: anchor, calendar: cal)
        #expect(range.start == at(9, 0) && range.end == at(16, 0))
        #expect(TodoCalendar.step(anchor, mode: .day, by: 1, calendar: cal) == at(12, 15))
        #expect(TodoCalendar.step(anchor, mode: .week, by: -1, calendar: cal) == at(4, 15))
        #expect(TodoCalendar.step(anchor, mode: .month, by: 1, calendar: cal) == cal.date(from: DateComponents(year: 2026, month: 12, day: 11, hour: 15)))
    }

    @Test func aDaysEntriesInDrawingOrder() {
        let entries = [entry("late", at(9, 18)), entry("rent", at(9, 0), allDay: true), entry("done", at(9, 10), done: true),
                       entry("open", at(9, 10)), entry("tomorrow", at(10, 9))]
        #expect(TodoCalendar.on(at(9, 12), entries, calendar: cal).map(\.title) == ["rent", "open", "done", "late"])
    }

    @Test func overlappingBlocksSideBySide() {
        // A block is 50 minutes: two at 18:00 and one at 18:30 share a cluster of three lanes; 20:00 is on its own.
        let a = entry("a", at(9, 18)), b = entry("b", at(9, 18)), c = entry("c", at(9, 18, 30)), d = entry("d", at(9, 20))
        let placed = TodoCalendar.lanes([d, c, b, a], minutes: 50)
        let byID = Dictionary(uniqueKeysWithValues: placed.map { ($0.entry.title, $0) })
        #expect(byID["a"]?.lane == 0 && byID["b"]?.lane == 1 && byID["c"]?.lane == 2)
        #expect(byID["a"]?.lanes == 3 && byID["c"]?.lanes == 3)
        #expect(byID["d"]?.lane == 0 && byID["d"]?.lanes == 1)
        // 18:50 starts where 18:00's block ends: a lane is free again.
        let e = entry("e", at(9, 18, 50))
        let again = Dictionary(uniqueKeysWithValues: TodoCalendar.lanes([a, e], minutes: 50).map { ($0.entry.title, $0) })
        #expect(again["e"]?.lane == 0 && again["e"]?.lanes == 1)

        #expect(TodoCalendar.clusters([a, b, c, d], minutes: 50).map { $0.map(\.title) } == [["a", "b", "c"], ["d"]])
        // An entry with an end (a calendar event, later) is as long as it lasts.
        var long = entry("long", at(9, 9))
        long.end = at(9, 12)
        #expect(TodoCalendar.clusters([long, entry("x", at(9, 11))], minutes: 50).count == 1)
    }

    @Test func aMonthDayFits() {
        let five = (0..<5).map { entry("t\($0)", at(20, 9 + $0)) }
        let fit = TodoCalendar.fit(five, lines: 3)
        #expect(fit.shown.count == 2 && fit.more == 3)
        #expect(TodoCalendar.fit(Array(five.prefix(3)), lines: 3).more == 0)
        #expect(TodoCalendar.fit([], lines: 3).shown.isEmpty)
    }

    @Test func aTapOnTheAxis() {
        #expect(TodoCalendar.slot(day: at(9, 0), minutes: 15 * 60 + 44, snap: 30, calendar: cal) == at(9, 15, 30))
        #expect(TodoCalendar.slot(day: at(9, 0), minutes: -5, snap: 30, calendar: cal) == at(9, 0))
        #expect(TodoCalendar.slot(day: at(9, 0), minutes: 24 * 60 + 10, snap: 30, calendar: cal) == at(9, 23, 30))
        #expect(TodoCalendar.minutes(at(9, 14, 20), in: at(9, 0), calendar: cal) == 14 * 60 + 20)

        // The axis opens where the moment and the first to-do are in sight, else the morning.
        #expect(TodoCalendar.firstHour(days: [at(9, 0)], timed: [], now: at(9, 14, 20), calendar: cal) == 12)
        #expect(TodoCalendar.firstHour(days: [at(9, 0)], timed: [], now: at(9, 1), calendar: cal) == 0)
        #expect(TodoCalendar.firstHour(days: [at(9, 0)], timed: [entry("x", at(9, 10))], now: at(9, 14, 20), calendar: cal) == 9)
        #expect(TodoCalendar.firstHour(days: [at(10, 0)], timed: [entry("x", at(10, 9, 30))], now: at(9, 14), calendar: cal) == 8)
        let week = TodoCalendar.days(.week, anchor: at(11, 0), calendar: cal)
        #expect(TodoCalendar.firstHour(days: week, timed: [entry("x", at(12, 7)), entry("y", at(20, 3))], now: at(1, 9), calendar: cal) == 6)
        #expect(TodoCalendar.firstHour(days: [at(11, 0)], timed: [], now: at(9, 14), calendar: cal) == TodoCalendar.morning)
    }

    @Test func headings() {
        let en = Locale(identifier: "en_US")
        #expect(TodoCalendar.title(.month, anchor: at(9, 12), calendar: cal, locale: en) == "November 2026")
        let day = TodoCalendar.title(.day, anchor: at(9, 12), calendar: cal, locale: en)
        #expect(day.contains("Mon") && day.contains("Nov") && day.contains("9"))
        let week = TodoCalendar.title(.week, anchor: at(11, 12), calendar: cal, locale: en)
        #expect(week.contains("Nov") && week.contains("9") && week.contains("15"))
        #expect(TodoCalendar.title(.month, anchor: at(9, 12), calendar: cal, locale: Locale(identifier: "zh-Hans")).contains("11"))

        #expect(TodoCalendar.weekdaySymbols(calendar: cal, narrow: false).first == "Mon")
        var sunday = cal
        sunday.firstWeekday = 1
        #expect(TodoCalendar.weekdaySymbols(calendar: sunday, narrow: false).first == "Sun")
        #expect(TodoCalendar.weekdaySymbols(calendar: cal, narrow: true).count == 7)
    }
}
