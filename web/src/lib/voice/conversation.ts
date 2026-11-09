// One voice call bound to one conversation (docs/assistant/voice.md 6.3; the web's take in docs/web/web.md "Voice"): the socket
// to the hub's voice extension, the frames, the captions, the runs it handed over, and the audio. As iOS's
// VoiceConversation.swift, with everything the browser does behind `VoiceDeps`, so the tests drive it with a fake socket,
// fake audio and a clock they move by hand.
import { mergeCaption, historyOf, type VoiceCaption } from "./captions";
import { Uplink } from "./uplink";
import {
  DEFAULT_INPUT_RATE, DEFAULT_OUTPUT_RATE, INTERRUPT_FRAME, parseFrame, resendFrame, runOpen, startFrame, textFrame,
  type VoiceFailure, type VoiceFrame, type VoiceLine, type VoiceLink, type VoiceReady, type VoiceRun, type VoiceStart, type VoiceTargetInfo,
} from "./wire";
import type { Session } from "../types";
import type { VoiceCue } from "./cues";

export type VoiceLiveState = "listening" | "hearing" | "speaking";

export type VoicePhase =
  | { kind: "connecting" }
  | { kind: "live"; state: VoiceLiveState }
  /** The socket to the hub went away: the n-th of three tries (1, 4 and 10 seconds apart). */
  | { kind: "reconnecting"; attempt: number }
  | { kind: "ended"; end: VoiceEnd };

/** Why a call ended. The words for each are VoiceEndNote's; `failed` keeps the extension's own message for Details. */
export type VoiceEnd =
  /** End tapped. */
  | { kind: "user" }
  /** The page left the conversation, or the tab closed. */
  | { kind: "left" }
  /** Another call started: one at a time in the page. */
  | { kind: "replaced" }
  /** The browser suspended the call's audio (another app or tab took it, the tab was put to sleep). */
  | { kind: "interrupted" }
  | { kind: "permissionDenied" }
  /** No microphone to record from. */
  | { kind: "noMicrophone" }
  /** The microphone or the speaker failed (it was unplugged, the audio worklet would not load). */
  | { kind: "audioFailed" }
  /** Ten minutes nobody spoke, nothing handed over is open and the conversation is not working. */
  | { kind: "idle" }
  /** Three reconnects failed. */
  | { kind: "lost" }
  /** The hub refused the socket: 401 (the credential), 403 (one that may only answer), 404 (no such extension, on the first
   *  connection only: a reconnect's 404 is the extension being applied again). */
  | { kind: "rejected"; status: number }
  /** The extension does not speak protocol 2, or bound another conversation than asked. */
  | { kind: "incompatible" }
  /** The model chosen for the extension cannot be used. */
  | { kind: "modelUnavailable"; why: string }
  /** The page has no credential any more. */
  | { kind: "signedOut" }
  /** A fatal `error` frame. */
  | { kind: "failed"; code?: string; message: string };

/** Ended by the user's own doing: nothing stays on the page. After any other ending "Voice ended · Continue" stays
 *  until the page leaves the conversation (docs/assistant/voice.md 1.2 L). */
export function endedByUser(end: VoiceEnd): boolean {
  return end.kind === "user" || end.kind === "left" || end.kind === "replaced";
}

/** A read-only function tool at work. */
export interface VoiceTool {
  id: string;
  label: string;
}

/** What the page draws: replaced whole on every change, so a view can compare it by reference. */
export interface VoiceSnapshot {
  phase: VoicePhase;
  /** The upstream model's connection (paused while the agent works). */
  link: VoiceLink;
  /** At most 40, in the order they were said. */
  captions: readonly VoiceCaption[];
  /** The instructions handed over, in the order they came. */
  runs: readonly VoiceRun[];
  /** The card being asked about by voice (`ask` frame). */
  askingID: string | null;
  target?: VoiceTargetInfo;
  features: readonly string[];
  /** The latest error that did not end the call (machine_offline, model_unavailable…). */
  notice: VoiceFailure | null;
  tools: readonly VoiceTool[];
  muted: boolean;
  /** Refusals put away with Never Mind. */
  dismissed: readonly string[];
  /** Refused runs the connection up now still knows: Resend / Clear and Send go only for these. */
  resendable: readonly string[];
  model?: string;
  voice?: string;
}

/** The microphone and the speaker of a call (audio.ts in a browser, a fake in the tests). */
export interface VoiceAudio {
  /** Opens the microphone (the browser asks for permission) and starts sending frames at `inputRate`. Rejects with
   *  VoiceAudioError. */
  start(o: { inputRate: number; muted: boolean; onFrame: (frame: Int16Array, rate: number) => void; onEvent: (e: VoiceAudioEvent) => void }): Promise<void>;
  setInputRate(rate: number): void;
  setMuted(on: boolean): void;
  /** PCM16 little-endian at `rate`, after what is already queued, with no gap. */
  play(pcm: ArrayBuffer, rate: number): void;
  /** Drops everything queued and playing. */
  clear(): void;
  cue(cue: VoiceCue): void;
  /** 0…1 for the orb: the microphone's level, and the voice's as it plays. */
  levels(): { input: number; output: number };
  /** Whether the microphone records with echo cancellation, once known. */
  readonly cancelsEcho: boolean | undefined;
  /** Releases the microphone and closes the audio; safe before `start` settled. */
  stop(): void;
}

export type VoiceAudioEvent = { kind: "playing"; on: boolean } | { kind: "interrupted" } | { kind: "failed" };

export class VoiceAudioError extends Error {
  readonly reason: "permission" | "noMicrophone" | "failed";
  constructor(reason: "permission" | "noMicrophone" | "failed", message: string = reason) {
    super(message);
    this.reason = reason;
  }
}

/** The part of a WebSocket a call uses. */
export interface VoiceSocket {
  binaryType: string;
  readonly readyState: number;
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code: number }) => void) | null;
  send(data: string | ArrayBuffer): void;
  close(code?: number): void;
}

export type ModelPick = { ok: true; model?: string } | { ok: false; why: string };

/** What a call needs from the page; the tests hand in fakes. */
export interface VoiceDeps {
  /** The socket's address with this page's credential; null when signed out. */
  socketURL(): string | null;
  openSocket(url: string): VoiceSocket;
  /** Made at once, inside the click that starts the call: a browser lets audio start only from a user's gesture. */
  audio(outputRate: number): VoiceAudio;
  /** The status the hub answers the extension's /info with: a browser hides a refused handshake's, so a socket that
   *  never opened asks this way. null: no answer. */
  probe(): Promise<number | null>;
  /** The model the call asks for (the use `plugin.<id>`). */
  resolveModel(): Promise<ModelPick>;
  /** The voice picked in this browser for the extension. */
  voice(): string | undefined;
  /** The bound conversation as the page has it now (whether it works, for the idle rule). */
  session(): Session | undefined;
  now(): number;
  setTimer(fn: () => void, ms: number): unknown;
  clearTimer(id: unknown): void;
}

/** 1, 4 and 10 seconds before each of the three reconnects (docs/assistant/voice.md 1.2 M). */
export const RETRY_DELAYS = [1000, 4000, 10_000];
/** A connection that has not said `ready` by then is given up like a dropped one. */
export const READY_TIMEOUT = 15_000;
/** The user's caption stops counting as "hearing you" this long after it last changed. */
export const HEARING_QUIET = 2000;
export const IDLE_LIMIT = 600_000;
export const IDLE_CHECK = 30_000;
const RUN_LIMIT = 20;

/** When a call that nothing happens in ends by itself (docs/assistant/voice.md 6.5). */
export function idleExpired(o: { now: number; lastActivity: number; openRuns: boolean; targetWorking: boolean; playing: boolean }): boolean {
  return !o.playing && !o.openRuns && !o.targetWorking && o.now - o.lastActivity >= IDLE_LIMIT;
}

/** The rates each extension said last: the next call records at them before `ready`. */
const formats = new Map<string, { inputRate: number; outputRate: number }>();

export class VoiceConversation {
  readonly mid: string;
  readonly sid: string;
  readonly extensionID: string;
  private snap: VoiceSnapshot = {
    phase: { kind: "connecting" }, link: "live", captions: [], runs: [], askingID: null, features: [], notice: null, tools: [],
    muted: false, dismissed: [], resendable: [],
  };
  private listeners = new Set<() => void>();
  private readonly deps: VoiceDeps;
  private readonly recent: VoiceLine[];
  private uplink = new Uplink();
  private audio: VoiceAudio | null = null;
  private socket: VoiceSocket | null = null;
  /** Bumped by every socket: captions and late events of an older one stay apart. */
  private generation = 0;
  private attempt = 0;
  private ready = false;
  /** Some connection of this start said `ready`: the extension is there, so a 404 after that is passing. */
  private everReady = false;
  /** The connection that last reported each run: only that one knows it (see `canResend`). */
  private runSeen = new Map<string, number>();
  private playing = false;
  private hearing = false;
  private hearingTick = 0;
  private resolved = false;
  private format: { inputRate: number; outputRate: number };
  private lastActivity: number;
  /** Bumped by every start and end: an await that comes back after it does nothing. */
  private epoch = 0;
  private timers = new Map<string, unknown>();

  constructor(o: { mid: string; sid: string; extensionID: string; recent?: VoiceLine[]; deps: VoiceDeps }) {
    this.mid = o.mid;
    this.sid = o.sid;
    this.extensionID = o.extensionID;
    this.recent = o.recent ?? [];
    this.deps = o.deps;
    this.format = formats.get(o.extensionID) ?? { inputRate: DEFAULT_INPUT_RATE, outputRate: DEFAULT_OUTPUT_RATE };
    this.lastActivity = o.deps.now();
  }

  // ── what the page reads ──

  getSnapshot = (): VoiceSnapshot => this.snap;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  get over(): boolean {
    return this.snap.phase.kind === "ended";
  }

  /** The orb's levels now (read about 20 times a second, never stored in the snapshot). */
  levels(): { input: number; output: number } {
    return this.audio?.levels() ?? { input: 0, output: 0 };
  }

  /** Frames sent since the call began, and their bytes. */
  get sent(): { frames: number; bytes: number } {
    return { frames: this.uplink.sent, bytes: this.uplink.bytes };
  }

  // ── start and end ──

  /** Starts the call: the audio (made at once, inside the user's click), the microphone, the model, then the socket.
   *  Settles once the socket is being opened, or the call ended. */
  async begin(): Promise<void> {
    const epoch = ++this.epoch;
    this.attempt = 0;
    this.ready = false;
    this.everReady = false;
    this.set({ phase: { kind: "connecting" }, link: "live", notice: null });
    const uplink = (this.uplink = new Uplink());
    const audio = this.deps.audio(this.format.outputRate);
    this.audio = audio;
    try {
      await audio.start({
        inputRate: this.format.inputRate,
        muted: this.snap.muted,
        onFrame: (frame, rate) => {
          if (epoch === this.epoch) uplink.push(frame, rate);
        },
        onEvent: (e) => this.audioEvent(e, epoch),
      });
    } catch (e) {
      if (!this.current(epoch)) return;
      const reason = e instanceof VoiceAudioError ? e.reason : "failed";
      return this.end(reason === "permission" ? { kind: "permissionDenied" } : reason === "noMicrophone" ? { kind: "noMicrophone" } : { kind: "audioFailed" });
    }
    if (!this.current(epoch)) return;
    this.lastActivity = this.deps.now();
    if (!this.resolved) {
      const pick = await this.deps.resolveModel();
      if (!this.current(epoch)) return;
      if (!pick.ok) return this.end({ kind: "modelUnavailable", why: pick.why });
      this.set({ model: pick.model, voice: this.deps.voice() });
      this.resolved = true;
    }
    this.watchIdle(epoch);
    this.connect();
  }

  /** Continue: picks an ended call up again, with its captions and its open runs (`history`, `resume`). */
  restart(): Promise<void> {
    if (!this.over) return Promise.resolve();
    return this.begin();
  }

  /** Ends the call: the socket, the microphone and the speaker go. */
  end(reason: VoiceEnd): void {
    if (this.over) return;
    this.epoch++;
    for (const id of this.timers.values()) this.deps.clearTimer(id);
    this.timers.clear();
    this.closeSocket();
    this.uplink.hold();
    this.audio?.stop();
    this.audio = null;
    this.ready = false;
    this.playing = false;
    this.hearing = false;
    this.set({ phase: { kind: "ended", end: reason }, tools: [], askingID: null, link: "live" });
  }

  // ── what the page does ──

  /** Type a line: the same as saying it. */
  say(text: string): void {
    const words = text.trim();
    if (!words || !this.ready || !this.socket) return;
    this.socket.send(textFrame(words));
    this.lastActivity = this.deps.now();
  }

  /** Interrupt the voice: stops what it says (never the agent); what was queued is dropped here at once. */
  interruptSpeech(): void {
    if (!this.ready || !this.socket) return;
    this.socket.send(INTERRUPT_FRAME);
    this.audio?.clear();
    this.lastActivity = this.deps.now();
  }

  setMuted(on: boolean): void {
    if (on === this.snap.muted) return;
    this.audio?.setMuted(on);
    this.set({ muted: on });
  }

  /** Whether Resend / Clear and Send can go for a run: refused, and reported by the connection that is up now. A
   *  reconnect rebuilds only the open runs, so after one the extension no longer knows a refused run. */
  canResend(runID: string): boolean {
    if (!this.ready || !this.socket) return false;
    const run = this.snap.runs.find((r) => r.id === runID);
    return run?.state === "refused" && this.runSeen.get(runID) === this.generation;
  }

  /** Resend / Clear and Send a run a 409 refused, under its id; `clearingDraft` hands the hub's `clearable` back. */
  resend(runID: string, clearingDraft = false): boolean {
    const run = this.snap.runs.find((r) => r.id === runID);
    if (!run || !this.canResend(runID) || !this.socket) return false;
    if (clearingDraft && !run.clearable) return false;
    this.socket.send(resendFrame(runID, clearingDraft ? run.clearable : undefined));
    this.lastActivity = this.deps.now();
    return true;
  }

  /** Never Mind on what did not go: its line goes away. */
  dismiss(runID: string): void {
    if (!this.snap.dismissed.includes(runID)) this.set({ dismissed: [...this.snap.dismissed, runID] });
  }

  /** Another voice: a new connection that picks the call up where it was. */
  changeVoice(voice: string | undefined): void {
    if (voice === this.snap.voice) return;
    this.set({ voice });
    this.reopen();
  }

  cue(cue: VoiceCue): void {
    this.audio?.cue(cue);
  }

  /** What `start` says now: the target, the conversation's last turns, whether the microphone cancels echo, and when it
   *  picks up again the captions so far and the runs not over yet, each as last reported. */
  startFrame(): VoiceStart {
    return {
      model: this.snap.model, voice: this.snap.voice, target: { machine_id: this.mid, session_id: this.sid }, recent: this.recent,
      history: historyOf(this.snap.captions), resume: this.snap.runs.filter((r) => runOpen(r.state)), echoCancellation: this.audio?.cancelsEcho,
    };
  }

  // ── the socket ──

  private connect(): void {
    const url = this.deps.socketURL();
    if (!url) return this.end({ kind: "signedOut" });
    const generation = ++this.generation;
    this.ready = false;
    // A new connection is a new conversation with the extension, which reports again what still holds.
    this.set({ link: "live", askingID: null, notice: null });
    let opened = false;
    const sock = this.deps.openSocket(url);
    sock.binaryType = "arraybuffer";
    this.socket = sock;
    sock.onopen = () => {
      if (generation !== this.generation || this.over) return;
      opened = true;
      sock.send(startFrame(this.startFrame()));
    };
    sock.onmessage = (ev) => this.received(ev.data, generation);
    sock.onclose = () => {
      if (generation !== this.generation || this.over) return;
      this.socket = null;
      void this.dropped(generation, opened);
    };
    this.after(READY_TIMEOUT, "ready", () => {
      if (this.generation !== generation || this.ready || this.over) return;
      this.closeSocket();
      void this.dropped(generation, true);
    });
  }

  /** A new connection now, not counted as a reconnect (another voice). */
  private reopen(): void {
    if (this.over || !this.audio) return;
    this.cancel("ready");
    this.cancel("retry");
    this.closeSocket();
    this.ready = false;
    this.uplink.hold();
    this.dropSpeech();
    this.attempt = 0;
    this.set({ phase: { kind: "connecting" } });
    this.connect();
  }

  private closeSocket(): void {
    const s = this.socket;
    this.socket = null;
    if (!s) return;
    s.onopen = s.onmessage = s.onclose = null;
    try {
      s.close(1000);
    } catch {
      // already closing
    }
  }

  private received(data: unknown, generation: number): void {
    if (generation !== this.generation || this.over) return;
    if (typeof data === "string") {
      const frame = parseFrame(data);
      if (frame) this.handle(frame);
    } else if (data instanceof ArrayBuffer && this.ready) {
      this.audio?.play(data, this.format.outputRate);
    }
  }

  /** The socket went away: three tries, 1, 4 and 10 seconds apart, each with `history` and `resume`; then Continue. A
   *  handshake the hub refused for good is not tried again. */
  private async dropped(generation: number, opened: boolean): Promise<void> {
    if (generation !== this.generation || this.over) return;
    this.cancel("ready");
    this.ready = false;
    this.uplink.hold();
    this.dropSpeech();
    this.set({ tools: [] });
    if (!opened) {
      const status = await this.deps.probe();
      if (generation !== this.generation || this.over) return;
      if (status !== null && this.refusedForGood(status)) return this.end({ kind: "rejected", status });
    }
    this.attempt++;
    if (this.attempt > RETRY_DELAYS.length) return this.end({ kind: "lost" });
    this.set({ phase: { kind: "reconnecting", attempt: this.attempt } });
    this.audio?.cue("reconnecting");
    this.after(RETRY_DELAYS[this.attempt - 1], "retry", () => {
      if (this.generation === generation && !this.over) this.connect();
    });
  }

  /** 401 and 403 always end it; 404 only while no connection of this start was ready (after that the hub answers 404
   *  while it applies the extension again: a setting changed, switched off and on, an upgrade). */
  private refusedForGood(status: number): boolean {
    return status === 401 || status === 403 || (status === 404 && !this.everReady);
  }

  /** What the voice of a connection that went away still had to say is stale. */
  private dropSpeech(): void {
    this.audio?.clear();
    this.playing = false;
    this.hearing = false;
  }

  private handle(frame: VoiceFrame): void {
    switch (frame.type) {
      case "ready":
        return this.becomeReady(frame.ready);
      case "clear":
        this.audio?.clear();
        return;
      case "transcript":
        return this.caption(frame.transcript);
      case "tool": {
        const tools = this.snap.tools;
        if (frame.done) this.set({ tools: tools.filter((x) => x.id !== frame.id) });
        else if (tools.some((x) => x.id === frame.id)) this.set({ tools: tools.map((x) => (x.id === frame.id ? { ...x, label: frame.label } : x)) });
        else this.set({ tools: [...tools, { id: frame.id, label: frame.label }] });
        return;
      }
      case "run":
        return this.update(frame.run);
      case "ask": {
        const { ask } = frame;
        if (ask.state === "asking") this.set({ askingID: ask.interactionID });
        else if ((ask.state === "answered" || ask.state === "released") && this.snap.askingID === ask.interactionID) this.set({ askingID: null });
        return;
      }
      case "link":
        if (frame.state === "reconnecting" && this.snap.link !== "reconnecting") this.audio?.cue("reconnecting");
        this.set({ link: frame.state });
        return;
      case "error":
        if (frame.failure.fatal) this.end({ kind: "failed", code: frame.failure.code, message: frame.failure.message });
        else this.set({ notice: frame.failure });
        return;
    }
  }

  /** The extension is connected upstream: the microphone's held audio goes out, at the rate it asked for. */
  private becomeReady(r: VoiceReady): void {
    const sock = this.socket;
    if (this.ready || !sock) return;
    // The web has no protocol 1 client: an older extension would hand everything to the main conversation instead of
    // the one the call was opened from.
    if (r.protocol < 2 || (r.target && (r.target.machineID !== this.mid || r.target.sessionID !== this.sid))) return this.end({ kind: "incompatible" });
    this.cancel("ready");
    if (r.inputRate !== this.format.inputRate) this.audio?.setInputRate(r.inputRate);
    this.format = { inputRate: r.inputRate, outputRate: r.outputRate };
    formats.set(this.extensionID, this.format);
    this.ready = true;
    this.everReady = true;
    this.attempt = 0;
    this.uplink.open(r.inputRate, (bytes) => {
      if (sock.readyState === 1) sock.send(bytes);
    });
    this.lastActivity = this.deps.now();
    this.set({ features: r.features, target: r.target ?? this.snap.target });
    this.refreshPhase();
  }

  private caption(t: { id: string; role: string; text: string; final: boolean; before?: string }): void {
    const captions = mergeCaption(this.snap.captions, t, this.generation);
    this.lastActivity = this.deps.now();
    if (t.role === "user") {
      this.hearing = !t.final;
      const tick = ++this.hearingTick;
      if (this.hearing) {
        this.after(HEARING_QUIET, "hearing", () => {
          if (this.hearingTick !== tick) return;
          this.hearing = false;
          this.refreshPhase();
        });
      }
    }
    if (captions !== this.snap.captions) this.set({ captions });
    this.refreshPhase();
  }

  private update(run: VoiceRun): void {
    this.runSeen.set(run.id, this.generation);
    const runs = this.snap.runs;
    const at = runs.findIndex((r) => r.id === run.id);
    if (at >= 0) {
      const next = runs.slice();
      next[at] = run;
      return this.set({ runs: next });
    }
    const next = [...runs, run];
    while (next.length > RUN_LIMIT) {
      const i = Math.max(0, next.findIndex((r) => !runOpen(r.state)));
      this.runSeen.delete(next[i].id);
      next.splice(i, 1);
    }
    this.set({ runs: next });
  }

  private refreshPhase(): void {
    if (!this.ready || this.over) return;
    const state: VoiceLiveState = this.playing ? "speaking" : this.hearing ? "hearing" : "listening";
    const was = this.snap.phase;
    if (was.kind === "live" && was.state === state) return;
    // connected (again): a cue up, as the phone plays
    if (was.kind !== "live") this.audio?.cue("ready");
    this.set({ phase: { kind: "live", state } });
  }

  // ── audio and timers ──

  private audioEvent(e: VoiceAudioEvent, epoch: number): void {
    if (!this.current(epoch)) return;
    switch (e.kind) {
      case "playing":
        this.playing = e.on;
        if (e.on) this.lastActivity = this.deps.now();
        this.refreshPhase();
        return;
      case "interrupted":
        return this.end({ kind: "interrupted" });
      case "failed":
        return this.end({ kind: "audioFailed" });
    }
  }

  /** Whether the bound conversation works or waits on the user: a call is not idle then. */
  private targetBusy(): boolean {
    const s = this.deps.session();
    return s?.state === "working" || s?.state === "needs_you" || s?.status === "working";
  }

  private watchIdle(epoch: number): void {
    const tick = () => {
      if (!this.current(epoch)) return;
      if (idleExpired({ now: this.deps.now(), lastActivity: this.lastActivity, openRuns: this.snap.runs.some((r) => runOpen(r.state)), targetWorking: this.targetBusy(), playing: this.playing })) {
        return this.end({ kind: "idle" });
      }
      this.after(IDLE_CHECK, "idle", tick);
    };
    this.after(IDLE_CHECK, "idle", tick);
  }

  /** Runs `fn` after `ms` on the call's clock, replacing the timer of the same name. */
  private after(ms: number, name: string, fn: () => void): void {
    this.cancel(name);
    this.timers.set(name, this.deps.setTimer(() => {
      this.timers.delete(name);
      fn();
    }, ms));
  }

  private cancel(name: string): void {
    const id = this.timers.get(name);
    if (id === undefined) return;
    this.deps.clearTimer(id);
    this.timers.delete(name);
  }

  private current(epoch: number): boolean {
    return epoch === this.epoch && !this.over;
  }

  private set(patch: Partial<VoiceSnapshot>): void {
    const next = { ...this.snap, ...patch };
    const resendable = next.runs.filter((r) => r.state === "refused" && this.ready && this.socket !== null && this.runSeen.get(r.id) === this.generation).map((r) => r.id);
    next.resendable = sameList(resendable, this.snap.resendable) ? this.snap.resendable : resendable;
    if (shallowSame(next, this.snap)) return;
    this.snap = next;
    this.listeners.forEach((l) => l());
  }
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((x, i) => x === b[i]);
}

function shallowSame(a: VoiceSnapshot, b: VoiceSnapshot): boolean {
  return (Object.keys(a) as (keyof VoiceSnapshot)[]).every((k) => Object.is(a[k], b[k])) && Object.keys(b).length === Object.keys(a).length;
}
