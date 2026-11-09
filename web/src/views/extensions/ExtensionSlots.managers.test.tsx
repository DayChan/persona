// @vitest-environment jsdom
// The extensions' places with several managers (docs/assistant/managers.md "Clients"): another manager's inbox item is drawn by
// its own extensions and a computer of another manager gets that manager's sections, each asked for and drawn with the
// manager's name. The sidebar's badges and the workflow board's switch are the default's: before the roster says which
// manager that is none of their pushes is taken, and both are read again once it does.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";

const hub = vi.hoisted(() => ({ listeners: new Map<string, ((m: Record<string, unknown>) => void)[]>() }));
const STATE = vi.hoisted(() => ({
  features: ["extension_ui_slots", "extensions"], conn: "online", version: "1", machines: [{ id: "work-mac", name: "Work Mac", online: true, manager: "m-1" }],
}));
vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return {
    ...real,
    getState: () => ({ ...real.getState(), ...STATE }),
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T): T => select({ ...real.getState(), ...STATE } as ReturnType<typeof real.getState>),
    onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
      hub.listeners.set(type, [...(hub.listeners.get(type) ?? []), l]);
      return () => {};
    },
  };
});

import { useExtensionSlot } from "../../lib/extension-slots";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { getWorkspaceBoardSetting } from "../../lib/workspace-board-setting";
import { InboxExtensionBody } from "../inbox/InboxExtensionBody";
import { MachineExtensionSections } from "./ExtensionSlots";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const emit = (type: string, msg: Record<string, unknown>) => hub.listeners.get(type)?.forEach((l) => l({ type, ...msg }));
const calls: { url: string; method: string; header: string | null }[] = [];
const place = (extension: string, id: string, placement: string, badge?: string) => ({ extension, id, placement, title: `${extension} ${id}`, ...(badge ? { badge: { text: badge } } : {}) });
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(() => {
  calls.length = 0;
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const header = new Headers(init?.headers).get("X-Persona-Manager");
    calls.push({ url, method: init?.method ?? "GET", header });
    let body: unknown = {};
    if (url.includes("/-/ui") && !url.includes("/-/ui/")) {
      // each manager's own places
      body = { ui: header === "m-1" ? [place("work-ext", "item", "inbox_item"), place("work-ext", "mac", "machine_section")] : [place("home-ext", "nav", "nav_page", "2")] };
    } else if (url.includes("/-/ui/")) body = { view: { sections: [{ items: [{ type: "text", text: "drawn" }] }] } };
    else if (url.includes("/workspace-board/settings")) body = { enabled: true, can_write: true };
    return { ok: true, status: 200, text: async () => JSON.stringify(body), headers: new Headers() };
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

function Slot({ placement, manager }: { placement: "inbox_item" | "nav_page"; manager?: string }) {
  const places = useExtensionSlot(placement, manager);
  return <p data-places>{places.map((p) => `${p.extension}/${p.id}:${p.badge?.text ?? 0}`).join(",")}</p>;
}

// the code views and the extension page are their own chunks (lib/lazy-page.ts, #1794): in before anything is drawn
beforeAll(() => loadPages(), 60_000);

describe("the extensions' places of each manager", () => {
  it("takes no badge before the roster says which manager is the default, and reads the places again once it does", async () => {
    resetManagerChoice();
    act(() => root.render(<Slot placement="nav_page" />));
    await flush();
    expect(host.textContent).toBe("home-ext/nav:2");
    const asked = calls.filter((c) => c.url.includes("/-/ui")).length;
    // the real default (m-2, not the hub's own) pushes a badge before the roster is read: left for the read below
    act(() => emit("extension_ui", { extension: "home-ext", id: "nav", badge: { text: "5" }, manager: "m-2" }));
    expect(host.textContent).toBe("home-ext/nav:2");
    act(() => setRosterDefault("m-2"));
    await flush();
    expect(calls.filter((c) => c.url.includes("/-/ui")).length).toBeGreaterThan(asked);
    // the default's now: taken; the hub's own assistant's (no manager) is not this one's
    act(() => emit("extension_ui", { extension: "home-ext", id: "nav", badge: { text: "7" }, manager: "m-2" }));
    expect(host.textContent).toBe("home-ext/nav:7");
    act(() => emit("extension_ui", { extension: "home-ext", id: "nav", badge: { text: "9" } }));
    expect(host.textContent).toBe("home-ext/nav:7");
  });

  it("asks another manager's places with its name", async () => {
    resetManagerChoice();
    setRosterDefault("assistant");
    act(() => root.render(<Slot placement="inbox_item" manager="m-1" />));
    await flush();
    expect(host.textContent).toBe("work-ext/item:0");
    expect(calls.filter((c) => c.url.includes("/-/ui")).some((c) => c.header === "m-1")).toBe(true);
  });

  it("draws another manager's inbox item with its manager's name", async () => {
    resetManagerChoice();
    setRosterDefault("assistant");
    const item = { id: "github:1", manager: "m-1", source: "github", kind: "dm", account: "", account_name: "", title: "x", preview: "", count: 1, updated_at: "2026-11-01T00:00:00Z", tier: "action" };
    act(() => root.render(<InboxExtensionBody item={item} place={place("work-ext", "item", "inbox_item")} own={() => null} />));
    await flush();
    const drawn = calls.find((c) => c.url.includes("/work-ext/-/ui/item"))!;
    expect(drawn.header).toBe("m-1");
  });

  it("gives a computer of another manager its manager's sections, drawn with its name", async () => {
    resetManagerChoice();
    setRosterDefault("assistant");
    act(() => root.render(<MachineExtensionSections mid="work-mac" heading={(title) => <h3>{title}</h3>} />));
    await flush();
    expect(host.textContent).toContain("work-ext mac");
    expect(calls.find((c) => c.url.includes("/work-ext/-/ui/mac"))?.header).toBe("m-1");
  });
});

describe("the workflow board's switch", () => {
  it("is read again once the roster says which manager is the default; another manager's push is not it", async () => {
    resetManagerChoice();
    act(() => emit("workspace_board_settings", { enabled: false, manager: "m-2" }));
    expect(getWorkspaceBoardSetting().enabled).not.toBe(false);
    act(() => setRosterDefault("m-2"));
    await flush();
    expect(calls.some((c) => c.url.includes("/workspace-board/settings"))).toBe(true);
    expect(getWorkspaceBoardSetting().enabled).toBe(true);
    act(() => emit("workspace_board_settings", { enabled: false }));
    expect(getWorkspaceBoardSetting().enabled).toBe(true);
    act(() => emit("workspace_board_settings", { enabled: false, manager: "m-2" }));
    expect(getWorkspaceBoardSetting().enabled).toBe(false);
  });
});
