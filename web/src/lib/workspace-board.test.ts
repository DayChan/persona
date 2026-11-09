import { describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { BoardModel, boundCard, cardSessions, columnCards, directory, sameDirectory, selectedColumn, optimisticBoard, type BoardCard, type BoardState, type Workspace } from "./workspace-board";
import { href, parse } from "./router";
import type { Session } from "./types";
const card = (id: string, patch: Partial<BoardCard> = {}): BoardCard => ({ id, machine_id: "computer", workspace_id: `space-${id}`, title: id, cwd: `/projects/${id}`, source: "directory", column_id: "todo", pinned: false, archived: false, ...patch });
const initial = (): BoardState => ({ revision: 3, can_write: true, columns: [{ id: "todo", title: "待办" }, { id: "done", title: "已完成" }], cards: [card("a"), card("b"), card("c", { pinned: true }), card("old", { archived: true })] });
const session = (mid: string, cwd: string, status = "idle"): Session => ({ machine_id: mid, session_id: `${mid}-${cwd}`, cwd, title: "Agent", status });
function deferred<T>() { let resolve!: (v: T) => void, reject!: (e: unknown) => void; const promise = new Promise<T>((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }

describe("workspace board projection", () => {
  it("has a native route", () => { expect(parse("/workspace-board")).toEqual({ name: "workspace-board" }); expect(href({ name: "workspace-board" })).toBe("/workspace-board"); });
  it("selects one narrow-screen column and falls back after that column is deleted", () => {
    const board = initial();
    expect(selectedColumn(board, "done")).toBe("done");
    expect(selectedColumn(board, "removed")).toBe("todo");
    expect(selectedColumn({ ...board, columns: board.columns.slice(0, 1) }, "done")).toBe("todo");
    expect(selectedColumn({ ...board, columns: [] }, "done")).toBe("");
  });
  it("keeps persisted manual order under pinned grouping, filtering and archives", () => {
    const board = initial();
    expect(columnCards(board, "todo").map((c) => c.id)).toEqual(["c", "a", "b"]);
    expect(columnCards(board, "todo", { search: "/projects/b" }).map((c) => c.id)).toEqual(["b"]);
    expect(columnCards(board, "todo", { machine: "another" })).toEqual([]);
    expect(columnCards(board, "todo", { archived: true }).map((c) => c.id)).toEqual(["old"]);
    expect(board.cards.map((c) => c.id)).toEqual(["a", "b", "c", "old"]);
  });
  it("aggregates all Agents only on the same machine and directory without using activity as workflow", () => {
    const c = card("a");
    const agents = [session("computer", c.cwd), session("computer", `${c.cwd}/`, "working"), session("another", c.cwd), session("computer", `${c.cwd}-other`)];
    expect(cardSessions(c, agents)).toEqual(agents.slice(0, 2));
    expect(c.column_id).toBe("todo");
    expect(columnCards(initial(), "todo").map((c) => c.id)).toEqual(["c", "a", "b"]);
  });
  it("prefers the node-verified physical directory when an Agent uses a symlink alias", () => {
    const c = card("a", { cwd: "/private/tmp/project" });
    const aliased = { ...session("computer", "/tmp/project"), workspace_cwd: "/private/tmp/project" };
    expect(cardSessions(c, [aliased])).toEqual([aliased]);
    expect(cardSessions({ ...c, cwd: "/tmp/project" }, [aliased])).toEqual([]);
    expect(cardSessions(c, [session("computer", "/private/tmp/project")])).toHaveLength(1);
  });
  it("matches the hub's Windows drive, UNC and POSIX canonical paths", () => {
    const cases = [["C:\\repo\\one\\..\\two\\", "c:/repo/two"], ["\\\\Server\\Share\\repo\\..\\two", "//server/share/two"], ["C:\\", "c:/"], ["/a/../b//", "/b"], ["/unix/back\\slash", "/unix/back\\slash"]];
    for (const [input, expected] of cases) expect(directory(input)).toBe(expected);
    for (const input of ["C:repo", "C:", String.raw`\repo`, String.raw`\\server`, "relative", ""]) expect(directory(input)).toBeNull();
    expect(sameDirectory("", "relative")).toBe(false);
    const c = card("windows", { cwd: "c:/repo/two" });
    expect(cardSessions(c, [session("computer", "C:\\repo\\one\\..\\two\\")])).toHaveLength(1);
    expect(boundCard({ ...initial(), cards: [c] }, { machine_id: "computer", cwd: "C:\\repo\\two\\" } as Workspace)?.id).toBe(c.id);
  });
  it("recognizes archived bindings for restoration, scoped to a machine", () => {
    const board = initial(), w = { machine_id: "computer", cwd: "/projects/old/" } as Workspace;
    expect(boundCard(board, w)?.archived).toBe(true);
    expect(boundCard(board, { ...w, machine_id: "another" })).toBeUndefined();
  });
  it("moves within and between columns without changing identity or mutating inputs", () => {
    const board = initial();
    const moved = optimisticBoard(board, { action: "move_card", id: "b", column_id: "todo", before_id: "a" });
    expect(columnCards(moved, "todo").map((c) => c.id)).toEqual(["c", "b", "a"]);
    const crossed = optimisticBoard(moved, { action: "move_card", id: "b", column_id: "done" });
    expect(columnCards(crossed, "done")[0]).toEqual({ ...board.cards[1], column_id: "done" });
    expect(board.cards[1].column_id).toBe("todo");
    expect(optimisticBoard(board, { action: "update_card", id: "a", archived: true }).cards[0].archived).toBe(true);
    expect(board.cards[0].archived).toBe(false);
  });
});

describe("workspace board HTTP contract", () => {
  it("posts only revision and metadata for movement; no session operation is invoked", async () => {
    const request = vi.fn(async (path: string, init?: { json?: unknown }) => path === "/workspaces" ? { workspaces: [] } : init ? { ...initial(), revision: 4 } : initial());
    const model = new BoardModel(request as never); await model.refresh();
    await model.mutate({ action: "move_card", id: "a", column_id: "done", before_id: "b" });
    expect(request.mock.calls).toContainEqual(["/workspace-board", { method: "POST", json: { revision: 3, action: "move_card", id: "a", column_id: "done", before_id: "b" } }]);
    expect(request.mock.calls.every(([path]) => ["/workspace-board", "/workspaces"].includes(path))).toBe(true);
  });
  it("rolls back optimistic edits on failure and blocks concurrent writes", async () => {
    const write = deferred<BoardState>();
    const request = vi.fn(async (path: string, init?: unknown) => path === "/workspaces" ? { workspaces: [] } : init ? write.promise : initial());
    const model = new BoardModel(request as never); await model.refresh();
    const pending = model.mutate({ action: "move_card", id: "a", column_id: "done" });
    expect(model.snapshot().board?.cards.find((c) => c.id === "a")?.column_id).toBe("done");
    expect(await model.mutate({ action: "update_card", id: "b", pinned: true })).toBe(false);
    write.reject(new ApiError(500, "storage failed", {}));
    expect(await pending).toBe(false); expect(model.snapshot().board).toEqual(initial()); expect(model.snapshot().busy).toBe(false);
  });
  it("reloads on revision conflict and explains it", async () => {
    let reads = 0;
    const request = vi.fn(async (path: string, init?: unknown) => { if (path === "/workspaces") return { workspaces: [] }; if (init) throw new ApiError(409, "conflict", {}); return { ...initial(), revision: ++reads === 1 ? 3 : 5 }; });
    const model = new BoardModel(request as never); await model.refresh();
    expect(await model.mutate({ action: "update_card", id: "a", pinned: true })).toBe(false);
    expect(model.snapshot().board?.revision).toBe(5); expect(model.snapshot().error).toContain("已经重新加载");
  });
  it("honors server can_write on first load and 403 on revoked access", async () => {
    const request = vi.fn(async (path: string) => path === "/workspaces" ? { workspaces: [] } : { ...initial(), can_write: false });
    const readonly = new BoardModel(request as never); await readonly.refresh(); expect(readonly.snapshot().readonly).toBe(true);
    expect(await readonly.mutate({ action: "add_column", title: "x" })).toBe(false); expect(request).toHaveBeenCalledTimes(2);
    const revoked = new BoardModel((async (path: string, init?: unknown) => { if (init) throw new ApiError(403, "denied", {}); return path === "/workspaces" ? { workspaces: [] } : initial(); }) as never);
    await revoked.refresh(); await revoked.mutate({ action: "update_card", id: "a", pinned: true }); expect(revoked.snapshot().readonly).toBe(true); expect(revoked.snapshot().board).toEqual(initial());
  });
  it("recovers editing when a successful GET grants full access again", async () => {
    let canWrite = false;
    const model = new BoardModel((async (path: string) => path === "/workspaces" ? { workspaces: [] } : { ...initial(), can_write: canWrite }) as never);
    await model.refresh(); expect(model.snapshot().readonly).toBe(true);
    canWrite = true; await model.refresh(); expect(model.snapshot().readonly).toBe(false);
  });
  it("retains cards when discovery fails and describes unsupported hubs", async () => {
    const model = new BoardModel((async (path: string) => { if (path === "/workspaces") throw new Error("node unsupported"); return initial(); }) as never);
    await model.refresh(); expect(model.snapshot().board?.cards).toHaveLength(4); expect(model.snapshot().discoveryError).toContain("unsupported");
    const old = new BoardModel((async () => { throw new ApiError(503, "unsupported", {}); }) as never); await old.refresh(); expect(old.snapshot().error).toContain("更新 hub");
  });
  it("does not let an in-flight GET overwrite a completed mutation", async () => {
    const stale = deferred<BoardState>(); let reads = 0;
    const model = new BoardModel((async (path: string, init?: unknown) => { if (path === "/workspaces") return { workspaces: [] }; if (init) return { ...initial(), revision: 4 }; return ++reads === 1 ? initial() : stale.promise; }) as never);
    await model.refresh(); const pending = model.refresh(); await model.mutate({ action: "move_card", id: "a", column_id: "done" }); stale.resolve(initial()); await pending;
    expect(model.snapshot().board?.revision).toBe(4);
  });
});

describe("workspace board polling", () => {
  const discovery = () => ({ workspaces: [{ id: "w", machine_id: "computer", source: "directory", source_id: "w", title: "w", cwd: "/projects/w" }] });
  /** Every read returns freshly parsed JSON, as the network does. */
  const polled = (board: () => BoardState) => new BoardModel((async (path: string) => path === "/workspaces" ? discovery() : board()) as never);
  const counted = (model: BoardModel) => { let n = 0; model.subscribe(() => { n++; }); return () => n; };
  it("an unchanged poll notifies no one and keeps every object", async () => {
    const model = polled(initial); await model.refresh();
    const before = model.snapshot(), notified = counted(model);
    await model.refresh();
    expect(notified()).toBe(0);
    expect(model.snapshot()).toBe(before); expect(model.snapshot().board).toBe(before.board); expect(model.snapshot().discovery).toBe(before.discovery);
  });
  it("a card edit on the same revision notifies once and replaces only the board", async () => {
    let title = "a";
    const model = polled(() => { const b = initial(); b.cards[0].title = title; return b; }); await model.refresh();
    const before = model.snapshot(), notified = counted(model);
    title = "renamed"; await model.refresh();
    expect(notified()).toBe(1);
    expect(model.snapshot().board).not.toBe(before.board); expect(model.snapshot().board?.cards[0].title).toBe("renamed");
    expect(model.snapshot().discovery).toBe(before.discovery);
  });
  it("a can_write flip on the same revision updates readonly in one notification", async () => {
    let canWrite = true;
    const model = polled(() => ({ ...initial(), can_write: canWrite })); await model.refresh();
    const notified = counted(model);
    canWrite = false; await model.refresh();
    expect(notified()).toBe(1); expect(model.snapshot().readonly).toBe(true); expect(model.snapshot().board?.can_write).toBe(false);
  });
  it("a repeated error stays quiet and a recovered read clears it", async () => {
    let fail = true;
    const model = polled(() => { if (fail) throw new ApiError(500, "storage failed", {}); return initial(); }); await model.refresh();
    const error = model.snapshot().error, notified = counted(model);
    expect(error).not.toBe("");
    await model.refresh(); expect(notified()).toBe(0); expect(model.snapshot().error).toBe(error);
    fail = false; await model.refresh();
    expect(notified()).toBe(1); expect(model.snapshot().error).toBe(""); expect(model.snapshot().board).toEqual(initial());
  });
});
