import { t } from "../i18n";
import { reviewState, type Supervision } from "./supervision";
import { api, ApiError, enc } from "./api";
import { contextBody } from "./context-body";
import { parseAPIError, retryable } from "./api-errors";
import { stateOf } from "./format";
import { splitAgentSessions, type AgentGroup, type AgentMutes } from "./agent-groups";
import type { AgentState, Machine, Session } from "./types";
import type { Route } from "./router";
import { byTime } from "./times";
import { stateName } from "./state-labels";
import { usageLimitAgent } from "./usage-limit";

export type TodoAgentSource = "manual" | "space" | "spawned" | "chat";

export interface TodoAgent {
  machine_id: string;
  session_id: string;
  linked_at: string;
  released_at?: string | null;
  title?: string;
  cwd?: string;
  agent?: string;
  start_error?: string;
  source?: TodoAgentSource;
  via?: string;
}

export interface TodoSpace {
  machine_id: string;
  space: string;
  linked_at: string;
}

export interface Todo {
  id: string;
  title: string;
  notes?: string;
  done_at?: string | null;
  created_at: string;
  updated_at: string;
  agents?: TodoAgent[];
  spaces?: TodoSpace[];
  /** The assistant conversation that opened it as a task, and the plan (docs/assistant/unified-entry.md); unset for a todo made by hand. */
  origin?: string;
  plan_id?: string;
  /** "auto" when the hub marked the task done because its work was judged finished, with why (docs/assistant/todos.md "Auto-complete"). */
  done_by?: string;
  done_reason?: string;
  /** When the user last reopened it after it was done. */
  reopened_at?: string | null;
  /** Opened from the main conversation while "notify tasks the main chat dispatched only in the main chat" was on (#1731). */
  quiet?: boolean;
  /** The task's agent did not start, and why: the hub ended it there (done_at is set too, #1077). */
  start_error?: string;
  /** The main conversation supervises it (#3124): what done means and where its review stands (lib/supervision.ts). */
  supervision?: Supervision;
  /** How far the work got (`plan` / `develop` / `review` / `merge` / `deploy` / `verify`, #2776); unset on hubs that
   * don't send it, and unknown values draw nothing (lib/todo-stage.ts). */
  stage?: string;
  /** The pull request the work is in, when the hub knows one (#2776). */
  pr_url?: string;
  /** How far its finished work went out; short of live, where it is held and who can unhold it (docs/assistant/todos.md "Delivery"). */
  delivery?: TodoDelivery;
  /** The chat thread the todo is bound to (#5279, docs/assistant/todos.md "Chat thread"); unset while it has none. */
  thread?: TodoThread;
}

/** A chat thread a todo is bound to: the channel, the chat and the thread in it name it; the title is only a label. */
export interface TodoThread {
  channel: string;
  chat_id: string;
  thread_id: string;
  title: string;
}

export interface TodoDelivery {
  state: "live" | "pr" | "merged" | "off" | "none" | string;
  gate?: string;
  unblock?: "user" | "agent" | "other" | string;
  at?: string;
}

/** An open task whose work is finished but held short of live (stopped at a PR, merged, off by default); else null. */
export function heldDelivery(todo: Todo): TodoDelivery | null {
  const d = todo.delivery;
  if (todo.done_at || !d) return null;
  return d.state === "pr" || d.state === "merged" || d.state === "off" ? d : null;
}

export type TodoStatus = "done" | "needs_you" | "interrupted" | "usage_limit" | "unread" | "working" | "follow_up" | "not_started" | "failed";

/** Where a status sorts, as the task board orders its sections (lib/task-board.ts): what the user must act on first —
 *  Needs you, Awaiting review (rank 2, see todoRank), Failed to start — then Working, Finished, Stopped, Done (#4896). */
export const TODO_STATUS_RANK: Record<TodoStatus, number> = {
  needs_you: 0,
  interrupted: 1,
  usage_limit: 1.5, // after Interrupted, before Awaiting review (todoRank returns 2)
  failed: 3,
  working: 4,
  not_started: 5,
  unread: 6,
  follow_up: 7,
  done: 8,
};

/** The text color of a status's tone (TODO_STATUS_INFO), as the Tasks page and the overview draw it. */
export const TODO_TONE_CLASS: Record<(typeof TODO_STATUS_INFO)[TodoStatus]["tone"], string> = {
  red: "text-red",
  green: "text-green",
  amber: "text-orange",
  idle: "text-ink-3",
  off: "text-ink-3",
};

/** A supervised task whose work was judged finished and waits for the user to accept it (#3124): nothing of it runs or
 *  asks, so it is the user's to review rather than just stopped or finished-unseen (#4896). */
export function awaitsReview(todo: Todo, status: TodoStatus): boolean {
  return reviewState(todo) === "pending_review" && (status === "follow_up" || status === "unread" || status === "not_started");
}

/** A task's place in the order: its status's rank, Awaiting review right after Needs you. */
export function todoRank(todo: Todo, status: TodoStatus): number {
  return awaitsReview(todo, status) ? 2 : TODO_STATUS_RANK[status];
}

export const TODO_STATUS_INFO: Record<TodoStatus, { label: string; tone: "red" | "green" | "amber" | "idle" | "off" }> = {
  done: { label: stateName("done"), tone: "off" },
  needs_you: { label: stateName("needs_you"), tone: "amber" },
  interrupted: { label: stateName("interrupted"), tone: "red" },
  usage_limit: { label: t("lib-usage-limit.short"), tone: "amber" },
  unread: { label: stateName("unread"), tone: "green" },
  working: { label: stateName("working"), tone: "amber" },
  follow_up: { label: stateName("follow_up"), tone: "idle" },
  not_started: { label: stateName("not_started"), tone: "off" },
  failed: { label: stateName("failed"), tone: "red" },
};

/** Nothing of the task runs (it ended, stopped, or never started): the user may remove it (#1142). */
export function isRemovable(status: TodoStatus): boolean {
  return status !== "working" && status !== "needs_you";
}

export type SessionLookup = (mid: string, sid: string) => Session | undefined;

/** The hub closed it because its task's work was judged finished; reopening undoes it (docs/assistant/todos.md "Auto-complete"). */
export function isAutoDone(todo: Todo): boolean {
  return Boolean(todo.done_at) && todo.done_by === "auto";
}

/** Its supervisor closed it after reviewing the work (done_by review, #3124). */
export function isReviewDone(todo: Todo): boolean {
  return Boolean(todo.done_at) && todo.done_by === "review";
}

/** The status's label for this todo: "Auto-completed" for one the hub closed, "Accepted" for one its supervisor did. */
export function todoStatusLabel(todo: Todo, status: TodoStatus): string {
  if (status === "done" && isReviewDone(todo)) return todo.supervision?.state === "cancelled" ? t("lib-todos.review_cancelled") : t("lib-todos.review_done");
  return status === "done" && isAutoDone(todo) ? t("lib-todos.auto_done") : TODO_STATUS_INFO[status].label;
}

export function makeSessionLookup(sessions: Session[] | Record<string, Session[]>): SessionLookup {
  if (Array.isArray(sessions)) {
    const map = new Map<string, Session>();
    for (const s of sessions) map.set(`${s.machine_id}/${s.session_id}`, s);
    return (mid, sid) => map.get(`${mid}/${sid}`);
  }
  return (mid, sid) => (sessions[mid] ?? []).find((s) => s.session_id === sid);
}

/**
 * Todo status rules (the first that holds wins):
 * 0. Didn't start: a task agent opened from a conversation never started and the hub ended it (start_error, #1077)
 * 1. Done: the user marked it done (done_at != null)
 * 2. Needs you: an attached agent needs you (a question, an approval)
 * 2.5 Interrupted: an attached agent's turn ended on a model API error (turn_error, #1516), unfinished, waiting for you to continue it.
 *     A subscription usage limit is not this (usage_limit, below).
 * 2.6 Usage limit: an attached agent is stopped on a subscription usage limit that has not reset yet
 * 3. Unread: an attached agent finished and you have not seen it
 * 4. Working: an attached agent is working or starting
 * 5. Follow-up: every attached agent is stopped (seen, not running, failed to start)
 * 6. Not started: no attached agent
 */
export function todoStatus(todo: Todo, lookup?: SessionLookup): TodoStatus {
  if (todo.start_error) return "failed";
  if (todo.done_at) return "done";
  const active = (todo.agents ?? []).filter((a) => !a.released_at);
  if (active.length === 0) return "not_started";

  let hasNeedsYou = false;
  let hasInterrupted = false;
  let hasLimit = false;
  let hasUnread = false;
  let hasWorking = false;

  for (const a of active) {
    if (a.start_error) {
      // a failed start counts as stopped
      continue;
    }
    const s = lookup?.(a.machine_id, a.session_id);
    const st: AgentState = s ? stateOf(s) : "offline";
    if (st === "needs_you") {
      hasNeedsYou = true;
      break;
    }
    if (usageLimitAgent(s)) hasLimit = true;
    else if (interruptedAgent(s)) hasInterrupted = true;
    else if (st === "unread") hasUnread = true;
    else if (st === "working" || s?.starting) hasWorking = true;
  }

  if (hasNeedsYou) return "needs_you";
  if (hasInterrupted) return "interrupted";
  if (hasLimit) return "usage_limit";
  if (hasUnread) return "unread";
  if (hasWorking) return "working";
  return "follow_up";
}

/** The agent stopped on a model API error (the hub's turn_error): cut off, not finished (#1516). */
export function interruptedAgent(s?: Session): boolean {
  if (!s?.turn_error || usageLimitAgent(s)) return false;
  const st = stateOf(s);
  return st === "unread" || st === "idle";
}

/** Why the model service cut the agent off (#1516), in words, and whether "Continue" can help (a login or a bad request is
 *  not fixed by going on); undefined when it was not cut off. */
export function interruption(s?: Session): { reason: string; canContinue: boolean } | undefined {
  if (!interruptedAgent(s)) return undefined;
  const info = parseAPIError(s?.turn_error);
  return { reason: info?.reason ?? t("lib-todos.model_request_failed"), canContinue: !info || retryable(info) };
}

/**
 * The todo list's order, the task board's (#4896): by todoRank — Needs you, Interrupted, Awaiting review, Failed to start, Working, Not started, Finished,
 * Stopped, Done — then newest first. A task that did not start has done_at too, but it is the user's to retry or remove,
 * so it does not sink with the done ones.
 */
export function sortTodos(todos: Todo[], lookup?: SessionLookup): Todo[] {
  return [...todos].sort((a, b) => {
    const rA = todoRank(a, todoStatus(a, lookup));
    const rB = todoRank(b, todoStatus(b, lookup));
    if (rA !== rB) return rA - rB;

    const tA = a.created_at ? Date.parse(a.created_at) || 0 : 0;
    const tB = b.created_at ? Date.parse(b.created_at) || 0 : 0;
    if (tA !== tB) return tB - tA;
    return a.id.localeCompare(b.id);
  });
}

/**
 * Finds the most pressing agent of a todo and its summary
 */
export function mostUrgentAgent(
  todo: Todo,
  lookup?: SessionLookup,
): { agent: TodoAgent; session?: Session; status: AgentState | "failed" } | null {
  const active = (todo.agents ?? []).filter((a) => !a.released_at);
  if (active.length === 0) return null;

  const score = (st: AgentState | "failed", s?: Session) => {
    if (interruptedAgent(s)) return 3.5; // after one waiting on the user, before one done
    if (usageLimitAgent(s)) return 3.4;
    switch (st) {
      case "needs_you":
        return 4;
      case "unread":
        return 3;
      case "working":
        return 2;
      case "failed":
        return 1;
      default:
        return 0;
    }
  };

  let best: { agent: TodoAgent; session?: Session; status: AgentState | "failed" } | null = null;
  let bestScore = -1;

  for (const a of active) {
    const s = lookup?.(a.machine_id, a.session_id);
    const st: AgentState | "failed" = a.start_error ? "failed" : s ? stateOf(s) : "offline";
    const sc = score(st, s);
    if (sc > bestScore || !best) {
      bestScore = sc;
      best = { agent: a, session: s, status: st };
    }
  }

  return best;
}

function byDate(a: Session, b: Session): number {
  const parseTime = (s: Session) => {
    const raw = (stateOf(s) === "working" ? s.state_since || s.updated_at : s.updated_at) ?? "";
    return raw ? Date.parse(raw) || 0 : 0;
  };
  const diff = parseTime(b) - parseTime(a);
  if (diff !== 0) return diff;
  return a.session_id.localeCompare(b.session_id) || a.machine_id.localeCompare(b.machine_id);
}

function sessionRank(s: Session, mutes: AgentMutes): number {
  const state = stateOf(s);
  const waiting = ["idle", "unknown"].includes(state) && Boolean(s.loop || s.background?.length || s.subagents?.some((a) => a.running));
  const isMuted = mutes.sessions.includes(`${s.machine_id}/${s.session_id}`) || Boolean(s.space_name && mutes.spaces.includes(`${s.machine_id}/${s.space_name}`));
  if (state === "needs_you") return 0;
  if (isMuted && (state === "working" || state === "unread" || waiting)) return 4;
  if (state === "unread") return 1;
  if (state === "working") return 2;
  if (waiting) return 3;
  return 5;
}

/** An agent opened by hand in herdr: not started by Persona (nor spawned through it). */
function isManualHerdr(s: Session) {
  return s.space_kind === "herdr" && !s.started_by_persona;
}

/**
 * Pure function for the Agents sidebar's "By to-do" grouping:
 * - one segment per unfinished todo with attached agents; the header is the todo's title and status, clicking it opens the todo
 * - agents on no todo opened by hand in herdr get one "Opened by hand" segment (#2608)
 * - the other agents on no todo go last in one "Unassigned" segment, folded by default (#1836)
 * - pin, mute and "Recent (not running)" follow the same rules as "By status"
 */
export function buildTodoAgentGroups(
  all: Session[],
  machines: Machine[],
  todos: Todo[],
  mutes: AgentMutes = { spaces: [], sessions: [] },
): AgentGroup[] {
  const { live } = splitAgentSessions(all, machines);
  const lookup = makeSessionLookup(live);

  // order of unfinished todos
  const openTodos = sortTodos(todos.filter((t) => !t.done_at), lookup);

  // map each todo to its live sessions
  const liveByKey = new Map<string, Session>();
  for (const s of live) {
    liveByKey.set(`${s.machine_id}/${s.session_id}`, s);
  }

  // Each agent sits under one todo: the session's own open todo first (pushed with the session, so newer than a
  // todo's agents list that still has it after a move, #4802; as Android's AgentList), else the first open todo
  // listing it (not released).
  const openIds = new Set(openTodos.map((t) => t.id));
  const homeOf = new Map<string, string>();
  for (const [key, s] of liveByKey) {
    const own = s.todo?.id;
    const home = own && openIds.has(own) ? own
      : openTodos.find((t) => (t.agents ?? []).some((a) => !a.released_at && `${a.machine_id}/${a.session_id}` === key))?.id;
    if (home) homeOf.set(key, home);
  }
  const assignedSessionKeys = new Set(homeOf.keys());
  const groups: AgentGroup[] = [];

  for (const todo of openTodos) {
    // in the todo's agents order, then the sessions on it by their own todo only
    const listed = (todo.agents ?? []).filter((a) => !a.released_at).map((a) => `${a.machine_id}/${a.session_id}`);
    const keys = [...new Set([...listed, ...liveByKey.keys()])].filter((key) => homeOf.get(key) === todo.id);
    const todoSessions = keys.map((key) => liveByKey.get(key)!);

    if (todoSessions.length > 0) {
      // order within a group: by status priority (pinned / needs you first, muted down), same priority by time, newest first
      todoSessions.sort((a, b) => {
        const rA = sessionRank(a, mutes);
        const rB = sessionRank(b, mutes);
        if (rA !== rB) return rA - rB;
        return byDate(a, b);
      });

      const st = todoStatus(todo, lookup);
      groups.push({
        key: `todo:${todo.id}`,
        name: todo.title,
        sessions: todoSessions,
        todoId: todo.id,
        todoStatus: st,
      });
    }
  }

  const byRank = (a: Session, b: Session) => {
    const rA = sessionRank(a, mutes);
    const rB = sessionRank(b, mutes);
    if (rA !== rB) return rA - rB;
    return byDate(a, b);
  };
  const rest = live.filter((s) => !assignedSessionKeys.has(`${s.machine_id}/${s.session_id}`));

  // agents opened by hand in herdr and on no todo get their own "Opened by hand" segment, before "Unassigned", open (#2608):
  // no auto-attach rule recognizes them (not spawned, their space is on no todo either)
  const manual = rest.filter(isManualHerdr);
  if (manual.length > 0) {
    manual.sort(byRank);
    groups.push({ key: "todo:manual", name: t("lib-todos.manual"), hint: t("lib-todos.manual_hint"), sessions: manual });
  }

  // agents on no todo go last in one "Unassigned" segment, folded by default
  const unassigned = rest.filter((s) => !isManualHerdr(s));
  if (unassigned.length > 0) {
    unassigned.sort(byRank);
    groups.push({
      key: "todo:none",
      name: t("lib-todos.unassigned"),
      hint: t("lib-todos.unassigned_hint"),
      sessions: unassigned,
      // folded below todos, unless something in it needs you (unread alone stays folded: many lists always have some);
      // with no todo above it is the whole list, so open
      folded: groups.some((g) => g.todoId) && !unassigned.some((s) => stateOf(s) === "needs_you"),
    });
  }

  return groups;
}

/**
 * The format when handing it to an agent (the same as the inbox's)
 * `<persona-context kind="context" title="To-do · <title>" url="">notes</persona-context>`
 */
export function formatTodoContext(title: string, notes?: string): string {
  const escape = (s: string) => s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  // a closing tag in the notes would end the block early (either name, #3182)
  const body = contextBody(notes ?? "").trim();
  return `<persona-context kind="context" title="${escape(t("lib-todos.context_title", { title }))}" url="">${body}</persona-context>`;
}

// ── Decision log (#1618, docs/assistant/todos.md "Decision log") ───────────────

/** One call the user made on a todo: written by hand, or their answer to a question card of an agent on it. */
export interface TodoDecision {
  id: string;
  todo_id: string;
  question: string;
  choice: string;
  reason: string;
  source: "manual" | "card" | "outcome" | "agent" | "thread" | string;
  source_machine: string;
  source_session: string;
  /** The question card (interaction id) it was answered in; empty for one written by hand. */
  source_message: string;
  /** The earlier decision this one replaces. */
  supersedes_id: string;
  /** The newest decision that replaces this one; empty while it is in effect. */
  superseded_by: string;
  created_at: string;
}

/** Where a decision came from, as the detail page says it: the agent that asked, by its title when known. */
export function decisionSourceLabel(d: TodoDecision, todo: Todo, lookup: ReturnType<typeof makeSessionLookup>): string {
  if (d.source === "outcome") return t("lib-todos.decision_outcome");
  if (d.source === "agent") return t("lib-todos.decision_by_agent");
  if (d.source === "thread") return t("lib-todos.decision_by_thread");
  if (d.source !== "card") return t("lib-todos.decision_manual");
  const title = lookup(d.source_machine, d.source_session)?.title
    || todo.agents?.find((a) => a.machine_id === d.source_machine && a.session_id === d.source_session)?.title;
  if (!title) return t("lib-todos.decision_agent_question");
  // A session's title is often its first message: keep the label to a glance.
  const short = [...title.trim()].length > 24 ? `${[...title.trim()].slice(0, 24).join("")}…` : title.trim();
  return t("lib-todos.decision_in_question", { title: short });
}

/** The decisions d may be marked as replacing: earlier ones (list order) that are in effect, not d itself. */
export function supersedeCandidates(d: TodoDecision, all: TodoDecision[]): TodoDecision[] {
  const at = all.findIndex((x) => x.id === d.id);
  return all.slice(0, Math.max(at, 0)).filter((x) => !x.superseded_by && x.id !== d.supersedes_id);
}

/** Every decision of a todo, oldest first, superseded ones included (`superseded_by` set). */
export async function fetchTodoDecisions(todoId: string): Promise<TodoDecision[]> {
  const r = await api<{ decisions: TodoDecision[] }>(`/todos/${enc(todoId)}/decisions?all=1`);
  return r.decisions ?? [];
}

export async function addTodoDecision(
  todoId: string,
  d: { question: string; choice: string; reason?: string; supersedes_id?: string },
): Promise<TodoDecision> {
  const r = await api<{ decision: TodoDecision }>(`/todos/${enc(todoId)}/decisions`, {
    method: "POST",
    json: { question: d.question.trim(), choice: d.choice.trim(), reason: d.reason?.trim() || undefined, supersedes_id: d.supersedes_id || undefined },
  });
  return r.decision;
}

/** Marks decision `id` as replacing the earlier `supersedesId` ("" undoes it). */
export async function setDecisionSupersedes(todoId: string, id: string, supersedesId: string): Promise<TodoDecision> {
  const r = await api<{ decision: TodoDecision }>(`/todos/${enc(todoId)}/decisions/${enc(id)}`, {
    method: "PATCH",
    json: { supersedes_id: supersedesId },
  });
  return r.decision;
}

// ── API helpers ──────────────────────────────────────────────

export async function createTodo(title: string, notes?: string): Promise<Todo> {
  const r = await api<{ todo: Todo }>("/todos", {
    method: "POST",
    json: { title: title.trim(), notes: notes?.trim() || undefined },
  });
  return r.todo;
}

export async function updateTodo(id: string, patch: { title?: string; notes?: string; done?: boolean }): Promise<Todo> {
  const r = await api<{ todo: Todo }>(`/todos/${enc(id)}`, {
    method: "PATCH",
    json: patch,
  });
  return r.todo;
}

export async function deleteTodo(id: string): Promise<void> {
  await api(`/todos/${enc(id)}`, { method: "DELETE" });
}

export async function linkAgentToTodo(todoId: string, mid: string, sid: string): Promise<void> {
  await api(`/todos/${enc(todoId)}/agents/${enc(mid)}/${enc(sid)}`, { method: "PUT" });
}

export async function unlinkAgentFromTodo(todoId: string, mid: string, sid: string): Promise<void> {
  await api(`/todos/${enc(todoId)}/agents/${enc(mid)}/${enc(sid)}`, { method: "DELETE" });
}

export async function linkSpaceToTodo(
  todoId: string,
  mid: string,
  space: string,
): Promise<{ ok: boolean; linked: number }> {
  return await api<{ ok: boolean; linked: number }>(`/todos/${enc(todoId)}/spaces/${enc(mid)}/${enc(space)}`, {
    method: "PUT",
  });
}

export async function unlinkSpaceFromTodo(todoId: string, mid: string, space: string): Promise<void> {
  await api(`/todos/${enc(todoId)}/spaces/${enc(mid)}/${enc(space)}`, { method: "DELETE" });
}

/**
 * Which todo a project hangs on:
 * returns the unfinished Todo that holds the project (machineId, spaceName).
 */
export function findTodoForSpace(todos: Todo[], machineId: string, spaceName: string): Todo | undefined {
  if (!machineId || !spaceName) return undefined;
  for (const t of todos) {
    if (t.done_at) continue;
    if (t.spaces?.some((sp) => sp.machine_id === machineId && sp.space === spaceName)) {
      return t;
    }
  }
  return undefined;
}

/**
 * Navigate to a todo detail view and trigger drawer close callback if provided.
 */
export function openTodoFromTag(
  todoId: string,
  nav: (to: Route) => void,
  onNavigate?: () => void,
) {
  nav({ name: "todos", id: todoId });
  onNavigate?.();
}

/**
 * Check if an error from the spaces (or decisions, #1618) endpoints is a legacy hub 404 (route does not exist).
 * In Persona hub, jsonRouteErrors produces code === "not_found" and error === "Not Found"
 * for unregistered routes. Handlers themselves return specific messages like "todo not found"
 * or a localized "todo does not exist". Only a route-level 404 (code === "not_found" and error === "Not Found", or non-JSON 404)
 * marks the hub as legacy; other 404s are ordinary business failures (#881 review).
 */
export function isLegacySpacesRouteError(e: unknown): boolean {
  if (!(e instanceof ApiError) || e.status !== 404) return false;
  if (!e.isJson) return true;
  return e.body?.code === "not_found" && e.body?.error === "Not Found";
}

/**
 * Check whether todos data supports the spaces field (legacy hub compatibility).
 * An empty list is treated as unknown/unsupported (returns false).
 * Only returns true if at least one todo explicitly contains a spaces array.
 */
export function isSpacesSupported(todos: Todo[]): boolean {
  if (todos.length === 0) return false;
  return todos.some((t) => "spaces" in t && Array.isArray(t.spaces));
}

/**
 * Resolve the current todo ID for LinkTodoDialog target.
 * For a space, looks up from the todos list.
 * For a session, uses session.todo?.id directly so the unlink control
 * does not disappear if /todos hasn't loaded or failed (#874 item 4).
 */
export function resolveLinkCurrentTodoId(
  space?: { machineId: string; space: string },
  session?: Session,
  todos: Todo[] = [],
): string | undefined {
  if (space) {
    return findTodoForSpace(todos, space.machineId, space.space)?.id;
  }
  return session?.todo?.id;
}

/**
 * Format agent source label:
 * - Spawned from project: "With project <space>" (resolved from via, or agent's own session lookup as fallback);
 *   "With project" if neither is available;
 * - Spawned from parent agent: "Spawned by <parent title>"; "Spawned" if parent is not in session list;
 * - Same Feishu chat as most of the todo's botmux agents: "Same chat";
 * - Manual: undefined (not labeled).
 */
export function formatAgentSource(
  agent: TodoAgent,
  _spaces?: TodoSpace[],
  lookup?: SessionLookup,
): string | undefined {
  if (!agent.source || agent.source === "manual") return undefined;

  if (agent.source === "space") {
    let spaceName = "";
    if (agent.via) {
      const slash = agent.via.indexOf("/");
      if (slash >= 0) {
        spaceName = agent.via.slice(slash + 1);
      } else {
        spaceName = agent.via;
      }
    }
    if (!spaceName && lookup) {
      const s = lookup(agent.machine_id, agent.session_id);
      if (s?.space_name) spaceName = s.space_name;
    }
    return spaceName ? t("lib-todos.with_project_named", { name: spaceName }) : t("lib-todos.with_project");
  }

  if (agent.source === "spawned") {
    if (agent.via && lookup) {
      const slash = agent.via.indexOf("/");
      if (slash >= 0) {
        const mid = agent.via.slice(0, slash);
        const sid = agent.via.slice(slash + 1);
        const parent = lookup(mid, sid);
        if (parent?.title) {
          return t("lib-todos.spawned_by", { title: parent.title });
        }
      }
    }
    return t("lib-todos.spawned");
  }

  if (agent.source === "chat") return t("lib-todos.same_chat");

  return undefined;
}


/** An agent going on ("Needs you" / "Working") that the main conversation's dock shows after its tasks (#1113, iOS DockAgent). */
export interface DockAgent {
  session: Session;
  state: "needs_you" | "working";
}

/**
 * What a conversation's dock shows after its tasks (docs/assistant/unified-entry.md "Manual agents in the main chat's task dock"): in the main
 * conversation, every agent going on that no task stands for — the ones opened by hand on the Agents page — waiting
 * for the user first, then the newest in its state; elsewhere nothing. Persona's own conversations are not agents. A
 * task's agents (a task not done, from any conversation) are its pill already; an agent the hub tags with a todo shows
 * only when that todo was made by hand, so a task the todo list has not caught up with stays out too.
 */
export function dockAgents(sessions: Session[], todos: Todo[], conversation: string, main: string | undefined): DockAgent[] {
  if (!main || conversation !== main) return [];
  const taskAgents = new Set<string>();
  const manual = new Set<string>();
  for (const t of todos) {
    if (!t.origin) manual.add(t.id);
    else if (!t.done_at) for (const a of t.agents ?? []) if (!a.released_at) taskAgents.add(`${a.machine_id}/${a.session_id}`);
  }
  const out: DockAgent[] = [];
  for (const s of sessions) {
    if (s.assistant) continue;
    const state = stateOf(s);
    if (state !== "needs_you" && state !== "working") continue;
    if (taskAgents.has(`${s.machine_id}/${s.session_id}`)) continue;
    if (s.todo && !manual.has(s.todo.id)) continue;
    out.push({ session: s, state });
  }
  return out.sort(
    (a, b) =>
      (a.state === "needs_you" ? 0 : 1) - (b.state === "needs_you" ? 0 : 1) ||
      byTime(b.session.state_since, a.session.state_since) ||
      `${a.session.machine_id}/${a.session.session_id}`.localeCompare(`${b.session.machine_id}/${b.session.session_id}`),
  );
}

/** What a task's lead agent says or does now (iOS TaskNow). */
export type TaskNow = { kind: "words"; text: string } | { kind: "step"; text: string; since?: string };

/**
 * A working agent's step (its latest tool call, which the hub only sends when it is newer than its words) comes
 * before its words; once it stopped, its words do (a turn cut short on a tool call), and the step only stands in for
 * words it never said. Null when it has said and done nothing yet (docs/assistant/unified-entry.md "Task card progress", #1583).
 */
export function taskNow(lead: Session | undefined, working: boolean): TaskNow | null {
  const words = lead?.last_text?.trim() ?? "";
  const step = lead?.activity?.trim() ?? "";
  if (step && (working || !words)) return { kind: "step", text: step, since: working ? lead?.activity_at : undefined };
  if (words) return { kind: "words", text: words };
  return null;
}

/** What each of an agent's running subagents is for: shown under its own progress, never instead of it. */
export function runningSubagents(s: Session | undefined): string[] {
  return (s?.subagents ?? []).filter((a) => a.running).map((a) => a.description?.trim() || a.type || t("lib-todos.subtask"));
}
