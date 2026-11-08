// Comments on content (#2437, Moshi's "Comment"): the words picked in the transcript, or lines of a file or a diff,
// each with a note of the reader's. A comment is one more entry in the composer's quote list (lib/drafts.ts DraftQuote
// with `comment`), not a second mechanism: it waits as a card above the input and goes out in the same `> ` block in
// front of the message, so the agent gets the location, the code and the note in one message. The pure parts live
// here: the caps, line ranges, the block as written into the message and read back (an edit, a sent bubble).
import type { DraftQuote } from "./drafts";
import type { DiffLine } from "./files";
import { openPanel } from "./panel";

/** A comment's note is cut at this many characters. */
export const MAX_COMMENT_CHARS = 4000;
/** At most this many lines of code go with a code comment; the rest is counted, not sent. */
export const MAX_CODE_LINES = 60;

/** What a diff shows: the staged changes, the unstaged ones, or one commit; or what a review of the session's changes
 *  compares the files on disk with (lib/session-review.ts, #151): HEAD, where the branch left the default branch, a
 *  commit (`base`: its first 8 hex digits). */
export type DiffScope =
  | { kind: "staged" }
  | { kind: "unstaged" }
  | { kind: "commit"; sha: string }
  | { kind: "review"; scope: "uncommitted" }
  | { kind: "review"; scope: "branch" | "commit"; base: string };

/** Where picked words sit in the transcript: the message (its `data-items` id) and the offsets in its text. */
export interface TextAnchor {
  item: string;
  start: number;
  end: number;
  /** The words at those offsets; they are looked for again when the text moved. */
  exact: string;
  /** What stood just before them, to tell the same words apart when they are looked for again. */
  before?: string;
}

/** The lines a code comment is about, and how to open them again. */
export interface CodeSpot {
  /** The path the agent reads: relative to the session's folder when inside it, else as it is. */
  file: string;
  /** The path to open it by: the file viewer's path, or a diff's repository-relative one. */
  path: string;
  from: number;
  to: number;
  /** The numbers are the old file's (a diff of removed lines). */
  old?: boolean;
  diff?: DiffScope;
  /** The fence's language: a file's extension, `diff` for a diff's lines. */
  lang?: string;
  /** The machine and session the panel was opened for, to open it again. */
  mid?: string;
  sid?: string;
  cwd?: string;
  /** The lines carried (at most MAX_CODE_LINES) and how many more there were. */
  omitted: number;
}

/** Where an element comment points: the page, the element, and the preview to open again. */
export interface ElementSpot {
  url: string;
  selector: string;
  /** The component that drew it and where its code is, when the dev build said. */
  component?: string;
  source?: string;
  /** Its visible text, cut short. */
  text?: string;
  omitted: number;
  mid?: string;
  sid?: string;
  cwd?: string;
  port?: number;
}

export interface QuoteComment {
  /** The reader's note (at most MAX_COMMENT_CHARS). */
  body: string;
  anchor?: TextAnchor;
  spot?: CodeSpot;
  /** An element picked in a dev server preview (lib/preview.ts, #150); its HTML is the quote's text. */
  element?: ElementSpot;
}

/** Rows `a`..`b` (either order) of a parsed diff as a code comment's lines: the first and last line numbers (the new file's,
 *  the old file's when only removed lines are picked), and the rows with their `+` / `-` / space marks. Hunk headers and
 *  notes in between are left out; null when no code row is in the range. */
export function diffPick(rows: readonly DiffLine[], a: number, b: number): { from: number; to: number; old: boolean; lines: string[] } | null {
  const { from: lo, to: hi } = lineRange(a, b);
  const code = rows.slice(Math.max(0, lo), hi + 1).filter((r) => r.kind === "add" || r.kind === "del" || r.kind === "context");
  if (!code.length) return null;
  const old = code.every((r) => r.kind === "del");
  const num = (r: DiffLine) => (old ? r.old : (r.new ?? r.old)) ?? 0;
  const nums = code.map(num).filter((n) => n > 0);
  if (!nums.length) return null;
  const mark = (r: DiffLine) => (r.kind === "add" ? "+" : r.kind === "del" ? "-" : " ");
  return { from: Math.min(...nums), to: Math.max(...nums), old, lines: code.map((r) => mark(r) + r.text) };
}

/** The note cut to its cap, never in the middle of a surrogate pair. */
export function clampComment(s: string): string {
  if (s.length <= MAX_COMMENT_CHARS) return s;
  const cut = s.slice(0, MAX_COMMENT_CHARS);
  const last = cut.charCodeAt(cut.length - 1);
  return last >= 0xd800 && last <= 0xdbff ? cut.slice(0, -1) : cut;
}

/** Two clicked lines as a range, whichever came first. */
export function lineRange(a: number, b: number): { from: number; to: number } {
  return a <= b ? { from: a, to: b } : { from: b, to: a };
}

/** `12` or `12-14`. */
export function rangeLabel(from: number, to: number): string {
  return from === to ? String(from) : `${from}-${to}`;
}

/** The first MAX_CODE_LINES lines and how many were left out. */
export function truncateLines(lines: readonly string[], max = MAX_CODE_LINES): { lines: string[]; omitted: number } {
  if (lines.length <= max) return { lines: [...lines], omitted: 0 };
  return { lines: lines.slice(0, max), omitted: lines.length - max };
}

/** `staged`, `unstaged` or `commit <first 8 of the hash>`; a review's `uncommitted`, `branch since <hash>`, `since <hash>`. */
export function diffLabel(d: DiffScope): string {
  if (d.kind === "review") return d.scope === "uncommitted" ? "uncommitted" : `${d.scope === "branch" ? "branch since" : "since"} ${d.base}`;
  return d.kind === "commit" ? `commit ${d.sha.slice(0, 8)}` : d.kind;
}

/** A `Diff:` line's value read back (diffLabel's). */
export function parseDiffLabel(v: string): DiffScope | null {
  const d = /^(staged|unstaged|uncommitted|commit ([0-9a-f]+)|(branch since|since) ([0-9a-f]+))$/.exec(v.trim());
  if (!d) return null;
  if (d[2]) return { kind: "commit", sha: d[2] };
  if (d[4]) return { kind: "review", scope: d[3] === "since" ? "commit" : "branch", base: d[4] };
  return d[1] === "uncommitted" ? { kind: "review", scope: "uncommitted" } : { kind: d[1] as "staged" | "unstaged" };
}

/** The first `max` lines of some code for a card, with the indentation they all share taken off. */
export function codePreview(code: string, max = 2): string[] {
  const rows = code.split("\n").filter((l) => l.trim() !== "");
  const indent = rows.reduce((m, l) => Math.min(m, /^\s*/.exec(l)![0].length), Infinity);
  return rows.slice(0, max).map((l) => l.slice(Number.isFinite(indent) ? indent : 0));
}

/** A fence that the code itself cannot close: longer than any run of backticks in it. */
export function fenceFor(code: string): string {
  let run = 0;
  for (const m of code.matchAll(/`+/g)) run = Math.max(run, m[0].length);
  return "`".repeat(Math.max(3, run + 1));
}

/** The path as the agent reads it: relative to the session's folder when it is inside, else unchanged. */
export function relativeTo(cwd: string | undefined, path: string): string {
  if (!cwd) return path;
  const root = cwd.replace(/\/+$/, "");
  return root && path.startsWith(root + "/") ? path.slice(root.length + 1) : path;
}

/** A code comment's quote: the lines (`lines` is everything in the range), cut to MAX_CODE_LINES. */
export function codeQuote(a: { file: string; path: string; from: number; to: number; lines: readonly string[]; old?: boolean; diff?: DiffScope; lang?: string; mid?: string; sid?: string; cwd?: string }): DraftQuote {
  const cut = truncateLines(a.lines);
  const spot: CodeSpot = { file: a.file, path: a.path, from: a.from, to: a.to, omitted: cut.omitted };
  if (a.old) spot.old = true;
  if (a.diff) spot.diff = a.diff;
  if (a.lang) spot.lang = a.lang;
  if (a.mid) spot.mid = a.mid;
  if (a.sid !== undefined) spot.sid = a.sid;
  if (a.cwd) spot.cwd = a.cwd;
  return { text: cut.lines.join("\n"), comment: { body: "", spot } };
}

/** The words picked in the transcript as a quote. */
export function textQuote(words: string, anchor: TextAnchor | undefined, from?: string, ts?: string): DraftQuote {
  return { text: words.trim(), from, ts, comment: { body: "", ...(anchor ? { anchor } : {}) } };
}

// i18n-ignore: the block below is written into the message for the agent and read back by parseCommentBlock
const HEAD = /^(Code comment|Element comment|Comment) (\d+)$/;

/** The first line of a comment block (`Comment 2`, `Code comment 1`, `Element comment 3`). */
export function isCommentHead(line: string): boolean {
  return HEAD.test(line.trim());
}

/** `path:12-14` for a code comment, with the diff it is in when it is one. */
export function spotLabel(s: CodeSpot): string {
  return `${s.file}:${rangeLabel(s.from, s.to)}${s.diff ? ` · ${diffLabel(s.diff)}` : ""}`;
}

/** The comment as the block the agent reads (before the `> ` marks): its number, where, the words or code, the note. */
export function commentBlock(q: DraftQuote, n: number): string {
  const c = q.comment;
  if (!c) return q.text;
  const note = clampComment(c.body).trim();
  const out: string[] = [];
  if (c.element) {
    const e = c.element;
    out.push(`Element comment ${n}`, `Page: ${e.url}`, `Element: ${e.selector}`);
    if (e.component) out.push(`Component: ${e.component}${e.source ? ` (${e.source})` : ""}`);
    else if (e.source) out.push(`Source: ${e.source}`);
    if (e.text) out.push(`Text: ${e.text.replace(/\s+/g, " ")}`);
    const shown = q.text.split("\n").length;
    if (e.omitted > 0) out.push(`Truncated: the first ${shown} of ${shown + e.omitted} lines`);
    const fence = fenceFor(q.text);
    out.push(fence + "html", q.text, fence);
  } else if (c.spot) {
    const s = c.spot;
    out.push(`Code comment ${n}`, `File: ${s.file}:${rangeLabel(s.from, s.to)}`);
    if (s.old) out.push("Side: old");
    if (s.diff) out.push(`Diff: ${diffLabel(s.diff)}`);
    const shown = q.text.split("\n").length;
    if (s.omitted > 0) out.push(`Truncated: the first ${shown} of ${shown + s.omitted} lines`);
    const fence = fenceFor(q.text);
    out.push(fence + (s.lang ?? ""), q.text, fence);
  } else {
    out.push(`Comment ${n}`, "Selected text:", ...q.text.split("\n").map((l) => `> ${l}`));
  }
  if (note) out.push("", note);
  return out.join("\n");
}

/** A comment block read back (an edited message, a sent bubble); null when it is not one. */
export function parseCommentBlock(block: string): DraftQuote | null {
  const lines = block.split("\n");
  const head = HEAD.exec(lines[0]?.trim() ?? "");
  if (!head) return null;
  let i = 1;
  if (head[1] === "Comment") {
    if (lines[i]?.trim() === "Selected text:") i++;
    const words: string[] = [];
    while (i < lines.length && /^ {0,3}>/.test(lines[i])) words.push(lines[i++].replace(/^ {0,3}> ?/, ""));
    const body = lines.slice(i).join("\n").trim();
    return { text: words.join("\n").trim(), comment: { body } };
  }
  if (head[1] === "Element comment") return parseElementBlock(lines.slice(1));
  const spot: Partial<CodeSpot> = { omitted: 0 };
  for (; i < lines.length && !/^`{3,}/.test(lines[i]); i++) {
    const m = /^(File|Side|Diff|Truncated): ?(.*)$/.exec(lines[i]);
    if (!m) continue;
    if (m[1] === "File") {
      const at = /^(.*):(\d+)(?:-(\d+))?$/.exec(m[2]);
      if (at) Object.assign(spot, { file: at[1], path: at[1], from: Number(at[2]), to: Number(at[3] ?? at[2]) });
    } else if (m[1] === "Side") spot.old = m[2] === "old";
    else if (m[1] === "Diff") {
      const d = parseDiffLabel(m[2]);
      if (d) spot.diff = d;
    } else {
      const t = /^the first (\d+) of (\d+) lines$/.exec(m[2]);
      if (t) spot.omitted = Number(t[2]) - Number(t[1]);
    }
  }
  const open = /^(`{3,})(.*)$/.exec(lines[i] ?? "");
  const code: string[] = [];
  if (open) {
    if (open[2]) spot.lang = open[2];
    for (i++; i < lines.length && lines[i] !== open[1]; i++) code.push(lines[i]);
    i++;
  }
  if (spot.file === undefined) return null;
  return { text: code.join("\n"), comment: { body: lines.slice(i).join("\n").trim(), spot: spot as CodeSpot } };
}

/** The rest of an `Element comment N` block (after its head). */
function parseElementBlock(lines: string[]): DraftQuote | null {
  const el: Partial<ElementSpot> = { omitted: 0 };
  let i = 0;
  for (; i < lines.length && !/^`{3,}/.test(lines[i]); i++) {
    const m = /^(Page|Element|Component|Source|Text|Truncated): ?(.*)$/.exec(lines[i]);
    if (!m) continue;
    if (m[1] === "Page") el.url = m[2];
    else if (m[1] === "Element") el.selector = m[2];
    else if (m[1] === "Component") {
      const c = /^(.*?)(?: \((.*)\))?$/.exec(m[2])!;
      el.component = c[1];
      if (c[2]) el.source = c[2];
    } else if (m[1] === "Source") el.source = m[2];
    else if (m[1] === "Text") el.text = m[2];
    else {
      const t = /^the first (\d+) of (\d+) lines$/.exec(m[2]);
      if (t) el.omitted = Number(t[2]) - Number(t[1]);
    }
  }
  const open = /^(`{3,})(.*)$/.exec(lines[i] ?? "");
  const code: string[] = [];
  if (open) {
    for (i++; i < lines.length && lines[i] !== open[1]; i++) code.push(lines[i]);
    i++;
  }
  if (el.url === undefined || el.selector === undefined) return null;
  return { text: code.join("\n"), comment: { body: lines.slice(i).join("\n").trim(), element: el as ElementSpot } };
}

/** Opens the file or the diff a code comment is about at its lines; false when it does not know where (a bubble read back). */
export function openSpot(s: CodeSpot): boolean {
  if (!s.mid) return false;
  if (s.diff?.kind === "review") openPanel({ kind: "review", mid: s.mid, sid: s.sid ?? "", cwd: s.cwd ?? "", at: { path: s.path, line: s.from, old: s.old } });
  else if (s.diff) openPanel({ kind: "git", mid: s.mid, sid: s.sid ?? "", cwd: s.cwd ?? "", at: { path: s.path, diff: s.diff, line: s.from, to: s.to, old: s.old } });
  else openPanel({ kind: "file", mid: s.mid, path: s.path, cwd: s.cwd, line: s.from, to: s.to, sid: s.sid });
  return true;
}
