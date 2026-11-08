// @vitest-environment jsdom
// A message that did not go stays in the chat as a failed bubble with why, Retry and Delete, the box empty (#1122). A
// refused send in a Persona conversation also gets a card above the composer (docs/assistant/onboarding.md 4.6); the next
// send that goes through takes it away, so it does not linger over a later setup step.
import { act, useEffect, useReducer } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Outgoing } from "./Composer";
import type { Session } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

const captured = vi.hoisted(() => ({
  onSend: undefined as ((out: Outgoing) => Promise<boolean>) | undefined,
  session: { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle" } as Session,
}));

vi.mock("./Composer", async (importOriginal) => ({
  ...await importOriginal<typeof import("./Composer")>(),
  Composer: ({ onSend }: { onSend: (out: Outgoing) => Promise<boolean> }) => {
    captured.onSend = onSend;
    return null;
  },
}));
// one transcript for every render: a new one each time would re-run the sending bubble's echo check forever
const transcript = vi.hoisted(() => ({ items: [], loading: false, error: "", hasMore: false, streaming: "" }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/store", async (importOriginal) => {
  const store = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...store,
    getSession: () => captured.session,
    // drawn again on every change, as the real one is (a failed message is kept in the store's outbox)
    useStore: <T,>(select: (s: ReturnType<typeof store.getState>) => T) => {
      const [, changed] = useReducer((n: number) => n + 1, 0);
      useEffect(() => store.subscribe(changed), []);
      return select({ ...store.getState(), sessions: { hub: [captured.session] } });
    },
  };
});
vi.mock("../../lib/panel", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/panel")>(),
  usePanel: () => null,
}));
vi.mock("../../lib/router", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/router")>(),
  useRoute: () => ({ name: "session", mid: "hub", id: "c1" }),
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));
vi.mock("../assistant/AssistantChat", () => ({
  AssistantFace: () => null,
  AssistantLauncherChip: () => null,
  CompactionStrip: () => null,
  useAssistantMenu: () => ({ items: [], dialogs: null }),
}));
vi.mock("../assistant/Tasks", () => ({
  DayLine: () => null,
  FeedLineView: () => null,
  QuickResultCard: () => null,
  RouteChip: () => null,
  TaskDock: () => null,
  TaskQuestions: () => null,
}));

// the wait for an ultracode change to go through (#3428): the test says when
const ultra = vi.hoisted(() => ({ release: undefined as ((through: boolean) => void) | undefined }));
vi.mock("../../lib/ultracode", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/ultracode")>(),
  ultracodeQueueDone: () => new Promise<boolean>((resolve) => (ultra.release = resolve)),
}));

const { ChatView } = await import("./ChatView");
const { getState, setOutbox } = await import("../../lib/store");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
let reply: () => Response;
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};

beforeEach(() => {
  localStorage.clear();
  for (const key of Object.keys(getState().outbox)) setOutbox(key, []);
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => Promise.resolve(String(input).endsWith("/messages") ? reply() : json({}))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("a refused send's card", () => {
  it("goes away once a later send goes through", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    reply = () => json({ error: "模型没接好", code: "model_missing", action: "connect_model" }, 409);
    await act(async () => void (await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    expect(host.querySelector('[data-testid="readiness-failure"]')).not.toBeNull();
    reply = () => json({});
    await act(async () => void (await captured.onSend!({ text: "再来", images: [] })));
    await flush();
    expect(host.querySelector('[data-testid="readiness-failure"]')).toBeNull();
  });
});

describe("a message that did not go, in an agent's chat (#1122)", () => {
  const failed = () => host.querySelector('[data-user-bubble][data-send-state="failed"]');
  const button = (label: string) => Array.from(host.querySelectorAll("[data-outbox] button")).find((b) => b.textContent === label) as HTMLButtonElement | undefined;
  let bodies: { client_msg_id: string; text: string }[];
  beforeEach(() => {
    bodies = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/messages")) return Promise.resolve(json({}));
      bodies.push(JSON.parse(String(init?.body)));
      return Promise.resolve(reply());
    }));
  });

  it("stays in the chat as a failed bubble with why, Retry and Delete, and the box stays empty", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json({ error: "会话已结束" }, 400);
    let kept: boolean | undefined;
    await act(async () => void (kept = await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    expect(kept).toBe(true); // the composer does not put the words back
    expect(failed()?.textContent).toContain("你好");
    expect(host.querySelector("[data-outbox] [role=alert]")?.textContent).toContain("没发出去：会话已结束");
    expect(button("重试")).toBeDefined();
    expect(button("删除")).toBeDefined();
    expect(localStorage.getItem("draft:hub/c1")).toBeNull();
    expect(host.querySelector('[data-testid="readiness-failure"]')).toBeNull(); // the bubble says why: no card over the box
  });

  it("Retry sends it again, under a new id (the hub keeps its answer to the first), and the bubble waits for the transcript", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json({ error: "会话已结束" }, 400);
    await act(async () => void (await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    reply = () => json({});
    await act(async () => button("重试")!.click());
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["你好", "你好"]);
    expect(bodies[1].client_msg_id).not.toBe(bodies[0].client_msg_id);
    expect(failed()).toBeNull();
    expect(host.querySelector("[data-outbox] [data-user-bubble]")?.getAttribute("data-send-state")).toBe("sent");
  });

  it("Delete takes it away", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json({ error: "会话已结束" }, 400);
    await act(async () => void (await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    act(() => button("删除")!.click());
    await flush();
    expect(host.querySelector("[data-outbox]")).toBeNull();
    expect(bodies).toHaveLength(1);
  });

  it("a send that never went (nothing shows it) gives the words back", async () => {
    captured.session = { ...captured.session, status: "offline", source_kind: "codex_history" } as Session;
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      let went: boolean | undefined;
      await act(async () => void (went = await captured.onSend!({ text: "你好", images: [] })));
      expect(went).toBe(false);
      expect(host.querySelector("[data-outbox]")).toBeNull();
    } finally {
      captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle" } as Session;
    }
  });
});

describe("a message the node held back for an ultracode change (#3428)", () => {
  const HELD = { code: "ultracode_pending", error: "ultracode 等这一轮结束后生效，请稍后再发送消息" };
  let bodies: { client_msg_id: string; text: string }[];
  beforeEach(() => {
    bodies = [];
    ultra.release = undefined;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/messages")) return Promise.resolve(json({}));
      bodies.push(JSON.parse(String(init?.body)));
      return Promise.resolve(reply());
    }));
  });

  it("waits in its bubble saying so, leaves the box free, and goes again with a new id once the change is through", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json(HELD, 409);
    let went: boolean | undefined;
    await act(async () => void (went = await captured.onSend!({ text: "再看看测试", images: [] })));
    await flush();
    expect(went).toBe(true); // the box is free: its stop button too, while the turn runs
    expect(host.textContent).toContain("等 ultracode 的切换在这一轮结束后生效，再发这条");
    expect(host.textContent).toContain("再看看测试");
    expect(host.querySelector('[data-testid="readiness-failure"]')).toBeNull();
    reply = () => json({});
    await act(async () => ultra.release!(true));
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["再看看测试", "再看看测试"]);
    expect(bodies[1].client_msg_id).not.toBe(bodies[0].client_msg_id);
    expect(host.textContent).not.toContain("等 ultracode 的切换");
  });

  it("not through in time: it stays as a failed bubble that says why, the box empty", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json(HELD, 409);
    await act(async () => void (await captured.onSend!({ text: "再看看测试", images: [] })));
    await flush();
    await act(async () => ultra.release!(false));
    await flush();
    expect(bodies).toHaveLength(1);
    expect(localStorage.getItem("draft:hub/c1")).toBeNull();
    expect(host.querySelector('[data-user-bubble][data-send-state="failed"]')?.textContent).toContain("再看看测试");
    expect(host.querySelector("[data-outbox] [role=alert]")?.textContent).toContain("ultracode 等这一轮结束后生效");
  });
});
