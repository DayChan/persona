import { describe, expect, it } from "vitest";
import { agentTreeState, buildTodoTree, groupTodosByProject, nestAgents, projectOf, STALE_MS, todoTreeState, treeStateLabel } from "./todo-tree";
import { makeSessionLookup, type Todo } from "./todos";
import { matchesFilter } from "./agent-filter";
import { sessionSource } from "./agent-groups";
import type { Machine, Session } from "./types";

const NOW = Date.parse("2026-11-05T12:00:00Z");
const fresh = new Date(NOW - 60_000).toISOString();
const old = new Date(NOW - STALE_MS - 60_000).toISOString();
const machines: Machine[] = [{ id: "m1", name: "Mac", online: true }];
const s = (sid: string, state: Session["state"], cwd: string, patch: Partial<Session> = {}): Session => ({
  machine_id: "m1", session_id: sid, title: sid, cwd, status: state === "working" ? "working" : "idle", state, updated_at: fresh, ...patch,
});
const todo = (id: string, agents: Todo["agents"], patch: Partial<Todo> = {}): Todo => ({ id, title: id, created_at: fresh, updated_at: fresh, agents, ...patch });
const link = (sid: string, patch: object = {}) => ({ machine_id: "m1", session_id: sid, linked_at: fresh, ...patch });
const noMutes = { spaces: [], sessions: [] };

describe("项目 › 待办 › Agent 三层树 (#2526)", () => {
  it("待办按 Agent 所在仓库归到项目，未归属放最后", () => {
    const all = [s("a", "working", "/code/persona"), s("b", "unread", "/code/persona/.worktrees/x"), s("c", "needs_you", "/code/botmux"), s("loose", "idle", "/code/persona")];
    const todos = [todo("t1", [link("a")]), todo("t2", [link("b")]), todo("t3", [link("c")])];
    const tree = buildTodoTree(all, machines, todos, noMutes, NOW);
    expect(tree.projects.map((p) => [p.name, p.state, p.todos.map((t) => t.todoId)])).toEqual([
      ["botmux", "needs_you", ["t3"]],
      // in sortTodos's order: Working (t1) before Finished (t2), the task board's (#4896)
      ["persona", "working", ["t1", "t2"]],
    ]);
    expect(tree.loose.at(-1)?.projects.map((p) => [p.name, p.agents.map((a) => a.session.session_id)])).toEqual([["persona", ["loose"]]]);
  });

  it("派生的 Agent 挂在派它的 Agent 下，状态向上汇总", () => {
    const all = [s("parent", "idle", "/code/p"), s("child", "needs_you", "/code/p"), s("grand", "working", "/code/p")];
    const td = todo("t", [link("parent"), link("child", { source: "spawned", via: "m1/parent" }), link("grand", { source: "spawned", via: "m1/child" })]);
    const roots = nestAgents(all, td, NOW);
    expect(roots).toHaveLength(1);
    expect(roots[0]!.state).toBe("needs_you");
    expect(roots[0]!.children[0]!.session.session_id).toBe("child");
    expect(roots[0]!.children[0]!.children[0]!.session.session_id).toBe("grand");
  });

  it("父 Agent 不在列表里或成环时留在顶层", () => {
    const all = [s("x", "idle", "/p"), s("y", "idle", "/p")];
    const td = todo("t", [link("x", { source: "spawned", via: "m1/y" }), link("y", { source: "spawned", via: "m1/x" })]);
    expect(nestAgents(all, td, NOW)).toHaveLength(2);
    expect(nestAgents([s("z", "idle", "/p")], todo("t", [link("z", { source: "spawned", via: "m1/gone" })]), NOW)).toHaveLength(1);
  });

  it("做完且超过 1 天没动的分支默认折叠，在做的不折叠", () => {
    const all = [s("quiet", "idle", "/code/a", { updated_at: old }), s("busy", "working", "/code/b", { updated_at: old })];
    const tree = buildTodoTree(all, machines, [todo("t1", [link("quiet")]), todo("t2", [link("busy")])], noMutes, NOW);
    const byName = Object.fromEntries(tree.projects.map((p) => [p.name, p]));
    expect(byName.a!.folded).toBe(true);
    expect(byName.a!.todos[0]!.state).toBe("done");
    expect(byName.b!.folded).toBe(false);
  });

  it("手动开的（#2623）在未归属前面，各自一段", () => {
    const all = [s("a", "working", "/code/p"), s("hand", "idle", "/code/p", { space_kind: "herdr" }), s("bot", "idle", "/code/q", { pane_id: "botmux:x" })];
    const tree = buildTodoTree(all, machines, [todo("t1", [link("a")])], noMutes, NOW);
    expect(tree.loose.map((g) => [g.key, g.sessions.map((x) => x.session_id)])).toEqual([["todo:manual", ["hand"]], ["todo:none", ["bot"]]]);
    // the loose groups keep the line saying why their agents are there (#3057)
    expect(tree.loose[0].hint).toContain("在终端（herdr）里开的");
    expect(tree.loose[1].hint).toContain("没挂在任何待办上");
  });

  it("未归属里有等你（含中断）时展开", () => {
    const all = [s("a", "working", "/code/p"), s("cut", "idle", "/code/p", { turn_error: "overloaded" })];
    const tree = buildTodoTree(all, machines, [todo("t1", [link("a")])], noMutes, NOW);
    expect(tree.loose.at(-1)?.state).toBe("needs_you");
    expect(tree.loose.at(-1)?.folded).toBe(false);
  });

  it("projectOf 取多数仓库", () => {
    expect(projectOf(["/r/a", "/r/b", "/r/b/.claude/worktrees/x"])).toBe("b");
    expect(projectOf([])).toBe("");
  });

  it("待办页按项目分段，没 Agent 的放最后", () => {
    const all = [s("a", "working", "/code/persona")];
    const groups = groupTodosByProject([todo("t1", [link("a")]), todo("t2", [])], makeSessionLookup(all), NOW);
    expect(groups.map((g) => [g.name, g.state, g.todos.map((t) => t.id)])).toEqual([["persona", "working", ["t1"]], ["还没有 Agent", "done", ["t2"]]]);
  });
});

describe("来源：飞书群 / 终端 / 桌面端 (#2526)", () => {
  it("botmux 是飞书群，终端引擎的窗格是终端，桌面 App 自己的对话是桌面端，其余没有", () => {
    expect(sessionSource(s("b", "idle", "/", { pane_id: "botmux:abc" }))).toBe("feishu");
    expect(sessionSource(s("h", "idle", "/", { pane_id: "w1:p1" }))).toBe("terminal");
    expect(sessionSource(s("c", "idle", "/", { pane_id: "herdr-core:7" }))).toBe("terminal");
    expect(sessionSource(s("o", "idle", "/", { pane_id: "orca:1" }))).toBe("terminal");
    expect(sessionSource(s("x", "idle", "/", { space_kind: "codex_app", source_kind: "codex_history" }))).toBe("desktop");
    expect(sessionSource(s("p", "idle", "/", { host: "paseo", source_kind: "runtime", source_instance_id: "paseo" }))).toBe("desktop");
    expect(sessionSource(s("q", "idle", "/", { source_kind: "runtime", source_instance_id: "orca", space_kind: "orca" }))).toBe("desktop");
    expect(sessionSource(s("d", "idle", "/", { source_kind: "runtime", source_instance_id: "dsh" }))).toBeUndefined();
    expect(sessionSource(s("n", "idle", "/"))).toBeUndefined();
    expect(matchesFilter(s("b", "idle", "/", { pane_id: "botmux:abc" }), "feishu", noMutes)).toBe(true);
    expect(matchesFilter(s("h", "idle", "/", { pane_id: "w1:p1" }), "feishu", noMutes)).toBe(false);
    expect(matchesFilter(s("x", "idle", "/", { space_kind: "codex_app" }), "terminal", noMutes)).toBe(false);
  });
});

describe("a usage limit rolls up as Waiting, not Working", () => {
  const reset = new Date(Date.now() + 3 * 3600_000).toISOString();
  const limited = s("lim", "idle", "/code/weather", { turn_error: "You've hit your usage limit", turn_error_kind: "usage_limit", turn_error_resets_at: reset, turn_error_resumes: true });

  it("is Waiting for the agent, the todo and a project that has only that", () => {
    expect(agentTreeState(limited)).toBe("waiting");
    expect(todoTreeState("usage_limit", [limited])).toBe("waiting");
    expect(treeStateLabel("waiting")).toBe("待唤醒");
    const tree = buildTodoTree([limited], machines, [todo("t", [link("lim")])], noMutes, NOW);
    expect(tree.projects.map((p) => [p.name, p.state, p.todos.map((td) => td.state)])).toEqual([["weather", "waiting", ["waiting"]]]);
  });

  it("does not hide a todo that is actually working", () => {
    const busy = s("busy", "working", "/code/weather");
    const tree = buildTodoTree([limited, busy], machines, [todo("t-lim", [link("lim")]), todo("t-busy", [link("busy")])], noMutes, NOW);
    const project = tree.projects.find((p) => p.name === "weather");
    expect(project?.state).toBe("working");
    expect(project?.todos.map((td) => td.state).sort()).toEqual(["waiting", "working"]);
  });
});

describe("waiting in the background in the tree (#6045)", () => {
  const sess = (id: string, patch: Partial<Session>): Session => ({ machine_id: "m", session_id: id, state: "working", status: "done", ...patch }) as Session;
  it("ranks between Working and Done, for an agent and for a todo whose agents only wait", () => {
    const waiting = sess("w", { background_waiting: true });
    const busy = sess("b", {});
    expect(agentTreeState(waiting)).toBe("bg_waiting");
    expect(agentTreeState(sess("s", { background_waiting: true, starting: true }))).toBe("working");
    expect(todoTreeState("working", [waiting])).toBe("bg_waiting");
    expect(todoTreeState("working", [waiting, busy])).toBe("working");
    expect(todoTreeState("working")).toBe("working");
    expect(todoTreeState("needs_you", [waiting])).toBe("needs_you");
  });
});
