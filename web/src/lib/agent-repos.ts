import { byAttention, stateOf } from "./format";
import { summaryState, type ChildStates } from "./herdr-tree";
import type { Machine, Session } from "./types";

/** Where a session's cwd sits: the repository it belongs to and the working tree inside it. */
export type Repo = { repo: string; name: string; worktree: string; main: boolean };

export type RepoWorktree = {
  key: string;
  /** Its branch when the node says (#1805), else its folder's name (the main checkout: the repository's). */
  name: string;
  /** The branch checked out there, when the node says. */
  branch?: string;
  path: string;
  main: boolean;
  sessions: Session[];
  /** Orca child worktrees, sorted by name. */
  children: RepoWorktree[];
  /** This worktree's own sessions, followed by every descendant's sessions. */
  tree: Session[];
  /** Direct child worktrees needing you, working, or done, by each child's whole-subtree summary. */
  childStates: ChildStates;
};

/** One repository on one machine, with its top-level working trees (the main checkout first, then by name). */
export type RepoTree = {
  key: string;
  machineId: string;
  name: string;
  repo: string;
  count: number;
  worktrees: RepoWorktree[];
};

/** One computer's section in the repository tree: its live repositories ordered by name. */
export interface RepoMachine {
  key: string;
  machine: Machine;
  foldKey: string;
  repos: RepoTree[];
  sessions: Session[];
  needs: number;
}

// The layouts a working tree commonly lives in (herdr, Claude Code, a home-level collection): the parts before the
// last one name the repository, everything from there on is the tree.
const WORKTREE_SEGMENTS = [["/.worktrees/"], ["/.claude/worktrees/"], ["/.herdr/worktrees/"]];

const segments = (path: string) => path.split("/").filter(Boolean);
const baseName = (path: string) => segments(path).pop() ?? "";

/**
 * The repository and working tree a cwd belongs to. The host may name the repository (the session's repo_root, #2840:
 * Paseo keeps its worktrees under its own folder): a cwd inside it is its main checkout, any other is one of its
 * working trees. Otherwise from the path alone (docs/web/web.md): a tree under one of the known layouts belongs to the
 * directory above that marker; any other path is its own repository's main checkout.
 */
export function repoOf(cwd: string, repoRoot?: string): Repo {
  if (!cwd) return { repo: "", name: "", worktree: "", main: true };
  const path = cwd.replace(/\/+$/, "");
  const root = (repoRoot ?? "").replace(/\/+$/, "");
  if (root) {
    const inside = path === root || path.startsWith(`${root}/`);
    return { repo: root, name: baseName(root), worktree: inside ? root : path, main: inside };
  }
  for (const [marker] of WORKTREE_SEGMENTS) {
    const at = path.indexOf(marker);
    if (at <= 0) continue;
    const rest = path.slice(at + marker.length).split("/").filter(Boolean);
    const repo = path.slice(0, at);
    // ~/.worktrees/<repo>/<tree>: the marker sits at the home level, so the repository is the first part after it and
    // the tree is what follows.
    if (rest.length >= 2) return { repo: `${repo}${marker}${rest[0]}`, name: rest[0]!, worktree: path, main: false };
    return { repo, name: baseName(repo), worktree: path, main: false };
  }
  return { repo: path, name: baseName(path), worktree: path, main: true };
}

const worktreeName = (path: string, repo: string) => (path === repo ? baseName(repo) : baseName(path));

const trimmed = (path: string | undefined) => (path ?? "").replace(/\/+$/, "");

/** Where the node placed a session in git (#1805): its project (repo_root, else the working tree itself) and the
 *  working tree; undefined from an older node, which leaves the path inference above. */
export function gitPlaceOf(s: Pick<Session, "repo_root" | "worktree">): Repo | undefined {
  const worktree = trimmed(s.worktree);
  if (!worktree) return undefined;
  const repo = trimmed(s.repo_root) || worktree;
  return { repo, name: baseName(repo), worktree, main: worktree === repo };
}

type Placement = Repo & { path: string; spaceId?: string; git?: boolean };
type OrcaWorktree = { repoId: string; spaceId: string; path: string };

/** An Orca git worktree id is `<project id>::<absolute path>`; folder workspaces have no `::` and keep path inference. */
function orcaSpace(s: Session): OrcaWorktree | undefined {
  if (s.space_kind !== "orca" || !s.space_id) return undefined;
  const at = s.space_id.indexOf("::");
  if (at <= 0) return undefined;
  const repoId = s.space_id.slice(0, at);
  const path = s.space_id.slice(at + 2).replace(/\/+$/, "");
  return repoId && path ? { repoId, spaceId: s.space_id, path } : undefined;
}

/** The deepest known Orca worktree containing this session's cwd. A separate git worktree below a marker is not
 *  part of the containing Orca worktree. workspace_cwd is the node's verified fallback when cwd does not match. */
function containingOrcaWorktree(s: Session, known: ReadonlyMap<string, OrcaWorktree>): OrcaWorktree | undefined {
  const cwds = [s.cwd ?? ""];
  if (s.workspace_cwd && s.workspace_cwd !== s.cwd) cwds.push(s.workspace_cwd);
  for (const value of cwds) {
    const cwd = value.replace(/\/+$/, "");
    if (!cwd) continue;
    let deepest: OrcaWorktree | undefined;
    for (const [path, worktree] of known) {
      if (deepest && path.length <= deepest.path.length) continue;
      if (cwd !== path && !cwd.startsWith(`${path}/`)) continue;
      const below = cwd === path ? "" : cwd.slice(path.length + 1);
      if (WORKTREE_SEGMENTS.some(([marker]) => below.startsWith(marker.slice(1)))) continue;
      deepest = worktree;
    }
    if (deepest) return deepest;
  }
  return undefined;
}

/** Orca's exact project id wins for its own sessions and for any source running inside a known Orca worktree.
 *  Other sessions keep the host-supplied root or path inference. */
function placementOf(s: Session, known: ReadonlyMap<string, OrcaWorktree>): Placement {
  const git = gitPlaceOf(s);
  const orca = orcaSpace(s) ?? containingOrcaWorktree(s, known);
  if (orca) {
    // The node's git project joins an Orca worktree with every other source in the same repository; Orca's project id
    // is the fallback for older nodes.
    if (git) return { repo: git.repo, name: git.name, worktree: orca.spaceId, path: orca.path, main: git.main, spaceId: orca.spaceId, git: true };
    return { repo: `orca:${orca.repoId}`, name: baseName(orca.path), worktree: orca.spaceId, path: orca.path, main: false, spaceId: orca.spaceId };
  }
  if (git) return { ...git, path: git.worktree, git: true };
  if (s.repo_root) {
    const where = repoOf(s.cwd ?? "", s.repo_root);
    return { ...where, path: where.worktree };
  }
  const where = repoOf(s.cwd ?? "", undefined);
  return { ...where, path: where.worktree };
}

/** Prefer the repository name a member session's repo_root supplies. Without one Orca does not send the project name:
 *  guess it from distinct worktree folder names, preferring the most frequent, then the shortest, then alphabetical. */
function orcaProjectName(worktrees: readonly RepoWorktree[]): string {
  const roots = worktrees.flatMap((w) => w.sessions.map((s) => (s.repo_root ?? "").replace(/\/+$/, "")).filter(Boolean));
  if (roots.length) {
    const counts = new Map<string, number>();
    for (const root of roots) counts.set(root, (counts.get(root) ?? 0) + 1);
    const root = [...counts].sort((a, b) => b[1] - a[1] || a[0].length - b[0].length || a[0].localeCompare(b[0]))[0]?.[0] ?? "";
    return baseName(root);
  }
  const counts = new Map<string, number>();
  for (const path of new Set(worktrees.map((w) => w.path))) {
    const name = baseName(path);
    counts.set(name, (counts.get(name) ?? 0) + 1);
  }
  return [...counts].sort((a, b) => b[1] - a[1] || a[0].length - b[0].length || a[0].localeCompare(b[0]))[0]?.[0] ?? "";
}

/** Move Orca worktrees under their listed parent in the same project. A missing/self parent or any parent chain that
 *  loops leaves the worktree at the top level, as in By machine; then fill descendant sessions and state counts. */
function nestWorktrees(worktrees: RepoWorktree[], bySpace: ReadonlyMap<string, RepoWorktree>): RepoWorktree[] {
  const orca = new Set(bySpace.values());
  const parentOf = (w: RepoWorktree): RepoWorktree | undefined => {
    if (!orca.has(w)) return undefined;
    // Sessions attributed by cwd may carry their own unrelated parent id; only Orca-hosted sessions define this edge.
    const hosted = w.sessions.find((s) => s.parent_space_id && orcaSpace(s));
    const parent = hosted?.parent_space_id ? bySpace.get(hosted.parent_space_id) : undefined;
    return parent === w ? undefined : parent;
  };
  const loops = (w: RepoWorktree) => {
    const seen = new Set<RepoWorktree>([w]);
    for (let p = parentOf(w); p; p = parentOf(p)) {
      if (seen.has(p)) return true;
      seen.add(p);
    }
    return false;
  };
  const top: RepoWorktree[] = [];
  for (const w of worktrees) {
    const parent = loops(w) ? undefined : parentOf(w);
    if (parent) parent.children.push(w);
    else top.push(w);
  }
  for (const w of worktrees) w.children.sort((a, b) => a.name.localeCompare(b.name));
  const fill = (w: RepoWorktree): Session[] => {
    const below = w.children.flatMap(fill);
    const counts: ChildStates = { needs_you: 0, working: 0, done: 0 };
    for (const c of w.children) {
      const st = summaryState(c.tree);
      if (st === "needs_you" || st === "working") counts[st]++;
      else if (st === "unread" || st === "idle") counts.done++;
    }
    w.childStates = counts;
    w.tree = [...w.sessions, ...below];
    return w.tree;
  };
  top.forEach(fill);
  return top;
}

/** Whether By repo shows a worktree row, rather than putting a lone main or equivalently named worktree's agents
 *  directly under the project. A worktree with children always keeps its level, and so does a branch the node named
 *  (#1805: machine → project → branch → sessions, as Orca shows it). */
export function showsWorktreeLevel(tree: RepoTree): boolean {
  if (tree.worktrees.length !== 1) return true;
  const w = tree.worktrees[0]!;
  return w.children.length > 0 || Boolean(w.branch) || (!w.main && w.name !== tree.name);
}

type RepoBucket = { tree: RepoTree; orca: boolean; git: boolean; bySpace: Map<string, RepoWorktree> };

/** Build one computer's repositories. Learn Orca worktrees from live Orca-hosted sessions first, so every agent source
 *  running in one can share its exact identity; without a live Orca-hosted session its path is not known to be Orca's. */
function buildRepos(sessions: Session[], machineId: string): RepoTree[] {
  const known = new Map<string, OrcaWorktree>();
  for (const s of sessions) {
    const worktree = orcaSpace(s);
    if (worktree && !known.has(worktree.path)) known.set(worktree.path, worktree);
  }
  const repos = new Map<string, RepoBucket>();
  for (const s of sessions) {
    const where = placementOf(s, known);
    const key = `${machineId}\u0000${where.repo}`;
    let bucket = repos.get(key);
    if (!bucket) {
      bucket = {
        tree: { key, machineId, name: where.name, repo: where.repo, count: 0, worktrees: [] },
        orca: Boolean(where.spaceId),
        git: Boolean(where.git),
        bySpace: new Map(),
      };
      repos.set(key, bucket);
    }
    const { tree } = bucket;
    const identity = where.spaceId ?? where.worktree;
    const worktreeKey = `${key}\u0000${identity}`;
    let worktree = tree.worktrees.find((w) => w.key === worktreeKey);
    if (!worktree) {
      worktree = {
        key: worktreeKey,
        name: where.spaceId ? baseName(where.path) : worktreeName(where.worktree, where.repo),
        path: where.path,
        main: where.main,
        sessions: [],
        children: [],
        tree: [],
        childStates: { needs_you: 0, working: 0, done: 0 },
      };
      tree.worktrees.push(worktree);
    }
    if (where.spaceId && !bucket.bySpace.has(where.spaceId)) bucket.bySpace.set(where.spaceId, worktree);
    if (where.git) bucket.git = true;
    // Orca's sidebar and git name a working tree by its branch.
    if (!worktree.branch && s.branch) {
      worktree.branch = s.branch;
      worktree.name = s.branch;
    }
    worktree.sessions.push(s);
    tree.count++;
  }
  return [...repos.values()].map((bucket) => {
    const { tree } = bucket;
    for (const w of tree.worktrees) {
      w.sessions.sort(byAttention);
      if (bucket.orca && !bucket.git) w.main = w.sessions.some((s) => (s.repo_root ?? "").replace(/\/+$/, "") === w.path);
    }
    const worktrees = nestWorktrees(tree.worktrees, bucket.bySpace)
      .sort((a, b) => Number(b.main) - Number(a.main) || a.name.localeCompare(b.name));
    return { ...tree, name: bucket.orca && !bucket.git ? orcaProjectName(tree.worktrees) : tree.name, worktrees };
  }).sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * The sidebar's repository tree: every live agent under the repository its cwd names, that repository's working trees
 * under it. Repositories sort by name (then machine), a tree's main checkout comes first, the rest by name; agents
 * inside a tree keep the sidebar's own order (needs-you first).
 */
export function buildRepoTree(all: Session[], machines: Machine[]): RepoTree[] {
  const order = (m: string) => machines.findIndex((x) => x.id === m);
  return buildRepoMachineTree(all, machines)
    .flatMap((m) => m.repos)
    .sort((a, b) => a.name.localeCompare(b.name) || order(a.machineId) - order(b.machineId));
}

/**
 * The sidebar's repository tree partitioned by computer (#2108 / #2126): each computer with live agents gets its own
 * section, kept in the hub's machine order; repositories within each computer sort alphabetically by name.
 */
export function buildRepoMachineTree(all: Session[], machines: Machine[]): RepoMachine[] {
  const online = new Set(machines.filter((m) => m.online).map((m) => m.id));
  const byMachine = new Map<string, Session[]>();
  for (const s of all) {
    if (s.assistant || !online.has(s.machine_id)) continue;
    let list = byMachine.get(s.machine_id);
    if (!list) {
      list = [];
      byMachine.set(s.machine_id, list);
    }
    list.push(s);
  }

  const out: RepoMachine[] = [];
  for (const m of machines) {
    const sessions = byMachine.get(m.id);
    if (!sessions || sessions.length === 0) continue;
    out.push({
      key: `repo-machine:${m.id}`,
      machine: m,
      foldKey: `repo-machine:${m.id}`,
      repos: buildRepos(sessions, m.id),
      sessions,
      needs: sessions.filter((s) => stateOf(s) === "needs_you").length,
    });
  }
  return out;
}
