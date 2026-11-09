// Genuine loaded user inputs for the edge navigator. The data-source adapter can replace this projection with
// the independent index without coupling the rail's gestures to transcript loading.

import { NAV_RAIL_METRICS } from "../ui/nav-rail-metrics";
import { isLoopTick, isOwnMessage, type ChatLine, type UserMessage } from "./turns";

/** One of your questions in the loaded conversation: what a tick knows. */
export interface NavRailTick {
  /** The line's id (a `seg:…` ChatLine): what a jump looks for in the scroller (`[data-line=…]`). */
  id: string;
  /** The question's first line worth showing (quote blocks skipped, trimmed, capped): the preview card and the label. */
  preview: string;
  /** A question with no words of its own (only pictures, files or picked contexts): how many, for a fallback label. */
  attachmentsOnly?: number;
  /** The start of this turn's answer (Markdown noise stripped, one line, capped); absent while it has none yet. */
  answerExcerpt?: string;
}

/** Even a single loaded input is a useful position and selection target. */
export const NAV_RAIL_MIN_TICKS = 1;
export const NAV_RAIL_TICK_PITCH = NAV_RAIL_METRICS.tickPitch;
export const NAV_RAIL_TICK_MIN_PITCH = NAV_RAIL_METRICS.minTickPitch;
export const NAV_RAIL_MIN_HEIGHT = NAV_RAIL_METRICS.minHeight;
/** The answer excerpt's cap: the preview card clamps to three lines anyway; this keeps the string itself bounded. */
export const NAV_RAIL_EXCERPT_MAX = 200;
/** The question line's cap for the label; the card truncates to one line, the label carries the whole preview. */
export const NAV_RAIL_PREVIEW_MAX = 120;

/** The questions of the loaded lines, oldest first: one tick per turn you wrote yourself. Persona's own messages,
 *  other sessions' and other conversations' (`isOwnMessage`), /loop wake-ups (`isLoopTick`) and lines that are not
 *  turns (notices, compactions, recaps, errors) never become ticks; a question with neither words nor attachments has
 *  nothing to show or jump to and is skipped. Only what is loaded counts: the rail never pages history in. */
export function navRailTicks(lines: ChatLine[]): NavRailTick[] {
  const out: NavRailTick[] = [];
  for (const line of lines) {
    if (line.kind !== "segment") continue;
    const msg = line.seg.user;
    if (!msg || !isOwnMessage(msg) || isLoopTick(msg)) continue;
    const preview = navRailPreview(wordsOf(msg));
    const attachments = msg.images.length + (msg.files?.length ?? 0) + (msg.photos?.length ?? 0) + msg.contexts.length;
    if (!preview && !attachments) continue;
    const answer = navRailExcerpt(line.seg.answer?.text ?? "");
    out.push({ id: line.id, preview, ...(preview ? {} : { attachmentsOnly: attachments }), ...(answer ? { answerExcerpt: answer } : {}) });
  }
  return out;
}

/** A merged bubble (several queued messages in one turn) previews its parts in order; the first line of the first
 *  part that has one is the question's face. */
function wordsOf(msg: UserMessage): string {
  return msg.parts ? msg.parts.join("\n") : msg.text;
}

/** The question's first own line: quote blocks (`> `, drawn as cards by Quote.tsx) are skipped for your words above
 *  them, and a message that is all quote falls back to the quote's first line. */
export function navRailPreview(text: string): string {
  const lines = text.split("\n");
  const own = lines.find((l) => l.trim() && !l.trimStart().startsWith(">"));
  const any = lines.find((l) => l.trim()) ?? "";
  const line = (own ?? any).replace(/^\s*>\s?/, "").trim();
  return line.length > NAV_RAIL_PREVIEW_MAX ? `${line.slice(0, NAV_RAIL_PREVIEW_MAX - 1)}…` : line;
}

/** The answer's start as one plain line: Markdown marks a preview does not render are stripped (links keep their
 *  text, headings and quotes their prefix, bold and code their marks), whitespace folds, and the result is capped. */
export function navRailExcerpt(raw: string): string {
  return raw
    .replace(/<!--[\s\S]*?-->/g, "")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/^#{1,6}\s+/gm, "")
    .replace(/^>\s?/gm, "")
    .replace(/^[-+*]\s+/gm, "")
    .replace(/[*`]/g, "")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, NAV_RAIL_EXCERPT_MAX);
}

export interface NavRailPlan {
  start: number;
  pitch: number;
  hidden: number;
  count: number;
}

/** Bound the DOM, not the navigable history: keep a window around the selected input. Keyboard navigation and
 *  the rail's wheel can move this window in either direction; older inputs are never permanently discarded. */
export function planNavRailTicks(count: number, height: number, focus = count - 1): NavRailPlan {
  if (count <= 0 || height <= 0) return { start: 0, pitch: NAV_RAIL_TICK_PITCH, hidden: 0, count: 0 };
  const capacity = Math.max(1, Math.floor(height / NAV_RAIL_TICK_MIN_PITCH));
  const shown = Math.min(count, capacity);
  const start = Math.max(0, Math.min(Math.floor(focus) - Math.floor(shown / 2), count - shown));
  return { start, pitch: Math.min(NAV_RAIL_TICK_PITCH, height / shown), hidden: count - shown, count: shown };
}

/** The input owning the viewport. Missing anchors are unknown, not evidence that an input has been read past. */
export function pickActiveTick(count: number, thresholdTop: number, topAt: (i: number) => number | null): number | null {
  let first: number | null = null;
  let found: number | null = null;
  for (let i = 0; i < count; i++) {
    const top = topAt(i);
    if (top === null) continue;
    first ??= i;
    if (top <= thresholdTop) found = i;
  }
  return found ?? first;
}

/** Cached monotone offsets make normal scrolling logarithmic, without per-message DOM reads. */
export function activeNavRailOffset(offsets: { index: number; top: number }[], top: number): number | null {
  if (!offsets.length) return null;
  let lo = 0;
  let hi = offsets.length;
  while (lo < hi) {
    const mid = (lo + hi) >>> 1;
    if (offsets[mid].top <= top) lo = mid + 1;
    else hi = mid;
  }
  return offsets[Math.max(0, lo - 1)].index;
}

/** Explicit data-source seam: an index adapter may load missing content before resolving a selection. */
export interface NavRailSource {
  ticks: NavRailTick[];
  select: (id: string) => void | Promise<void>;
}
