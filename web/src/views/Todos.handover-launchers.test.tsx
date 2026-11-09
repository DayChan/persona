// @vitest-environment jsdom
// Handing a to-do to a new agent picks a launcher like "New agent" does (#1643): not one this computer's terminal can't
// run by default, and a blocked one is still a choice with why. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Launcher, Machine } from "../lib/types";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

let launchers: Launcher[] = [];
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: async (path: string) => (path.endsWith("/launchers") ? { launchers } : {}),
}));
vi.mock("react-dom", async (original) => ({ ...(await original<typeof import("react-dom")>()), createPortal: (children: React.ReactNode) => children }));

const { HandoverDialog } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const machines: Machine[] = [{ id: "mac", name: "Mac", online: true }];
const todo = { id: "t1", title: "Release checklist", created_at: "2026-11-09T08:00:00Z", updated_at: "2026-11-09T08:00:00Z" };
const select = () => [...document.querySelectorAll<HTMLSelectElement>("select")].find((s) => [...s.options].some((o) => o.value === "claude"))!;

async function open() {
  await act(async () => root.render(<HandoverDialog open todo={todo} machines={machines} onClose={() => {}} />));
  await flush();
}

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("handing a to-do to a new agent", () => {
  it("starts with a launcher that can start, and lists a blocked one after the rest with why", async () => {
    launchers = [
      { name: "work_gw", health: { state: "blocked", kind: "not_in_terminal" } },
      { name: "claude", health: { state: "ok" } },
    ];
    await open();
    const s = select();
    expect(s.value).toBe("claude");
    expect([...s.options].map((o) => o.textContent)).toEqual(["claude", "work_gw · 这台电脑的终端里用不了"]);
    expect(document.querySelector('[data-testid="todo-handoff-blocked"]')).toBeNull();
    // still a choice: a start that works clears it; picked, the reason is said under it
    await act(async () => {
      s.value = "work_gw";
      s.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(document.querySelector('[data-testid="todo-handoff-blocked"]')!.textContent).toBe("这台电脑的终端里用不了");
  });

  it("an older hub's launchers (no health) start with the first, as before", async () => {
    launchers = [{ name: "work_gw" }, { name: "claude" }];
    await open();
    expect(select().value).toBe("work_gw");
  });
});
