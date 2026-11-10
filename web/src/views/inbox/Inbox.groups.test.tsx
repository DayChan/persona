// @vitest-environment jsdom
// The inbox's groups (#2059): by source at first (the chips' order), by time on the switch (kept on this device); each
// group folds to its title and count; selecting, a group's tick picks all of it, folded or not, "Select all" picks only
// what is in sight, and the bar says how many picked are folded away. Done is by when it was done, with the hub's
// history after the list's few days.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const calls = vi.hoisted(() => ({ list: [] as { path: string; json?: unknown }[], items: [] as unknown[], history: [] as unknown[], historyStatus: 200, days: false }));
vi.mock("../../lib/api", async (orig) => {
  const m = await orig<typeof import("../../lib/api")>();
  return {
    ...m,
    api: (path: string, init?: { method?: string; json?: { items?: { id: string }[] } }) => {
      calls.list.push({ path, json: init?.json });
      if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.items });
      // the day counts (#2967): off, a hub from before them (404); on, counted from the list's and the history's done items
      if (path.startsWith("/inbox/done/days?")) {
        if (!calls.days) return Promise.reject(new m.ApiError(404, "not found", { error: "not found" }));
        const counts: Record<string, number> = {};
        for (const it of [...calls.items, ...calls.history] as { done_at?: string }[]) {
          if (it.done_at) counts[new Date(it.done_at).toLocaleDateString("sv")] = (counts[new Date(it.done_at).toLocaleDateString("sv")] ?? 0) + 1;
        }
        return Promise.resolve({ tz: "x", days: Object.entries(counts).sort((a, b) => b[0].localeCompare(a[0])).map(([day, count]) => ({ day, count })) });
      }
      // the history's pages as the hub gives them: newest first, before (and before_id) where the page starts, limit
      if (path.startsWith("/inbox/done?")) {
        if (calls.historyStatus !== 200) return Promise.reject(new m.ApiError(calls.historyStatus, "not found", { error: "not found" }));
        const q = new URLSearchParams(path.slice(path.indexOf("?") + 1));
        const at = (x: { done_at?: string }) => Date.parse(x.done_at ?? "");
        let done = [...calls.history as { id: string; done_at?: string }[]].sort((a, b) => at(b) - at(a) || b.id.localeCompare(a.id));
        const before = q.get("before");
        if (before) done = done.filter((x) => at(x) < Date.parse(before) || (at(x) === Date.parse(before) && x.id < (q.get("before_id") ?? "")));
        const page = done.slice(0, Number(q.get("limit") ?? 50));
        const last = page.at(-1);
        return Promise.resolve({ items: page, next: page.length === Number(q.get("limit") ?? 50) && last ? { before: last.done_at, before_id: last.id } : undefined });
      }
      if (path === "/mail/accounts") return Promise.resolve({ accounts: [{ id: "acc1", email: "me@example.com" }] });
      if (path === "/inbox/bulk") {
        const ids = new Set((init?.json?.items ?? []).map((x) => x.id));
        return Promise.resolve({ changed: (calls.items as { id: string }[]).filter((x) => ids.has(x.id)).map((x) => ({ ...x, done_at: new Date().toISOString(), done_by: "user" })), unchanged: [], skipped: [] });
      }
      return Promise.resolve({});
    },
  };
});
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});

import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { getState } from "../../lib/store";
import { resetMailForTests } from "../../lib/mail";
import { Inbox } from "./Inbox";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ago = (days: number, hours = 1) => new Date(Date.now() - days * 86_400_000 - hours * 3_600_000).toISOString();
const lark = (id: string, patch: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "lark", kind: "dm", account: "feishu", account_name: "Lark", title: `chat ${id}`, preview: "", count: 1, updated_at: ago(0), tier: "action", version: `v-${id}`, ...patch,
});
const mail = (id: string, patch: Partial<InboxItem> = {}) => lark(id, { source: "mail", kind: "mail", account: "acc1", account_name: "me@example.com", ...patch });

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};
const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === text);
const groups = (section: string) => [...host.querySelectorAll<HTMLElement>(`[data-section="${section}"] [data-group]`)].map((g) => g.dataset.group!.split("|")[2]);
const head = (section: string, key: string) => host.querySelector<HTMLElement>(`[data-group="|${section}|${key}"]`)!;
const toggle = (section: string, key: string) => head(section, key).querySelector<HTMLButtonElement>("button[aria-expanded]")!;
const tick = (section: string, key: string) => head(section, key).querySelector<HTMLButtonElement>('button[role="checkbox"]');
// the picked rows in sight (a folded group keeps its rows, inert)
const rowsInSight = () => [...host.querySelectorAll<HTMLElement>("[data-item][data-picked]")].filter((el) => !el.closest("[inert]")).map((el) => el.dataset.item);
const click = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};

beforeEach(async () => {
  // a fixed afternoon: the groups are calendar days, so "an hour ago" must not cross midnight
  vi.useFakeTimers({ now: new Date(2026, 10, 9, 15, 0), toFake: ["Date"] });
  calls.list = [];
  calls.historyStatus = 200;
  calls.days = false;
  // Needs you: two Lark today, one mail yesterday, one mail 3 days ago; FYI: one Lark; Done: one today (the list's)
  calls.items = [lark("l1"), lark("l2"), mail("m1", { updated_at: ago(1) }), mail("m2", { updated_at: ago(3) }), lark("f1", { tier: "fyi" }),
    lark("d1", { done_at: ago(0, 0.2), done_by: "user" })];
  // the hub's history: 10 and 40 days ago
  calls.history = [lark("h10", { done_at: ago(10), done_by: "user" }), mail("h40", { done_at: ago(40), done_by: "user" })];
  localStorage.clear();
  resetInboxForTests();
  resetMailForTests();
  vi.stubGlobal("CSS", { escape: (v: string) => v });
  Element.prototype.scrollIntoView = vi.fn();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Inbox onToggleSidebar={() => {}} />));
  await flush();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("inbox groups (#2059)", () => {
  it("by source: in the chips' order, titles with counts; FYI with one group draws no title", async () => {
    expect(groups("action")).toEqual(["lark", "mail:acc1"]);
    expect(head("action", "lark").textContent).toContain("飞书2");
    expect(head("action", "mail:acc1").textContent).toContain("邮件 · me@example.com2");
    expect(groups("fyi")).toEqual([]);
  });

  it("by time: today, yesterday, each date; the switch is kept on this device", async () => {
    await click([...host.querySelectorAll('[role="radio"]')].find((b) => b.textContent === "按时间"));
    expect(groups("action").slice(0, 2)).toEqual(["today", "yesterday"]);
    expect(groups("action")[2]).toMatch(/^d:\d{4}-\d{2}-\d{2}$/);
    expect(localStorage.getItem("inbox.grouping")).toBe("time");
  });

  it("a group folds to its title and count, and its fold is kept on this device", async () => {
    await click(toggle("action", "mail:acc1"));
    expect(toggle("action", "mail:acc1").getAttribute("aria-expanded")).toBe("false");
    expect(head("action", "mail:acc1").querySelector("[inert]")).not.toBeNull();
    expect(JSON.parse(localStorage.getItem("inbox.folds")!)).toEqual({ "|action|mail:acc1": false });
  });

  it("Done by when it was done, continued from the hub's history: just now, 7+ days, 1+ month", async () => {
    expect(calls.list.some((c) => c.path.startsWith("/inbox/done?") && c.path.includes("sources=all"))).toBe(true);
    expect(groups("done")).toEqual(["just", "week", "month"]);
    expect(head("done", "month").textContent).toContain("1 个月前1");
  });

  it("an old hub without history (404): Done has only the list's items", async () => {
    act(() => root.unmount());
    calls.historyStatus = 404;
    resetInboxForTests();
    root = createRoot(host);
    act(() => root.render(<Inbox onToggleSidebar={() => {}} />));
    await flush();
    expect(groups("done")).toEqual([]); // one group: no titles
    expect(host.querySelector('[data-section="done"]')?.textContent).not.toContain("+");
  });

  it("Agents waiting on you are their own group first in Needs you, counting the Agents", async () => {
    act(() => root.unmount());
    Object.assign(getState(), { interactions: [{ id: "ix1", machine_id: "m", session_id: "s1", session_title: "改登录页", kind: "question" }] });
    resetInboxForTests();
    root = createRoot(host);
    act(() => root.render(<Inbox onToggleSidebar={() => {}} />));
    await flush();
    expect(groups("action")[0]).toBe("agents");
    expect(head("action", "agents").textContent).toContain("Agent 在等你1");
    Object.assign(getState(), { interactions: [] });
  });

  it("selecting: a group's tick picks all of it, folded or not; Select All picks what is in sight; the bar counts the folded ones", async () => {
    await click(toggle("action", "mail:acc1"));
    await click(button("选择"));
    // the folded mail group's tick picks both of its items
    await click(tick("action", "mail:acc1"));
    expect(tick("action", "mail:acc1")?.getAttribute("aria-checked")).toBe("true");
    expect(head("action", "mail:acc1").textContent).toContain("已选 2");
    expect(host.querySelector("[data-pick-bar]")?.textContent).toContain("已选 2 项（含折叠的 2 项）");
    // one of the Lark group: "some"
    await click(host.querySelector('[data-item="l1"] [role="checkbox"]'));
    expect(tick("action", "lark")?.getAttribute("aria-checked")).toBe("mixed");
    // Select all: the items in sight (Lark l1, l2 and FYI f1), not the folded mail ones again
    await click(button("全选"));
    expect(rowsInSight().sort()).toEqual(["f1", "l1", "l2"]);
    expect(host.querySelector("[data-pick-bar]")?.textContent).toContain("已选 5 项（含折叠的 2 项）");
    // Done and the history can't be picked
    expect(tick("done", "week")).toBeNull();
    await click(button("标为已处理"));
    const bulk = calls.list.filter((c) => c.path === "/inbox/bulk");
    expect((bulk[0].json as { items: { id: string }[] }).items.map((x) => x.id).sort()).toEqual(["f1", "l1", "l2", "m1", "m2"]);
  });
});

// Mounted again with what a test set up (the hub's day counts, a filter kept on this device).
const remount = async () => {
  act(() => root.unmount());
  calls.list = [];
  resetInboxForTests();
  root = createRoot(host);
  act(() => root.render(<Inbox onToggleSidebar={() => {}} />));
  await flush();
};
const historyAsks = () => calls.list.filter((c) => c.path.startsWith("/inbox/done?")).map((c) => new URLSearchParams(c.path.slice(c.path.indexOf("?") + 1)));
/** Local midnight `n` days before the tests' "now" (Mon 9 Nov 2026, 15:00). */
const midnight = (n: number) => new Date(2026, 10, 9 - n);

describe("Done's day counts from the hub (#2969)", () => {
  it("every day the hub counts has its group and exact count, loaded or not, with no page asked for", async () => {
    // three done 3 days ago, which the list no longer holds
    calls.history.push(...[1, 2, 3].map((i) => lark(`h3-${i}`, { done_at: ago(3, i), done_by: "user" })));
    calls.days = true;
    await remount();
    expect(groups("done")).toEqual(["just", "d:2026-11-06", "week", "month"]);
    expect(head("done", "d:2026-11-06").textContent).toContain("3");
    expect(head("done", "week").textContent).toContain("7 天前1");
    expect(head("done", "month").textContent).toContain("1 个月前1");
    expect(host.querySelector('[data-section="done"]')?.textContent).not.toContain("+");
    expect(historyAsks()).toEqual([]); // the groups past the list's days are folded: nothing loads until one opens
    expect(calls.list.find((c) => c.path.startsWith("/inbox/done/days?"))?.path).toContain("sources=all");
  });

  it("Jump To a group not loaded asks for the page from its end, then shows its items", async () => {
    calls.days = true;
    await remount();
    await click(toggle("done", "just").closest("section")?.querySelector('button[aria-haspopup="menu"]'));
    await click([...document.querySelectorAll('[role="menuitem"]')].find((el) => el.textContent?.includes("7 天前")));
    const ask = historyAsks().at(-1)!;
    expect(ask.get("before")).toBe(midnight(6).toISOString()); // 7+ days ends where the 6th day back starts
    expect(ask.get("before_id")).toBeNull();
    expect(toggle("done", "week").getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector('[data-item="h10"]')).toBeTruthy();
  });

  it("a big group loads a page at a time: Show Older continues where its page ended, until the group is complete", async () => {
    // 150 more done 8 to 22 days ago: 7+ days has 151
    calls.history.push(...Array.from({ length: 150 }, (_, i) => lark(`w${String(i).padStart(3, "0")}`, { done_at: ago(8 + (i % 15), 1 + (i % 7)), done_by: "user" })));
    calls.days = true;
    await remount();
    expect(head("done", "week").textContent).toContain("7 天前151");
    await click(toggle("done", "just").closest("section")?.querySelector('button[aria-haspopup="menu"]'));
    await click([...document.querySelectorAll('[role="menuitem"]')].find((el) => el.textContent?.includes("7 天前")));
    expect(historyAsks()).toHaveLength(1);
    expect(head("done", "week").querySelectorAll("[data-item]").length).toBe(100);
    await click(button("加载更早的"));
    const ask = historyAsks().at(-1)!;
    expect(ask.get("before_id")).toBeTruthy(); // where the first page ended
    expect(head("done", "week").querySelectorAll("[data-item]").length).toBe(151);
    expect(button("加载更早的")).toBeUndefined();
  });

  it("filtered, the counts are of what is loaded, and n+ until a group is complete", async () => {
    localStorage.setItem("inbox.source", "feishu"); // the Lark chip
    calls.history.push(lark("h12", { done_at: ago(12), done_by: "user" }));
    calls.days = true;
    await remount();
    // nothing of 7+ days is loaded yet: only Lark's done item of the list shows
    expect(groups("done")).toEqual([]);
    expect(host.querySelector('[data-section="done"]')?.textContent).toContain("1+");
    await click(button("加载更早的"));
    expect(historyAsks().at(-1)!.get("before")).toBe(midnight(6).toISOString());
    expect(head("done", "week").textContent).toContain("7 天前2"); // h10 and h12, Lark's; the week is complete
    expect(head("done", "week").textContent).not.toContain("+");
  });
});
