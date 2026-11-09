// @vitest-environment jsdom
// The ⋯ menu's "Review this work" (#1808): an entry only where the hub can start independent reviews (the agent_review
// feature) and only on an agent's own chat; it starts one and toasts, and stays disabled with its hint while the agent
// is busy.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// jsdom has no matchMedia (the header reads the phone-width query)
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({
    matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});
const toasts = vi.hoisted(() => [] as unknown[][]);
vi.mock("../../ui/atoms", async (orig) => ({
  ...(await orig<typeof import("../../ui/atoms")>()),
  toast: vi.fn((...args: unknown[]) => toasts.push(args)),
}));

import { Header } from "./ChatView";
import { t } from "../../i18n";
import { getState, reset } from "../../lib/store";
import type { Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const agent = (o: Partial<Session> = {}): Session => ({ machine_id: "m", session_id: "s1", title: "Demo", cwd: "/code", status: "idle", ...o });
let host: HTMLDivElement;
let root: Root | null = null;

beforeEach(() => {
  toasts.length = 0;
  reset();
  Object.assign(getState(), { features: ["agent_review"], sessions: { m: [agent()] } });
  host = document.createElement("div");
  document.body.append(host);
});

afterEach(() => {
  const live = root;
  if (live) act(() => live.unmount());
  root = null;
  host.remove();
  vi.unstubAllGlobals();
});

const render = (session: Session, props: Record<string, unknown> = {}) => {
  if (root) act(() => root!.unmount());
  root = createRoot(host);
  rerender(session, props);
};
const rerender = (session: Session, props: Record<string, unknown> = {}) => {
  act(() => root!.render(<Header session={session} title="Demo" machineName="Box" siblings={[]} onInterrupt={() => {}} onStopLoop={() => {}} {...props} />));
};
const openMenu = () => act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more")}"]`)!.click());
/** Every ⋯ row by its label text, disabled as the menu says. */
const rows = () => [...document.querySelectorAll<HTMLButtonElement>('[role="menu"] [role="menuitem"], [role="menu"] [role="menuitemradio"]')];
const row = (label: string) => rows().find((r) => r.textContent?.includes(label));

/** A hub that answers every request with `{}` and records it. */
function hub() {
  const calls: { method: string; url: string }[] = [];
  vi.stubGlobal("fetch", vi.fn().mockImplementation((url: string, init?: RequestInit) => {
    calls.push({ method: init?.method ?? "GET", url });
    return Promise.resolve(new Response("{}", { status: 200 }));
  }));
  return calls;
}

const settle = async () => { await act(async () => { for (let i = 0; i < 15; i++) await Promise.resolve(); }); };
const answer = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const item = () => row(t("chat-chatview.review_work"))!;

describe("the ⋯ menu's independent review entry", () => {
  it("disables a clean branch with a localized reason, and enables changed or truncated results", async () => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(answer({ files: [] }))
      .mockResolvedValueOnce(answer({ files: [{ path: "a" }] }))
      .mockResolvedValueOnce(answer({ files: [], truncated: true }));
    vi.stubGlobal("fetch", fetch);
    render(agent());
    openMenu();
    await settle();
    expect(item().disabled).toBe(true);
    expect(item().textContent).toContain(t("chat-agent-review.no_changes"));
    openMenu();
    openMenu();
    await settle();
    expect(item().disabled).toBe(false);
    openMenu();
    openMenu();
    await settle();
    expect(item().disabled).toBe(false);
    expect(fetch).toHaveBeenCalledTimes(3);
  });

  it.each(["no_default_branch", "no_merge_base"])("falls back to uncommitted for %s", async (code) => {
    const fetch = vi.fn()
      .mockResolvedValueOnce(answer({ code }, 409))
      .mockResolvedValueOnce(answer({ files: [] }))
      .mockResolvedValueOnce(answer({ code }, 409))
      .mockResolvedValueOnce(answer({ files: [{ path: "a" }] }));
    vi.stubGlobal("fetch", fetch);
    render(agent());
    openMenu();
    await settle();
    expect(item().disabled).toBe(true);
    expect(fetch.mock.calls.map(([url]) => url)).toEqual([
      "/api/v1/machines/m/git/review?cwd=%2Fcode&scope=branch",
      "/api/v1/machines/m/git/review?cwd=%2Fcode&scope=uncommitted",
    ]);
    openMenu();
    openMenu();
    await settle();
    expect(item().disabled).toBe(false);
  });

  it("leaves errors and malformed responses unknown", async () => {
    const fetch = vi.fn().mockResolvedValueOnce(answer({ code: "not_git" }, 409)).mockResolvedValueOnce(answer({})).mockResolvedValueOnce(answer({ code: "no_merge_base" }, 409)).mockResolvedValueOnce(answer({ code: "unavailable" }, 503));
    vi.stubGlobal("fetch", fetch);
    render(agent());
    openMenu();
    await settle();
    expect(item().disabled).toBe(false);
    openMenu();
    openMenu();
    await settle();
    expect(item().disabled).toBe(false);
    openMenu();
    openMenu();
    await settle();
    expect(item().disabled).toBe(false);
    expect(fetch).toHaveBeenCalledTimes(4);
  });

  it("checks the workspace directory when present and falls back to the session directory", async () => {
    const calls = hub();
    render(agent({ workspace_cwd: "/workspace" }));
    openMenu();
    await settle();
    expect(calls[0].url).toBe("/api/v1/machines/m/git/review?cwd=%2Fworkspace&scope=branch");
    rerender(agent({ workspace_cwd: "" }));
    await settle();
    expect(calls[1].url).toBe("/api/v1/machines/m/git/review?cwd=%2Fcode&scope=branch");
  });

  it("discards old responses when switching sessions or reopening the menu", async () => {
    const pending: ((res: Response) => void)[] = [];
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => new Promise<Response>((resolve) => pending.push(resolve))));
    render(agent());
    openMenu();
    expect(pending).toHaveLength(1);
    expect(item().disabled).toBe(true);
    expect(item().textContent).toContain(t("chat-agent-review.checking_changes"));
    rerender(agent({ session_id: "s2", cwd: "/other" }));
    expect(pending).toHaveLength(2);
    await act(async () => { pending[1](answer({ files: [{ path: "new" }] })); });
    await settle();
    expect(item().disabled).toBe(false);
    await act(async () => { pending[0](answer({ files: [] })); });
    await settle();
    expect(item().disabled).toBe(false);
    openMenu();
    openMenu();
    expect(pending).toHaveLength(3);
    await act(async () => { pending[2](answer({ files: [] })); });
    await settle();
    expect(item().disabled).toBe(true);
    openMenu();
    openMenu();
    expect(item().disabled).toBe(true);
    expect(item().textContent).toContain(t("chat-agent-review.checking_changes"));
    expect(pending).toHaveLength(4);
    await act(async () => { pending[3](answer({ files: [{ path: "new" }] })); });
    await settle();
    expect(item().disabled).toBe(false);
  });
  it("starts a review and toasts that the findings will come back as a card", async () => {
    const calls = hub();
    render(agent());
    openMenu();
    await settle();
    const item = row(t("chat-chatview.review_work"))!;
    expect(item.disabled).toBe(false);
    act(() => item.click());
    await act(async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); });
    expect(calls).toEqual([
      { method: "GET", url: "/api/v1/machines/m/git/review?cwd=%2Fcode&scope=branch" },
      { method: "POST", url: "/api/v1/machines/m/sessions/s1/review" },
    ]);
    expect(toasts).toEqual([[t("chat-agent-review.started"), "info"]]);
  });

  it("toasts the hub's own words when it refuses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ error: "这个 Agent 正在忙", code: "session_busy" }), { status: 409 }))));
    render(agent());
    openMenu();
    await settle();
    act(() => row(t("chat-chatview.review_work"))!.click());
    await act(async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); });
    expect(toasts).toEqual([["这个 Agent 正在忙", "error"]]);
  });

  it("stays, disabled with its hint, while the agent works", () => {
    const calls = hub();
    render(agent({ status: "working" }));
    openMenu();
    const item = row(t("chat-chatview.review_work"))!;
    expect(item.disabled).toBe(true);
    expect(item.textContent).toContain(t("chat-agent-review.busy"));
    expect(calls).toEqual([]);
  });

  it("is only where the hub starts independent reviews: not without the feature, not on the assistant's chat", () => {
    const calls = hub();
    render(agent());
    openMenu();
    expect(row(t("chat-chatview.review_work"))).toBeTruthy();
    expect(row(t("chat-chatview.review_changes"))).toBeUndefined(); // no git_review feature either: only the new entry is the question
    expect(calls.filter((call) => call.url.includes("/git/review"))).toHaveLength(1);

    Object.assign(getState(), { features: [] });
    render(agent());
    openMenu();
    expect(row(t("chat-chatview.review_work"))).toBeUndefined();

    Object.assign(getState(), { features: ["agent_review"] });
    render(agent(), { assistant: true });
    openMenu();
    expect(row(t("chat-chatview.review_work"))).toBeUndefined();
    expect(calls.filter((call) => call.url.includes("/git/review"))).toHaveLength(1);
  });
});
