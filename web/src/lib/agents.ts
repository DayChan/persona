import { storedValue, storeValue } from "./compatibility";
// "Personal agents": the Lark gateway's paired personal agents through the hub's /api/v1/agents (docs/protocol/protocol.md). A session id is
// "<source>:<sessionKey>" and bridge keys contain "/", so the whole id is percent-encoded in paths.
import { useSyncExternalStore } from "react";
import { t } from "../i18n";
import { api, errorText } from "./api";
import { SessionList, compareListIds } from "./session-list";
import { toast } from "../ui/atoms";

export interface AgentSession {
  id: string;
  source: string;
  sessionKey: string;
  accountId?: string;
  provider: string;
  title: string;
  kind?: string;
  canSend: boolean;
  unreadCount: number;
  rankTimeMs: number;
  hasAvatar?: boolean;
  avatarVersion?: string;
}

// Blocks are open-ended: text and tool-call are known, anything else is shown raw.
export interface AgentBlock {
  type: string;
  text?: string;
  name?: string;
  status?: string;
  summary?: string;
  input?: unknown;
  [k: string]: unknown;
}

export interface AgentMessage {
  externalId: string;
  sessionKey?: string;
  role: string;
  blocks: AgentBlock[];
  createTimeMs: number;
  position: number;
  local?: { clientId: string; status: string };
}

export interface MessagePage {
  session?: AgentSession;
  messages: AgentMessage[];
  hasMore: boolean;
  nextCursor?: string;
}

export interface SendResult {
  outbox: { clientId: string; status: string; error?: string };
  duplicate: boolean;
  upstream?: AgentMessage;
}

export const agentPath = (id: string) => `/agents/sessions/${encodeURIComponent(id)}`;

export function fetchSessions() {
  return api<{ sessions: AgentSession[] | null }>("/agents/sessions").then((r) => r.sessions ?? []);
}

// A source's setup state (agents.Setup* on the hub): why a reachable source has nothing to list yet.
export interface AgentSourceStatus {
  source: string;
  enabled: boolean;
  error?: string;
  setup?: "missing" | "disabled" | "no_accounts" | string;
  accounts?: unknown[];
}

const SETUP_HINTS: Record<string, string> = {
  missing: t("lib-agents.setup_missing"),
  disabled: t("lib-agents.setup_disabled"),
  no_accounts: t("lib-agents.setup_no_accounts"),
};

/** What to do so the sources list agents, or why one can't: one line per source that is not set up or is failing. */
export const setupHints = (sources: AgentSourceStatus[]) => sources.flatMap((x) => (x.setup && SETUP_HINTS[x.setup] ? [SETUP_HINTS[x.setup]] : x.error ? [x.error] : []));

export function fetchSources() {
  return api<{ sources: AgentSourceStatus[] | null }>("/agents").then((r) => r.sources ?? []);
}

export function fetchMessages(id: string, cursor?: string, limit = 50) {
  const q = new URLSearchParams({ limit: String(limit) });
  if (cursor) q.set("cursor", cursor);
  return api<MessagePage>(`${agentPath(id)}/messages?${q}`).then((p) => ({ ...p, messages: p.messages ?? [] }));
}

export function sendMessage(id: string, clientId: string, text: string) {
  return api<SendResult>(`${agentPath(id)}/messages`, { method: "POST", json: { clientId, text, origin: "app" } });
}

export function markRead(id: string) {
  return api(`${agentPath(id)}/read`, { method: "POST" });
}

// "Remove from list" (POST …/hide): the conversation stays in its source and comes back by itself when it gets a new
// message; …/unhide brings it back by hand. Hidden ones are left out of the list and its counts by the hub.
export function hideSession(id: string) {
  return api(`${agentPath(id)}/hide`, { method: "POST" });
}
export function unhideSession(id: string) {
  return api(`${agentPath(id)}/unhide`, { method: "POST" });
}
export function fetchHiddenSessions() {
  return api<{ sessions: AgentSession[] | null }>("/agents/hidden").then((r) => r.sessions ?? []);
}

// Removed here and not yet re-pulled from the hub: rows leave the list at once, and come back if the hub refuses.
let goingIds: ReadonlySet<string> = new Set();
const goingListeners = new Set<() => void>();
function setGoing(next: ReadonlySet<string>) {
  goingIds = next;
  goingListeners.forEach((l) => l());
}
export function useGoingIds(): ReadonlySet<string> {
  return useSyncExternalStore(
    (l) => {
      goingListeners.add(l);
      return () => goingListeners.delete(l);
    },
    () => goingIds,
  );
}

/** What the hub does for a remove / restore; tests swap it. */
export interface HideDeps {
  hide: (id: string) => Promise<unknown>;
  unhide: (id: string) => Promise<unknown>;
  refresh: () => Promise<unknown>;
  notify: (text: string, tone: "ok" | "error", action?: { label: string; run: () => void }) => void;
}

/** The hub and the toasts, for the app; tests pass their own. */
export const personalHideDeps: HideDeps = {
  hide: hideSession,
  unhide: unhideSession,
  refresh: () => refreshAgentSessions(),
  notify: (text, tone, action) => toast(text, tone, action),
};

/** "Remove from list": the row goes at once; the toast offers Undo; a refusal puts it back and says why. */
export async function removeAgentSession(s: Pick<AgentSession, "id" | "title">, deps: HideDeps): Promise<boolean> {
  const title = s.title || t("lib-format.untitled_session");
  setGoing(new Set([...goingIds, s.id]));
  try {
    await deps.hide(s.id);
    deps.notify(t("lib-agents.removed_toast", { title }), "ok", { label: t("lib-agents.undo"), run: () => void restoreAgentSession(s, deps) });
    return true;
  } catch (e) {
    deps.notify(t("lib-agents.hide_failed", { error: errorText(e) }), "error");
    return false;
  } finally {
    await deps.refresh().catch(() => {});
    const next = new Set(goingIds);
    next.delete(s.id);
    setGoing(next);
  }
}

/** "Restore": back in the list. */
export async function restoreAgentSession(s: Pick<AgentSession, "id" | "title">, deps: HideDeps): Promise<boolean> {
  const title = s.title || t("lib-format.untitled_session");
  try {
    await deps.unhide(s.id);
    deps.notify(t("lib-agents.restored_toast", { title }), "ok");
    return true;
  } catch (e) {
    deps.notify(t("lib-agents.unhide_failed", { error: errorText(e) }), "error");
    return false;
  } finally {
    await deps.refresh().catch(() => {});
  }
}

// ── pure helpers (tested) ───────────────────────────────

const PROVIDER_LABEL: Record<string, string> = { grok_bot: "Grok Bot", muse: "Muse", cue: "Cue", dots: "Dots", claude_duet: "Claude Duet" };
export function providerLabel(p: string): string {
  return PROVIDER_LABEL[p] ?? (p || t("lib-agents.other"));
}

export const totalUnread = (list: AgentSession[]) => list.reduce((n, s) => n + (s.unreadCount || 0), 0);

// Pages come back in position order with the odd swap: merge by externalId, newest copy wins, sort by position.
export function mergeMessages(a: AgentMessage[], b: AgentMessage[]): AgentMessage[] {
  const m = new Map<string, AgentMessage>();
  for (const x of [...a, ...b]) m.set(x.externalId, x);
  return [...m.values()].sort((x, y) => x.position - y.position || x.createTimeMs - y.createTimeMs);
}

export type BlockView = { kind: "text"; text: string } | { kind: "reaction"; emoji: string } | { kind: "tool"; name: string; detail: string } | { kind: "raw"; json: string };

export function blockView(b: AgentBlock): BlockView {
  if (b.type === "text" && typeof b.text === "string") return { kind: "text", text: b.text };
  if (b.type === "reaction" && typeof b.emoji === "string" && b.emoji) return { kind: "reaction", emoji: b.emoji };
  if (b.type === "tool-call" || b.type === "tool_call") {
    const { type: _t, name, ...rest } = b;
    return { kind: "tool", name: String(name ?? "tool"), detail: Object.keys(rest).length ? JSON.stringify(rest, null, 2) : "" };
  }
  return { kind: "raw", json: JSON.stringify(b, null, 2) };
}

export const messageText = (m: AgentMessage) => m.blocks.filter((b) => b.type === "text").map((b) => b.text ?? "").join("\n");

// A message typed here and not yet seen in the session. It keeps its clientId so a retry is idempotent on the hub.
export interface Outgoing {
  clientId: string;
  text: string;
  at: number;
  status: "sending" | "sent" | "failed";
  error?: string;
}

// Persisted messages lose the clientId (externalId changes from local:<clientId>), so a local bubble is dropped once
// a user message with the same text, no older than it was typed (with clock slack), shows up.
export function pendingOutgoing(out: Outgoing[], messages: AgentMessage[], slackMs = 120_000): Outgoing[] {
  const used = new Set<string>();
  return out.filter((o) => {
    if (o.status === "failed" || o.status === "sending") return true;
    const m = messages.find((x) => !used.has(x.externalId) && x.role === "user" && x.createTimeMs >= o.at - slackMs && messageText(x).trim() === o.text.trim());
    if (!m) return true;
    used.add(m.externalId);
    return false;
  });
}

// ── session list store, refreshed on the hub's {"type":"agents"} event ─

type ListState = { sessions: AgentSession[]; loaded: boolean; error: string; hints: string[]; sources: AgentSourceStatus[] | null };
let list: ListState = { sessions: [], loaded: false, error: "", hints: [], sources: null };
const listeners = new Set<() => void>();
const emit = (p: Partial<ListState>) => {
  list = { ...list, ...p };
  listeners.forEach((l) => l());
};

let bounded = false;
export const personalOrder = (a: AgentSession, b: AgentSession) => b.rankTimeMs - a.rankTimeMs || compareListIds(a.id, b.id);
export const personalSessions = new SessionList<AgentSession>("/agents/sessions", (s) => s.id, personalOrder);
export function setAgentListMode(enabled: boolean) {
  bounded = enabled;
}
personalSessions.subscribe(() => {
  if (!bounded) return;
  const page = personalSessions.getSnapshot();
  emit({ sessions: page.rows, loaded: page.loaded, error: page.error ?? "" });
});

let inflight: Promise<void> | null = null;
// A call while a pull is on the way: that pull may have been answered before the change the call is for, so one more
// pull follows it (one, however many calls came meanwhile).
let queued: Promise<void> | null = null;
// Bumped by resetAgents(): a pull still on its way belongs to the previous login, so its answer (and the follow-up
// pull queued behind it) is dropped.
let listGen = 0;
export function refreshAgentSessions(): Promise<void> {
  if (inflight) {
    const gen = listGen;
    if (!queued) {
      const next: Promise<void> = inflight.then(() => {
        // only clear our own slot: after resetAgents() a newer login may have queued its own follow-up
        if (queued === next) queued = null;
        return gen === listGen ? refreshAgentSessions() : undefined;
      });
      queued = next;
    }
    return queued;
  }
  const gen = listGen;
  const read = bounded ? personalSessions.load().then(() => personalSessions.getSnapshot().rows) : fetchSessions();
  const pull: Promise<void> = Promise.all([read, fetchSources().catch(() => null)])
    .then(([sessions, sources]) => {
      if (gen === listGen) emit({ sessions, loaded: true, error: "", ...(sources ? { sources, hints: setupHints(sources) } : {}) });
    })
    .catch((e) => {
      if (gen === listGen) emit({ loaded: true, error: errorText(e) });
    })
    .finally(() => {
      if (inflight === pull) inflight = null;
    });
  inflight = pull;
  return pull;
}

// Called by the WebSocket handler: re-pull the list, and tell open sessions which ids moved (empty = all).
export function agentsChanged(ids: string[]) {
  if (bounded) personalSessions.invalidate();
  void refreshAgentSessions();
  changedListeners.forEach((l) => l(ids));
}

const changedListeners = new Set<(ids: string[]) => void>();
export function onAgentsChanged(l: (ids: string[]) => void) {
  changedListeners.add(l);
  return () => {
    changedListeners.delete(l);
  };
}

export function setLocalRead(id: string) {
  if (list.sessions.some((s) => s.id === id && s.unreadCount)) emit({ sessions: list.sessions.map((s) => (s.id === id ? { ...s, unreadCount: 0 } : s)) });
}

export function resetAgents() {
  bounded = false;
  personalSessions.reset();
  listGen++;
  // the next login's first pull starts right away instead of waiting behind the old one
  inflight = null;
  queued = null;
  emit({ sessions: [], loaded: false, error: "", hints: [], sources: null });
}

export function useAgentList(): ListState {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => list,
  );
}

// ── list prefs: pins, view (group / sort / show / providers), custom sections — shared on the hub ─

export type GroupBy = "date" | "provider" | "none";
export type SortBy = "updated" | "unread" | "name";
export type ShowMode = "all" | "unread" | "sendable";
export interface AgentView {
  group?: GroupBy;
  sort?: SortBy;
  show?: ShowMode;
  providers?: string[]; // empty = every provider
}
export interface AgentSectionDef {
  id: string;
  name: string;
}
export interface AgentPrefs {
  pins: string[];
  view: AgentView;
  sections: AgentSectionDef[];
  assign: Record<string, string>; // session id → section id
  /** Removed from the list: session id → when (ms). Changed only through hide / unhide; the hub keeps it on a save. */
  hidden?: Record<string, number>;
}
export const DEFAULT_VIEW: Required<AgentView> = { group: "date", sort: "updated", show: "all", providers: [] };
export const emptyPrefs = (): AgentPrefs => ({ pins: [], view: {}, sections: [], assign: {} });
export const viewOf = (p: AgentPrefs): Required<AgentView> => ({ ...DEFAULT_VIEW, ...Object.fromEntries(Object.entries(p.view ?? {}).filter(([, v]) => v !== undefined && v !== "")) }) as Required<AgentView>;
export const isDefaultView = (p: AgentPrefs) => {
  const v = viewOf(p);
  return v.group === DEFAULT_VIEW.group && v.sort === DEFAULT_VIEW.sort && v.show === DEFAULT_VIEW.show && !v.providers.length;
};

export const GROUP_LABEL: Record<GroupBy, string> = { date: t("lib-agents.group_date"), provider: t("lib-agents.group_provider"), none: t("lib-agents.group_none") };
export const SORT_LABEL: Record<SortBy, string> = { updated: t("lib-agents.sort_updated"), unread: t("lib-agents.sort_unread"), name: t("lib-agents.sort_name") };
export const SHOW_LABEL: Record<ShowMode, string> = { all: t("lib-agents.show_all"), unread: t("lib-agents.show_unread"), sendable: t("lib-agents.show_sendable") };

export interface SessionGroup {
  key: string; // "pinned", "sec:<id>", "p:<provider>", "d:<bucket>", "all"
  label: string;
  sessions: AgentSession[];
  unread: number;
}

const DAY = 86_400_000;
export function dateBucket(ms: number, now = Date.now()): { key: string; label: string; order: number } {
  const d = new Date(now);
  const today = new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  if (ms >= today) return { key: "today", label: t("lib-agents.date_today"), order: 0 };
  if (ms >= today - DAY) return { key: "yesterday", label: t("lib-agents.date_yesterday"), order: 1 };
  if (ms >= today - 7 * DAY) return { key: "week", label: t("lib-agents.date_week"), order: 2 };
  if (ms >= today - 30 * DAY) return { key: "month", label: t("lib-agents.date_month"), order: 3 };
  return { key: "older", label: t("lib-agents.date_older"), order: 4 };
}

export function matchesQuery(s: AgentSession, q: string) {
  const needle = q.trim().toLowerCase();
  return !needle || s.title.toLowerCase().includes(needle) || s.provider.toLowerCase().includes(needle) || providerLabel(s.provider).toLowerCase().includes(needle);
}

function sorter(sort: SortBy) {
  return (a: AgentSession, b: AgentSession) =>
    sort === "unread" ? (b.unreadCount || 0) - (a.unreadCount || 0) || b.rankTimeMs - a.rankTimeMs
    : sort === "name" ? (a.title || "").localeCompare(b.title || "", "zh") || b.rankTimeMs - a.rankTimeMs
    : b.rankTimeMs - a.rankTimeMs;
}

const mk = (key: string, label: string, sessions: AgentSession[]): SessionGroup => ({ key, label, sessions, unread: sessions.reduce((n, s) => n + (s.unreadCount || 0), 0) });

// Filters (query, show, providers), then "Pinned" (pin order) first, custom sections next, then the chosen grouping,
// each sorted by the chosen order. Empty groups are dropped.
export function arrangeSessions(list: AgentSession[], prefs: AgentPrefs, q = "", now = Date.now()): SessionGroup[] {
  const v = viewOf(prefs);
  const keep = list.filter((s) => matchesQuery(s, q) && (v.show !== "unread" || s.unreadCount > 0) && (v.show !== "sendable" || s.canSend) && (!v.providers.length || v.providers.includes(s.provider)));
  const byId = new Map(keep.map((s) => [s.id, s]));
  const out: SessionGroup[] = [];
  const pinned = prefs.pins.map((id) => byId.get(id)).filter((s): s is AgentSession => !!s);
  const used = new Set(pinned.map((s) => s.id));
  if (pinned.length) out.push(mk("pinned", t("lib-agents.pinned"), pinned));
  const cmp = sorter(v.sort);
  for (const sec of prefs.sections) {
    const ss = keep.filter((s) => !used.has(s.id) && prefs.assign[s.id] === sec.id).sort(cmp);
    ss.forEach((s) => used.add(s.id));
    if (ss.length) out.push(mk(`sec:${sec.id}`, sec.name, ss));
  }
  const rest = keep.filter((s) => !used.has(s.id)).sort(cmp);
  if (v.group === "none") {
    if (rest.length) out.push(mk("all", out.length ? t("lib-agents.other") : "", rest));
  } else if (v.group === "provider") {
    const by = new Map<string, AgentSession[]>();
    for (const s of rest) (by.get(s.provider) ?? by.set(s.provider, []).get(s.provider)!).push(s);
    const gs = [...by].map(([p, ss]) => mk(`p:${p}`, providerLabel(p), ss));
    const top = (g: SessionGroup) => Math.max(...g.sessions.map((s) => s.rankTimeMs));
    out.push(...(v.sort === "name" ? gs.sort((a, b) => a.label.localeCompare(b.label, "zh")) : gs.sort((a, b) => top(b) - top(a))));
  } else {
    const by = new Map<string, { label: string; order: number; ss: AgentSession[] }>();
    for (const s of rest) {
      const b = dateBucket(s.rankTimeMs, now);
      (by.get(b.key) ?? by.set(b.key, { label: b.label, order: b.order, ss: [] }).get(b.key)!).ss.push(s);
    }
    out.push(...[...by].sort((a, b) => a[1].order - b[1].order).map(([k, g]) => mk(`d:${k}`, g.label, g.ss)));
  }
  return out;
}

export function togglePin(p: AgentPrefs, id: string): AgentPrefs {
  return { ...p, pins: p.pins.includes(id) ? p.pins.filter((x) => x !== id) : [id, ...p.pins] };
}
export function assignSection(p: AgentPrefs, id: string, section: string | null): AgentPrefs {
  const assign = { ...p.assign };
  if (section) assign[id] = section;
  else delete assign[id];
  return { ...p, assign };
}
export function addSection(p: AgentPrefs, name: string, id = `s${Date.now().toString(36)}`): AgentPrefs {
  return { ...p, sections: [...p.sections, { id, name: name.trim() }] };
}
export function removeSection(p: AgentPrefs, id: string): AgentPrefs {
  return { ...p, sections: p.sections.filter((s) => s.id !== id), assign: Object.fromEntries(Object.entries(p.assign).filter(([, v]) => v !== id)) };
}
// "Reset to default" resets the view only; pins and sections stay.
export const resetView = (p: AgentPrefs): AgentPrefs => ({ ...p, view: {} });
export function setView(p: AgentPrefs, patch: AgentView): AgentPrefs {
  return { ...p, view: { ...p.view, ...patch } };
}
export function toggleProvider(p: AgentPrefs, provider: string): AgentPrefs {
  const cur = viewOf(p).providers;
  return setView(p, { providers: cur.includes(provider) ? cur.filter((x) => x !== provider) : [...cur, provider] });
}

export function normalizePrefs(x: Partial<AgentPrefs> | null | undefined): AgentPrefs {
  return { pins: Array.isArray(x?.pins) ? x!.pins : [], view: x?.view && typeof x.view === "object" ? x.view : {}, sections: Array.isArray(x?.sections) ? x!.sections : [], assign: x?.assign && typeof x.assign === "object" ? x.assign : {}, ...(x?.hidden && typeof x.hidden === "object" ? { hidden: x.hidden } : {}) };
}

// Hub-backed (GET/PUT /agents/prefs) so web and iOS share pins; mirrored to localStorage, which is also the
// fallback when the hub predates the endpoint.
const PREFS_LS = "persona.agents.prefs";
const readLocal = (): AgentPrefs => {
  try {
    return normalizePrefs(JSON.parse(storedValue(PREFS_LS) ?? "null"));
  } catch {
    return emptyPrefs();
  }
};
let prefs: AgentPrefs = typeof localStorage === "undefined" ? emptyPrefs() : readLocal();
let hubPrefs = true;
const prefListeners = new Set<() => void>();
const emitPrefs = (p: AgentPrefs) => {
  prefs = p;
  try {
    storeValue(PREFS_LS, JSON.stringify(p));
  } catch {
    /* private mode */
  }
  prefListeners.forEach((l) => l());
};

const prefsGone = (e: unknown) => {
  const status = (e as { status?: number }).status;
  if (status === 404 || status === 405) hubPrefs = false;
};

// A GET answer is applied only if it can't undo something newer: no local change since it was sent (prefsRev), no
// PUT on its way when it was sent (prefsOut; that PUT's answer carries the hub's state), and no later GET applied
// already (prefsGetSeq / prefsGetApplied; separate connections can answer out of order).
let prefsRev = 0;
let prefsOut = 0;
let prefsGetSeq = 0;
let prefsGetApplied = 0;
// A GET sent while a PUT is out will be dropped, so a change from another device it may carry is pulled again once
// the PUTs are done.
let prefsPullAfterSave = false;
export function refreshAgentPrefs(): Promise<void> {
  const rev = prefsOut ? -1 : prefsRev;
  if (rev === -1) prefsPullAfterSave = true;
  const seq = ++prefsGetSeq;
  return api<AgentPrefs>("/agents/prefs")
    .then((p) => {
      hubPrefs = true;
      if (rev !== prefsRev || seq < prefsGetApplied) return;
      prefsGetApplied = seq;
      emitPrefs(normalizePrefs(p));
    })
    .catch(prefsGone);
}

let saveSeq = 0;
export function updateAgentPrefs(f: (p: AgentPrefs) => AgentPrefs) {
  const next = f(prefs);
  prefsRev++;
  emitPrefs(next);
  if (!hubPrefs) return;
  const seq = ++saveSeq;
  prefsOut++;
  api<AgentPrefs>("/agents/prefs", { method: "PUT", json: next })
    .then((p) => seq === saveSeq && emitPrefs(normalizePrefs(p)))
    .catch(prefsGone)
    .finally(() => {
      prefsOut--;
      if (prefsOut || !prefsPullAfterSave) return;
      prefsPullAfterSave = false;
      if (hubPrefs) void refreshAgentPrefs();
    });
}

export function useAgentPrefs(): AgentPrefs {
  return useSyncExternalStore(
    (l) => {
      prefListeners.add(l);
      return () => prefListeners.delete(l);
    },
    () => prefs,
  );
}
