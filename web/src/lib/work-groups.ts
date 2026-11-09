// How one exchange's work reads in the chat (cindy's workRunGrouping + messageWorkGroups, docs/web/web.md "Chat"): the
// agent's steps fold into "Worked Xs" groups and only what it delivered stays in the open.
//
// - While the agent works, each run of tools and thoughts at the end is one live group (its newest five steps peek
//   out); its words in between stay visible.
// - Once the exchange is done, the last words are the answer; everything before them folds into one "Worked Xs"
//   whose inside is a timeline: the intermediate words, and each run of steps between them as its own inner group.
// - Delivered prose (600 characters or more, a heading, a table, a list of three or more, an image) never folds, nor
//   does a subagent's card (running or done, #5602), nor a workflow run's card (#3434: running or ended, while the hub
//   lists the run), nor a plan card, nor a compaction divider.
// - A gap of over 30 minutes between two steps (history that never loaded) splits the work: no group spans it.
// - Durations: from the previous boundary (your message, the words before) when there is one, to the next words; else
//   from the group's first step to the latest end among its steps (a tool ends at its result).

import { t } from "../i18n";
import type { Step } from "./turns";
import type { Item } from "./types";

export type ToolStep = Extract<Step, { kind: "tool" }>;

export type AgentStatus = "running" | "completed" | "failed" | "stopped";

/** A group of steps (`children` hold steps and, in a finished timeline, inner groups), or a step left in the open. */
export type WorkNode = WorkGroupNode | { kind: "step"; step: Step };

export interface WorkGroupNode {
  kind: "group";
  /** Stable across the live → done change for the run it started with (`work-…`); a finished timeline: `work-summary-…`. */
  id: string;
  /** Its first step's seq: a group inside a fold the reader opened starts open. */
  seq?: number;
  children: WorkNode[];
  /** The run still going at the end of a live exchange. */
  live: boolean;
  durationMs?: number;
  /** Where its clock starts (ms): the live timer counts from here. */
  startedAt?: number;
}

interface WorkGroupOptions {
  /** The agent is at work on this exchange: its tail stays live. */
  live: boolean;
  /** When the exchange began (your message): the first group's clock starts there. */
  turnStart?: string;
  /** How a subagent call is going (ChatView knows the session's subagents). */
  agentStatus?: (step: ToolStep) => AgentStatus;
  /** Whether a Workflow call draws its run's card (the hub lists the run): it stays in the open, running or ended. */
  workflowCard?: (step: ToolStep) => boolean;
  /** Round-specific visibility receipts stay discoverable outside the ordinary work fold. */
  keepTool?: (step: ToolStep) => boolean;
}

/** History that was never loaded between two steps: further apart than this, they do not share a group. */
const HISTORY_GAP_MS = 30 * 60 * 1000;
/** Words this long are a delivery, not a progress note. */
const DELIVERY_MIN_LENGTH = 600;
/** A live group shows its newest this many steps. */
const LIVE_PREVIEW = 5;

const AGENT_TOOLS = new Set(["Task", "Agent"]);
const PLAN_TOOLS = new Set(["TodoWrite"]);

export const isAgentStep = (s: Step): boolean => s.kind === "tool" && AGENT_TOOLS.has(s.use.tool_name ?? "");
export const isPlanStep = (s: Step): boolean => s.kind === "tool" && PLAN_TOOLS.has(s.use.tool_name ?? "");
/** Claude Code's Workflow call (#3434): its run's card where the hub lists the run, else the row it always was. */
export const isWorkflowStep = (s: Step): s is ToolStep => s.kind === "tool" && s.use.tool_name === "Workflow";
/** A tool call drawn as an action row (not a subagent card, not a plan card, not a workflow's call). */
export const isRowStep = (s: Step): s is ToolStep => s.kind === "tool" && !isAgentStep(s) && !isPlanStep(s) && !isWorkflowStep(s);
const isCompactStep = (s: Step) => s.kind === "notice" && Boolean(s.item.compact);

const HEADING = /^[ \t]{0,3}#{1,6}[ \t]+\S/m;
const TABLE_DIVIDER = /^[ \t]{0,3}\|?[ \t]*:?-{3,}:?[ \t]*\|[-:| \t]*$/m;
const LIST_ITEM = /^[ \t]{0,3}(?:[-*+][ \t]+|\d{1,3}[.)][ \t]+)\S/gm;
const MD_IMAGE = /!\[[^\]\n]*\]\([^)\s]+(?:\s[^)]*)?\)/;
const HTML_IMAGE = /<img(?=[\s/>])[^<>]*\ssrc\s*=\s*["']?[^\s"'<>]+[^<>]*>/i;

/** Words that are what the agent delivered (a report, a brief), not a progress note: they stay in the open wherever
 *  they fall in the exchange. */
export function isDeliveryText(text: string): boolean {
  const t = text.trim();
  if (!t) return false;
  if (t.length >= DELIVERY_MIN_LENGTH) return true;
  if (HEADING.test(t) || TABLE_DIVIDER.test(t) || MD_IMAGE.test(t) || HTML_IMAGE.test(t)) return true;
  return (t.match(LIST_ITEM)?.length ?? 0) >= 3;
}

/** The item a step stands for (a tool: its call). */
function stepItem(s: Step): Item {
  return s.kind === "tool" ? s.use : s.item;
}

export const tsMs = (ts?: string): number | null => {
  const t = Date.parse(ts ?? "");
  return Number.isFinite(t) ? t : null;
};

function stepStart(s: Step): number | null {
  return tsMs(stepItem(s).ts);
}

/** When a step was over: a tool at its result, folded work at its last step. */
function stepEnd(s: Step): number | null {
  const start = stepStart(s);
  let end: number | null = null;
  if (s.kind === "tool") end = tsMs(s.result?.ts);
  else if (s.kind === "fold") end = tsMs(s.item.fold?.until);
  if (start === null) return end;
  return end === null ? start : Math.max(start, end);
}

/** Your words and the agent's: the boundaries a group's clock runs between. */
const boundaryTs = (s: Step | undefined): number | null => (s && (s.kind === "text" || isCompactStep(s)) ? stepStart(s) : null);

const isAnswer = (s: Step) => s.kind === "text" && Boolean(s.item.text?.trim());

function adapter(opts: WorkGroupOptions) {
  /** A step that folds into a run: tools, thoughts, notices, pictures, folded work. A subagent's card never does,
   *  running or done (#5602: it used to fold once done, so a run split around a running one joined up when it ended,
   *  the group took another key and the card jumped into it), nor does a Workflow call that draws its run's card
   *  (#3434), for as long as the hub lists the run. */
  const isActivity = (s: Step) => {
    if (s.kind === "text" || isCompactStep(s)) return false;
    if (isPlanStep(s) || isAgentStep(s)) return false;
    if (s.kind === "tool" && opts.keepTool?.(s)) return false;
    if (isWorkflowStep(s) && opts.workflowCard?.(s)) return false;
    return true;
  };
  /** What a finished exchange may fold: the activities and the agent's words, except delivered ones. */
  const isArchivable = (s: Step) => {
    if (s.kind === "text") return !isDeliveryText(s.item.text ?? "");
    return isActivity(s);
  };
  return { isActivity, isArchivable };
}

type Adapter = ReturnType<typeof adapter>;

/** The step a group is known by: its first real one (cindy keys a group by its first activity, so a live group keeps
 *  its key as it grows and once it is done). */
const anchorStep = (run: Step[]) => run.find((s) => s.kind !== "text") ?? run[0];

/** A group's key: its anchor's seq and block (a fold's is the seq of the first step it stands for, block 0). */
function anchorKey(run: Step[]): string {
  const first = anchorStep(run);
  const it = stepItem(first);
  return `${it.seq}.${first.kind === "fold" ? 0 : (it.id.split("#")[1] ?? 0)}`;
}

function createGroup(run: Step[], next: Step | undefined, live: boolean, prevBoundary: number | null): WorkGroupNode {
  const first = anchorStep(run);
  const anchor = stepStart(first);
  // the previous boundary when it is not after the run's first step: a thought written at once carries the time it
  // was written, and the thinking before it belongs to the run
  const start = prevBoundary !== null && (anchor === null || prevBoundary <= anchor) ? prevBoundary : anchor;
  let end: number | null = next && boundaryTs(next) !== null ? boundaryTs(next) : null;
  if (end === null) {
    // parallel calls finish out of order: the latest end, not the last step's
    for (const s of run) {
      const e = stepEnd(s);
      if (e !== null) end = end === null ? e : Math.max(end, e);
    }
  }
  return {
    kind: "group",
    id: `work-${anchorKey(run)}`,
    seq: stepItem(first).seq,
    children: run.map((step) => ({ kind: "step", step })),
    live,
    durationMs: start !== null && end !== null && end >= start ? end - start : undefined,
    ...(start !== null ? { startedAt: start } : {}),
  };
}

/** A finished run with the agent's words in it: the words, and each run of activities between them as an inner group. */
function createCompletedGroup(run: Step[], next: Step | undefined, prevBoundary: number | null, ad: Adapter): WorkGroupNode {
  if (!run.some((s) => s.kind === "text")) return createGroup(run, next, false, prevBoundary);
  const children: WorkNode[] = [];
  let acts: Step[] = [];
  let inner = prevBoundary;
  const flush = (n: Step | undefined) => {
    if (acts.length) children.push(createGroup(acts, n, false, inner));
    acts = [];
  };
  for (const s of run) {
    if (ad.isActivity(s)) {
      acts.push(s);
      continue;
    }
    flush(s);
    children.push({ kind: "step", step: s });
    inner = boundaryTs(s);
  }
  flush(next);
  const outer = createGroup(run, next, false, prevBoundary);
  return { ...outer, id: `work-summary-${anchorKey(run)}`, children };
}

/** A live tail, or a finished exchange whose last words are not at its end: runs of activities fold, the rest shows. */
function groupActivityRuns(items: Step[], turnStart: number | null, active: boolean, ad: Adapter): WorkNode[] {
  let lastBoundary = -1;
  if (active) items.forEach((s, i) => (isAnswer(s) || isCompactStep(s)) && (lastBoundary = i));
  const out: WorkNode[] = [];
  let run: Step[] = [];
  let runLast = -1;
  let prev = turnStart;
  const flush = (next?: Step) => {
    if (run.length) out.push(createGroup(run, next, active && runLast > lastBoundary, prev));
    run = [];
  };
  items.forEach((s, i) => {
    if (ad.isActivity(s)) {
      run.push(s);
      runLast = i;
      return;
    }
    flush(s);
    out.push({ kind: "step", step: s });
    prev = boundaryTs(s);
  });
  flush();
  return out;
}

/** A finished exchange: its last words (the run of words after the last activity) stay; the rest folds. Null when
 *  something other than words comes after them (an interrupted exchange): that one folds as runs. */
function groupAnsweredTurn(items: Step[], turnStart: number | null, ad: Adapter): WorkNode[] | null {
  let lastAnswer = -1;
  items.forEach((s, i) => isAnswer(s) && (lastAnswer = i));
  if (lastAnswer < 0) return null;
  if (items.some((s, i) => i > lastAnswer && ad.isActivity(s))) return null;
  let lastActivity = -1;
  for (let i = lastAnswer - 1; i >= 0; i--) {
    if (ad.isActivity(items[i])) {
      lastActivity = i;
      break;
    }
  }
  const answers = new Set<number>();
  let from = lastAnswer;
  if (lastActivity >= 0) while (from > lastActivity + 1 && isAnswer(items[from - 1])) from--;
  for (let i = from; i <= lastAnswer; i++) if (isAnswer(items[i])) answers.add(i);

  // (cindy also folds a thought that lands behind the answer back into the group above it; that needs its turn seals,
  // which Claude Code transcripts do not have: an activity after the answer already sent us to groupActivityRuns)
  const out: WorkNode[] = [];
  let run: Step[] = [];
  let prev = turnStart;
  const flush = (next?: Step) => {
    if (run.length) out.push(createCompletedGroup(run, next, prev, ad));
    run = [];
  };
  items.forEach((s, i) => {
    if (!answers.has(i) && ad.isArchivable(s)) {
      run.push(s);
      return;
    }
    flush(s);
    out.push({ kind: "step", step: s });
    prev = boundaryTs(s);
  });
  flush();
  return out;
}

/** One exchange's steps (the answer among them, in order) as what the chat draws. */
export function workBlocks(steps: Step[], opts: WorkGroupOptions): WorkNode[] {
  const ad = adapter(opts);
  const out: WorkNode[] = [];
  let turn: Step[] = [];
  let turnStart = tsMs(opts.turnStart);
  let prevEnd = turnStart;
  const flush = (activeTail: boolean) => {
    if (!turn.length) return;
    if (activeTail && opts.live) out.push(...groupActivityRuns(turn, turnStart, true, ad));
    else out.push(...(groupAnsweredTurn(turn, turnStart, ad) ?? groupActivityRuns(turn, turnStart, false, ad)));
    turn = [];
  };
  for (const s of steps) {
    const start = stepStart(s);
    if (prevEnd !== null && start !== null && start - prevEnd > HISTORY_GAP_MS) {
      flush(false);
      turnStart = null; // what came before the gap cannot start this part's clock
    }
    turn.push(s);
    const end = stepEnd(s);
    if (end !== null) prevEnd = prevEnd === null ? end : Math.max(prevEnd, end);
  }
  flush(true);
  return out;
}

/** The plan of an exchange: only its latest TodoWrite of two or more items (that the tool took) draws a card; the
 *  others are not drawn. */
export function planSteps(steps: Step[]): Step[] {
  let latest = -1;
  // a TodoWrite the tool refused did not change the plan (#5602)
  steps.forEach((s, i) => isPlanStep(s) && !(s as ToolStep).result?.is_error && planTodos(s as ToolStep).length >= 2 && (latest = i));
  return steps.filter((s, i) => !isPlanStep(s) || i === latest);
}

export interface Todo {
  content: string;
  status: "pending" | "in_progress" | "completed";
  activeForm?: string;
}

export function planTodos(s: ToolStep): Todo[] {
  const todos = (s.use.tool_input as { todos?: unknown } | undefined)?.todos;
  if (!Array.isArray(todos)) return [];
  return todos.flatMap((t) => {
    if (!t || typeof t !== "object") return [];
    const o = t as Record<string, unknown>;
    const content = typeof o.content === "string" ? o.content : typeof o.activeForm === "string" ? o.activeForm : "";
    const status = o.status === "completed" || o.status === "in_progress" ? o.status : "pending";
    return content ? [{ content, status, ...(typeof o.activeForm === "string" ? { activeForm: o.activeForm } : {}) }] : [];
  });
}

/** done / total, the current step (in progress, else the next pending, else the last), and its 1-based number. */
export function planProgress(todos: Todo[]): { done: number; total: number; current: string; step: number; allDone: boolean } {
  const done = todos.filter((t) => t.status === "completed").length;
  const total = todos.length;
  const active = todos.findIndex((t) => t.status === "in_progress");
  const pending = todos.findIndex((t) => t.status === "pending");
  const idx = active >= 0 ? active : pending >= 0 ? pending : Math.max(0, total - 1);
  const allDone = total > 0 && done >= total;
  return { done, total, current: todos[idx]?.content ?? "", step: allDone ? total : idx + 1, allDone };
}

// ── what a group says and shows ──

type ExplorationKind = "read" | "search" | "list";

/** Which reading a tool call is (cindy's descriptor kinds): a file read, a search, a listing; anything else is not. */
function explorationOf(s: ToolStep): { kind: ExplorationKind; key?: string } | undefined {
  const name = s.use.tool_name ?? "";
  const inp = (s.use.tool_input ?? {}) as Record<string, unknown>;
  if (name === "Read") return { kind: "read", key: typeof inp.file_path === "string" ? inp.file_path : undefined };
  if (name === "Grep" || name === "Glob") return { kind: "search" };
  if (name === "LS") return { kind: "list" };
  return undefined;
}

// a workflow's call counts too: a group that launched one did more than read
const directTools = (g: WorkGroupNode): ToolStep[] => g.children.flatMap((c) => (c.kind === "step" && (isRowStep(c.step) || isWorkflowStep(c.step)) ? [c.step] : []));

/** "Read 3 files · searched 2 times" when a group did nothing but read and search (a file read twice counts once). */
export function explorationSummary(g: WorkGroupNode): string {
  const tools = directTools(g);
  if (!tools.length) return "";
  const reads = new Set<string>();
  let anon = 0;
  let search = 0;
  let list = 0;
  for (const t of tools) {
    const e = explorationOf(t);
    if (!e) return "";
    if (e.kind === "read") e.key ? reads.add(e.key) : anon++;
    else if (e.kind === "search") search++;
    else list++;
  }
  const read = reads.size + anon;
  return [read ? t("lib-work-groups.read_files", { count: read }) : "", search ? t("lib-work-groups.searches", { count: search }) : "", list ? t("lib-work-groups.listings", { count: list }) : ""].filter(Boolean).join(" · ");
}

/** Newest steps, with the latest narration kept at the end even when more tool calls arrive. */
export function previewSteps(g: WorkGroupNode, limit = LIVE_PREVIEW): Step[] {
  if (limit <= 0) return [];
  let narration: Step | undefined;
  for (let i = g.children.length - 1; i >= 0; i--) {
    const c = g.children[i];
    if (c.kind === "step") {
      if (c.step.kind === "thinking" && c.step.item.text?.trim()) {
        narration = c.step;
        break;
      }
      if (c.step.kind === "fold" && c.step.item.fold?.latest_thinking?.text?.trim()) {
        const item = c.step.item.fold.latest_thinking;
        narration = { kind: "thinking", id: item.id, item };
        break;
      }
    }
  }
  const out: Step[] = [];
  const seen = new Set<string>();
  for (let i = g.children.length - 1; i >= 0 && out.length < limit; i--) {
    const c = g.children[i];
    if (c.kind !== "step") continue;
    const s = c.step;
    if (s.kind === "thinking") {
      if (s.item.text?.trim()) out.push(s);
      continue;
    }
    if (s.kind !== "tool" || isAgentStep(s)) continue;
    const e = explorationOf(s);
    if (!e || e.kind !== "read") seen.clear(); // like cindy: another kind of step starts reading afresh
    else if (e.key) {
      if (seen.has(e.key)) continue;
      seen.add(e.key);
    }
    out.push(s);
  }
  if (narration && !out.includes(narration)) {
    if (out.length === limit) out.pop();
    out.push(narration);
  }
  return [...out.reverse().filter((s) => s !== narration), ...(narration ? [narration] : [])];
}

/** The groups holding the tool call `call` (its tool_use_id), outer first, down to the one it sits in: what opens to
 *  bring it into view (a background task's "Jump to the step that started it", iOS ChatView.jumpToCall). Empty: it is in the open; null:
 *  not among these nodes. */
export function groupsHolding(nodes: WorkNode[], call: string): string[] | null {
  for (const n of nodes) {
    if (n.kind === "step") {
      if (n.step.kind === "tool" && n.step.use.tool_use_id === call) return [];
      continue;
    }
    const inner = groupsHolding(n.children, call);
    if (inner) return [n.id, ...inner];
  }
  return null;
}

/** The runs of work the hub folded (fold_work) that hold the call of a workflow run it lists (#3434): its card belongs in
 *  the open, so the chat fetches what those folds stand for. A fold names no calls, so it is told by time: the run's
 *  start is its call's own time (the node takes it from the call's line), which falls within [fold.ts, fold.until] of
 *  the fold holding it. Two folds can both hold that instant (the lines of one message split by its words): both are
 *  fetched rather than the wrong one. `calls`: the runs' calls and start times (ms); a call among the steps needs nothing. */
export function foldsHoldingCalls(steps: Step[], calls: ReadonlyMap<string, number>): Item[] {
  if (!calls.size) return [];
  const present = new Set(steps.flatMap((s) => (s.kind === "tool" && s.use.tool_use_id ? [s.use.tool_use_id] : [])));
  const out: Item[] = [];
  for (const [call, at] of calls) {
    if (present.has(call) || !Number.isFinite(at)) continue;
    for (const s of steps) {
      if (s.kind !== "fold") continue;
      const from = tsMs(s.item.ts);
      const until = tsMs(s.item.fold?.until) ?? from;
      if (from !== null && until !== null && at >= from && at <= until && !out.includes(s.item)) out.push(s.item);
    }
  }
  return out;
}

/** Whether a live group opens: it holds more than its preview shows. A finished one always does. */
export function groupOpens(g: WorkGroupNode): boolean {
  if (!g.live) return true;
  const beyond = g.children.some((c) => c.kind === "group" || (c.kind === "step" && !(c.step.kind === "thinking" || (c.step.kind === "tool" && !isAgentStep(c.step)))));
  return beyond || previewSteps(g, LIVE_PREVIEW + 1).length > LIVE_PREVIEW;
}

/** How a chat opens its work lines (#1614). "agents" (a coding agent's chat): folded until the reader opens one, as they
 *  left it. "assistant" (a Persona conversation): while the exchange runs its lines are open and list each step as it
 *  comes, unless the reader folded one during the run; once it ends they fold back to their summary, whatever the
 *  reader did during the run, and from then on a tap opens and closes them as in "agents". */
export type WorkLineStyle = "agents" | "assistant";

/** A chat's work line style: "assistant" on the assistant's page, or for a Persona conversation opened anywhere else
 *  (the Agents page's link to it); "agents" for a coding agent's chat. */
export const workLineStyle = (assistantView?: boolean, assistantSession?: boolean): WorkLineStyle => (assistantView || assistantSession ? "assistant" : "agents");

export interface WorkLineState {
  style: WorkLineStyle;
  /** The exchange this line is of is still running. */
  turnLive: boolean;
  /** What the reader left it at outside a run (expanded.ts), or its default. */
  remembered: boolean;
  /** What the reader set it to during this run, if anything (assistant style only; dropped when the run ends). */
  liveOverride?: boolean;
}

/** Whether a work line is open (before groupOpens: a line with nothing to open stays shut either way). */
export function workLineOpen(s: WorkLineState): boolean {
  if (s.style === "assistant" && s.turnLive) return s.liveOverride ?? true;
  return s.remembered;
}

/** Whether the toggle of a line goes to the run's override (kept only while the exchange runs) or to what is remembered. */
export const togglesLiveOverride = (style: WorkLineStyle, turnLive: boolean): boolean => style === "assistant" && turnLive;

/** Whether a line can be opened or folded: groupOpens, and in a running assistant exchange any line with steps (its
 *  steps are listed open, so the reader can fold it). */
export function workLineOpens(g: WorkGroupNode, style: WorkLineStyle, turnLive: boolean): boolean {
  return groupOpens(g) || (togglesLiveOverride(style, turnLive) && g.children.length > 0);
}

/** A finished timeline's inner lines: folded in "agents"; open in "assistant", so opening the summary lists every call. */
export const nestedLineDefault = (style: WorkLineStyle): boolean => style === "assistant";

/** How long each thought took: from the step before it (or the exchange's start) to when it was written. */
export function thinkingDurations(steps: Step[], turnStart?: string): Map<string, number> {
  const out = new Map<string, number>();
  let prev = tsMs(turnStart);
  for (const s of steps) {
    const st = stepStart(s);
    if (s.kind === "thinking" && st !== null && prev !== null && st >= prev) out.set(s.id, st - prev);
    const e = stepEnd(s);
    if (e !== null) prev = prev === null ? e : Math.max(prev, e);
  }
  return out;
}

/** cindy's formatDuration in the page's language: Chinese "6 s", "1 min 5 s", "2 min", "1 h 05 min", 6s, 1m 5s in English (at least a second). */
export function workDuration(msValue: number): string {
  const total = Math.max(1, Math.round((Number.isFinite(msValue) ? msValue : 0) / 1000));
  // Units come from the catalog (#3592): "1 min 5 s" in Chinese, 1m 5s in English.
  if (total < 60) return t("lib-work-groups.dur_seconds", { s: total });
  if (total >= 3600) {
    const d = Math.floor(total / 86_400);
    const h = Math.floor((total % 86_400) / 3600);
    const m = Math.floor((total % 3600) / 60);
    const pad = (n: number) => String(n).padStart(2, "0");
    if (d > 0) return t("lib-work-groups.dur_days_hours_minutes", { d, h: pad(h), m: pad(m) });
    return m ? t("lib-work-groups.dur_hours_minutes", { h, m: pad(m) }) : t("lib-work-groups.dur_hours", { h });
  }
  const m = Math.floor(total / 60);
  const s = total % 60;
  return s ? t("lib-work-groups.dur_minutes_seconds", { m, s }) : t("lib-work-groups.dur_minutes", { m });
}

/** A finished group's title: "Worked 1m 5s", and what it read when that is all it did. */
export function groupTitle(g: WorkGroupNode): string {
  if (g.live) return t("lib-work-groups.working");
  const base = g.durationMs !== undefined ? t("lib-work-groups.worked_for", { duration: workDuration(g.durationMs) }) : t("lib-work-groups.work");
  const ex = explorationSummary(g);
  return ex ? `${base} · ${ex}` : base;
}
