// Which work groups, cards and thoughts the reader opened (cindy's useExpandedBlockMemory), kept for the page's life:
// a group that goes from live to done, or remounts when a page of history lands above it, stays as the reader left it.
import { useCallback, useEffect, useState } from "react";

const opened = new Map<string, boolean>();
/** The drawn groups, told when something outside them opened theirs (`openExpanded`). */
const openListeners = new Set<(keys: ReadonlySet<string>) => void>();

/** Opens these keys from outside their groups ("Jump to the step that started it"): the ones on screen redraw open. */
export function openExpanded(keys: string[]) {
  if (!keys.length) return;
  for (const k of keys) opened.set(k, true);
  const set = new Set(keys);
  openListeners.forEach((l) => l(set));
}
/** Folded ranges (seq … through_seq) the reader opened, by chat: the groups their steps land in start open, whatever
 *  key they get once the range is unfolded (a plan card taken out of it moves the first step). */
const foldRanges = new Map<string, [number, number][]>();

/** Forget what was opened: for tests, whose rows reuse the same ids. */
export function forgetExpanded() {
  opened.clear();
  liveOverrides.clear();
  foldRanges.clear();
}

export function openFoldRange(scope: string, from: number, through: number) {
  foldRanges.set(scope, [...(foldRanges.get(scope) ?? []), [from, through]]);
}

export function inOpenedFold(scope: string, seq?: number): boolean {
  return seq !== undefined && (foldRanges.get(scope) ?? []).some(([a, b]) => seq >= a && seq <= b);
}

export function useExpanded(key: string, initial = false): [boolean, (next: boolean | ((v: boolean) => boolean)) => void] {
  const [, bump] = useState(0);
  const value = opened.get(key) ?? initial;
  useEffect(() => {
    const listen = (keys: ReadonlySet<string>) => keys.has(key) && bump((n) => n + 1);
    openListeners.add(listen);
    return () => void openListeners.delete(listen);
  }, [key]);
  const set = useCallback(
    (next: boolean | ((v: boolean) => boolean)) => {
      const v = typeof next === "function" ? next(opened.get(key) ?? initial) : next;
      opened.set(key, v);
      bump((n) => n + 1);
    },
    [key, initial],
  );
  return [value, set];
}

/** What the reader set a work line to while its exchange ran (#1614, assistant chats): kept apart from `opened` so the
 *  run's toggles never outlive it, and across a remount (a page of history landing above) while it runs. */
const liveOverrides = new Map<string, boolean>();

/** The run's override of a work line: undefined until the reader toggles it; dropped once `running` goes false. */
export function useLiveOverride(key: string, running: boolean): [boolean | undefined, (next: boolean) => void] {
  const [, bump] = useState(0);
  useEffect(() => {
    if (!running) liveOverrides.delete(key);
  }, [key, running]);
  const set = useCallback(
    (next: boolean) => {
      liveOverrides.set(key, next);
      bump((n) => n + 1);
    },
    [key],
  );
  return [running ? liveOverrides.get(key) : undefined, set];
}
