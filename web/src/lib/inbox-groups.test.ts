// @vitest-environment jsdom
// The inbox's groups (#2059): the time boundaries (local calendar days; just now only in Done; the last week by day,
// then 7+ days and 1+ month), the source order (the chips'), one group means none.
import { describe, expect, it } from "vitest";
import type { InboxItem } from "./inbox";
import { dayGroupCounts, doneGroups, groupBySource, openAtFirst, sectionGroups, sortTimeKeys, sourceKey, timeKey, timeRange } from "./inbox-groups";

// Mon 9 Nov 2026, 15:00 local
const now = new Date(2026, 10, 9, 15, 0).getTime();
const at = (days: number, hours = 0, minutes = 0) => new Date(2026, 10, 9 - days, 15 - hours, -minutes).toISOString();
const item = (id: string, patch: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "lark", kind: "dm", account: "feishu", account_name: "Lark", title: id, preview: "", count: 1, updated_at: at(0), ...patch,
});

describe("timeKey", () => {
  it("counts calendar days, not 24 hours", () => {
    expect(timeKey(new Date(2026, 10, 9, 0, 5).toISOString(), now)).toBe("today");
    expect(timeKey(new Date(2026, 10, 8, 23, 55).toISOString(), now)).toBe("yesterday");
    expect(timeKey(new Date(2026, 10, 7, 9, 0).toISOString(), now)).toBe("d:2026-11-07");
    expect(timeKey(new Date(2026, 10, 3, 9, 0).toISOString(), now)).toBe("d:2026-11-03"); // 6 days: still its own day
    expect(timeKey(new Date(2026, 10, 2, 9, 0).toISOString(), now)).toBe("week"); // 7 days
    expect(timeKey(new Date(2026, 9, 11, 9, 0).toISOString(), now)).toBe("week"); // 29 days
    expect(timeKey(new Date(2026, 9, 10, 9, 0).toISOString(), now)).toBe("month"); // 30 days
  });

  it("just now only where asked (Done), and within the hour", () => {
    expect(timeKey(at(0, 0, 30), now)).toBe("today");
    expect(timeKey(at(0, 0, 30), now, true)).toBe("just");
    expect(timeKey(at(0, 1, 5), now, true)).toBe("today");
  });

  it("a time in the future is today; none or a bad one goes last", () => {
    expect(timeKey(new Date(now + 3_600_000).toISOString(), now)).toBe("today");
    expect(timeKey(undefined, now)).toBe("month");
    expect(timeKey("nope", now)).toBe("month");
  });
});

describe("groups", () => {
  it("Done newest first: just now, today, yesterday, each day, 7+ days, 1+ month", () => {
    const done = (id: string, iso: string) => item(id, { done_at: iso, done_by: "user" });
    const groups = doneGroups([done("m", at(40)), done("d5", at(5)), done("j", at(0, 0, 10)), done("w", at(9)), done("y", at(1)), done("t", at(0, 3)), done("d2", at(2))], now);
    expect(groups.map((g) => g.key)).toEqual(["just", "today", "yesterday", "d:2026-11-07", "d:2026-11-04", "week", "month"]);
    expect(groups.map((g) => g.items.map((it) => it.id))).toEqual([["j"], ["t"], ["y"], ["d2"], ["d5"], ["w"], ["m"]]);
    expect(groups[0].title).toBe("刚刚处理");
    expect(groups[5].title).toBe("7 天前");
  });

  it("by source in the chips' order, a source they don't name after them, items in their order", () => {
    const list = [
      item("g1", { source: "github", account: "github", account_name: "GitHub", connector: { thread: "t" } as InboxItem["connector"] }),
      item("m1", { source: "mail", account: "acc1" }),
      item("l1"),
      item("x1", { source: "moments", account: "x", account_name: "Moments" }),
      item("l2"),
    ];
    const groups = groupBySource(list, ["lark", "mail:acc1", "c:github"], (a) => (a === "acc1" ? "me@example.com" : a));
    expect(groups.map((g) => [g.key, g.title, g.items.map((it) => it.id)])).toEqual([
      ["lark", "飞书", ["l1", "l2"]],
      ["mail:acc1", "邮件 · me@example.com", ["m1"]],
      ["c:github", "GitHub", ["g1"]],
      ["moments", "Moments", ["x1"]],
    ]);
    expect(sourceKey(list[0])).toBe("c:github");
  });

  it("one group is no group: the items show without a title", () => {
    expect(sectionGroups([item("l1"), item("l2")], "source", now, ["lark"], (a) => a)).toEqual([]);
    expect(sectionGroups([item("a", { updated_at: at(0) }), item("b", { updated_at: at(1) })], "time", now, [], (a) => a).map((g) => g.key)).toEqual(["today", "yesterday"]);
  });

  it("open at first: every group, except Done's older than today", () => {
    expect(openAtFirst("action", "lark")).toBe(true);
    expect(openAtFirst("noise", "mail:acc1")).toBe(true);
    expect(["just", "today", "yesterday", "week"].map((k) => openAtFirst("done", k))).toEqual([true, true, false, false]);
  });
});

describe("Done's groups from the hub's day counts (#2969)", () => {
  it("a group's range holds exactly its own moments: its start and its last moment are in it, its end is not", () => {
    for (const key of ["yesterday", "d:2026-11-07", "d:2026-11-03", "week"]) {
      const { start, end } = timeRange(key, now);
      expect(timeKey(new Date(start).toISOString(), now)).toBe(key);
      expect(timeKey(new Date(end - 1).toISOString(), now)).toBe(key);
      expect(timeKey(new Date(end).toISOString(), now)).not.toBe(key);
    }
    expect(timeRange("today", now)).toEqual({ start: new Date(2026, 10, 9).getTime(), end: Infinity });
    expect(timeRange("month", now)).toEqual({ start: -Infinity, end: new Date(2026, 9, 11).getTime() });
    expect(timeKey(new Date(timeRange("month", now).end - 1).toISOString(), now)).toBe("month");
  });

  it("each day's count goes to its group; Just now (the list's) comes off Today", () => {
    // 2 Nov is 7 days back (7+ days), 11 Oct 29 (still 7+ days), 10 Oct 30 (1+ month)
    const counts = dayGroupCounts({ "2026-11-09": 5, "2026-11-08": 3, "2026-11-04": 2, "2026-11-02": 7, "2026-10-12": 1, "2026-10-11": 1, "2026-10-10": 4, "2026-09-01": 6 }, now, 2);
    expect(Object.fromEntries(counts)).toEqual({ just: 2, today: 3, yesterday: 3, "d:2026-11-04": 2, week: 9, month: 10 });
    expect(Object.fromEntries(dayGroupCounts({ "2026-11-09": 2 }, now, 2))).toEqual({ just: 2 }); // all of today is just now
    expect(sortTimeKeys(["month", "d:2026-11-04", "just", "week", "yesterday", "today", "week"], now)).toEqual(["just", "today", "yesterday", "d:2026-11-04", "week", "month"]);
  });
});
