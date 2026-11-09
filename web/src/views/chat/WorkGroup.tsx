// An exchange's work as lib/work-groups.ts groups it (cindy's WorkGroupBlock): "Working…" with a clock and its
// newest steps peeking out while it runs, "Worked for Xs" once done; open, every step on a 2 px rail. A finished
// exchange's outer group is a timeline: the agent's words between inner groups that stay folded.
import { memo, useContext, useEffect, useLayoutEffect, useMemo, useState } from "react";
import { useSessionLive } from "../../lib/live";
import { useStore } from "../../lib/store";
import { subagentFor } from "../../lib/tasks";
import type { Item } from "../../lib/types";
import { groupTitle, nestedLineDefault, togglesLiveOverride, workLineOpen, workLineOpens, isAgentStep, isPlanStep, isRowStep, isWorkflowStep, planTodos, previewSteps, workDuration, type AgentStatus, type ToolStep, type WorkGroupNode, type WorkLineStyle, type WorkNode } from "../../lib/work-groups";
import type { WorkflowCalls } from "../../lib/workflows";
import type { Step } from "../../lib/turns";
import { Collapse, cx, Spinner, useNow } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { ActionRow } from "./ActionRow";
import { FreshWords, LiveClockContext, useChatContext } from "./chat-context";
import { inOpenedFold, openFoldRange, useExpanded, useLiveOverride } from "./expanded";
import { useReveal } from "./search-reveal";
import { nodeItemIds } from "../../lib/chat-search";
import { PlanCard } from "./PlanCard";
import { Answer } from "./SegmentView";
import { NoticeRow, Thumb } from "./Steps";
import { SubagentCard } from "./SubagentCard";
import { WorkflowCallCard } from "./WorkflowCard";
import { ThinkingCard, ThinkingLine } from "./ThinkingCard";
import { t } from "../../i18n";

export interface WorkProps {
  mid: string;
  sid: string;
  /** Keys the reader's open / closed state (expanded.ts) to this chat. */
  scope: string;
  expandFold: (f: Item) => Promise<void>;
  agentStatus: (s: ToolStep) => AgentStatus;
  thinking: Map<string, number>;
  /** The Workflow calls whose run the hub lists (#3434): those draw the run's card. */
  workflowCalls?: WorkflowCalls;
  /** Inside an open group (a finished timeline's inner groups stay folded). */
  nested?: boolean;
  /** The exchange these steps are of (its segment's id): what of it the reader opened is kept under it. */
  segment?: string;
  /** Show every tool's original input/result in place in an opened supervision trace. */
  trace?: boolean;
  /** How its work lines open (work-groups.ts workLineOpen): "assistant" in a Persona conversation; "agents" when unset. */
  lineStyle?: WorkLineStyle;
  /** The exchange these steps are of is still running. */
  turnLive?: boolean;
}

/** A Workflow call that draws its run's card (the hub lists the run); one that does not is the row it always was. */
const workflowCard = (s: ToolStep, p: WorkProps) => isWorkflowStep(s) && Boolean(s.use.tool_use_id && p.workflowCalls?.known.has(s.use.tool_use_id));

/** A step drawn as an action row, sitting with the rows next to it. */
const drawnAsRow = (s: Step, p: WorkProps): s is ToolStep => (p.trace && s.kind === "tool") || isRowStep(s) || (isWorkflowStep(s) && !workflowCard(s, p));

/** Every fold under a group, nested ones too: opening it fetches their steps. */
function foldsOf(g: WorkGroupNode): Item[] {
  return g.children.flatMap((c) => (c.kind === "group" ? foldsOf(c) : c.step.kind === "fold" ? [c.step.item] : []));
}

/** The screen's status line while this exchange is live (contract §3): a thinking card at the live group's end. Read
 *  here, not handed down: it changes about once a second while the agent works, and only this card shows it (#5417). */
function useThinkingNow(mid: string, sid: string) {
  const status = useSessionLive(mid, sid);
  return status?.thinking ? <ThinkingCard live={status} /> : null;
}

/** A live group's newest steps, peeking out while it is closed, and the thinking card after them. */
function LivePreview({ preview, open, mid, sid, trace }: { preview: Step[]; open: boolean; mid: string; sid: string; trace?: boolean }) {
  const thinkingNow = useThinkingNow(mid, sid);
  if (!(preview.length > 0 || (thinkingNow && !open))) return null;
  return (
    <div className="mt-1 flex min-w-0 flex-col border-l-2 border-tool-rail py-[2px] pl-3" data-live-preview>
      {preview.map((s) => (s.kind === "tool" ? <ActionRow key={s.id} step={s} running={!s.result} mid={mid} sid={sid} trace={trace} /> : <ThinkingLine key={s.id} item={s.item} />))}
      {thinkingNow}
    </div>
  );
}

function LiveThinking({ mid, sid }: { mid: string; sid: string }) {
  return useThinkingNow(mid, sid);
}

export const WorkGroup = memo(function WorkGroup({ g, p }: { g: WorkGroupNode; p: WorkProps }) {
  // an outer group whose folded steps the reader just opened: open under whatever key it has once they are in
  const style = p.lineStyle ?? "agents";
  const turnLive = Boolean(p.turnLive);
  const key = `${p.scope}${g.id}`;
  const [remembered, setRemembered] = useExpanded(key, p.nested ? nestedLineDefault(style) : inOpenedFold(p.scope, g.seq));
  // in a running assistant exchange the reader's toggle holds only for the run (#1614): it goes elsewhere
  const runToggle = togglesLiveOverride(style, turnLive);
  const [liveOverride, setLiveOverride] = useLiveOverride(key, runToggle);
  const setOpen = runToggle ? setLiveOverride : setRemembered;
  const [loading, setLoading] = useState(false);
  const opens = workLineOpens(g, style, turnLive);
  const open = workLineOpen({ style, turnLive, remembered, liveOverride }) && opens;
  // a line the reader folded during a running assistant exchange is just its summary: no steps peek out (#1614)
  const preview = g.live && !open && !(runToggle && liveOverride === false) ? previewSteps(g) : [];
  const inside = useMemo(() => ({ ...p, nested: true }), [p]);
  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next) for (const f of foldsOf(g)) if (f.fold) openFoldRange(p.scope, f.seq, f.fold.through_seq);
  };
  // a search hit inside it (search-reveal.ts): it opens, and "Back to where you were" closes it again
  const ids = useMemo(() => nodeItemIds(g.children), [g.children]);
  useReveal(p.scope, ids, () => {
    if (open || !opens) return;
    toggle();
    return () => setOpen(false);
  });
  // an open group's folded steps are fetched: opened just now, or remembered open when the chat comes back (#1996: it
  // stayed at "Loading N steps" then); once in, the folds are gone and this has nothing left to do. The outer group fetches
  // its nested groups' folds too.
  const folds = useMemo(() => (open && !p.nested ? foldsOf(g) : []), [open, p.nested, g]);
  const foldKey = folds.map((f) => f.id).join(" ");
  const expandFold = p.expandFold;
  // a fetch that failed (the hub restarting, the network): the group says so, and "Retry" runs it again
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    // every fold is in: done. A run cut short by the last fold coming in (its key changed) does not get to say so, and
    // the header's spinner kept spinning (#1614)
    if (!foldKey) return setLoading(false);
    let live = true;
    setLoading(true);
    setFailed(false);
    folds
      // each fold that comes in runs this again with the rest: this run stops there
      .reduce((done, f) => done.then(() => (live ? expandFold(f) : undefined)), Promise.resolve())
      .catch(() => live && setFailed(true))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
    // the folds by their ids: a new `g` with the same folds is not a new fetch
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [foldKey, expandFold, attempt]);
  return (
    <div className="flex w-full min-w-0 flex-col" data-work-group={g.live ? "live" : "done"} data-group-id={g.id}>
      <button
        type="button"
        onClick={opens ? toggle : undefined}
        disabled={!opens}
        aria-expanded={opens ? open || preview.length > 0 : undefined}
        // on a touch screen 40 tall, the margin taking back what it adds, so nothing around it moves
        className={cx("flex w-full min-w-0 items-center gap-1.5 py-[2px] text-left select-none [@media(pointer:coarse)]:-my-[7.5px] [@media(pointer:coarse)]:min-h-10", opens ? "cursor-pointer transition-opacity hover:opacity-80" : "cursor-default")}
      >
        <span className="inline-flex h-[1lh] shrink-0 items-center text-tool-ink">{g.live ? <Spinner size={14} /> : <Icon name="layers" size={14} />}</span>
        <span className="min-w-0 translate-y-[1px] truncate text-[length:var(--chat-secondary)] text-tool-ink">{groupTitle(g)}</span>
        <span className="flex-1" />
        {loading && <Spinner size={11} />}
        {g.live && g.startedAt !== undefined && <Elapsed since={g.startedAt} />}
        {opens && <Icon name="chevronRight" size={14} stroke={2.2} className="shrink-0 text-tool-ink transition-transform duration-150 motion-reduce:transition-none" style={{ transform: open ? "rotate(90deg)" : "none" }} />}
      </button>
      {open && failed && !loading && (
        <div className="mt-1 flex items-center gap-2 pl-[22px] text-[12px] text-ink-3" role="status">
          {t("chat-workgroup.load_failed")}
          <button type="button" onClick={() => setAttempt((n) => n + 1)} className="touch-hit rounded-control px-1.5 py-0.5 font-medium whitespace-nowrap text-accent-ink hover:bg-hover">{t("chat-workgroup.retry")}</button>
        </div>
      )}
      {/* only a live group has a preview or a thinking card (previewSteps is empty otherwise) */}
      {g.live && <LivePreview preview={preview} open={open} mid={p.mid} sid={p.sid} trace={p.trace} />}
      <Collapse open={open} lazy>
        <div className="mt-1 flex min-w-0 flex-col gap-2 border-l-2 border-tool-rail py-[2px] pl-3" data-work-detail>
          <WorkNodes nodes={g.children} p={inside} live={g.live} />
          {g.live && <LiveThinking mid={p.mid} sid={p.sid} />}
        </div>
      </Collapse>
    </div>
  );
});

/** Nodes in order; tool calls next to each other sit together without a gap, like cindy's tool segments. `fade`: the
 *  agent's words among them that just came in fade in (FreshWords); not inside a group. */
export function WorkNodes({ nodes, p, live, fade }: { nodes: WorkNode[]; p: WorkProps; live?: boolean; fade?: boolean }) {
  const runs: (WorkNode | ToolStep[] | AgentRun)[] = [];
  for (const n of nodes) {
    const last = runs[runs.length - 1];
    if (n.kind === "step" && drawnAsRow(n.step, p)) {
      if (Array.isArray(last)) last.push(n.step);
      else runs.push([n.step]);
    } else if (n.kind === "step" && isAgentStep(n.step)) {
      // subagents launched one after another (in parallel) sit together in one grid (#5602)
      if (last && "agents" in last) last.agents.push(n.step as ToolStep);
      else runs.push({ agents: [n.step as ToolStep] });
    } else runs.push(n);
  }
  return (
    <>
      {runs.map((run) =>
        "agents" in run ? (
          run.agents.length === 1 ? (
            <StepView key={run.agents[0].id} step={run.agents[0]} p={p} live={live} fade={fade} />
          ) : (
            <AgentGrid key={run.agents[0].id} agents={run.agents} p={p} />
          )
        ) : Array.isArray(run) ? (
          <div key={run[0].id} className="flex min-w-0 flex-col">
            {run.map((s) => (
              <ActionRow key={s.id} step={s} running={live && !s.result} mid={p.mid} sid={p.sid} trace={p.trace} />
            ))}
          </div>
        ) : run.kind === "group" ? (
          <WorkGroup key={run.id} g={run} p={p} />
        ) : (
          <StepView key={run.step.id} step={run.step} p={p} live={live} fade={fade} />
        ),
      )}
    </>
  );
}

interface AgentRun {
  agents: ToolStep[];
}

/** Subagents launched together: "Running N agents" while any runs, "Ran N agents · M steps" after; their cards in a
 *  grid, two to a row where there is room, in the order they were launched. */
function AgentGrid({ agents, p }: { agents: ToolStep[]; p: WorkProps }) {
  const { subagents } = useChatContext();
  const statuses = agents.map((a) => p.agentStatus(a));
  const running = statuses.filter((s) => s === "running").length;
  const steps = agents.reduce((n, a) => n + (subagentFor(subagents, a.use)?.steps ?? 0), 0);
  const head = running
    ? t("chat-subagentcard.agents_running", { count: running })
    : [t("chat-subagentcard.agents_ran", { count: agents.length }), steps ? t("chat-subagentcard.agents_steps", { count: steps }) : ""].filter(Boolean).join(" · ");
  return (
    <div className="@container flex min-w-0 flex-col gap-1.5" data-subagent-grid={agents.length}>
      <div className="flex items-center gap-1.5 text-[12px] leading-[1.333333] text-ink-2">
        {running ? <Spinner size={11} /> : <Icon name="agent" size={12} />}
        <span className="min-w-0 truncate">{head}</span>
      </div>
      <div className="grid min-w-0 grid-cols-1 gap-2 @[560px]:grid-cols-2">
        {agents.map((a, i) => (
          <SubagentCard key={a.id} step={a} status={statuses[i]} mid={p.mid} scope={p.scope} />
        ))}
      </div>
    </div>
  );
}

/** Words that fade in if they just came in: the only reader of FreshWords, so a new reply renders these, not the chat.
 *  Memo: a streamed delta re-renders the segment, not these; FreshWords still reaches them through the context. */
const FreshAnswer = memo(function FreshAnswer({ item, mid, sid }: { item: Item; mid: string; sid: string }) {
  const fresh = useContext(FreshWords);
  return <Answer item={item} mid={mid} sid={sid} fade={fresh?.has(item.id)} />;
});

/** A step on its own: in the open, or inside an open group. Memo: its props stay the same objects while the reply
 *  streams (SegmentView memoizes steps and p on the segment), so a delta skips every step of the turn. */
export const StepView = memo(function StepView({ step, p, live, fade }: { step: Step; p: WorkProps; live?: boolean; fade?: boolean }) {
  switch (step.kind) {
    case "tool":
      if (p.trace) return <ActionRow step={step} running={live && !step.result} mid={p.mid} sid={p.sid} trace />;
      if (isAgentStep(step)) return <SubagentCard step={step} status={p.agentStatus(step)} mid={p.mid} scope={p.scope} />;
      if (isPlanStep(step)) return <PlanCard id={step.id} todos={planTodos(step)} live={live} mid={p.mid} sid={p.sid} />;
      if (workflowCard(step, p)) return <WorkflowCallCard step={step} mid={p.mid} sid={p.sid} scope={p.scope} />;
      return <ActionRow step={step} running={live && !step.result} mid={p.mid} sid={p.sid} />;
    case "thinking":
      return <ThinkingCard item={step.item} durationMs={p.thinking.get(step.id)} scope={p.scope} />;
    case "text":
      return fade ? <FreshAnswer item={step.item} mid={p.mid} sid={p.sid} /> : <Answer item={step.item} mid={p.mid} sid={p.sid} />;
    case "notice":
      return <NoticeRow item={step.item} mid={p.mid} sid={p.sid} />;
    case "image":
      return (
        <div className="py-1">
          <Thumb item={step.item} mid={p.mid} sid={p.sid} />
        </div>
      );
    case "fold":
      return (
        <div className="flex h-7 items-center gap-2 px-2 text-[13px] text-tool-ink">
          <Spinner size={11} /> {t("chat-workgroup.loading_steps", { count: step.item.fold?.items ?? 0 })}
        </div>
      );
  }
});

/** A live group's clock, every 500 ms; its own leaf, so the ticking redraws nothing else. While it shows, the running
 *  bar shows no time of its own (#3534). */
function Elapsed({ since }: { since: number }) {
  // stands still with the hub out of reach (the running bar says so): nothing can be known to go on
  const now = useNow(500, useStore((s) => s.conn === "online"));
  const count = useContext(LiveClockContext);
  // before paint: the bar's time goes in the same frame this one comes in
  useLayoutEffect(() => {
    count?.(1);
    return () => count?.(-1);
  }, [count]);
  return <span className="shrink-0 font-mono text-[12px] text-tool-ink tabular-nums">{workDuration(Math.max(0, now - since))}</span>;
}
