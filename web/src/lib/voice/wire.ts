// The browser's side of the voice extension's socket (docs/assistant/voice.md 3: protocol 2). Pure data and parsing, as
// iOS's VoiceWire.swift: an unknown frame comes back as `other`, a missing or odd field falls back to its default, so a
// newer extension never breaks an older page.
import { CONTEXT_RE } from "../compatibility";
import type { Item } from "../types";

/** The conversation a call is bound to, as `start.target` sends it. */
export interface VoiceTarget {
  machine_id: string;
  session_id: string;
}

/** The bound conversation as `ready.target` describes it: fixed for the whole call. */
export interface VoiceTargetInfo {
  machineID: string;
  sessionID: string;
  kind?: string;
  title?: string;
  /** "Claude Code", "Codex": who the call hands things to. */
  agentLabel?: string;
  machineName?: string;
}

/** One line of words: `start.recent` (the conversation's last turns) and `start.history` (the call's captions so far). */
export interface VoiceLine {
  role: "user" | "assistant";
  text: string;
}

export const RECENT_TURNS = 3;
export const RECENT_LENGTH = 300;
/** At most this many lines of `recent`: an agent's turn can be a long run of short texts between tool calls. */
export const RECENT_LINES = 12;
/** How many of the call's captions a reconnect hands back (`history`). */
export const HISTORY_LINES = 40;

export const DEFAULT_INPUT_RATE = 16000;
export const DEFAULT_OUTPUT_RATE = 24000;

/** Text without the page's context blocks, its runs of blank space made one. */
export function cleanLine(raw: string): string {
  return raw.replace(new RegExp(CONTEXT_RE.source, "g"), " ").split(/\s+/).filter(Boolean).join(" ");
}

/** At most `limit` characters (code points), the last one an ellipsis when cut. */
export function cutLine(text: string, limit: number): string {
  const chars = Array.from(text);
  return chars.length <= limit ? text : chars.slice(0, limit - 1).join("") + "…";
}

/** The conversation's last three turns as `start.recent`: what the user and the agent wrote, nothing else (tool calls,
 *  thinking and notices stay out), context blocks taken out, each line cut to 300 characters. The extension fetches
 *  them itself when this is empty. */
export function recentLines(items: readonly Item[]): VoiceLine[] {
  const lines: VoiceLine[] = [];
  for (const item of items) {
    if (item.kind !== "text" || (item.role !== "user" && item.role !== "assistant")) continue;
    const text = cleanLine(item.text ?? "");
    if (!text) continue;
    lines.push({ role: item.role, text: cutLine(text, RECENT_LENGTH) });
  }
  const starts = lines.flatMap((l, i) => (l.role === "user" ? [i] : []));
  const from = starts.length > RECENT_TURNS ? starts[starts.length - RECENT_TURNS] : 0;
  return lines.slice(from).slice(-RECENT_LINES);
}

/** What a `run` frame says about one instruction the call handed to the conversation (docs/assistant/voice.md 3.3); a
 *  state this page does not know is kept as it came. */
export type VoiceRunState = "sending" | "sent" | "working" | "needs_you" | "blocked" | "held" | "done" | "failed" | "refused" | "unknown" | "folded" | (string & {});

const CLOSED_RUNS = new Set(["done", "failed", "refused", "unknown", "folded"]);

/** Not over yet: a reconnect hands it back (`resume`). `held` answered once and still works in the background; a state
 *  this page does not know is kept open too, so a result that comes later is still read out. */
export function runOpen(state: VoiceRunState): boolean {
  return !CLOSED_RUNS.has(state);
}

/** `started_at` as the extension wrote it (an RFC 3339 string or a number), echoed back unchanged in `resume`. */
export type VoiceStamp = string | number;

/** When it started: a number is Unix seconds, or milliseconds when it is that large. */
export function stampDate(stamp: VoiceStamp | undefined): number | undefined {
  if (stamp === undefined) return undefined;
  if (typeof stamp === "number") return stamp > 100_000_000_000 ? stamp : stamp * 1000;
  const ms = Date.parse(stamp);
  return Number.isFinite(ms) ? ms : undefined;
}

/** One instruction handed to the bound conversation, as the latest `run` frame for its id says. */
export interface VoiceRun {
  id: string;
  task: string;
  state: VoiceRunState;
  msgIDs: string[];
  /** The transcript's last seq before it was sent; moves on after a held answer. */
  baseSeq: number;
  startedAt?: VoiceStamp;
  /** Why it was refused or failed: dialog_open, input_draft, unprompted… */
  code?: string;
  message?: string;
  /** The words left unsent in the agent's input box on the computer (409 input_draft): the whole line, shown. */
  draft?: string;
  /** The hub's 409 `clearable` text: only when the node can clear that line, not always the same as `draft` (an IDE
   *  selection marker stays out of it). Clear and Send sends it back as `clear_draft` exactly (docs/assistant/voice.md 3.2). */
  clearable?: string;
}

export type VoiceAskState = "asking" | "answered" | "released" | (string & {});

/** Which card the call is asking about by voice (`ask` frame). */
export interface VoiceAsk {
  interactionID: string;
  state: VoiceAskState;
  by?: string;
}

/** The upstream model's connection as the extension reports it (`link` frame). */
export type VoiceLink = "live" | "paused" | "waking" | "reconnecting" | (string & {});

/** An `error` frame. `message` is the extension's (or the upstream's) own words: for Details, not the line on screen. */
export interface VoiceFailure {
  code?: string;
  message: string;
  /** The extension closes the socket after a fatal one: the call is over, nothing reconnects. */
  fatal: boolean;
}

/** The `ready` frame: the extension is connected upstream and the rates are known. */
export interface VoiceReady {
  /** 1 when the frame does not say (an extension from before protocol 2). */
  protocol: number;
  features: string[];
  inputRate: number;
  outputRate: number;
  model?: string;
  voice?: string;
  target?: VoiceTargetInfo;
}

export interface VoiceTranscript {
  id: string;
  role: string;
  text: string;
  final: boolean;
  /** The line this one goes before, when it is new (protocol 2): the user's caption came after the voice began to
   *  answer it. Absent: at the end. */
  before?: string;
}

export type VoiceFrame =
  | { type: "ready"; ready: VoiceReady }
  | { type: "clear" }
  | { type: "transcript"; transcript: VoiceTranscript }
  /** A read-only function tool at work. */
  | { type: "tool"; id: string; label: string; done: boolean }
  | { type: "run"; run: VoiceRun }
  | { type: "ask"; ask: VoiceAsk }
  | { type: "link"; state: VoiceLink }
  | { type: "error"; failure: VoiceFailure }
  | { type: "other"; name: string };

const str = (v: unknown): string | undefined => (typeof v === "string" ? v : undefined);
const nonEmpty = (v: unknown): string | undefined => {
  const s = str(v);
  return s ? s : undefined;
};
/** A whole number; anything else (a fraction, a string, too large) takes the default. */
const whole = (v: unknown): number | undefined => (typeof v === "number" && Number.isSafeInteger(v) ? v : undefined);
/** A rate must be a positive number below a million. */
const rate = (v: unknown): number | undefined => (typeof v === "number" && Number.isFinite(v) && v > 0 && v < 1_000_000 ? Math.floor(v) : undefined);

function target(v: unknown): VoiceTargetInfo | undefined {
  if (!v || typeof v !== "object") return undefined;
  const t = v as Record<string, unknown>;
  const machineID = str(t.machine_id), sessionID = str(t.session_id);
  if (machineID === undefined || sessionID === undefined) return undefined;
  return { machineID, sessionID, kind: str(t.kind), title: str(t.title), agentLabel: str(t.agent_label), machineName: str(t.machine_name) };
}

/** One text frame from the extension; null when it is not a JSON object with a string "type". */
export function parseFrame(text: string): VoiceFrame | null {
  let o: Record<string, unknown>;
  try {
    const v: unknown = JSON.parse(text);
    if (!v || typeof v !== "object" || Array.isArray(v)) return null;
    o = v as Record<string, unknown>;
  } catch {
    return null;
  }
  const type = str(o.type);
  if (type === undefined) return null;
  switch (type) {
    case "ready": {
      const protocol = whole(o.protocol);
      return {
        type,
        ready: {
          protocol: protocol !== undefined && protocol > 0 ? protocol : 1,
          features: Array.isArray(o.features) ? o.features.filter((f): f is string => typeof f === "string") : [],
          inputRate: rate(o.input_rate) ?? DEFAULT_INPUT_RATE,
          outputRate: rate(o.output_rate) ?? DEFAULT_OUTPUT_RATE,
          model: str(o.model),
          voice: str(o.voice),
          target: target(o.target),
        },
      };
    }
    case "clear":
      return { type };
    case "transcript":
      return { type, transcript: { id: str(o.id) ?? "", role: str(o.role) ?? "", text: str(o.text) ?? "", final: o.final === true, before: nonEmpty(o.before) } };
    case "tool":
      return { type, id: str(o.id) ?? "", label: str(o.label) ?? "", done: o.done === true };
    case "run": {
      const id = nonEmpty(o.id);
      if (!id) return { type: "other", name: type };
      const started = o.started_at;
      return {
        type,
        run: {
          id,
          task: str(o.task) ?? "",
          state: str(o.state) ?? "",
          msgIDs: Array.isArray(o.msg_ids) ? o.msg_ids.filter((m): m is string => typeof m === "string") : [],
          baseSeq: whole(o.base_seq) ?? 0,
          startedAt: (typeof started === "string" && started) || (typeof started === "number" && Number.isFinite(started)) ? started : undefined,
          code: str(o.code),
          message: str(o.message),
          draft: str(o.draft),
          clearable: nonEmpty(o.clearable),
        },
      };
    }
    case "ask": {
      const id = nonEmpty(o.interaction_id);
      if (!id) return { type: "other", name: type };
      return { type, ask: { interactionID: id, state: str(o.state) ?? "", by: str(o.by) } };
    }
    case "link":
      return { type, state: str(o.state) ?? "" };
    case "error":
      return { type, failure: { code: nonEmpty(o.code), message: str(o.message) ?? "", fatal: o.fatal === true } };
    default:
      return { type: "other", name: type };
  }
}

/** The `start` frame: the call's first, and again after every reconnect. */
export interface VoiceStart {
  model?: string;
  voice?: string;
  target: VoiceTarget;
  recent?: VoiceLine[];
  /** The call's captions so far: only when it picks up again. */
  history?: VoiceLine[];
  /** The runs not over yet, as last reported: only when it picks up again. */
  resume?: VoiceRun[];
  /** False: the microphone records without echo cancellation, so a caption of the user's may be the voice's own. */
  echoCancellation?: boolean;
}

/** Text frames the page sends. Keys in a fixed order, absent fields left out. */
export function startFrame(s: VoiceStart): string {
  const frame: Record<string, unknown> = { type: "start", protocol: 2 };
  if (s.model) frame.model = s.model;
  if (s.voice) frame.voice = s.voice;
  frame.target = { machine_id: s.target.machine_id, session_id: s.target.session_id };
  if (s.recent?.length) frame.recent = s.recent;
  if (s.history?.length) frame.history = s.history;
  if (s.resume?.length) {
    frame.resume = {
      runs: s.resume.map((r) => ({
        id: r.id, msg_ids: r.msgIDs, task: r.task, base_seq: r.baseSeq,
        // returned unchanged: a string stays a string, a number a number
        ...(r.startedAt !== undefined ? { started_at: r.startedAt } : {}),
        state: r.state,
      })),
    };
  }
  if (s.echoCancellation !== undefined) frame.echo_cancellation = s.echoCancellation;
  return JSON.stringify(frame);
}

/** Stops what the voice is saying (never the agent). */
export const INTERRUPT_FRAME = JSON.stringify({ type: "interrupt" });

/** Says a sentence by typing it: the same as speaking it. */
export function textFrame(text: string): string {
  return JSON.stringify({ type: "text", text });
}

/** Sends again a run a 409 refused, under the same id. `clearDraft`: the line in the agent's input box the node is to
 *  clear first (`run.clearable`, checked against what is there). */
export function resendFrame(id: string, clearDraft?: string): string {
  return JSON.stringify(clearDraft ? { type: "resend", id, clear_draft: clearDraft } : { type: "resend", id });
}

/** Extension ids are a-z, 0-9, `_` and `-` (docs/extensions/extensions.md): anything else must not become a path. */
export function validExtensionID(id: string): boolean {
  return /^[a-z0-9_-]+$/.test(id);
}

/** The call's socket: the extension's `/ws` under the hub's extension routes, on this page's own origin and sub-path,
 *  with the browser's token in the query (a browser cannot set a header on a WebSocket; the hub takes `?token=` and
 *  strips it before forwarding, as it does for /api/v1/ws). */
export function voiceSocketURL(loc: { protocol: string; host: string }, base: string, root: string, extensionID: string, token: string): string {
  const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${loc.host}${base}/api/v1${root}/${encodeURIComponent(extensionID)}/ws?token=${encodeURIComponent(token)}`;
}
