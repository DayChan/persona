// @vitest-environment jsdom
// A burst of hub pushes renders the page once per animation frame, not once per push; what the user does still
// renders at once, and a pending card or a machines list the hub sends again unchanged changes nothing.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyMachine, connect, endInteraction, getState, refresh, reset, savePins, useStore } from "./store";
import { NO_PINS } from "./pins";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

class FakeSocket {
  static OPEN = 1;
  static all: FakeSocket[] = [];
  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  send() {}
  close() {
    this.readyState = 3;
  }
  push(msg: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(msg) });
  }
}

// the animation frames: run by hand
let frames: (FrameRequestCallback | null)[] = [];
const runFrame = () =>
  act(() => {
    const due = frames;
    frames = [];
    due.forEach((f) => f?.(0));
  });

let host: HTMLDivElement;
let root: Root;
let renders = 0;
let seen = "";

function Probe() {
  const machines = useStore((s) => s.machines.length);
  const sessions = useStore((s) => (s.sessions.m ?? []).map((x) => x.last_text).join(","));
  const cards = useStore((s) => s.interactions.map((i) => i.id).join(","));
  const pins = useStore((s) => s.pins.spaces.length);
  const convs = useStore((s) => s.assistant?.conversations.map((c) => c.last_text).join(",") ?? "");
  renders++;
  seen = `${machines}|${sessions}|${cards}|${pins}|${convs}`;
  return null;
}

const session = (id: string, last_text: string) => ({ machine_id: "m", session_id: id, state: "working", last_text });
const card = (id: string, question = "发吗？") => ({ id, machine_id: "m", session_id: "a", kind: "question", questions: [{ question, options: [{ label: "发" }] }] });

function online() {
  connect();
  const s = FakeSocket.all[FakeSocket.all.length - 1];
  s.readyState = 1;
  s.onopen?.();
  s.push({ type: "hello", server_version: "v1", event_stream: "s1", event_seq: 1, resumed: true, features: ["sessions_delta"] });
  runFrame();
  return s;
}

beforeEach(() => {
  reset();
  FakeSocket.all = [];
  frames = [];
  vi.stubGlobal("WebSocket", FakeSocket);
  vi.stubGlobal("requestAnimationFrame", (f: FrameRequestCallback) => frames.push(f));
  vi.stubGlobal("cancelAnimationFrame", (id: number) => {
    frames[id - 1] = null;
  });
  vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response("{}", { status: 200 }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Probe />));
  renders = 0;
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("hub pushes render once per frame", () => {
  it("a burst is in the state at once and on the page once, at the next frame", () => {
    const s = online();
    renders = 0;
    act(() => {
      s.push({ type: "machines", machines: [{ id: "m", name: "M", online: true }] });
      s.push({ type: "sessions", machine_id: "m", sessions: [session("a", "一"), session("b", "二")] });
      s.push({ type: "sessions_delta", machine_id: "m", upsert: [session("a", "三")], remove: ["b"] });
      s.push({ type: "interaction", interaction: card("ix1") });
      s.push({ type: "interaction", interaction: card("ix2") });
    });
    // the code after a push reads the new state
    expect(getState().machines).toHaveLength(1);
    expect(getState().sessions.m.map((x) => x.last_text)).toEqual(["三"]);
    expect(renders).toBe(0);
    runFrame();
    expect(renders).toBe(1);
    expect(seen).toBe("1|三|ix1,ix2|0|");
    runFrame();
    expect(renders).toBe(1);
  });

  it("the assistant's copy of its conversations changes in the same update as the sessions", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string) =>
      Promise.resolve(new Response(JSON.stringify(url.includes("/assistant") ? { machine_id: "m", conversations: [session("a", "旧")] } : {}), { status: 200 }))));
    await act(() => refresh());
    const s = online();
    renders = 0;
    act(() => s.push({ type: "sessions_delta", machine_id: "m", upsert: [session("a", "新")], remove: [] }));
    expect(getState().assistant?.conversations[0].last_text).toBe("新");
    runFrame();
    expect(renders).toBe(1);
    expect(seen).toBe("0|新||0|新");
  });

  it("what the user does (an optimistic pin) renders at once, and takes a pending push along", () => {
    const s = online();
    renders = 0;
    act(() => void savePins({ ...NO_PINS, spaces: ["m/x"] }));
    expect(renders).toBe(1);
    expect(seen).toBe("0|||1|");

    act(() => s.push({ type: "interaction", interaction: card("ix1") }));
    expect(renders).toBe(1);
    act(() => endInteraction("ix0")); // not one the page has: changes nothing
    act(() => void savePins(NO_PINS));
    expect(renders).toBe(2);
    expect(seen).toBe("0||ix1|0|");
    runFrame(); // the frame it had asked for has nothing left to tell
    expect(renders).toBe(2);
  });

  it("a hidden tab runs no frames: the views hear of the pushes in the end anyway", () => {
    const s = online();
    vi.useFakeTimers();
    renders = 0;
    act(() => s.push({ type: "interaction", interaction: card("ix1") }));
    expect(renders).toBe(0);
    act(() => vi.advanceTimersByTime(150));
    expect(renders).toBe(1);
    expect(seen).toBe("0||ix1|0|");
  });
});

describe("a pending card sent again", () => {
  it("unchanged: nothing changes, it keeps its place", () => {
    const s = online();
    act(() => {
      s.push({ type: "interaction", interaction: card("ix1") });
      s.push({ type: "interaction", interaction: card("ix2") });
    });
    runFrame();
    const before = getState().interactions;
    renders = 0;
    act(() => s.push({ type: "interaction", interaction: card("ix1") }));
    runFrame();
    expect(getState().interactions).toBe(before);
    expect(renders).toBe(0);
  });

  it("changed: it takes the new one, as before", () => {
    const s = online();
    act(() => {
      s.push({ type: "interaction", interaction: card("ix1") });
      s.push({ type: "interaction", interaction: card("ix2") });
    });
    act(() => s.push({ type: "interaction", interaction: card("ix1", "现在发吗？") }));
    runFrame();
    expect(getState().interactions.map((i) => [i.id, i.kind === "question" && i.questions?.[0].question])).toEqual([
      ["ix2", "发吗？"],
      ["ix1", "现在发吗？"],
    ]);
  });
});

describe("a machines list sent again", () => {
  // what the sidebar, the Machines page and the palette read: the list itself
  let listRenders = 0;
  function MachinesProbe() {
    useStore((s) => s.machines);
    listRenders++;
    return null;
  }
  const machine = (id: string, online = true) => ({ id, name: id.toUpperCase(), online, engine: "herdr", system: { cores: 8 } });
  const answering = (machines: unknown[]) =>
    vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string) =>
      Promise.resolve(new Response(JSON.stringify(url.includes("/machines") ? { machines } : {}), { status: 200 }))));

  beforeEach(() => {
    act(() => root.render(<MachinesProbe />));
    listRenders = 0;
  });

  it("pushed unchanged: the page keeps its list and does not render", () => {
    const s = online();
    act(() => s.push({ type: "machines", machines: [machine("a"), machine("b")] }));
    runFrame();
    const before = getState().machines;
    listRenders = 0;
    act(() => s.push({ type: "machines", machines: [machine("a"), machine("b")] }));
    runFrame();
    expect(listRenders).toBe(0);
    expect(getState().machines).toBe(before);
  });

  it("answered unchanged by GET /machines, or pushed as the answer was: no render", async () => {
    answering([machine("a"), machine("b")]);
    await act(() => refresh());
    const before = getState().machines;
    expect(before.map((m) => m.id)).toEqual(["a", "b"]);
    listRenders = 0;
    await act(() => refresh());
    expect(listRenders).toBe(0);
    expect(getState().machines).toBe(before);
    const s = online();
    act(() => s.push({ type: "machines", machines: [machine("a"), machine("b")] }));
    runFrame();
    expect(getState().machines).toBe(before);
    expect(listRenders).toBe(0);
  });

  it("applyMachine() with what the list already says: no render", () => {
    const s = online();
    act(() => s.push({ type: "machines", machines: [machine("a"), machine("b")] }));
    runFrame();
    const before = getState().machines;
    listRenders = 0;
    act(() => applyMachine(machine("a")));
    expect(listRenders).toBe(0);
    expect(getState().machines).toBe(before);
  });

  it("one machine changed: one render, and the other keeps its object", () => {
    const s = online();
    act(() => s.push({ type: "machines", machines: [machine("a"), machine("b")] }));
    runFrame();
    const [a, b] = getState().machines;
    listRenders = 0;
    act(() => s.push({ type: "machines", machines: [machine("a", false), machine("b")] }));
    runFrame();
    expect(listRenders).toBe(1);
    expect(getState().machines[0]).not.toBe(a);
    expect(getState().machines[0].online).toBe(false);
    expect(getState().machines[1]).toBe(b);
  });

  it("reordered, added or removed: a new list, each machine still there keeps its object", () => {
    const s = online();
    act(() => s.push({ type: "machines", machines: [machine("a"), machine("b")] }));
    runFrame();
    const [a, b] = getState().machines;
    listRenders = 0;
    act(() => s.push({ type: "machines", machines: [machine("b"), machine("a")] }));
    runFrame();
    expect(listRenders).toBe(1);
    expect(getState().machines[0]).toBe(b);
    expect(getState().machines[1]).toBe(a);
    act(() => s.push({ type: "machines", machines: [machine("b"), machine("a"), machine("c")] }));
    runFrame();
    expect(listRenders).toBe(2);
    expect(getState().machines.map((m) => m.id)).toEqual(["b", "a", "c"]);
    act(() => s.push({ type: "machines", machines: [machine("b")] }));
    runFrame();
    expect(listRenders).toBe(3);
    expect(getState().machines).toHaveLength(1);
    expect(getState().machines[0]).toBe(b);
  });
});
