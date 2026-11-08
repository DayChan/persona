// The question navigation rail's model (#1739, docs/web/web.md "Question rail"): which turns of a chat become ticks,
// the thresholds that gate the rail, and the trimming of its preview cards. Pure functions over the chat's lines, so
// they run in node tests (nav-rail.test.ts); the NavRail component measures the DOM and draws.

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

/** Fewer questions than this and the rail stays away: a short chat is one or two screens, a map of it adds nothing. */
export const NAV_RAIL_MIN_TICKS = 4;
/** The chat column's width (ChatView's `max-w-[880px]`): the rail lives in the free margin beside it. */
export const NAV_RAIL_COLUMN = 880;
/** Free margin at one side below which the rail stays away (a tick's reach plus its clearance to the column). */
export const NAV_RAIL_MIN_GUTTER = 44;
/** Vertical room one tick takes (a 2px line in a 9px hit area); 9px stays legible tick by tick. */
export const NAV_RAIL_TICK_PITCH = 9;
/** What the pitch may compress to when the rail is short of room; closer ticks merge into noise. */
export const NAV_RAIL_TICK_MIN_PITCH = 5;
/** The rail needs room for its opening count of ticks before it shows at all. */
export const NAV_RAIL_MIN_HEIGHT = NAV_RAIL_MIN_TICKS * NAV_RAIL_TICK_PITCH;
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

/** Whether the chat area leaves the rail room: the centered column (NAV_RAIL_COLUMN) with at least NAV_RAIL_MIN_GUTTER
 *  free on the side the rail sits in. `width` is the content width (clientWidth — a classic scrollbar is not margin).
 *  Panels and narrow windows take the margin away and the rail goes with it. */
export function hasNavRailRoom(width: number): boolean {
  return width > NAV_RAIL_COLUMN && (width - NAV_RAIL_COLUMN) / 2 >= NAV_RAIL_MIN_GUTTER;
}

export interface NavRailPlan {
  /** The first tick drawn (earlier ones are dropped; a placeholder stands for them). */
  start: number;
  /** The pitch in use. */
  pitch: number;
  /** How many earlier ticks were dropped. */
  hidden: number;
}

/** Fitting the ticks in the rail's height: at the normal pitch while they fit, compressed while that stays legible,
 *  and only the newest run otherwise — one slot kept for the "earlier questions" placeholder. */
export function planNavRailTicks(count: number, height: number): NavRailPlan {
  if (count <= 0 || height <= 0) return { start: 0, pitch: NAV_RAIL_TICK_PITCH, hidden: 0 };
  if (count * NAV_RAIL_TICK_PITCH <= height) return { start: 0, pitch: NAV_RAIL_TICK_PITCH, hidden: 0 };
  const pitch = Math.floor(height / count);
  if (pitch >= NAV_RAIL_TICK_MIN_PITCH) return { start: 0, pitch, hidden: 0 };
  const slots = Math.max(2, Math.floor(height / NAV_RAIL_TICK_MIN_PITCH));
  const shown = Math.min(count, slots - 1);
  return { start: count - shown, pitch: NAV_RAIL_TICK_MIN_PITCH, hidden: count - shown };
}

/** The tick being read: the last question whose line has started above the threshold (a little under the scroller's
 *  top, so a jump that just landed keeps its own tick). `topAt(i)` returning null means the line is not on the page —
 *  it is above the view, which counts as read past. */
export function pickActiveTick(count: number, thresholdTop: number, topAt: (i: number) => number | null): number {
  for (let i = count - 1; i >= 0; i--) {
    if ((topAt(i) ?? Number.NEGATIVE_INFINITY) <= thresholdTop) return i;
  }
  return 0;
}
