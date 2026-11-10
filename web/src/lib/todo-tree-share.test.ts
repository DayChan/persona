// shareTree hands a rebuilt "By to-do" tree's unchanged nodes on as last time's objects, so the memoized rows under them
// do not render again (SidebarTree.tsx); the tree it returns is the new one in every value.
import { describe, expect, it } from "vitest";
import { buildTodoTree, shareTree, type TodoTree } from "./todo-tree";
import type { Todo } from "./todos";
import type { Machine, Session } from "./types";

const NOW = Date.parse("2026-11-05T12:00:00Z");
const at = new Date(NOW - 60_000).toISOString();
const machines: Machine[] = [{ id: "m1", name: "Mac", online: true }, { id: "m2", name: "Linux", online: true }];
const s = (sid: string, cwd: string, patch: Partial<Session> = {}): Session => ({ machine_id: "m1", session_id: sid, title: sid, cwd, status: "working", state: "working", updated_at: at, ...patch });
const link = (sid: string, patch: object = {}) => ({ machine_id: "m1", session_id: sid, linked_at: at, ...patch });
const todos: Todo[] = [
  { id: "t1", title: "t1", created_at: at, updated_at: at, agents: [link("a"), link("a2", { source: "spawned", via: "m1/a" }), link("b")] },
  { id: "t2", title: "t2", created_at: at, updated_at: at, agents: [link("c")] },
];
const sessions = [s("a", "/code/one"), s("a2", "/code/one"), s("b", "/code/one"), s("c", "/code/two"), s("loose", "/code/two")];
const build = (all: Session[], ms = machines) => buildTodoTree(all.map((x) => ({ ...x })), ms.map((m) => ({ ...m })), todos, { spaces: [], sessions: [] }, NOW);

const project = (tree: TodoTree, name: string) => tree.projects.find((p) => p.name === name)!;

describe("shareTree", () => {
  it("keeps the whole tree when a push changed nothing in it", () => {
    const first = build(sessions);
    expect(shareTree(first, build(sessions))).toBe(first);
    // a machines push (its load, when it was last seen) leaves every node the same
    expect(shareTree(first, build(sessions, machines.map((m) => ({ ...m, last_seen: at }))))).toBe(first);
    expect(shareTree(undefined, first)).toBe(first);
  });

  it("renews only the nodes on the changed agent's path, with the new tree's values", () => {
    const first = build(sessions);
    const next = build(sessions.map((x) => (x.session_id === "a2" ? { ...x, state: "needs_you" } : x)));
    const shared = shareTree(first, next);
    expect(shared).toEqual(next);
    expect(shared).not.toBe(first);
    // the other project, the loose agents, and the changed project's other agent stay last time's objects
    expect(project(shared, "two")).toBe(project(first, "two"));
    expect(shared.loose).toBe(first.loose);
    const [before, after] = [project(first, "one").todos[0]!, project(shared, "one").todos[0]!];
    expect(after).not.toBe(before);
    expect(after.agents.find((a) => a.session.session_id === "b")).toBe(before.agents.find((a) => a.session.session_id === "b"));
    expect(after.agents.find((a) => a.session.session_id === "a")).not.toBe(before.agents.find((a) => a.session.session_id === "a"));
  });

  it("takes the new tree's shape when agents come and go", () => {
    const first = build(sessions);
    const next = build([...sessions.filter((x) => x.session_id !== "b"), s("d", "/code/three")]);
    const shared = shareTree(first, next);
    expect(shared).toEqual(next);
    expect(project(shared, "two")).toBe(project(first, "two"));
  });
});
