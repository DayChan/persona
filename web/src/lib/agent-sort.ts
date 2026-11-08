// How the Agents list orders its agents (iOS / Mac AgentSort, `dev.sort`; G099): "State first" (the default) keeps every
// grouping's own order, "Recently updated" orders the rows by time only and each grouping's sections (machines, spaces, repositories,
// tabs) by their latest agent, except that "By repo" keeps its machines in order. Under "By status" the sections are the states, so the choice is not offered there.
import { t } from "../i18n";
import type { Space } from "./agent-groups";
import type { RepoMachine, RepoWorktree } from "./agent-repos";
import { stateOf } from "./format";
import type { HerdrMachine, HerdrSpace } from "./herdr-tree";
import type { TodoTree, TreeAgent } from "./todo-tree";
import type { Session } from "./types";
import { byTime, timeMs } from "./times";

export type AgentSort = "state" | "updated";

export const AGENT_SORT_KEY = "persona.agentSort";

export const AGENT_SORTS: { key: AgentSort; label: string }[] = [
  { key: "state", label: t("lib-agent-sort.state") },
  { key: "updated", label: t("lib-agent-sort.updated") },
];

/** The order this browser chose last; "State first" until it chooses. */
export function agentSort(value: string | null): AgentSort {
  return value === "updated" ? "updated" : "state";
}

/** An agent's time: a working one by when it started working, so its every line does not move it. */
export function sortDate(s: Session): string {
  return (stateOf(s) === "working" ? s.state_since || s.updated_at : s.updated_at) ?? "";
}

/** The latest of the agents' times ("" for none). */
export function latestOf(sessions: readonly Session[]): string {
  let out = "";
  for (const s of sessions) {
    const d = sortDate(s);
    if (timeMs(d) > timeMs(out)) out = d;
  }
  return out;
}

/** The most recently updated first, then by id so equal times never swap between renders. */
export function byUpdated(a: Session, b: Session): number {
  return byTime(sortDate(b), sortDate(a)) || a.session_id.localeCompare(b.session_id) || a.machine_id.localeCompare(b.machine_id);
}

/** `list` with the most recent first (`date`), ties by `id`; a new array. */
export function latestFirst<T>(list: readonly T[], date: (x: T) => string, id: (x: T) => string): T[] {
  return list.map((x) => ({ x, d: date(x), k: id(x) }))
    .sort((a, b) => byTime(b.d, a.d) || a.k.localeCompare(b.k))
    .map(({ x }) => x);
}

/** "By project" under "Recently updated": the spaces by their latest agent, the agents inside by time. `arrangeSpaces` still puts the
 *  pinned ones first and folds the quiet ones. */
export function spacesByUpdate(spaces: readonly Space[]): Space[] {
  return latestFirst(spaces.map((sp) => ({ ...sp, sessions: [...sp.sessions].sort(byUpdated) })), (sp) => latestOf(sp.sessions), (sp) => sp.key);
}

/** "By project" under "Recently updated", both halves: the spaces as `spacesByUpdate`, and "Other agents" (`loose`) by time too, as "By machine"'s
 *  `other` is. */
export function projectsByUpdate<T extends { spaces: readonly Space[]; loose: readonly Session[] }>(built: T): T {
  return { ...built, spaces: spacesByUpdate(built.spaces), loose: [...built.loose].sort(byUpdated) };
}

function spaceByUpdate(sp: HerdrSpace): HerdrSpace {
  const tabs = latestFirst(sp.tabs, (tab) => latestOf(tab.sessions), (tab) => tab.key);
  const children = latestFirst(sp.children.map(spaceByUpdate), (c) => latestOf(c.tree), (c) => c.key);
  return { ...sp, tabs, children, loose: [...sp.loose].sort(byUpdated) };
}

/** "By machine" under "Recently updated": the machines, their spaces, nested spaces and tabs by their latest agent (pinned spaces still
 *  lead their machine); a tab's panes keep their layout order. */
export function machineTreeByUpdate(tree: readonly HerdrMachine[]): HerdrMachine[] {
  const machines = tree.map((m) => {
    const spaces = m.spaces.map(spaceByUpdate);
    const pinned = spaces.filter((sp) => sp.pinned);
    const rest = spaces.filter((sp) => !sp.pinned);
    const latest = (list: HerdrSpace[]) => latestFirst(list, (sp) => latestOf(sp.tree), (sp) => sp.key);
    return { ...m, spaces: [...latest(pinned), ...latest(rest)], other: [...m.other].sort(byUpdated) };
  });
  return latestFirst(machines, (m) => latestOf(m.sessions), (m) => m.key);
}

function repoWorktreeByUpdate(w: RepoWorktree): RepoWorktree {
  const children = latestFirst(w.children.map(repoWorktreeByUpdate), (c) => latestOf(c.tree), (c) => c.key);
  return { ...w, sessions: [...w.sessions].sort(byUpdated), children };
}

/** "By repo" under "Recently updated": each machine's repositories by their latest agent, including nested worktrees;
 *  the machines keep their order (as on iOS, so a section does not jump when another computer's agent updates); a
 *  repository's main checkout still comes first, child worktrees sort by their latest descendant, and agents by time. */
export function repoMachinesByUpdate(list: readonly RepoMachine[]): RepoMachine[] {
  return list.map((m) => ({
    ...m,
    repos: latestFirst(
      m.repos.map((r) => ({ ...r, worktrees: r.worktrees.map(repoWorktreeByUpdate) })),
      (r) => latestOf(r.worktrees.flatMap((w) => w.tree)),
      (r) => r.key,
    ),
  }));
}

function agentsByUpdate(agents: readonly TreeAgent[]): TreeAgent[] {
  return agents.map((a) => ({ ...a, children: agentsByUpdate(a.children) })).sort((a, b) => byUpdated(a.session, b.session));
}

/** "By to-do" under "Recently updated": the todos keep their order (they are ranked by their own status), the agents under each by time. */
export function todoTreeByUpdate(tree: TodoTree): TodoTree {
  return {
    projects: tree.projects.map((p) => ({ ...p, todos: p.todos.map((td) => ({ ...td, agents: agentsByUpdate(td.agents) })) })),
    loose: tree.loose.map((g) => ({ ...g, projects: g.projects.map((td) => ({ ...td, agents: agentsByUpdate(td.agents) })) })),
  };
}
