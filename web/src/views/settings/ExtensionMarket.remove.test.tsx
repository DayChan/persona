// @vitest-environment jsdom
// Removing a market (#1895): the extensions installed from it stay but stop upgrading, so the confirmation says how many
// (the manager's own, by the market the hub says each upgrades from) and the toast says they no longer upgrade.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Market } from "../../lib/extension-market";
import type { HubExtension } from "../../lib/extensions";

const hub = vi.hoisted(() => ({
  markets: [] as Market[],
  extensions: [] as Partial<HubExtension>[],
  listFails: false,
  removed: [] as string[],
  toasts: [] as string[],
}));
vi.mock("../../lib/extension-market", async (original) => ({
  ...(await original<typeof import("../../lib/extension-market")>()),
  listMarkets: () => Promise.resolve(hub.markets),
  removeMarket: (id: string) => (hub.removed.push(id), Promise.resolve({ ok: true })),
}));
vi.mock("../../lib/extensions", async (original) => ({
  ...(await original<typeof import("../../lib/extensions")>()),
  extensionUpgrades: () => Promise.resolve(true),
  listExtensions: () => (hub.listFails ? Promise.reject(new Error("offline")) : Promise.resolve(hub.extensions)),
}));
vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast: (text: string) => void hub.toasts.push(text),
}));
import { Markets } from "./ExtensionMarket";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const market = (id: string): Market => ({ id, spec: `https://example.com/${id}`, name: `Market ${id}`, has_token: false, extensions: 0 });
const click = async (label: string) => {
  const b = [...host.querySelectorAll("button")].find((x) => x.textContent === label);
  if (!b) throw new Error(`no button ${label}`);
  act(() => b.click());
  await flush();
};
const note = () => host.querySelector("[data-market-removal-note]")?.textContent ?? null;

beforeEach(() => {
  hub.markets = [market("m1")];
  hub.extensions = [];
  hub.listFails = false;
  hub.removed = [];
  hub.toasts = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

const open = async () => {
  act(() => root.render(<Markets onBack={() => {}} />));
  await flush();
};

describe("removing a market", () => {
  it("counts the manager's own extensions that stop upgrading, and says so after removing", async () => {
    // the hub's merged list: every item names its manager, the default's (the hub's own assistant) first
    hub.extensions = [
      { id: "a", manager: "assistant", auto_upgrade: true, market: "m1" },
      { id: "b", manager: "assistant", auto_upgrade: false, market: "m1" },
      { id: "c", manager: "assistant", auto_upgrade: true, market: "m2" },
      { id: "e", manager: "assistant", source: "builtin" },
      { id: "d", manager: "example-manager", auto_upgrade: true, market: "m1" },
    ];
    await open();
    expect(note()).toBeNull();
    await click("删除");
    expect(note()).toBe("hub 不再读这个市场。从它装的 2 个扩展留着，但不再升级，直到用同样的地址把市场加回来。");
    await click("确认删除");
    expect(hub.removed).toEqual(["m1"]);
    expect(hub.toasts).toEqual(["已删除「Market m1」，从它装的扩展留着，但不再升级"]);
    expect(note()).toBeNull();
  });

  it("says nothing stops upgrading when no installed extension came from it", async () => {
    hub.extensions = [{ id: "c", auto_upgrade: true, market: "m2" }];
    await open();
    await click("删除");
    expect(note()).toBe("hub 不再读这个市场。没有扩展是从它装的。");
    await click("确认删除");
    expect(hub.toasts).toEqual(["已删除「Market m1」"]);
  });

  it("still says extensions stop upgrading when the count is unknown", async () => {
    hub.extensions = [{ id: "a", auto_upgrade: true }]; // a hub from before `market`
    await open();
    await click("删除");
    expect(note()).toBe("hub 不再读这个市场。从它装的扩展留着，但不再升级，直到用同样的地址把市场加回来。");
    await click("取消");
    expect(note()).toBeNull();
    hub.listFails = true;
    await click("删除");
    expect(note()).toContain("从它装的扩展留着，但不再升级");
    await click("确认删除");
    expect(hub.toasts).toEqual(["已删除「Market m1」，从它装的扩展留着，但不再升级"]);
  });

  it("keeps each market's count to its own confirmation", async () => {
    hub.markets = [market("m1"), market("m2")];
    hub.extensions = [{ id: "a", auto_upgrade: true, market: "m1" }];
    await open();
    const deletes = () => [...host.querySelectorAll("button")].filter((x) => x.textContent === "删除");
    act(() => deletes()[0].click());
    await flush();
    expect(note()).toContain("从它装的 1 个扩展");
    await click("取消");
    act(() => deletes()[1].click());
    await flush();
    expect(note()).toBe("hub 不再读这个市场。没有扩展是从它装的。");
  });
});
