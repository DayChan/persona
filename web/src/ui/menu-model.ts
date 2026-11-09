// The pure half of the menu in atoms.tsx (#2329): what a key does in an open menu, whether a row needs a second
// activation, where the context-menu key opens it. No React, so every rule is tested without a DOM.
import type { AgentState } from "../lib/types";
import type { IconName } from "./icons";

export interface MenuRename {
  /** What the input starts with (selected, so typing replaces it). */
  value: string;
  placeholder?: string;
  /** Enter on a changed, non-empty name. A rejection is shown inline and the input stays. */
  commit: (name: string) => void | Promise<unknown>;
  /** Why what is typed can't be committed, "" when it can: said under the input as it is typed, and Enter waits for
   *  a good one (a model ID, #1644). */
  validate?: (text: string) => string;
  /** Under the input while nothing is wrong (what it takes). */
  hint?: string;
}

export interface MenuItem {
  key: string;
  label: string;
  /** A short note at the row's end that never wraps (a count, a unit, a shortcut). A sentence goes in `detail`. */
  hint?: string;
  /** A sentence under the label that explains the choice (#5584): it wraps at word breaks instead of crowding the
   *  label off the row, and screen readers read it as the row's description. */
  detail?: string;
  icon?: IconName;
  state?: AgentState;
  checked?: boolean;
  /** The row is one choice of a set and `checked` marks the chosen one (permission mode, effort, model): it is a
   *  menuitemradio with aria-checked, so screen readers say which is selected. A row that sets only `checked` (an
   *  on/off toggle, a filter) stays a plain menuitem. */
  radio?: boolean;
  danger?: boolean;
  disabled?: boolean;
  divider?: boolean; // a hairline above this row
  submenu?: boolean; // shows a › and keeps the menu open (the caller swaps the items)
  keepOpen?: boolean;
  /** Runs when the row is chosen. Unused by a row with `children` or `rename`. */
  onSelect: () => void | Promise<unknown>;
  /** A single letter that chooses the row while the menu is open; shown at the row's end. */
  accel?: string;
  /** Asks again before running: the first choice turns the row into this label ("Close"), the second runs it. */
  confirm?: string;
  /** The row ⌫ / Delete chooses (the menu's close action). */
  closes?: boolean;
  /** A submenu (→ opens it, ← goes back). A function loads the rows when it opens ("Loading…" meanwhile). */
  children?: MenuItem[] | (() => Promise<MenuItem[]>);
  /** The row turns into an input when chosen: Enter commits, Esc cancels. */
  rename?: MenuRename;
  /** Awaits `onSelect` inside the menu: the row says "Working…", an error shows under the rows and the menu stays, and
   *  the menu closes once it succeeds. Without it the menu closes at once, as it always did. */
  inline?: boolean;
  /** The label wraps (breaking anywhere) instead of ending in …: a value that must be read in full, like a path. */
  wrap?: boolean;
  /** A line to read, not a choice (#4054): plain text that wraps at word breaks, with the hint after it; the keys and
   *  the pointer pass over it. A menu of notes only is a popover that explains something. */
  note?: boolean;
  /** A group's small title over the rows after it ("Typed before"), not a choice: the keys and the pointer pass over it. */
  heading?: boolean;
  /** The label is a value typed by hand (a model ID): drawn monospaced, as the field it came from. */
  mono?: boolean;
}

export const enabled = (it: MenuItem | undefined): it is MenuItem => Boolean(it) && !it!.disabled && !it!.note && !it!.heading;

/** The next row that can be chosen when moving `dir` (1 down, -1 up) from `from` (-1: none yet), wrapping round; `from`
 *  when nothing can. Moving up from nothing lands on the last row. */
export function step(items: readonly MenuItem[], from: number, dir: 1 | -1): number {
  const n = items.length;
  let i = from;
  for (let k = 0; k < n; k++) {
    i = i < 0 ? (dir === 1 ? 0 : n - 1) : (i + dir + n) % n;
    if (enabled(items[i])) return i;
  }
  return from;
}

/** The first or last row that can be chosen. */
export function edge(items: readonly MenuItem[], last: boolean): number {
  const order = items.map((_, i) => i);
  for (const i of last ? order.reverse() : order) if (enabled(items[i])) return i;
  return -1;
}

/** The row a letter chooses ("" or a longer name matches none). */
export function accelTarget(items: readonly MenuItem[], key: string): number {
  if (key.length !== 1) return -1;
  const k = key.toLowerCase();
  return items.findIndex((it) => enabled(it) && it.accel?.toLowerCase() === k);
}

/** The row ⌫ / Delete chooses: the close action. */
export function closeTarget(items: readonly MenuItem[]): number {
  return items.findIndex((it) => enabled(it) && it.closes);
}

export interface KeyInput {
  key: string;
  ctrlKey?: boolean;
  metaKey?: boolean;
  altKey?: boolean;
}

/** What a key asks of the menu, given the level it is in (`depth` 0 is the first level). */
export type Intent =
  | { type: "move"; index: number }
  | { type: "choose"; index: number }
  | { type: "back" }
  | { type: "close" }
  /** Tab and Shift+Tab: the menu closes and the key keeps its default, so the focus moves on from the menu's button. */
  | { type: "leave" }
  | { type: "none" };

const NONE: Intent = { type: "none" };

export function keyIntent(e: KeyInput, items: readonly MenuItem[], active: number, depth: number): Intent {
  if (e.ctrlKey || e.metaKey || e.altKey) return NONE;
  switch (e.key) {
    case "Escape":
      return { type: "close" };
    case "Tab":
      return { type: "leave" };
    case "ArrowDown":
    case "ArrowUp": {
      const index = step(items, active, e.key === "ArrowDown" ? 1 : -1);
      return index < 0 ? NONE : { type: "move", index };
    }
    case "Home":
    case "End": {
      const index = edge(items, e.key === "End");
      return index < 0 ? NONE : { type: "move", index };
    }
    case "ArrowRight":
      // → opens the submenu of the row it is on; nowhere else
      return enabled(items[active]) && items[active].children ? { type: "choose", index: active } : NONE;
    case "ArrowLeft":
      return depth > 0 ? { type: "back" } : NONE;
    case "Enter":
      return enabled(items[active]) ? { type: "choose", index: active } : NONE;
    case "Backspace":
    case "Delete": {
      const index = closeTarget(items);
      return index < 0 ? NONE : { type: "choose", index };
    }
  }
  const index = accelTarget(items, e.key);
  return index < 0 ? NONE : { type: "choose", index };
}

/** What choosing a row does: run it, ask again first, open its submenu, or turn it into an input. */
export type Activation = "run" | "confirm" | "children" | "rename" | "none";

export function activation(it: MenuItem | undefined, confirming: string | null): Activation {
  if (!enabled(it)) return "none";
  if (it.children) return "children";
  if (it.rename) return "rename";
  if (it.confirm && confirming !== it.key) return "confirm";
  return "run";
}

/** The label a row shows: the question once its first choice was made. */
export function rowLabel(it: MenuItem, confirming: string | null): string {
  return it.confirm && confirming === it.key ? it.confirm : it.label;
}

/** What the accelerator column shows: the letter, or ⌫ for the close action. */
export function accelLabel(it: MenuItem): string | undefined {
  return it.accel ? it.accel.toUpperCase() : it.closes ? "⌫" : undefined;
}

/** What a rename input does with Enter: nothing to commit (empty or unchanged: it just closes the input) or the name. */
export function renameCommit(current: string, typed: string): string | null {
  const name = typed.trim();
  return name && name !== current.trim() ? name : null;
}

// ── opening a menu on a row ───────────────────────────────

/** Where a node's menu opens: at the pointer (right click), or on the row's own button / the row (the ⋯, the menu
 *  key, Shift+F10). */
export interface MenuAt {
  point?: { x: number; y: number };
  anchor?: HTMLElement;
}

/** A contextmenu event's place: its pointer position, or the row when there is none (the menu key and Shift+F10 send
 *  the event with no position, 0,0 in Chrome and Firefox). */
export function menuAtFromContext(e: { clientX: number; clientY: number; currentTarget: HTMLElement }): MenuAt {
  if (e.clientX === 0 && e.clientY === 0) return { anchor: e.currentTarget };
  return { point: { x: e.clientX, y: e.clientY } };
}

/** The keys that ask for a context menu from the keyboard (a browser that sends no contextmenu event for them). */
export function isContextKey(e: { key: string; shiftKey?: boolean; ctrlKey?: boolean; metaKey?: boolean; altKey?: boolean }): boolean {
  if (e.ctrlKey || e.metaKey || e.altKey) return false;
  return e.key === "ContextMenu" || (e.key === "F10" && Boolean(e.shiftKey));
}
