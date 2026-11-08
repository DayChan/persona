import { describe, expect, it } from "vitest";
import { buildSpaces } from "./agent-groups";
import { buildRepoMachineTree } from "./agent-repos";
import { agentSort, byUpdated, latestOf, machineTreeByUpdate, projectsByUpdate, repoMachinesByUpdate, sortDate, spacesByUpdate } from "./agent-sort";
import { buildMachineTree } from "./herdr-tree";
import type { AgentState, Machine, Session } from "./types";

const machines: Machine[] = [
  { id: "machine-a", name: "电脑 A", online: true },
  { id: "machine-b", name: "电脑 B", online: true },
];
const date = (hour: number) => `2026-01-01T${String(hour).padStart(2, "0")}:00:00Z`;
const session = (id: string, state: AgentState, hour: number, patch: Partial<Session> = {}): Session => ({
  machine_id: "machine-a", session_id: id, title: id, cwd: "/tmp/project", status: state, state, updated_at: date(hour), ...patch,
});
const ids = (list: { session_id: string }[]) => list.map((s) => s.session_id);

describe("Agent 排序偏好", () => {
  it("没选过的是状态优先，只认最近更新", () => {
    expect(agentSort(null)).toBe("state");
    expect(agentSort("bogus")).toBe("state");
    expect(agentSort("updated")).toBe("updated");
  });

  it("工作中的按开始工作的时间算，其余按更新时间；同一时间按 id 定序", () => {
    const working = session("w", "working", 9, { state_since: date(2) });
    expect(sortDate(working)).toBe(date(2));
    expect(latestOf([working, session("x", "idle", 5)])).toBe(date(5));
    expect(latestOf([])).toBe("");
    const list = [session("b", "idle", 3), session("a", "idle", 3), session("c", "needs_you", 1)];
    expect(ids([...list].sort(byUpdated))).toEqual(["a", "b", "c"]);
  });

  it("compares the times as times: a fraction of another length or an offset does not reorder them (#3638)", () => {
    const at = (id: string, updated_at: string) => session(id, "idle", 0, { updated_at });
    const list = [at("ms", "2026-11-06T10:00:05.12Z"), at("ns", "2026-11-06T10:00:05.123456789Z"), at("whole", "2026-11-06T10:00:05Z")];
    expect(ids([...list].sort(byUpdated))).toEqual(["ns", "ms", "whole"]);
    expect(latestOf(list)).toBe("2026-11-06T10:00:05.123456789Z");
    const zoned = [at("east", "2026-11-06T18:00:00+08:00"), at("utc", "2026-11-06T10:30:00Z")];
    expect(ids([...zoned].sort(byUpdated))).toEqual(["utc", "east"]);
    expect(latestOf(zoned)).toBe("2026-11-06T10:30:00Z");
  });
});

describe("最近更新", () => {
  it("按项目：空间按最新的 Agent 排，空间里只按时间排（不按状态）", () => {
    const all = [
      session("old-needs", "needs_you", 1, { space_id: "s1", space_name: "老项目", space_index: 0, slot_index: 0 }),
      session("old-idle", "idle", 2, { space_id: "s1", space_name: "老项目", space_index: 0, slot_index: 1 }),
      session("new-idle", "idle", 8, { space_id: "s2", space_name: "新项目", space_index: 1, slot_index: 0 }),
    ];
    const { spaces } = buildSpaces(all, machines);
    expect(spaces.map((sp) => sp.name)).toEqual(["老项目", "新项目"]);
    const sorted = spacesByUpdate(spaces);
    expect(sorted.map((sp) => sp.name)).toEqual(["新项目", "老项目"]);
    expect(ids(sorted[1]!.sessions)).toEqual(["old-idle", "old-needs"]);
  });

  it("按项目：其他 Agent 也只按时间排，较早的「需要你」排在较新的后面", () => {
    const all = [
      session("loose-needs", "needs_you", 1),
      session("loose-new", "idle", 8),
      session("in-space", "idle", 5, { space_id: "s1", space_name: "项目", space_index: 0, slot_index: 0 }),
    ];
    const built = buildSpaces(all, machines);
    expect(ids(built.loose)).toEqual(["loose-needs", "loose-new"]);
    const sorted = projectsByUpdate(built);
    expect(ids(sorted.loose)).toEqual(["loose-new", "loose-needs"]);
    expect(sorted.spaces.map((sp) => sp.name)).toEqual(["项目"]);
  });

  it("按机器：机器、空间、tab 都按最新的 Agent 排，置顶的空间仍在前", () => {
    const all = [
      session("a-old", "idle", 1, { space_id: "s1", space_name: "一", space_index: 0, slot_id: "t1", slot_index: 0 }),
      session("a-new", "idle", 6, { space_id: "s2", space_name: "二", space_index: 1, slot_id: "t2", slot_index: 0 }),
      session("a-tab-old", "idle", 2, { space_id: "s2", space_name: "二", space_index: 1, slot_id: "t3", slot_index: 1 }),
      session("b-latest", "idle", 9, { machine_id: "machine-b", space_id: "s9", space_name: "九", space_index: 0, slot_id: "t9", slot_index: 0 }),
    ];
    const tree = buildMachineTree(machines, all);
    expect(tree.map((m) => m.machine.id)).toEqual(["machine-a", "machine-b"]);
    const sorted = machineTreeByUpdate(tree);
    expect(sorted.map((m) => m.machine.id)).toEqual(["machine-b", "machine-a"]);
    const a = sorted[1]!;
    expect(a.spaces.map((sp) => sp.name)).toEqual(["二", "一"]);
    expect(a.spaces[0]!.tabs.map((tab) => tab.slotId)).toEqual(["t2", "t3"]);

    const pinned = machineTreeByUpdate(buildMachineTree(machines, all, { pins: { sessions: [], spaces: ["machine-a/一"] } }));
    expect(pinned.find((m) => m.machine.id === "machine-a")!.spaces.map((sp) => sp.name)).toEqual(["一", "二"]);
  });

  it("按仓库：仓库按最新的 Agent 排，工作树里只按时间排，机器保持原来的顺序", () => {
    const all = [
      session("b-latest", "idle", 9, { machine_id: "machine-b", cwd: "/src/beta" }),
      session("alpha-needs", "needs_you", 1, { cwd: "/src/alpha" }),
      session("alpha-idle", "idle", 3, { cwd: "/src/alpha" }),
      session("zeta", "idle", 7, { cwd: "/src/zeta" }),
    ];
    const tree = buildRepoMachineTree(all, machines);
    expect(tree[0]!.repos.map((r) => r.name)).toEqual(["alpha", "zeta"]);
    const sorted = repoMachinesByUpdate(tree);
    expect(sorted.map((m) => m.key)).toEqual(tree.map((m) => m.key));
    expect(sorted[0]!.repos.map((r) => r.name)).toEqual(["zeta", "alpha"]);
    expect(ids(sorted[0]!.repos[1]!.worktrees[0]!.sessions)).toEqual(["alpha-idle", "alpha-needs"]);
  });

  it("By repo: nested children decide repository order and sort their own agents and siblings", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const b = "/home/u/orca/workspaces/myrepo/feature-b";
    const c = "/home/u/orca/workspaces/myrepo/feature-c";
    const parentId = `repo-a::${a}`;
    const childId = `repo-a::${b}`;
    const child = (id: string, state: AgentState, hour: number) => session(id, state, hour, {
      cwd: b,
      space_kind: "orca",
      space_id: childId,
      parent_space_id: parentId,
    });
    const tree = buildRepoMachineTree([
      session("parent", "idle", 1, { cwd: a, space_kind: "orca", space_id: parentId }),
      child("child-old", "needs_you", 1),
      child("child-new", "idle", 8),
      session("other-child", "idle", 9, { cwd: c, space_kind: "orca", space_id: `repo-a::${c}`, parent_space_id: parentId }),
      session("zeta", "idle", 7, { cwd: "/src/zeta" }),
    ], machines);
    const sorted = repoMachinesByUpdate(tree);
    expect(sorted[0]!.repos.map((r) => r.name)).toEqual(["feature-a", "zeta"]);
    const parent = sorted[0]!.repos[0]!.worktrees[0]!;
    expect(parent.children.map((w) => w.name)).toEqual(["feature-c", "feature-b"]);
    expect(ids(parent.children[1]!.sessions)).toEqual(["child-new", "child-old"]);
  });
});
