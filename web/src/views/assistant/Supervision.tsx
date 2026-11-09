import { useState } from "react";
import { t } from "../../i18n";
import type { FeedItem } from "../../lib/feed";
import { autoReviewLine, checkCount, checklist, latestReview, reportLine, reviewBadge, reviewVerdictLabel, type CheckMark, type ReviewTone, type Supervision } from "../../lib/supervision";
import type { Todo } from "../../lib/todos";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { useClamped } from "../../ui/use-clamped";

// How a supervised task's review stands (#3124, docs/assistant/unified-entry.md "Supervision"): a chip and the count of criteria met
// on its card, the checklist on its page, a line in the conversation for each review.

const TONE: Record<ReviewTone, string> = {
  accent: "bg-field text-accent-ink",
  red: "bg-field text-red",
  orange: "bg-field text-orange",
  green: "bg-green-tint text-green",
  muted: "bg-field text-ink-2",
};

/** The review's chip, with the criteria met; nothing for a task that is not supervised. */
export function ReviewBadge({ todo, className }: { todo: Todo; className?: string }) {
  const badge = reviewBadge(todo);
  if (!badge) return null;
  const count = checkCount(todo.supervision);
  return (
    <span data-testid="review-badge" className={cx("inline-flex h-5 shrink-0 items-center gap-1 whitespace-nowrap rounded-md px-1.5 text-[11.5px] font-semibold", TONE[badge.tone], className)}>
      <Icon name="target" size={11} stroke={2.2} />
      {badge.label}
      {count && <span className="font-medium tabular-nums opacity-80">· {count}</span>}
    </span>
  );
}

const MARK: Record<CheckMark, { icon: "check" | "x" | "clock"; className: string; label: () => string }> = {
  met: { icon: "check", className: "text-green", label: () => t("assistant-tasks.review_met") },
  missing: { icon: "x", className: "text-red", label: () => t("assistant-tasks.review_missing") },
  open: { icon: "clock", className: "text-ink-3", label: () => t("assistant-tasks.review_open") },
};

/** The task's criteria, each as its latest review found it, and what the supervisor said last. */
export function ReviewChecklist({ todo }: { todo: Todo }) {
  const sup = todo.supervision;
  if (!sup) return null;
  const list = checklist(sup);
  const review = latestReview(sup);
  return (
    <section data-testid="review-checklist" className="mt-4 rounded-control bg-inset p-3.5 shadow-hairline">
      <div className="flex items-center gap-2">
        <span className="text-[12.5px] font-semibold text-ink-2">{t("assistant-tasks.review_title")}</span>
        <ReviewBadge todo={todo} />
        <span className="flex-1" />
        {sup.expect_minutes ? <span className="text-[12px] text-ink-3">{t("assistant-tasks.review_expect", { minutes: sup.expect_minutes })}</span> : null}
      </div>
      {list.length === 0 ? (
        <p className="mt-2 text-[12.5px] text-ink-3">{t("assistant-tasks.review_none")}</p>
      ) : (
        <ul className="mt-2 flex flex-col gap-1.5">
          {list.map((c) => {
            const m = MARK[c.mark];
            return (
              <li key={c.criterion} className="flex items-start gap-2 text-[13px]">
                <span role="img" aria-label={m.label()} title={m.label()} className={cx("mt-0.5 shrink-0", m.className)}>
                  <Icon name={m.icon} size={13} stroke={2.4} />
                </span>
                <span className="flex min-w-0 flex-col">
                  <span className="text-ink">{c.criterion}</span>
                  {c.evidence && <span className="text-[12px] text-ink-3 select-text">{c.evidence}</span>}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {review?.note && <p className="mt-2 text-[12.5px] leading-[1.6] text-ink-2 select-text">{t("assistant-tasks.review_note", { note: review.note })}</p>}
      <AutoReviewResult sup={sup} />
    </section>
  );
}

/** The independent reviewer's result (#2173) under the criteria: a line, and its report on a click. Nothing when no
 *  review ran, so a task without one looks as it did. */
function AutoReviewResult({ sup }: { sup: Supervision }) {
  const [open, setOpen] = useState(false);
  const line = autoReviewLine(sup);
  if (!line) return null;
  const findings = sup.auto_review?.findings;
  return (
    <div data-testid="auto-review" className="mt-2 flex flex-col gap-1 text-[12.5px] text-ink-2">
      <div className="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5">
        <Icon name="target" size={12} className="shrink-0" />
        <span className="shrink-0 whitespace-nowrap font-semibold">{t("assistant-tasks.auto_review_title")}</span>
        <span className="min-w-0">· {line}</span>
        {findings && (
          <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="touch-hit ml-auto shrink-0 whitespace-nowrap text-ink-3 hover:text-ink-2">
            {t(open ? "assistant-tasks.auto_review_hide" : "assistant-tasks.auto_review_show")}
          </button>
        )}
      </div>
      {open && findings && (
        <div data-testid="auto-review-findings" className="leading-[1.55] whitespace-pre-wrap text-ink select-text [overflow-wrap:anywhere]">
          {findings}
        </div>
      )}
    </div>
  );
}

/** A supervised task's agent reported to Persona (feed kind "report": a question, a block, a finish): who, and what. */
export function ReportLine(props: { item: FeedItem; title: string; onOpenTask?: () => void }) {
  const [open, setOpen] = useState(false);
  if (props.item.body?.kind !== "progress") return <ReportBody {...props} />;
  return (
    <div className="flex min-w-0 flex-col gap-1" data-testid="progress-report">
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="touch-hit flex min-w-0 items-center gap-1 text-left text-xs whitespace-nowrap text-ink-3 hover:text-ink-2">
        <span className="min-w-0 truncate">{t("assistant-tasks.report_progress", { title: props.title })}</span>
        <Icon name="chevronRight" size={12} className="shrink-0" style={{ transform: open ? "rotate(90deg)" : undefined }} />
      </button>
      {open && <ReportBody {...props} />}
    </div>
  );
}

function ReportBody({ item, title, onOpenTask }: { item: FeedItem; title: string; onOpenTask?: () => void }) {
  const body = (item.body ?? {}) as { kind?: string; text?: string; step?: number; steps?: number; evidence?: string[]; later?: boolean };
  // A question that can wait is no call on the owner now: it is drawn quietly, with its mark.
  const tone = !body.later && (body.kind === "blocked" || body.kind === "question") ? "text-orange" : "text-ink-2";
  return (
    <div data-testid="report-line" className="flex max-w-full min-w-0 flex-col gap-1 self-start pr-10">
      <span className={cx("inline-flex min-w-0 items-center gap-1 text-[12px] font-medium", tone)}>
        <Icon name="chat" size={11} className="shrink-0" />
        {onOpenTask ? (
          <button type="button" onClick={onOpenTask} className="min-w-0 truncate text-left hover:underline">
            {t("assistant-tasks.report_from", { title })}
          </button>
        ) : (
          <span className="min-w-0 truncate">{t("assistant-tasks.report_from", { title })}</span>
        )}
        {body.later && <span className="shrink-0 font-normal" data-testid="report-later">· {t("assistant-tasks.report_later")}</span>}
      </span>
      <div className="rounded-[18px] bg-field px-3.5 py-2 text-[14.5px] whitespace-pre-wrap">{reportLine(body.kind ?? "", body.text ?? "", body.step, body.steps)}</div>
      {body.evidence && body.evidence.length > 0 && (
        <ul className="flex flex-col gap-0.5 pl-1 text-[12px] text-ink-3">
          {body.evidence.slice(0, 8).map((e) => (
            <li key={e} className="truncate">· {e}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** A review in the conversation (feed kind "review"): the verdict and the supervisor's note. A note cut at the line's
 *  end opens in full on a click (what to decide, what is missing, #4861), as an answered card's line does. */
export function ReviewLine({ item, title, onOpenTask }: { item: FeedItem; title: string; onOpenTask?: () => void }) {
  const body = (item.body ?? {}) as { verdict?: string; round?: number; note?: string };
  const [open, setOpen] = useState(false);
  const { ref, cut } = useClamped<HTMLSpanElement>(open, "x");
  const expandable = Boolean(body.note) && (cut || open);
  return (
    <div data-testid="review-line" className={cx("flex max-w-full min-w-0 flex-col self-start bg-field text-[12px] text-ink-2", open ? "w-full rounded-card" : "rounded-full")}>
      <div className="flex min-w-0 items-center gap-1.5 px-3 py-1.5">
        <Icon name="target" size={12} className="shrink-0" />
        {onOpenTask ? (
          <button type="button" onClick={onOpenTask} className="max-w-[45%] shrink-0 truncate text-left hover:text-ink hover:underline">
            {title}
          </button>
        ) : (
          <span className="max-w-[45%] shrink-0 truncate">{title}</span>
        )}
        <button
          type="button"
          aria-expanded={expandable ? open : undefined}
          disabled={!expandable}
          onClick={() => setOpen(!open)}
          className="flex min-w-0 flex-1 items-center gap-1.5 text-left enabled:cursor-pointer"
        >
          <span className="shrink-0 whitespace-nowrap font-semibold">· {reviewVerdictLabel(body.verdict ?? "", body.round)}</span>
          {body.note && !open && (
            <span ref={ref} className="min-w-0 flex-1 truncate">
              {body.note}
            </span>
          )}
          {open && <span className="flex-1" />}
          {expandable && (
            <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3 transition-transform duration-200" style={{ transform: open ? "rotate(90deg)" : "none" }} />
          )}
        </button>
      </div>
      {open && body.note && (
        <div className="px-3 pt-0.5 pb-2.5 text-[13px] leading-[1.55] whitespace-pre-wrap text-ink select-text [overflow-wrap:anywhere]" data-testid="review-note">
          {body.note}
        </div>
      )}
    </div>
  );
}
