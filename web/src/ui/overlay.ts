// The pure parts of the overlays in atoms.tsx (#1996): where Tab goes in a dialog, where a menu sits on screen.
import { useEffect, useRef, type RefObject } from "react";
import { NARROW, PHONE, useMedia } from "../lib/viewport";

/** A phone-width screen: Dialog is a bottom sheet, Settings a list of categories (Tailwind's max-sm). */
export const SHEET_QUERY = PHONE;
/** Narrower than the sidebar's breakpoint (lg): a menu keeps to the visible viewport above the keyboard. */
export const NARROW_QUERY = NARROW;
export { useMedia };

/** Tab inside a modal: the index to move focus to so it stays in, or null where the browser's own order already does
 *  (between two of its controls). `current` is -1 when the focus is not on one of the `count` controls. */
export function trapTab(count: number, current: number, back: boolean): number | null {
  if (count === 0) return -1; // nothing to land on: the dialog itself keeps it
  if (current < 0) return back ? count - 1 : 0;
  if (back && current === 0) return count - 1;
  if (!back && current === count - 1) return 0;
  return null;
}

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"]), [contenteditable="true"]';

/** The controls Tab can reach in `root`, in document order (shown and not inert). */
export function focusables(root: HTMLElement): HTMLElement[] {
  return [...root.querySelectorAll<HTMLElement>(FOCUSABLE)].filter((el) => el.getClientRects().length > 0 && !el.closest("[inert]"));
}

// Where each closed modal gives the focus back, by its element: its effect's cleanup notes it, and opening again drops it.
const closedOpeners = new WeakMap<Element, Element | null>();

/** The opener a modal gives the focus back to, from what had the focus while it rendered its first open frame. When it
 *  opens in the same render another modal closes (a Palette command or a Dialog button that opens a Dialog), that is
 *  inside the closing one, which is about to unmount: the focus goes back to that modal's own opener instead. Every
 *  cleanup in a commit runs before any setup, so the closing modal has noted its opener by then. */
function handedOver(opener: Element | null): Element | null {
  for (let node = opener; node; node = node.parentElement) if (closedOpeners.has(node)) return closedOpeners.get(node) ?? null;
  return opener;
}

/** While `open`, `ref`'s element is a modal for the keyboard, as Dialog in atoms.tsx does it: focus moves into it (the
 *  element itself, which needs tabIndex={-1}, unless something inside has it already), Tab and Shift+Tab wrap among its
 *  controls, and on close the focus goes back to where it was before (the button that opened it), unless the close moved it
 *  somewhere else on purpose. */
export function useModalFocus(ref: RefObject<HTMLElement | null>, open = true): void {
  // The opener is read while rendering the first open frame: React focuses an autoFocus field inside while it commits,
  // before any effect runs, so an effect would see that field and give the focus back to it (then to body) on close.
  const opener = useRef<Element | null>(null);
  if (!open) opener.current = null;
  else if (opener.current === null) opener.current = document.activeElement;
  // What had the focus inside at the last cleanup. StrictMode replays the effect of a modal mounted open (cleanup, which
  // gives the focus back to the opener, then setup), and an autoFocus field does not take it again by itself.
  const inside = useRef<Element | null>(null);
  useEffect(() => {
    const el = ref.current;
    const was = inside.current;
    inside.current = null;
    if (!open || !el) return;
    closedOpeners.delete(el);
    const back = handedOver(opener.current);
    if (!el.contains(document.activeElement)) (was instanceof HTMLElement && el.contains(was) ? was : el).focus({ preventScroll: true });
    const tab = (e: KeyboardEvent) => {
      if (e.key !== "Tab") return;
      const list = focusables(el);
      const i = trapTab(list.length, list.indexOf(document.activeElement as HTMLElement), e.shiftKey);
      if (i === null) return;
      e.preventDefault();
      (list[i] ?? el).focus();
    };
    el.addEventListener("keydown", tab);
    return () => {
      el.removeEventListener("keydown", tab);
      inside.current = el.contains(document.activeElement) ? document.activeElement : null;
      closedOpeners.set(el, back);
      returnFocus(el, back);
    };
  }, [ref, open]);
}

/** An overlay or panel `el` closed: the focus goes back to `back` (what opened it), or to `fallback` when `back` is gone
 *  or can't take it (hidden). Only when the focus is still ours: inside `el`, or lost to body when `el` left the DOM. A
 *  close that moved focus on purpose (attaching an image focuses the composer) keeps it there. */
export function returnFocus(el: HTMLElement, back: Element | null, fallback?: () => void): void {
  const active = document.activeElement;
  const ours = !active || active === document.body || el.contains(active);
  if (!ours) return;
  const to = back instanceof HTMLElement && back !== document.body && back.isConnected ? back : null;
  to?.focus({ preventScroll: true });
  if (!to || document.activeElement !== to) fallback?.();
}

export interface MenuPlace {
  left: number;
  top: number;
  width: number;
  /** Set only when the visible viewport is shorter than the menu's own cap (a phone with the keyboard up). */
  maxHeight?: number;
}

/** Where a menu of height `h` opens next to `anchor`, kept 8px inside the area it may use: the window, or on a phone
 *  the visible viewport (`view`: its top and height, from --kb-top / --kb-h) the soft keyboard leaves. */
export function placeMenu(
  anchor: { left: number; right: number; top: number; bottom: number },
  h: number,
  o: { width: number; align: "left" | "right"; side: "top" | "bottom"; screenW: number; screenH: number; view?: { top: number; height: number }; gap?: number },
): MenuPlace {
  const width = Math.min(o.width, o.screenW - 16);
  const left = Math.max(8, Math.min(o.align === "left" ? anchor.left : anchor.right - width, o.screenW - width - 8));
  const lo = (o.view?.top ?? 0) + 8;
  const hi = o.view ? o.view.top + o.view.height - 8 : o.screenH - 8;
  const maxHeight = o.view ? Math.max(0, hi - lo) : undefined;
  const fit = Math.min(h, maxHeight ?? h);
  const gap = o.gap ?? 6; // a menu at the cursor has none: it opens at the pointer
  let top = o.side === "bottom" ? anchor.bottom + gap : anchor.top - fit - gap;
  if (o.side === "bottom" && top + fit > hi) top = anchor.top - fit - gap;
  if (top < lo) top = lo;
  if (o.view && top + fit > hi) top = Math.max(lo, hi - fit);
  return maxHeight === undefined ? { left, top, width } : { left, top, width, maxHeight };
}

/** Where a submenu of height `h` and width `width` opens next to the row (`row`) that opened it: beside it on the right,
 *  flush with the row's top and overlapping the parent by a few px so the pointer crosses no gap; on the left when the
 *  right has no room; kept 8px inside the area it may use (see placeMenu). */
export function placeSubmenu(
  row: { left: number; right: number; top: number; bottom: number },
  h: number,
  o: { width: number; screenW: number; screenH: number; view?: { top: number; height: number } },
): MenuPlace {
  const width = Math.min(o.width, o.screenW - 16);
  const overlap = 4;
  let left = row.right - overlap;
  if (left + width > o.screenW - 8) left = row.left - width + overlap;
  left = Math.max(8, Math.min(left, o.screenW - width - 8));
  const lo = (o.view?.top ?? 0) + 8;
  const hi = o.view ? o.view.top + o.view.height - 8 : o.screenH - 8;
  const maxHeight = o.view ? Math.max(0, hi - lo) : undefined;
  const fit = Math.min(h, maxHeight ?? h);
  let top = row.top - overlap; // the menu's own padding: the first row lines up with the one that opened it
  if (top + fit > hi) top = hi - fit;
  if (top < lo) top = lo;
  return maxHeight === undefined ? { left, top, width } : { left, top, width, maxHeight };
}

// Escape closes one overlay, the topmost. Each open overlay that closes on Escape has an entry here; one window listener
// runs the onClose of the entry opened last and stops the event, so a zoomed diagram in a Dialog, or a Dialog over a
// Dialog, closes alone and the one under it stays open for the next Escape.
type EscapeEntry = { order: number; close: () => void };
const escapeStack: EscapeEntry[] = [];
let escapeOrder = 0;

function onEscapeKey(e: KeyboardEvent): void {
  if (e.key !== "Escape") return;
  let top: EscapeEntry | undefined;
  for (const entry of escapeStack) if (!top || entry.order > top.order) top = entry;
  if (!top) return;
  e.stopPropagation();
  top.close();
}

/** While `open`, Escape anywhere calls `onClose`, but only for the most recently opened overlay that uses this hook.
 *  The order is taken when `open` turns true during render, which React does parents first, so an overlay inside
 *  another (even both open from the first render, whose effects run children first) still counts as above it. */
export function useEscapeClose(open: boolean, onClose: () => void): void {
  const close = useRef(onClose);
  const order = useRef(0);
  if (!open) order.current = 0;
  else if (!order.current) order.current = ++escapeOrder;
  useEffect(() => {
    close.current = onClose;
  });
  useEffect(() => {
    if (!open) return;
    const entry: EscapeEntry = { order: order.current, close: () => close.current() };
    if (escapeStack.length === 0) addEventListener("keydown", onEscapeKey);
    escapeStack.push(entry);
    return () => {
      escapeStack.splice(escapeStack.indexOf(entry), 1);
      if (escapeStack.length === 0) removeEventListener("keydown", onEscapeKey);
    };
  }, [open]);
}
