// The overview ("Overview") rendered statically (#2320): a waiting card is something to do, and "Mark all as seen" is there whenever
// an agent is unread, though its tab is filed under "Stuck, take a look" or "Working".
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { Interaction, Machine, Session } from "../lib/types";
import type { Todo } from "../lib/todos";

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

const fake = vi.hoisted(() => ({ loaded: true, sessions: [] as Session[], interactions: [] as Interaction[], machines: [] as Machine[], todos: [] as Todo[], answered: {} as Record<string, unknown> }));

vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: fake.loaded, machines: fake.machines, interactions: fake.interactions, todos: fake.todos, answered: fake.answered as ReturnType<typeof real.getState>["answered"] }) };
});
vi.mock("../lib/sessions", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/sessions")>();
  return { ...real, useAgentSessions: () => fake.sessions, useCounts: () => ({ ...real.stateCounts(fake.sessions), interactions: fake.interactions.length }) };
});

// the extensions' overview cards are not this test's
vi.mock("./extensions/ExtensionSlots", () => ({ OverviewCards: () => null }));

const { Home, offlineLine, rowTags } = await import("./Home");

const mac: Machine = { id: "mac", name: "Mac", online: true };
let n = 0;
const agent = (slot: number, pane: number, state: Session["state"], o: Partial<Session> = {}): Session => ({
  machine_id: "mac",
  session_id: `s${++n}`,
  title: `agent ${n}`,
  cwd: "/code",
  status: "idle",
  state,
  space_kind: "herdr",
  space_id: "w1",
  space_name: "app",
  slot_id: `w1:t${slot}`,
  slot_index: slot,
  pane_id: `w1:p${pane}`,
  pane_index: pane,
  ...o,
});
// Preserve fake.loaded unless explicitly provided so tests can set the loading state themselves.
const render = (sessions: Session[], interactions: Interaction[] = [], { machines = [mac], loaded }: { machines?: Machine[]; loaded?: boolean } = {}) => {
  Object.assign(fake, { sessions, interactions, machines }, loaded === undefined ? {} : { loaded });
  return renderToStaticMarkup(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />);
};
const markAll = (html: string) => html.match(/<button [^>]*title="把 (\d+) 个完成未看的会话标为已看"[^>]*>/)?.[1];
// where a group's heading is in the page (-1: not there)
const heading = (html: string, title: string) => html.indexOf(`>${title}</h2>`);
const buttonLabels = (html: string) => (html.match(/<button\b[^>]*>[\s\S]*?<\/button>/g) ?? []).map((button) => button.replace(/<[^>]*>/g, ""));

describe("the overview", () => {
  it("restores green unread success badges without tinting Needs you rows", () => {
    const html = render([agent(1, 0, "unread"), agent(2, 0, "needs_you")]);
    expect(html).toMatch(/class="[^"]*bg-green-tint text-green" aria-label="完成未看"/);
    expect(html).toMatch(/class="[^"]*bg-field text-attention" aria-label="需要你"/);
    expect(html).not.toContain("bg-orange-tint");
    expect(html).not.toContain("bg-red-tint");
  });

  it("says what each waiting card asks, from whatever field has it (#3780)", () => {
    const a = agent(1, 0, "needs_you");
    const html = render([a], [
      { id: "q", kind: "question", machine_id: "mac", session_id: a.session_id, summary: "先做 A 还是 B？" } as Interaction,
      { id: "p", kind: "permission", machine_id: "mac", session_id: a.session_id, tool_name: "Bash", tool_input: { command: "swift test" } } as Interaction,
      { id: "l", kind: "plan", machine_id: "mac", session_id: a.session_id, plan: "# 计划" } as Interaction,
    ]);
    expect(html).toContain("先做 A 还是 B？");
    expect(html).toContain("swift test");
    expect(html).toContain("计划写好了，等你批准");
  });

  it("answers a permission or a one-choice question on its row, and sends a plan to its chat (#4090)", () => {
    const a = agent(1, 0, "needs_you");
    const buttons = (html: string) => [...html.matchAll(/<button [^>]*>(?:<[^>]+>)*([^<]+)/g)].map((m) => m[1]);
    const html = render([a], [
      { id: "p", kind: "permission", machine_id: "mac", session_id: a.session_id, tool_name: "Bash", tool_input: { command: "swift test", description: "运行测试" } } as Interaction,
      { id: "q", kind: "question", machine_id: "mac", session_id: a.session_id, questions: [{ question: "哪种格式？", options: [{ label: "JPEG" }, { label: "PNG" }] }] } as Interaction,
    ]);
    expect(buttons(html)).toEqual(expect.arrayContaining(["允许", "拒绝", "JPEG", "PNG"]));
    // what "Allow" allows, by tool and command, not the agent's description of it
    expect(html).toContain("Bash：swift test");
    const plan = render([a], [{ id: "l", kind: "plan", machine_id: "mac", session_id: a.session_id, plan: "# 计划" } as Interaction]);
    expect(buttons(plan)).not.toContain("允许");
    expect(plan).toContain("计划写好了，等你批准");
  });

  it("asks for a computer before offering to create an agent (#3729)", () => {
    const html = render([], [], { machines: [] });
    expect(html).toContain("还没有电脑");
    expect(html).toContain("先接入一台电脑，再开始使用 Agent。");
    expect(html).toContain("把一台电脑接入 hub 后，就能在这里新建 Agent、查看进展。");
    expect(buttonLabels(html)).toContain("打开机器页");
    expect(buttonLabels(html)).not.toContain("新建 Agent");
    expect(html).not.toContain("一切就绪");
    expect(html).not.toContain("所有 Agent 都在等你");
  });

  it("waits for the initial load before showing the no-computers guidance", () => {
    let html: string;
    try {
      html = render([], [], { machines: [], loaded: false });
    } finally {
      fake.loaded = true;
    }
    expect(html).toContain("正在连上 hub");
    expect(html).not.toContain("还没有电脑");
    expect(html).not.toContain("先接入一台电脑");
    expect(buttonLabels(html)).not.toContain("打开机器页");
    expect(html).not.toContain("一切就绪");
    expect(html).not.toContain("所有 Agent 都在等你");
  });

  it.each([{ state: "online", online: true }, { state: "offline", online: false }])("keeps the ready state with an existing $state computer and no agents", ({ online }) => {
    const html = render([], [], { machines: [{ ...mac, online }] });
    expect(html).toContain("一切就绪");
    expect(html).toContain("所有 Agent 都在等你");
    expect(buttonLabels(html)).toContain("新建 Agent");
    expect(html).not.toContain("还没有电脑");
    expect(buttonLabels(html)).not.toContain("打开机器页");
  });

  it("does not say nothing needs you above a waiting card", () => {
    const asking = agent(1, 0, "needs_you");
    const html = render([asking, agent(2, 0, "idle")], [{ id: "i1", kind: "permission", machine_id: "mac", session_id: asking.session_id, tool_name: "Bash" } as Interaction]);
    expect(heading(html, "需要你")).toBeGreaterThan(-1);
    expect(html).not.toContain("没有需要你处理的");
    expect(html).not.toContain("一切就绪");
    expect(render([agent(2, 0, "idle")])).toContain("没有需要你处理的");
  });

  it("offers 全部标为已看 beside the summary when the unread agents' tabs are working or need you", () => {
    const html = render([agent(1, 0, "working"), agent(1, 1, "unread"), agent(2, 0, "needs_you"), agent(2, 1, "unread")]);
    expect(heading(html, "完成未看")).toBe(-1);
    expect(heading(html, "工作中")).toBeGreaterThan(-1);
    expect(html).toContain("卡住了，要你去看");
    expect(markAll(html)).toBe("2");
  });

  it("keeps it in 完成未看's header, for every unread agent", () => {
    const html = render([agent(1, 0, "working"), agent(1, 1, "unread"), agent(3, 0, "unread")]);
    const section = heading(html, "完成未看");
    expect(section).toBeGreaterThan(-1);
    expect(markAll(html)).toBe("2");
    expect(html.indexOf("全部标为已看")).toBeGreaterThan(section);
    expect(markAll(render([agent(1, 0, "working")]))).toBeUndefined();
  });

  it("says the headline counts agents and why the cards can be more (#2956)", () => {
    const a = agent(2, 0, "needs_you");
    const card = (id: string) => ({ id, machine_id: "mac", session_id: a.session_id, kind: "question" }) as Interaction;
    const two = render([a], [card("q1"), card("q2")]);
    expect(two).toContain("1 个 Agent 需要你");
    expect(two).toContain("同一个 Agent 可能连着问你几件事");
    // one card per agent: the numbers agree, nothing to explain
    expect(render([a], [card("q1")])).not.toContain("同一个 Agent 可能连着问你几件事");
    // two cards on one agent beside a stuck agent without a card: still one agent with several (#2956 review)
    const stuck = agent(3, 0, "needs_you");
    expect(render([a, stuck], [card("q1"), card("q2")])).toContain("同一个 Agent 可能连着问你几件事");
    // a card per agent, its agent not counted (an assistant session): nothing to explain
    const other = { id: "q3", machine_id: "mac", session_id: "assistant-1", kind: "question" } as Interaction;
    expect(render([a], [card("q1"), other])).not.toContain("同一个 Agent 可能连着问你几件事");
  });

  it("explains the agents that need you without a card (#2956)", () => {
    const html = render([agent(2, 0, "needs_you")]);
    expect(html).toContain("卡住了，要你去看");
    expect(html).toContain("这些 Agent 没发来要你处理的卡片，多半是出错或卡住了");
  });

  it("before the first snapshot shows only 加载中, not the agents and cards the socket has streamed in so far", () => {
    const asking = agent(1, 0, "needs_you");
    const ix = { id: "i1", kind: "permission", machine_id: "mac", session_id: asking.session_id, tool_name: "Bash" } as Interaction;
    fake.loaded = false;
    try {
      const early = render([asking, agent(2, 0, "working"), agent(3, 0, "unread")], [ix]);
      expect(early).toContain("加载中");
      for (const group of ["需要你", "卡住了，要你去看", "工作中", "完成未看", "电脑"]) expect(heading(early, group)).toBe(-1);
      expect(early).not.toContain("全部标为已看");
    } finally {
      fake.loaded = true;
    }
    const html = render([asking, agent(2, 0, "working"), agent(3, 0, "unread")], [ix]);
    expect(html).not.toContain("加载中");
    for (const group of ["需要你", "工作中", "完成未看", "电脑"]) expect(heading(html, group)).toBeGreaterThan(-1);
  });
});

describe("compact rows", () => {
  const tags = (html: string) => (html.match(/<span [^>]*data-row-tags[^>]*>([\s\S]*?)<\/span><\/span>/g) ?? []).map((t) => [...t.matchAll(/>([^<]+)<\/span>/g)].map((m) => m[1]));

  it("shows the title once: a tab named after it is not a tag, its machine and space are", () => {
    const title = "Migrate the shared storage client to the new region config";
    const html = render([agent(1, 0, "unread", { title, slot_name: title.slice(0, 30) + "…", last_text: "Pushed the branch." })]);
    expect(tags(html)).toEqual([["Mac", "app"]]);
    // (Continue's screen-reader label names the row too: not something on screen)
    expect(html.replace(/<span class="sr-only">[^<]*<\/span>/g, "").split(title).length - 1).toBe(2); // the title text and the row's tooltip
  });

  it("says a session's source once, as a tag, not again before its latest words", () => {
    const html = render([agent(1, 0, "unread", { source_kind: "runtime", agent_label: "DSH", capabilities: { read: true, send: true }, last_text: "Report ready." })]);
    expect(tags(html)[0]).toContain("DSH");
    expect(html).not.toContain("DSH · Report ready.");
    expect(html).toContain(">Report ready.<");
  });

  it("puts the rows of every agent section in columns on a wide page, cards with buttons later", () => {
    const a = agent(1, 0, "needs_you");
    const html = render([a, agent(2, 0, "unread"), agent(3, 0, "working")], [{ id: "p", kind: "permission", machine_id: "mac", session_id: a.session_id, tool_name: "Bash", tool_input: { command: "ls" } } as Interaction]);
    expect(html.match(/@min-\[60rem\]:grid-cols-2 @min-\[100rem\]:grid-cols-3/g)?.length).toBe(2);
    // the cards with buttons only once each has room for its question and buttons
    expect(html.match(/@min-\[84rem\]:grid-cols-2 @min-\[120rem\]:grid-cols-3/g)?.length).toBe(1);
    expect(html).toContain("@container");
  });

  it("keeps a short space name that only starts the title, and drops a repeat", () => {
    expect(rowTags("app store listing", ["Mac", "app", "#2"])).toEqual(["Mac", "app", "#2"]);
    expect(rowTags("Fix the login redirect loop", ["Mac", "web", "Fix the login redir…"])).toEqual(["Mac", "web"]);
    expect(rowTags("Untitled", ["Mac", "", undefined, false, "Mac"])).toEqual(["Mac"]);
  });
});

describe("an offline computer's line on the overview (#4038)", () => {
  const raw = "ssh: connect to host box port 22: Operation timed out";
  it("says why in plain words and keeps the raw error in the tooltip", () => {
    Object.assign(fake, { sessions: [], interactions: [], machines: [mac, { id: "box", name: "盒子", online: false, error: raw }] });
    const html = renderToStaticMarkup(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />);
    const line = html.match(/<span [^>]*data-machine-line[^>]*>([^<]*)<\/span>/g)?.find((s) => s.includes("Operation"));
    expect(line).toContain(`title="${raw}"`);
    expect(line).toContain(">连接超时<");
  });
  it("keeps the raw text when it does not say why, and 离线 without one", () => {
    expect(offlineLine({ id: "b", name: "b", online: false, error: "something odd" })).toBe("something odd");
    expect(offlineLine({ id: "b", name: "b", online: false })).toBe("离线");
  });
  it("reads it in the machines page's order: waiting to be confirmed, then the hub's error_code", () => {
    expect(offlineLine({ id: "b", name: "b", online: false, trust: "pending", error: "ssh: connect to host b port 22: Operation timed out" } as Machine)).toBe("还没连上来过");
    expect(offlineLine({ id: "b", name: "b", online: false, error_code: "node_service_down", error: "dial tcp: i/o timeout" } as Machine)).not.toBe("连接超时");
  });
});

// Tasks in progress (#6450): every open task with its stage, PR and held delivery, above the working agents; none, no section.
describe("tasks in progress", () => {
  const todo = (id: string, o: Partial<Todo> = {}): Todo => ({ id, title: `task ${id}`, created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-01T00:00:00Z", ...o });
  it("lists open tasks with their stage, PR and delivery", () => {
    fake.todos = [
      todo("a", { stage: "review", pr_url: "https://example.com/o/r/pull/42", agents: [{ machine_id: "mac", session_id: "w", linked_at: "x" }] }),
      todo("b", { delivery: { state: "merged" } }),
      todo("c", { done_at: "2026-11-02T00:00:00Z" }),
    ];
    const html = render([agent(1, 1, "working", { session_id: "w" })]);
    fake.todos = [];
    expect(heading(html, "进行中的任务")).toBeGreaterThan(-1);
    expect(heading(html, "进行中的任务")).toBeLessThan(heading(html, "工作中"));
    expect(html).toContain('data-task="a"');
    expect(html).toContain('data-task="b"');
    expect(html).not.toContain('data-task="c"');
    expect(html).toContain('data-stage="review" data-state="current"');
    expect(html).toContain("https://example.com/o/r/pull/42");
    expect(html).toContain("1 个 Agent");
    expect(html).not.toContain("查看全部");
    expect(html).toContain("已合未上线");
  });
  it("offers See all past six tasks", () => {
    fake.todos = Array.from({ length: 7 }, (_, i) => todo(`t${i}`));
    const html = render([]);
    fake.todos = [];
    expect(html.match(/data-task=/g)?.length).toBe(6);
    expect(html).toContain("查看全部");
  });
  it("moves a task whose card was answered here out of Needs you at once", () => {
    const s = agent(1, 1, "needs_you", { session_id: "w" });
    const ix = { id: "ix1", machine_id: "mac", session_id: "w", kind: "question" } as Interaction;
    fake.todos = [todo("a", { agents: [{ machine_id: "mac", session_id: "w", linked_at: "x" }] })];
    const before = render([s], [ix]);
    fake.answered = { ix1: { ix, resolution: {}, at: Date.now() } };
    const after = render([s], []);
    fake.todos = [];
    fake.answered = {};
    const pill = (html: string) => html.slice(html.indexOf('data-task="a"')).match(/rounded-full[^>]*>([^<]+)</)?.[1];
    expect(pill(before)).toBe("需要你");
    expect(pill(after)).not.toBe("需要你");
  });
  it("draws nothing without open tasks", () => {
    fake.todos = [todo("c", { done_at: "2026-11-02T00:00:00Z" })];
    const html = render([]);
    fake.todos = [];
    expect(heading(html, "进行中的任务")).toBe(-1);
  });
});

it("opens an explicitly sessionless request and offers its quick answers", () => {
  const html = render([], [{ id: "script-request", machine_id: "mac", session_id: "", kind: "permission", tool_name: "personad_ask", summary: "Publish the draft?" }]);
  expect(html).toContain('href="/requests/script-request"');
  expect(buttonLabels(html)).toContain("允许");
  expect(html).not.toContain('href="/s/mac/"');
});
