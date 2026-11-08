// @vitest-environment jsdom
// lib/notify.ts against the hub and the other tabs (#2971): ordered saves, a browser without
// storage, the prefs and the front tab shared between tabs, one notification per event.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Call = { path: string; body?: unknown; resolve: (v: unknown) => void; reject: (e: unknown) => void };
const calls: Call[] = [];
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  api: (path: string, init?: { json?: unknown }) => new Promise((resolve, reject) => calls.push({ path, body: init?.json, resolve, reject })),
  enc: encodeURIComponent,
}));

const n = await import("./notify");

beforeEach(() => {
  calls.length = 0;
  localStorage.clear();
  n._setTabId("tab-me");
});
afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("serialized preference saves", () => {
  it("keeps a queued edit visible when the preceding save completes", async () => {
    const load = n.loadNotifyPrefs();
    calls[0].resolve({ input: true, done: "long" });
    await load;
    calls.length = 0;
    const a = n.saveNotifyPrefs({ input: false });
    const b = n.saveNotifyPrefs({ done: "always" });
    expect(n.notifyPrefs()).toMatchObject({ input: false, done: "always" });
    expect(calls).toHaveLength(1);
    calls[0].resolve({ input: false, done: "long" });
    await a;
    expect(n.notifyPrefs()).toMatchObject({ input: false, done: "always" });
    calls[1].resolve({ input: false, done: "always" });
    await b;
  });

  it("rejects an earlier failed edit while keeping the queued edit", async () => {
    calls.length = 0;
    const a = n.saveNotifyPrefs({ input: true, done: "long" });
    calls[0].resolve({ input: true, done: "long" });
    await a;
    calls.length = 0;
    const first = n.saveNotifyPrefs({ input: false });
    const second = n.saveNotifyPrefs({ done: "off" });
    calls[0].reject(new Error("offline"));
    await expect(first).rejects.toThrow("offline");
    expect(n.notifyPrefs()).toMatchObject({ input: true, done: "off" });
    calls[1].resolve({ input: true, done: "off" });
    await second;
    expect(n.notifyPrefs()).toMatchObject({ input: true, done: "off" });
  });

  it("最后一次失败就回到它之前", async () => {
    calls.length = 0;
    const a = n.saveNotifyPrefs({ input: true, done: "long" });
    calls[0].resolve({ input: true, done: "long" });
    await a;
    calls.length = 0;
    const p = n.saveNotifyPrefs({ done: "always" });
    calls[0].reject(new Error("500"));
    await expect(p).rejects.toThrow("500");
    expect(n.notifyPrefs().done).toBe("long");
  });
});

describe("设备 id", () => {
  it("存在这个浏览器里，每次一样", () => {
    const a = n.deviceId();
    expect(a.kept).toBe(true);
    expect(a.id).toMatch(/^web-/);
    expect(n.deviceId().id).toBe(a.id);
  });

  it("没有存储时是这一页自己的，不发给 hub，也不和别的浏览器共用一个 id", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const id = n.deviceId();
    expect(id.kept).toBe(false);
    expect(id.id).not.toBe("web");
    calls.length = 0;
    await n.saveNotifyPrefs({ done: "off" });
    await n.loadNotifyPrefs();
    expect(calls).toEqual([]);
    expect(n.notifyPrefs().done).toBe("off");
  });
});

describe("标签页之间", () => {
  it("别的标签页改了偏好，这一页马上跟上", () => {
    localStorage.setItem("persona.notifyPrefs", JSON.stringify({ input: false, done: "off" }));
    window.dispatchEvent(new StorageEvent("storage", { key: "persona.notifyPrefs" }));
    expect(n.notifyPrefs()).toMatchObject({ input: false, done: "off" });
  });

  it("另一个标签页在前台时算在前台；它走开或太久没说话就不算", () => {
    const t0 = 1_000_000;
    n._setTabId("tab-a");
    n.markFront(true, t0);
    n._setTabId("tab-b");
    expect(n.otherTabInFront(t0 + 1000)).toBe(true);
    expect(n.otherTabInFront(t0 + n.FRONT_FRESH_MS)).toBe(false);
    n.markFront(false, t0); // b was not the one in front: a's mark stays
    expect(n.otherTabInFront(t0 + 1000)).toBe(true);
    n._setTabId("tab-a");
    expect(n.otherTabInFront(t0 + 1000)).toBe(false); // itself
    n.markFront(false, t0 + 2000);
    n._setTabId("tab-b");
    expect(n.otherTabInFront(t0 + 3000)).toBe(false);
  });

  it("同一件事只有先到的标签页发通知", () => {
    const t0 = 2_000_000;
    n._setTabId("tab-a");
    expect(n.claimNotification("m/s#needs", t0)).toBe(true);
    n._setTabId("tab-b");
    expect(n.claimNotification("m/s#needs", t0 + 300)).toBe(false);
    expect(n.claimNotification("m/s#finished", t0 + 300)).toBe(true);
    expect(n.claimNotification("m/s#needs", t0 + n.NOTIFIED_WINDOW_MS + 1)).toBe(true);
  });
});
