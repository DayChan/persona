// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// The Agents the board sees, as an array that counts every element read; and every render of a card, counted by the one
// t() call a card makes for its menu label.
const live = vi.hoisted(() => ({ sessions: [] as unknown[], reads: 0, cardRenders: 0, listeners: new Set<() => void>() }));
vi.mock("../lib/sessions", async (load) => {
  const { useSyncExternalStore } = await import("react");
  const subscribe = (fn: () => void) => { live.listeners.add(fn); return () => { live.listeners.delete(fn); }; };
  return { ...(await load<typeof import("../lib/sessions")>()), useAgentSessions: () => useSyncExternalStore(subscribe, () => live.sessions) };
});
vi.mock("../i18n", async (load) => {
  const real = await load<typeof import("../i18n")>();
  return { ...real, t: (key: string, vars?: Parameters<typeof real.t>[1]) => { if (key === "workspaceboard.card_menu") live.cardRenders++; return real.t(key, vars); } };
});
import { refresh, reset } from "../lib/store";
import { resetWorkspaceBoardSettingForTest } from "../lib/workspace-board-setting";
import type { BoardCard } from "../lib/workspace-board";
import type { Machine, Session } from "../lib/types";
import { WorkspaceBoard, WorkspaceCard } from "./WorkspaceBoard";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const card: BoardCard = { id: "card", machine_id: "computer", workspace_id: "space", title: "Project", cwd: "/projects/demo", source: "directory", column_id: "review", pinned: true, archived: false };
const draw = (offline = false, disabled = false) => renderToStaticMarkup(<WorkspaceCard card={card} machine={{ id: "computer", name: "Computer", online: !offline }} sessions={[]} disabled={disabled} onOpen={() => {}} onMenu={() => {}} />);
describe("WorkspaceCard", () => {
  it("renders a genuine workspace card without any running Agent", () => { const html = draw(); expect(html).toContain("Project"); expect(html).toContain("/projects/demo"); expect(html).toContain("暂无 Agent"); expect(html).toContain('data-card-id="card"'); expect(html).toContain('draggable="true"'); });
  it("keeps offline, agent-free cards visible and makes readonly cards non-draggable", () => { const html = draw(true, true); expect(html).toContain("Project"); expect(html).toContain("机器离线"); expect(html).toContain('draggable="false"'); expect(html).toContain("disabled"); expect(card.column_id).toBe("review"); });
});

// 60 cards over 3 columns and 3 computers, 10 Agents in each card's directory (written with and without a trailing slash)
const CARDS = 60, PER_CARD = 10;
const machines: Machine[] = [0, 1, 2].map((i) => ({ id: `m${i}`, name: `Computer ${i}`, online: true }));
const cards: BoardCard[] = Array.from({ length: CARDS }, (_, i) => ({ id: `c${i}`, machine_id: `m${i % 3}`, workspace_id: `w${i}`, title: `Project ${i}`, cwd: `/projects/p${i}`, source: "directory", column_id: `col${i % 3}`, pinned: false, archived: false }));
const board = { revision: 1, can_write: true, columns: [0, 1, 2].map((i) => ({ id: `col${i}`, title: `Column ${i}` })), cards };
const workspaces = cards.map((c) => ({ id: c.workspace_id, machine_id: c.machine_id, source: "directory", source_id: c.workspace_id, title: c.title, cwd: c.cwd, online: true, available: true }));
const agents = (): Session[] => Array.from({ length: CARDS * PER_CARD }, (_, i) => ({ machine_id: `m${i % CARDS % 3}`, session_id: `s${i}`, title: `Agent ${i}`, cwd: `/projects/p${i % CARDS}${Math.floor(i / CARDS) % 2 ? "/" : ""}`, status: "idle", state: "idle" }));
const counted = (rows: Session[]) => new Proxy(rows, { get(target, key, receiver) { if (typeof key === "string" && String(Number(key)) === key) live.reads++; return Reflect.get(target, key, receiver); } });
const setAgents = (rows: Session[]) => act(() => { live.sessions = counted(rows); live.listeners.forEach((fn) => fn()); });

const answers: Record<string, unknown> = { "/api/v1/workspace-board/settings": { enabled: true, can_write: true }, "/api/v1/workspace-board": board, "/api/v1/workspaces": { workspaces }, "/api/v1/machines": { machines } };
let host: HTMLDivElement;
let root: Root;
const flush = async () => { for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); }); };
const shownCards = () => host.querySelectorAll("article[data-card-id]").length;
const cardText = (id: string) => host.querySelector(`article[data-card-id="${id}"]`)?.textContent ?? "";
const type = (text: string) => act(() => {
  const input = host.querySelector<HTMLInputElement>('input[aria-label="搜索工作空间"]')!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, text);
  input.dispatchEvent(new Event("input", { bubbles: true }));
});

describe("the workflow board with many cards and Agents", () => {
  beforeEach(() => {
    live.sessions = []; live.reads = 0; live.cardRenders = 0;
    resetWorkspaceBoardSettingForTest();
    vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(answers[new URL(url, "http://x").pathname] ?? {}), { headers: { "Content-Type": "application/json" } }))));
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
    reset();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("reads no Agent and re-renders no kept card on a search keystroke, and each Agent once when they change", async () => {
    await act(() => refresh());
    setAgents(agents());
    act(() => root.render(<WorkspaceBoard />));
    await flush();
    expect(shownCards()).toBe(CARDS);
    expect(cardText("c7")).toContain(`${PER_CARD} 个 Agent`);
    expect(cardText("c7")).toContain("Computer 1");
    expect(cardText("c7")).not.toContain("空间未发现");

    // a keystroke that hides no card: no Agent read (was cards x Agents = 36,000), no card rendered again (was 60)
    live.reads = 0; live.cardRenders = 0;
    type("p");
    expect(shownCards()).toBe(CARDS);
    expect(live.reads).toBe(0);
    expect(live.cardRenders).toBe(0);

    // one that hides most: the cards it keeps do not render again either
    type("project 5");
    expect(shownCards()).toBe(11);
    expect(live.reads).toBe(0);
    expect(live.cardRenders).toBe(0);
    type("");
    expect(shownCards()).toBe(CARDS);
    expect(live.reads).toBe(0);

    // the Agents change: each is read once to index them, and the cards show the new ones
    live.reads = 0;
    const next = agents().filter((s) => !s.cwd.startsWith("/projects/p7") || s.cwd === "/projects/p7");
    setAgents(next.map((s) => s.session_id === "s7" ? { ...s, status: "working", state: "working" } : s));
    expect(live.reads).toBeLessThanOrEqual(next.length);
    expect(live.reads).toBeGreaterThan(0);
    expect(cardText("c7")).toContain(`${PER_CARD / 2} 个 Agent`);
    expect(cardText("c8")).toContain(`${PER_CARD} 个 Agent`);
  });
});
