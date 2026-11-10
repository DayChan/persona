// @vitest-environment jsdom
// Exercise the real store and sidebar: a telemetry push must skip tree construction, while the fields the sidebar
// draws and each memoized component's own subscriptions still reach the screen.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const renders = vi.hoisted(() => new Map<string, number>());
vi.mock("../i18n", async (load) => {
  const real = await load<typeof import("../i18n")>();
  return { ...real, t: (...args: Parameters<typeof real.t>) => {
    // These labels are read once in the bodies of the grouping bar, filter bar and account row.
    renders.set(args[0], (renders.get(args[0]) ?? 0) + 1);
    return real.t(...args);
  } };
});
vi.mock("../lib/todo-tree", async (load) => {
  const real = await load<typeof import("../lib/todo-tree")>();
  return { ...real, buildTodoTree: vi.fn(real.buildTodoTree) };
});
vi.mock("../lib/todos", async (load) => {
  const real = await load<typeof import("../lib/todos")>();
  return { ...real, buildTodoAgentGroups: vi.fn(real.buildTodoAgentGroups) };
});
vi.mock("./LinkTodoDialog", async (load) => {
  const real = await load<typeof import("./LinkTodoDialog")>();
  return { ...real, LinkTodoDialog: (props: Parameters<typeof real.LinkTodoDialog>[0]) => {
    renders.set("link", (renders.get("link") ?? 0) + 1);
    return <real.LinkTodoDialog {...props} />;
  } };
});
vi.mock("./AgentMenus", async (load) => {
  const real = await load<typeof import("./AgentMenus")>();
  return { ...real, MarkSeenDialog: (props: Parameters<typeof real.MarkSeenDialog>[0]) => {
    renders.set("seen", (renders.get("seen") ?? 0) + 1);
    return <real.MarkSeenDialog {...props} />;
  } };
});
vi.mock("./NodePrompt", async (load) => {
  const real = await load<typeof import("./NodePrompt")>();
  return { ...real, NodePrompt: () => {
    renders.set("node", (renders.get("node") ?? 0) + 1);
    return <real.NodePrompt />;
  } };
});
vi.mock("../lib/usage-windows", async (load) => {
  const real = await load<typeof import("../lib/usage-windows")>();
  return { ...real, useUsageWindows: () => {
    renders.set("usage", (renders.get("usage") ?? 0) + 1);
    return real.useUsageWindows();
  } };
});
import { t } from "../i18n";
import { connect, flushPushes, getState, refresh, reset } from "../lib/store";
import { buildTodoTree } from "../lib/todo-tree";
import { buildTodoAgentGroups, type Todo } from "../lib/todos";
import type { Machine, Session } from "../lib/types";
import type { UsageAccount } from "../lib/usage-windows";
import { Sidebar } from "./Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const at = "2026-11-10T10:00:00Z";
const machines: Machine[] = [
  { id: "laptop", name: "Laptop", online: true, last_seen: at, hub_id: "home", hub_name: "Home" },
  { id: "desktop", name: "Desktop", online: true, last_seen: at, hub_id: "office", hub_name: "Office" },
];
const agent: Session = { machine_id: "laptop", session_id: "build", title: "Build app", cwd: "/code/weather", status: "working", state: "working", updated_at: at };
const todo: Todo = { id: "release", title: "Release app", created_at: at, updated_at: at, agents: [{ machine_id: "laptop", session_id: "build", linked_at: at }] };
const account = (used: number): UsageAccount => ({ id: "plan", provider: "codex", name: "Plan", sampled_at: at, windows: [{ label: "5h", window_minutes: 300, used_percent: used }] });
let usage: UsageAccount[];

class FakeSocket {
  static OPEN = 1;
  static last: FakeSocket;
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor() { FakeSocket.last = this; }
  send() {}
  close() { this.readyState = 3; }
}
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};
const push = async (message: Record<string, unknown>) => {
  act(() => FakeSocket.last.onmessage?.({ data: JSON.stringify(message) }));
  act(() => flushPushes());
  await flush();
};
const clearCounts = () => {
  renders.clear();
  vi.mocked(buildTodoTree).mockClear();
  vi.mocked(buildTodoAgentGroups).mockClear();
};
const shellReads = () => ["sidebar.grouping_header", "sidebar.quick_filters", "sidebar.theme_system", "link", "seen", "node", "usage"].map((key) => [key, renders.get(key) ?? 0]);
const row = () => host.querySelector<HTMLAnchorElement>('a[href="/s/laptop/build"]');
const filterBar = () => host.querySelector('[role="radiogroup"][aria-label="' + t("sidebar.quick_filters") + '"]')!;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(at));
  localStorage.clear();
  reset();
  usage = [account(20)];
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("fetch", vi.fn((url: string) => {
    const path = new URL(url, "http://hub.example.com").pathname;
    const answers: Record<string, unknown> = {
      "/api/v1/sessions": { sessions: [agent] },
      "/api/v1/machines": { machines },
      "/api/v1/todos": { todos: [todo] },
      "/api/v1/usage/windows": { accounts: usage },
    };
    return Promise.resolve(new Response(JSON.stringify(answers[path] ?? {}), { headers: { "Content-Type": "application/json" } }));
  }));
  history.replaceState(null, "", "/overview");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  clearCounts();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function mount() {
  await act(() => refresh());
  act(() => connect());
  act(() => { FakeSocket.last.readyState = 1; FakeSocket.last.onopen?.(); });
  await push({ type: "hello", server_version: "v1", event_stream: "stream", event_seq: 1, resumed: true, features: ["sessions_delta"] });
  act(() => root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />));
  await flush();
  // Drain mount effects and collapse transitions, then keep the clock fixed across the measured pushes.
  await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
  await flush();
  expect(host.textContent).toContain("Build app");
  expect(host.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow")).toBe("20");
  expect(buildTodoTree).toHaveBeenCalled();
  expect(buildTodoAgentGroups).toHaveBeenCalled();
  for (const [, count] of shellReads()) expect(count).toBeGreaterThan(0);
  clearCounts();
}

describe("the sidebar shell across store pushes", () => {
  it("skips tree construction and unchanged shell components for load and memory pushes", async () => {
    await mount();
    const before = host.textContent;
    for (let i = 1; i <= 3; i++) {
      await push({ type: "machines", machines: machines.map((m) => ({ ...m, load: [i, i, i], memory: { used: i * 1024, total: 8192 }, last_seen: new Date(Date.parse(at) + i).toISOString() })) });
    }
    expect(buildTodoTree).not.toHaveBeenCalled();
    expect(buildTodoAgentGroups).not.toHaveBeenCalled();
    expect(shellReads()).toEqual(shellReads().map(([key]) => [key, 0]));
    expect(host.textContent).toBe(before);
    // Prove the socket notifications were delivered, not merely queued behind a frozen timer.
    expect(getState().machines[0].last_seen).toBe(new Date(Date.parse(at) + 3).toISOString());
  });

  it("updates the changed agent but skips the bars, meters and closed dialogs when counts stay equal", async () => {
    await mount();
    for (let i = 1; i <= 3; i++) {
      await push({ type: "sessions", machine_id: "laptop", sessions: [{ ...agent, title: `Build app ${i}` }] });
    }
    expect(buildTodoTree).toHaveBeenCalledTimes(3);
    expect(buildTodoAgentGroups).toHaveBeenCalledTimes(3);
    expect(host.textContent).toContain("Build app 3");
    expect(shellReads()).toEqual(shellReads().map(([key]) => [key, 0]));
    // A change to the counted state must still update the quick filter.
    await push({ type: "sessions", machine_id: "laptop", sessions: [{ ...agent, state: "needs_you", status: "needs_you" }] });
    expect(filterBar().textContent).toContain(t("lib-state-labels.needs_you"));
    expect(renders.get("sidebar.quick_filters")).toBeGreaterThan(0);
    expect(filterBar().textContent).toContain("1");
  });

  it("chooses the latest matching sessions after the filter bar skipped a push", async () => {
    await mount();
    const replacement = { ...agent, session_id: "replacement", title: "Build replacement" };
    await push({ type: "sessions", machine_id: "laptop", sessions: [replacement] });
    expect(renders.get("sidebar.quick_filters") ?? 0).toBe(0);
    const working = [...filterBar().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("lib-state-labels.working")))!;
    act(() => working.click());
    await flush();
    expect(localStorage.getItem("persona.agentFilter")).toBe("working");
    await push({ type: "sessions", machine_id: "laptop", sessions: [{ ...replacement, state: "idle", status: "idle" }] });
    // A selected filter keeps its original matches after their state changes; the stable handler must have
    // captured replacement, not the old build session from the last time the bar rendered.
    expect(host.querySelector('a[href="/s/laptop/replacement"]')?.textContent).toContain("Build replacement");
    expect(row()).toBeNull();
  });

  it("opens and closes the memoized link dialog for the current row", async () => {
    await mount();
    const openLink = async () => {
      act(() => row()!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 })));
      await flush();
      const link = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find((b) => b.textContent?.includes(t("agentmenus.link_to_todo")))!;
      expect(link).toBeTruthy();
      act(() => link.click());
      await flush();
      return document.querySelector('[role="dialog"][aria-modal="true"]');
    };
    expect((await openLink())?.textContent).toContain("Build app");
    act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })));
    await flush();
    expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeNull();
    await push({ type: "sessions", machine_id: "laptop", sessions: [{ ...agent, title: "Build changed" }] });
    expect((await openLink())?.textContent).toContain("Build changed");
  });

  it("opens, cancels and completes mark seen through its stable callbacks", async () => {
    await mount();
    await push({ type: "sessions", machine_id: "laptop", sessions: [{ ...agent, state: "unread", status: "idle" }] });
    const unread = [...filterBar().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("lib-state-labels.unread")))!;
    act(() => unread.click());
    await flush();
    const openSeen = async () => {
      const button = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("lib-state-labels.mark_all_seen")))!;
      act(() => button.click());
      await flush();
      return document.querySelector('[role="dialog"][aria-modal="true"]')!;
    };
    const first = await openSeen();
    expect(first).toBeTruthy();
    const cancel = [...first.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("common.cancel"))!;
    act(() => cancel.click());
    await flush();
    expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeNull();
    expect(getState().sessions.laptop[0].state).toBe("unread");
    const second = await openSeen();
    const done = [...second.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("lib-state-labels.mark_all_seen_count", { count: 1 }))!;
    act(() => done.click());
    await flush();
    expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeNull();
    expect(getState().sessions.laptop[0].state).toBe("idle");
    expect(row()).toBeNull(); // completion clears the filter's kept matches too
  });

  it("updates computer and hub names in agent rows and rebuilds when a computer goes offline", async () => {
    await mount();
    expect(host.textContent).toContain("Home · Laptop");
    await push({ type: "machines", machines: machines.map((m) => m.id === "laptop" ? { ...m, name: "Notebook", hub_name: "Studio" } : m) });
    expect(host.textContent).toContain("Studio · Notebook");
    expect(host.textContent).not.toContain("Home · Laptop");
    expect(buildTodoTree).toHaveBeenCalled();
    clearCounts();
    await push({ type: "machines", machines: getState().machines.map((m) => m.id === "laptop" ? { ...m, online: false } : m) });
    expect(buildTodoTree).toHaveBeenCalled();
    expect(buildTodoAgentGroups).toHaveBeenCalled();
    expect(renders.get("sidebar.theme_system")).toBeGreaterThan(0);
    expect(host.textContent).toContain("1/2");
    expect(row()?.closest("[inert]")).toBeTruthy(); // the agent moves into the folded Recent section
    await push({ type: "machines", machines });
    expect(host.textContent).toContain("Home · Laptop");
    expect(host.textContent).toContain("2/2");
  });

  it("keeps the usage meter's own refresh live behind its memo boundary", async () => {
    await mount();
    usage = [account(65)];
    act(() => document.dispatchEvent(new Event("visibilitychange")));
    await flush();
    expect(renders.get("usage")).toBeGreaterThan(0);
    expect(host.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow")).toBe("65");
    expect(buildTodoTree).not.toHaveBeenCalled();
  });
});
