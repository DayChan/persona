// Claude Code's ultracode switch on the web (#3428; the server is #3427, docs/protocol/protocol.md "Claude Code ultracode"): with
// it on, Claude runs every substantive task as a multi-agent workflow. It is its own switch beside the thinking effort,
// only for the session's current process, off by default and never remembered. Pure rules and the request here; the
// views are in views/chat/Ultracode.tsx.
import { useEffect, useSyncExternalStore } from "react";
import { t } from "../i18n";
import { api, ApiError, errorText, sessionPath } from "./api";
import { stateOf } from "./format";
import { hasModes } from "./session-actions";
import { flushPushes, getSession, hubHas, upsertSession } from "./store";
import type { PlanTask } from "./feed";
import type { SessionLookup, Todo } from "./todos";
import type { Machine, Session } from "./types";
import { isWarn, liveAccounts, sampleAge, windowMinutes, type UsageAccount } from "./usage-windows";

/** The hub takes the switch (hello `features`): an older hub never shows it. */
export const ULTRACODE_FEATURE = "ultracode";
/** The first Claude Code with the independent switch; older ones are refused (the node's version gate). */
export const ULTRACODE_MIN_VERSION: readonly [number, number, number] = [2, 1, 284];
/** Its name everywhere: a proper name, not translated (docs/protocol/i18n.md). */
export const ULTRACODE = "ultracode";

const isClaude = (s: Pick<Session, "agent">) => !s.agent || s.agent === "claude";

/** Whether a chat offers the switch: the hub has it, the agent is Claude Code whose thinking effort can change now
 *  (the composer's mode chips), and it is not a Persona conversation (the main one included). */
export function ultracodeOffered(s: Session | undefined, features: string[]): boolean {
  if (!s || s.assistant || !hubHas(features, ULTRACODE_FEATURE)) return false;
  return isClaude(s) && hasModes(s) && stateOf(s) !== "offline";
}

/** The workflow mark on a row and on the thinking chip: the node saw it on (unknown is not on). */
export function ultracodeOn(s: Pick<Session, "ultracode" | "assistant" | "agent">): boolean {
  return s.ultracode === true && !s.assistant && isClaude(s);
}

/** The workflow mark on an Agent list row (and so on a to-do): on, and not offline: the node keeps the last value it saw
 *  after the process is gone, and a resume starts it off. */
export function ultracodeMarkShown(s: Session): boolean {
  return stateOf(s) !== "offline" && ultracodeOn(s);
}

/** The workflow mark on a to-do (#12): one of its agents shows it on its Agent list row; before any agent started, the
 *  plan task it starts from asks for it. Never on a to-do that is done or did not start. */
export function todoUltracode(todo: Todo, lookup: SessionLookup, planned?: Pick<PlanTask, "ultracode">): boolean {
  if (todo.done_at || todo.start_error) return false;
  const agents = todo.agents ?? [];
  // the plan's choice only until an agent starts: one moved to another to-do or taken out leaves nothing running here
  if (agents.length === 0) return planned?.ultracode === true;
  return agents.some((a) => {
    if (a.released_at) return false;
    const s = lookup(a.machine_id, a.session_id);
    return s !== undefined && ultracodeMarkShown(s);
  });
}

/** Whether "New agent" offers it: the hub has it and the agent that starts is Claude Code. */
export function ultracodeStartOffered(features: string[], agent: string): boolean {
  return hubHas(features, ULTRACODE_FEATURE) && agent === "claude";
}

/** A known Claude Code version older than the switch's (the node's own rule): an unknown one may be tried. */
export function versionTooOld(version: string | undefined, min: readonly [number, number, number] = ULTRACODE_MIN_VERSION): boolean {
  const m = /^v?(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$/.exec((version ?? "").trim());
  if (!m) return false;
  for (let i = 0; i < 3; i++) {
    const n = Number(m[i + 1]);
    if (n !== min[i]) return n < min[i];
  }
  return false;
}

/** Why "New agent" can't start with it on this computer, "" when it can. */
export function startBlocked(claudeVersion: string | undefined): string {
  return versionTooOld(claudeVersion) ? t("ultracode.version_low", { version: claudeVersion!.trim(), min: ULTRACODE_MIN_VERSION.join(".") }) : "";
}

/** Why an agent can't start with it on computer `machine` (a plan task's, #12), "" when it can or the hub has no
 *  ultracode (then nothing about it is shown or sent). */
export function machineStartBlocked(s: { features: string[]; machines: Pick<Machine, "id" | "claude_version">[] }, machine: string): string {
  return hubHas(s.features, ULTRACODE_FEATURE) ? startBlocked(s.machines.find((m) => m.id === machine)?.claude_version) : "";
}

// ── what this page asked for ─────────────────────────────

/** A switch this page asked for: the node answers by pushing the session, sometimes after the request returns, and a
 *  queued one (`ultracode_pending`) says it waits, not what for. While either holds, the switch stands where it was
 *  put. A refusal is not kept here: it is said once under the row, and asking again gets the node's answer again. */
export interface UltracodeAsk {
  on: boolean;
  sending: boolean;
  /** The session as it was when the request came back: the push with the result has not come while it still is. */
  answered?: Session;
}

/** Where the switch stands: what was asked while it is on its way, otherwise what the node saw. */
export function shownOn(s: Session, ask?: UltracodeAsk): boolean {
  if (!ask) return s.ultracode === true;
  if (ask.sending || s.ultracode_pending) return ask.on;
  if (ask.answered === s && s.ultracode !== ask.on && !s.ultracode_note) return ask.on;
  return s.ultracode === true;
}

/** An ask that no longer says anything: the session moved past it. */
export function askSpent(s: Session, ask: UltracodeAsk): boolean {
  return !ask.sending && !s.ultracode_pending && ask.answered !== s;
}

export interface UltracodeSwitch {
  on: boolean;
  disabled: boolean;
  pending: boolean;
  /** The line under it: what it does, that a change waits for the turn, or the node's reason. */
  line: string;
  /** The line is the node's reason (`ultracode_note`): said in the caution colour. */
  caution: boolean;
}

/** The switch as drawn. Grayed only while its own request is out. The node's reason (`ultracode_note`) is said in place
 *  of the line but does not gray it: many of them are fixed elsewhere (dynamic workflows turned on in Claude Code's
 *  settings, a draft cleared on the computer, a state it could not confirm) and only clear on new terminal evidence, and
 *  the ones that can't be (an old Claude Code, a model without it) the node refuses again at once, nothing typed. A
 *  queued change this page asked for says "Applies next turn" beside the switch it put; one asked elsewhere (another
 *  device, the page reopened) says a change waits, without tying it to where the switch stands. */
export function ultracodeSwitch(s: Session, ask?: UltracodeAsk): UltracodeSwitch {
  const on = shownOn(s, ask);
  const pending = Boolean(s.ultracode_pending);
  const note = (s.ultracode_note ?? "").trim();
  const disabled = Boolean(ask?.sending);
  if (pending) {
    const queued = t(ask ? "ultracode.pending" : "ultracode.pending_other");
    return { on, disabled, pending, line: note ? `${queued} · ${note}` : queued, caution: false };
  }
  if (note) return { on, disabled, pending, line: note, caution: true };
  return { on, disabled, pending, line: t("ultracode.subtitle"), caution: false };
}

/** The quota warning goes with a switch that stands on, or that is off and can simply be turned on: not under the
 *  node's reason, not while turning it off is out or queued. */
export function quotaWarnShown(sw: UltracodeSwitch): boolean {
  return sw.on || (!sw.caution && !sw.pending && !sw.disabled);
}

const asks = new Map<string, UltracodeAsk>();
const listeners = new Set<() => void>();
const keyOf = (s: Pick<Session, "machine_id" | "session_id">) => `${s.machine_id}/${s.session_id}`;

function putAsk(key: string, ask: UltracodeAsk | undefined) {
  if (ask) asks.set(key, ask);
  else asks.delete(key);
  listeners.forEach((l) => l());
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

/** What this page asked of the session's switch, dropped once the session moved past it. */
export function useUltracodeAsk(s: Session | undefined): UltracodeAsk | undefined {
  const key = s ? keyOf(s) : "";
  const get = () => (key ? asks.get(key) : undefined);
  const ask = useSyncExternalStore(subscribe, get, get); // the third: static rendering (tests)
  useEffect(() => {
    if (s && ask && askSpent(s, ask)) putAsk(key, undefined);
  }, [s, ask, key]);
  return ask;
}

/** Forgets every ask (tests). */
export function resetUltracodeAsks() {
  asks.clear();
  listeners.forEach((l) => l());
}

/** The words of a refusal the hub names (an old node, a session that can't take it), when it brought none. */
function refusal(e: unknown): string {
  if (!(e instanceof ApiError)) return "";
  if (e.code === "node_outdated") return e.isJson && e.message !== `HTTP ${e.status}` ? e.message : t("ultracode.node_outdated");
  if (e.code === "ultracode_unsupported") return e.isJson && e.message !== `HTTP ${e.status}` ? e.message : t("ultracode.unsupported");
  return "";
}

/** The hub refused to turn it on for this start or session (an old node, a session or CLI that can't take it):
 *  asking the same again gets the same answer. */
export function ultracodeRefused(e: unknown): boolean {
  return e instanceof ApiError && (e.code === "node_outdated" || e.code === "ultracode_unsupported");
}

/** Why a request failed, in words: the hub's or node's own reason (a refusal, a verdict it could not read), which comes
 *  as a 409 or a 502 with words, rather than "the hub is unavailable". */
export function ultracodeErrorText(e: unknown): string {
  const r = refusal(e);
  if (r) return r;
  if (e instanceof ApiError && e.isJson && (e.status === 409 || e.status === 502) && typeof e.body.error === "string" && e.body.error) return e.body.error;
  return errorText(e);
}

/** POST …/modes {"ultracode": on}; the switch stands where it was put until the node answers. Never sends anything
 *  else: model and effort keep their own requests. A failure (a refusal too) puts it back where the node saw it. */
export async function setUltracode(s: Session, on: boolean): Promise<{ ok: true } | { ok: false; error: string }> {
  const key = keyOf(s);
  putAsk(key, { on, sending: true });
  try {
    await api(`${sessionPath(s.machine_id, s.session_id)}/modes`, { method: "POST", json: { ultracode: on } });
    // A push that came while the request was out (the node's "queued", often) is in the state but reaches the views
    // only at the next frame (lib/store.ts). `answered` is read from the state, so the views hear of it first: drawn
    // with the older session, the ask would look spent and the queue would read as another device's.
    flushPushes();
    putAsk(key, { on, sending: false, answered: getSession(s.machine_id, s.session_id) });
    return { ok: true };
  } catch (e) {
    putAsk(key, undefined);
    return { ok: false, error: ultracodeErrorText(e) };
  }
}

// ── a message while a change waits for the turn ──────────

/** The node holds a message back while an ultracode change waits for the turn (docs/protocol/protocol.md "Claude Code ultracode":
 *  the next turn must not overtake it); the client keeps it and tries again once the change is through. */
export const ULTRACODE_PENDING_CODE = "ultracode_pending";

export function isUltracodeQueued(e: unknown): boolean {
  return e instanceof ApiError && e.code === ULTRACODE_PENDING_CODE;
}

/** Resolves true once the session has no change waiting for its turn (checked every `every` ms, not before `minMs`, so
 *  a push that has not come yet is not taken for "through"); false after `maxMs`, or when the session is gone. A turn
 *  may run long: the default waits up to half an hour. */
export function ultracodeQueueDone(mid: string, sid: string, { minMs = 1500, maxMs = 30 * 60_000, every = 1000 }: { minMs?: number; maxMs?: number; every?: number } = {}): Promise<boolean> {
  const start = Date.now();
  return new Promise((resolve) => {
    const tick = setInterval(() => {
      const s = getSession(mid, sid);
      const waited = Date.now() - start;
      const done = !s || waited >= maxMs ? false : waited >= minMs && !s.ultracode_pending ? true : undefined;
      if (done === undefined) return;
      clearInterval(tick);
      resolve(done);
    }, every);
  });
}

/** Takes the resume notice away (POST …/modes {"ultracode_dismiss_lost": true}); it goes at once and comes back if the
 *  hub says no. */
export async function dismissUltracodeLost(s: Session): Promise<{ ok: true } | { ok: false; error: string }> {
  const before = s.ultracode_lost_at;
  upsertSession({ ...s, ultracode_lost_at: undefined });
  try {
    await api(`${sessionPath(s.machine_id, s.session_id)}/modes`, { method: "POST", json: { ultracode_dismiss_lost: true } });
    return { ok: true };
  } catch (e) {
    const now = getSession(s.machine_id, s.session_id);
    if (now && !now.ultracode_lost_at) upsertSession({ ...now, ultracode_lost_at: before });
    return { ok: false, error: ultracodeErrorText(e) };
  }
}

/** The resume notice is up: it was on before the agent resumed, and nothing turned it back on yet. */
export function lostShown(s: Session | undefined, features: string[]): boolean {
  return Boolean(s && ultracodeOffered(s, features) && s.ultracode_lost_at && s.ultracode !== true);
}

// ── the quota under the switch ───────────────────────────

export interface UltracodeQuota {
  name: string;
  /** The plan's 5-hour and weekly windows it has, in that order: used percentage. */
  windows: { kind: "5h" | "week"; percent: number }[];
  /** One of them is past the caution line (the sidebar's, 80%): the switch says it will run out sooner. */
  warn: boolean;
  /** The numbers are out of date (the hub's mark, as on the usage bars): use only grows, so the real use may be higher
   *  and the line says so, with when they were read (#1906). */
  stale: boolean;
  sampled_at: string;
}

const QUOTA_KINDS: [number, "5h" | "week"][] = [[300, "5h"], [10080, "week"]];

/** The Claude subscription's 5-hour and weekly use (#2432's windows), for the line under the switch: a fresh sample
 *  before a stale one (a fresh account only a gateway reports says more about now than old numbers from a computer,
 *  #1906), then an account read on the computers before one only a gateway reports, then the fullest. null without
 *  one: nothing is drawn. The hub merges accounts across computers, so this is the account, not one computer's share. */
export function ultracodeQuota(accounts: UsageAccount[], now = Date.now()): UltracodeQuota | null {
  const rank = (a: UsageAccount) => (a.stale ? 2 : 0) + (a.local ? 0 : 1);
  const claude = liveAccounts(accounts, now)
    .filter((a) => a.provider === "claude")
    .map((a) => ({ a, windows: QUOTA_KINDS.flatMap(([mins, kind]) => {
      const w = a.windows.find((x) => !x.model && windowMinutes(x) === mins);
      return w ? [{ kind, percent: Math.max(0, Math.min(100, w.used_percent)) }] : [];
    }) }))
    .filter((x) => x.windows.length > 0)
    .sort((x, y) => rank(x.a) - rank(y.a) || Math.max(...y.windows.map((w) => w.percent)) - Math.max(...x.windows.map((w) => w.percent)));
  const top = claude[0];
  if (!top) return null;
  return { name: top.a.name, windows: top.windows, warn: top.windows.some((w) => isWarn(w.percent)), stale: Boolean(top.a.stale), sampled_at: top.a.sampled_at };
}

/** "Out of date · 2 hours ago" when the quota's numbers are, as the usage bars say it; "" while they are current. */
export function quotaAge(q: UltracodeQuota, now = Date.now()): string {
  return q.stale ? t("usage.stale_age", { age: sampleAge(q.sampled_at, now) }) : "";
}

/** "5h 86% · weekly 41%" */
export function quotaText(q: UltracodeQuota): string {
  return q.windows.map((w) => t(w.kind === "5h" ? "ultracode.quota_5h" : "ultracode.quota_week", { percent: `${Math.round(w.percent)}%` })).join(" · ");
}

/** The Codex effort that does the same thing (several subagents, a lot of quota): its chip carries the same mark. */
export const CODEX_ULTRA = "ultra";
