// The keyboard shortcuts of the web client (docs/web/web.md "Keyboard shortcuts"): the registry of commands (id, label, default chord,
// scope), chord parsing / formatting per platform, the user's rebindings (localStorage), the reserved chords, and the
// dispatcher that App.tsx's keydown listener and the terminal's key handler call.
//
// Every chord carries Mod (⌘ on a Mac, Ctrl elsewhere): a bare key is never a shortcut, so typing and the terminal's own
// keys are never swallowed. Pure apart from the small store of rebindings, so it is tested without a DOM.
import { t } from "../i18n";
import { useSyncExternalStore } from "react";
import type { IconName } from "../ui/icons";
import { herdrOwnsKey, inFullSurface } from "./terminal-conflicts";

// ── platform ─────────────────────────────────────────────

export type Platform = "mac" | "other";

/** ⌘ is Mod on Apple platforms (Mac, iPad Safari asking for the desktop site), Ctrl everywhere else. */
export function platformOf(nav?: { platform?: string; userAgent?: string; userAgentData?: { platform?: string } }): Platform {
  const hint = nav?.userAgentData?.platform || nav?.platform || nav?.userAgent || "";
  return /mac|iphone|ipad|ipod/i.test(hint) ? "mac" : "other";
}

let detected: Platform | null = null;
export function currentPlatform(): Platform {
  detected ??= platformOf(typeof navigator === "undefined" ? undefined : (navigator as unknown as Parameters<typeof platformOf>[0]));
  return detected;
}

/** What the modifier is called on this platform, for messages: "needs Ctrl" / "needs ⌘". */
export const modName = (p: Platform) => (p === "mac" ? "⌘" : "Ctrl");

// ── chords ───────────────────────────────────────────────

/**
 * A chord is written platform-independently, modifiers in a fixed order and then the key: "Mod+Alt+Shift+Up".
 * Mod is required. A key is a letter, a digit, one of `,./;'[]\-=\``, Up / Down / Left / Right, Enter, Space, Home / End /
 * PageUp / PageDown or F1–F12. "1-9" stands for the digits 1 to 9 together (the one command that takes a number).
 */
export interface Chord {
  alt: boolean;
  shift: boolean;
  key: string;
}

export const DIGITS = "1-9";

const PUNCTUATION = ",./;'[]\\-=`";
const NAMED = ["Up", "Down", "Left", "Right", "Enter", "Space", "Home", "End", "PageUp", "PageDown"];

function normalKey(raw: string): string | null {
  if (raw === DIGITS) return DIGITS;
  if (raw.length === 1) {
    if (/[a-z]/i.test(raw)) return raw.toUpperCase();
    if (/[0-9]/.test(raw) || PUNCTUATION.includes(raw)) return raw;
    return null;
  }
  const named = NAMED.find((n) => n.toLowerCase() === raw.toLowerCase());
  if (named) return named;
  const f = raw.match(/^f([1-9]|1[0-2])$/i);
  return f ? `F${f[1]}` : null;
}

/** "Mod+Shift+P" → its parts; null when it is not a chord (no Mod, no key, an unknown key or modifier). */
export function parseChord(text: string): Chord | null {
  const parts = text.split("+").map((s) => s.trim());
  const key = normalKey(parts.pop() ?? "");
  if (!key || !parts.length) return null;
  let mod = false;
  const c: Chord = { alt: false, shift: false, key };
  for (const p of parts) {
    const m = p.toLowerCase();
    if (m === "mod" || m === "ctrl" || m === "control" || m === "cmd" || m === "command" || m === "meta") mod = true;
    else if (m === "alt" || m === "option" || m === "opt") c.alt = true;
    else if (m === "shift") c.shift = true;
    else return null;
  }
  return mod ? c : null;
}

export const chordText = (c: Chord) => ["Mod", c.alt && "Alt", c.shift && "Shift", c.key].filter(Boolean).join("+");

/** The one spelling of a chord ("ctrl+shift+p" → "Mod+Shift+P"); null when it is not one. */
export function canonChord(text: string): string | null {
  const c = parseChord(text);
  return c ? chordText(c) : null;
}

/** The chords a binding stands for: itself, or the nine of "Mod+1-9". */
export function expandChord(text: string): string[] {
  const c = parseChord(text);
  if (!c) return [];
  return c.key === DIGITS ? Array.from({ length: 9 }, (_, i) => chordText({ ...c, key: String(i + 1) })) : [chordText(c)];
}

const ARROWS: Record<string, string> = { Up: "↑", Down: "↓", Left: "←", Right: "→" };

/** A chord as the keycaps it is drawn with: ["Ctrl", "Alt", "B"] on Windows / Linux, ["⌥", "⌘", "B"] on a Mac. */
export function chordParts(text: string, platform: Platform = currentPlatform()): string[] {
  const c = parseChord(text);
  if (!c) return [];
  const key = c.key === DIGITS ? "1–9" : (ARROWS[c.key] ?? c.key);
  return platform === "mac" ? [c.alt && "⌥", c.shift && "⇧", "⌘", key].filter((s): s is string => Boolean(s)) : ["Ctrl", c.alt && "Alt", c.shift && "Shift", key].filter((s): s is string => Boolean(s));
}

/** One line of text for a chord: "⌥⌘B" or "Ctrl+Alt+B". */
export function formatChord(text: string, platform: Platform = currentPlatform()): string {
  return chordParts(text, platform).join(platform === "mac" ? "" : "+");
}

const ARIA_KEYS: Record<string, string> = { Up: "ArrowUp", Down: "ArrowDown", Left: "ArrowLeft", Right: "ArrowRight", Space: "Space" };

/** A chord as `aria-keyshortcuts` writes it ("Meta+Alt+P" on a Mac, "Control+Alt+P" elsewhere); "" for none or 1–9. */
export function ariaKeyshortcuts(text: string, platform: Platform = currentPlatform()): string {
  const c = parseChord(text);
  if (!c || c.key === DIGITS) return "";
  return [platform === "mac" ? "Meta" : "Control", c.alt && "Alt", c.shift && "Shift", ARIA_KEYS[c.key] ?? c.key].filter(Boolean).join("+");
}

// ── keyboard events → chords ─────────────────────────────

/** The parts of a KeyboardEvent the shortcuts read (so they are tested with plain objects). */
export interface KeyEventLike {
  type?: string;
  key: string;
  code?: string;
  keyCode?: number;
  isComposing?: boolean;
  repeat?: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  getModifierState?: (key: string) => boolean;
}

// event.key spellings of the keys chords use (when there is no code to go by)
const KEY_NAMES: Record<string, string> = { ArrowUp: "Up", ArrowDown: "Down", ArrowLeft: "Left", ArrowRight: "Right", " ": "Space" };

const CODE_KEYS: Record<string, string> = {
  Comma: ",", Period: ".", Slash: "/", Semicolon: ";", Quote: "'", BracketLeft: "[", BracketRight: "]", Backslash: "\\", Minus: "-", Equal: "=", Backquote: "`",
  ArrowUp: "Up", ArrowDown: "Down", ArrowLeft: "Left", ArrowRight: "Right", Enter: "Enter", NumpadEnter: "Enter", Space: "Space",
  Home: "Home", End: "End", PageUp: "PageUp", PageDown: "PageDown",
};

/** The key an event is for, in the chord's spelling; null for a modifier on its own or a key chords do not use (Esc, Tab…). */
export function eventKey(e: Pick<KeyEventLike, "key" | "code">): string | null {
  const code = e.code ?? "";
  // digits and punctuation by position: Shift (or a layout) changes what they type, not which key they are
  const digit = code.match(/^(?:Digit|Numpad)([0-9])$/);
  if (digit) return digit[1]!;
  // a letter by what it types (a Dvorak or AZERTY keyboard follows its labels), or by its key when Option / Alt composed
  // it into another character (⌥B types ∫)
  if (e.key.length === 1 && /[a-z]/i.test(e.key)) return e.key.toUpperCase();
  const letter = code.match(/^Key([A-Z])$/);
  if (letter) return letter[1]!;
  if (CODE_KEYS[code]) return CODE_KEYS[code]!;
  return normalKey(KEY_NAMES[e.key] ?? e.key);
}

/**
 * The chord an event presses, with whatever modifiers it holds: `mod` says whether Mod (and nothing that stands in for
 * it: Ctrl on a Mac is the terminal's, the Windows key is the system's) is down. null while only modifiers are down.
 */
export function eventChord(e: KeyEventLike, platform: Platform = currentPlatform()): (Chord & { mod: boolean }) | null {
  const key = eventKey(e);
  if (!key) return null;
  // AltGr (Ctrl+Alt on some Windows layouts) types characters: it is not Mod+Alt
  if (e.getModifierState?.("AltGraph")) return { alt: e.altKey, shift: e.shiftKey, key, mod: false };
  const mod = platform === "mac" ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  return { alt: e.altKey, shift: e.shiftKey, key, mod };
}

/** The canonical chord of a keydown that has Mod; null for anything else (a bare key is never a shortcut). */
export function chordOfEvent(e: KeyEventLike, platform: Platform = currentPlatform()): string | null {
  const c = eventChord(e, platform);
  return c?.mod ? chordText(c) : null;
}

// ── the reserved chords ──────────────────────────────────

export interface Reserved {
  chord: string;
  /** Why it is not for rebinding, shown in the settings. */
  why: string;
  /** Only on this platform. */
  only?: Platform;
}

const BROWSER = t("lib-shortcuts.reserved_browser");
const EDITING = t("lib-shortcuts.reserved_editing");
const TERMINAL = t("lib-shortcuts.reserved_terminal");
const SYSTEM = t("lib-shortcuts.reserved_system");
const FIND = t("lib-shortcuts.reserved_find");

const mod = (keys: string[], why: string, only?: Platform): Reserved[] => keys.map((k) => ({ chord: `Mod+${k}`, why, only }));

/**
 * Chords that cannot be bound, and that no default uses: the ones the browser keeps for itself (a page cannot take them,
 * or should not), the editing keys of any text field, the dialogs' Mod+Enter, and the control keys of a terminal on
 * Windows / Linux, where Mod is the terminal's Ctrl.
 */
export const RESERVED: readonly Reserved[] = [
  ...mod(["T", "W", "N", "R", "L", "D", "P", "S", "O", "U", "F", "G", "H", "0", "=", "-", "[", "]", "Shift+T", "Shift+N", "Shift+W", "Shift+R", "Shift+G", "Shift+I", "Shift+J", "Shift+C", "Shift+B"], BROWSER),
  ...mod(["Q", "M", "Alt+I"], BROWSER, "mac"),
  ...mod(["A", "C", "V", "X", "Z", "Y", "Shift+Z", "Shift+V", "Up", "Down", "Left", "Right", "Home", "End", "Shift+Left", "Shift+Right", "Shift+Up", "Shift+Down"], EDITING),
  ...mod(["Enter"], t("lib-shortcuts.reserved_dialog_submit")),
  ...mod(["Space"], SYSTEM),
  ...mod(["W", "R", "L", "U", "D", "Z"], TERMINAL, "other"),
  // the terminal's find bar takes Ctrl+Shift+F off the Mac (⌘F on a Mac is reserved above, with the browser's)
  ...mod(["Shift+F"], FIND, "other"),
];

/** Why a chord cannot be bound on this platform; null when it can. */
export function reservedWhy(chord: string, platform: Platform = currentPlatform()): string | null {
  const keys = new Set(expandChord(chord));
  return RESERVED.find((r) => (!r.only || r.only === platform) && keys.has(r.chord))?.why ?? null;
}

// ── the commands ─────────────────────────────────────────

/** Where a command works: everywhere, on a conversation / terminal page, or on the sidebar. */
export type ShortcutScope = "global" | "session" | "sidebar";

export const SCOPE_LABEL: Record<ShortcutScope, string> = { global: t("lib-shortcuts.scope_global"), session: t("lib-shortcuts.scope_session"), sidebar: t("lib-shortcuts.scope_sidebar") };

export interface ShortcutDef {
  id: string;
  /** What the settings, the help overlay and the command palette call it. */
  label: string;
  /** The default chord (platform-independent, see Chord). */
  chord: string;
  scope: ShortcutScope;
  /** Still works with a dialog or the palette open (they are the ones that open those). */
  overlay?: boolean;
  /** Fires again while held (walking the sidebar's rows). */
  repeat?: boolean;
  icon?: IconName;
}

/**
 * Moshi's keymap, each mapped to the closest Persona action (docs/web/web.md "Keyboard shortcuts"):
 *   Mod+K go to · Mod+Shift+P command palette · Mod+J terminal ⇄ chat · Mod+B right panel · Mod+Alt+B sidebar · Mod+E sidebar groups ·
 *   Mod+Shift+Enter maximize the center area · Mod+I input · Mod+Alt+Shift+↑/↓ collapse / expand · Mod+Alt+↑/↓ previous / next row · Mod+, settings ·
 *   Mod+1–9 computers · Mod+Alt+←/→ previous / next tab on this machine. Persona's own: Mod+Alt+P opens Persona.
 * Not taken over: Moshi's Mod+Alt+1–3 (choose the right panel's tab), which Persona has no single place for.
 */
export const SHORTCUTS: readonly ShortcutDef[] = [
  { id: "jump", label: t("lib-shortcuts.cmd_jump"), chord: "Mod+K", scope: "global", overlay: true, icon: "search" },
  { id: "commands", label: t("lib-shortcuts.cmd_commands"), chord: "Mod+Shift+P", scope: "global", overlay: true, icon: "run" },
  { id: "help", label: t("lib-shortcuts.cmd_help"), chord: "Mod+/", scope: "global", overlay: true, icon: "keyboard" },
  { id: "settings", label: t("lib-shortcuts.cmd_settings"), chord: "Mod+,", scope: "global", icon: "gear" },
  // the sidebar's first row, Persona's main conversation (#5581); not in Moshi's keymap. Mod+Alt+P: P for Persona, free in
  // Chrome / Safari / Firefox and not a terminal's control key (Mod+Shift+P is the palette, Mod+P the browser's print)
  { id: "persona", label: t("lib-shortcuts.cmd_persona"), chord: "Mod+Alt+P", scope: "global", icon: "chat" },
  { id: "machine", label: t("lib-shortcuts.cmd_machine"), chord: `Mod+${DIGITS}`, scope: "global", icon: "machine" },
  { id: "left-sidebar", label: t("lib-shortcuts.cmd_left_sidebar"), chord: "Mod+Alt+B", scope: "global", icon: "sidebar" },
  { id: "maximize", label: t("lib-shortcuts.cmd_maximize"), chord: "Mod+Shift+Enter", scope: "global", icon: "layers" },
  { id: "terminal-chat", label: t("lib-shortcuts.cmd_terminal_chat"), chord: "Mod+J", scope: "session", icon: "terminal" },
  { id: "terminal-mode", label: t("terminal-full.cmd_terminal_mode"), chord: "Mod+Alt+F", scope: "session", icon: "sidebar" },
  { id: "right-panel", label: t("lib-shortcuts.cmd_right_panel"), chord: "Mod+B", scope: "session", icon: "sidebar" },
  { id: "focus-input", label: t("lib-shortcuts.cmd_focus_input"), chord: "Mod+I", scope: "session", icon: "keyboard" },
  // Persona's own: the first waiting card answers from the keyboard while it has focus (lib/card-keys.ts). Mod+Alt+Enter
  // is free — Mod+Enter is the dialogs' (reserved above), Mod+Shift+Enter maximizes, and ⌘⌥⏎ / Ctrl+Alt+Enter is no
  // browser or system key
  { id: "focus-card", label: t("lib-shortcuts.cmd_focus_card"), chord: "Mod+Alt+Enter", scope: "session", icon: "enter" },
  { id: "prev-tab", label: t("lib-shortcuts.cmd_prev_tab"), chord: "Mod+Alt+Left", scope: "session", icon: "arrowLeft" },
  { id: "next-tab", label: t("lib-shortcuts.cmd_next_tab"), chord: "Mod+Alt+Right", scope: "session", icon: "arrowRight" },
  { id: "sidebar-grouping", label: t("lib-shortcuts.cmd_sidebar_grouping"), chord: "Mod+E", scope: "sidebar", icon: "filter" },
  { id: "prev-row", label: t("lib-shortcuts.cmd_prev_row"), chord: "Mod+Alt+Up", scope: "sidebar", repeat: true, icon: "up" },
  { id: "next-row", label: t("lib-shortcuts.cmd_next_row"), chord: "Mod+Alt+Down", scope: "sidebar", repeat: true, icon: "arrowDown" },
  { id: "collapse-tree", label: t("lib-shortcuts.cmd_collapse_tree"), chord: "Mod+Alt+Shift+Up", scope: "sidebar", icon: "chevron" },
  { id: "expand-tree", label: t("lib-shortcuts.cmd_expand_tree"), chord: "Mod+Alt+Shift+Down", scope: "sidebar", icon: "chevron" },
];

export const shortcutDef = (id: string) => SHORTCUTS.find((s) => s.id === id);

/**
 * Keys a component handles itself, where it is the one that knows if they apply (the terminal's find bar defers to the
 * browser's own find when nothing is being driven). They are not in the registry, so they are not rebound and the
 * dispatcher never sees them; they are reserved (so no command is bound to them) and listed in the help and the settings.
 */
export interface LocalShortcut {
  id: string;
  label: string;
  /** The chord on each platform. */
  chord: Record<Platform, string>;
  /** What else to know, one line. */
  note?: string;
}

export const LOCAL_SHORTCUTS: readonly LocalShortcut[] = [
  { id: "terminal-find", label: t("lib-shortcuts.local_terminal_find"), chord: { mac: "Mod+F", other: "Mod+Shift+F" }, note: t("lib-shortcuts.local_terminal_find_note") },
];

// ── the user's bindings ──────────────────────────────────

/** Only what differs from the defaults: command id → chord, "" for a command whose chord was cleared (no key, no default). */
export type Overrides = Record<string, string>;
/** Command id → chord; "" when it has none (cleared, or, for the active bindings, turned off). */
export type Bindings = Record<string, string>;

/** What the user set: the chords, the commands turned off one by one (they keep their chord), and the switch for all. */
export interface ShortcutPrefs {
  overrides: Overrides;
  disabled: string[];
  /** Every shortcut is off. */
  off: boolean;
}

export const EMPTY_PREFS: ShortcutPrefs = { overrides: {}, disabled: [], off: false };

export const STORAGE_KEY = "persona.shortcuts";

/** The chord of every command as set: the default unless the user rebound or cleared it. */
export function bindingsOf(overrides: Overrides): Bindings {
  const out: Bindings = {};
  for (const s of SHORTCUTS) out[s.id] = overrides[s.id] ?? s.chord;
  return out;
}

/** The chords that fire: as set, minus the commands turned off and everything while the switch for all is off. */
export function activeBindings(prefs: ShortcutPrefs): Bindings {
  const out = bindingsOf(prefs.overrides);
  for (const id of Object.keys(out)) if (prefs.off || prefs.disabled.includes(id)) out[id] = "";
  return out;
}

/** The command that has `chord` (or one of the digits it stands for), other than `id`; chords set count, on or off. */
export function conflictOf(id: string, chord: string, bindings: Bindings): ShortcutDef | undefined {
  const mine = new Set(expandChord(chord));
  return SHORTCUTS.find((other) => other.id !== id && expandChord(bindings[other.id] ?? other.chord).some((c) => mine.has(c)));
}

/** On a conflict, `with` is the command that has the chord and `chord` the chord pressed (the settings offer "Replace"). */
export type Check = { ok: true; chord: string } | { ok: false; reason: "mod" | "key" | "reserved" | "conflict" | "digits"; message: string; with?: string; chord?: string };

/**
 * Whether `pressed` can be the chord of command `id`. `pressed` is what the user pressed (a Chord with `mod`); the
 * answer is the canonical chord, or why not: no Mod ("needs Ctrl"), a key chords do not use, a reserved chord, or
 * another command's ("already used by "X""). `bindings` are the chords as set (getConfigured): a command turned off keeps its chord.
 */
export function checkBinding(id: string, pressed: (Chord & { mod: boolean }) | null, bindings: Bindings, platform: Platform = currentPlatform()): Check {
  if (!pressed) return { ok: false, reason: "key", message: t("lib-shortcuts.check_key") };
  if (!pressed.mod) return { ok: false, reason: "mod", message: t("lib-shortcuts.check_mod", { mod: modName(platform) }) };
  const def = shortcutDef(id);
  let chord = chordText(pressed);
  if (def && parseChord(def.chord)?.key === DIGITS) {
    if (!/^[1-9]$/.test(pressed.key)) return { ok: false, reason: "digits", message: t("lib-shortcuts.check_digits") };
    chord = chordText({ ...pressed, key: DIGITS });
  }
  const why = reservedWhy(chord, platform);
  if (why) return { ok: false, reason: "reserved", message: t("lib-shortcuts.check_reserved", { chord: formatChord(chord, platform), why }) };
  const other = conflictOf(id, chord, bindings);
  if (other) return { ok: false, reason: "conflict", message: t("lib-shortcuts.check_conflict", { label: other.label }), with: other.id, chord };
  return { ok: true, chord };
}

/** What localStorage held, kept to what still makes sense: known commands, valid chords that are not reserved, or "". */
export function sanitizeOverrides(raw: unknown, platform: Platform = currentPlatform()): Overrides {
  const out: Overrides = {};
  if (!raw || typeof raw !== "object") return out;
  for (const [id, v] of Object.entries(raw as Record<string, unknown>)) {
    const def = shortcutDef(id);
    if (!def || typeof v !== "string") continue;
    if (v === "") {
      out[id] = "";
      continue;
    }
    const chord = canonChord(v);
    if (!chord || chord === canonChord(def.chord) || reservedWhy(chord, platform)) continue;
    out[id] = chord;
  }
  // two commands on one chord: a chord the user chose wins over a default (a command added or a default moved since
  // takes none, #5581); between two chosen ones the first keeps it and the other goes back to its default (or, when that
  // is taken too, to none)
  const seen = new Set<string>();
  const taken = (chords: string[]) => chords.some((c) => seen.has(c));
  for (const s of SHORTCUTS) {
    if (!out[s.id]) continue;
    const chords = expandChord(out[s.id]);
    if (taken(chords)) delete out[s.id];
    else for (const c of chords) seen.add(c);
  }
  for (const s of SHORTCUTS) {
    if (s.id in out) continue;
    const chords = expandChord(s.chord);
    if (taken(chords)) out[s.id] = "";
    else for (const c of chords) seen.add(c);
  }
  return out;
}

/**
 * What localStorage held, as prefs. The first version stored the overrides on their own ({"jump": "Mod+Shift+O"}); it is
 * read as such.
 */
export function sanitizePrefs(raw: unknown, platform: Platform = currentPlatform()): ShortcutPrefs {
  if (!raw || typeof raw !== "object") return { ...EMPTY_PREFS };
  const r = raw as Record<string, unknown>;
  if (!("overrides" in r || "disabled" in r || "off" in r)) return { ...EMPTY_PREFS, overrides: sanitizeOverrides(r, platform) };
  const disabled = Array.isArray(r.disabled) ? r.disabled.filter((id): id is string => typeof id === "string" && Boolean(shortcutDef(id))) : [];
  return { overrides: sanitizeOverrides(r.overrides, platform), disabled: [...new Set(disabled)], off: r.off === true };
}

function load(): ShortcutPrefs {
  try {
    return sanitizePrefs(JSON.parse(localStorage.getItem(STORAGE_KEY) ?? "null"));
  } catch {
    return { ...EMPTY_PREFS };
  }
}

const isEmpty = (p: ShortcutPrefs) => !Object.keys(p.overrides).length && !p.disabled.length && !p.off;

let prefs: ShortcutPrefs = typeof localStorage === "undefined" ? { ...EMPTY_PREFS } : load();
let configured: Bindings = bindingsOf(prefs.overrides);
let snapshot: Bindings = activeBindings(prefs);
const subscribers = new Set<() => void>();

function commit(next: ShortcutPrefs, save = true) {
  prefs = next;
  configured = bindingsOf(next.overrides);
  snapshot = activeBindings(next);
  if (save) {
    try {
      if (isEmpty(next)) localStorage.removeItem(STORAGE_KEY);
      else localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
    } catch {
      // storage full or blocked: the bindings last until the page is closed
    }
  }
  subscribers.forEach((f) => f());
}

/** The chords that fire now (cleared and turned-off commands have ""). */
export const getBindings = (): Bindings => snapshot;
/** The chords as set, whether on or off: what the settings show and check conflicts against. */
export const getConfigured = (): Bindings => configured;
export const getShortcutPrefs = (): ShortcutPrefs => prefs;

/** Binds a command to a chord (the caller has checked it), or clears it with ""; the default chord drops the override. */
export function setBinding(id: string, chord: string) {
  const def = shortcutDef(id);
  const canon = chord === "" ? "" : canonChord(chord);
  if (!def || canon === null) return;
  const overrides = { ...prefs.overrides };
  if (canon === canonChord(def.chord)) delete overrides[id];
  else overrides[id] = canon;
  commit({ ...prefs, overrides });
}

/** Clears a command's chord: its key does nothing, and it does not fall back to its default. */
export const clearBinding = (id: string) => setBinding(id, "");

/** Binds `id` to `chord` and clears the command that had it (the settings' "Replace"). */
export function replaceBinding(id: string, chord: string, other: string) {
  const def = shortcutDef(id);
  const canon = canonChord(chord);
  if (!def || !canon || !shortcutDef(other) || other === id) return;
  const overrides = { ...prefs.overrides, [other]: "" };
  if (canon === canonChord(def.chord)) delete overrides[id];
  else overrides[id] = canon;
  commit({ ...prefs, overrides });
}

/** Turns one command on or off; off keeps its chord. */
export function setShortcutEnabled(id: string, on: boolean) {
  if (!shortcutDef(id) || on === !prefs.disabled.includes(id)) return;
  commit({ ...prefs, disabled: on ? prefs.disabled.filter((d) => d !== id) : [...prefs.disabled, id] });
}

/** The switch for all: off, no shortcut fires (each one's own setting is kept for when it is back on). */
export function setShortcutsOn(on: boolean) {
  if (on === !prefs.off) return;
  commit({ ...prefs, off: !on });
}

/** Whether a command is as it came: its default chord, and on. */
export const isDefault = (id: string, p: ShortcutPrefs = prefs) => !(id in p.overrides) && !p.disabled.includes(id);

/** Back to the default chord, and on. The caller checks the default is free (conflictOf), or the other one is cleared. */
export function resetBinding(id: string) {
  if (isDefault(id)) return;
  const overrides = { ...prefs.overrides };
  delete overrides[id];
  commit({ ...prefs, overrides, disabled: prefs.disabled.filter((d) => d !== id) });
}

/** Every command back to its default chord and on, and the switch for all on. */
export function resetAllBindings() {
  if (!isEmpty(prefs)) commit({ ...EMPTY_PREFS });
}

function subscribe(f: () => void) {
  subscribers.add(f);
  return () => void subscribers.delete(f);
}

// another tab rebinding
if (typeof addEventListener !== "undefined") {
  addEventListener("storage", (e) => {
    if (e.key === STORAGE_KEY || e.key === null) commit(load(), false);
  });
}

/** The chords that fire, live. */
export function useBindings(): Bindings {
  return useSyncExternalStore(subscribe, getBindings, getBindings);
}

/** Everything the user set, live (the settings page). */
export function useShortcutPrefs(): ShortcutPrefs {
  return useSyncExternalStore(subscribe, getShortcutPrefs, getShortcutPrefs);
}

/** The hint on the sidebar's search box: the ⌘K it has always shown while "Go to" has its default chord, else the chord it has now. */
export function jumpHint(bindings: Bindings, platform: Platform = currentPlatform()): string {
  const def = shortcutDef("jump")!;
  if (!bindings.jump) return "";
  return bindings.jump === def.chord ? "⌘K" : formatChord(bindings.jump, platform);
}

// ── handlers and the dispatcher ──────────────────────────

/** Where the key was pressed: in a terminal (xterm's helper textarea), in a text field, or on the page. */
export type FocusKind = "terminal" | "input" | "page";

/** Elements that type; whoever reads them (the dispatcher's focusKind, a card's own keys) needs the same list. */
export const EDITABLE = "input, textarea, select, [contenteditable=''], [contenteditable='true']";

interface ElementLike {
  closest?: (selector: string) => unknown;
  tagName?: string;
  isContentEditable?: boolean;
}

export function focusKind(target: unknown): FocusKind {
  const el = target as ElementLike | null;
  if (!el || typeof el.closest !== "function") return "page";
  if (el.closest(".xterm")) return "terminal";
  if (el.closest(EDITABLE) || el.isContentEditable) return "input";
  return "page";
}

export interface ShortcutCall {
  /** Where the key was pressed. */
  focus: FocusKind;
  /** The digit of the chord, for the command that takes 1–9. */
  digit?: number;
  /** Set when the key started it; undefined when the command palette runs it. */
  event?: KeyEventLike;
}

/** Returns false when the command does not apply here (no conversation on the page…): the key is then left alone. */
export type ShortcutHandler = (call: ShortcutCall) => boolean | void;

const handlers = new Map<string, ShortcutHandler>();

/** Registers what a command does (the shell does, while it is mounted); returns the way to take it back. */
export function registerShortcut(id: string, run: ShortcutHandler): () => void {
  handlers.set(id, run);
  return () => {
    if (handlers.get(id) === run) handlers.delete(id);
  };
}

export const handlerOf = (id: string): ShortcutHandler | undefined => handlers.get(id);

/** Runs a command by its id (the command palette): false when it has no handler here or the handler says it does not apply. */
export function runShortcut(id: string, call: Partial<ShortcutCall> = {}): boolean {
  const run = handlers.get(id);
  if (!run) return false;
  return run({ focus: "page", ...call }) !== false;
}

/** Keys being recorded in the settings: the dispatcher stands aside. */
let recording = 0;
export function holdShortcuts(): () => void {
  recording++;
  let released = false;
  return () => {
    if (!released) recording--;
    released = true;
  };
}

/** The command a key press is for: the one bound to its chord, and the digit for 1–9. */
export function matchShortcut(e: KeyEventLike, bindings: Bindings = snapshot, platform: Platform = currentPlatform()): { def: ShortcutDef; digit?: number } | undefined {
  if (e.isComposing || e.keyCode === 229) return undefined;
  const chord = chordOfEvent(e, platform);
  if (!chord) return undefined;
  for (const def of SHORTCUTS) {
    const bound = bindings[def.id] ?? def.chord;
    if (!expandChord(bound).includes(chord)) continue;
    const digit = parseChord(bound)?.key === DIGITS ? Number(chord.split("+").pop()) : undefined;
    return { def, digit };
  }
  return undefined;
}

/**
 * The terminal's key handler asks this first: true when the key is one of the app's shortcuts (the app takes it, xterm
 * must not send it to the program). One call is all the terminal needs: `if (passesToApp(e)) return false`.
 */
export function passesToApp(e: KeyEventLike, bindings: Bindings = snapshot, platform: Platform = currentPlatform()): boolean {
  if (recording > 0) return false;
  const hit = matchShortcut(e, bindings, platform);
  return Boolean(hit && handlers.has(hit.def.id));
}

export interface Dispatchable extends KeyEventLike {
  target?: unknown;
  defaultPrevented?: boolean;
  preventDefault(): void;
  stopPropagation(): void;
}

export interface DispatchEnv {
  platform?: Platform;
  bindings?: Bindings;
  /** A dialog or the palette is open: only the commands that work over them run. */
  overlay?: boolean;
}

/** Runs the command a keydown is for. true: it did, and the event is consumed (no browser action, no one else sees it). */
export function dispatchKey(e: Dispatchable, env: DispatchEnv = {}): boolean {
  if (recording > 0 || e.defaultPrevented) return false;
  // herdr's own key in its full surface (Ctrl+B, its prefix, where Mod is Ctrl) is not a shortcut there (lib/terminal-conflicts.ts)
  if (herdrOwnsKey(e) && inFullSurface(e.target)) return false;
  const hit = matchShortcut(e, env.bindings, env.platform);
  if (!hit) return false;
  if (env.overlay && !hit.def.overlay) return false;
  const run = handlers.get(hit.def.id);
  if (!run) return false;
  // held down: one toggle, not a flicker (the browser's own action for the chord is still held off)
  if (e.repeat && !hit.def.repeat) {
    e.preventDefault();
    e.stopPropagation();
    return true;
  }
  if (run({ focus: focusKind(e.target), digit: hit.digit, event: e }) === false) return false;
  e.preventDefault();
  e.stopPropagation();
  return true;
}
