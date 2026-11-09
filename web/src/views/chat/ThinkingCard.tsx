// A thought in a work group (the reference client's ThinkingCard): "Thought for Ns", opening to its words (14 px italic on a rail) when it
// has any. Claude Code mostly writes thoughts without words: those only say how long they took. While the agent is
// thinking right now (the screen's status line, `live.thinking`) the card at the end of the live group says
// Thinking with three dots and a clock.
import { useState } from "react";
import type { Item, SessionLive } from "../../lib/types";
import { useStore } from "../../lib/store";
import { workDuration } from "../../lib/work-groups";
import { Collapse, cx, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { CHEVRON_SLOT, STATUS_SLOT } from "./ActionRow";
import { useExpanded } from "./expanded";
import { useReveal } from "./search-reveal";
import { t } from "../../i18n";

const NONE: string[] = [];

const HEAD = "flex w-full min-w-0 items-center gap-1.5 rounded-[8px] px-2 py-0.5 [@media(pointer:coarse)]:min-h-10 text-left select-none transition-colors duration-150 motion-reduce:transition-none";

export function ThinkingCard({ item, durationMs, live, scope = "" }: { item?: Item; durationMs?: number; live?: SessionLive | null; scope?: string }) {
  const text = item?.text?.trim() ?? "";
  const [open, setOpen] = useExpanded(`${scope}thinking:${item?.id ?? "live"}`);
  // a search hit in its words (search-reveal.ts): it opens
  useReveal(scope, item ? [item.id] : NONE, () => {
    if (open || !text) return;
    setOpen(true);
    return () => setOpen(false);
  });
  if (live !== undefined) return live?.thinking ? <LiveThinking /> : null;
  const label = t("chat-thinkingcard.thought_for", { duration: workDuration(durationMs ?? 0) });
  return (
    <div className="flex min-w-0 flex-col" data-thinking={text ? "words" : "empty"} data-items={item?.id}>
      <button type="button" disabled={!text} aria-expanded={text ? open : undefined} onClick={() => setOpen((o) => !o)} className={cx(HEAD, text ? "group cursor-pointer hover:bg-tool-hover" : "cursor-default")}>
        <span className={STATUS_SLOT}>
          <Icon name="spark" size={14} />
        </span>
        <span className="translate-y-[1px] text-[length:var(--chat-secondary)] whitespace-nowrap text-tool-ink">{label}</span>
        <span className={CHEVRON_SLOT} aria-hidden>
          {text && <Icon name="chevronRight" size={13} stroke={2.2} className="transition-transform duration-150 motion-reduce:transition-none" style={{ transform: open ? "rotate(90deg)" : "none" }} />}
        </span>
      </button>
      {text && (
        <Collapse open={open} lazy>
          <div className="mt-1 border-l-2 border-tool-rail py-1.5 pl-3 select-text">
            <p className="text-[length:var(--chat-secondary)] leading-[1.6] whitespace-pre-wrap text-thinking-ink italic">{text}</p>
          </div>
        </Collapse>
      )}
    </div>
  );
}

/** A thought's opening as one row of a live group's preview (cindy's ThinkingActivityRow). */
export function ThinkingLine({ item }: { item: Item }) {
  const text = (item.text ?? "").trim().replace(/\s+/g, " ");
  return (
    <div className="flex w-full min-w-0 items-center gap-1.5 px-2 py-[3px]" data-thinking-line>
      <span className={STATUS_SLOT}>
        <Icon name="spark" size={13} />
      </span>
      <span className="min-w-0 flex-1 truncate text-[length:var(--chat-secondary)] text-thinking-ink italic" title={text}>
        {text}
      </span>
    </div>
  );
}

function LiveThinking() {
  // the clock runs from when this card saw the thinking start
  const [since] = useState(Date.now);
  const now = useNow(500, useStore((s) => s.conn === "online"));
  return (
    <div className={HEAD} data-thinking="live" aria-live="polite">
      <span className={STATUS_SLOT}>
        <Icon name="spark" size={14} />
      </span>
      <span className="translate-y-[1px] text-[length:var(--chat-secondary)] text-tool-ink">Thinking</span>
      <span aria-hidden className="flex translate-y-[2px] items-center gap-[3px]">
        {[0, 200, 400].map((d) => (
          <span key={d} className="size-1 rounded-full bg-tool-ink animate-[breathe_1.2s_ease-in-out_infinite] busy-motion" style={{ animationDelay: `${d}ms` }} />
        ))}
      </span>
      <span className="flex-1" />
      <span className="font-mono text-[12px] text-tool-ink tabular-nums">{workDuration(Math.max(0, now - since))}</span>
    </div>
  );
}
