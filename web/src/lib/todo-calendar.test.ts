import { describe, expect, it } from "vitest";
import type { Todo } from "./todos";
import {
  addDays,
  calendarDays,
  calendarEntries,
  calendarRange,
  calendarTitle,
  clusters,
  dayKey,
  entriesOn,
  firstHour,
  fitDay,
  lanes,
  MORNING,
  minutesInto,
  parseDayKey,
  slotAt,
  startOfWeek,
  stepAnchor,
  todoEntry,
  undatedCount,
  weekdayNames,
  weekStartDay,
  type CalendarEntry,
} from "./todo-calendar";

// Local times: these run in whatever zone the machine has, as the page does. Monday 2026-11-09 is "today" here.
const at = (day: number, hour: number, minute = 0) => new Date(2026, 10, day, hour, minute).getTime();
const iso = (ms: number) => new Date(ms).toISOString();
const todo = (id: string, extra: Partial<Todo> = {}): Todo => ({ id, title: id, agents: [], ...extra }) as Todo;
const entry = (id: string, start: number, extra: Partial<CalendarEntry> = {}): CalendarEntry => ({
  id: `todo:${id}`, todoId: id, title: id, start, allDay: false, status: "not_started", statusLabel: "",
  done: false, overdue: false, high: false, reminding: false, ...extra,
});
const MON = 1, SUN = 0;

describe("to-dos on the calendar (#2029)", () => {
  const now = at(9, 14, 20);

  it("a to-do becomes an entry at its due moment or on its due day", () => {
    const timed = todoEntry(todo("timed", { due_at: iso(at(9, 18)), remind_at: iso(at(9, 17)), priority: "high" }), undefined, now)!;
    expect(timed).toMatchObject({ allDay: false, start: at(9, 18), high: true, reminding: true, overdue: false });
    const day = todoEntry(todo("day", { due_date: "2026-11-11" }), undefined, now)!;
    expect(day).toMatchObject({ allDay: true, start: at(11, 0) });
    expect(todoEntry(todo("late", { due_at: iso(at(9, 11, 30)) }), undefined, now)!.overdue).toBe(true);
    const done = todoEntry(todo("done", { done_at: iso(at(9, 10)), due_at: iso(at(9, 10)), priority: "high" }), undefined, now)!;
    expect(done).toMatchObject({ done: true, overdue: false, high: false, status: "done" });
    expect(todoEntry(todo("pushed", { due_at: iso(at(10, 9)), remind_at: iso(at(9, 8)), reminded_at: iso(at(9, 8)) }), undefined, now)!.reminding).toBe(false);
    expect(todoEntry(todo("none"), undefined, now)).toBeUndefined();
  });

  it("the entries of a range, done ones when shown, and those without a due date counted", () => {
    const all = [
      todo("a", { due_at: iso(at(9, 18)) }),
      todo("b", { due_date: "2026-11-11" }),
      todo("c", { due_at: iso(at(16, 9)) }),
      todo("d", { done_at: iso(at(9, 9)), due_at: iso(at(9, 9)) }),
      todo("e"),
      todo("f", { done_at: iso(at(9, 9)) }),
    ];
    const [from, to] = calendarRange("week", at(11, 12), MON);
    expect(calendarEntries(all, { showDone: true, from, to, now }).map((e) => e.todoId).sort()).toEqual(["a", "b", "d"]);
    expect(calendarEntries(all, { showDone: false, from, to, now }).map((e) => e.todoId).sort()).toEqual(["a", "b"]);
    expect(undatedCount(all)).toBe(1);
  });

  it("the days of a day, a week and a month", () => {
    expect(calendarDays("day", at(11, 15), MON)).toEqual([at(11, 0)]);
    const week = calendarDays("week", at(11, 15), MON);
    expect(week).toHaveLength(7);
    expect([week[0], week[6]]).toEqual([at(9, 0), at(15, 0)]);
    expect(startOfWeek(at(11, 15), SUN)).toBe(at(8, 0));
    // November 2026 starts on a Sunday: Monday-first, its weeks run from October 26 to December 6.
    const month = calendarDays("month", at(11, 15), MON);
    expect(month).toHaveLength(42);
    expect([month[0], month[41]]).toEqual([new Date(2026, 9, 26).getTime(), new Date(2026, 11, 6).getTime()]);
    expect(calendarDays("month", new Date(2027, 1, 10).getTime(), MON)).toHaveLength(28);
    expect(calendarRange("week", at(11, 15), MON)).toEqual([at(9, 0), at(16, 0)]);
    expect(addDays(at(9, 0), 7)).toBe(at(16, 0));
  });

  it("a page later or earlier", () => {
    expect(stepAnchor(at(11, 15), "day", 1)).toBe(at(12, 15));
    expect(stepAnchor(at(11, 15), "week", -1)).toBe(at(4, 15));
    expect(stepAnchor(at(11, 15), "month", 1)).toBe(new Date(2026, 11, 11, 15).getTime());
    expect(stepAnchor(new Date(2027, 0, 31).getTime(), "month", 1)).toBe(new Date(2027, 1, 28).getTime());
  });

  it("a day's entries in drawing order", () => {
    const list = [entry("late", at(9, 18)), entry("rent", at(9, 0), { allDay: true }), entry("done", at(9, 10), { done: true }),
      entry("open", at(9, 10)), entry("tomorrow", at(10, 9))];
    expect(entriesOn(at(9, 12), list).map((e) => e.title)).toEqual(["rent", "open", "done", "late"]);
  });

  it("overlapping blocks side by side", () => {
    const a = entry("a", at(9, 18)), b = entry("b", at(9, 18)), c = entry("c", at(9, 18, 30)), d = entry("d", at(9, 20));
    const byId = Object.fromEntries(lanes([d, c, b, a], 50).map((p) => [p.entry.title, p]));
    expect([byId.a.lane, byId.b.lane, byId.c.lane, byId.a.lanes]).toEqual([0, 1, 2, 3]);
    expect([byId.d.lane, byId.d.lanes]).toEqual([0, 1]);
    const again = Object.fromEntries(lanes([a, entry("e", at(9, 18, 50))], 50).map((p) => [p.entry.title, p]));
    expect([again.e.lane, again.e.lanes]).toEqual([0, 1]);
    expect(clusters([a, b, c, d], 50).map((g) => g.map((e) => e.title))).toEqual([["a", "b", "c"], ["d"]]);
    // an entry with an end (a calendar event, later) is as long as it lasts
    expect(clusters([entry("long", at(9, 9), { end: at(9, 12) }), entry("x", at(9, 11))], 50)).toHaveLength(1);
  });

  it("what a day of the month fits", () => {
    expect(fitDay([1, 2, 3, 4, 5], 3)).toEqual({ shown: [1, 2], more: 3 });
    expect(fitDay([1, 2, 3], 3)).toEqual({ shown: [1, 2, 3], more: 0 });
  });

  it("where a click on the axis lands, and where the axis opens", () => {
    expect(slotAt(at(9, 0), 15 * 60 + 44)).toBe(at(9, 15, 30));
    expect(slotAt(at(9, 0), -5)).toBe(at(9, 0));
    expect(slotAt(at(9, 0), 24 * 60 + 10)).toBe(at(9, 23, 30));
    expect(minutesInto(at(9, 14, 20), at(9, 0))).toBe(14 * 60 + 20);
    expect(firstHour([at(9, 0)], [], now)).toBe(12);
    expect(firstHour([at(9, 0)], [], at(9, 1))).toBe(0);
    // today's 10:00 to-do comes into sight before the moment's hour and a half
    expect(firstHour([at(9, 0)], [entry("x", at(9, 10))], now)).toBe(9);
    expect(firstHour([at(10, 0)], [entry("x", at(10, 9, 30))], now)).toBe(8);
    // a week: its first timed to-do on any of its days; one on a day not shown does not count
    expect(firstHour(calendarDays("week", at(11, 0), MON), [entry("x", at(12, 7)), entry("y", at(20, 3))], at(1, 9))).toBe(6);
    expect(firstHour([at(11, 0)], [], now)).toBe(MORNING);
  });

  it("headings, weekday names and addresses", () => {
    expect(calendarTitle("month", at(9, 12), MON)).toMatch(/2026/);
    expect(calendarTitle("week", at(11, 12), MON)).toMatch(/9/);
    expect(weekdayNames(MON, false)).toHaveLength(7);
    expect(weekdayNames(MON, false)[0]).not.toBe(weekdayNames(SUN, false)[0]);
    expect(weekStartDay("en-US")).toBe(SUN);
    expect(weekStartDay("de-DE")).toBe(MON);
    expect(dayKey(at(9, 23, 59))).toBe("2026-11-09");
    expect(parseDayKey("2026-11-09")).toBe(at(9, 0));
    expect(parseDayKey("nope")).toBeUndefined();
  });
});
