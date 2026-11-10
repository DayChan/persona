// Groups inside the inbox's sections (docs/assistant/inbox.md "Grouping and selection", #2059): Needs you, FYI and Collapsed by source
// or by time (the switch at the top, kept on this device), Done always by time. Pure functions: tests give `now`.
import { formatDate, t } from "../i18n";
import type { InboxItem } from "./inbox";

export type Grouping = "source" | "time";

/** A group of a section: its key (stable for the folds kept on this device), title and items, in the section's order. */
export type InboxGroup = { key: string; title: string; items: InboxItem[] };

const DAY = 86_400_000;

/** Local midnight of the day `t` falls on. */
function dayStart(t: number): number {
  const d = new Date(t);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
}

/** Whole local days from `t`'s day to today's (0: today). Calendar days, not 24-hour windows. */
function daysAgo(t: number, now: number): number {
  return Math.round((dayStart(now) - dayStart(t)) / DAY);
}

/**
 * The time group of a moment: "just" (within the hour; Done only, so a slip is easy to find and undo), "today",
 * "yesterday", "d:YYYY-MM-DD" for each of the 2 to 6 days before (the last week, not the calendar week: a Monday still
 * shows last week's days), "week" (7 to 29 days ago) and "month" (30 days or more). A moment in the future is today.
 */
export function timeKey(at: string | undefined, now: number, recent = false): string {
  const t = at ? Date.parse(at) : NaN;
  if (Number.isNaN(t)) return "month";
  if (recent && now - t <= 3_600_000) return "just";
  const days = Math.max(0, daysAgo(t, now));
  if (days === 0) return "today";
  if (days === 1) return "yesterday";
  if (days <= 6) return "d:" + new Date(t).toLocaleDateString("sv"); // YYYY-MM-DD, local
  return days <= 29 ? "week" : "month";
}

/** A time group's title: "Just now", "Today", "Yesterday", "Fri, Nov 6", "7+ days ago", "1+ month ago". */
export function timeTitle(key: string): string {
  if (key.startsWith("d:")) return formatDate(new Date(keyDay(key)), { weekday: "short", month: "long", day: "numeric" });
  return t(`lib-inbox.group_${key}`);
}

/** A day key's date at local midnight ("d:2026-11-06"; Date.parse of a bare date would be UTC midnight). */
function keyDay(key: string): number {
  const [y, m, d] = key.slice(2).split("-").map(Number);
  return new Date(y, m - 1, d).getTime();
}

/** Where a time group goes: newest first (just now, today, yesterday, the days before, 7+ days, 1+ month). */
function timeRank(key: string, now: number): number {
  const fixed: Record<string, number> = { just: 0, today: 1, yesterday: 2, week: 100, month: 101 };
  return key in fixed ? fixed[key] : 2 + daysAgo(keyDay(key), now);
}

/** The items by time group, newest group first; `at` is the moment that counts (the last message, or when it was done). */
export function groupByTime(items: InboxItem[], now: number, at: (it: InboxItem) => string | undefined, recent = false): InboxGroup[] {
  const groups = new Map<string, InboxItem[]>();
  for (const it of items) {
    const k = timeKey(at(it), now, recent);
    groups.set(k, [...(groups.get(k) ?? []), it]);
  }
  return [...groups].sort((a, b) => timeRank(a[0], now) - timeRank(b[0], now)).map(([key, list]) => ({ key, title: timeTitle(key), items: list }));
}

/** An item's source group: Lark, each mail account, each connector or extension source (by its account), else its source. */
export function sourceKey(it: InboxItem): string {
  if (it.connector) return `c:${it.account}`;
  if (it.source === "mail") return `mail:${it.account}`;
  return it.source;
}

/**
 * The items by source, in the filter chips' order (`order`: the source keys as the chips list them, Lark, the mail
 * accounts, the connectors); a source the chips don't name comes after them, in the order it first shows up.
 */
export function groupBySource(items: InboxItem[], order: string[], mailName: (account: string) => string): InboxGroup[] {
  const groups = new Map<string, InboxItem[]>();
  for (const it of items) {
    const k = sourceKey(it);
    groups.set(k, [...(groups.get(k) ?? []), it]);
  }
  const rank = (k: string) => (order.includes(k) ? order.indexOf(k) : order.length);
  return [...groups].sort((a, b) => rank(a[0]) - rank(b[0])).map(([key, list]) => ({
    key,
    title: key === "lark" ? t("lib-inbox.group_lark")
      : key.startsWith("mail:") ? t("lib-inbox.group_mail", { account: mailName(key.slice(5)) })
      : key.startsWith("c:") ? list[0].account_name || list[0].source
      : list[0].account_name || key,
    items: list,
  }));
}

/** The groups of one section: by source or by time (the last message's), or none when there would be only one. */
export function sectionGroups(items: InboxItem[], grouping: Grouping, now: number, order: string[], mailName: (account: string) => string): InboxGroup[] {
  const groups = grouping === "time" ? groupByTime(items, now, (it) => it.updated_at) : groupBySource(items, order, mailName);
  return groups.length > 1 ? groups : [];
}

/** Done's groups: always by when they were done, "Just now" first. */
export function doneGroups(items: InboxItem[], now: number): InboxGroup[] {
  return groupByTime(items, now, (it) => it.done_at ?? undefined, true);
}

/** Groups open at first: every group of Needs you, FYI and Collapsed; in Done only "Just now" and "Today". */
export function openAtFirst(section: string, key: string): boolean {
  return section !== "done" || key === "just" || key === "today";
}

/** Local midnight `n` days after the day `t` falls on (negative: before), by the calendar, so across daylight saving. */
function shiftDays(t: number, n: number): number {
  const d = new Date(t);
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n).getTime();
}

/**
 * Where a Done group starts and ends (end not included), so it can load its own items (#2969): Just now and Today run
 * on past now, 1+ month has no start. The bounds are local midnights, as timeKey's days are.
 */
export function timeRange(key: string, now: number): { start: number; end: number } {
  const today = dayStart(now);
  switch (key) {
    case "just": return { start: now - 3_600_000, end: Infinity };
    case "today": return { start: today, end: Infinity };
    case "yesterday": return { start: shiftDays(today, -1), end: today };
    case "week": return { start: shiftDays(today, -29), end: shiftDays(today, -6) };
    case "month": return { start: -Infinity, end: shiftDays(today, -29) };
  }
  const start = keyDay(key);
  return { start, end: shiftDays(start, 1) };
}

/**
 * Done's group counts from the hub's per-day counts (GET /api/v1/inbox/done/days, #2967; days as "YYYY-MM-DD" in this
 * device's zone): each day's count goes to the group its day falls in. Just now is part of Today and all in the list:
 * `just` (how many it holds) comes off Today.
 */
export function dayGroupCounts(days: Record<string, number>, now: number, just = 0): Map<string, number> {
  const out = new Map<string, number>();
  for (const [day, n] of Object.entries(days)) {
    // the day's noon: its key whatever the day's length (a daylight-saving day has 23 or 25 hours)
    const k = timeKey(new Date(keyDay(`d:${day}`) + 12 * 3_600_000).toISOString(), now);
    out.set(k, (out.get(k) ?? 0) + n);
  }
  if (just > 0) {
    out.set("just", just);
    const today = (out.get("today") ?? 0) - just;
    if (today > 0) out.set("today", today);
    else out.delete("today");
  }
  return out;
}

/** The time groups' keys in Done's order (newest first), for groups that come from counts as well as from items. */
export function sortTimeKeys(keys: Iterable<string>, now: number): string[] {
  return [...new Set(keys)].sort((a, b) => timeRank(a, now) - timeRank(b, now));
}
