// Subscription rate-limit windows (#2432, docs/server/usage-windows.md): GET /api/v1/usage/windows lists the accounts the
// machines' agents are signed in to with how much of each window (5h, 7d …) is used. Pure helpers here (pace,
// countdown, sample age, what the sidebar shows); the bars and the popover are views/UsageBars.tsx.
import { useEffect, useState } from "react";
import { formatDate, formatRelative, t as tr } from "../i18n";
import { api, ApiError } from "./api";

export interface UsageWindow {
  label: string;
  window_minutes: number;
  used_percent: number;
  resets_at?: string;
  /** An allowance a provider keeps for one model next to the plan's own (#2595); absent for the plan's window. */
  model?: string;
}
export interface UsageAccount {
  id: string;
  provider: string;
  name: string;
  plan?: string;
  windows: UsageWindow[];
  sampled_at: string;
  stale?: boolean;
  /** When it turns stale if nothing newer comes (#1905): kept numbers are judged again against it (`liveAccounts`). */
  stale_at?: string;
  /** Where the numbers came from (#2595): an agent's files on one of the computers, and/or account-pool gateways by name. */
  local?: boolean;
  sources?: string[];
  /** The computers (by name) that read it (#1907); an older hub only says `local`. */
  machines?: string[];
  /** Why a gateway's numbers are old: "error" (it could not read this account just now) or, when its own last read
   *  failed, "rejected" (the key was turned away), "unreachable" or "failed" (docs/server/usage-windows.md). */
  problem?: string;
}

/** Samples older than this are dropped (a source that went quiet for days says nothing about now). */
export const SAMPLE_MAX_AGE_MS = 3 * 24 * 3600_000;
/** From this used percentage a bar turns to the caution colour. */
export const WARN_PERCENT = 80;
/** The pace is not judged in the first part of a window: a few minutes in, any use is "ahead". */
export const PACE_QUIET_FRACTION = 0.05;
/** How far (percentage points) the actual use may be from the expected before it is over / under. */
export const PACE_TOLERANCE = 5;
const POLL_MS = 60_000;

/** The window's length in minutes from its label ("5h", "7d", "45m"); the given minutes when the label is not one. */
export function windowMinutes(w: Pick<UsageWindow, "label" | "window_minutes">): number {
  const m = /^(\d+(?:\.\d+)?)\s*([mhdw])$/i.exec(w.label.trim());
  if (m) return Math.round(Number(m[1]) * { m: 1, h: 60, d: 1440, w: 10080 }[m[2].toLowerCase() as "m" | "h" | "d" | "w"]);
  return w.window_minutes > 0 ? w.window_minutes : 0;
}

export type PaceState = "over" | "under" | "on";
export interface Pace {
  /** Where use would be if it were spread evenly over the window, 0-100. */
  expected: number;
  state: PaceState;
}

/** How use compares with an even spread: expected% = elapsed / length; over when use is more than 5 points above it,
 *  under when more than 5 below. null with no reset time or length, and in the first 5% of the window. */
export function pace(w: UsageWindow, now = Date.now()): Pace | null {
  const total = windowMinutes(w) * 60_000;
  const resets = w.resets_at ? Date.parse(w.resets_at) : NaN;
  if (!(total > 0) || !Number.isFinite(resets)) return null;
  const elapsed = total - (resets - now);
  if (elapsed < 0 || elapsed > total) return null;
  if (elapsed / total < PACE_QUIET_FRACTION) return null;
  const expected = (elapsed / total) * 100;
  const diff = w.used_percent - expected;
  return { expected, state: diff > PACE_TOLERANCE ? "over" : diff < -PACE_TOLERANCE ? "under" : "on" };
}

export const PACE_LABEL: Record<PaceState, string> = { over: tr("usage.pace_over"), under: tr("usage.pace_under"), on: tr("usage.pace_on") };

type Countdown = { kind: "due" | "clock" | "span"; text: string };

/** When a window starts over: the span ("45m", "2d 3h"), the local clock time when that is today, or "due". null without a time. */
function countdownOf(resetsAt: string | undefined, now: number): Countdown | null {
  const at = resetsAt ? Date.parse(resetsAt) : NaN;
  if (!Number.isFinite(at)) return null;
  const ms = at - now;
  if (ms <= 0) return { kind: "due", text: tr("usage.due") };
  const mins = Math.ceil(ms / 60_000);
  if (mins < 60) return { kind: "span", text: tr("usage.minutes", { m: mins }) };
  if (new Date(at).toDateString() === new Date(now).toDateString()) return { kind: "clock", text: formatDate(at, { timeStyle: "short" }) };
  const hours = Math.floor(mins / 60);
  const d = Math.floor(hours / 24);
  return { kind: "span", text: d > 0 ? tr("usage.days_hours", { d, h: hours % 24 }) : tr("usage.hours", { h: hours }) };
}

/** When a window starts over, as it reads in the popover: "45m" within the hour, the clock time when it is today,
 *  otherwise "2d 3h" (days and hours). "" without a time; "Resets soon" when it is due. */
export function countdown(resetsAt: string | undefined, now = Date.now()): string {
  return countdownOf(resetsAt, now)?.text ?? "";
}

/** The countdown as a phrase: "Resets in 55m", "Resets in 2d 3h", but a clock time reads "Resets at 1:05 PM". */
export function resetLabel(resetsAt: string | undefined, now = Date.now()): string {
  const c = countdownOf(resetsAt, now);
  if (!c) return "";
  if (c.kind === "due") return c.text;
  return c.kind === "clock" ? tr("usage.reset_at", { time: c.text }) : tr("usage.reset_in", { countdown: c.text });
}

/** How long ago a sample was taken ("12 seconds ago"); coarser past a minute. "" for a time that is not one. */
export function sampleAge(iso: string, now = Date.now()): string {
  const t = Date.parse(iso);
  if (!Number.isFinite(t)) return "";
  const s = Math.max(0, Math.round((now - t) / 1000));
  const ago = (n: number, unit: Intl.RelativeTimeFormatUnit) => formatRelative(-n, unit, { numeric: "always" });
  if (s < 60) return ago(s, "second");
  if (s < 3600) return ago(Math.floor(s / 60), "minute");
  if (s < 86400) return ago(Math.floor(s / 3600), "hour");
  return ago(Math.floor(s / 86400), "day");
}

/** The accounts worth showing: samples within three days, windows that have not reset, accounts that still have one;
 *  each stale once its `stale_at` has passed, however long ago the hub said it (#1905: the last answer is kept while
 *  refreshes fail, and its own stale mark was the hub's at that moment). */
export function liveAccounts(accounts: UsageAccount[], now = Date.now()): UsageAccount[] {
  const out: UsageAccount[] = [];
  for (const a of accounts) {
    const sampled = Date.parse(a.sampled_at);
    if (Number.isFinite(sampled) && now - sampled > SAMPLE_MAX_AGE_MS) continue;
    const windows = a.windows.filter((w) => !w.resets_at || Date.parse(w.resets_at) > now);
    const staleAt = a.stale_at ? Date.parse(a.stale_at) : NaN;
    const stale = Boolean(a.stale) || (Number.isFinite(staleAt) && now >= staleAt);
    if (windows.length) out.push({ ...a, windows, ...(stale ? { stale } : {}) });
  }
  return out;
}

/** The used percentage of an account's fullest window. A per-model allowance does not count while the plan's own windows
 *  are there: one model running out is not the account running out. */
export function fullest(a: UsageAccount): number {
  const plan = a.windows.filter((w) => !w.model);
  return (plan.length ? plan : a.windows).reduce((m, w) => Math.max(m, w.used_percent), 0);
}

/** Where an account was seen, for people: the computers that read it by name (an older hub only says "a computer"),
 *  then the gateways'. Never "this computer": the numbers were read on a node, not on the device in hand (#1907). */
export function sourceNames(a: UsageAccount): string[] {
  const machines = a.machines?.length ? a.machines : a.local ? [tr("usage.source_local")] : [];
  return [...machines, ...(a.sources ?? [])];
}

/** What the gateway could not do, in words; "" when nothing went wrong or the hub sends a reason this build doesn't know. */
export function problemText(a: UsageAccount): string {
  switch (a.problem) {
    case "rejected":
      return tr("usage.problem_rejected");
    case "unreachable":
      return tr("usage.problem_unreachable");
    case "failed":
      return tr("usage.problem_failed");
    case "error":
      return tr("usage.problem_error");
    default:
      return "";
  }
}

/** The plan to write beside the name, unless the name already says it ("Codex Pro" and plan "pro"). */
export function planNote(a: UsageAccount): string {
  const plan = (a.plan ?? "").trim();
  return plan && !a.name.toLowerCase().includes(plan.toLowerCase()) ? plan : "";
}

export const windowKey = (w: UsageWindow) => `${w.label}:${w.window_minutes}:${w.model ?? ""}`;

/** The accounts the sidebar draws bars for: the two with the fullest window, stale ones last. */
export function topAccounts(accounts: UsageAccount[], n = 2): UsageAccount[] {
  return [...accounts].sort((a, b) => Number(!!a.stale) - Number(!!b.stale) || fullest(b) - fullest(a)).slice(0, n);
}

export const isWarn = (percent: number) => percent >= WARN_PERCENT;

/** The accounts for the sidebar; [] until the hub answers and when it has none (the bars are not drawn then). A failed
 *  refresh (the assistant away, the hub out of reach) keeps the last answer, which `liveAccounts` ages (#1905); only an
 *  older hub without the route (404) means there is nothing to show. */
export function useUsageWindows(): UsageAccount[] {
  const [accounts, setAccounts] = useState<UsageAccount[]>([]);
  useEffect(() => {
    let dead = false;
    // The tab coming back to the front asks again while the poll's request may still be out; only the newest
    // request's reply (or failure) counts, so an older one landing late cannot overwrite or blank the bars.
    let seq = 0;
    const load = () => {
      if (document.visibilityState !== "visible") return;
      const mine = ++seq;
      const current = () => !dead && mine === seq;
      api<{ accounts?: UsageAccount[] }>("/usage/windows")
        .then((r) => current() && setAccounts(r.accounts ?? []))
        // an older hub has no such route: nothing to show; any other failure keeps the bars it last had, as a copy so
        // that they render again and liveAccounts judges their age with the time now (#1905)
        .catch((e: unknown) => current() && setAccounts((kept) => (e instanceof ApiError && e.status === 404 ? [] : kept.slice())));
    };
    load();
    const t = setInterval(load, POLL_MS);
    document.addEventListener("visibilitychange", load);
    return () => {
      dead = true;
      clearInterval(t);
      document.removeEventListener("visibilitychange", load);
    };
  }, []);
  return accounts;
}
