// What the voice bar says (docs/assistant/voice.md 6.2), as iOS's VoiceBarLine.swift: the first line beside the orb, and the
// second line about the bound conversation with the buttons that go with it. Pure: the bar only draws them.
import { t } from "../../i18n";
import { sessionCan } from "../format";
import type { Interaction, Session } from "../types";
import type { VoicePhase } from "./conversation";
import { runOpen, stampDate, type VoiceFailure, type VoiceLink, type VoiceRun } from "./wire";

/** What the second line is about. A screen reader hears a change of it, not of its words (the time goes on). */
export type BarLineKind =
  /** Nothing going on: what is said goes to the conversation. */
  | "ready"
  | "muted"
  /** An instruction on its way, or the conversation being started for it. */
  | "handing"
  | "working"
  /** The conversation waits on something outside: a deploy, CI, another agent. */
  | "waiting"
  /** The voice's upstream is paused while the conversation works (it wakes up by itself). */
  | "paused"
  /** A card wants a tap: a plan, a permission, a terminal dialog, a question the voice is not asking. */
  | "needsTap"
  /** The voice is asking a question card's question: what the user says is the answer. */
  | "asking"
  /** The last instruction did not go (refused, failed, or not known to have gone). */
  | "notSent"
  /** The computer is offline: nothing said can go. */
  | "offline";

/** Worth saying to a screen reader as it comes: it asks for the user, or the voice stops. */
export function announced(kind: BarLineKind): boolean {
  return kind === "needsTap" || kind === "asking" || kind === "notSent" || kind === "offline" || kind === "paused";
}

export type BarAction =
  /** Stop <agent>: interrupts the agent (`POST …/interrupt`), never the voice. */
  | { kind: "stopAgent" }
  /** Resend: the refused instruction again, under its id. */
  | { kind: "resend"; run: string }
  /** Clear and Send: the computer's unsent line cleared first (`run.clearable`), then the instruction. */
  | { kind: "clearAndSend"; run: string }
  /** Send Back as Feedback: the plan card answered with the refused words as its feedback (the page's own resolve). */
  | { kind: "returnPlan"; card: string; feedback: string; run: string }
  /** Never Mind: the line about it goes away. */
  | { kind: "dismiss"; run: string };

export interface BarLine {
  kind: BarLineKind;
  text: string;
  /** Since when it has been so (ms since the epoch): the bar adds how long. */
  since?: number;
  actions: BarAction[];
}

export interface BarLineInput {
  phase: VoicePhase;
  link: VoiceLink;
  runs: readonly VoiceRun[];
  session: Session | undefined;
  /** The bound conversation's pending cards. */
  cards: readonly Pick<Interaction, "id" | "kind">[];
  muted: boolean;
  asking?: string | null;
  notice?: VoiceFailure | null;
  /** Who works in it: "Claude", "Codex", the persona's name for its own conversations. */
  label: string;
  /** Refusals put away with Never Mind. */
  dismissed?: readonly string[];
  /** The refused runs the connection up now still knows (`VoiceConversation.canResend`). */
  resendable?: readonly string[];
}

const NOT_SENT = new Set(["refused", "failed", "unknown"]);

/** The second line now. */
export function barLine(o: BarLineInput): BarLine {
  const { label, cards, session } = o;
  const dismissed = new Set(o.dismissed ?? []);
  const live = o.runs.filter((r) => !dismissed.has(r.id) && r.state !== "folded");
  // What did not go, while it is the latest word on it: a later instruction that went takes its place.
  const last = live.at(-1);
  if (last && NOT_SENT.has(last.state)) return notSent(last, cards, label, (o.resendable ?? []).includes(last.id));
  if (o.notice?.code === "machine_offline") return { kind: "offline", text: t("voice.line_offline"), actions: [] };
  if (o.asking && cards.some((c) => c.id === o.asking)) return { kind: "asking", text: t("voice.line_asking", { name: label }), actions: [] };
  if (cards.length) return { kind: "needsTap", text: t("voice.line_needs_tap"), actions: [] };
  const working = session?.status === "working" || session?.state === "working";
  const open = live.filter((r) => runOpen(r.state));
  const stop: BarAction[] = working && sessionCan(session, "interrupt") ? [{ kind: "stopAgent" }] : [];
  const firstStart = (): number | undefined => {
    const times = open.map((r) => stampDate(r.startedAt)).filter((x): x is number => x !== undefined);
    return times.length ? Math.min(...times) : undefined;
  };
  const stateSince = session?.state_since ? Date.parse(session.state_since) : NaN;
  const since = working && Number.isFinite(stateSince) ? stateSince : firstStart();
  if (o.link === "paused") {
    // The first line says the voice is paused: this one says what for, and that speaking wakes it.
    return { kind: "paused", text: t("voice.line_paused", { name: label }), actions: stop };
  }
  if (session?.starting) return { kind: "handing", text: t("voice.line_starting", { name: label }), actions: [] };
  if (!working && open.some((r) => r.state === "sending")) return { kind: "handing", text: t("voice.line_handing", { name: label }), actions: [] };
  const work = session?.work;
  if (work && work.phase === "blocked" && work.on !== "user") {
    const line = work.line?.trim();
    return { kind: "waiting", text: line ? t("voice.line_waiting_on", { name: label, what: line }) : t("voice.line_waiting", { name: label }), actions: stop };
  }
  if (!working && open.some((r) => r.state === "blocked")) return { kind: "waiting", text: t("voice.line_waiting", { name: label }), actions: [] };
  if (working || open.some((r) => r.state === "sent" || r.state === "working" || r.state === "needs_you")) {
    const activity = session?.activity?.trim();
    // Who works in it, then what it does now in the hub's own words ("Claude 在跑测试").
    return { kind: "working", text: activity ? t("voice.line_activity", { name: label, activity }) : t("voice.line_working", { name: label }), since, actions: stop };
  }
  if (open.some((r) => r.state === "held")) return { kind: "working", text: t("voice.line_held", { name: label }), actions: [] };
  if (o.muted) return { kind: "muted", text: t("voice.line_muted"), actions: [] };
  return { kind: "ready", text: t("voice.line_ready", { name: label }), actions: [] };
}

/** Why `run` did not go, in the user's words (docs/assistant/voice.md 1.3), and what can be done about it. */
function notSent(run: VoiceRun, cards: readonly Pick<Interaction, "id" | "kind">[], label: string, resendable: boolean): BarLine {
  const dismiss: BarAction = { kind: "dismiss", run: run.id };
  const line = (text: string, actions: BarAction[] = []): BarLine => ({ kind: "notSent", text, actions: [...actions, dismiss] });
  if (run.state === "unknown") return line(t("voice.not_sent_unknown"));
  if (run.state === "failed") return line(t("voice.not_sent_failed", { name: label }));
  switch (run.code) {
    case "input_draft": {
      const draft = (run.draft ?? run.clearable ?? "").trim();
      return line(draft ? t("voice.not_sent_draft_quoted", { name: label, draft }) : t("voice.not_sent_draft", { name: label }), run.clearable && resendable ? [{ kind: "clearAndSend", run: run.id }] : []);
    }
    case "dialog_open": {
      const plan = [...cards].reverse().find((c) => c.kind === "plan");
      if (plan) return line(t("voice.not_sent_plan", { name: label }), [{ kind: "returnPlan", card: plan.id, feedback: run.task, run: run.id }]);
      return line(t("voice.not_sent_card", { name: label }));
    }
    case "unprompted":
      return line(t("voice.not_sent_unprompted"));
    case "on_screen":
      return line(t("voice.not_sent_on_screen"));
    case "empty":
      return line(t("voice.not_sent_empty"));
    case "open_elsewhere":
      return line(t("voice.not_sent_elsewhere"));
    case "engine_missing":
      return line(t("voice.not_sent_engine", { name: label }));
    case "runtime_away":
      return line(t("voice.not_sent_away", { name: label }));
    default:
      return line(t("voice.not_sent"), resendable ? [{ kind: "resend", run: run.id }] : []);
  }
}

/** The orb's look. */
export type OrbPhase = "connecting" | "listening" | "hearing" | "speaking" | "working" | "muted" | "paused" | "ended";

export interface BarStatus {
  text: string;
  orb: OrbPhase;
}

/** The first line beside the orb: the voice's own state, in a few words, and the orb's phase for it. `working`: the
 *  bound conversation works; `tool`: a read-only tool the voice runs. */
export function barStatus(o: { phase: VoicePhase; link: VoiceLink; muted: boolean; working: boolean; tool?: string }): BarStatus {
  const { phase } = o;
  switch (phase.kind) {
    case "connecting":
      return { text: t("voice.status_connecting"), orb: "connecting" };
    case "reconnecting":
      return { text: t("voice.status_reconnecting"), orb: "connecting" };
    case "ended":
      return { text: t("voice.status_ended"), orb: "ended" };
  }
  if (o.link === "paused") return { text: t("voice.status_paused"), orb: "paused" };
  if (o.link === "waking") return { text: t("voice.status_waking"), orb: "connecting" };
  if (o.link === "reconnecting") return { text: t("voice.status_reconnecting"), orb: "connecting" };
  if (phase.state === "speaking") return { text: t("voice.status_speaking"), orb: "speaking" };
  if (o.muted) return { text: t("voice.status_muted"), orb: "muted" };
  if (o.tool?.trim()) return { text: t("voice.status_tool", { tool: o.tool.trim() }), orb: o.working ? "working" : "listening" };
  if (phase.state === "hearing") return { text: t("voice.status_hearing"), orb: "hearing" };
  return { text: t("voice.status_listening"), orb: o.working ? "working" : "listening" };
}

/** "Claude 在跑测试 · 2 分钟": the line's words and how long it has been so; nothing under a minute. */
export function sinceText(text: string, since: number | undefined, now: number): string {
  if (since === undefined) return text;
  const minutes = Math.floor((now - since) / 60_000);
  if (minutes < 1) return text;
  const hours = Math.floor(minutes / 60);
  const span = hours ? t("voice.since_hours", { hours, minutes: minutes % 60 }) : t("voice.since_minutes", { minutes });
  return t("voice.since", { text, span });
}
