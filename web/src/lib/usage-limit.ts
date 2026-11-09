// A subscription usage limit that stopped a turn (docs/assistant/agent-work.md "Usage limits").
// The hub reports it on the session; task cards, agent rows and the chat say when it resumes
// instead of calling the turn interrupted. An older hub that does not send the fields is not one of these.
import { formatDate, t } from "../i18n";
import type { Session } from "./types";

/** How long past its reset a usage-limit wait still counts (the hub's limitResumeGrace). */
const GRACE_MS = 10 * 60 * 1000;

/** Stopped the way an interrupted turn is: unread or idle, not working and not on a card. */
function stopped(s: Session): boolean {
  if (s.state === "unread" || s.state === "idle") return true;
  if (s.state) return false;
  if (s.status === "working" || s.status === "offline" || s.status === "blocked" || (s.pending_interactions ?? 0) > 0) return false;
  return true;
}

function resetIso(s: Session): string | undefined {
  const iso = s.turn_error_resets_at || s.work?.resets_at;
  if (!iso || !Number.isFinite(Date.parse(iso))) return undefined;
  return iso;
}

/** The CLI said it goes on by itself, or the hub scheduled this limit's continuation.
 *  A wake counts only with the limit's own resets_at (the hub's limitTakenUp): another wait's wake_at does not. */
export function usageLimitContinues(s: Session): boolean {
  if (s.turn_error_resumes) return true;
  const w = s.work;
  return Boolean(
    w?.phase === "blocked" && w.resets_at && w.wake_at && Number.isFinite(Date.parse(w.resets_at)) && Number.isFinite(Date.parse(w.wake_at)),
  );
}

/** Stopped on a subscription usage limit whose reset is still ahead, or not known.
 *  A known reset more than the grace ago is over: the turn reads as interrupted again. */
export function usageLimitAgent(s?: Session, now = Date.now()): boolean {
  if (!s?.turn_error || s.turn_error_kind !== "usage_limit" || !stopped(s)) return false;
  const iso = resetIso(s);
  if (!iso) return true;
  return now <= Date.parse(iso) + GRACE_MS;
}

/** When a limit resets, in the user's time zone: the clock when that is today, the date as well on another day. */
export function usageLimitClock(iso: string, now = Date.now()): string {
  const at = Date.parse(iso);
  if (!Number.isFinite(at)) return "";
  const r = new Date(at);
  const n = new Date(now);
  const sameDay = r.getFullYear() === n.getFullYear() && r.getMonth() === n.getMonth() && r.getDate() === n.getDate();
  const clock = { hour: "2-digit" as const, minute: "2-digit" as const, hourCycle: "h23" as const };
  return formatDate(at, sameDay ? clock : { month: "2-digit", day: "2-digit", ...clock });
}

/** The line for the lists and the chat. Undefined when this is not a usage-limit wait, so callers keep today's interrupted text. */
export function usageLimitLabel(s?: Session, now = Date.now()): string | undefined {
  if (!s || !usageLimitAgent(s, now)) return undefined;
  const iso = resetIso(s);
  const time = iso ? usageLimitClock(iso, now) : "";
  const goesOn = usageLimitContinues(s);
  if (!time) return t(goesOn ? "lib-usage-limit.resumes_later" : "lib-usage-limit.short");
  return t(goesOn ? "lib-usage-limit.resumes" : "lib-usage-limit.resets", { time });
}
