// A comment on picked words or on lines of code (#2437, Moshi's "Comment"), as a card in the quote list above the
// composer (CommentCard: a numbered header, the words or the code, the note to write, a click goes back to the spot) and
// as a card in a sent bubble (CommentBlock). Same family as the quote bar next to it (Quote.tsx): one accent rule, the
// same fill; the number is the one the spot carries in the transcript (CommentMarks.tsx) and the one the agent reads.
import { useLayoutEffect, useRef } from "react";
import { jumpToComment } from "../../lib/comment-marks";
import { codePreview, diffLabel, MAX_COMMENT_CHARS, openSpot, rangeLabel, type CodeSpot, type ElementSpot } from "../../lib/comments";
import { openPanel } from "../../lib/panel";
import { addressOf } from "../../lib/preview";
import { cancelNote, editNote, setNote, type DraftQuote } from "../../lib/drafts";
import { cx, EASE, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { t } from "../../i18n";
import { composing } from "../../lib/send-history";

/** `staged`, `unstaged`, `commit 1a2b3c4d`, worded for the reader. */
export function diffName(d: NonNullable<CodeSpot["diff"]>): string {
  if (d.kind === "review") return d.scope === "uncommitted" ? t("chat-comments.review_uncommitted") : t(d.scope === "branch" ? "chat-comments.review_branch" : "chat-comments.review_since", { sha: d.base });
  return d.kind === "commit" ? t("chat-comments.commit", { sha: diffLabel(d).slice(7) }) : t(d.kind === "staged" ? "chat-comments.staged" : "chat-comments.unstaged");
}

/** `src/app.ts:12-14 · staged`, worded for the reader. */
export function spotWhere(s: CodeSpot): string {
  return [`${s.file}:${rangeLabel(s.from, s.to)}`, s.old ? t("chat-comments.side_old") : "", s.diff ? diffName(s.diff) : ""].filter(Boolean).join(" · ");
}

/** `button.save · /settings`: an element comment's element and the page it is on. */
export function elementWhere(e: ElementSpot): string {
  return [e.selector, e.port ? addressOf(e.url, e.port) : e.url].join(" · ");
}

/** Where a comment points, worded for the reader: a file's lines, or an element of a preview. */
function whereOf(c: NonNullable<DraftQuote["comment"]>): string {
  return c.element ? elementWhere(c.element) : c.spot ? spotWhere(c.spot) : "";
}

/** Lines of code (or of an element's HTML) a comment carries beyond the ones sent. */
function omittedOf(c: NonNullable<DraftQuote["comment"]>): number {
  return c.element?.omitted ?? c.spot?.omitted ?? 0;
}

/** The round number a comment carries, in the card and next to its words in the transcript. */
export function NumberDot({ n, className }: { n: number; className?: string }) {
  return (
    <span aria-hidden className={cx("flex size-4 shrink-0 items-center justify-center rounded-full bg-accent text-[10px] leading-none font-semibold text-white tabular-nums", className)}>
      {n}
    </span>
  );
}

/** The words or the lines a comment is about: two lines in the card, code in mono, one line of it each. */
function Excerpt({ q, lines }: { q: DraftQuote; lines: number }) {
  const spot = q.comment?.spot ?? q.comment?.element;
  const text = q.text.trim();
  if (!text) return null;
  if (spot) {
    return (
      <span className="flex min-w-0 flex-col font-mono text-[12px] leading-[1.5] text-ink-2" data-comment-excerpt>
        {codePreview(text, lines).map((l, i) => (
          <span key={i} className="truncate whitespace-pre">
            {l || " "}
          </span>
        ))}
      </span>
    );
  }
  return (
    <span className={cx(lines === 3 ? "line-clamp-3" : "line-clamp-2", "text-[12.5px] leading-[1.5] text-ink-2 [overflow-wrap:anywhere]")} data-comment-excerpt>
      {text}
    </span>
  );
}

/** Goes back to where the comment was made: the words in the transcript, or the file / diff at its lines. */
export function goToComment(q: DraftQuote): boolean {
  const c = q.comment;
  if (!c) return false;
  if (c.element) {
    const e = c.element;
    // the preview of that session, at the page the element was on
    if (!e.mid || !e.sid || !e.cwd || !e.port) {
      toast(t("chat-comments.jump_code_gone"));
      return false;
    }
    const path = addressOf(e.url, e.port);
    openPanel({ kind: "preview", mid: e.mid, sid: e.sid, cwd: e.cwd, port: e.port, path: path.startsWith("/") ? path : "/" });
    return true;
  }
  if (c.spot) {
    const ok = openSpot(c.spot);
    if (!ok) toast(t("chat-comments.jump_code_gone"));
    return ok;
  }
  if (q.id && jumpToComment(q.id)) return true;
  toast(t("chat-comments.jump_text_gone"));
  return false;
}

/** The card above the input. `editing`: its note is open for writing (Enter keeps it, Esc cancels). */
export function CommentCard({ draftKey, quote, n, editing, onRemove }: { draftKey: string; quote: DraftQuote; n: number; editing: boolean; onRemove: () => void }) {
  const c = quote.comment!;
  const id = quote.id!;
  const box = useRef<HTMLTextAreaElement>(null);
  const before = useRef(c.body); // the note as it was when writing began, for Esc
  const done = useRef(false);
  useLayoutEffect(() => {
    if (!editing) return;
    before.current = c.body;
    done.current = false;
    const el = box.current;
    if (el) {
      el.focus({ preventScroll: true });
      el.setSelectionRange(el.value.length, el.value.length);
    }
    // only when the writing begins
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing]);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 112)}px`;
  }, [c.body, editing]);

  const finish = (cancel: boolean) => {
    if (done.current) return;
    done.current = true;
    if (cancel) cancelNote(draftKey, id, before.current);
    else setNote(draftKey, id, c.body, true);
  };
  const where = whereOf(c);
  return (
    <div
      className="flex min-w-0 items-start gap-2 rounded-[10px] bg-inset py-1.5 pr-1 pl-2 shadow-hairline"
      aria-label={t("chat-comments.comment_n", { n })}
      data-comment-card={n}
      style={{ animation: `pop-in 180ms ${EASE} both` }}
    >
      <span aria-hidden className="w-[3px] shrink-0 self-stretch rounded-full bg-accent" />
      <div className="flex min-w-0 flex-1 flex-col gap-1 py-px">
        <button
          type="button"
          onClick={() => goToComment(quote)}
          title={t("chat-comments.jump")}
          className="flex min-w-0 flex-col gap-0.5 rounded-[6px] text-left transition-colors hover:bg-hover-2 focus-visible:outline-2 focus-visible:outline-accent"
        >
          <span className="flex min-w-0 items-center gap-1.5 text-[11.5px] font-medium whitespace-nowrap text-ink-3">
            <NumberDot n={n} />
            <span className="shrink-0">{t("chat-comments.comment_n", { n })}</span>
          </span>
          {where && <span className="truncate font-mono text-[11px] text-ink-3">{where}</span>}
          <Excerpt q={quote} lines={2} />
          {omittedOf(c) > 0 && <span className="text-[11px] text-ink-3">{t("chat-comments.omitted", { count: omittedOf(c) })}</span>}
        </button>
        {editing ? (
          <div className="flex flex-col gap-0.5">
            <textarea
              ref={box}
              value={c.body}
              rows={1}
              maxLength={MAX_COMMENT_CHARS}
              aria-label={t("chat-comments.note_label", { n })}
              placeholder={t("chat-comments.placeholder")}
              onChange={(e) => setNote(draftKey, id, e.target.value)}
              onKeyDown={(e) => {
                if (composing(e.nativeEvent)) return;
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  finish(false);
                } else if (e.key === "Escape") {
                  // not the page's Esc (it stops a running agent)
                  e.preventDefault();
                  e.stopPropagation();
                  finish(true);
                }
              }}
              onBlur={() => finish(false)}
              className="w-full resize-none rounded-[8px] bg-surface px-2 py-1 text-[13px] leading-[1.5] text-ink shadow-hairline outline-none placeholder:text-ink-3 focus:shadow-[0_0_0_1.5px_var(--accent)]"
            />
            <span className="flex justify-between text-[11px] text-ink-3">
              <span className="min-w-0 truncate">{t("chat-comments.hint_enter")}</span>
              {c.body.length > MAX_COMMENT_CHARS * 0.8 && <span className="tabular-nums">{t("chat-comments.counter", { n: c.body.length, max: MAX_COMMENT_CHARS })}</span>}
            </span>
          </div>
        ) : (
          <button
            type="button"
            onClick={() => editNote(draftKey, id)}
            className={cx("w-full rounded-[6px] text-left text-[13px] leading-[1.5] [overflow-wrap:anywhere] whitespace-pre-wrap transition-colors hover:bg-hover-2", c.body ? "text-ink" : "text-ink-3")}
          >
            {c.body || t("chat-comments.placeholder")}
          </button>
        )}
      </div>
      <button
        type="button"
        aria-label={t("chat-comments.remove")}
        title={t("chat-comments.remove")}
        onMouseDown={(e) => e.preventDefault()}
        onClick={onRemove}
        className="flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2"
      >
        <Icon name="x" size={12} stroke={2.4} />
      </button>
    </div>
  );
}

/** A comment in a sent bubble: its number and where, the words or the code, the note. The code one opens its file or
 *  diff when the session's folder is known (`cwd`). */
export function CommentBlock({ quote, n, open }: { quote: DraftQuote; n: number; open?: () => void }) {
  const c = quote.comment!;
  const where = whereOf(c);
  const head = (
    <>
      <NumberDot n={n} />
      <span className="shrink-0">{t("chat-comments.comment_n", { n })}</span>
    </>
  );
  const headClass = "flex max-w-full min-w-0 items-center gap-1.5 text-[12px] font-medium whitespace-nowrap text-ink-3";
  return (
    <div className="flex min-w-0 gap-2 rounded-[8px] bg-ink/[0.05] py-1.5 pr-2.5 pl-2 whitespace-normal" data-quote data-comment-block={n}>
      <span aria-hidden className="w-[3px] shrink-0 self-stretch rounded-full bg-accent/60" />
      <div className="flex min-w-0 flex-1 flex-col items-start gap-1">
        {open ? (
          <button type="button" onClick={open} title={t("chat-comments.jump")} className={cx(headClass, "rounded-[6px] transition-colors hover:bg-hover-2")}>
            {head}
          </button>
        ) : (
          <span className={headClass}>{head}</span>
        )}
        {where && <span className="max-w-full truncate font-mono text-[11px] text-ink-3">{where}</span>}
        <Excerpt q={quote} lines={3} />
        {omittedOf(c) > 0 && <span className="text-[11px] text-ink-3">{t("chat-comments.omitted", { count: omittedOf(c) })}</span>}
        {c.body && <span className="text-[13px] leading-[1.55] whitespace-pre-wrap text-ink [overflow-wrap:anywhere]">{c.body}</span>}
      </div>
    </div>
  );
}
