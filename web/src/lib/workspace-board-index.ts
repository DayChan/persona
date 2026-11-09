// Lookups the workflow board makes for every card, built once per change of their source instead of once per card per
// render: a search keystroke used to filter every session for every card (cards x sessions directory() calls).
// Each lookup gives what the scan it replaces gives (cardSessions, Array.find), in the same order.
import type { Machine, Session } from "./types";
import { directory, type BoardCard, type Workspace } from "./workspace-board";

/** What a card with no Agent gets: one shared array, so a memoized card sees no change. */
export const NO_SESSIONS: readonly Session[] = Object.freeze([]);

// directory() never returns a path with a NUL in it, so the last NUL of a key splits machine and directory: no two
// different pairs share a key. null: the path is not absolute, which sameDirectory never matches.
function placeKey(machineID: string, cwd: string): string | null {
  const dir = directory(cwd);
  return dir === null ? null : `${machineID}\0${dir}`;
}

/** cardSessions(card, sessions) for any card, after one pass over the sessions. */
export function sessionsByCard(sessions: readonly Session[]): (card: BoardCard) => readonly Session[] {
  const groups = new Map<string, Session[]>();
  for (const s of sessions) {
    const key = placeKey(s.machine_id, s.workspace_cwd || s.cwd);
    if (key === null) continue;
    const group = groups.get(key);
    if (group) group.push(s); else groups.set(key, [s]);
  }
  return (card) => {
    const key = placeKey(card.machine_id, card.cwd);
    return (key !== null && groups.get(key)) || NO_SESSIONS;
  };
}

/** machines.find((m) => m.id === id): the first machine with that id. */
export function machinesByID(machines: readonly Machine[]): (id: string) => Machine | undefined {
  const byID = new Map<string, Machine>();
  for (const m of machines) if (!byID.has(m.id)) byID.set(m.id, m);
  return (id) => byID.get(id);
}

/** The first workspace bound to the card by id, or on the card's machine in the card's directory, as find() would pick. */
export function workspacesByCard(workspaces: readonly Workspace[]): (card: BoardCard) => Workspace | undefined {
  const byID = new Map<string, number>(), byPlace = new Map<string, number>();
  workspaces.forEach((w, i) => {
    if (!byID.has(w.id)) byID.set(w.id, i);
    const key = placeKey(w.machine_id, w.cwd);
    if (key !== null && !byPlace.has(key)) byPlace.set(key, i);
  });
  return (card) => {
    const key = placeKey(card.machine_id, card.cwd);
    const i = byID.get(card.workspace_id), j = key === null ? undefined : byPlace.get(key);
    const first = i === undefined ? j : j === undefined ? i : Math.min(i, j);
    return first === undefined ? undefined : workspaces[first];
  };
}
