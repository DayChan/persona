// @vitest-environment jsdom
// "By to-do": a push renders again only the branch it changed (shareTree + memoized rows), a machines push (load) none,
// and opening another agent only the branches it leaves and enters.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
// every tree node (project, to-do, agent) reads its fold once per render: counted by the node's key
const renders = vi.hoisted(() => [] as string[]);
vi.mock("../lib/use-folded", async (load) => {
  const real = await load<typeof import("../lib/use-folded")>();
  return { ...real, useFolded: (key: string, ...rest: Parameters<typeof real.useFolded> extends [string, ...infer R] ? R : never) => (renders.push(key), real.useFolded(key, ...rest)) };
});
import { buildTodoTree, shareTree, type TodoTree } from "../lib/todo-tree";
import type { Todo } from "../lib/todos";
import type { Machine, Session } from "../lib/types";
import { TodoTreeView } from "./SidebarTree";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const at = new Date().toISOString();
const machines: Machine[] = [{ id: "m1", name: "Mac", online: true }, { id: "m2", name: "Linux", online: true }];
const s = (sid: string, cwd: string, patch: Partial<Session> = {}): Session => ({ machine_id: "m1", session_id: sid, title: sid, cwd, status: "working", state: "working", updated_at: at, ...patch });
const link = (sid: string) => ({ machine_id: "m1", session_id: sid, linked_at: at });
const todos: Todo[] = [
  { id: "t1", title: "t1", created_at: at, updated_at: at, agents: [link("a"), link("b")] },
  { id: "t2", title: "t2", created_at: at, updated_at: at, agents: [link("c"), link("d")] },
  // untouched by the pushes and the switch below: a second to-do in project one, and a third project
  { id: "t3", title: "t3", created_at: at, updated_at: at, agents: [link("e")] },
  { id: "t4", title: "t4", created_at: at, updated_at: at, agents: [link("f")] },
];
const sessions = [s("a", "/code/one"), s("b", "/code/one"), s("c", "/code/two"), s("d", "/code/two"), s("e", "/code/one"), s("f", "/code/three")];
const stable = { reg: () => () => {}, hover: () => {}, go: () => () => {}, onRowMenu: () => {} };

let host: HTMLDivElement;
let root: Root;
let tree: TodoTree | undefined;
function show(all: Session[], ms: Machine[], activeKey: string) {
  tree = shareTree(tree, buildTodoTree(all.map((x) => ({ ...x })), ms, todos, { spaces: [], sessions: [] }));
  renders.length = 0;
  act(() => root.render(<TodoTreeView tree={tree!} machines={ms} activeKey={activeKey} {...stable} />));
  // distinct: a node that comes to hold the open row (or stops) renders twice in a row (useFolded adjusts while rendering)
  return [...new Set(renders)].sort();
}

beforeEach(() => {
  localStorage.clear();
  tree = undefined;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("the to-do tree's rows", () => {
  it("render again only where a push or the open agent changed them", () => {
    const first = show(sessions, machines, "s:m1/a");
    expect(first).toHaveLength(13); // three projects, four to-dos, six agents
    // one agent's push: its project, its to-do and its own row
    expect(show(sessions.map((x) => (x.session_id === "d" ? { ...x, state: "idle", status: "idle" } : x)), machines, "s:m1/a")).toEqual(["s:m1/d:children", "todo-tree:project:two", "todo-tree:todo:t2"]);
    // a machines push with the same names: nothing
    expect(show(sessions.map((x) => (x.session_id === "d" ? { ...x, state: "idle", status: "idle" } : x)), machines.map((m) => ({ ...m, last_seen: at })), "s:m1/a")).toEqual([]);
    // opening c: the branch that held a and the one that holds c (not t3 beside a, nor project three)
    expect(show(sessions.map((x) => (x.session_id === "d" ? { ...x, state: "idle", status: "idle" } : x)), machines, "s:m1/c")).toEqual([
      "s:m1/a:children", "s:m1/c:children", "todo-tree:project:one", "todo-tree:project:two", "todo-tree:todo:t1", "todo-tree:todo:t2",
    ]);
    // and the row says which agent is open
    expect(host.querySelector('[aria-current="page"]')?.textContent ?? "").toContain("c");
  });
});
