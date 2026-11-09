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
  const old = root;
  if (old) act(() => old.unmount());
  const r = createRoot(host);
  root = r;
  act(() => r.render(<Header session={session} title="Demo" machineName="Box" siblings={[]} onInterrupt={() => {}} onStopLoop={() => {}} {...props} />));
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

describe("the ⋯ menu's independent review entry", () => {
  it("starts a review and toasts that the findings will come back as a card", async () => {
    const calls = hub();
    render(agent());
    openMenu();
    const item = row(t("chat-chatview.review_work"))!;
    expect(item.disabled).toBe(false);
    act(() => item.click());
    await act(async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); });
    expect(calls).toEqual([{ method: "POST", url: "/api/v1/machines/m/sessions/s1/review" }]);
    expect(toasts).toEqual([[t("chat-agent-review.started"), "info"]]);
  });

  it("toasts the hub's own words when it refuses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({ error: "这个 Agent 正在忙", code: "session_busy" }), { status: 409 }))));
    render(agent());
    openMenu();
    act(() => row(t("chat-chatview.review_work"))!.click());
    await act(async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); });
    expect(toasts).toEqual([["这个 Agent 正在忙", "error"]]);
  });

  it("stays, disabled with its hint, while the agent works", () => {
    render(agent({ status: "working" }));
    openMenu();
    const item = row(t("chat-chatview.review_work"))!;
    expect(item.disabled).toBe(true);
    expect(item.textContent).toContain(t("chat-agent-review.busy"));
  });

  it("is only where the hub starts independent reviews: not without the feature, not on the assistant's chat", () => {
    render(agent());
    openMenu();
    expect(row(t("chat-chatview.review_work"))).toBeTruthy();
    expect(row(t("chat-chatview.review_changes"))).toBeUndefined(); // no git_review feature either: only the new entry is the question

    Object.assign(getState(), { features: [] });
    render(agent());
    openMenu();
    expect(row(t("chat-chatview.review_work"))).toBeUndefined();

    Object.assign(getState(), { features: ["agent_review"] });
    render(agent(), { assistant: true });
    openMenu();
    expect(row(t("chat-chatview.review_work"))).toBeUndefined();
  });
});
