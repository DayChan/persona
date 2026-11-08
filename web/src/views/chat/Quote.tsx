// "Quote", drawn instead of `> ` lines (WeChat / Slack's reply quote): above the input, a bar with who said it and a
// two-line plain summary, × to drop it; in a sent bubble, the leading `> ` block as a three-line card that opens to
// the whole passage. The message itself still carries the `> ` block (lib/drafts.ts withQuoteBlock / splitQuote). A
// task's card quotes as its title (and todo id, on the block's first line) and what its agent said last.
import { useLayoutEffect, useRef, useState } from "react";
import { openSpot } from "../../lib/comments";
import { parseQuote, parseQuotes, useQuotes, type DraftQuote } from "../../lib/drafts";
import { getSession } from "../../lib/store";
import { clock } from "../../lib/format";
import { plainMarkdown } from "../../lib/plain-markdown";
import { cx, EASE } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { CommentBlock, goToComment, CommentCard } from "./CommentCard";
import { t } from "../../i18n";

/** "Quoting Persona · 16:45" / "Quoting task "…" · 16:45": who said it, or which task, and when, as far as is known. */
export function quoteSource(q: DraftQuote): string {
  if (q.task) return [t("chat-quote.quoting_task", { title: q.task.title }), clock(q.ts)].filter(Boolean).join(" · ");
  return [q.from ? t("chat-quote.quoting_from", { name: q.from }) : t("chat-quote.quoting"), clock(q.ts)].filter(Boolean).join(" · ");
}

/** The quote waiting above the composer's input. */
export function QuoteBar({ quote, onRemove }: { quote: DraftQuote; onRemove: () => void }) {
  const summary = plainMarkdown(quote.text) || quote.text.trim();
  return (
    <div
      className="flex min-w-0 items-start gap-2 rounded-[10px] bg-inset py-1.5 pr-1 pl-2 shadow-hairline"
      title={quote.task ? `${quote.task.title}\n${quote.text}` : quote.text}
      aria-label={t("chat-quote.quoted_message")}
      style={{ animation: `pop-in 180ms ${EASE} both` }}
    >
      <span aria-hidden className="w-[3px] shrink-0 self-stretch rounded-full bg-accent" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5 py-px">
        <span className="flex min-w-0 items-center gap-1 text-[11.5px] font-medium whitespace-nowrap text-ink-3">
          <Icon name={quote.task ? "check" : "quote"} size={12} className="shrink-0" />
          <span className="min-w-0 truncate">{quoteSource(quote)}</span>
        </span>
        {summary && <span className="line-clamp-2 text-[12.5px] leading-[1.5] text-ink-2 [overflow-wrap:anywhere]">{summary}</span>}
      </div>
      <button
        type="button"
        aria-label={t("chat-quote.remove")}
        title={t("chat-quote.remove")}
        onClick={onRemove}
        className="flex size-6 shrink-0 items-center justify-center rounded-full text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2"
      >
        <Icon name="x" size={12} stroke={2.4} />
      </button>
    </div>
  );
}

/** Everything waiting above the input: the message quote (at most one), then the comments numbered in order. */
export function QuoteBars({ draftKey, quotes, onRemove }: { draftKey: string; quotes: readonly DraftQuote[]; onRemove: (id: string) => void }) {
  const { editing } = useQuotes(draftKey);
  let n = 0;
  return (
    <div className="flex flex-col gap-1.5 px-0.5 pt-0.5" data-quotes>
      {quotes.map((q) =>
        q.comment ? (
          <CommentCard key={q.id} draftKey={draftKey} quote={q} n={++n} editing={editing === q.id} onRemove={() => onRemove(q.id!)} />
        ) : (
          <QuoteBar key={q.id} quote={q} onRemove={() => onRemove(q.id!)} />
        ),
      )}
    </div>
  );
}

/** A sent message's leading quote blocks (splitQuotes): the quote card, and a comment card for each comment. `mid`/`sid`
 *  let a code comment open its file or diff again. */
export function QuoteStack({ blocks, mid, sid }: { blocks: readonly string[]; mid?: string; sid?: string }) {
  const quotes = parseQuotes(blocks);
  let n = 0;
  return (
    <>
      {quotes.map((q, i) => {
        if (!q.comment) return <QuoteBlock key={i} text={blocks[i]} />;
        const spot = q.comment.spot;
        const cwd = mid && sid ? getSession(mid, sid)?.cwd : undefined;
        // the path the agent read is relative to the session's folder; with the folder known it opens again
        let open = spot && mid && cwd ? () => openSpot({ ...spot, mid, sid, cwd, path: spot.diff || spot.file.startsWith("/") ? spot.path : `${cwd.replace(/\/+$/, "")}/${spot.file}` }) : undefined;
        // an element of a preview: that session's preview, at the page it was on (the port is the page's)
        const el = q.comment.element;
        const port = el ? Number(new URL(el.url, "http://localhost").port) : 0;
        if (el && mid && sid && cwd && port) open = () => goToComment({ ...q, comment: { ...q.comment!, element: { ...el, mid, sid, cwd, port } } });
        return <CommentBlock key={i} quote={q} n={++n} open={open} />;
      })}
    </>
  );
}

/** A sent message's leading `> ` block, at the top of its bubble: three plain lines, a click opens the whole passage, as Markdown. */
export function QuoteBlock({ text: whole }: { text: string }) {
  const clampRef = useRef<HTMLSpanElement>(null);
  const [long, setLong] = useState(false);
  const [open, setOpen] = useState(false);
  // a task's quote: its title on a line of its own (the todo id is for the assistant), then the words under it
  const { text, task } = parseQuote(whole);
  const summary = plainMarkdown(text) || text;
  useLayoutEffect(() => {
    const el = clampRef.current;
    if (el && !open) setLong(el.scrollHeight > el.clientHeight + 1);
  }, [summary, open]);
  return (
    <div className="flex min-w-0 gap-2 rounded-[8px] bg-ink/[0.05] py-1.5 pr-2.5 pl-2 whitespace-normal" data-quote>
      <span aria-hidden className="w-[3px] shrink-0 self-stretch rounded-full bg-ink/20" />
      <div className="flex min-w-0 flex-1 flex-col items-start gap-0.5">
        {task && (
          <span className="flex max-w-full min-w-0 items-center gap-1 text-[12px] font-medium whitespace-nowrap text-ink-3" data-quote-task>
            <Icon name="check" size={12} className="shrink-0" />
            <span className="min-w-0 truncate">{t("chat-quote.task_title", { title: task.title })}</span>
          </span>
        )}
        {!text ? null : open ? (
          <div className="max-h-[320px] w-full overflow-y-auto text-ink-2 [overflow-wrap:anywhere]">
            <Markdown text={text} small compact />
          </div>
        ) : (
          // the words open on a click too; the button under them is the keyboard's way
          <span
            ref={clampRef}
            onClick={long ? () => setOpen(true) : undefined}
            className={cx(task ? "line-clamp-2" : "line-clamp-3", "text-[13px] leading-[1.55] text-ink-2 [overflow-wrap:anywhere]", long && "cursor-pointer")}
          >
            {summary}
          </span>
        )}
        {long && (
          <button
            type="button"
            onClick={() => setOpen((o) => !o)}
            aria-expanded={open}
            className="-ml-1 rounded-[6px] px-1 text-[12px] font-medium whitespace-nowrap text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2"
          >
            {open ? t("chat-quote.collapse") : t("chat-quote.expand")}
          </button>
        )}
      </div>
    </div>
  );
}
