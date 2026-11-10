// @vitest-environment jsdom
// Right after a send, before the hub says the agent is working (awaiting, #3654), the run bar already says Working…: the
// composer offers Interrupt and Esc stops it too, as during the turn (#2450). With nothing on its way, Esc sends nothing.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item, Session } from "../../lib/types";
import { setQuotes } from "../../lib/drafts";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

const captured = vi.hoisted(() => ({
  session: { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle", state: "idle" } as Session,
}));
// one transcript object for every render, as the hook keeps one (a new one each time re-runs the echo checks forever)
const transcript = vi.hoisted(() => ({ items: [] as Item[], loading: false, error: "", hasMore: false, streaming: "" }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/store", async (importOriginal) => {
  const store = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...store,
    getSession: () => captured.session,
    useStore: <T,>(select: (s: ReturnType<typeof store.getState>) => T) => select({ ...store.getState(), sessions: { hub: [captured.session] } }),
  };
});
vi.mock("../../lib/panel", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/panel")>(), usePanel: () => null }));
vi.mock("../../lib/router", async (importOriginal) => ({ ...await importOriginal<typeof import("../../lib/router")>(), useRoute: () => ({ name: "session", mid: "hub", id: "c1" }) }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));

const { ChatView } = await import("./ChatView");
const { t } = await import("../../i18n");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
let fetcher: ReturnType<typeof vi.fn>;
const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const interrupts = () => fetcher.mock.calls.filter(([url]) => String(url).endsWith("/sessions/hub/c1/interrupt"));
const stopButton = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.interrupt")}"]`);
const stopControl = () => host.querySelector(`button[aria-label="${t("chat-composer.interrupt")}"], button[aria-label="${t("chat-composer.stopping")}"]`);
const esc = () => act(() => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));

beforeEach(() => {
  localStorage.clear();
  setQuotes("hub/c1", []);
  captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle", state: "idle", agent: "claude" } as Session;
  transcript.items = [];
  transcript.loading = false;
  transcript.error = "";
  fetcher = vi.fn(async () => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetcher);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

describe("editing the last Claude prompt", () => {
  const prompt = '> Quoted text\n\nRevise this paragraph';
  const error = "API Error: Opus 5.5's safeguards flagged this message. Details: [frontier_llm]";
  const box = () => host.querySelector<HTMLTextAreaElement>("textarea")!;
  const rewinds = () => fetcher.mock.calls.filter(([url]) => String(url).endsWith("/sessions/hub/c1/rewind"));
  const pressEsc = () => act(() => void box().dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
  const mountError = async (assistant = false) => {
    transcript.items = [
      { id: "u1#0", seq: 1, role: "user", kind: "text", text: "Earlier input", ts: "2020-01-01T00:00:00Z" },
      { id: "a1#0", seq: 2, role: "assistant", kind: "text", text: "Earlier reply" },
      { id: "u2#0", seq: 3, role: "user", kind: "text", text: prompt, ts: "2020-01-01T00:01:00Z" },
      { id: "a2#0", seq: 4, role: "assistant", kind: "text", text: error },
    ];
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />));
    await flush();
  };
  const editButton = () => [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("rewind.edit_last"));

  it("rewinds exactly once and restores quotes and text without sending automatically", async () => {
    let finish!: () => void;
    fetcher.mockImplementation(async (url) => {
      if (String(url).endsWith("/rewind")) {
        await new Promise<void>((resolve) => { finish = resolve; });
        return new Response(JSON.stringify({ prompt, restore: "conversation", input_cleared: true, code_restored: false }));
      }
      return new Response("{}");
    });
    await mountError();
    pressEsc();
    expect(rewinds()).toHaveLength(0);
    pressEsc();
    await flush();
    expect(rewinds()).toHaveLength(1);
    expect(JSON.parse(rewinds()[0][1].body)).toEqual({ prompt, occurrence: 1, prompts_after: 0, restore: "conversation" });
    expect(box().readOnly).toBe(true);
    pressEsc(); pressEsc();
    expect(rewinds()).toHaveLength(1);
    await act(async () => finish());
    await flush();
    expect(box().readOnly).toBe(false);
    expect(box().value).toBe("Revise this paragraph");
    expect(host.textContent).toContain("Quoted text");
    expect(fetcher.mock.calls.some(([url]) => String(url).endsWith("/messages"))).toBe(false);
    expect(interrupts()).toHaveLength(0);
  });

  it("offers the same recovery on the error card", async () => {
    fetcher.mockResolvedValue(new Response(JSON.stringify({ prompt, restore: "conversation", input_cleared: true, code_restored: false })));
    await mountError();
    expect(editButton()).toBeDefined();
    await act(async () => editButton()!.click());
    await flush();
    expect(rewinds()).toHaveLength(1);
    expect(box().value).toBe("Revise this paragraph");
  });

  it.each(["rewind_mismatch", "input_draft"])("keeps history and the composer unchanged when the node refuses: %s", async (code) => {
    fetcher.mockImplementation(async (url) => String(url).endsWith("/rewind")
      ? new Response(JSON.stringify({ error: "Cannot rewind", code }), { status: 409 })
      : new Response("{}"));
    await mountError();
    pressEsc(); pressEsc(); await flush();
    expect(rewinds()).toHaveLength(1);
    expect(box().value).toBe("");
    expect(box().readOnly).toBe(false);
    expect(host.textContent).toContain("Revise this paragraph");
  });

  it("does not duplicate a prompt the node could not clear from Claude's input", async () => {
    fetcher.mockResolvedValue(new Response(JSON.stringify({ prompt, restore: "conversation", input_cleared: false, code_restored: false })));
    await mountError();
    pressEsc(); pressEsc(); await flush();
    expect(box().value).toBe("");
  });

  it.each(["codex", "working", "offline", "needs_you", "assistant"] as const)("does not expose the shortcut in an unsupported state: %s", async (kind) => {
    if (kind === "codex") captured.session.agent = "codex";
    else if (kind !== "assistant") captured.session.state = kind;
    await mountError(kind === "assistant");
    expect(editButton()).toBeUndefined();
    pressEsc(); pressEsc(); await flush();
    expect(rewinds()).toHaveLength(0);
  });
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("a message waiting for its turn to start (#2450)", () => {
  it("can be interrupted: the composer shows Interrupt, Esc sends it, and the wait ends there", async () => {
    transcript.items = [{ id: "u1", seq: 1, role: "user", kind: "text", text: "Write a status line", ts: new Date().toISOString() }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(stopButton()).not.toBeNull();
    await esc();
    await flush();
    expect(interrupts()).toHaveLength(1);
    expect(interrupts()[0][1]).toMatchObject({ method: "POST" });
    // the hub took it: the wait ends now instead of running out, so no stop button (nor its "Stopping" form) stays
    expect(stopControl()).toBeNull();
  });

  it("with nothing on its way, Esc sends nothing and there is no Interrupt", async () => {
    transcript.items = [
      { id: "u1", seq: 1, role: "user", kind: "text", text: "Write a status line", ts: new Date(Date.now() - 60_000).toISOString() },
      { id: "a1", seq: 2, role: "assistant", kind: "text", text: "Done.", ts: new Date().toISOString() },
    ];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(stopButton()).toBeNull();
    await esc();
    await flush();
    expect(interrupts()).toHaveLength(0);
  });
});
