// Where a page's own scroller was, by page (#5133): Overview and Settings's first page unmount or swap their content when a row
// opens something, so Back used to land at the top. The inbox keeps its list mounted under the item and needs none.
import { useLayoutEffect, useRef, type RefObject } from "react";
import { takeReturn } from "./router";

const where = new Map<string, number>();
// Content that arrives after the page mounts (a store push, an extension's cards) may be needed to reach the old spot:
// keep trying while it grows, for this long at most, and not after the reader moves the page themselves.
const SETTLE_MS = 1500;
const READER = ["wheel", "touchstart", "pointerdown", "keydown"] as const;

export function rememberScroll(key: string, top: number) {
  where.set(key, top);
}

/** Tests: forget every page. */
export function forgetScrolls() {
  where.clear();
}

/** What `key` opens at: the remembered spot on a return (Back / Forward), else the top. */
export function scrollTarget(key: string, returned: boolean): number {
  return returned ? (where.get(key) ?? 0) : 0;
}

/** Remembers `box`'s scrollTop under `key` and, when `key` is reached by Back / Forward, scrolls back to it; a fresh
 *  visit (a link, a tap on a row) starts at the top. `key` names the page the scroller shows (its route, its sub-page). */
export function useScrollMemory(box: RefObject<HTMLElement | null>, key: string) {
  const at = useRef(key);
  // decided once per key: StrictMode runs the effect twice, and the second run must not read the return as used up
  const decided = useRef<{ key: string; target: number } | null>(null);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    at.current = key;
    if (decided.current?.key !== key) decided.current = { key, target: scrollTarget(key, takeReturn()) };
    const { target } = decided.current;
    el.scrollTop = target;
    if (target === 0 || el.scrollTop >= target - 1) return;
    let ro: ResizeObserver | undefined;
    const stop = () => {
      ro?.disconnect();
      ro = undefined;
      clearTimeout(timer);
      for (const ev of READER) el.removeEventListener(ev, stop);
    };
    const timer = setTimeout(stop, SETTLE_MS);
    for (const ev of READER) el.addEventListener(ev, stop, { passive: true });
    if (typeof ResizeObserver !== "undefined") {
      ro = new ResizeObserver(() => {
        el.scrollTop = target;
        if (el.scrollTop >= target - 1) stop();
      });
      for (const child of Array.from(el.children)) ro.observe(child);
    }
    return stop;
  }, [box, key]);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    const save = () => rememberScroll(at.current, el.scrollTop);
    el.addEventListener("scroll", save, { passive: true });
    return () => el.removeEventListener("scroll", save);
  }, [box]);
}
