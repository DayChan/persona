// @vitest-environment jsdom
// The pane tabs over a chat (PaneTabs) list the agents of its herdr tab. ChatView used to read the machine's whole row
// list for them, so any push for another agent on that machine redrew the open chat (header, composer, cards), even
// where there are no tabs (a task's side panel, a subagent's transcript). It now reads only the tab's own rows.
import { act, Profiler } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});
vi.mock("./Composer", async (importOriginal) => ({ ...await importOriginal<typeof import("./Composer")>(), Composer: () => null }));
const transcript = vi.hoisted(() => ({ items: [], loading: false, error: "", hasMore: false, streaming: "" }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));

import { applyCompleteSessions, flushPushes, getState, reset, upsertSession } from "../../lib/store";
const { ChatView } = await import("./ChatView");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let commits = 0;
beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(new Response("{}", { status: 200 }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  commits = 0;
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.clearAllTimers();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

const row = (id: string, extra: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: id, title: id, cwd: "/code", status: "working", state: "working", ...extra }) as Session;
// s1, s2 and s4: three panes of one tab in space w1; s3: an agent in another space on the same machine
const pane = (id: string, index: number, extra: Partial<Session> = {}) => row(id, { space_id: "w1", slot_id: "t1", pane_index: index, ...extra });
const other = () => row("s3", { space_id: "w2", slot_id: "t9", pane_index: 0 });

// Drain mount work before measuring. Keep unrelated live-status clocks fixed
// during the push sequence, and flush any batched store notifications explicitly.
const settle = async () => {
  for (let i = 0; i < 3; i++) await act(async () => { await vi.advanceTimersByTimeAsync(100); });
};
async function open(rows: Session[]) {
  act(() => applyCompleteSessions(rows));
  act(() => root.render(<Profiler id="chat" onRender={() => commits++}><ChatView mid="mac" sid="s1" inPage /></Profiler>));
  await settle();
}
async function push(sid: string, change: Partial<Session>) {
  act(() => upsertSession({ ...getState().sessions.mac!.find((s) => s.session_id === sid)!, ...change }));
  act(() => flushPushes());
  await act(async () => { await vi.advanceTimersByTimeAsync(0); });
}
const tabs = () => [...host.querySelectorAll('[role="tablist"] [role="tab"]')].map((b) => b.textContent);

describe("ChatView pane tabs", () => {
  it("redraws when the current chat's title changes", async () => {
    await open([row("s1"), row("s2"), other()]);
    const before = commits;
    await push("s1", { title: "renamed current chat" });
    expect(commits).toBeGreaterThan(before);
    expect(host.textContent).toContain("renamed current chat");
  });

  it("does not redraw the chat for another tab's agent on the same machine", async () => {
    await open([pane("s1", 0), pane("s2", 1), other()]);
    expect(tabs()).toHaveLength(2);
    const before = commits;
    for (let i = 0; i < 10; i++) await push("s3", { title: `s3-${i}` });
    expect(commits - before).toBe(0);
  });

  it("does not redraw a chat outside any tab for its machine's other agents", async () => {
    await open([row("s1"), row("s2"), other()]);
    expect(tabs()).toHaveLength(0);
    const before = commits;
    for (let i = 0; i < 10; i++) await push("s2", { title: `s2-${i}` });
    expect(commits - before).toBe(0);
  });

  it("renames a tab when a pane in the same tab changes its title", async () => {
    await open([pane("s1", 0), pane("s2", 1), other()]);
    expect(tabs().some((t) => t?.includes("s2"))).toBe(true);
    await push("s2", { title: "renamed pane" });
    expect(tabs()).toHaveLength(2);
    expect(tabs()[1]).toContain("renamed pane");
  });

  it("drops a pane that goes offline from the tabs", async () => {
    await open([pane("s1", 0), pane("s2", 1), pane("s4", 2), other()]);
    expect(tabs()).toHaveLength(3);
    await push("s2", { status: "offline", state: "offline" });
    const left = tabs();
    expect(left).toHaveLength(2);
    expect(left.some((t) => t?.includes("s2"))).toBe(false);
    expect(left[1]).toContain("s4");
  });
});
