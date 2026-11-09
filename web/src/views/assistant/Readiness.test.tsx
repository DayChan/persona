// @vitest-environment jsdom
// The main conversation's first screen against the hub's readiness (docs/assistant/onboarding.md 4.4): in `setup` a card with the
// first missing step, the send button saying what to do first and sending nothing (the draft stays), the hub's example
// questions with the local ones opening help; a refused start becomes a card with the original under "Details"; an older
// hub (no `assistant_readiness`) keeps the page as it was.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../../lib/types";
import type { Readiness } from "../../lib/readiness";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  // a mouse (Enter sends, as on a desktop); nothing else matches
  g.matchMedia = (q: string) => ({ matches: /pointer:\s*fine/.test(q), addEventListener() {}, removeEventListener() {} });
});

const fake = vi.hoisted(() => ({ machines: [] as Machine[], features: [] as string[] }));
vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: true, machines: fake.machines, features: fake.features, assistant: null }),
  };
});

const { PersonaAssistant } = await import("../PersonaAssistant");
const { resetReadiness, actionLabel } = await import("../../lib/readiness");
const { getState } = await import("../../lib/store");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const setup: Readiness = {
  version: 1,
  mode: "setup",
  ready: false,
  next: "computer",
  machine: null,
  launcher: "claude",
  steps: [
    { id: "computer", state: "todo", code: "no_machine", title: "添加你的电脑", detail: "Persona 要在你的一台电脑上干活，现在还没接上电脑。", action: { id: "add_computer" } },
    { id: "claude", state: "unknown" },
    { id: "model", state: "unknown" },
    { id: "first_message", state: "unknown" },
  ],
  suggestions: [
    { id: "what_can_you_do", title: "你能帮我做什么？", local: true },
    { id: "how_to_connect_model", title: "怎么接模型？", local: true },
  ],
  dismissed: false,
  checked_at: "2026-11-06T09:00:00Z",
};

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
let readiness: Readiness | null = setup;
let startReply: () => Response = () => json({ session: { machine_id: "m", session_id: "s" } });
const calls: string[] = [];
function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const path = String(input).split("?")[0]!;
  calls.push(`${init?.method ?? "GET"} ${path}`);
  if (path === "/api/v1/assistant/readiness") return Promise.resolve(readiness ? json(readiness) : json({ error: "not found", code: "not_found" }, 404));
  if (path === "/api/v1/assistant/conversations") return Promise.resolve(startReply());
  if (path === "/api/v1/machines/m1/confirm") return Promise.resolve(json({ ok: true }));
  return Promise.resolve(json({ error: "not found", code: "not_found" }, 404));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const render = async () => {
  act(() => root.render(<PersonaAssistant onToggleSidebar={() => {}} />));
  await flush();
};
const textarea = () => host.querySelector("textarea")!;
const type = (text: string) =>
  act(() => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(textarea(), text);
    textarea().dispatchEvent(new Event("input", { bubbles: true }));
  });

beforeEach(() => {
  localStorage.clear();
  resetReadiness();
  readiness = setup;
  fake.features = ["assistant_readiness"];
  fake.machines = [];
  calls.length = 0;
  startReply = () => json({ session: { machine_id: "m", session_id: "s" } });
  vi.stubGlobal("fetch", vi.fn(hub));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  Object.assign(getState(), { features: [], machines: [] });
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("the main conversation's readiness", () => {
  it("shows the first missing step and turns the send button, sending nothing", async () => {
    await render();
    const card = host.querySelector('[data-testid="readiness-card"]');
    expect(card?.textContent).toContain("添加你的电脑");
    expect(card?.textContent).toContain("还没接上电脑");
    const blocked = host.querySelector<HTMLButtonElement>('[data-testid="composer-blocked"]');
    expect(blocked?.textContent).toBe("先添加电脑");
    expect(host.querySelector('[aria-label="发送"]')).toBeNull();

    type("帮我看看日程");
    act(() => textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(calls).not.toContain("POST /api/v1/assistant/conversations");
    expect(textarea().value).toBe("帮我看看日程");
    // not nothing (#4480): a line says why, and the card is nudged
    expect(host.querySelector('[data-testid="composer-nudge"]')?.textContent).toContain("还没发出去：先添加电脑");
    expect(host.querySelector('[data-testid="readiness-card"]')?.getAttribute("data-nudged")).toBe("1");

    // the button opens the step (add a computer: how, in place); the nudge drew it anew, so it is looked up again
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="composer-blocked"]')!.click());
    expect(document.body.textContent).toContain("Persona Link");
  });

  it("answers Enter on an empty box too while blocked", async () => {
    await render();
    act(() => textarea().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(calls).not.toContain("POST /api/v1/assistant/conversations");
    expect(host.querySelector('[data-testid="composer-nudge"]')?.textContent).toContain("先添加电脑");
  });

  it("draws the hub's local questions as help, not messages", async () => {
    await render();
    const q = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("怎么接模型？"));
    expect(q).toBeTruthy();
    act(() => q!.click());
    expect(document.body.textContent).toContain("设置 › 模型供应商");
    // a key alone is not enough today: the computer comes first (#4480)
    const note = document.body.querySelector('[data-testid="help-model-computer"]')?.textContent ?? "";
    expect(note).toContain("只粘贴 API Key、不接电脑，还开始不了");
    expect(note).toContain("先添加一台");
    expect(textarea().value).toBe("");
    expect(host.textContent).not.toContain("今天我有哪些会");
  });

  it("points the model help at a waiting computer, not at adding another", async () => {
    readiness = {
      ...setup,
      steps: [
        { id: "computer", state: "problem", code: "machine_pending", machine: "m1", title: "确认小林的 MacBook", action: { id: "confirm_computer" } },
        ...setup.steps.slice(1),
      ],
    };
    await render();
    const q = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("怎么接模型？"));
    act(() => q!.click());
    const note = document.body.querySelector('[data-testid="help-model-computer"]')?.textContent ?? "";
    expect(note).not.toContain("先添加一台");
    expect(note).toContain("确认小林的 MacBook");
    const buttons = [...document.body.querySelectorAll("button")].map((b) => b.textContent ?? "");
    expect(buttons).not.toContain("添加电脑");
    expect(buttons).toContain(actionLabel("confirm_computer"));
  });

  it("is the page as before on an older hub", async () => {
    fake.features = [];
    readiness = null;
    await render();
    expect(host.querySelector('[data-testid="readiness-card"]')).toBeNull();
    expect(host.querySelector('[data-testid="composer-blocked"]')).toBeNull();
    expect(host.textContent).toContain("今天我有哪些会");
    expect(calls).not.toContain("GET /api/v1/assistant/readiness");
  });

  it("puts a refused start in a card with the original under 详情", async () => {
    readiness = { ...setup, mode: "full", ready: true, next: null, steps: setup.steps.map((s) => ({ id: s.id, state: "ok" })) };
    startReply = () =>
      json({ error: "小林的 MacBook 上还没装 Claude Code。", code: "claude_missing", detail: "claude not found in PATH", action: "install_claude", machine_name: "小林的 MacBook" }, 409);
    await render();
    type("你好");
    await act(async () => host.querySelector<HTMLButtonElement>('[aria-label="发送"]')!.click());
    await flush();
    const card = host.querySelector('[data-testid="readiness-failure"]');
    expect(card?.getAttribute("data-code")).toBe("claude_missing");
    expect(card?.textContent).toContain("还没装 Claude Code");
    expect(card?.textContent).toContain("怎么安装");
    expect(card?.textContent).toContain("详情");
    // the draft is back in the box, not lost
    expect(textarea().value).toBe("你好");
  });

  it("adds a computer in place where the hub lets this device add one", async () => {
    fake.features = ["assistant_readiness", "machine_trust", "machines_add"];
    await render();
    act(() => host.querySelector<HTMLButtonElement>('[data-testid="composer-blocked"]')!.click());
    await flush();
    // the machines page's own flow, not the how-to pointing at the admin page
    expect(document.body.textContent).toContain("这台电脑的系统");
    expect(document.body.textContent).not.toContain("打开管理界面");
  });

  it("asks about the computer waiting to be confirmed in place, then asks for the readiness again", async () => {
    fake.features = ["assistant_readiness", "machine_trust", "machines_add"];
    fake.machines = [{ id: "m1", name: "小林的 MacBook", online: true, trust: "pending" }];
    // what readinessChanged and the confirm button read outside React
    Object.assign(getState(), { features: fake.features, machines: fake.machines });
    readiness = {
      ...setup,
      steps: [{ id: "computer", state: "problem", code: "machine_pending", machine: "m1", title: "确认这台电脑是你的", action: { id: "confirm_computer" } }, ...setup.steps.slice(1)],
    };
    await render();
    const blocked = host.querySelector<HTMLButtonElement>('[data-testid="composer-blocked"]')!;
    expect(blocked.textContent).toBe("先确认电脑");
    act(() => blocked.click());
    await flush();
    expect(document.body.textContent).toContain("有一台电脑想加入：小林的 MacBook");
    const before = calls.filter((c) => c === "GET /api/v1/assistant/readiness").length;
    const yes = [...document.body.querySelectorAll("button")].find((b) => b.textContent === "是我的电脑")!;
    await act(async () => yes.click());
    await flush();
    expect(calls).toContain("POST /api/v1/machines/m1/confirm");
    expect(document.body.textContent).toContain("已添加：小林的 MacBook");
    expect(calls.filter((c) => c === "GET /api/v1/assistant/readiness").length).toBeGreaterThan(before);
  });
});
