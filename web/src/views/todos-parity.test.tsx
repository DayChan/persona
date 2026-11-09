// @vitest-environment jsdom
// Todos and the assistant's tasks against the iPhone (#2975): the todos page's loading and empty states, a todo's
// detail on a narrow screen, the list's time, unlinking an agent; the task board and the plan card's "Edit".
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { getState } from "../lib/store";
import type { Todo } from "../lib/todos";
import type { Session } from "../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

vi.mock("react-dom", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react-dom")>()),
  createPortal: (children: React.ReactNode) => children,
}));

const { TodosView } = await import("./Todos");
const { PlanCard, TaskBoard } = await import("./assistant/Tasks");

const hour = 3600_000;
const iso = (agoMs: number) => new Date(Date.now() - agoMs).toISOString();
const todo = (o: Partial<Todo> = {}): Todo => ({ id: "t1", title: "整理发布清单", created_at: iso(72 * hour), updated_at: iso(2 * hour), ...o });
const session = (o: Partial<Session> = {}): Session => ({ machine_id: "m1", session_id: "s1", title: "写清单", cwd: "/work/app", status: "idle", state: "idle", ...o }) as Session;

const seed = (patch: Record<string, unknown>) => Object.assign(getState() as unknown as Record<string, unknown>, patch);
const before = { ...getState() };
afterEach(() => seed(before));

describe("the todos page", () => {
  it("shows loading before the first load, not 没有待办", () => {
    seed({ todos: [], todosLoaded: false });
    const html = renderToStaticMarkup(<TodosView />);
    expect(html).toContain('data-testid="todos-loading"');
    expect(html).not.toContain("没有待办");
  });

  it("shows the iPhone's empty state with 新建待办 once loaded", () => {
    seed({ todos: [], todosLoaded: true });
    const html = renderToStaticMarkup(<TodosView />);
    expect(html).toContain("没有待办");
    expect(html).toContain("记下一件要做的事，交给 Agent 执行并跟踪进度。");
    // the header's (wider screens' button; a phone's plus, its label and its tooltip, #2029) and the empty state's
    expect(html.match(/新建待办/g)?.length).toBe(4);
  });

  it("dates a row by its last update", () => {
    seed({ todos: [todo()], todosLoaded: true });
    const html = renderToStaticMarkup(<TodosView />);
    expect(html).toContain("2小时前");
    expect(html).not.toContain("3天前");
  });

  it("on a narrow screen shows an opened todo's detail instead of the list, with a way back", () => {
    seed({ todos: [todo()], todosLoaded: true });
    const list = renderToStaticMarkup(<TodosView />);
    expect(list).not.toContain("data-narrow-hidden");
    expect(list).not.toContain("data-narrow-shown");
    expect(list).not.toContain('data-testid="todos-back"');
    const opened = renderToStaticMarkup(<TodosView selectedId="t1" />);
    expect(opened).toMatch(/data-testid="todos-list-column" data-narrow-hidden="true"/);
    expect(opened).toMatch(/data-testid="todos-detail-column" data-narrow-shown="true"/);
    expect(opened).toMatch(/data-testid="todos-back"><button[^>]*>.*待办<\/button>/);
  });

  it("lets every current agent be unlinked: 解除关联 on a running one, 移除 on one that did not start", () => {
    const linked_at = iso(hour);
    seed({
      todos: [todo({ agents: [{ machine_id: "m1", session_id: "s1", linked_at }, { machine_id: "m1", session_id: "s2", linked_at, start_error: "机器离线" }] })],
      todosLoaded: true,
      sessions: { m1: [session()] },
    });
    const html = renderToStaticMarkup(<TodosView selectedId="t1" />);
    expect(html).toContain('aria-label="解除「写清单」的关联"');
    expect(html.match(/aria-label="解除「/g)).toHaveLength(1);
    expect(html).toContain(">移除</button>");
  });

  it("on a narrow screen an agent row's source tag leads the title and cannot push the title or the time out (#3577)", () => {
    seed({
      todos: [todo({ agents: [{ machine_id: "m1", session_id: "s2", linked_at: iso(hour), source: "spawned", via: "m1/s1" }] })],
      todosLoaded: true,
      sessions: { m1: [session({ title: "给天气 App 加主屏幕小组件" }), session({ session_id: "s2", title: "整理发布说明" })] },
    });
    const doc = new DOMParser().parseFromString(renderToStaticMarkup(<TodosView selectedId="t1" />), "text/html");
    const tag = doc.querySelector('[data-testid="todo-agent-source"]')!;
    const title = doc.querySelector('[data-testid="todo-agent-title"]')!;
    const time = doc.querySelector('[data-testid="todo-agent-time"]')!;
    const cls = (el: Element) => el.className.split(/\s+/);

    // the tag comes first on the title line, keeps its size but stops at 45% of the line, and shows its whole text on hover
    expect(tag.parentElement).toBe(title.parentElement);
    expect(tag.parentElement!.firstElementChild).toBe(tag);
    expect(cls(tag)).toEqual(expect.arrayContaining(["shrink-0", "max-w-[45%]", "truncate"]));
    expect(tag.getAttribute("title")).toBe("由 给天气 App 加主屏幕小组件 派出");
    // the title takes what is left and truncates instead of collapsing to nothing
    expect(title.textContent).toBe("整理发布说明");
    expect(cls(title)).toEqual(expect.arrayContaining(["min-w-0", "flex-1", "truncate"]));
    // the time stays on one line
    expect(cls(time)).toContain("whitespace-nowrap");
  });
});

describe("the task board", () => {
  it("lists only this conversation's tasks by section, removable ones with 移除", () => {
    seed({
      todos: [
        todo({ id: "a", title: "跑测试", origin: "c1", agents: [{ machine_id: "m1", session_id: "s1", linked_at: iso(hour) }] }),
        todo({ id: "b", title: "已经做完", origin: "c1", done_at: iso(hour) }),
        todo({ id: "c", title: "别的对话", origin: "c2" }),
      ],
      sessions: { m1: [session({ state: "working", status: "working" })] },
    });
    const html = renderToStaticMarkup(<TaskBoard open conversation="c1" onClose={() => {}} />);
    expect(html).toContain("工作中 · 1");
    expect(html).toContain("已完成 · 1");
    expect(html).not.toContain("别的对话");
    expect(html).toContain('aria-label="移除「已经做完」"');
    expect(html).not.toContain('aria-label="移除「跑测试」"');
  });

  it("says how tasks come about when there are none", () => {
    seed({ todos: [], sessions: {} });
    const html = renderToStaticMarkup(<TaskBoard open conversation="c1" onClose={() => {}} />);
    expect(html).toContain("还没有任务");
    expect(html).toContain("在对话里说要做的事，Persona 会把它变成任务。");
  });
});

describe("the plan card", () => {
  it("offers 开始 / 改一下 / 不开 on one line each", () => {
    const html = renderToStaticMarkup(<PlanCard plan={{ id: "p1", conversation: "c1", state: "open", tasks: [{ title: "补测试", prompt: "补", machine: "m1", cwd: "/w" }] }} />);
    const labels = [...html.matchAll(/<button[^>]*whitespace-nowrap[^>]*>(?:<[^>]+>)*([^<]+)<\/button>/g)].map((m) => m[1]);
    expect(labels).toEqual(["开始", "改一下", "不开"]);
  });
});
