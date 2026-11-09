// The sidebar's bottom (#2432, docs/server/usage-windows.md): the two fullest subscription accounts as thin bars; a click opens
// a popover with every window of every account, its reset countdown, when it was sampled and whether use is ahead of or
// behind an even pace. Hidden entirely while the hub has no data.
import { useEffect, useRef, useState } from "react";
import { t as tr } from "../i18n";
import { fullest, isWarn, liveAccounts, pace, PACE_LABEL, planNote, problemText, resetLabel, sampleAge, sourceNames, topAccounts, useUsageWindows, windowKey, type PaceState, type UsageAccount, type UsageWindow } from "../lib/usage-windows";
import { cx, EASE } from "../ui/atoms";

const percent = (p: number) => Math.min(100, Math.max(0, p));
const shown = (p: number) => `${Math.round(p)}%`;
const fill = (p: number) => (isWarn(p) ? "bg-orange" : "bg-accent");
const PACE_TEXT: Record<PaceState, string> = { over: "text-orange", under: "text-green", on: "text-ink-3" };

/** A bar: the track, the used part (caution colour from 80%) and, with `expected`, a tick where an even pace would be. */
export function Meter({ used, expected, label, tall }: { used: number; expected?: number; label: string; tall?: boolean }) {
  return (
    <div role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(percent(used))} className={cx("relative w-full rounded-full bg-inset", tall ? "h-1.5" : "h-1")}>
      <div className={cx("h-full rounded-full transition-[width] duration-300", fill(used))} style={{ width: `${percent(used)}%` }} />
      {expected !== undefined && <span aria-hidden data-pace-tick className="absolute -top-0.5 -bottom-0.5 w-0.5 -translate-x-1/2 rounded-full bg-ink-2" style={{ left: `${percent(expected)}%` }} />}
    </div>
  );
}

/** The sidebar's strip. `accounts` as the hub gave them; renders nothing when none is live. */
export function UsageBars({ accounts, now = Date.now() }: { accounts: UsageAccount[]; now?: number }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const live = liveAccounts(accounts, now);
  const top = topAccounts(live);

  useEffect(() => {
    if (!open) return;
    const down = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const key = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    addEventListener("pointerdown", down, true);
    addEventListener("keydown", key);
    return () => {
      removeEventListener("pointerdown", down, true);
      removeEventListener("keydown", key);
    };
  }, [open]);

  if (!top.length) return null;
  return (
    <div ref={ref} className="relative shrink-0 px-2.5 pb-2.5 pt-1" data-usage-bars>
      {open && <UsagePopover accounts={live} />}
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="dialog"
        aria-expanded={open}
        title={tr("usage.title")}
        className="touch-hit flex w-full flex-col gap-1.5 rounded-control px-2.5 py-2 text-left shadow-hairline transition-colors hover:bg-hover"
      >
        {top.map((a) => {
          const used = fullest(a);
          const from = sourceNames(a);
          const title = [from.length ? tr("usage.sources_title", { sources: from.join(" · ") }) : "", a.stale ? tr("usage.stale_title") : ""].filter(Boolean).join(" · ");
          return (
            <span key={a.id} className={cx("flex flex-col gap-1", a.stale && "opacity-60")} title={title || undefined}>
              <span className="flex items-baseline gap-2">
                <span className="min-w-0 flex-1 truncate text-[11.5px] text-ink-3">{a.name}</span>
                <span className={cx("shrink-0 text-[11.5px] tabular-nums", isWarn(used) ? "text-orange" : "text-ink-2")}>{shown(used)}</span>
              </span>
              <Meter used={used} label={tr("usage.used_label", { name: a.name, percent: shown(used) })} />
            </span>
          );
        })}
      </button>
    </div>
  );
}

/** Every live account, window by window. `now` ticks while it is open so the countdowns and sample ages keep up. */
export function UsagePopover({ accounts, now: fixed }: { accounts: UsageAccount[]; now?: number }) {
  const [tick, setTick] = useState(() => Date.now());
  useEffect(() => {
    if (fixed !== undefined) return;
    const t = setInterval(() => setTick(Date.now()), 1000);
    return () => clearInterval(t);
  }, [fixed]);
  const now = fixed ?? tick;
  return (
    <div role="dialog" aria-label={tr("usage.title")} className="absolute bottom-full left-2.5 right-2.5 z-20 mb-1.5 max-h-[60vh] overflow-y-auto rounded-card bg-surface p-3 shadow-overlay" style={{ animation: `pop-in 160ms ${EASE} both`, transformOrigin: "bottom left" }}>
      <div className="flex flex-col gap-3">
        {accounts.map((a) => (
          <AccountSection key={a.id} a={a} now={now} />
        ))}
      </div>
    </div>
  );
}

function AccountSection({ a, now }: { a: UsageAccount; now: number }) {
  const from = sourceNames(a);
  const plan = planNote(a);
  const problem = problemText(a);
  return (
    <section aria-label={a.name} className={cx("flex flex-col gap-2", a.stale && "opacity-70")}>
      <header className="flex flex-col gap-0.5">
        <div className="flex items-baseline gap-2">
          <h3 className="min-w-0 flex-1 truncate text-[12.5px] font-medium text-ink">{a.name}</h3>
          <span className="shrink-0 whitespace-nowrap text-[11px] text-ink-3">{a.stale ? tr("usage.stale_age", { age: sampleAge(a.sampled_at, now) }) : sampleAge(a.sampled_at, now)}</span>
        </div>
        {(from.length > 0 || plan) && (
          <div data-usage-source className="flex min-w-0 items-baseline gap-1.5 text-[11px] text-ink-3">
            {plan && <span className="shrink-0 whitespace-nowrap">{plan}</span>}
            {plan && from.length > 0 && <span aria-hidden>·</span>}
            <span className="min-w-0 truncate" title={tr("usage.sources_title", { sources: from.join(" · ") })}>{from.join(" · ")}</span>
          </div>
        )}
        {problem && <div className="text-[11px] leading-snug text-orange">{problem}</div>}
      </header>
      {a.windows.map((w) => (
        <WindowRow key={windowKey(w)} name={a.name} w={w} now={now} />
      ))}
    </section>
  );
}

function WindowRow({ name, w, now }: { name: string; w: UsageWindow; now: number }) {
  const p = pace(w, now);
  return (
    <div className="flex flex-col gap-1">
      {w.model && (
        <span data-usage-model className="truncate text-[11px] text-ink-3" title={w.model}>
          {w.model}
        </span>
      )}
      <div className="flex items-baseline gap-2 text-[11.5px]">
        <span className="w-7 shrink-0 text-ink-2">{w.label}</span>
        <span className={cx("shrink-0 tabular-nums", isWarn(w.used_percent) ? "text-orange" : "text-ink")}>{shown(w.used_percent)}</span>
        {p && p.state !== "on" && <span className={cx("shrink-0 whitespace-nowrap", PACE_TEXT[p.state])}>{PACE_LABEL[p.state]}</span>}
        <span className="min-w-0 flex-1 truncate text-right text-ink-3">{resetLabel(w.resets_at, now)}</span>
      </div>
      <Meter used={w.used_percent} expected={p?.expected} label={w.model ? tr("usage.window_model_label", { name, window: w.label, model: w.model, percent: shown(w.used_percent) }) : tr("usage.window_used_label", { name, window: w.label, percent: shown(w.used_percent) })} tall />
    </div>
  );
}

/** What the sidebar mounts. */
export function SidebarUsage() {
  return <UsageBars accounts={useUsageWindows()} />;
}
