// @vitest-environment jsdom
// The Enter that picks an input-method candidate belongs to the input method, not the box (#2733): creating a todo
// in "Link to-do" and adding a blocked Claude config directory wait for the composition to end. Safari sends that Enter with
// isComposing false and keyCode 229 (#4667): "Search in chat", a question card's own answer, a plan's "Send back" reason and the
// Agents page composer ignore it too, and so do the code views' find-in-file and composer and the provider wizard's
// model ID (#4937).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { loadPages } from "../lib/lazy-page";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const apiOp = vi.hoisted(() => vi.fn(async (_path: string, _init?: unknown) => ({})));
vi.mock("../lib/ops", () => ({ apiOp: (path: string, init?: unknown) => apiOp(path, init) }));
import { getState } from "../lib/store";
import type { Interaction, Item, Session } from "../lib/types";
import { AgentComposer } from "./agents/AgentsView";
import { ChatSearch } from "./chat/ChatSearch";
import { InteractionCard } from "./chat/Interactions";
import { LinkTodoDialog } from "./LinkTodoDialog";
import { ClaudeDirs } from "./settings/ClaudeDirs";
import { ProviderWizard } from "./settings/ProviderWizard";
import { INERT, ViewSections } from "./extensions/ViewBody";
import { SHOWCASE } from "../showcase/snapshots";

const createTodo = vi.fn(async (title: string) => ({ id: "t1", title }));
vi.mock("../lib/todos", async (orig) => ({
  ...(await orig<typeof import("../lib/todos")>()),
  createTodo: (title: string) => createTodo(title),
  linkAgentToTodo: vi.fn(async () => {}),
}));

const api = vi.fn(async (path: string, _init?: unknown) =>
  path.endsWith("/blocked") ? { dirs: [], blocked: ["~/.work"], home: "/home/me" } : { dirs: [], blocked: [], home: "/home/me" },
);
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  apiOp.mockClear();
  createTodo.mockClear();
  api.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const type = (input: HTMLInputElement, value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });

const press = (input: HTMLElement, key: string, isComposing = false) =>
  act(async () => {
    input.dispatchEvent(new KeyboardEvent("keydown", { key, isComposing, bubbles: true, cancelable: true }));
  });

// Safari's keydown for the Enter that picks a candidate: isComposing is false, keyCode is 229
const pressSafariIme = (input: HTMLElement) =>
  act(async () => {
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", keyCode: 229, isComposing: false, bubbles: true, cancelable: true }));
  });

const typeArea = (area: HTMLTextAreaElement, value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, value);
    area.dispatchEvent(new Event("input", { bubbles: true }));
  });

const session = { machine_id: "m1", session_id: "s1" } as Session;

describe("LinkTodoDialog's new todo", () => {
  const openCreate = async () => {
    await act(async () => root.render(<LinkTodoDialog open session={session} onClose={() => {}} />));
    const button = [...document.querySelectorAll("button")].find((b) => b.textContent?.includes("新建待办"))!;
    act(() => button.click());
    return document.querySelector<HTMLInputElement>('input[type="text"]')!;
  };

  it("is not created by the Enter that confirms a composition", async () => {
    const input = await openCreate();
    type(input, "会議");
    await press(input, "Enter", true);
    expect(createTodo).not.toHaveBeenCalled();
    await press(input, "Escape", true);
    expect(document.querySelector('input[type="text"]')).toBe(input); // the row stays open
  });

  it("is created by a plain Enter", async () => {
    const input = await openCreate();
    type(input, " 会議の準備 ");
    await press(input, "Enter");
    expect(createTodo).toHaveBeenCalledExactlyOnceWith("会議の準備");
  });
});

describe("ClaudeDirs' typed rule", () => {
  const blockedPuts = () => api.mock.calls.filter(([path]) => path.endsWith("/blocked"));

  const renderInput = async () => {
    await act(async () => root.render(<ClaudeDirs mid="m1" name="mac" />));
    return host.querySelector<HTMLInputElement>("input")!;
  };

  it("is not added by the Enter that confirms a composition", async () => {
    const input = await renderInput();
    type(input, "~/.work");
    await press(input, "Enter", true);
    expect(blockedPuts()).toHaveLength(0);
  });

  it("is added by a plain Enter", async () => {
    const input = await renderInput();
    type(input, "~/.work");
    await press(input, "Enter");
    expect(blockedPuts()).toHaveLength(1);
  });
});

describe("Safari's candidate Enter (isComposing false, keyCode 229)", () => {
  it("does not pick a 会话内搜索 hit", async () => {
    vi.useFakeTimers();
    try {
      const items: Item[] = [{ id: "u#0", seq: 1, role: "user", kind: "text", text: "look at the parser" }];
      const onPick = vi.fn();
      act(() => root.render(<ChatSearch open query="parser" onQuery={() => {}} items={items} rounds={1} hasMore={false} onLoadEarlier={async () => null} onPick={onPick} onClose={() => {}} />));
      act(() => vi.advanceTimersByTime(250));
      const input = document.querySelector<HTMLInputElement>("[data-chat-search] input")!;
      await pressSafariIme(input);
      expect(onPick).not.toHaveBeenCalled();
      await press(input, "Enter");
      expect(onPick).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });

  const pending = (ix: Interaction) => {
    Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
    act(() => root.render(<InteractionCard ix={ix} />));
  };
  const resolves = () => apiOp.mock.calls.filter(([path]) => path.endsWith("/resolve"));

  it("does not send a question card's own answer", async () => {
    pending({ id: "ix_q", machine_id: "m", session_id: "s", kind: "question", questions: [{ question: "叫什么？", options: [{ label: "A" }] }] });
    const input = host.querySelector<HTMLInputElement>('input[aria-label="自定义回答"]')!;
    type(input, "会議");
    await pressSafariIme(input);
    expect(resolves()).toHaveLength(0);
    await press(input, "Enter");
    expect(resolves()).toHaveLength(1);
  });

  it("does not reject a plan with a half-typed reason", async () => {
    pending({ id: "ix_p", machine_id: "m", session_id: "s", kind: "plan", plan: "改一下" });
    const open = [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === "打回修改")!;
    act(() => open.click());
    const input = host.querySelector<HTMLInputElement>('input[placeholder="要怎么改…"]')!;
    type(input, "会議");
    await pressSafariIme(input);
    expect(resolves()).toHaveLength(0);
    await press(input, "Enter");
    expect(resolves()).toHaveLength(1);
  });

  it("does not send from the Agents page composer", async () => {
    const onSend = vi.fn();
    act(() => root.render(<AgentComposer disabled={false} reason="" onSend={onSend} />));
    const area = host.querySelector("textarea")!;
    typeArea(area, "会議");
    await pressSafariIme(area);
    expect(onSend).not.toHaveBeenCalled();
    await press(area, "Enter");
    expect(onSend).toHaveBeenCalledExactlyOnceWith("会議");
  });
});

describe("Safari's candidate keys in the code views and the provider wizard (#4937)", () => {
  // the code views are their own chunk (lib/lazy-page.ts, #1794): in before they are drawn
  beforeAll(() => loadPages(), 60_000);
  const safari = (el: HTMLElement, key: string, init: KeyboardEventInit = {}) =>
    act(async () => {
      el.dispatchEvent(new KeyboardEvent("keydown", { key, keyCode: 229, isComposing: false, bubbles: true, cancelable: true, ...init }));
    });
  const plain = (el: HTMLElement, key: string, init: KeyboardEventInit = {}) =>
    act(async () => {
      el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...init }));
    });

  it("does not move to the next hit or close find-in-file", async () => {
    act(() => root.render(<ViewSections view={SHOWCASE["code-browse"]} {...INERT} />));
    const card = host.querySelector(".code-hl")!.parentElement!;
    act(() => card.querySelector<HTMLButtonElement>('[aria-label="查找"]')!.click());
    const input = card.querySelector("input")!;
    type(input, "retry");
    const count = () => card.textContent!.match(/(\d+) \/ \d+/)![1];
    expect(count()).toBe("1");
    await safari(input, "Enter");
    expect(count()).toBe("1");
    await safari(input, "Escape");
    expect(card.querySelector("input")).toBe(input); // still open
    await plain(input, "Enter");
    expect(count()).toBe("2");
  });

  it("does not send the code composer with ⌘/Ctrl+Enter", async () => {
    const sent: string[] = [];
    act(() => root.render(<ViewSections view={SHOWCASE["code-overview"]} {...INERT} onAct={(a) => sent.push(a.id)} />));
    const area = host.querySelector("textarea")!;
    typeArea(area, "LGTM");
    await safari(area, "Enter", { ctrlKey: true });
    expect(sent).toEqual([]);
    await plain(area, "Enter", { ctrlKey: true });
    expect(sent).toHaveLength(1);
  });

  it("does not add a half-typed model ID", async () => {
    const existing = { name: "gw", base_url: "https://gateway.example.com", has_key: true, model: "m1", models: [{ id: "m1" }] };
    act(() => root.render(<ProviderWizard existing={existing} onClose={() => {}} onSaved={() => {}} />));
    act(() => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.includes("下一步"))!.click());
    const input = document.body.querySelector<HTMLInputElement>('input[aria-label="手动添加模型 ID"]')!;
    const rows = () => document.body.querySelectorAll('ul[aria-label="模型"] > li').length;
    expect(rows()).toBe(1);
    type(input, "glm");
    await safari(input, "Enter");
    expect(rows()).toBe(1);
    await plain(input, "Enter");
    expect(rows()).toBe(2);
  });
});
