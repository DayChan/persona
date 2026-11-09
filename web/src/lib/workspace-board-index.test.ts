// The board's indexes give what the scans they replace give: cardSessions for a card's Agents, machines.find for its
// computer and the first workspace find() picks by id or by machine and directory, for every kind of path.
import { describe, expect, it } from "vitest";
import { cardSessions, sameDirectory, type BoardCard, type Workspace } from "./workspace-board";
import { machinesByID, NO_SESSIONS, sessionsByCard, workspacesByCard } from "./workspace-board-index";
import type { Machine, Session } from "./types";

const card = (id: string, machine_id: string, cwd: string, workspace_id = `space-${id}`): BoardCard =>
  ({ id, machine_id, workspace_id, title: id, cwd, source: "directory", column_id: "todo", pinned: false, archived: false });
let n = 0;
const session = (machine_id: string, cwd: string, workspace_cwd?: string): Session =>
  ({ machine_id, session_id: `s${++n}`, cwd, workspace_cwd, title: "Agent", status: "idle" });
const workspace = (id: string, machine_id: string, cwd: string): Workspace =>
  ({ id, machine_id, cwd, source: "directory", source_id: id, title: id });

// every path shape the hub canonicalizes, plus ones it never matches
const paths = ["/repo/app", "/repo/app/", "/repo//app", "/repo/./app", "/repo/x/../app", "/repo/app-other", "/repo", "/app", "", "relative/app",
  "C:\\Repo\\App\\", "c:/Repo/App", "\\\\Server\\Share\\app", "//server/share/app/", "C:repo", "/repo/\0app"];
const machines = ["mac", "nas", "mac\0/repo"];
const sessions = machines.flatMap((m) => paths.flatMap((p) => [session(m, p), session(m, "/elsewhere", p), session(m, p, "")]));
const cards = machines.flatMap((m) => paths.map((p, i) => card(`${m}-${i}`, m, p)));

describe("sessionsByCard", () => {
  it("gives every card what cardSessions gives, in the same order", () => {
    const of = sessionsByCard(sessions);
    for (const c of cards) expect(of(c)).toEqual(cardSessions(c, sessions));
    // the shapes above do group: trailing and repeated slashes, dot segments and workspace_cwd over cwd
    expect(of(card("a", "mac", "/repo/app")).map((s) => [s.cwd, s.workspace_cwd])).toEqual([
      ["/repo/app", undefined], ["/elsewhere", "/repo/app"], ["/repo/app", ""],
      ["/repo/app/", undefined], ["/elsewhere", "/repo/app/"], ["/repo/app/", ""],
      ["/repo//app", undefined], ["/elsewhere", "/repo//app"], ["/repo//app", ""],
      ["/repo/./app", undefined], ["/elsewhere", "/repo/./app"], ["/repo/./app", ""],
      ["/repo/x/../app", undefined], ["/elsewhere", "/repo/x/../app"], ["/repo/x/../app", ""],
    ]);
  });

  it("keeps other machines, other directories, empty and relative paths apart", () => {
    const of = sessionsByCard([session("mac", "/repo/app"), session("nas", "/repo/app"), session("mac", ""), session("mac", "relative")]);
    expect(of(card("a", "mac", "/repo/app")).map((s) => s.machine_id)).toEqual(["mac"]);
    expect(of(card("b", "other", "/repo/app"))).toBe(NO_SESSIONS);
    expect(of(card("c", "mac", ""))).toBe(NO_SESSIONS);
    expect(of(card("d", "mac", "relative"))).toBe(NO_SESSIONS);
  });

  it("hands out the same array on every lookup, and one frozen empty array for a card with no Agent", () => {
    const of = sessionsByCard(sessions), c = card("a", "mac", "/repo/app");
    expect(of(c)).toBe(of({ ...c, cwd: "/repo/app/" }));
    expect(of(card("none", "mac", "/nothing/here"))).toBe(NO_SESSIONS);
    expect(Object.isFrozen(NO_SESSIONS)).toBe(true);
    expect(sessionsByCard([])(c)).toBe(NO_SESSIONS);
  });
});

describe("machinesByID", () => {
  it("gives the machine find() gives, the first of duplicate ids", () => {
    const list: Machine[] = [{ id: "mac", name: "First", online: true }, { id: "nas", name: "NAS", online: false }, { id: "mac", name: "Second", online: true }];
    const of = machinesByID(list);
    for (const id of ["mac", "nas", "missing", ""]) expect(of(id)).toBe(list.find((m) => m.id === id));
    expect(of("mac")?.name).toBe("First");
  });
});

describe("workspacesByCard", () => {
  const find = (list: Workspace[], c: BoardCard) => list.find((w) => w.id === c.workspace_id || w.machine_id === c.machine_id && sameDirectory(w.cwd, c.cwd));

  it("gives every card the workspace find() gives", () => {
    const list = machines.flatMap((m) => paths.map((p, i) => workspace(`w-${m}-${i}`, m, p)));
    // cards bound by id to one workspace and placed at another, both orders
    const extra = [card("by-id", "nas", "/nowhere", "w-mac-0"), card("by-id-later", "mac", "/repo/app", "w-nas-9"), card("missing", "mac", "/repo/none", "")];
    const of = workspacesByCard(list);
    for (const c of [...cards, ...extra]) expect(of(c)).toBe(find(list, c));
    for (const c of [...cards, ...extra]) expect(of({ ...c, workspace_id: "w-none" })).toBe(find(list, { ...c, workspace_id: "w-none" }));
  });

  it("picks whichever match comes first, by id or by place", () => {
    const place = workspace("place", "mac", "/repo/app/"), bound = workspace("bound", "nas", "/elsewhere"), again = workspace("bound", "mac", "/third");
    const c = card("a", "mac", "/repo//app", "bound");
    expect(workspacesByCard([place, bound, again])(c)).toBe(place);
    expect(workspacesByCard([bound, place, again])(c)).toBe(bound);
    expect(workspacesByCard([again, bound])(c)).toBe(again);
    expect(workspacesByCard([workspace("x", "nas", "/repo/app")])(c)).toBeUndefined();
    expect(workspacesByCard([workspace("x", "mac", "")])(card("b", "mac", ""))).toBeUndefined();
  });
});
