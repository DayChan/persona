// Auto flow (#2648): the board's optional second view. The manual columns stay what they are; these lanes are
// computed from the agents a card has, so a card moves as its agents change, and nothing is persisted for them.
// Needs you → Working → To review → Idle; reviewing and clearing a card is the existing Archive action.
import { stateOf } from "./format";
import { sameDirectory } from "./workspace-board";
import { todoStatus, type SessionLookup, type Todo } from "./todos";
import type { BoardCard } from "./workspace-board";
import type { Session } from "./types";

export type AutoLane = "needs_you" | "working" | "review" | "idle";
export const AUTO_LANES: readonly AutoLane[] = ["needs_you", "working", "review", "idle"];

/** A card's lane: the most urgent thing any of its agents says. No agents, or only offline ones: idle. */
export function autoLaneOf(sessions: readonly Session[]): AutoLane {
  if (sessions.some((s) => stateOf(s) === "needs_you")) return "needs_you";
  if (sessions.some((s) => stateOf(s) === "working")) return "working";
  if (sessions.some((s) => stateOf(s) !== "offline")) return "review";
  return "idle";
}

/** Latest agent activity, for ordering; cards without any go last. */
export function autoActivity(sessions: readonly Session[]): number {
  let latest = 0;
  for (const s of sessions) {
    const at = Date.parse(s.updated_at ?? s.state_since ?? "");
    if (Number.isFinite(at) && at > latest) latest = at;
  }
  return latest;
}

/** The session to open from a card: the one needing you, else a working one, else the most recently active. */
export function autoFocusSession(sessions: readonly Session[]): Session | undefined {
  const needy = sessions.filter((s) => stateOf(s) === "needs_you");
  const working = sessions.filter((s) => stateOf(s) === "working");
  const pool = needy.length ? needy : working.length ? working : sessions;
  return [...pool].sort((a, b) => autoActivity([b]) - autoActivity([a]))[0];
}

/** One lane's cards: most recent agent activity first, then pinned, then title so the order is stable. */
export function autoSort(cards: readonly BoardCard[], sessionsOf: (card: BoardCard) => readonly Session[]): BoardCard[] {
  return [...cards].sort((a, b) => {
    const activity = autoActivity(sessionsOf(b)) - autoActivity(sessionsOf(a));
    if (activity) return activity;
    if (a.pinned !== b.pinned) return a.pinned ? -1 : 1;
    return a.title.localeCompare(b.title) || a.id.localeCompare(b.id);
  });
}

/** The lanes and their cards for the auto view, after the board's own search/machine/archived filters. */
export function autoLanes(
  cards: readonly BoardCard[],
  sessionsOf: (card: BoardCard) => readonly Session[],
): { lane: AutoLane; cards: BoardCard[] }[] {
  return AUTO_LANES.map((lane) => ({
    lane,
    cards: autoSort(cards.filter((c) => autoLaneOf(sessionsOf(c)) === lane), sessionsOf),
  }));
}

// ---- All work in one panel (#2648): not only bound workspaces. Unbound agent sessions and to-dos enter the same
// lanes, so the auto view is "everything in flight", not only what the user bound by hand.

export type WorkLane = AutoLane | "planned";
export const WORK_LANES: readonly WorkLane[] = ["needs_you", "working", "review", "planned", "idle"];

export interface WorkItem {
  kind: "workspace" | "session" | "todo";
  id: string;
  lane: WorkLane;
  title: string;
  activity: number;
  pinned: boolean;
  card?: BoardCard;
  session?: Session;
  todo?: Todo;
}

export function workspaceItem(card: BoardCard, sessions: readonly Session[]): WorkItem {
  return { kind: "workspace", id: `workspace:${card.id}`, lane: autoLaneOf(sessions), title: card.title, activity: autoActivity(sessions), pinned: card.pinned, card };
}

export function sessionItem(s: Session): WorkItem {
  const st = stateOf(s);
  const lane: WorkLane = st === "needs_you" ? "needs_you" : st === "working" ? "working" : st === "offline" ? "idle" : "review";
  return { kind: "session", id: `session:${s.machine_id}/${s.session_id}`, lane, title: s.title || s.session_id, activity: autoActivity([s]), pinned: false, session: s };
}

/** A to-do follows todoStatus: what needs an answer is Needs you, finished work is To review, the rest is Planned. */
export function todoItem(t: Todo, lookup?: SessionLookup): WorkItem {
  const st = todoStatus(t, lookup);
  const lane: WorkLane =
    st === "needs_you" || st === "interrupted" || st === "usage_limit" || st === "failed" ? "needs_you"
    : st === "working" ? "working"
    : st === "done" || st === "unread" ? "review"
    : "planned";
  return { kind: "todo", id: `todo:${t.id}`, lane, title: t.title, activity: Date.parse(t.updated_at ?? "") || 0, pinned: false, todo: t };
}

/** Sessions no workspace card claims (a different machine or directory), so they appear on the board too. The main
 *  assistant conversation is not an agent task and stays out. */
export function unboundSessions(sessions: readonly Session[], cards: readonly BoardCard[]): Session[] {
  return sessions.filter((s) => !s.assistant && !cards.some((c) => c.machine_id === s.machine_id && sameDirectory(s.workspace_cwd || s.cwd, c.cwd)));
}

/** Every work item on the board, in its lane: most recent activity first, then pinned, then title. */
export function workLanes(items: readonly WorkItem[]): { lane: WorkLane; items: WorkItem[] }[] {
  return WORK_LANES.map((lane) => ({
    lane,
    items: items
      .filter((i) => i.lane === lane)
      .sort((a, b) => (b.activity - a.activity) || (a.pinned === b.pinned ? 0 : a.pinned ? -1 : 1) || a.title.localeCompare(b.title) || a.id.localeCompare(b.id)),
  }));
}
