// Reviewing a session's changes (docs/web/review.md, #151): what git/review answers, as the diff items the code review
// draws (views/extensions/DiffReview.tsx, the same component an extension's code page uses), the comments of the review
// that is under way (kept per session in this browser), and the review as the message the agent gets. Drawn by
// views/session/ReviewPanel.tsx.
import { api, enc } from "./api";
import { codeQuote, type DiffScope } from "./comments";
import { hunkRows, type Hunk, type Thread } from "./code-view";
import { withQuoteBlock, type DraftQuote } from "./drafts";
import type { UIItem } from "./extension-ui";
import { uuid } from "./format";
import { useSyncExternalStore } from "react";

/** The hub feature that says it has git/review. */
export const REVIEW_FEATURE = "git_review";

export type ReviewScope = "uncommitted" | "branch" | "commit";

export interface GitReviewFile {
  path: string;
  old_path?: string;
  status: "added" | "modified" | "deleted" | "renamed" | "copied" | "type_changed" | string;
  untracked?: boolean;
  added: number;
  deleted: number;
  binary?: boolean;
  generated?: boolean;
  refused?: boolean;
  big?: boolean;
  revision?: string;
  hunks?: Hunk[];
  /** This page's own: too big even asked for on its own. */
  huge?: boolean;
}

export interface GitReview {
  root: string;
  scope: ReviewScope;
  base: { hash?: string; short?: string; subject?: string; ref?: string; empty?: boolean };
  head?: { hash: string; short: string; branch?: string };
  files: GitReviewFile[];
  truncated?: boolean;
}

/** What is reviewed: a scope, and the commit (commit) or branch (branch) it is against when one was picked. */
export interface ReviewTarget {
  scope: ReviewScope;
  base?: string;
}

export function reviewURL(mid: string, cwd: string, t: ReviewTarget, paths: readonly string[] = []): string {
  const q = new URLSearchParams({ cwd, scope: t.scope });
  if (t.base) q.set("base", t.base);
  for (const p of paths) q.append("path", p);
  return `/machines/${enc(mid)}/git/review?${q}`;
}

export const loadReview = (mid: string, cwd: string, t: ReviewTarget, signal?: AbortSignal) => api<GitReview>(reviewURL(mid, cwd, t), { signal });

/** One file asked for on its own (it came `big`): against the same base, so it is the same review. */
export async function loadReviewFile(mid: string, cwd: string, res: GitReview, f: GitReviewFile): Promise<GitReviewFile | null> {
  const target: ReviewTarget = res.base.hash ? { scope: "commit", base: res.base.hash } : { scope: res.scope };
  const got = await api<GitReview>(reviewURL(mid, cwd, target, f.old_path ? [f.old_path, f.path] : [f.path]));
  return got.files.find((x) => x.path === f.path) ?? null;
}

/** The totals of a review's files. */
export function reviewTotals(files: readonly GitReviewFile[]): { added: number; deleted: number } {
  return files.reduce((s, f) => ({ added: s.added + f.added, deleted: s.deleted + f.deleted }), { added: 0, deleted: 0 });
}

/** The scope as a comment's `Diff:` line says it: `uncommitted`, `branch since <hash>`, `since <hash>` (Git's own
 *  short hash of the base, unique in the repository). */
export function reviewDiff(res: Pick<GitReview, "scope" | "base">): DiffScope {
  const base = res.base.short || res.base.hash?.slice(0, 8);
  if (res.scope === "uncommitted" || !base) return { kind: "review", scope: "uncommitted" };
  return { kind: "review", scope: res.scope, base };
}

// ── the comments of the review under way ──────────────

/** A comment waiting in the review: where it is, the code it is on (with the diff's marks), and the note. */
export interface PendingComment {
  id: string;
  path: string;
  side: "old" | "new";
  line: number;
  start_line?: number;
  body: string;
  /** The diff's rows of those lines (" ", "+", "-" in front), as the agent gets them. */
  code: string[];
  /** The diff it was made on (an old side's numbers belong to it). */
  diff: DiffScope;
  at: string;
}

export interface PendingReview {
  comments: PendingComment[];
  /** The summary written when a send failed, offered again. */
  note?: string;
}

const storeKey = (session: string) => `persona.review:${session}`;
const listeners = new Set<() => void>();
const cache = new Map<string, PendingReview>();
const EMPTY: PendingReview = { comments: [] };

export function pendingReview(session: string): PendingReview {
  if (cache.has(session)) return cache.get(session)!;
  let v: PendingReview = EMPTY;
  try {
    const raw = JSON.parse(localStorage.getItem(storeKey(session)) ?? "null") as PendingReview | null;
    if (raw && Array.isArray(raw.comments)) v = { comments: raw.comments.filter((c) => c && typeof c.path === "string" && typeof c.line === "number"), note: typeof raw.note === "string" ? raw.note : undefined };
  } catch {
    // a value from another build: start again
  }
  cache.set(session, v);
  return v;
}

export function setPendingReview(session: string, v: PendingReview) {
  const next = v.comments.length || v.note ? v : EMPTY;
  cache.set(session, next);
  try {
    if (next === EMPTY) localStorage.removeItem(storeKey(session));
    else localStorage.setItem(storeKey(session), JSON.stringify(next));
  } catch {
    // private mode: kept for this page
  }
  listeners.forEach((l) => l());
}

export function usePendingReview(session: string): PendingReview {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      const storage = (e: StorageEvent) => {
        if (e.key !== storeKey(session)) return;
        cache.delete(session);
        l();
      };
      addEventListener("storage", storage);
      return () => {
        listeners.delete(l);
        removeEventListener("storage", storage);
      };
    },
    () => pendingReview(session),
    () => pendingReview(session),
  );
}

/** A comment made on lines of a file's diff: the rows from the first picked line to the last (the removed ones between
 *  them too, as they read in the diff); lines shown from between hunks come as unchanged ones. */
export function pendingComment(
  f: Pick<GitReviewFile, "path" | "hunks">,
  pick: { side: "old" | "new"; line: number; start_line?: number },
  body: string,
  diff: DiffScope,
  shownLine: (n: number) => string | undefined = () => undefined,
): PendingComment {
  const from = pick.start_line ?? pick.line;
  const to = pick.line;
  const rows = (f.hunks ?? []).flatMap((h) => hunkRows(h));
  const num = (r: (typeof rows)[number]) => (pick.side === "old" ? r.old : r.new);
  const first = rows.findIndex((r) => num(r) === from);
  const last = rows.findLastIndex((r) => num(r) === to);
  const code: string[] = [];
  if (first >= 0 && last >= first) {
    for (const r of rows.slice(first, last + 1)) {
      if (r.kind === "note") continue;
      code.push((r.kind === "add" ? "+" : r.kind === "del" ? "-" : " ") + r.text);
    }
  } else {
    // lines shown from between hunks (the new side's): unchanged
    for (let n = from; n <= to; n++) {
      const l = rows.find((r) => num(r) === n)?.text ?? shownLine(n);
      if (l !== undefined) code.push(" " + l);
    }
  }
  return { id: uuid(), path: f.path, side: pick.side, line: to, ...(pick.start_line && pick.start_line < to ? { start_line: from } : {}), body, code, diff, at: new Date().toISOString() };
}

const sameDiff = (a: DiffScope, b: DiffScope) => JSON.stringify(a) === JSON.stringify(b);

/** The review's comments on a file as the diff draws them: drafts at their lines (old-side ones made on another diff
 *  are outdated: those numbers are that diff's), with a way to delete each. */
export function pendingThreads(comments: readonly PendingComment[], path: string, diff: DiffScope, you: string): Thread[] {
  return comments
    .filter((c) => c.path === path)
    .map((c) => ({
      id: c.id,
      side: c.side,
      line: c.line,
      ...(c.start_line ? { start_line: c.start_line } : {}),
      pending: true,
      outdated: c.side === "old" && !sameDiff(c.diff, diff),
      comments: [{ id: c.id, author: you, avatar: { text: you.slice(0, 1) }, at: c.at, body: c.body }],
      remove: { id: "remove" },
    }));
}

/** The comments in the order the agent reads them: by file as the review lists them, then by line. */
export function orderedComments(comments: readonly PendingComment[], files: readonly { path: string }[]): PendingComment[] {
  const at = new Map(files.map((f, i) => [f.path, i]));
  return [...comments].sort((a, b) => (at.get(a.path) ?? 1e9) - (at.get(b.path) ?? 1e9) || a.path.localeCompare(b.path) || (a.start_line ?? a.line) - (b.start_line ?? b.line));
}

/** A comment as the code comment the agent gets (lib/comments.ts `Code comment N`). */
export function reviewQuote(c: PendingComment, at: { mid: string; sid: string; cwd: string }): DraftQuote {
  const lines = c.code.length ? c.code : [""];
  const q = codeQuote({ file: c.path, path: c.path, from: c.start_line ?? c.line, to: c.line, lines, old: c.side === "old", diff: c.diff, lang: "diff", mid: at.mid, sid: at.sid, cwd: at.cwd });
  return { ...q, comment: { ...q.comment!, body: c.body } };
}

/** The review as one message: every comment's block, then the summary. */
export function reviewMessage(comments: readonly PendingComment[], note: string, at: { mid: string; sid: string; cwd: string }): string {
  return withQuoteBlock(
    comments.map((c) => reviewQuote(c, at)),
    note.trim(),
  );
}

// ── as the code review's items ────────────────────────

/** The diff items of a review: one per file, with the actions the panel answers (a draft, a comment straight to the
 *  message box, a big file's diff, the lines between hunks). */
export function reviewItems(res: GitReview, loaded: ReadonlyMap<string, GitReviewFile>, comments: readonly PendingComment[], you: string): UIItem[] {
  const diff = reviewDiff(res);
  return res.files.map((orig) => {
    const f = loaded.get(orig.path) ?? orig;
    const hunks = f.hunks ?? [];
    const readable = !f.refused && !f.binary;
    const item: UIItem = {
      type: "diff",
      key: `${f.path}:${f.revision ?? ""}`,
      path: f.path,
      status: f.status,
      additions: f.added,
      deletions: f.deleted,
      binary: !!f.binary,
      generated: !!f.generated,
      hunks,
      threads: pendingThreads(comments, f.path, diff, you),
      revision: f.revision,
    };
    if (f.old_path && f.old_path !== f.path) item.old_path = f.old_path;
    if (readable) {
      item.draft = { id: "draft" };
      item.comment = { id: "comment" };
      item.suggestions = true;
      // the new side is the file on disk: the lines between hunks are read from it (a removed file has none)
      if (f.status !== "deleted") item.context = { id: "context" };
      if (f.big && !f.huge && !hunks.length) item.load = { id: "load" };
    }
    return item;
  });
}
