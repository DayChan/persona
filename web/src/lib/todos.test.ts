import { describe, expect, it } from "vitest";
import type { Machine, Session } from "./types";
import {
  heldDelivery,
  isAutoDone,
  isRemovable,
  TODO_STATUS_INFO,
  todoStatusLabel,
  buildTodoAgentGroups,
  dockAgents,
  findTodoForSpace,
  formatAgentSource,
  formatTodoContext,
  interruption,
  isOverdue,
  isSpacesSupported,
  makeSessionLookup,
  mostUrgentAgent,
  resolveLinkCurrentTodoId,
  runningSubagents,
  sortTodos,
  taskNow,
  todoDueBy,
  todoStatus,
  type Todo,
  type TodoAgent,
  type TodoSpace,
} from "./todos";
import type { AgentMutes } from "./agent-groups";
import { BOARD_ORDER, boardSectionOf } from "./task-board";

const machines: Machine[] = [
  { id: "mac-1", name: "MacBook", online: true },
  { id: "server-1", name: "服务器", online: true },
  { id: "mac-off", name: "离线电脑", online: false },
];

const mockSession = (mid: string, sid: string, state: Session["state"], patch: Partial<Session> = {}): Session => ({
  machine_id: mid,
  session_id: sid,
  title: sid,
  cwd: `/code/${sid}`,
  status: state ?? "idle",
  state,
  updated_at: "2026-11-02T10:00:00Z",
  ...patch,
});

describe("待办状态计算 (todoStatus)", () => {
  it("用户点了完成（done_at != null），不论 Agent 状态如何均判定为已完成", () => {
    const s = mockSession("mac-1", "s1", "needs_you");
    const lookup = makeSessionLookup([s]);
    const todo: Todo = {
      id: "t1",
      title: "已完成待办",
      created_at: "2026-11-02T10:00:00Z",
      updated_at: "2026-11-02T10:00:00Z",
      done_at: "2026-11-02T11:00:00Z",
      agents: [{ machine_id: "mac-1", session_id: "s1", linked_at: "2026-11-02T10:00:00Z" }],
    };
    expect(todoStatus(todo, lookup)).toBe("done");
  });

  it("没有挂着的 Agent 时为未开始 (not_started)", () => {
    const todoNoAgents: Todo = {
      id: "t1",
      title: "空待办",
      created_at: "2026-11-02T10:00:00Z",
      updated_at: "2026-11-02T10:00:00Z",
      agents: [],
    };
    expect(todoStatus(todoNoAgents)).toBe("not_started");

    // an agent that is only released also counts as not started
    const todoReleased: Todo = {
      ...todoNoAgents,
      agents: [{ machine_id: "mac-1", session_id: "s1", linked_at: "2026-11-02T09:00:00Z", released_at: "2026-11-02T09:30:00Z" }],
    };
    expect(todoStatus(todoReleased)).toBe("not_started");
  });

  it("有挂着的 Agent 需要你，状态为等你 (needs_you)", () => {
    const s1 = mockSession("mac-1", "s1", "needs_you");
    const s2 = mockSession("mac-1", "s2", "working");
    const lookup = makeSessionLookup([s1, s2]);
    const todo: Todo = {
      id: "t1",
      title: "待办",
      created_at: "2026-11-02T10:00:00Z",
      updated_at: "2026-11-02T10:00:00Z",
      agents: [
        { machine_id: "mac-1", session_id: "s1", linked_at: "2026-11-02T10:00:00Z" },
        { machine_id: "mac-1", session_id: "s2", linked_at: "2026-11-02T10:00:00Z" },
      ],
    };
    expect(todoStatus(todo, lookup)).toBe("needs_you");
  });

  it("没有等你，但有挂着的 Agent 完成了未看，状态为待查看 (unread)", () => {
    const s1 = mockSession("mac-1", "s1", "unread");
    const s2 = mockSession("mac-1", "s2", "working");
    const lookup = makeSessionLookup([s1, s2]);
    const todo: Todo = {
      id: "t1",
      title: "待办",
      created_at: "2026-11-02T10:00:00Z",
      updated_at: "2026-11-02T10:00:00Z",
      agents: [
        { machine_id: "mac-1", session_id: "s1", linked_at: "2026-11-02T10:00:00Z" },
        { machine_id: "mac-1", session_id: "s2", linked_at: "2026-11-02T10:00:00Z" },
      ],
    };
    expect(todoStatus(todo, lookup)).toBe("unread");
  });

  it("没有等你和待查看，但有在工作或正在启动，状态为进行中 (working)", () => {
    const s1 = mockSession("mac-1", "s1", "idle");
    const s2 = mockSession("mac-1", "s2", "working");
    const lookup = makeSessionLookup([s1, s2]);
    const todo: Todo = {
      id: "t1",
      title: "待办",
      created_at: "2026-11-02T10:00:00Z",
      updated_at: "2026-11-02T10:00:00Z",
      agents: [
        { machine_id: "mac-1", session_id: "s1", linked_at: "2026-11-02T10:00:00Z" },
        { machine_id: "mac-1", session_id: "s2", linked_at: "2026-11-02T10:00:00Z" },
      ],
    };
    expect(todoStatus(todo, lookup)).toBe("working");

    // a starting session also counts as working / starting
    const sStarting = mockSession("mac-1", "s3", "idle", { starting: true });
    const lookupStarting = makeSessionLookup([sStarting]);
    const todoStarting: Todo = {
      ...todo,
      agents: [{ machine_id: "mac-1", session_id: "s3", linked_at: "2026-11-02T10:00:00Z" }],
    };
    expect(todoStatus(todoStarting, lookupStarting)).toBe("working");
  });

  it("挂着的 Agent 都停着（已看、未运行、启动失败），状态为待跟进 (follow_up)", () => {
    const sIdle = mockSession("mac-1", "s1", "idle");
    const lookup = makeSessionLookup([sIdle]);
    const todo: Todo = {
      id: "t1",
      title: "待办",
      created_at: "2026-11-02T10:00:00Z",
      updated_at: "2026-11-02T10:00:00Z",
      agents: [
        { machine_id: "mac-1", session_id: "s1", linked_at: "2026-11-02T10:00:00Z" },
        { machine_id: "mac-1", session_id: "s2", linked_at: "2026-11-02T10:00:00Z", start_error: "launch failed" },
      ],
    };
    expect(todoStatus(todo, lookup)).toBe("follow_up");
  });
});

describe("Agent 没开起来的任务 (#1077 / #1142)", () => {
  const failed: Todo = {
    id: "t9",
    title: "设置页按钮对齐",
    created_at: "2026-11-04T10:00:00Z",
    updated_at: "2026-11-04T10:00:05Z",
    done_at: "2026-11-04T10:00:05Z",
    start_error: "rpc: connection closed",
    origin: "c1",
    plan_id: "p1",
    agents: [],
  };

  it("hub 结束掉的失败任务是「没开起来」，不是已完成", () => {
    expect(todoStatus(failed)).toBe("failed");
    expect(TODO_STATUS_INFO.failed).toEqual({ label: "没开起来", tone: "red" });
    expect(todoStatusLabel(failed, "failed")).toBe("没开起来");
    expect(todoStatus({ ...failed, start_error: undefined })).toBe("done");
  });

  it("排在在做的前面，不跟已完成一起沉底（#4896）", () => {
    const open: Todo = { id: "t1", title: "在做", created_at: "2026-11-04T09:00:00Z", updated_at: "2026-11-04T09:00:00Z", agents: [] };
    const done: Todo = { ...open, id: "t2", done_at: "2026-11-04T09:30:00Z" };
    expect(sortTodos([done, open, failed]).map((t) => t.id)).toEqual(["t9", "t1", "t2"]);
  });

  it("没在运行的才能移除", () => {
    expect(isRemovable("failed")).toBe(true);
    expect(isRemovable("done")).toBe(true);
    expect(isRemovable("unread")).toBe(true);
    expect(isRemovable("follow_up")).toBe(true);
    expect(isRemovable("not_started")).toBe(true);
    expect(isRemovable("working")).toBe(false);
    expect(isRemovable("needs_you")).toBe(false);
  });
});

describe("待办列表排序 (sortTodos)", () => {
  it("按任务板的顺序：等你 > 进行中 > 未开始 > 待查看 > 待跟进 > 已完成（#4896）", () => {
    const sNeeds = mockSession("mac-1", "s-needs", "needs_you");
    const sUnread = mockSession("mac-1", "s-unread", "unread");
    const sWorking = mockSession("mac-1", "s-work", "working");
    const sIdle = mockSession("mac-1", "s-idle", "idle");
    const lookup = makeSessionLookup([sNeeds, sUnread, sWorking, sIdle]);

    const todos: Todo[] = [
      { id: "done-1", title: "已完成1", created_at: "2026-11-02T12:00:00Z", updated_at: "", done_at: "2026-11-02T13:00:00Z" },
      { id: "empty-1", title: "未开始1", created_at: "2026-11-02T10:00:00Z", updated_at: "", agents: [] },
      { id: "work-1", title: "工作中1", created_at: "2026-11-02T10:00:00Z", updated_at: "", agents: [{ machine_id: "mac-1", session_id: "s-work", linked_at: "" }] },
      { id: "needs-1", title: "等你1", created_at: "2026-11-02T10:00:00Z", updated_at: "", agents: [{ machine_id: "mac-1", session_id: "s-needs", linked_at: "" }] },
      { id: "idle-1", title: "待跟进1", created_at: "2026-11-02T10:00:00Z", updated_at: "", agents: [{ machine_id: "mac-1", session_id: "s-idle", linked_at: "" }] },
      { id: "unread-1", title: "待查看1", created_at: "2026-11-02T10:00:00Z", updated_at: "", agents: [{ machine_id: "mac-1", session_id: "s-unread", linked_at: "" }] },
    ];

    const sorted = sortTodos(todos, lookup);
    expect(sorted.map((t) => t.id)).toEqual([
      "needs-1",
      "work-1",
      "empty-1",
      "unread-1",
      "idle-1",
      "done-1",
    ]);
  });

  it("每种状态都排在任务板上它那一组的位置，待验收紧跟等你（#4896）", () => {
    const lookup = makeSessionLookup([
      mockSession("mac-1", "s-needs", "needs_you"),
      mockSession("mac-1", "s-cut", "idle", { turn_error: "API Error: 529 overloaded" }),
      mockSession("mac-1", "s-work", "working"),
      mockSession("mac-1", "s-unread", "unread"),
      mockSession("mac-1", "s-idle", "idle"),
    ]);
    const on = (sid: string) => [{ machine_id: "mac-1", session_id: sid, linked_at: "" }];
    const at = "2026-11-06T10:00:00Z";
    const review = { supervisor: "main", state: "pending_review" as const };
    const todos: Todo[] = [
      { id: "done", title: "", created_at: at, updated_at: "", done_at: at },
      { id: "stopped", title: "", created_at: at, updated_at: "", agents: on("s-idle") },
      { id: "unread", title: "", created_at: at, updated_at: "", agents: on("s-unread") },
      { id: "not-started", title: "", created_at: at, updated_at: "", agents: [] },
      { id: "working", title: "", created_at: at, updated_at: "", agents: on("s-work") },
      { id: "failed", title: "", created_at: at, updated_at: "", done_at: at, start_error: "spawn failed" },
      { id: "review-unread", title: "", created_at: at, updated_at: "", agents: on("s-unread"), supervision: review },
      { id: "review-stopped", title: "", created_at: at, updated_at: "", agents: on("s-idle"), supervision: review },
      { id: "interrupted", title: "", created_at: at, updated_at: "", agents: on("s-cut") },
      { id: "needs", title: "", created_at: at, updated_at: "", agents: on("s-needs") },
    ];
    const sorted = sortTodos(todos, lookup);
    expect(sorted.map((t) => t.id)).toEqual(["needs", "interrupted", "review-stopped", "review-unread", "failed", "working", "not-started", "unread", "stopped", "done"]);
    // the same order as the board's sections
    const sections = sorted.map((t) => BOARD_ORDER.indexOf(boardSectionOf({ todo: t, status: todoStatus(t, lookup) })));
    expect(sections).toEqual([...sections].sort((a, b) => a - b));
    expect(new Set(sections).size).toBe(BOARD_ORDER.length);
  });

  it("同状态下按创建时间倒序排（新的在前）", () => {
    const sNeeds = mockSession("mac-1", "s-needs", "needs_you");
    const lookup = makeSessionLookup([sNeeds]);

    const todos: Todo[] = [
      { id: "needs-old", title: "较早创建", created_at: "2026-11-02T09:00:00Z", updated_at: "", agents: [{ machine_id: "mac-1", session_id: "s-needs", linked_at: "" }] },
      { id: "needs-new", title: "较晚创建", created_at: "2026-11-02T11:00:00Z", updated_at: "", agents: [{ machine_id: "mac-1", session_id: "s-needs", linked_at: "" }] },
    ];

    const sorted = sortTodos(todos, lookup);
    expect(sorted.map((t) => t.id)).toEqual(["needs-new", "needs-old"]);
  });

  it("同一秒内、小数位数不同的两个时间按真实时间戳解析正确排序 (R5)", () => {
    // 500ms (1 decimal) is later than 450ms (3 decimals), both not started
    const tOlder: Todo = { id: "older-subsecond", title: "较早", created_at: "2026-11-02T10:00:00.450Z", updated_at: "", agents: [] };
    const tNewer: Todo = { id: "newer-subsecond", title: "较晚", created_at: "2026-11-02T10:00:00.5Z", updated_at: "", agents: [] };

    const sorted = sortTodos([tOlder, tNewer]);
    expect(sorted.map((t) => t.id)).toEqual(["newer-subsecond", "older-subsecond"]);
  });
});

describe("按截止时间排序（#1683）", () => {
  it("等你的在最上，其余按截止从早到晚（过期的在前），没有截止的排后面，已完成的最后", () => {
    const lookup = makeSessionLookup([mockSession("mac-1", "s-needs", "needs_you")]);
    const todos: Todo[] = [
      { id: "done", title: "done", created_at: "2026-11-02T10:00:00Z", updated_at: "", done_at: "2026-11-02T13:00:00Z", due_date: "2020-01-01" },
      { id: "none", title: "none", created_at: "2026-11-02T10:00:00Z", updated_at: "" },
      { id: "day", title: "day", created_at: "2026-11-02T10:00:00Z", updated_at: "", due_date: "2026-11-12" },
      { id: "overdue", title: "overdue", created_at: "2026-11-02T10:00:00Z", updated_at: "", due_date: "2020-01-01" },
      { id: "morning", title: "morning", created_at: "2026-11-02T10:00:00Z", updated_at: "", due_at: new Date(2026, 10, 12, 9).toISOString() },
      { id: "needs", title: "needs", created_at: "2026-11-02T10:00:00Z", updated_at: "", agents: [{ machine_id: "mac-1", session_id: "s-needs", linked_at: "" }] },
    ];
    expect(sortTodos(todos, lookup).map((t) => t.id)).toEqual(["needs", "overdue", "morning", "day", "none", "done"]);
  });

  it("截止日期算到当天结束，过期只看未完成的", () => {
    const day: Todo = { id: "d", title: "d", created_at: "", updated_at: "", due_date: "2026-11-12" };
    expect(todoDueBy(day)).toBe(new Date(2026, 10, 13).getTime() - 1000);
    expect(todoDueBy({ ...day, due_date: "soon" })).toBeUndefined();
    expect(isOverdue(day, new Date(2026, 10, 12, 23).getTime())).toBe(false);
    expect(isOverdue(day, new Date(2026, 10, 13, 0, 0, 1).getTime())).toBe(true);
    expect(isOverdue({ ...day, done_at: "2026-11-13T00:00:00Z" }, new Date(2026, 10, 14).getTime())).toBe(false);
  });
});

describe("最要紧的 Agent (mostUrgentAgent)", () => {
  it("选出最高优先级的 Agent", () => {
    const sIdle = mockSession("mac-1", "s1", "idle");
    const sNeeds = mockSession("mac-1", "s2", "needs_you", { last_text: "请选择测试框架" });
    const lookup = makeSessionLookup([sIdle, sNeeds]);

    const todo: Todo = {
      id: "t1",
      title: "待办",
      created_at: "",
      updated_at: "",
      agents: [
        { machine_id: "mac-1", session_id: "s1", linked_at: "" },
        { machine_id: "mac-1", session_id: "s2", linked_at: "" },
      ],
    };

    const best = mostUrgentAgent(todo, lookup);
    expect(best?.agent.session_id).toBe("s2");
    expect(best?.status).toBe("needs_you");
    expect(best?.session?.last_text).toBe("请选择测试框架");
  });
});

describe("Agents 侧边栏「按待办」分组 (buildTodoAgentGroups)", () => {
  const mutes: AgentMutes = { spaces: [], sessions: ["mac-1/muted-work"] };

  it("每个挂了 live agent 的未完成待办一段，没挂待办的放最后「未归属」，默认折叠", () => {
    const sWeatherTests = mockSession("mac-1", "weather-tests", "needs_you", { todo: { id: "todo-weather", title: "天气 App" } });
    const sWeatherWidget = mockSession("mac-1", "weather-widget", "working", { todo: { id: "todo-weather", title: "天气 App" } });
    const sBlog = mockSession("mac-1", "blog-migrate", "unread", { todo: { id: "todo-blog", title: "博客迁移" } });
    const sSolo = mockSession("server-1", "solo-task", "idle"); // not linked to a todo

    const all = [sWeatherTests, sWeatherWidget, sBlog, sSolo];

    const todos: Todo[] = [
      {
        id: "todo-weather",
        title: "天气 App 发 1.3 版",
        created_at: "2026-11-02T10:00:00Z",
        updated_at: "",
        agents: [
          { machine_id: "mac-1", session_id: "weather-tests", linked_at: "" },
          { machine_id: "mac-1", session_id: "weather-widget", linked_at: "" },
        ],
      },
      {
        id: "todo-blog",
        title: "博客迁移到 Astro",
        created_at: "2026-11-02T09:00:00Z",
        updated_at: "",
        agents: [{ machine_id: "mac-1", session_id: "blog-migrate", linked_at: "" }],
      },
    ];

    const groups = buildTodoAgentGroups(all, machines, todos, mutes);
    expect(groups.map((g) => g.name)).toEqual(["天气 App 发 1.3 版", "博客迁移到 Astro", "没挂待办的"]);
    expect(groups[2].hint).toContain("没挂在任何待办上");
    expect(groups[0].hint).toBeUndefined();
    expect(groups[groups.length - 1].folded).toBe(true);
    expect(groups[0].todoStatus).toBe("needs_you");
    expect(groups[0].sessions.map((s) => s.session_id)).toEqual(["weather-tests", "weather-widget"]);
    expect(groups[1].todoStatus).toBe("unread");
    expect(groups[1].sessions.map((s) => s.session_id)).toEqual(["blog-migrate"]);
    expect(groups[2].sessions.map((s) => s.session_id)).toEqual(["solo-task"]);
  });

  it("没挂待办、在 herdr 里手动开的单独一段「手动开的」，在「未归属」前面，展开 (#2608)", () => {
    const sOnTodo = mockSession("mac-1", "on-todo", "working", { space_kind: "herdr", started_by_persona: false, todo: { id: "todo-a", title: "A" } });
    const sManual = mockSession("mac-1", "manual-herdr", "idle", { space_kind: "herdr", started_by_persona: false });
    const sByPersona = mockSession("mac-1", "persona-herdr", "idle", { space_kind: "herdr", started_by_persona: true });
    const sBotmux = mockSession("mac-1", "botmux-chat", "idle", { space_kind: "botmux" });
    const todos: Todo[] = [
      { id: "todo-a", title: "待办 A", created_at: "2026-11-02T10:00:00Z", updated_at: "", agents: [{ machine_id: "mac-1", session_id: "on-todo", linked_at: "" }] },
    ];

    const groups = buildTodoAgentGroups([sOnTodo, sManual, sByPersona, sBotmux], machines, todos, mutes);
    expect(groups.map((g) => g.key)).toEqual(["todo:todo-a", "todo:manual", "todo:none"]);
    expect(groups[1].name).toBe("在终端里开的");
    // the name alone does not say why these are here: the group says it (#3057)
    expect(groups[1].hint).toContain("没挂在待办上");
    expect(groups[1].folded).toBeFalsy();
    expect(groups[1].sessions.map((s) => s.session_id)).toEqual(["manual-herdr"]);
    expect(groups[2].sessions.map((s) => s.session_id).sort()).toEqual(["botmux-chat", "persona-herdr"]);

    // no todo above: "Opened by hand" alone does not fold "Unassigned"
    const noTodo = buildTodoAgentGroups([sManual, sBotmux], machines, [], mutes);
    expect(noTodo.map((g) => g.key)).toEqual(["todo:manual", "todo:none"]);
    expect(noTodo[1].folded).toBe(false);
  });

  it("Agent 换到另一个待办后马上归新待办，即使旧待办的列表还没刷新 (#4802)", () => {
    // the session (pushed first) is already on B; A's agents list, refreshed later, still has it
    const sMoved = mockSession("mac-1", "moved", "working", { todo: { id: "todo-b", title: "B" } });
    const sStays = mockSession("mac-1", "stays", "working", { todo: { id: "todo-a", title: "A" } });
    const todos: Todo[] = [
      {
        id: "todo-a",
        title: "待办 A",
        created_at: "2026-11-02T10:00:00Z",
        updated_at: "",
        agents: [
          { machine_id: "mac-1", session_id: "moved", linked_at: "" },
          { machine_id: "mac-1", session_id: "stays", linked_at: "" },
        ],
      },
      { id: "todo-b", title: "待办 B", created_at: "2026-11-02T09:00:00Z", updated_at: "", agents: [] },
    ];

    const groups = buildTodoAgentGroups([sMoved, sStays], machines, todos, mutes);
    expect(groups.map((g) => g.key)).toEqual(["todo:todo-a", "todo:todo-b"]);
    expect(groups[0].sessions.map((s) => s.session_id)).toEqual(["stays"]);
    expect(groups[1].sessions.map((s) => s.session_id)).toEqual(["moved"]);
  });

  it("会话挂的待办已完成时，归第一个列着它的未完成待办 (#4802)", () => {
    const s = mockSession("mac-1", "s1", "idle", { todo: { id: "todo-done", title: "旧" } });
    const todos: Todo[] = [
      { id: "todo-done", title: "已完成", created_at: "2026-11-02T08:00:00Z", updated_at: "", done_at: "2026-11-02T12:00:00Z", agents: [] },
      { id: "todo-open", title: "进行中", created_at: "2026-11-02T09:00:00Z", updated_at: "", agents: [{ machine_id: "mac-1", session_id: "s1", linked_at: "" }] },
    ];

    const groups = buildTodoAgentGroups([s], machines, todos, mutes);
    expect(groups.map((g) => g.key)).toEqual(["todo:todo-open"]);
    expect(groups[0].sessions.map((x) => x.session_id)).toEqual(["s1"]);
  });

  it("已完成的待办不生成 live 待办组", () => {
    const sDoneTask = mockSession("mac-1", "done-task", "idle", { todo: { id: "todo-done", title: "已完成" } });
    const todos: Todo[] = [
      {
        id: "todo-done",
        title: "已完成待办",
        created_at: "2026-11-02T08:00:00Z",
        updated_at: "",
        done_at: "2026-11-02T12:00:00Z",
        agents: [{ machine_id: "mac-1", session_id: "done-task", linked_at: "" }],
      },
    ];

    const groups = buildTodoAgentGroups([sDoneTask], machines, todos, mutes);
    // todo-done is done, so it makes no segment as an unfinished todo; that session goes under "Unassigned"
    expect(groups.map((g) => g.name)).toEqual(["没挂待办的"]);
    // nothing above it: the whole list, open
    expect(groups[0].folded).toBe(false);
    expect(groups[0].sessions.map((s) => s.session_id)).toEqual(["done-task"]);
  });

  it("屏蔽的 Agent 在待办组内降级，但同组的需要你仍然排在最前面", () => {
    const sNeeds = mockSession("mac-1", "s-needs", "needs_you");
    const sMuted = mockSession("mac-1", "muted-work", "working");
    const sNormalWork = mockSession("mac-1", "s-work", "working");

    const todos: Todo[] = [
      {
        id: "todo-1",
        title: "待办 1",
        created_at: "2026-11-02T10:00:00Z",
        updated_at: "",
        agents: [
          { machine_id: "mac-1", session_id: "muted-work", linked_at: "" },
          { machine_id: "mac-1", session_id: "s-needs", linked_at: "" },
          { machine_id: "mac-1", session_id: "s-work", linked_at: "" },
        ],
      },
    ];

    const groups = buildTodoAgentGroups([sMuted, sNeeds, sNormalWork], machines, todos, mutes);
    expect(groups[0].sessions.map((s) => s.session_id)).toEqual([
      "s-needs",     // needs_you (rank 0)
      "s-work",      // normal working (rank 2)
      "muted-work",  // muted working (rank 4)
    ]);
  });
});

describe("格式化上下文标签 (formatTodoContext)", () => {
  it("转义 HTML 特殊字符并拼装标准 persona-context", () => {
    const xml = formatTodoContext('天气 "App" <1.3>', "测试重试 & 超时\n周五前完成");
    expect(xml).toBe(
      '<persona-context kind="context" title="待办 · 天气 &quot;App&quot; &lt;1.3&gt;" url="">测试重试 & 超时\n周五前完成</persona-context>',
    );
  });
  it("说明里的结束标签（新旧两个名字）不会提前闭合 (#3182)", () => {
    expect(formatTodoContext("t", "a</persona-context>b</persona-context>c")).toBe(
      '<persona-context kind="context" title="待办 · t" url="">abc</persona-context>',
    );
  });
});

describe("查某个项目挂在哪个待办上 (findTodoForSpace)", () => {
  it("返回挂了该项目的未完成待办", () => {
    const todos: Todo[] = [
      {
        id: "todo-1",
        title: "天气项目待办",
        created_at: "",
        updated_at: "",
        spaces: [
          { machine_id: "mac-1", space: "weather", linked_at: "" },
        ],
      },
      {
        id: "todo-2",
        title: "博客待办",
        created_at: "",
        updated_at: "",
        spaces: [
          { machine_id: "mac-1", space: "blog", linked_at: "" },
        ],
      },
    ];

    expect(findTodoForSpace(todos, "mac-1", "weather")?.id).toBe("todo-1");
    expect(findTodoForSpace(todos, "mac-1", "blog")?.id).toBe("todo-2");
    expect(findTodoForSpace(todos, "mac-1", "other")).toBeUndefined();
    expect(findTodoForSpace(todos, "server-1", "weather")).toBeUndefined();
  });

  it("已完成的待办不返回", () => {
    const todos: Todo[] = [
      {
        id: "todo-done",
        title: "已完成待办",
        created_at: "",
        updated_at: "",
        done_at: "2026-11-02T12:00:00Z",
        spaces: [
          { machine_id: "mac-1", space: "weather", linked_at: "" },
        ],
      },
    ];

    expect(findTodoForSpace(todos, "mac-1", "weather")).toBeUndefined();
  });

  it("空入参安全返回 undefined", () => {
    expect(findTodoForSpace([], "mac-1", "weather")).toBeUndefined();
    expect(findTodoForSpace([], "", "")).toBeUndefined();
  });
});

describe("生成来源文案 (formatAgentSource)", () => {
  const spaces: TodoSpace[] = [
    { machine_id: "mac-1", space: "weather", linked_at: "" },
  ];

  it("手动挂的不标（返回 undefined）", () => {
    const a1: TodoAgent = { machine_id: "mac-1", session_id: "s1", linked_at: "", source: "manual" };
    const a2: TodoAgent = { machine_id: "mac-1", session_id: "s2", linked_at: "" };
    expect(formatAgentSource(a1)).toBeUndefined();
    expect(formatAgentSource(a2)).toBeUndefined();
  });

  it("随项目挂上的标「随项目 <项目名>」", () => {
    const a: TodoAgent = {
      machine_id: "mac-1",
      session_id: "s1",
      linked_at: "",
      source: "space",
      via: "mac-1/weather",
    };
    expect(formatAgentSource(a, spaces)).toBe("随项目 weather");
  });

  it("来源是 space 且 via 缺失时，通过 lookup 兜底取 Agent 会话自身 space_name", () => {
    const s = mockSession("mac-1", "s1", "idle", { space_name: "custom-space" });
    const lookup = makeSessionLookup([s]);
    const a: TodoAgent = {
      machine_id: "mac-1",
      session_id: "s1",
      linked_at: "",
      source: "space",
      via: "",
    };
    expect(formatAgentSource(a, spaces, lookup)).toBe("随项目 custom-space");
  });

  it("来源是 space 且 via 和 lookup 都缺失时只返回「随项目」，不随机挑同机第一个项目", () => {
    const aWithoutVia: TodoAgent = {
      machine_id: "mac-1",
      session_id: "s1",
      linked_at: "",
      source: "space",
      via: "",
    };
    // Even if spaces has a space on mac-1, we must NOT pick spaces[0]
    expect(formatAgentSource(aWithoutVia, spaces)).toBe("随项目");
  });

  it("派生的标「由 <父 Agent 标题> 派出」；父 Agent 找不到时只写「派生」", () => {
    const parent = mockSession("mac-1", "p1", "working", { title: "给天气 App 加主屏幕小组件" });
    const lookup = makeSessionLookup([parent]);

    const childWithParent: TodoAgent = {
      machine_id: "mac-1",
      session_id: "c1",
      linked_at: "",
      source: "spawned",
      via: "mac-1/p1",
    };
    expect(formatAgentSource(childWithParent, spaces, lookup)).toBe("由 给天气 App 加主屏幕小组件 派出");

    const childOrphan: TodoAgent = {
      machine_id: "mac-1",
      session_id: "c2",
      linked_at: "",
      source: "spawned",
      via: "mac-1/unknown-parent",
    };
    expect(formatAgentSource(childOrphan, spaces, lookup)).toBe("派生");

    const childNoVia: TodoAgent = {
      machine_id: "mac-1",
      session_id: "c3",
      linked_at: "",
      source: "spawned",
      via: "",
    };
    expect(formatAgentSource(childNoVia, spaces, lookup)).toBe("派生");
  });
});

describe("旧 hub 兼容判定 (isSpacesSupported)", () => {
  it("待办为空时算不确定，返回 false (#874 item 1)", () => {
    expect(isSpacesSupported([])).toBe(false);
  });

  it("有待办且待办中包含 spaces 数组时为 true", () => {
    const modernTodos: Todo[] = [
      { id: "t1", title: "现代待办", created_at: "", updated_at: "", spaces: [] },
    ];
    expect(isSpacesSupported(modernTodos)).toBe(true);
  });

  it("待办列表中各项均无 spaces 字段（旧 hub 格式）时为 false", () => {
    const legacyTodos: Todo[] = [
      { id: "t1", title: "旧待办", created_at: "", updated_at: "" },
    ];
    expect(isSpacesSupported(legacyTodos)).toBe(false);
  });
});

describe("弹窗关联目标判定 (resolveLinkCurrentTodoId)", () => {
  it("对于 space 目标，从 todos 列表中匹配对应项目的待办 ID", () => {
    const todos: Todo[] = [
      {
        id: "t_space",
        title: "项目待办",
        created_at: "",
        updated_at: "",
        spaces: [{ machine_id: "mac-1", space: "weather", linked_at: "" }],
      },
    ];
    expect(resolveLinkCurrentTodoId({ machineId: "mac-1", space: "weather" }, undefined, todos)).toBe("t_space");
    expect(resolveLinkCurrentTodoId({ machineId: "mac-1", space: "other" }, undefined, todos)).toBeUndefined();
  });

  it("对于 session 目标，即使 todos 尚未加载或加载失败（空列表），依然保留 session.todo.id (#874 item 4)", () => {
    const session = mockSession("mac-1", "s1", "idle");
    session.todo = { id: "todo_123", title: "关联待办" };

    // When todos is empty (not yet loaded), resolveLinkCurrentTodoId still returns session.todo.id
    expect(resolveLinkCurrentTodoId(undefined, session, [])).toBe("todo_123");
  });

  it("对于未关联待办的 session 目标，返回 undefined", () => {
    const session = mockSession("mac-1", "s2", "idle");
    expect(resolveLinkCurrentTodoId(undefined, session, [])).toBeUndefined();
  });
});

describe("自动完成 (docs/assistant/todos.md「自动完成」)", () => {
  const base = { id: "t", title: "统一连接器 logo 尺寸", created_at: "2026-11-04T06:00:00Z", updated_at: "2026-11-04T08:00:00Z" };
  it("hub 判断做完后关掉的任务标「自动完成」", () => {
    const todo = { ...base, done_at: "2026-11-04T08:00:00Z", done_by: "auto", done_reason: "已合并 PR 并发了 TestFlight" };
    expect(isAutoDone(todo)).toBe(true);
    expect(todoStatusLabel(todo, todoStatus(todo))).toBe("自动完成");
  });
  it("用户点的完成、重新打开以后都不算自动完成", () => {
    const byUser = { ...base, done_at: "2026-11-04T08:00:00Z" };
    expect(isAutoDone(byUser)).toBe(false);
    expect(todoStatusLabel(byUser, "done")).toBe("已完成");
    const reopened = { ...base, done_at: null, done_by: "auto", reopened_at: "2026-11-04T09:00:00Z" };
    expect(isAutoDone(reopened)).toBe(false);
    expect(todoStatusLabel(reopened, todoStatus(reopened))).toBe("未开始");
  });
});

describe("交付 (docs/assistant/todos.md「交付」)", () => {
  const base = { id: "t", title: "修登录", created_at: "2026-11-06T06:00:00Z", updated_at: "2026-11-06T08:00:00Z" };
  it("没做到上线的开着的任务显示卡在哪", () => {
    const todo = { ...base, delivery: { state: "pr", gate: "等用户批准合并", unblock: "user" } };
    expect(heldDelivery(todo)?.gate).toBe("等用户批准合并");
    expect(heldDelivery({ ...base, delivery: { state: "merged" } })?.state).toBe("merged");
  });
  it("上线了、不涉及上线、已经关掉的都不显示", () => {
    expect(heldDelivery({ ...base, delivery: { state: "live" } })).toBeNull();
    expect(heldDelivery({ ...base, delivery: { state: "none" } })).toBeNull();
    expect(heldDelivery({ ...base, done_at: "2026-11-06T09:00:00Z", delivery: { state: "pr" } })).toBeNull();
    expect(heldDelivery(base)).toBeNull();
  });
});

describe("主对话任务坞里的手动 Agent (dockAgents)", () => {
  const task = (id: string, agents: [string, string][], patch: Partial<Todo> = {}): Todo => ({
    id,
    title: id,
    created_at: "2026-11-04T08:00:00Z",
    updated_at: "2026-11-04T08:00:00Z",
    origin: "main",
    agents: agents.map(([machine_id, session_id]) => ({ machine_id, session_id, linked_at: "2026-11-04T08:00:00Z" })),
    ...patch,
  });

  it("只在主对话里出现，其他对话没有", () => {
    const s = [mockSession("mac-1", "a", "working")];
    expect(dockAgents(s, [], "main", "main").map((x) => x.session.session_id)).toEqual(["a"]);
    expect(dockAgents(s, [], "other", "main")).toEqual([]);
    expect(dockAgents(s, [], "main", undefined)).toEqual([]);
  });

  it("只带在做和等你的，去掉 Persona 自己的对话", () => {
    const s = [
      mockSession("mac-1", "w", "working"),
      mockSession("mac-1", "n", "needs_you"),
      mockSession("mac-1", "u", "unread"),
      mockSession("mac-1", "i", "idle"),
      mockSession("mac-1", "o", "offline"),
      mockSession("mac-1", "p", "working", { assistant: true }),
    ];
    expect(dockAgents(s, [], "main", "main").map((x) => x.session.session_id)).toEqual(["n", "w"]);
  });

  it("任务挂着的 Agent 不重复出现（不管哪段对话开的），任务完成或放开后才算手动的", () => {
    const s = [mockSession("mac-1", "a", "working"), mockSession("mac-1", "b", "working"), mockSession("mac-1", "c", "working"), mockSession("mac-1", "d", "working")];
    const todos = [
      task("t1", [["mac-1", "a"]]),
      task("t2", [["mac-1", "b"]], { origin: "other" }),
      task("t3", [["mac-1", "c"]], { done_at: "2026-11-04T09:00:00Z" }),
      { ...task("t4", []), agents: [{ machine_id: "mac-1", session_id: "d", linked_at: "x", released_at: "y" }] },
    ];
    expect(dockAgents(s, todos, "main", "main").map((x) => x.session.session_id)).toEqual(["c", "d"]);
  });

  it("hub 标了待办的：手动建的待办照样算手动的，任务（含列表还没刷新到的）不算", () => {
    const s = [
      mockSession("mac-1", "m", "working", { todo: { id: "hand", title: "手动" } }),
      mockSession("mac-1", "t", "working", { todo: { id: "t1", title: "任务" } }),
      mockSession("mac-1", "x", "working", { todo: { id: "not-yet", title: "新任务" } }),
    ];
    const todos = [task("hand", [], { origin: undefined }), task("t1", [])];
    expect(dockAgents(s, todos, "main", "main").map((x) => x.session.session_id)).toEqual(["m"]);
  });

  it("等你的在前，同一状态按进入状态的时间新的在前", () => {
    const s = [
      mockSession("mac-1", "old", "working", { state_since: "2026-11-04T08:00:00Z" }),
      mockSession("mac-1", "new", "working", { state_since: "2026-11-04T09:00:00Z" }),
      mockSession("mac-1", "ask", "needs_you", { state_since: "2026-11-04T07:00:00Z" }),
    ];
    expect(dockAgents(s, [], "main", "main").map((x) => x.session.session_id)).toEqual(["ask", "new", "old"]);
  });
});

describe("模型服务出错中断的任务 (#1516)", () => {
  const cut = "API Error: Connection lost mid-response. The response above may be incomplete.";
  const todo: Todo = {
    id: "t1",
    title: "任务完成卡片摘要渲染 Markdown",
    created_at: "2026-11-02T10:00:00Z",
    updated_at: "2026-11-02T10:00:00Z",
    origin: "conv1",
    agents: [{ machine_id: "mac-1", session_id: "s1", linked_at: "2026-11-02T10:00:00Z" }],
  };

  it("这一轮以 API 错误结束：中断了，不是待查看", () => {
    const s = mockSession("mac-1", "s1", "unread", { last_text: cut, turn_error: cut });
    expect(todoStatus(todo, makeSessionLookup([s]))).toBe("interrupted");
    expect(TODO_STATUS_INFO.interrupted).toEqual({ label: "中断了", tone: "red" });
    expect(isRemovable("interrupted")).toBe(true);
    // still interrupted (not finished) after the user has seen it
    expect(todoStatus(todo, makeSessionLookup([mockSession("mac-1", "s1", "idle", { turn_error: cut })]))).toBe("interrupted");
  });

  it("重新跑起来回到进行中；正常做完是待查看", () => {
    expect(todoStatus(todo, makeSessionLookup([mockSession("mac-1", "s1", "working", { last_text: cut, turn_error: cut })]))).toBe("working");
    expect(todoStatus(todo, makeSessionLookup([mockSession("mac-1", "s1", "unread", { last_text: "修好了" })]))).toBe("unread");
  });

  it("等你优先于中断；中断排在待查看前面", () => {
    const two: Todo = { ...todo, agents: [...todo.agents!, { machine_id: "mac-1", session_id: "s2", linked_at: "" }] };
    const sCut = mockSession("mac-1", "s1", "unread", { turn_error: cut });
    expect(todoStatus(two, makeSessionLookup([sCut, mockSession("mac-1", "s2", "needs_you")]))).toBe("needs_you");
    expect(todoStatus(two, makeSessionLookup([sCut, mockSession("mac-1", "s2", "unread")]))).toBe("interrupted");
    expect(mostUrgentAgent(two, makeSessionLookup([sCut, mockSession("mac-1", "s2", "unread")]))?.agent.session_id).toBe("s1");
  });

  it("说清为什么中断、继续有没有用", () => {
    expect(interruption(mockSession("mac-1", "s1", "unread", { turn_error: cut }))).toEqual({ reason: "连不上模型服务", canContinue: true });
    expect(interruption(mockSession("mac-1", "s1", "unread", { turn_error: "API Error: 401 authentication_error" }))).toEqual({
      reason: "登录过期了，要在电脑上重新 /login",
      canContinue: false,
    });
    expect(interruption(mockSession("mac-1", "s1", "unread", { last_text: "修好了" }))).toBeUndefined();
  });
});

describe("任务卡片的进度 (taskNow, #1583)", () => {
  const base: Session = { machine_id: "mac-a", session_id: "s1", title: "", cwd: "/code", status: "working", state: "working" };

  it("只调了工具、还没说过话：显示这一步和开始时间，不是「还没有动静」", () => {
    const s = { ...base, activity: "在跑测试", activity_at: "2026-11-04T11:50:00Z" };
    expect(taskNow(s, true)).toEqual({ kind: "step", text: "在跑测试", since: "2026-11-04T11:50:00Z" });
  });

  it("说过一句话后接着调工具：工作中显示这一步，停下来显示那句话", () => {
    const s = { ...base, last_text: "已建 issue 并认领", activity: "在读 tasks.go", activity_at: "2026-11-04T11:50:00Z" };
    expect(taskNow(s, true)).toEqual({ kind: "step", text: "在读 tasks.go", since: "2026-11-04T11:50:00Z" });
    expect(taskNow({ ...s, state: "unread" }, false)).toEqual({ kind: "words", text: "已建 issue 并认领" });
  });

  it("停在一次工具调用上、从没说过话：还是显示这一步，但不计时", () => {
    expect(taskNow({ ...base, activity: "在跑测试", activity_at: "2026-11-04T11:50:00Z" }, false)).toEqual({ kind: "step", text: "在跑测试", since: undefined });
  });

  it("有了新的话（hub 不再下发这一步）就显示话；什么都没有是 null", () => {
    expect(taskNow({ ...base, last_text: "测试过了" }, true)).toEqual({ kind: "words", text: "测试过了" });
    expect(taskNow(base, true)).toBeNull();
    expect(taskNow(undefined, true)).toBeNull();
  });

  it("有子 Agent：主 Agent 的进度不变，在跑的子 Agent 另外列出来", () => {
    const s: Session = {
      ...base,
      last_text: "两个只读子任务在调查",
      activity: "在搜 Shutdown",
      subagents: [
        { id: "a1", session_id: "s1@a1", description: "Hub children lifecycle survey", running: true, last_text: "子任务的话" },
        { id: "a2", session_id: "s1@a2", type: "Explore", running: true },
        { id: "a3", session_id: "s1@a3", description: "做完的", running: false },
      ],
    };
    expect(taskNow(s, true)).toEqual({ kind: "step", text: "在搜 Shutdown", since: undefined });
    expect(runningSubagents(s)).toEqual(["Hub children lifecycle survey", "Explore"]);
    expect(runningSubagents(base)).toEqual([]);
  });
});
