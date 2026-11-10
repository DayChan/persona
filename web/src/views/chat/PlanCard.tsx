// The agent's plan (TodoWrite) in the chat: one line, which step of how many it is on and that step, with a 2 px
// progress bar; its whole list in the side panel's Plan (#5602, after Claude Desktop). While the line is out of view, a
// small pill above the composer (PlanPill) says the same and opens the panel.
import { useEffect, useState, type RefObject } from "react";
import { openPanel } from "../../lib/panel";
import { t } from "../../i18n";
import type { Segment } from "../../lib/turns";
import { isPlanStep, planProgress, planTodos, type Todo } from "../../lib/work-groups";
import { cx, Spinner } from "../../ui/atoms";
import { Icon } from "../../ui/icons";

/** The chat's newest plan: the latest TodoWrite of two or more items in its newest exchange that has one, also when the
 *  hub folded it away with the work around it (the fold keeps the list, #5602). */
export function latestPlan(segs: Segment[]): { id: string; todos: Todo[] } | undefined {
  for (let i = segs.length - 1; i >= 0; i--) {
    for (let k = segs[i].steps.length - 1; k >= 0; k--) {
      const st = segs[i].steps[k];
      if (st.kind === "tool" && isPlanStep(st) && !st.result?.is_error && planTodos(st).length >= 2) return { id: st.id, todos: planTodos(st) };
      if (st.kind === "fold") {
        const snap = st.item.fold?.tasks?.findLast((t) => t.replace === "todo" && t.entries.length >= 2);
        if (snap) return { id: snap.source_item_id, todos: snap.entries.map((e) => ({ content: e.subject ?? "", status: todoStatus(e.status) })).filter((t) => t.content) };
      }
    }
  }
  return undefined;
}

/** Whether the plan card `id` is out of the scroll view (scrolled past, not reached, or not drawn at all). */
export function usePlanOutOfView(scroller: RefObject<HTMLDivElement | null>, id?: string): boolean {
  const [out, setOut] = useState(false);
  useEffect(() => {
    const sc = scroller.current;
    const body = sc?.firstElementChild;
    if (!sc || !body || !id) return setOut(false);
    const find = () => sc.querySelector(`[data-plan-id="${CSS.escape(id)}"]`);
    return typeof IntersectionObserver === "undefined" ? watchByScroll(sc, body, find, setOut) : watchByIntersection(sc, body, id, find, setOut);
  }, [scroller, id]);
  return out;
}

/** The browser says when the card enters or leaves the view, so a scroll or a growing chat queries nothing and reads no
 *  layout (reading the rect of a card in an exchange that content-visibility skips lays that exchange out once the chat's
 *  width changed, #2485). Edge-adjacent counts as intersecting, the same rule as the measuring fallback. The card is
 *  looked for again only once the one watched left the DOM, or while none is drawn: the body's ResizeObserver sees a card
 *  that came or went; a card redrawn at the same size in view sends its last report as it leaves; one redrawn at the same
 *  size out of view sends none (it was not intersecting and still is not), so a scroll checks that the watched card is
 *  still in the DOM, which reads no layout, and looks for the new one at most once. */
function watchByIntersection(sc: Element, body: Element, id: string, find: () => Element | null, setOut: (out: boolean) => void): () => void {
  let el: Element | null = null;
  const current = () => el?.isConnected && el.getAttribute("data-plan-id") === id;
  const refind = () => {
    if (current()) return;
    if (el) io.unobserve(el);
    el = find();
    // not drawn (the hub folded it away with the work around it): the pill is where it shows
    if (!el) return setOut(true);
    io.observe(el);
  };
  // entries of a card no longer watched may still be queued: only the watched card's latest one counts
  const io = new IntersectionObserver((entries) => {
    const last = entries.findLast((e) => e.target === el);
    if (!last) return;
    if (current()) setOut(!last.isIntersecting);
    else refind();
  }, { root: sc });
  // while none is drawn, a scroll looks for nothing: a card that comes back changes the body's size
  const onScroll = () => {
    if (el && !current()) refind();
  };
  refind();
  sc.addEventListener("scroll", onScroll, { passive: true });
  const ro = new ResizeObserver(refind);
  ro.observe(body);
  return () => {
    sc.removeEventListener("scroll", onScroll);
    ro.disconnect();
    io.disconnect();
  };
}

/** Without IntersectionObserver (jsdom): measure the card against the view on every scroll and resize. */
function watchByScroll(sc: Element, body: Element, find: () => Element | null, setOut: (out: boolean) => void): () => void {
  const check = () => {
    const el = find();
    if (!el) return setOut(true);
    const r = el.getBoundingClientRect();
    const v = sc.getBoundingClientRect();
    setOut(r.bottom < v.top || r.top > v.bottom);
  };
  check();
  sc.addEventListener("scroll", check, { passive: true });
  const ro = new ResizeObserver(check);
  ro.observe(body);
  return () => {
    sc.removeEventListener("scroll", check);
    ro.disconnect();
  };
}

const todoStatus = (s?: string): Todo["status"] => (s === "completed" || s === "in_progress" ? s : "pending");

type Progress = ReturnType<typeof planProgress>;

/** How far along a plan is, said one way wherever it shows (the line, the pill, the panel): "Step 3 of 4", and "All 4
 *  steps done" once they are. */
export function planStepText(p: Progress): string {
  return p.allDone ? t("chat-plancard.all_done", { count: p.total }) : t("chat-plancard.step_of", { step: p.step, total: p.total });
}

/** The step it is on, after how far along: "Step 3 of 4 · Run the tests"; none once all are done. */
export function planSummary(p: Progress): string {
  return [planStepText(p), p.allDone ? "" : p.current].filter(Boolean).join(" · ");
}

/** The plan in the chat: one line (#5602, after Claude Desktop): which step of how many it is on and that step, a 2 px
 *  bar of how far along it is; it opens the whole list in the side panel, which follows the chat's newest plan. */
export function PlanCard({ id, todos, mid, sid }: { id?: string; todos: Todo[]; live?: boolean; mid?: string; sid?: string }) {
  if (!todos.length) return null;
  const p = planProgress(todos);
  return (
    <div className="flex w-full" data-plan-card data-plan-id={id}>
      <div className="max-w-full min-w-0 overflow-hidden rounded-[12px] border border-line bg-surface">
        <button
          type="button"
          onClick={() => mid && sid && openPanel({ kind: "plan", mid, sid })}
          aria-label={t("chat-plancard.open")}
          className="flex w-full min-w-0 cursor-pointer items-center gap-2 px-[14px] py-[10px] transition-opacity select-none hover:opacity-80 [@media(pointer:coarse)]:min-h-10"
        >
          <Icon name="todo" size={16} className="shrink-0 text-ink" />
          <span className="shrink-0 text-[13px] leading-none text-ink">{t("chat-plancard.updated")}</span>
          <span className="shrink-0 text-[13px] leading-none text-tool-ink">·</span>
          <span className="shrink-0 text-[13px] leading-none font-medium whitespace-nowrap text-ink tabular-nums" data-plan-step>
            {planStepText(p)}
          </span>
          {!p.allDone && p.current && (
            <>
              <span className="shrink-0 text-[13px] leading-none text-tool-ink">·</span>
              <span className="mt-px min-w-0 truncate text-[13px] leading-none text-ink">{p.current}</span>
            </>
          )}
          <Icon name="chevronRight" size={14} stroke={2.2} className="shrink-0 text-tool-ink" />
        </button>
        <div className="h-[2px] w-full bg-line" data-plan-progress>
          <div className="h-full bg-ink transition-[width] duration-200 motion-reduce:transition-none" style={{ width: `${p.total ? (p.done / p.total) * 100 : 0}%` }} />
        </div>
      </div>
    </div>
  );
}

/** A plan's items, each with its state (the side panel's Plan). */
export function PlanList({ todos, live }: { todos: Todo[]; live?: boolean }) {
  return (
    <div className="flex flex-col gap-[2px]" data-plan-list>
      {todos.map((todo, i) => (
        <div key={`${todo.content}:${i}`} className="flex min-h-[30px] items-center gap-[10px]" data-todo={todo.status}>
          {todo.status === "completed" ? (
            <span className="flex size-[18px] shrink-0 items-center justify-center rounded-full border-[1.5px] border-tool-ink text-tool-ink">
              <Icon name="check" size={11} stroke={2.6} />
            </span>
          ) : todo.status === "in_progress" ? (
            <span className="flex size-[18px] shrink-0 items-center justify-center">{live ? <Spinner size={16} /> : <span className="size-[16px] rounded-full border-[1.5px] border-ink border-r-transparent" />}</span>
          ) : (
            <span className="size-[18px] shrink-0 rounded-full border-[1.5px] border-ink" />
          )}
          <span className={cx("min-w-0 text-[13px] leading-[1.45] break-words", todo.status === "completed" ? "text-tool-ink" : todo.status === "in_progress" ? "font-medium text-ink" : "text-ink")}>{todo.content}</span>
        </div>
      ))}
    </div>
  );
}

/** Above the composer while the latest plan is out of view: a ring with how far along it is, and which step of how many. */
export function PlanPill({ todos, onClick }: { todos: Todo[]; onClick: () => void }) {
  const p = planProgress(todos);
  const r = 7;
  const c = 2 * Math.PI * r;
  return (
    <div className="flex justify-center">
      <button type="button" onClick={onClick} className="flex items-center gap-2 rounded-full border border-line bg-surface px-[14px] py-[6px] whitespace-nowrap transition-opacity select-none hover:opacity-80" data-plan-pill style={{ animation: "fade-in 150ms ease-out both" }}>
        {p.allDone ? (
          <Icon name="check" size={14} stroke={2.4} className="shrink-0 text-ink" />
        ) : (
          <svg width={16} height={16} viewBox="0 0 16 16" aria-hidden className="shrink-0">
            <circle cx={8} cy={8} r={r} fill="none" stroke="var(--tool-ink)" strokeWidth={2} opacity={0.45} />
            <circle cx={8} cy={8} r={r} fill="none" stroke="var(--ink)" strokeWidth={2} strokeLinecap="round" strokeDasharray={c} strokeDashoffset={c * (1 - (p.total ? p.step / p.total : 0))} transform="rotate(-90 8 8)" />
          </svg>
        )}
        <span className="text-[13px] leading-none text-ink tabular-nums">{planStepText(p)}</span>
      </button>
    </div>
  );
}
