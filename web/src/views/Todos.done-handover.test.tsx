// @vitest-environment jsdom
// #1856: a done to-do offers no "Hand to agent" (the hub refuses every hand-off for it), and its empty agent list says to
// reopen it first; an open one keeps the button. Handing over to a new agent opens its start page, which says how the start
// went: no success toast before the hub has answered. The hub is faked; the data made up.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Todo } from "../lib/todos";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../lib/api", async (orig) => ({
  ...(await orig<typeof import("../lib/api")>()),
  api: (path: string, init?: unknown) => api(path, init),
}));
const toast = vi.fn();
vi.mock("../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../ui/atoms")>()), toast: (...a: unknown[]) => toast(...a) }));
const launched: Record<string, unknown>[] = [];
vi.mock("../lib/new-agent", async (orig) => ({
  ...(await orig<typeof import("../lib/new-agent")>()),
  launchAgent: (input: Record<string, unknown>) => {
    launched.push(input);
    return "local-1";
  },
}));

const { getState } = await import("../lib/store");
const { TodosView } = await import("./Todos");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const button = (label: string) => [...document.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | undefined | null) => {
  expect(el, "element").toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};

const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const open: Todo = { id: "t1", title: "整理发布清单", created_at: iso(7_200_000), updated_at: iso(3_600_000) };
const done: Todo = { ...open, id: "t2", title: "写发布说明", done_at: iso(600_000) };

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };

beforeEach(() => {
  api.mockReset();
  api.mockImplementation(async (path: string) => (path.endsWith("/launchers") ? { launchers: [] } : {}));
  toast.mockReset();
  launched.length = 0;
  seed({ todos: [open, done], todosLoaded: true, machines: [{ id: "m1", name: "示例电脑", online: true }], sessions: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  seed(before);
  vi.restoreAllMocks();
});

const show = async (id: string) => {
  await act(async () => root.render(<TodosView selectedId={id} />));
  await flush();
};

describe("交给 Agent 与待办是否完成", () => {
  it("已完成的待办不给「交给 Agent」，空的 Agent 列表说先重新打开", async () => {
    await show("t2");
    expect(button("交给 Agent")).toBeUndefined();
    expect(button("重新打开")).toBeTruthy();
    expect(document.body.textContent).toContain("现在没有 Agent 在做。要交给 Agent，先重新打开。");
    expect(document.body.textContent).not.toContain("点击下方按钮开始");
  });

  it("没完成的待办照旧有「交给 Agent」", async () => {
    await show("t1");
    expect(button("交给 Agent")).toBeTruthy();
    expect(document.body.textContent).toContain("点击下方按钮开始");
  });

  it("交给新 Agent：打开它的启动页，不在 hub 回话前报成功", async () => {
    await show("t1");
    await click(button("交给 Agent"));
    await click(button("启动并交给 Agent"));
    expect(launched).toHaveLength(1);
    expect(launched[0]).toMatchObject({ machineId: "m1", todoId: "t1" });
    expect(toast.mock.calls.filter((c) => c[1] === "ok")).toEqual([]);
    expect(document.querySelector("[role=dialog]")).toBeNull();
  });
});
