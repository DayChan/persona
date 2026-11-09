// @vitest-environment jsdom
// The Messages chat list redraws only the rows that changed: a `lark` push whose reread brings back the same chats, or
// a search keystroke every row still matches, draws no row again; a push that changed one chat draws that one; a new
// day draws every row again so its time label moves on ("Yesterday" for what was "10:00").
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { LarkConversation } from "../../lib/lark-chats";

type Listener = (msg: Record<string, unknown>) => void;
const hub = vi.hoisted(() => ({ listeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>(), rowRenders: 0 }));
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: Listener) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
}));
// Only ChatRow builds a chat's second line, once per render: the count of those calls is the count of row renders.
vi.mock("../../lib/lark-chats", async (original) => {
  const real = await original<typeof import("../../lib/lark-chats")>();
  return {
    ...real,
    conversationPreview: (...args: Parameters<typeof real.conversationPreview>) => {
      hub.rowRenders++;
      return real.conversationPreview(...args);
    },
  };
});
import { t } from "../../i18n";
import { setLarkHome } from "../../lib/lark-chats";
import { Messages } from "./Messages";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ROWS = 60;
const chat = (n: number): LarkConversation => ({
  id: `oc_${n}`, name: `Chat ${n}`, is_group: n % 2 === 0, unread: n % 3, at_me: false, muted: false, preview: `hello ${n}`,
  is_mine: false, updated_at: new Date(2026, 10, 4, 10, 0).toISOString(),
});

let host: HTMLDivElement;
let root: Root;
let chats: LarkConversation[];
const flush = async (n = 20) => {
  for (let i = 0; i < n; i++) await act(async () => { await Promise.resolve(); });
};
/** A `lark` push, then the 800 ms the page waits before it reads the list again. */
const push = async () => {
  await act(async () => hub.listeners.get("lark")?.forEach((l) => l({ type: "lark", feed_ids: ["oc_1"] })));
  await act(async () => { vi.advanceTimersByTime(900); });
  await flush();
};
const type = async (text: string) => {
  const input = host.querySelector("input")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const rowText = (id: string) => host.querySelector(`a[data-chat="${id}"]`)?.textContent ?? "";

beforeEach(async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
  vi.setSystemTime(new Date(2026, 10, 4, 15, 0));
  hub.rowRenders = 0;
  chats = Array.from({ length: ROWS }, (_, i) => chat(i + 1));
  localStorage.clear();
  setLarkHome(true);
  // Every read answers with fresh objects, as the hub's JSON does.
  vi.stubGlobal("fetch", vi.fn(async (url: string) => {
    let body: unknown = { error: "not found" };
    let status = 404;
    if (url.includes("/lark/conversations?")) {
      body = { account_id: "acc", conversations: chats };
      status = 200;
    } else if (url.includes("/lark/pinned")) {
      body = { account_id: "acc", conversations: [] };
      status = 200;
    } else if (url.includes("/lark/accounts")) {
      body = { accounts: [] };
      status = 200;
    }
    return { ok: status < 400, status, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<Messages onToggleSidebar={() => {}} />));
  await flush();
  expect(host.querySelectorAll("a[data-chat]")).toHaveLength(ROWS);
  hub.rowRenders = 0;
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("Messages chat list renders", () => {
  it("draws no row again for a push that changed nothing", async () => {
    await push();
    expect(hub.rowRenders).toBe(0);
  });

  it("draws only the chat that changed", async () => {
    chats = chats.map((c) => (c.id === "oc_7" ? { ...c, preview: "new message", unread: 4 } : c));
    await push();
    expect(hub.rowRenders).toBe(1);
    expect(rowText("oc_7")).toContain("new message");
    expect(rowText("oc_8")).toContain("hello 8");
  });

  it("draws no row again for a keystroke every row still matches", async () => {
    await type("c");
    await type("chat");
    expect(host.querySelectorAll("a[data-chat]")).toHaveLength(ROWS);
    expect(hub.rowRenders).toBe(0);
    await type("chat 7");
    expect(host.querySelectorAll("a[data-chat]")).toHaveLength(1);
    expect(hub.rowRenders).toBe(0);
  });

  it("draws every row again on a new day so the time labels move on", async () => {
    expect(rowText("oc_1")).toContain("10:00");
    vi.setSystemTime(new Date(2026, 10, 5, 9, 0));
    await push();
    expect(hub.rowRenders).toBe(ROWS);
    expect(rowText("oc_1")).toContain(t("lib-inbox.yesterday"));
  });
});
