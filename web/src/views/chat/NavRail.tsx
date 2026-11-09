// The question navigation rail (#1739, docs/web/web.md "Question rail"): in a wide chat with several of your questions
// loaded, a slim column of ticks in the margin beside the chat — one per question. Hovering (or focusing) a tick
// previews the question and the start of its answer, clicking jumps to that turn, and the tick of the turn being read
// is the long one. It never shows on a narrow chat area, a short conversation, a cramped viewport or a touch screen
// (its ticks and previews are a precise pointer's), so those look exactly as before; which turns become ticks and
// where they fit is lib/nav-rail.ts (pure, tested there).
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { NAV_RAIL_COLUMN, NAV_RAIL_MIN_GUTTER, NAV_RAIL_MIN_HEIGHT, NAV_RAIL_MIN_TICKS, hasNavRailRoom, pickActiveTick, planNavRailTicks, type NavRailTick } from "../../lib/nav-rail";
import { useFinePointer } from "../../lib/viewport";
import { cx } from "../../ui/atoms";
import { t } from "../../i18n";

/** The rail's vertical padding (inside the chat area's height). */
const PAD_Y = 24;
/** How far under the scroller's top a line counts as read (past the jump's landing offset, so a jump that just landed
 *  keeps its own tick as the active one). */
const ACTIVE_FUDGE = 40;
/** Pointer activity this close to the rail wakes it (the ticks sit in that margin, which on a wide screen is far
 *  from the chat's left edge). */
const WAKE_GUTTER = 48;
/** Quiet for this long and the rail dims (it stays clickable); scrolling, the pointer or the focus wakes it. */
const IDLE_MS = 2000;
/** A clicked tick shows as active until the scroll lands (or this passes) even if the geometry has not caught up. */
const PENDING_MS = 3000;

/** The label a tick stands for: its question's words, or a count when it is only attachments. */
function labelOf(tick: NavRailTick): string {
  return tick.preview || (tick.attachmentsOnly ? t("chat-navrail.attachments", { count: tick.attachmentsOnly }) : "");
}

/** One wheel notch's pixels by the event's unit (Firefox sends lines or pages, not pixels; PreviewPanel's scale). */
function wheelPixels(delta: number, mode: number, page: number): number {
  return delta * (mode === 1 ? 16 : mode === 2 ? page : 1);
}

export function NavRail({ ticks, scroller, onJump, reset }: { ticks: NavRailTick[]; scroller: HTMLDivElement | null; onJump: (lineId: string) => void; /** mid/sid: another chat, everything starts over. */ reset: string }) {
  const [room, setRoom] = useState(false);
  const [height, setHeight] = useState(0);
  /** The rail's left edge in the chat area: beside the column as it centers in the scroller's content box. */
  const [left, setLeft] = useState(0);
  const [active, setActive] = useState<number | null>(null);
  const [pending, setPending] = useState<number | null>(null);
  const [preview, setPreview] = useState<number | null>(null);
  const [idle, setIdle] = useState(false);

  const railRef = useRef<HTMLElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  const raf = useRef(0);
  const idleTimer = useRef(0);
  const pendingTimer = useRef(0);
  // the rail's on-screen strip for the pointer-wake test, refreshed on measure (no layout read per pointermove)
  const railX = useRef<{ left: number; right: number } | null>(null);
  // what holds the dim off: the pointer resting on the rail, or the focus in it (its preview card would dim with it)
  const hovering = useRef(false);
  const focused = useRef(false);
  // the drawn lines by id, rebuilt only when the ticks change (not on every scroll frame)
  const anchors = useRef<{ of: NavRailTick[] | null; map: Map<string, HTMLElement> }>({ of: null, map: new Map() });
  const view = useRef(scroller);
  view.current = scroller;
  const list = useRef(ticks);
  list.current = ticks;

  const wake = useCallback(() => {
    setIdle(false);
    clearTimeout(idleTimer.current);
    idleTimer.current = window.setTimeout(() => {
      idleTimer.current = 0;
      // held while the pointer or the focus is on the rail: re-arm instead of dimming under it
      if (hovering.current || focused.current) return wake();
      setIdle(true);
    }, IDLE_MS);
  }, []);

  // its ticks and their hover previews are a precise pointer's; a touch screen keeps the margin empty
  const fine = useFinePointer();
  const measure = useCallback(() => {
    raf.current = 0;
    const sc = view.current;
    if (!sc) return;
    // Nothing to draw, so no layout reads. A short chat writes no state (mounting it draws no extra frame). A touch
    // screen drops whatever a pointer had measured and does not read the chat on this frame or the next scroll.
    if (!fine) {
      // Drop what a precise pointer had measured. Leaving room/left/height frozen would paint the rail from that old
      // geometry for a frame when a pointer comes back (a trackpad undocked, the window rotated, the trackpad back).
      // A hover or a pending jump dies with the rail: unmounting does not fire mouseleave, so those refs would stick
      // and the rail would come back already previewing, and never dim.
      railX.current = null;
      hovering.current = false;
      focused.current = false;
      setRoom(false);
      setActive((x) => (x === null ? x : null));
      setPreview((x) => (x === null ? x : null));
      setPending((x) => (x === null ? x : null));
      return;
    }
    if (list.current.length < NAV_RAIL_MIN_TICKS) {
      // The pointer-wake band goes with it; a strip rect left from an earlier rail would wake an empty margin.
      railX.current = null;
      setActive((x) => (x === null ? x : null));
      return;
    }
    // clientWidth: the column centers in the content box, and a classic scrollbar is not free margin
    const fits = hasNavRailRoom(sc.clientWidth);
    setRoom(fits);
    setLeft((sc.clientWidth - NAV_RAIL_COLUMN) / 2 - NAV_RAIL_MIN_GUTTER);
    setHeight(Math.max(0, sc.clientHeight - PAD_Y * 2));
    const strip = railRef.current?.getBoundingClientRect();
    railX.current = strip ? { left: strip.left, right: strip.right } : null;
    const now = list.current;
    if (!fits) {
      setActive((x) => (x === null ? x : null));
      return;
    }
    if (anchors.current.of !== now) {
      const map = new Map<string, HTMLElement>();
      for (const el of sc.querySelectorAll<HTMLElement>("[data-line]")) map.set(el.dataset.line ?? "", el);
      anchors.current = { of: now, map };
    }
    // a rect read per tick (positions move with the scroll; the elements themselves do not)
    const byId = anchors.current.map;
    setActive(pickActiveTick(now.length, sc.getBoundingClientRect().top + ACTIVE_FUDGE, (i) => byId.get(now[i].id)?.firstElementChild?.getBoundingClientRect().top ?? null));
  }, [fine]);

  const schedule = useCallback(() => {
    if (raf.current === 0) raf.current = requestAnimationFrame(measure);
  }, [measure]);

  // scrolling, resizing and the chat's own changes (a turn answered, history loaded) all re-measure; so does the rail
  // first drawing itself (its own strip, for the pointer-wake test, only exists once it is on the page)
  useEffect(() => {
    schedule();
  }, [ticks, scroller, room, schedule]);

  useEffect(() => {
    const sc = view.current;
    // No precise pointer: nothing to draw, so a scroll must not measure or arm the idle timer. A phone's long chat
    // would otherwise reset that timer on every scroll event and commit again when it fired. A pointer attached
    // later re-subscribes, and the effect above measures the chat as it is then.
    if (!sc || !fine) return;
    const onScroll = () => {
      wake();
      schedule();
    };
    const onMove = (e: PointerEvent) => {
      const strip = railX.current;
      if (strip && e.clientX >= strip.left - WAKE_GUTTER && e.clientX <= strip.right + WAKE_GUTTER) wake();
    };
    sc.addEventListener("scroll", onScroll, { passive: true });
    sc.addEventListener("pointermove", onMove, { passive: true });
    const ro = new ResizeObserver(schedule);
    ro.observe(sc);
    return () => {
      sc.removeEventListener("scroll", onScroll);
      sc.removeEventListener("pointermove", onMove);
      ro.disconnect();
      if (raf.current) cancelAnimationFrame(raf.current);
      raf.current = 0;
    };
  }, [scroller, schedule, wake, fine]);

  // another chat: no leftover active tick, hover or pending jump; show itself once as the where-you-are hint
  useEffect(() => {
    setActive(null);
    setPending(null);
    setPreview(null);
    wake();
    return () => {
      clearTimeout(idleTimer.current);
      clearTimeout(pendingTimer.current);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- reset only
  }, [reset]);

  // the scroll of a jump landed: the geometry's word takes over from the optimistic tick
  useEffect(() => {
    if (pending === null || pending !== active) return;
    setPending(null);
    clearTimeout(pendingTimer.current);
  }, [pending, active]);

  useEffect(() => () => {
    clearTimeout(idleTimer.current);
    clearTimeout(pendingTimer.current);
    if (raf.current) cancelAnimationFrame(raf.current);
  }, []);

  const jump = (i: number, id: string) => {
    setPending(i);
    clearTimeout(pendingTimer.current);
    pendingTimer.current = window.setTimeout(() => setPending(null), PENDING_MS);
    onJump(id);
  };

  // where the preview card sits: by its tick's button (so a re-planned rail re-seats it), kept inside the rail — both
  // before paint, so it never flashes at the wrong place
  useLayoutEffect(() => {
    const card = cardRef.current;
    const rail = railRef.current;
    if (!card || !rail || preview === null) return;
    const btn = rail.querySelector<HTMLElement>(`[data-tick="${preview}"]`);
    if (!btn) return setPreview(null); // its tick fell out of the newest run: no card for it
    const h = card.offsetHeight;
    const y = btn.getBoundingClientRect().top - rail.getBoundingClientRect().top + btn.offsetHeight / 2 - h / 2;
    card.style.top = `${Math.max(0, Math.min(y, rail.clientHeight - h))}px`;
  }, [preview, room, height, ticks]);

  if (!fine || !room || ticks.length < NAV_RAIL_MIN_TICKS || height < NAV_RAIL_MIN_HEIGHT) return null;

  const plan = planNavRailTicks(ticks.length, height);
  const shown = ticks.slice(plan.start);
  const at = preview ?? -1;
  const current = pending ?? active;
  const pick = (i: number) => {
    setPreview(i);
    wake();
  };
  const unpick = () => setPreview(null);

  return (
    <nav
      ref={railRef}
      aria-label={t("chat-navrail.rail")}
      data-nav-rail
      // the container takes no events (the margin stays free for text selection); the ticks and the card do
      className={cx("pointer-events-none absolute inset-y-0 z-10 flex w-8 flex-col justify-center gap-0 py-6 transition-opacity duration-200", idle ? "opacity-35" : "opacity-100")}
      style={{ left: `${left}px` }}
      onWheel={(e) => {
        // the ticks are a sibling of the scroller: the wheel on them would move nothing unless it is taken across
        const sc = view.current;
        if (sc) sc.scrollBy({ top: wheelPixels(e.deltaY, e.deltaMode, sc.clientHeight), left: wheelPixels(e.deltaX, e.deltaMode, sc.clientWidth) });
      }}
    >
      {plan.hidden > 0 && (
        <span
          aria-hidden
          data-tick="-1"
          className="pointer-events-auto flex items-center justify-end pr-0.5 text-[11px] leading-none text-ink-3"
          style={{ height: plan.pitch }}
          onMouseEnter={() => {
            hovering.current = true;
            pick(-1);
          }}
          onMouseLeave={() => {
            hovering.current = false;
            unpick();
          }}
        >
          …
        </span>
      )}
      {shown.map((tick, k) => {
        const i = plan.start + k;
        const label = labelOf(tick);
        const isCurrent = i === current;
        const near = at >= 0 ? Math.abs(i - at) : 99;
        return (
          <button
            key={tick.id}
            type="button"
            data-tick={i}
            className="pointer-events-auto flex items-center justify-end pr-0.5"
            style={{ height: plan.pitch }}
            aria-label={t("chat-navrail.jump", { index: i + 1, preview: label })}
            aria-current={isCurrent ? "true" : undefined}
            onClick={() => jump(i, tick.id)}
            onMouseEnter={() => {
              hovering.current = true;
              pick(i);
            }}
            onMouseLeave={() => {
              hovering.current = false;
              unpick();
            }}
            onFocus={() => {
              focused.current = true;
              pick(i);
            }}
            onBlur={() => {
              focused.current = false;
              unpick();
            }}
          >
            <span
              className={cx(
                "h-[2px] w-7 origin-right rounded-full transition-[transform,background-color] duration-150 ease-out",
                isCurrent || near === 0 ? "bg-ink" : near === 1 ? "bg-ink-2" : "bg-ink-3",
                near >= 2 && !isCurrent && "opacity-70",
              )}
              style={{ transform: `scaleX(${isCurrent || near === 0 ? 1 : near === 1 ? 0.85 : 0.68})` }}
            />
          </button>
        );
      })}
      {preview !== null && (
        <div ref={cardRef} data-nav-rail-preview className="pointer-events-none absolute top-0 left-10 z-20 w-[300px] max-w-[46vw]">
          <div className="rounded-[10px] border border-line bg-surface px-3 py-2 shadow-overlay">
            <p className="truncate text-[12.5px] font-medium text-ink">
              {preview < 0 ? t("chat-navrail.earlier", { count: plan.hidden }) : labelOf(ticks[preview])}
            </p>
            {preview >= 0 && ticks[preview].answerExcerpt && (
              <p className="mt-1 line-clamp-3 text-[12px] leading-relaxed text-ink-2">{ticks[preview].answerExcerpt}</p>
            )}
          </div>
        </div>
      )}
    </nav>
  );
}
