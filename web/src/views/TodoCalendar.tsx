// The to-dos as a calendar (#2029, docs/assistant/todos.md "Calendar"): a day, a week or a month of those with a due
// date, drawn as the list draws them (state, overdue, done, priority, reminder). Clicking one opens it, clicking the
// empty axis makes a new one due there; those without a due date are counted under it, a click from the list. The
// page (Todos.tsx) keeps the mode and the day in the address. What goes where: lib/todo-calendar.ts.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from "react";
import { t } from "../i18n";
import {
  calendarDays,
  calendarEntries,
  calendarRange,
  calendarTitle,
  entriesOn,
  firstHour,
  fitDay,
  lanes,
  minutesInto,
  sameDay,
  slotAt,
  startOfDay,
  stepAnchor,
  undatedCount,
  weekdayNames,
  weekStartDay,
  type CalendarEntry,
  type CalendarMode,
} from "../lib/todo-calendar";
import { TODO_STATUS_INFO, type SessionLookup, type Todo } from "../lib/todos";
import { PHONE, useMedia } from "../lib/viewport";
import { Button, cx, useNow } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { Segmented } from "./assistant/parts";

/** An hour on the axis, in px: a to-do's block (BLOCK px high) takes most of one. */
const HOUR = 48;
const BLOCK = 44;
const BLOCK_MINUTES = (BLOCK / HOUR) * 60;
const GUTTER = 56;
/** Room the axis opens with above its first hour: that hour's label sits across its line and would be cut. */
const LABEL_ROOM = HOUR / 4;
/** Lines a day of the month shows: its entries, or one fewer and "N more". */
const MONTH_LINES = 3;

/** Where a new to-do made on the calendar is due: at `at`, or all day on `day` without it. */
export interface CalendarSlot {
  day: number;
  at?: number;
}

/** An entry's colors: its state's, red once overdue (before the state: what is late is what to see first). */
function look(e: CalendarEntry): { bar: string; ground: string; text: string } {
  if (e.overdue) return { bar: "bg-red", ground: "bg-red/10", text: "text-red" };
  switch (TODO_STATUS_INFO[e.status].tone) {
    case "red":
      return { bar: "bg-red", ground: "bg-red/10", text: "text-red" };
    case "green":
      return { bar: "bg-green", ground: "bg-green-tint", text: "text-green" };
    case "amber":
      return { bar: "bg-orange", ground: "bg-orange-tint", text: "text-orange" };
    default:
      return { bar: "bg-ink-3", ground: "bg-hover", text: "text-ink-3" };
  }
}

const clock = (ms: number) => new Date(ms).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });

function timeText(e: CalendarEntry): string {
  const at = clock(e.start);
  return e.overdue ? t("todos.calendar_was_due", { when: at }) : at;
}

function spoken(e: CalendarEntry): string {
  return [e.title, e.statusLabel, e.allDay ? t("todos.calendar_all_day") : timeText(e)].join(", ");
}

function Marks({ e }: { e: CalendarEntry }) {
  return (
    <>
      {e.high && <Icon name="flag" size={11} className="shrink-0 text-orange" />}
      {e.reminding && <Icon name="bell" size={11} className="shrink-0 text-ink-3" />}
    </>
  );
}

/** An entry in a row of its own: the all-day row, a day of the month. */
function Chip({ e, onOpen, time, compact }: { e: CalendarEntry; onOpen: (id: string) => void; time?: boolean; compact?: boolean }) {
  const l = look(e);
  return (
    <button
      type="button"
      data-testid="todo-calendar-entry"
      data-todo-id={e.todoId}
      aria-label={spoken(e)}
      onClick={(ev) => {
        ev.stopPropagation();
        onOpen(e.todoId);
      }}
      className={cx(
        "relative flex w-full min-w-0 items-center gap-1 overflow-hidden rounded-chip text-left hover:brightness-95",
        compact ? "h-[18px] pr-1 pl-2 text-[11px]" : "h-[22px] pr-1.5 pl-2.5 text-[12px]",
        l.ground,
        e.done && "opacity-50",
      )}
    >
      <span aria-hidden className={cx("absolute inset-y-0 left-0", compact ? "w-0.5" : "w-[3px]", l.bar)} />
      {time && !e.allDay && <span className="shrink-0 text-ink-3 tabular-nums">{clock(e.start)}</span>}
      <span className={cx("truncate", e.overdue ? "text-red" : "text-ink", e.done && "line-through")}>{e.title}</span>
      {!compact && <Marks e={e} />}
    </button>
  );
}

/** A to-do on the time axis: its top edge is when it is due. */
function Block({ e, onOpen, selected }: { e: CalendarEntry; onOpen: (id: string) => void; selected: boolean }) {
  const l = look(e);
  return (
    <button
      type="button"
      data-testid="todo-calendar-entry"
      data-todo-id={e.todoId}
      aria-label={spoken(e)}
      onClick={(ev) => {
        ev.stopPropagation();
        onOpen(e.todoId);
      }}
      className={cx(
        "relative flex h-full w-full flex-col overflow-hidden rounded-chip py-1 pr-1.5 pl-2.5 text-left hover:brightness-95",
        l.ground,
        e.done && "opacity-50",
        selected && "ring-2 ring-accent",
      )}
    >
      <span aria-hidden className={cx("absolute inset-y-0 left-0 w-[3px]", l.bar)} />
      <span className="flex min-w-0 items-center gap-1">
        <span className={cx("truncate text-[12.5px] font-semibold text-ink", e.done && "line-through")}>{e.title}</span>
        <Marks e={e} />
      </span>
      <span className="truncate text-[11px] text-ink-3">
        <span className={e.overdue ? "text-red" : l.text}>{e.statusLabel}</span>
        <span className={e.overdue ? "text-red" : undefined}> · {timeText(e)}</span>
      </span>
    </button>
  );
}

/** A day or a week on a time axis: the all-day row, then the hours, each to-do at its due time, side by side where they
 * overlap; a red line at the moment. Clicking the empty axis offers a new to-do due there. */
function Axis({
  days,
  entries,
  now,
  showHeader,
  selectedId,
  onOpen,
  onAdd,
  onPickDay,
}: {
  days: number[];
  entries: CalendarEntry[];
  now: number;
  showHeader: boolean;
  selectedId?: string;
  onOpen: (id: string) => void;
  onAdd: (slot: CalendarSlot) => void;
  onPickDay: (day: number) => void;
}) {
  const scroller = useRef<HTMLDivElement>(null);
  const names = weekdayNames(new Date(days[0]).getDay(), false);
  const timed = entries.filter((e) => !e.allDay);
  const today = days.find((d) => sameDay(d, now));
  // Open where the day's to-dos and the moment are in sight; again whenever the days change.
  const start = firstHour(days, timed, now);
  useLayoutEffect(() => {
    if (scroller.current) scroller.current.scrollTop = Math.max(0, start * HOUR - LABEL_ROOM);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [days[0], days.length]);
  const cols = { gridTemplateColumns: `${GUTTER}px repeat(${days.length}, minmax(0, 1fr))` };
  const nowY = today !== undefined ? (minutesInto(now, today) / 60) * HOUR : undefined;

  return (
    <div className="flex h-full flex-col" data-testid="todo-calendar-axis">
      {showHeader && (
        <div className="grid shrink-0 border-b border-line" style={cols}>
          <span />
          {days.map((d, i) => {
            const isToday = sameDay(d, now);
            return (
              <button
                key={d}
                type="button"
                data-testid="todo-calendar-day-header"
                onClick={() => onPickDay(d)}
                className="flex items-center gap-1.5 border-l border-line px-2.5 py-2 text-left hover:bg-hover"
              >
                <span className="text-[12px] text-ink-3">{names[i]}</span>
                <span className={cx("inline-flex h-6 min-w-6 items-center justify-center rounded-full px-1 text-[14px] tabular-nums", isToday ? "bg-accent font-semibold text-white" : "text-ink")}>
                  {new Date(d).getDate()}
                </span>
              </button>
            );
          })}
        </div>
      )}
      <div className="grid shrink-0 border-b border-line-strong" style={cols}>
        <span className="pt-1.5 pr-2 text-right text-[11px] text-ink-3">{t("todos.calendar_all_day")}</span>
        {days.map((d) => (
          <div
            key={d}
            data-testid="todo-calendar-all-day"
            onClick={() => onAdd({ day: d })}
            className="flex min-h-7 min-w-0 cursor-cell flex-col gap-0.5 border-l border-line p-1"
          >
            {entriesOn(d, entries)
              .filter((e) => e.allDay)
              .map((e) => (
                <Chip key={e.id} e={e} onOpen={onOpen} />
              ))}
          </div>
        ))}
      </div>
      <div ref={scroller} className="relative min-h-0 flex-1 overflow-y-auto">
        <div className="relative grid" style={{ ...cols, height: 24 * HOUR }}>
          <div className="relative">
            {Array.from({ length: 23 }, (_, i) => i + 1)
              .filter((h) => nowY === undefined || Math.abs(h * HOUR - nowY) > HOUR / 4)
              .map((h) => (
                <span key={h} className="absolute right-2 -translate-y-1/2 text-[11px] text-ink-3 tabular-nums" style={{ top: h * HOUR }}>
                  {clock(new Date(2023, 0, 1, h).getTime())}
                </span>
              ))}
            {nowY !== undefined && (
              <span className="absolute right-2 -translate-y-1/2 text-[11px] font-bold text-red tabular-nums" style={{ top: nowY }}>
                {clock(now)}
              </span>
            )}
          </div>
          {days.map((d) => {
            const mine = entriesOn(d, timed);
            return (
              <div
                key={d}
                data-testid="todo-calendar-column"
                className="relative cursor-cell border-l border-line"
                style={{ backgroundImage: `repeating-linear-gradient(to bottom, var(--line) 0 1px, transparent 1px ${HOUR}px)` }}
                onClick={(ev) => {
                  const box = ev.currentTarget.getBoundingClientRect();
                  onAdd({ day: d, at: slotAt(d, ((ev.clientY - box.top) / HOUR) * 60) });
                }}
              >
                {lanes(mine, BLOCK_MINUTES).map((p) => (
                  <BlockAt key={p.entry.id} placed={p} day={d} onOpen={onOpen} selected={p.entry.todoId === selectedId} />
                ))}
                {sameDay(d, now) && nowY !== undefined && (
                  <div aria-hidden className="pointer-events-none absolute right-0 left-0 border-t-[1.5px] border-red" style={{ top: nowY }}>
                    <span className="absolute -top-[4.5px] -left-1 size-2 rounded-full bg-red" />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function BlockAt({ placed, day, onOpen, selected }: { placed: { entry: CalendarEntry; lane: number; lanes: number }; day: number; onOpen: (id: string) => void; selected: boolean }) {
  const top = (minutesInto(placed.entry.start, day) / 60) * HOUR;
  const width = 100 / placed.lanes;
  return (
    <div className="absolute" style={{ top: top + 1, height: BLOCK - 2, left: `calc(${placed.lane * width}% + 3px)`, width: `calc(${width}% - 5px)` }}>
      <Block e={placed.entry} onOpen={onOpen} selected={selected} />
    </div>
  );
}

/** A month: the weeks it touches, each day with its first entries and how many more; clicking a day opens it. */
function Month({
  anchor,
  first,
  entries,
  now,
  compact,
  onOpen,
  onPickDay,
}: {
  anchor: number;
  first: number;
  entries: CalendarEntry[];
  now: number;
  compact: boolean;
  onOpen: (id: string) => void;
  onPickDay: (day: number) => void;
}) {
  const days = calendarDays("month", anchor, first);
  const month = new Date(anchor).getMonth();
  const names = weekdayNames(first, compact);
  return (
    <div className="flex h-full flex-col" data-testid="todo-calendar-month">
      <div className="grid shrink-0 grid-cols-7 border-b border-line">
        {names.map((n, i) => (
          <span key={i} className={cx("py-2 text-[12px] text-ink-3", compact ? "text-center" : "px-2.5")}>
            {n}
          </span>
        ))}
      </div>
      <div className="grid min-h-0 flex-1 grid-cols-7" style={{ gridTemplateRows: `repeat(${days.length / 7}, minmax(0, 1fr))` }}>
        {days.map((d, i) => {
          const inMonth = new Date(d).getMonth() === month;
          const isToday = sameDay(d, now);
          const fit = fitDay(entriesOn(d, entries), MONTH_LINES);
          const date = new Date(d);
          return (
            <div
              key={d}
              data-testid="todo-calendar-month-day"
              onClick={() => onPickDay(d)}
              className={cx(
                "flex min-h-0 min-w-0 cursor-pointer flex-col gap-0.5 overflow-hidden p-1 hover:bg-hover",
                i >= 7 && "border-t border-line",
                i % 7 > 0 && "border-l border-line",
                !inMonth && "bg-inset",
                isToday && "bg-accent/5",
              )}
            >
              <span className={cx("mb-0.5 flex", compact && "justify-center")}>
                <span
                  className={cx(
                    "inline-flex h-[22px] min-w-[22px] items-center justify-center rounded-full px-1 text-[12.5px] tabular-nums",
                    isToday ? "bg-accent font-semibold text-white" : inMonth ? "text-ink" : "text-ink-3",
                  )}
                >
                  {date.getDate() === 1 && !compact ? formatMonthDay(d) : date.getDate()}
                </span>
              </span>
              {fit.shown.map((e) => (
                <Chip key={e.id} e={e} onOpen={compact ? () => onPickDay(d) : onOpen} time={!compact} compact={compact} />
              ))}
              {fit.more > 0 && (
                <span className="truncate pl-1 text-[11.5px] text-ink-3">{compact ? `+${fit.more}` : t("todos.calendar_more", { count: fit.more })}</span>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}

function formatMonthDay(ms: number): string {
  return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function TodoCalendar({
  todos,
  lookup,
  mode,
  anchor,
  showDone,
  selectedId,
  onMode,
  onAnchor,
  onShowDone,
  onOpen,
  onAdd,
  onShowList,
}: {
  todos: readonly Todo[];
  lookup: SessionLookup;
  mode: CalendarMode;
  anchor: number;
  showDone: boolean;
  selectedId?: string;
  onMode: (mode: CalendarMode, day?: number) => void;
  onAnchor: (day: number) => void;
  onShowDone: (on: boolean) => void;
  onOpen: (id: string) => void;
  onAdd: (slot: CalendarSlot) => void;
  onShowList: () => void;
}) {
  const phone = useMedia(PHONE);
  const now = useNow(60_000);
  const first = useMemo(() => weekStartDay(), []);
  // A phone's width has no room for seven columns: its week is the day.
  const shown: CalendarMode = phone && mode === "week" ? "day" : mode;
  const [from, to] = calendarRange(shown, anchor, first);
  const entries = useMemo(() => calendarEntries(todos, { lookup, showDone, from, to, now }), [todos, lookup, showDone, from, to, now]);
  const undated = useMemo(() => undatedCount(todos), [todos]);
  const step = useCallback((n: number) => onAnchor(stepAnchor(anchor, shown, n)), [anchor, shown, onAnchor]);

  // ← → turn the page and T comes back to today, while nothing is being typed.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (e.metaKey || e.ctrlKey || e.altKey || (el && (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)))) return;
      if (e.key === "ArrowLeft") step(-1);
      else if (e.key === "ArrowRight") step(1);
      else if (e.key === "t" || e.key === "T") onAnchor(startOfDay(Date.now()));
      else return;
      e.preventDefault();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [step, onAnchor]);

  const modes: CalendarMode[] = phone ? ["day", "month"] : ["day", "week", "month"];
  const modeName: Record<CalendarMode, string> = { day: t("todos.calendar_day"), week: t("todos.calendar_week"), month: t("todos.calendar_month") };

  return (
    <div className="flex min-h-0 flex-1 flex-col" data-testid="todo-calendar" data-mode={shown}>
      <div className="flex shrink-0 flex-wrap items-center gap-2 px-3 py-3 sm:px-6">
        <Button tone="secondary" icon="chevronLeft" onClick={() => step(-1)} className="h-8 px-2" title={t("todos.calendar_previous")}>
          {null}
        </Button>
        <Button tone="secondary" onClick={() => onAnchor(startOfDay(Date.now()))} className="h-8 text-[12.5px]">
          {t("todos.calendar_today")}
        </Button>
        <Button tone="secondary" icon="chevronRight" onClick={() => step(1)} className="h-8 px-2" title={t("todos.calendar_next")}>
          {null}
        </Button>
        <h2 data-testid="todo-calendar-title" className="ml-1 truncate text-[16px] font-semibold text-ink">
          {calendarTitle(shown, anchor, first)}
        </h2>
        <div className="ml-auto flex items-center gap-3">
          <Segmented label={t("todos.calendar_mode")} value={shown} options={modes.map((v) => ({ v, label: modeName[v] }))} onChange={(v) => onMode(v)} compact />
          <label className="flex cursor-pointer items-center gap-1.5 text-[12.5px] whitespace-nowrap text-ink-2">
            <input type="checkbox" data-testid="todo-calendar-show-done" checked={showDone} onChange={(e) => onShowDone(e.target.checked)} className="accent-accent" />
            {t("todos.calendar_show_done")}
          </label>
        </div>
      </div>
      <div className="mx-3 min-h-0 flex-1 overflow-hidden rounded-card bg-surface shadow-card sm:mx-6">
        {shown === "month" ? (
          <Month anchor={anchor} first={first} entries={entries} now={now} compact={phone} onOpen={onOpen} onPickDay={(d) => onMode("day", d)} />
        ) : (
          <Axis
            days={shown === "day" ? [startOfDay(anchor)] : calendarDays("week", anchor, first)}
            entries={entries}
            now={now}
            showHeader={shown === "week"}
            selectedId={selectedId}
            onOpen={onOpen}
            onAdd={onAdd}
            onPickDay={(d) => onMode("day", d)}
          />
        )}
      </div>
      <div className="flex h-11 shrink-0 items-center gap-2 px-4 text-[12.5px] text-ink-3 sm:px-7">
        {undated > 0 && (
          <>
            <Icon name="list" size={13} />
            <span>{t("todos.calendar_undated", { count: undated })}</span>
            <button type="button" data-testid="todo-calendar-undated" onClick={onShowList} className="font-medium text-accent-ink hover:underline">
              {t("todos.calendar_see_list")}
            </button>
          </>
        )}
      </div>
    </div>
  );
}
