import { Fragment, memo, type ReactNode, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { clock } from "../../lib/format";
import { getSession, getState } from "../../lib/store";
import { agentStatus, subagentFor } from "../../lib/tasks";
import { turnImages } from "../../lib/turn-images";
import { TurnImages } from "./TurnImages";
import { exchangeSteps, isLoopTick, isOwnMessage, supervisionTool, type Segment, type Step, type UserMessage } from "../../lib/turns";
import type { Item } from "../../lib/types";
import { foldsHoldingCalls, planSteps, thinkingDurations, workBlocks, type ToolStep } from "../../lib/work-groups";
import { NO_WORKFLOW_CALLS } from "../../lib/workflows";
import { channelOf, useChannels } from "../../lib/channels";
import { splitQuotes } from "../../lib/drafts";
import { EASE } from "../../ui/atoms";
import { ChannelMark } from "../../ui/ChannelMark";
import { Markdown } from "../../ui/Markdown";
import { CardConversation, CardItem } from "../../ui/AssistantCard";
import { cardReply } from "../../lib/assistant-blocks";
import { InjectedHint, LoopTick, MergedBubble, PeerMessage } from "./Messages";
import { SourceMessage } from "./SourceMessage";
import { ContextGroup } from "./ContextGroup";
import { AttachedPhotos } from "./AttachedPhotos";
import { useFullText } from "./FullText";
import { FileCards, ReplyFiles } from "./FileCards";
import { MessageActions } from "./MessageActions";
import { QuoteStack } from "./Quote";
import { PathText } from "../../ui/PathLinks";
import { Thumb } from "./Steps";
import { useFoldScroll } from "./fold-scroll";
import { useChatContext } from "./chat-context";
import { Icon } from "../../ui/icons";
import { WorkGroup, WorkNodes, type WorkProps } from "./WorkGroup";
import { useCallSeeker } from "./jump";
import { t } from "../../i18n";

/** A message longer than this many lines folds to ten of them (FOLDED), with "Expand" under it (the reference client's UserMessage). */
export const BUBBLE_MAX_LINES = 14;
/** Before it is measured: about this many characters to a line of the 488 px bubble. */
const CHARS_PER_LINE = 30;
/** A folded message: ten lines high, scrolling inside (the chat scrolls on at its top and end). */
const FOLDED = "max-h-[10lh] overflow-y-auto";

/** How many lines a message takes in its bubble, by its text (the bubble measures itself once drawn). */
export function bubbleLines(text: string, perLine = CHARS_PER_LINE): number {
  return text.split("\n").reduce((n, l) => n + Math.max(1, Math.ceil([...l].length / perLine)), 0);
}

/** `sendState`: a message not in the transcript yet (a sending bubble, the outbox's), marked for the sync scenarios
 *  (spec/fixtures/sync); the transcript's own bubbles have none. `starting`: the first message of a conversation still
 *  starting (the new-conversation page, #1697): drawn as it will be once sent, without actions (there is nothing yet to
 *  act on) and without "Sending…" (the page says it is preparing). */
export function UserBubble({ msg, mid, sid, pending, sendState, starting }: { msg: UserMessage; mid: string; sid: string; pending?: boolean; sendState?: "sending" | "sent" | "failed"; starting?: boolean }) {
  const own = isOwnMessage(msg);
  return (
    <div className="ml-auto flex max-w-[488px] min-w-0 flex-col items-end gap-1.5 pl-[12%]" style={pending ? { animation: `fade-up 300ms ${EASE} both` } : undefined} data-user-bubble data-send-state={sendState} data-items={pending || starting ? undefined : [msg.id, ...msg.images.map((i) => i.id)].join(" ")}>
      {msg.images.length > 0 && (
        <div className="flex flex-wrap justify-end gap-1.5">
          {msg.images.map((im) => (
            <Thumb key={im.id} item={im} mid={mid} sid={sid} size={96} />
          ))}
        </div>
      )}
      {msg.photos && <AttachedPhotos paths={msg.photos} mid={mid} sid={sid} />}
      {msg.contexts.length > 0 && <ContextGroup contexts={msg.contexts} />}
      {msg.files && <FileCards files={msg.files} mid={mid} />}
      {msg.text &&
        (starting ? (
          <BubbleText text={msg.text} mid={mid} sid={sid} own={own} />
        ) : pending ? (
          <BubbleText text={msg.text} mid={mid} sid={sid} own={own} pending />
        ) : (
          <MessageActions text={msg.text} isUser ts={msg.ts} side messageId={msg.id} className="max-w-full">
            <BubbleText text={msg.text} ts={msg.ts} mid={mid} sid={sid} own={own} />
          </MessageActions>
        ))}
      {(pending || msg.queued) && <span className="text-[11px] text-ink-3">{pending ? t("chat-segmentview.sending") : t("chat-segmentview.queued")}</span>}
      {!pending && !starting && <ChannelSource clientMsgId={msg.client_msg_id} />}
    </div>
  );
}

/** The bubble's words, folded to ten lines past fourteen; the fold scrolls (the wheel, a trackpad, a finger), with a
 *  fade at its bottom while there is more (fold-scroll.ts). `pending` (still being sent): dimmed, the same fold. A
 *  leading `> ` block ("Quote") is a folded quote card at its top instead of `>` lines (Quote.tsx). */
function BubbleText({ text: full, ts, mid, sid, own, pending }: { text: string; ts?: string; mid: string; sid: string; own: boolean; pending?: boolean }) {
  const fill = own ? "bg-user-bubble" : "bg-neutral-bubble";
  // a reply sent from a card (in an assistant conversation): its words, without the hub's tag, marked "From a card"
  const fromCard = useContext(CardConversation) ? cardReply(full) : undefined;
  const whole = fromCard ? fromCard.text : full;
  const split = useMemo(() => splitQuotes(whole), [whole]);
  const text = split ? split.rest : whole;
  const ref = useRef<HTMLDivElement>(null);
  const [long, setLong] = useState(() => bubbleLines(text) > BUBBLE_MAX_LINES);
  const [open, setOpen] = useState(false);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || open) return;
    // its full height in lines (a fold's scrollHeight is all of it)
    const line = parseFloat(getComputedStyle(el).lineHeight) || 24;
    setLong(el.scrollHeight / line > BUBBLE_MAX_LINES + 0.5);
  }, [text, open]);
  const folded = long && !open;
  const fold = useFoldScroll(ref, folded, [text]);
  const words = text && (
    <div
      ref={ref}
      {...fold}
      className={`text-[length:var(--chat-body)] leading-[1.6] whitespace-pre-wrap [overflow-wrap:anywhere] ${folded ? FOLDED : ""}`}
      data-folded={folded || undefined}
      data-bubble-text
    >
      <PathText text={text} mid={mid} cwd={getSession(mid, sid)?.cwd} />
    </div>
  );
  return (
    <div className="flex max-w-full flex-col items-end gap-1" data-from-card={fromCard ? fromCard.view : undefined}>
      {fromCard && (
        <span className="flex items-center gap-1 text-[11px] font-medium whitespace-nowrap text-accent">
          <Icon name="check" size={11} />
          {t("ui-assistantcard.from_card")}
        </span>
      )}
      {split ? (
        <div className={`flex max-w-full flex-col gap-1.5 rounded-[12px] ${fill} px-4 py-2.5 text-ink ${pending ? "opacity-60" : ""}`} title={clock(ts)}>
          <QuoteStack blocks={split.quotes} mid={mid} sid={sid} />
          {words}
        </div>
      ) : (
        <div className={`max-w-full rounded-[12px] ${fill} px-4 py-2.5 text-ink ${pending ? "opacity-60" : ""}`} title={clock(ts)}>
          {words}
        </div>
      )}
      {long && (
        <button type="button" onClick={() => setOpen((o) => !o)} aria-expanded={open} className="rounded-[6px] px-1.5 py-0.5 text-[12px] font-medium whitespace-nowrap text-ink-3 transition-colors hover:bg-hover-2 hover:text-ink-2">
          {open ? t("chat-segmentview.collapse") : t("chat-segmentview.expand")}
        </button>
      )}
    </div>
  );
}

/** "From WeChat" under a message that came from a channel (its client_msg_id is `<channel>-…`, docs/extensions/channels.md). */
function ChannelSource({ clientMsgId }: { clientMsgId?: string }) {
  const via = channelOf(clientMsgId, useChannels());
  if (!via) return null;
  return (
    <span className="flex items-center gap-1.5 whitespace-nowrap text-[11px] text-ink-3">
      <ChannelMark c={via} size={14} />
      {t("chat-segmentview.from", { name: via.name })}
    </span>
  );
}

/** An answer's prose at the chat's body size, 1.6 lines. Important: styles.css's `.prose-j` (14 / 1.7) is outside
 *  Tailwind's layers, so it would win over a plain utility. */
const ANSWER = "[&_.prose-j]:text-[length:var(--chat-body)]! [&_.prose-j]:leading-[1.6]!";

/** What the agent said, without a bubble, the chat's body size (15, 16 on a phone) / 1.6. `fade`: it just came in, its
 *  words fade in once. Still streaming, it has the same menu (copy what is there so far, quote it).
 *  Memo'd: the chat context changes with every streamed item, so each SegmentView re-renders; a finished answer's
 *  props stay the same and it is skipped (#3024). */
export const Answer = memo(function Answer({ item, streaming, mid, sid, fade }: { item?: Item; streaming?: string; mid?: string; sid?: string; fade?: boolean }) {
  // a long answer the hub cut for the page: its start, and "View full text" under it
  const full = useFullText(streaming === undefined ? item : undefined, mid, sid);
  const text = streaming ?? full.text;
  if (!text) return null;
  const cwd = mid && sid ? getSession(mid, sid)?.cwd : undefined;
  if (streaming !== undefined) {
    return (
      <MessageActions text={text}>
        <div className={ANSWER} data-answer="streaming">
          <Markdown text={text} mid={mid} cwd={cwd} streaming />
        </div>
      </MessageActions>
    );
  }
  return (
    <MessageActions text={text} ts={item?.ts} itemId={item?.id}>
      <div className={ANSWER} data-answer data-items={item?.id}>
        {/* the cards in it are this item's blocks (ui/AssistantCard.tsx): `<item id>/<n>` */}
        <CardItem.Provider value={item?.id ? { id: item.id, text } : null}>
          <Markdown text={text} mid={mid} cwd={cwd} fade={fade || undefined} />
        </CardItem.Provider>
      </div>
      {full.more}
      {mid && sid && <ReplyFiles text={text} mid={mid} cwd={cwd} />}
    </MessageActions>
  );
});

export const SegmentView = memo(function SegmentView({
  seg,
  live,
  mid,
  sid,
  streaming,
  batchedWords,
  expandFold,
  beforeWords,
  trace,
}: {
  seg: Segment;
  live: boolean;
  mid: string;
  sid: string;
  streaming?: string;
  /** The live words are an agent's as Claude shows them (docs/integrations/claude-integration.md "Live replies"): a batch
   *  of lines at a time, and still while a tool runs. The live work row stays under them; the headless assistant's
   *  words stream as they are written and stand for it. */
  batchedWords?: boolean;
  expandFold: (f: Item) => Promise<void>;
  /** Drawn between the work and the agent's last words (the wakes chip, #3783); above the work while it is live. */
  beforeWords?: ReactNode;
  /** An opened supervision round shows its original input instead of the injected hint. */
  trace?: boolean;
}) {
  const { subagents, scope, workflowCalls = NO_WORKFLOW_CALLS, lineStyle } = useChatContext();
  const steps = useMemo(() => planSteps(exchangeSteps(seg)), [seg]);
  const turnStart = seg.user?.ts ?? seg.start;
  const thinking = useMemo(() => thinkingDurations(steps, turnStart), [steps, turnStart]);
  const p = useMemo<WorkProps>(() => {
    const agent = (s: ToolStep) => agentStatus(s.result, subagentFor(subagents, s.use), live);
    return { mid, sid, scope, expandFold, agentStatus: agent, thinking, segment: seg.id, workflowCalls, trace, lineStyle, turnLive: live };
  }, [mid, sid, scope, expandFold, subagents, live, thinking, seg.id, workflowCalls, trace, lineStyle]);
  const nodes = useMemo(
    () => workBlocks(steps, { live, turnStart, agentStatus: p.agentStatus, workflowCard: (s) => workflowCalls.known.has(s.use.tool_use_id ?? ""), keepTool: seg.supervision ? (s) => Boolean(supervisionTool(s.use.tool_name)) : undefined }),
    [steps, live, turnStart, p.agentStatus, workflowCalls, seg.supervision],
  );
  useCallSeeker(scope, nodes);
  useWorkflowFolds(steps, workflowCalls.startedAt, expandFold);
  // the pictures the agent looked at this turn, between its work and its last words
  const images = useMemo(() => turnImages(seg, getState().machines.find((m) => m.id === mid)?.name), [seg, mid]);
  // a finished exchange's last words (the run of them at its end) come after those pictures
  let cut = nodes.length;
  for (let n = nodes[cut - 1]; !live && n?.kind === "step" && n.step.kind === "text"; n = nodes[cut - 1]) cut--;
  const working = live && (streaming === undefined || batchedWords) && !nodes.some((n) => n.kind === "group" && n.live);
  const liveRow = working && <WorkGroup g={{ kind: "group", id: `work-live-${seg.id}`, children: [], live: true, startedAt: Date.parse(turnStart ?? "") || undefined }} p={p} />;
  const imageStrip = images.length > 0 && <TurnImages images={images} mid={mid} sid={sid} cwd={getSession(mid, sid)?.cwd} />;
  return (
    <section className="flex flex-col gap-3" data-segment>
      {trace && seg.user?.rawInput ? <RawInjectedInput items={seg.user.rawInput} mid={mid} sid={sid} /> : seg.user && (seg.user.source ? <SourceMessage msg={seg.user} mid={mid} sid={sid} /> : seg.user.injected ? <InjectedHint msg={seg.user} sid={sid} /> : seg.user.parts ? <MergedBubble msg={seg.user} mid={mid} sid={sid} /> : seg.user.from ? <PeerMessage msg={seg.user} /> : isLoopTick(seg.user) ? <LoopTick msg={seg.user} /> : <UserBubble msg={seg.user} mid={mid} sid={sid} />)}
      {/* while the agent works, its live work stays at the bottom */}
      {live && <Fragment key="before-words">{beforeWords}</Fragment>}
      <WorkNodes nodes={nodes.slice(0, cut)} p={p} live={live} fade />
      {/* working with no run of steps going (its words just came, or nothing yet): the live group still shows, under an
          agent's live words while they are there (batchedWords) */}
      {streaming === undefined && liveRow}
      {imageStrip}
      {!live && <Fragment key="before-words">{beforeWords}</Fragment>}
      <WorkNodes nodes={nodes.slice(cut)} p={p} fade />
      {streaming !== undefined && <Answer streaming={streaming} mid={mid} sid={sid} />}
      {streaming !== undefined && liveRow}
    </section>
  );
});

function RawInjectedInput({ items, mid, sid }: { items: Item[]; mid: string; sid: string }) {
  return (
    <div className="flex min-w-0 flex-col gap-1" data-supervision-input>
      <span className="text-[length:var(--chat-secondary)] text-ink-3">{t("chat-lines.supervision_input")}</span>
      {items.map((item) => item.kind === "image" ? <Thumb key={item.id} item={item} mid={mid} sid={sid} /> : <RawInjectedText key={item.id} item={item} mid={mid} sid={sid} />)}
    </div>
  );
}

function RawInjectedText({ item, mid, sid }: { item: Item; mid: string; sid: string }) {
  const full = useFullText(item, mid, sid);
  return (
    <div data-items={item.id}>
      <pre className="rounded-card bg-inset p-3 text-[length:var(--chat-secondary)] whitespace-pre-wrap text-ink-2 [overflow-wrap:anywhere]">{full.text}</pre>
      {full.more}
    </div>
  );
}

/** After a fold failed to come, the next try waits this long, doubling up to WORKFLOW_FOLD_RETRY_MAX_MS. */
const WORKFLOW_FOLD_RETRY_MS = 5000;
const WORKFLOW_FOLD_RETRY_MAX_MS = 60_000;

/** A workflow run's call the hub folded into this exchange's work (the page counted back from the end, #3434): its steps
 *  are fetched, so the run's card comes out of the fold and stays in the open. Kept by this exchange as drawn now, not
 *  for the page: a transcript loaded again (another chat and back, a rewrite) mounts it afresh and brings the same fold
 *  back, which is fetched again. A fold that came is gone from the steps; one that failed is asked for again later. */
function useWorkflowFolds(steps: Step[], calls: ReadonlyMap<string, number>, expandFold: (f: Item) => Promise<void>) {
  const folds = useMemo(() => foldsHoldingCalls(steps, calls), [steps, calls]);
  // asked for (on its way, or come: one that came back empty stays among the steps and is not asked for again)
  const asked = useRef(new Set<string>());
  const wait = useRef(WORKFLOW_FOLD_RETRY_MS);
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    for (const f of folds) {
      if (asked.current.has(f.id)) continue;
      asked.current.add(f.id);
      expandFold(f).then(
        () => void (wait.current = WORKFLOW_FOLD_RETRY_MS),
        () => {
          asked.current.delete(f.id);
          const ms = wait.current;
          wait.current = Math.min(ms * 2, WORKFLOW_FOLD_RETRY_MAX_MS);
          window.setTimeout(() => setRetry((n) => n + 1), ms);
        },
      );
    }
  }, [folds, expandFold, retry]);
}
