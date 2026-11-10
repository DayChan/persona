// Transcript notices, recaps and model errors. Background continuation belongs to the normal execution summary.
import { memo, useMemo, useState } from "react";
import { lineItemIds } from "../../lib/chat-search";
import { useChatContext } from "./chat-context";
import { useExpanded } from "./expanded";
import { useReveal } from "./search-reveal";
import { ago, clock } from "../../lib/format";
import { retryable, type APIErrorInfo } from "../../lib/api-errors";
import { runAction, turnErrorFix } from "../../lib/readiness";
import { wakeSummary } from "../../lib/tasks";
import { type ChatLine, type OpenRecap } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { Button, Collapse, copyText, cx, Dialog } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { SegmentView } from "./SegmentView";
import { CompactDivider } from "./Steps";
import { t } from "../../i18n";
import { ScrollFold } from "./fold-scroll";

export interface LineProps {
  line: ChatLine;
  mid: string;
  sid: string;
  streaming?: string;
  expandFold: (f: Item) => Promise<void>;
  /** "Retry" on an API error card (only on the last line, while the agent is not working). */
  onRetry?: () => void;
  onEditLastMessage?: () => void;
  /** The hub's word on what to fix for this line's model error (the session's turn_error_code, last line only). */
  fix?: { title: string; detail: string; action: string } | null;
  /** A Persona conversation: a model error's text may name a fix in Persona's settings. Agent sessions keep "Retry". */
  assistant?: boolean;
  /** The session is waiting out a subscription usage limit: the last error card says that, not that the turn was interrupted. */
  limit?: string;
  /** Inside an opened supervision trace, show complete injected inputs. */
  trace?: boolean;
  /** A finished exchange before the last two may skip drawing off screen (SegmentView `offscreen`). */
  offscreen?: (section: HTMLElement) => () => void;
}

export const ChatLineView = memo(function ChatLineView(props: LineProps) {
  // a box-less wrapper: the line's own element stays the chat's flex item; search finds the line by it (ChatSearch)
  return (
    <div className="contents" data-line={props.line.id}>
      <LineBody {...props} />
    </div>
  );
});

function LineBody({ line, mid, sid, streaming, expandFold, onRetry, onEditLastMessage, fix, assistant, limit, trace, offscreen }: LineProps) {
  switch (line.kind) {
    case "supervision":
      return <SupervisionLine line={line} mid={mid} sid={sid} streaming={streaming} expandFold={expandFold} assistant={assistant} />;
    case "segment":
      return (
        <SegmentView
          seg={line.seg}
          trace={trace}
          live={line.live}
          mid={mid}
          sid={sid}
          streaming={line.live ? streaming : undefined}
          batchedWords={!assistant}
          expandFold={expandFold}
          offscreen={offscreen}
        />
      );
    case "notice":
      return <LineNotice item={line.item} />;
    case "compact":
      return <CompactDivider item={line.item} />;
    case "recaps":
      return <RecapChip items={line.items} />;
    case "apiError": {
      if (limit) return <UsageLimitLine text={limit} />;
      // the text-derived fix points at Persona's model settings: only for Persona's own conversations
      const f = assistant ? (fix ?? turnErrorFix(line.info.code)) : null;
      return <APIErrorCard info={line.info} count={line.count} at={line.item.ts} fix={f} onRetry={onRetry && retryable(line.info) && !f ? onRetry : undefined} onEditLastMessage={!assistant && line.info.code === "safeguards" ? onEditLastMessage : undefined} />;
    }
  }
}

/** One gray line for a whole triage round; expansion uses the actual transcript renderer, not a lossy summary. */
function SupervisionLine({ line, ...props }: LineProps & { line: Extract<ChatLine, { kind: "supervision" }> }) {
  const { scope } = useChatContext();
  const [open, setOpen] = useExpanded(`${scope}supervision:${line.id}`);
  const ids = useMemo(() => lineItemIds(line), [line]);
  useReveal(scope, ids, () => {
    if (open) return;
    setOpen(true);
    return () => setOpen(false);
  });
  return (
    <section className="flex min-w-0 flex-col gap-3" data-supervision={line.live ? "running" : "done"}>
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="touch-hit flex w-full min-w-0 items-center gap-1.5 rounded-control py-1 text-left text-[length:var(--chat-secondary)] whitespace-nowrap text-ink-3 hover:text-ink-2">
        <span className="shrink-0 text-accent-ink">@{t("chat-messages.source_supervision")} · </span>
        <span className="min-w-0 truncate">{t(line.live ? "chat-lines.supervision_running" : "chat-lines.supervision_done")}</span>
        <Icon name="chevronRight" size={14} className="shrink-0" style={{ transform: open ? "rotate(90deg)" : undefined }} />
      </button>
      {open && <div className="flex min-w-0 flex-col gap-3 border-l-2 border-tool-rail pl-3" data-supervision-trace>
        {line.lines.map((child) => <LineBody key={child.id} {...props} line={child} trace />)}
      </div>}
    </section>
  );
}

/** A notice on a line of its own: a wake opening new work (a centered capsule), "Interrupted", a long local command's output. */
function LineNotice({ item }: { item: Item }) {
  const [open, setOpen] = useState(false);
  if (item.wake) {
    const w = wakeSummary(item);
    return (
      <div className="flex justify-center">
        <span className="flex max-w-full min-w-0 items-center gap-1.5 rounded-full bg-inset px-3 py-1 text-[12px] text-ink-2 shadow-hairline" title={clock(item.ts)}>
          <Icon name="bell" size={12} className={cx("shrink-0", w.failed ? "text-red" : "text-accent")} />
          <span className="min-w-0 truncate">{w.text}</span>
        </span>
      </div>
    );
  }
  const text = item.text ?? "";
  if (!text.includes("\n") && text.length <= 60) {
    return (
      <div className="flex justify-center">
        <span className="rounded-full bg-inset px-3 py-1 text-[12px] text-ink-2 shadow-hairline" title={clock(item.ts)}>
          {text}
        </span>
      </div>
    );
  }
  return (
    <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="flex w-full items-start gap-2 rounded-card bg-inset px-3 py-2 text-left shadow-hairline">
      <Icon name="info" size={13} className="mt-[3px] shrink-0 text-ink-3" />
      <span className={cx("min-w-0 text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2", !open && "line-clamp-4")}>{text}</span>
    </button>
  );
}

// ── recaps ──

/** Claude Code's "while you were away" recap: one short line; the recap itself opens in a drawer. Several in a row are
 *  one line, the newest first in the drawer. */
function RecapChip({ items }: { items: Item[] }) {
  const [open, setOpen] = useState(false);
  const newest = items[items.length - 1];
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        aria-label={items.length > 1 ? t("chat-lines.recap_open_many", { count: items.length }) : t("chat-lines.recap_open")}
        className="flex w-fit max-w-full items-center gap-1.5 rounded-full bg-inset px-3 py-1 text-[12px] whitespace-nowrap text-ink-2 shadow-hairline transition-colors hover:bg-hover-2"
      >
        <Icon name="retry" size={12} className="shrink-0 text-accent" />
        {t("chat-lines.recap")}
        {items.length > 1 && <span>{t("chat-lines.recap_times", { count: items.length })}</span>}
        {newest.ts && <span>· {ago(newest.ts)}</span>}
        <Icon name="chevronRight" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
      </button>
      <RecapDrawer items={items} open={open} onClose={() => setOpen(false)} />
    </>
  );
}

function RecapDrawer({ items, open, onClose }: { items: Item[]; open: boolean; onClose: () => void }) {
  return (
    <Dialog open={open} onClose={onClose} title={items.length > 1 ? t("chat-lines.recap_title_many", { count: items.length }) : t("chat-lines.recap")} width={600}>
      <div className="flex max-h-[65vh] flex-col gap-4 overflow-y-auto pr-1" data-scroll-box>
        {[...items].reverse().map((it, n) => (
          <section key={it.id} className={cx("flex flex-col gap-2", n > 0 && "border-t border-line pt-4")}>
            {it.ts && (
              <span className="text-[11.5px] text-ink-3">
                {t(n === 0 && items.length > 1 ? "chat-lines.recap_newest_written" : "chat-lines.recap_written", { ago: ago(it.ts) })}
              </span>
            )}
            <Markdown text={it.text ?? ""} lineBreaks />
          </section>
        ))}
      </div>
    </Dialog>
  );
}

/** The open recap's line, the chat's last (#1360): the older recaps folded with it as their chip, then the newest in
 *  full. In the flow with the agent's words: it scrolls with them, and the chat's end includes it. */
export function OpenRecapLine({ open, onClose }: { open: OpenRecap; onClose: () => void }) {
  return (
    <div className="flex flex-col gap-2" data-open-recap>
      {open.earlier.length > 0 && <RecapChip items={open.earlier} />}
      <RecapStrip item={open.newest} onClose={onClose} />
    </div>
  );
}

/** The newest recap in full when you come back to a chat; a click (or typing) closes it back to its chip. */
function RecapStrip({ item, onClose }: { item: Item; onClose: () => void }) {
  return (
    <div className="group/recap relative flex gap-2.5 rounded-card bg-accent-tint px-3 py-2.5" style={{ animation: "fade-up 250ms ease-out both" }}>
      <span aria-hidden className="w-[3px] shrink-0 rounded-full bg-accent" />
      <div className="flex min-w-0 flex-1 flex-col gap-1">
        <span className="flex items-center gap-1.5 text-[12px] font-semibold whitespace-nowrap text-ink">
          <Icon name="retry" size={12} className="text-accent" />
          {t("chat-lines.recap")}
          {item.ts && <span className="font-normal text-ink-3">· {ago(item.ts)}</span>}
        </span>
        <ScrollFold className="max-h-[6lh] text-[13px] leading-relaxed text-ink-2">
          <Markdown text={item.text ?? ""} small lineBreaks />
        </ScrollFold>
      </div>
      <button type="button" onClick={onClose} aria-label={t("chat-lines.recap_close")} title={t("chat-lines.close")} className="flex size-6 shrink-0 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink">
        <Icon name="x" size={13} />
      </button>
    </div>
  );
}

const CLOSED_RECAPS = "persona.closedRecaps";

/** The recap closed per conversation (its id), kept in this browser. */
export function closedRecap(key: string): string | undefined {
  try {
    return (JSON.parse(localStorage.getItem(CLOSED_RECAPS) ?? "{}") as Record<string, string>)[key];
  } catch {
    return undefined;
  }
}

export function closeRecap(key: string, id: string) {
  let all: Record<string, string> = {};
  try {
    all = JSON.parse(localStorage.getItem(CLOSED_RECAPS) ?? "{}");
  } catch {
    /* start over */
  }
  delete all[key];
  all[key] = id;
  const keys = Object.keys(all);
  if (keys.length > 300) for (const k of keys.slice(0, keys.length - 200)) delete all[k];
  localStorage.setItem(CLOSED_RECAPS, JSON.stringify(all));
}

// ── model API errors ──

/** A subscription usage limit, where an error card would have said the turn was interrupted. */
function UsageLimitLine({ text }: { text: string }) {
  return (
    <div role="status" className="flex items-start gap-2 rounded-card bg-field px-3.5 py-3 shadow-hairline">
      <Icon name="clock" size={15} stroke={2.2} className="mt-[2px] shrink-0 text-orange" />
      <span className="min-w-0 text-[14px] font-semibold text-ink">{text}</span>
    </div>
  );
}

/** A model API error: why, whether it passes by itself, the original under the fold, "Retry". One the user must fix (not
 *  signed in, a wrong key, a model that is not there) says so instead, with "Fix it" (docs/assistant/onboarding.md 4.4). */
function APIErrorCard({ info, count, at, onRetry, onEditLastMessage, fix }: { info: APIErrorInfo; count: number; at?: string; onRetry?: () => void; onEditLastMessage?: () => void; fix?: { title: string; detail: string; action: string } | null }) {
  const [open, setOpen] = useState(false);
  const sub = fix ? fix.detail : info.transient === true ? t("chat-lines.api_transient") : info.transient === false ? t("chat-lines.api_permanent") : "";
  const reason = fix ? fix.title : info.reason;
  return (
    <div className="flex flex-col gap-2.5 rounded-card bg-red-tint px-3.5 py-3 shadow-[inset_0_0_0_1px_color-mix(in_srgb,var(--red)_22%,transparent)]">
      <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} aria-label={t("chat-lines.api_error", { reason })} className="flex min-w-0 items-start gap-2 text-left">
        <Icon name="info" size={15} stroke={2.2} className="mt-[2px] shrink-0 text-red" />
        <span className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="text-[14px] font-semibold text-ink">{reason}</span>
          {sub && <span className="text-[12px] text-ink-2">{sub}</span>}
        </span>
        <span className="flex shrink-0 items-center gap-1">
          {info.status !== undefined && <span className="rounded-[4px] bg-red-tint px-1 font-mono text-[11px] font-medium text-red">{info.status}</span>}
          {count > 1 && <span className="rounded-[4px] bg-field px-1 text-[11px] font-medium whitespace-nowrap text-ink-2">{t("chat-lines.api_repeated", { count })}</span>}
          <Icon name="chevron" size={13} stroke={2.2} className="text-ink-3 transition-transform duration-200" style={{ transform: open ? "rotate(180deg)" : "none" }} />
        </span>
      </button>
      <Collapse open={open}>
        <div className="flex flex-col gap-1.5 pt-0.5">
          {info.requestId && <Field label={t("chat-lines.request_id")} value={info.requestId} />}
          {info.session && <Field label="Session" value={info.session} />}
          {at && <Field label={t("chat-lines.time")} value={clock(at)} copy={false} />}
          <pre className="max-h-56 overflow-auto rounded-[8px] bg-surface px-2.5 py-2 font-mono text-[11.5px] leading-[1.6] whitespace-pre-wrap text-ink-2 shadow-hairline [overflow-wrap:anywhere]">{info.raw}</pre>
          <button type="button" onClick={() => copyText(info.raw)} className="flex w-fit items-center gap-1 text-[12px] font-medium whitespace-nowrap text-accent-ink hover:underline">
            <Icon name="copy" size={12} />
            {t("chat-lines.copy_raw")}
          </button>
        </div>
      </Collapse>
      {fix && (
        <div>
          <Button tone="primary" onClick={() => runAction(fix.action, { onHelp: () => {} })}>
            {t("chat-lines.fix_it")}
          </Button>
        </div>
      )}
      {onRetry && (
        <div>
          <Button icon="retry" onClick={onRetry}>
            {t("chat-lines.retry")}
          </Button>
        </div>
      )}
      {onEditLastMessage && <div><Button icon="pencil" onClick={onEditLastMessage}>{t("rewind.edit_last")}</Button></div>}
    </div>
  );
}

function Field({ label, value, copy = true }: { label: string; value: string; copy?: boolean }) {
  return (
    <div className="flex min-w-0 items-center gap-2 text-[12px]">
      <span className="w-14 shrink-0 text-ink-3">{label}</span>
      <span className="min-w-0 truncate font-mono text-ink-2">{value}</span>
      {copy && (
        <button type="button" onClick={() => copyText(value)} aria-label={t("chat-lines.copy_field", { label })} className="flex size-5 shrink-0 items-center justify-center rounded-[5px] text-ink-3 hover:bg-hover-2 hover:text-ink">
          <Icon name="copy" size={11} />
        </button>
      )}
    </div>
  );
}
