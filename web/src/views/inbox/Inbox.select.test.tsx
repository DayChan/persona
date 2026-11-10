// @vitest-environment jsdom
// Selecting in the inbox (#2059): "Select" at the top right turns the open items in sight into checkboxes (the agents waiting
// and the done items stay as they are, dimmed), the bar under the list says how many are picked, "Select all" picks what the
// list shows, "Mark as done" files the picked ones in one bulk request and ends selecting; Done or Esc ends it too. Marking
// is Persona's only (the user's choice): no "Mark as read", nothing written back to Lark or mail.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const calls = vi.hoisted(() => ({ list: [] as { path: string; method: string; json?: unknown }[], items: [] as unknown[] }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: { items?: { id: string }[] } }) => {
    calls.list.push({ path, method: init?.method ?? "GET", json: init?.json });
    if (path.startsWith("/inbox?")) return Promise.resolve({ items: calls.items });
    if (path === "/mail/accounts") return Promise.resolve({ accounts: [] });
    if (path === "/inbox/bulk") {
      const ids = new Set((init?.json?.items ?? []).map((x) => x.id));
      const changed = (calls.items as { id: string }[]).filter((x) => ids.has(x.id)).map((x) => ({ ...x, done_at: "2026-11-09T05:00:00Z", done_by: "user" }));
      return Promise.resolve({ changed, unchanged: [], skipped: [] });
    }
    return Promise.resolve({});
  },
}));
vi.mock("../../lib/store", async (orig) => {
  const m = await orig<typeof import("../../lib/store")>();
  return { ...m, useStore: <T,>(sel: (s: ReturnType<typeof m.getState>) => T) => m.useStore((s) => sel({ ...s, loaded: true })) };
});

import { resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { Inbox } from "./Inbox";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (id: string, patch: Partial<InboxItem> = {}): InboxItem => ({
  id, source: "lark", kind: "group", account: "feishu", account_name: "Lark", title: `chat ${id}`, preview: "text", count: 1,
  updated_at: "2026-11-09T04:00:00Z", tier: "action", version: `v-${id}`, lark: { feed_id: id, is_group: true }, ...patch,
});

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};
const button = (text: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === text);
// the rows in sight: a closed section keeps its rows, inert
const boxes = () => [...host.querySelectorAll<HTMLElement>('[role="checkbox"]')].filter((b) => !b.closest("[inert]"));
const bar = () => host.querySelector("[data-pick-bar]");
const click = async (el: Element | undefined) => {
  expect(el).toBeDefined();
  await act(async () => (el as HTMLElement).click());
  await flush();
};

beforeEach(async () => {
  calls.list = [];
  // two in Needs you, one in FYI, one in Collapsed (closed at first), one done
  calls.items = [item("a1", { agent: { state: "drafted", draft: "好的，周五前给你。" } as InboxItem["agent"] }), item("a2"), item("f1", { tier: "fyi" }), item("n1", { tier: "noise" }), item("d1", { done_at: "2026-11-09T03:00:00Z", done_by: "user" })];
  resetInboxForTests();
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
});

describe("inbox selection (#2059)", () => {
  it("Select turns the open items in sight into ticks; a click picks instead of opening", async () => {
    expect(boxes()).toHaveLength(0);
    expect(bar()).toBeNull();
    await click(button("选择"));
    // Needs you and FYI are open: a1, a2, f1; Collapsed (n1) and Done (d1) are closed
    expect(boxes().map((b) => b.closest("[data-item]")?.getAttribute("data-item"))).toEqual(["a1", "a2", "f1"]);
    expect(host.querySelector('[data-item="a1"] a')).toBeNull();
    expect(button("完成")).toBeDefined();
    expect(button("选择")).toBeUndefined();
    expect(bar()?.textContent).toContain("已选 0 项");
    expect(button("标为已处理")?.disabled).toBe(true);
    expect(button("标为已读")).toBeUndefined();

    await click(boxes()[1]);
    expect(boxes()[1].getAttribute("aria-checked")).toBe("true");
    expect(host.querySelector('[data-item="a2"]')?.hasAttribute("data-picked")).toBe(true);
    expect(bar()?.textContent).toContain("已选 1 项");
    expect(button("标为已处理")?.disabled).toBe(false);
    await click(boxes()[1]);
    expect(bar()?.textContent).toContain("已选 0 项");
    // the draft under a row is inert while selecting: a click there picks the row, it sends nothing
    const draft = [...host.querySelectorAll<HTMLElement>('[data-item="a1"] [inert]')][0];
    expect(draft).toBeDefined();
    await click(draft);
    expect(boxes()[0].getAttribute("aria-checked")).toBe("true");
  });

  it("Select All picks only what the list shows; a second click deselects all", async () => {
    await click(button("选择"));
    await click(button("全选"));
    expect(boxes().every((b) => b.getAttribute("aria-checked") === "true")).toBe(true);
    expect(bar()?.textContent).toContain("已选 3 项");
    await click(button("取消全选"));
    expect(boxes().some((b) => b.getAttribute("aria-checked") === "true")).toBe(false);
  });

  it("done items can't be picked and are dimmed", async () => {
    await click(button("选择"));
    await click([...host.querySelectorAll("[data-section=done] button")].find((b) => b.getAttribute("aria-expanded") !== null));
    const done = host.querySelector('[data-item="d1"]')!;
    expect(done.querySelector('[role="checkbox"]')).toBeNull();
    expect(done.querySelector("a")).toBeNull();
    expect(done.className).toContain("opacity-45");
  });

  it("Mark as Done is one bulk request with versions, then selection ends", async () => {
    await click(button("选择"));
    await click(boxes()[0]);
    await click(boxes()[2]);
    await click(button("标为已处理"));
    const bulk = calls.list.filter((c) => c.path === "/inbox/bulk");
    expect(bulk).toHaveLength(1);
    expect(bulk[0].json).toEqual({ action: "done", items: [{ id: "a1", version: "v-a1" }, { id: "f1", version: "v-f1" }] });
    expect(boxes()).toHaveLength(0);
    expect(bar()).toBeNull();
    expect(button("选择")).toBeDefined();
  });

  it("Done and Esc both end selection without sending anything", async () => {
    await click(button("选择"));
    await click(boxes()[0]);
    await click(button("完成"));
    expect(boxes()).toHaveLength(0);
    await click(button("选择"));
    expect(bar()?.textContent).toContain("已选 0 项"); // a new selection starts empty
    await act(async () => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await flush();
    expect(bar()).toBeNull();
    expect(calls.list.some((c) => c.path === "/inbox/bulk")).toBe(false);
  });
});
