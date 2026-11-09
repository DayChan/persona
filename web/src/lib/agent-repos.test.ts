import { describe, expect, it } from "vitest";
import { buildRepoMachineTree, buildRepoTree, repoOf, showsWorktreeLevel, type RepoTree } from "./agent-repos";
import type { AgentState, Machine, Session } from "./types";

const machines: Machine[] = [
  { id: "machine-b", name: "电脑 B", online: true },
  { id: "machine-a", name: "电脑 A", online: true },
  { id: "machine-off", name: "离线电脑", online: false },
];
const date = (hour: number) => `2026-01-01T${String(hour).padStart(2, "0")}:00:00Z`;
const session = (id: string, cwd: string, patch: Partial<Session> = {}): Session => ({
  machine_id: "machine-a", session_id: id, title: id, cwd, status: "idle" as AgentState, state: "idle" as AgentState, updated_at: date(1), ...patch,
});

describe("从 cwd 推断仓库与工作树", () => {
  it("主工作树就是仓库目录本身", () => {
    expect(repoOf("/code/app")).toEqual({ repo: "/code/app", name: "app", worktree: "/code/app", main: true });
  });

  it("常见的工作树布局都归到仓库根", () => {
    for (const cwd of ["/code/app/.worktrees/fix-login", "/code/app/.claude/worktrees/fix-login"]) {
      const got = repoOf(cwd);
      expect(got.repo).toBe("/code/app");
      expect(got.name).toBe("app");
      expect(got.worktree).toBe(cwd);
      expect(got.main).toBe(false);
    }
  });

  it("~/.worktrees/<仓库>/<工作树> 这类集中布局也认得仓库", () => {
    const got = repoOf("/home/me/.worktrees/persona/fix-login");
    expect(got.repo).toBe("/home/me/.worktrees/persona");
    expect(got.name).toBe("persona");
    expect(got.worktree).toBe("/home/me/.worktrees/persona/fix-login");
    expect(got.main).toBe(false);
  });

  it("没有工作树标记时，目录本身就是仓库", () => {
    expect(repoOf("/code/app/server/internal")).toEqual({ repo: "/code/app/server/internal", name: "internal", worktree: "/code/app/server/internal", main: true });
    expect(repoOf("")).toEqual({ repo: "", name: "", worktree: "", main: true });
  });

  it("宿主给了仓库（repo_root，#2840）时按它分：在它里面是主工作树，别处是它的工作树", () => {
    const wt = "/home/me/.paseo/worktrees/1a2b3c4d/brave-fox";
    expect(repoOf(wt, "/code/app")).toEqual({ repo: "/code/app", name: "app", worktree: wt, main: false });
    expect(repoOf("/code/app", "/code/app/")).toEqual({ repo: "/code/app", name: "app", worktree: "/code/app", main: true });
    expect(repoOf("/code/app/server", "/code/app")).toEqual({ repo: "/code/app", name: "app", worktree: "/code/app", main: true });
    expect(repoOf("/code/app-2", "/code/app").main).toBe(false);
    expect(repoOf(wt, "")).toEqual(repoOf(wt)); // an empty repo_root is the same as none
  });
});

const ids = (sessions: Session[]) => sessions.map((s) => s.session_id);
const names = (tree: RepoTree[]) => tree.map((r) => r.name);
const worktrees = (r: RepoTree) => r.worktrees.map((w) => `${w.name}${w.main ? "*" : ""}`);

describe("按仓库分组的树", () => {
  it("带 repo_root 的会话（Paseo 的 worktree）归到主仓库下，不再各成一个仓库", () => {
    const tree = buildRepoTree([
      session("main", "/code/app", { repo_root: "/code/app" }),
      session("w1", "/home/me/.paseo/worktrees/1a2b3c4d/brave-fox", { repo_root: "/code/app" }),
      session("w2", "/home/me/.paseo/worktrees/1a2b3c4d/calm-bear", { repo_root: "/code/app" }),
    ], machines);
    expect(names(tree)).toEqual(["app"]);
    expect(tree[0]!.worktrees.map((w) => [w.name, w.main, ids(w.sessions)])).toEqual([
      ["app", true, ["main"]], ["brave-fox", false, ["w1"]], ["calm-bear", false, ["w2"]],
    ]);
  });

  it("先给主工作树，再按名字给工作树", () => {
    const all = [
      session("main", "/code/app"),
      session("wt-b", "/code/app/.worktrees/fix-b"),
      session("wt-a", "/code/app/.worktrees/fix-a"),
      session("wt-a2", "/code/app/.worktrees/fix-a"),
    ];
    const tree = buildRepoTree(all, machines);
    expect(names(tree)).toEqual(["app"]);
    expect(worktrees(tree[0]!)).toEqual(["app*", "fix-a", "fix-b"]);
    expect(ids(tree[0]!.worktrees[1]!.sessions)).toEqual(["wt-a", "wt-a2"]);
    expect(tree[0]!.count).toBe(4);
  });

  it("一个 Agent 都没有的仓库不出现；仓库按名字排序", () => {
    const tree = buildRepoTree([session("z", "/code/zeta"), session("a", "/code/alpha")], machines);
    expect(names(tree)).toEqual(["alpha", "zeta"]);
  });

  it("离线机器的 Agent 不进树", () => {
    const tree = buildRepoTree([session("on", "/code/app"), session("off", "/code/app", { machine_id: "machine-off" })], machines);
    expect(ids(tree[0]!.worktrees[0]!.sessions)).toEqual(["on"]);
  });

  it("不同机器上的同名仓库各算一个，同名时按机器的顺序", () => {
    const tree = buildRepoTree([session("a", "/code/app"), session("b", "/code/app", { machine_id: "machine-b" })], machines);
    expect(tree.map((r) => `${r.machineId}/${r.name}`)).toEqual(["machine-b/app", "machine-a/app"]);
  });

  it("工作树里的 Agent 排序沿用 byAttention（需要你优先）", () => {
    const all = [
      session("idle", "/code/app/.worktrees/wt"),
      session("needs", "/code/app/.worktrees/wt", { state: "needs_you", status: "needs_you" }),
    ];
    expect(ids(buildRepoTree(all, machines)[0]!.worktrees[0]!.sessions)).toEqual(["needs", "idle"]);
  });

  it("助手会话不进树", () => {
    expect(buildRepoTree([session("assistant", "/code/app", { assistant: true })], machines)).toEqual([]);
  });
});

describe("按电脑分段的仓库树", () => {
  it("只有一台电脑有 Agent 时返回这台电脑的仓库", () => {
    const all = [
      session("a", "/code/app"),
      session("b", "/code/app/.worktrees/wt"),
    ];
    const tree = buildRepoMachineTree(all, machines);
    expect(tree).toHaveLength(1);
    expect(tree[0]!.machine.id).toBe("machine-a");
    expect(tree[0]!.repos).toHaveLength(1);
    expect(tree[0]!.repos[0]!.name).toBe("app");
    expect(tree[0]!.sessions).toHaveLength(2);
  });

  it("多台电脑时按 hub 机器顺序分段，电脑内仓库按名字排", () => {
    const all = [
      session("a-zeta", "/code/zeta", { machine_id: "machine-a" }),
      session("a-alpha", "/code/alpha", { machine_id: "machine-a" }),
      session("b-beta", "/code/beta", { machine_id: "machine-b" }),
    ];
    const tree = buildRepoMachineTree(all, machines);
    expect(tree.map((m) => m.machine.id)).toEqual(["machine-b", "machine-a"]);
    expect(tree[1]!.repos.map((r) => r.name)).toEqual(["alpha", "zeta"]);
    expect(tree[0]!.repos.map((r) => r.name)).toEqual(["beta"]);
  });

  it("离线机器或没有 Agent 的机器不进树", () => {
    const all = [
      session("on", "/code/app", { machine_id: "machine-a" }),
      session("off", "/code/app", { machine_id: "machine-off" }),
    ];
    const tree = buildRepoMachineTree(all, machines);
    expect(tree.map((m) => m.machine.id)).toEqual(["machine-a"]);
  });

  it("needs 准确统计该电脑上需要你的 Agent 数量", () => {
    const all = [
      session("n1", "/code/app", { machine_id: "machine-a", state: "needs_you", status: "needs_you" }),
      session("w1", "/code/app", { machine_id: "machine-a", state: "working", status: "working" }),
      session("n2", "/code/app", { machine_id: "machine-b", state: "needs_you", status: "needs_you" }),
    ];
    const tree = buildRepoMachineTree(all, machines);
    const macA = tree.find((m) => m.machine.id === "machine-a")!;
    const macB = tree.find((m) => m.machine.id === "machine-b")!;
    expect(macA.needs).toBe(1);
    expect(macB.needs).toBe(1);
  });

  it("助手会话不进树", () => {
    const all = [session("ast", "/code/app", { machine_id: "machine-a", assistant: true })];
    expect(buildRepoMachineTree(all, machines)).toEqual([]);
  });
});

const orca = (id: string, repoId: string, path: string, patch: Partial<Session> = {}): Session =>
  session(id, path, { space_kind: "orca", space_id: `${repoId}::${path}`, ...patch });
const spaceId = (repoId: string, path: string) => `${repoId}::${path}`;
const onlyRepo = (all: Session[]) => buildRepoMachineTree(all, machines)[0]!.repos[0]!;

describe("Orca projects in By repo", () => {
  it("groups two worktrees by project id and resolves a name tie alphabetically", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const b = "/home/u/orca/workspaces/myrepo/feature-b";
    const tree = onlyRepo([orca("b", "repo-a", b), orca("a", "repo-a", a)]);
    expect(tree.key).toBe("machine-a\u0000orca:repo-a");
    expect(tree.repo).toBe("orca:repo-a");
    expect(tree.name).toBe("feature-a");
    expect(tree.worktrees.map((w) => [w.name, w.path, w.main])).toEqual([
      ["feature-a", a, false],
      ["feature-b", b, false],
    ]);
    expect(tree.count).toBe(2);
  });

  it("uses the most frequent folder name among distinct worktree paths", () => {
    const x = "/home/u/orca/workspaces/myrepo/x";
    const first = orca("x-1", "repo-a", x);
    const tree = onlyRepo([
      orca("main-1", "repo-a", "/home/u/orca/workspaces/one/myrepo"),
      first,
      session("x-2", `${x}/nested`, { space_kind: "orca", space_id: first.space_id }),
      orca("main-2", "repo-a", "/home/u/orca/workspaces/two/myrepo"),
    ]);
    expect(tree.name).toBe("myrepo");
    expect(tree.worktrees).toHaveLength(3);
    const shared = tree.worktrees.find((w) => w.name === "x")!;
    expect(shared.path).toBe(x);
    expect(shared.sessions).toHaveLength(2);
    expect(tree.count).toBe(4);
  });

  it("prefers the shortest folder name when frequencies tie", () => {
    const tree = onlyRepo([
      orca("long", "repo-a", "/home/u/orca/workspaces/myrepo/feature-a"),
      orca("short", "repo-a", "/home/u/orca/workspaces/myrepo/tiny"),
    ]);
    expect(tree.name).toBe("tiny");
  });

  it("names a single-worktree project after that worktree's folder", () => {
    const tree = onlyRepo([orca("one", "repo-a", "/home/u/orca/workspaces/myrepo/feature-a")]);
    expect(tree.name).toBe("feature-a");
    expect(tree.worktrees[0]!.name).toBe("feature-a");
  });

  it("uses Orca attribution ahead of repo_root, names the project from the root, and marks its checkout main", () => {
    const rootSpace = spaceId("repo-a", "/code/app");
    const tree = onlyRepo([
      session("main", "/code/app", { repo_root: "/code/app", space_kind: "orca", space_id: rootSpace }),
      session("wt", "/home/u/orca/workspaces/myrepo/feature-a", {
        repo_root: "/code/app",
        space_kind: "orca",
        space_id: spaceId("repo-a", "/home/u/orca/workspaces/myrepo/feature-a"),
        parent_space_id: rootSpace,
      }),
    ]);
    expect(tree.repo).toBe("orca:repo-a");
    expect(tree.name).toBe("app");
    expect(tree.worktrees.map((w) => [w.name, w.main, w.children.length])).toEqual([
      ["app", true, 1],
    ]);
    expect(tree.worktrees[0]!.children.map((w) => [w.name, w.main])).toEqual([["feature-a", false]]);
  });

  it("leaves Orca folder workspaces and non-Orca ids containing :: on path-based grouping", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const b = "/home/u/orca/workspaces/myrepo/feature-b";
    const built = buildRepoMachineTree([
      session("folder", a, { space_kind: "orca", space_id: "folder:00000000-0000-0000-0000-000000000000" }),
      session("herdr", b, { space_kind: "herdr", space_id: spaceId("repo-a", b) }),
    ], machines);
    expect(built[0]!.repos.map((r) => [r.name, r.repo, r.worktrees[0]!.main])).toEqual([
      ["feature-a", a, true],
      ["feature-b", b, true],
    ]);
  });

  it("keeps the same Orca project id separate on different machines", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const built = buildRepoMachineTree([
      orca("a", "repo-a", path),
      orca("b", "repo-a", path, { machine_id: "machine-b" }),
    ], machines);
    expect(built.map((m) => m.machine.id)).toEqual(["machine-b", "machine-a"]);
    expect(built.map((m) => [m.repos.length, m.repos[0]!.repo, m.repos[0]!.key])).toEqual([
      [1, "orca:repo-a", "machine-b\u0000orca:repo-a"],
      [1, "orca:repo-a", "machine-a\u0000orca:repo-a"],
    ]);
  });
});

describe("Every agent source in Orca worktrees", () => {
  it("attributes a native session in a worktree subfolder to that worktree", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const tree = onlyRepo([
      orca("hosted", "repo-a", path),
      session("native", `${path}/src/components`),
    ]);
    expect(tree.repo).toBe("orca:repo-a");
    expect(tree.count).toBe(2);
    expect(tree.worktrees).toHaveLength(1);
    expect(ids(tree.worktrees[0]!.sessions)).toEqual(["hosted", "native"]);
    expect(ids(tree.worktrees[0]!.tree)).toEqual(["hosted", "native"]);
  });

  it("attributes Codex by cwd ahead of repo_root and names the project from that root", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const tree = onlyRepo([
      orca("hosted", "repo-a", path),
      session("codex", `${path}/backend`, {
        space_kind: "codex_app",
        space_id: "codex-project:/code/myrepo",
        repo_root: "/code/myrepo",
      }),
    ]);
    expect(tree.repo).toBe("orca:repo-a");
    expect(tree.name).toBe("myrepo");
    expect(tree.worktrees).toHaveLength(1);
    expect(ids(tree.worktrees[0]!.sessions)).toEqual(["codex", "hosted"]);
    expect(tree.worktrees[0]!.main).toBe(false);
    expect(tree.count).toBe(2);
  });

  it("does not attribute other git worktrees below known marker folders", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const markers = [".worktrees", ".claude/worktrees", ".herdr/worktrees"];
    const nested = markers.map((marker, index) => session(`herdr-${index}`, `${path}/${marker}/feature-x`, {
      space_kind: "herdr",
      space_id: `herdr:space-${index}`,
    }));
    const built = buildRepoMachineTree([orca("hosted", "repo-a", path), ...nested], machines)[0]!;
    expect(built.repos).toHaveLength(2);
    const orcaRepo = built.repos.find((r) => r.repo === "orca:repo-a")!;
    expect(ids(orcaRepo.worktrees[0]!.sessions)).toEqual(["hosted"]);
    const inferred = built.repos.find((r) => r.repo === path)!;
    expect(inferred.count).toBe(3);
    expect(inferred.worktrees.map((w) => w.path)).toEqual(nested.map((s) => s.cwd));
  });

  it("matches complete path segments rather than a string prefix", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const outside = `${path}-copy`;
    const built = buildRepoMachineTree([
      orca("hosted", "repo-a", path),
      session("native", `${outside}/src`),
    ], machines)[0]!;
    expect(built.repos).toHaveLength(2);
    expect(ids(built.repos.find((r) => r.repo === "orca:repo-a")!.worktrees[0]!.sessions)).toEqual(["hosted"]);
    const inferred = built.repos.find((r) => r.repo === `${outside}/src`)!;
    expect(inferred.worktrees[0]!.main).toBe(true);
    expect(ids(inferred.worktrees[0]!.sessions)).toEqual(["native"]);
  });

  it("chooses the deepest known Orca worktree", () => {
    const outer = "/home/u/orca/workspaces/myrepo/feature-a";
    const inner = `${outer}/nested`;
    const tree = onlyRepo([
      orca("outer", "repo-a", outer),
      orca("inner", "repo-a", inner),
      session("native", `${inner}/src`),
    ]);
    expect(tree.worktrees).toHaveLength(2);
    expect(ids(tree.worktrees.find((w) => w.path === outer)!.sessions)).toEqual(["outer"]);
    expect(ids(tree.worktrees.find((w) => w.path === inner)!.sessions)).toEqual(["inner", "native"]);
  });

  it("uses workspace_cwd when cwd does not match an Orca worktree", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const tree = onlyRepo([
      orca("hosted", "repo-a", path),
      session("native", "/home/u/elsewhere", { workspace_cwd: `${path}/src` }),
    ]);
    expect(tree.worktrees).toHaveLength(1);
    expect(ids(tree.worktrees[0]!.sessions)).toEqual(["hosted", "native"]);
  });

  it("leaves a native session outside every known Orca worktree unchanged", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const outside = "/home/u/projects/other";
    const built = buildRepoMachineTree([
      orca("hosted", "repo-a", path),
      session("native", outside),
    ], machines)[0]!;
    expect(built.repos).toHaveLength(2);
    const inferred = built.repos.find((r) => r.repo === outside)!;
    expect(inferred.name).toBe("other");
    expect(inferred.worktrees[0]!.main).toBe(true);
    expect(ids(inferred.worktrees[0]!.sessions)).toEqual(["native"]);
  });

  it("does not learn Orca worktree paths from another machine", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const built = buildRepoMachineTree([
      orca("hosted", "repo-a", path),
      session("native", `${path}/src`, { machine_id: "machine-b" }),
    ], machines);
    const a = built.find((m) => m.machine.id === "machine-a")!;
    const b = built.find((m) => m.machine.id === "machine-b")!;
    expect(a.repos[0]!.repo).toBe("orca:repo-a");
    expect(b.repos[0]!.repo).toBe(`${path}/src`);
    expect(b.repos[0]!.worktrees[0]!.main).toBe(true);
  });

  it("does not recognize an Orca worktree without a live Orca-hosted session", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const tree = onlyRepo([session("native", `${path}/src`)]);
    expect(tree.repo).toBe(`${path}/src`);
    expect(tree.name).toBe("src");
    expect(tree.worktrees[0]!.main).toBe(true);
  });

  it("keeps the first space id learned for a path when attributing another source", () => {
    const path = "/home/u/orca/workspaces/myrepo/feature-a";
    const built = buildRepoMachineTree([
      orca("first", "repo-a", path),
      orca("second", "repo-b", path),
      session("native", `${path}/src`),
    ], machines)[0]!;
    expect(ids(built.repos.find((r) => r.repo === "orca:repo-a")!.worktrees[0]!.sessions)).toEqual(["first", "native"]);
    expect(ids(built.repos.find((r) => r.repo === "orca:repo-b")!.worktrees[0]!.sessions)).toEqual(["second"]);
  });
});

describe("Orca child worktrees in By repo", () => {
  it("nests recursively, sorts children, and fills descendant sessions and states", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const b = "/home/u/orca/workspaces/myrepo/feature-b";
    const c = "/home/u/orca/workspaces/myrepo/feature-c";
    const d = "/home/u/orca/workspaces/myrepo/feature-d";
    const tree = onlyRepo([
      orca("c", "repo-a", c, { state: "working", status: "working", parent_space_id: spaceId("repo-a", a) }),
      orca("d", "repo-a", d, { parent_space_id: spaceId("repo-a", b) }),
      orca("a", "repo-a", a),
      orca("b", "repo-a", b, { state: "needs_you", status: "needs_you", parent_space_id: spaceId("repo-a", a) }),
    ]);
    expect(tree.worktrees.map((w) => w.name)).toEqual(["feature-a"]);
    expect(tree.count).toBe(4);
    const parent = tree.worktrees[0]!;
    expect(parent.children.map((w) => w.name)).toEqual(["feature-b", "feature-c"]);
    expect(parent.children[0]!.children.map((w) => w.name)).toEqual(["feature-d"]);
    expect(ids(parent.tree)).toEqual(["a", "b", "d", "c"]);
    expect(parent.childStates).toEqual({ needs_you: 1, working: 1, done: 0 });
    expect(parent.children[0]!.childStates).toEqual({ needs_you: 0, working: 0, done: 1 });
    expect(parent.children[1]!.childStates).toEqual({ needs_you: 0, working: 0, done: 0 });
  });

  it("ignores a non-Orca member session's parent_space_id when nesting", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const b = "/home/u/orca/workspaces/myrepo/feature-b";
    const tree = onlyRepo([
      orca("a", "repo-a", a),
      session("native", `${a}/src`, { parent_space_id: spaceId("repo-a", b) }),
      orca("b", "repo-a", b),
    ]);
    expect(tree.worktrees.map((w) => w.name)).toEqual(["feature-a", "feature-b"]);
    expect(tree.worktrees.every((w) => w.children.length === 0)).toBe(true);
    expect(ids(tree.worktrees[0]!.sessions)).toEqual(["a", "native"]);
  });

  it("leaves a child with an unlisted parent at the top level", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const tree = onlyRepo([orca("a", "repo-a", a, { parent_space_id: spaceId("repo-a", "/home/u/orca/workspaces/myrepo/missing") })]);
    expect(tree.worktrees).toHaveLength(1);
    expect(tree.worktrees[0]!.children).toEqual([]);
    expect(ids(tree.worktrees[0]!.tree)).toEqual(["a"]);
  });

  it("leaves a self-parented worktree at the top level", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const tree = onlyRepo([orca("a", "repo-a", a, { parent_space_id: spaceId("repo-a", a) })]);
    expect(tree.worktrees).toHaveLength(1);
    expect(tree.worktrees[0]!.children).toEqual([]);
  });

  it("breaks a two-worktree parent cycle", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const b = "/home/u/orca/workspaces/myrepo/feature-b";
    const tree = onlyRepo([
      orca("a", "repo-a", a, { parent_space_id: spaceId("repo-a", b) }),
      orca("b", "repo-a", b, { parent_space_id: spaceId("repo-a", a) }),
    ]);
    expect(tree.worktrees.map((w) => w.name)).toEqual(["feature-a", "feature-b"]);
    expect(tree.worktrees.every((w) => w.children.length === 0)).toBe(true);
  });

  it("breaks a three-worktree parent cycle", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const b = "/home/u/orca/workspaces/myrepo/feature-b";
    const c = "/home/u/orca/workspaces/myrepo/feature-c";
    const tree = onlyRepo([
      orca("a", "repo-a", a, { parent_space_id: spaceId("repo-a", c) }),
      orca("b", "repo-a", b, { parent_space_id: spaceId("repo-a", a) }),
      orca("c", "repo-a", c, { parent_space_id: spaceId("repo-a", b) }),
    ]);
    expect(tree.worktrees.map((w) => w.name)).toEqual(["feature-a", "feature-b", "feature-c"]);
    expect(tree.worktrees.every((w) => w.children.length === 0)).toBe(true);
  });

  it("does not nest a child under a parent in another project", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const b = "/home/u/orca/workspaces/other/feature-b";
    const built = buildRepoMachineTree([
      orca("a", "repo-a", a, { parent_space_id: spaceId("repo-b", b) }),
      orca("b", "repo-b", b),
    ], machines)[0]!;
    expect(built.repos).toHaveLength(2);
    for (const repo of built.repos) {
      expect(repo.worktrees).toHaveLength(1);
      expect(repo.worktrees[0]!.children).toEqual([]);
    }
  });
});

describe("showsWorktreeLevel", () => {
  it("hides the level for a single main worktree", () => {
    expect(showsWorktreeLevel(buildRepoTree([session("main", "/code/app")], machines)[0]!)).toBe(false);
  });

  it("hides the level for a single Orca worktree named like its project", () => {
    expect(showsWorktreeLevel(onlyRepo([orca("one", "repo-a", "/home/u/orca/workspaces/myrepo/feature-a")]))).toBe(false);
  });

  it("shows the level when a single top-level worktree has a child", () => {
    const a = "/home/u/orca/workspaces/myrepo/feature-a";
    const b = "/home/u/orca/workspaces/myrepo/feature-b";
    const tree = onlyRepo([
      orca("a", "repo-a", a),
      orca("b", "repo-a", b, { parent_space_id: spaceId("repo-a", a) }),
    ]);
    expect(showsWorktreeLevel(tree)).toBe(true);
  });

  it("shows the level for two worktrees", () => {
    const tree = onlyRepo([
      orca("a", "repo-a", "/home/u/orca/workspaces/myrepo/feature-a"),
      orca("b", "repo-a", "/home/u/orca/workspaces/myrepo/feature-b"),
    ]);
    expect(showsWorktreeLevel(tree)).toBe(true);
  });

  it("shows the level for a single non-main worktree with another name", () => {
    expect(showsWorktreeLevel(buildRepoTree([session("wt", "/code/app/.worktrees/feature-a")], machines)[0]!)).toBe(true);
  });
});


describe("the node's git place (#1805): machine → project → branch → sessions", () => {
  const mac = (id: string, worktree: string, branch: string, patch: Partial<Session> = {}) =>
    session(id, worktree, { repo_root: "/home/u/Code/app", worktree, branch, ...patch });

  it("puts linked worktrees under their main repository, labelled by branch, main first", () => {
    const fix = "/home/u/orca/workspaces/app/fix-login";
    const tree = buildRepoTree([
      mac("fix", fix, "fix/login"),
      mac("main", "/home/u/Code/app", "main"),
      mac("sub", fix, "fix/login", { cwd: `${fix}/web` }),
      session("other", "/home/u/Code/notes", { repo_root: "/home/u/Code/notes", worktree: "/home/u/Code/notes", branch: "trunk" }),
    ], machines);
    expect(names(tree)).toEqual(["app", "notes"]);
    expect(worktrees(tree[0]!)).toEqual(["main*", "fix/login"]);
    expect(ids(tree[0]!.worktrees[1]!.sessions).sort()).toEqual(["fix", "sub"]);
  });

  it("joins Orca-hosted and other agents of one repository into one project", () => {
    const fix = "/home/u/orca/workspaces/app/fix-login";
    const tree = buildRepoTree([
      mac("orca", fix, "fix/login", { space_kind: "orca", space_id: spaceId("repo-a", fix) }),
      mac("cli", "/home/u/Code/app", "main"),
    ], machines);
    expect(names(tree)).toEqual(["app"]);
    expect(worktrees(tree[0]!)).toEqual(["main*", "fix/login"]);
  });

  it("keeps the branch level under a project with only its main checkout", () => {
    const tree = buildRepoTree([mac("main", "/home/u/Code/app", "main")], machines);
    expect(showsWorktreeLevel(tree[0]!)).toBe(true);
    expect(worktrees(tree[0]!)).toEqual(["main*"]);
  });

  it("lists projects by name inside each machine, in the hub's machine order", () => {
    const tree = buildRepoMachineTree([
      session("a", "/code/zeta", { repo_root: "/code/zeta", worktree: "/code/zeta", branch: "main" }),
      session("b", "/code/alpha", { repo_root: "/code/alpha", worktree: "/code/alpha", branch: "main", machine_id: "machine-b" }),
      session("c", "/code/beta", { repo_root: "/code/beta", worktree: "/code/beta", branch: "main" }),
    ], machines);
    expect(tree.map((m) => [m.machine.id, m.repos.map((r) => r.name)])).toEqual([["machine-b", ["alpha"]], ["machine-a", ["beta", "zeta"]]]);
  });
});
