import { describe, expect, it } from "vitest";
import type { Session } from "../types";
import { VoiceAudioError, VoiceConversation, type ModelPick, type VoiceAudio, type VoiceAudioEvent, type VoiceDeps, type VoiceSocket } from "./conversation";
import { pickModel } from "./support";
import { HELD_FRAMES, Uplink } from "./uplink";

class FakeSocket implements VoiceSocket {
  binaryType = "blob";
  readyState = 0;
  onopen: ((ev: unknown) => void) | null = null;
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  sent: (string | ArrayBuffer)[] = [];
  closed = false;
  constructor(readonly url: string) {}
  send(data: string | ArrayBuffer) {
    this.sent.push(data);
  }
  close() {
    this.closed = true;
    this.readyState = 3;
  }
  open() {
    this.readyState = 1;
    this.onopen?.({});
  }
  frame(o: Record<string, unknown>) {
    this.onmessage?.({ data: JSON.stringify(o) });
  }
  pcm(bytes: number) {
    this.onmessage?.({ data: new ArrayBuffer(bytes) });
  }
  drop() {
    this.readyState = 3;
    this.onclose?.({ code: 1006 });
  }
  texts() {
    return this.sent.filter((d): d is string => typeof d === "string").map((d) => JSON.parse(d));
  }
  binary() {
    return this.sent.filter((d): d is ArrayBuffer => typeof d !== "string");
  }
}

class FakeAudio implements VoiceAudio {
  started = false;
  stopped = false;
  muted = false;
  rate = 0;
  played: number[] = [];
  clears = 0;
  cues: string[] = [];
  cancelsEcho: boolean | undefined = true;
  failWith: VoiceAudioError | null = null;
  frame: ((f: Int16Array, rate: number) => void) | null = null;
  event: ((e: VoiceAudioEvent) => void) | null = null;
  async start(o: { inputRate: number; muted: boolean; onFrame: (f: Int16Array, rate: number) => void; onEvent: (e: VoiceAudioEvent) => void }) {
    if (this.failWith) throw this.failWith;
    this.started = true;
    this.rate = o.inputRate;
    this.muted = o.muted;
    this.frame = o.onFrame;
    this.event = o.onEvent;
  }
  setInputRate(rate: number) {
    this.rate = rate;
  }
  setMuted(on: boolean) {
    this.muted = on;
  }
  play(pcm: ArrayBuffer) {
    this.played.push(pcm.byteLength);
  }
  clear() {
    this.clears++;
  }
  cue(c: string) {
    this.cues.push(c);
  }
  levels() {
    return { input: 0.2, output: 0 };
  }
  stop() {
    this.stopped = true;
  }
  /** 20 ms of the microphone at the rate it records at. */
  speak(n = 1) {
    for (let i = 0; i < n; i++) this.frame?.(new Int16Array(this.rate / 50), this.rate);
  }
}

/** A clock moved by hand. */
class Clock {
  now = 1_000_000;
  private timers: { at: number; fn: () => void; id: number }[] = [];
  private next = 1;
  set = (fn: () => void, ms: number) => {
    const id = this.next++;
    this.timers.push({ at: this.now + ms, fn, id });
    return id;
  };
  clear = (id: unknown) => {
    this.timers = this.timers.filter((t) => t.id !== id);
  };
  advance(ms: number) {
    const until = this.now + ms;
    for (;;) {
      this.timers.sort((a, b) => a.at - b.at);
      const t = this.timers[0];
      if (!t || t.at > until) break;
      this.timers.shift();
      this.now = t.at;
      t.fn();
    }
    this.now = until;
  }
}

const flush = () => new Promise((r) => setTimeout(r, 0));

function setup(o: { probe?: number | null; model?: ModelPick; session?: Partial<Session>; token?: boolean } = {}) {
  const sockets: FakeSocket[] = [];
  const audio = new FakeAudio();
  const clock = new Clock();
  const deps: VoiceDeps = {
    socketURL: () => (o.token === false ? null : "ws://127.0.0.1/api/v1/extensions/voice/ws?token=t"),
    openSocket: (url) => {
      const s = new FakeSocket(url);
      sockets.push(s);
      return s;
    },
    audio: () => audio,
    probe: async () => (o.probe === undefined ? 503 : o.probe),
    resolveModel: async () => o.model ?? { ok: true, model: "rt/m1" },
    voice: () => "soft",
    session: () => ({ machine_id: "m", session_id: "s", ...o.session }) as Session,
    now: () => clock.now,
    setTimer: clock.set,
    clearTimer: clock.clear,
  };
  const call = new VoiceConversation({ mid: "m", sid: "s", extensionID: "voice", recent: [{ role: "user", text: "earlier" }], deps });
  return { call, sockets, audio, clock, last: () => sockets[sockets.length - 1] };
}

const READY = { type: "ready", protocol: 2, features: ["target", "runs", "asks", "resume", "suspend"], input_rate: 16000, output_rate: 24000, target: { machine_id: "m", session_id: "s", kind: "agent" } };

async function connected(o: Parameters<typeof setup>[0] = {}) {
  const t = setup(o);
  await t.call.begin();
  t.last().open();
  t.last().frame(READY);
  return t;
}

describe("a voice call", () => {
  it("opens the microphone, then sends start with the target, recent turns and the model", async () => {
    const { call, audio, last } = setup();
    await call.begin();
    expect(audio.started).toBe(true);
    expect(call.getSnapshot().phase).toEqual({ kind: "connecting" });
    expect(last().binaryType).toBe("arraybuffer");
    last().open();
    expect(last().texts()).toEqual([{ type: "start", protocol: 2, model: "rt/m1", voice: "soft", target: { machine_id: "m", session_id: "s" }, recent: [{ role: "user", text: "earlier" }], echo_cancellation: true }]);
  });

  it("holds what was said before ready, then sends it and the rest as PCM frames", async () => {
    const { call, audio, last } = setup();
    await call.begin();
    last().open();
    audio.speak(10);
    expect(last().binary()).toHaveLength(0);
    last().frame(READY);
    expect(call.getSnapshot().phase).toEqual({ kind: "live", state: "listening" });
    expect(audio.cues).toEqual(["ready"]);
    audio.speak(5);
    expect(last().binary()).toHaveLength(15);
    expect(last().binary().every((b) => b.byteLength === 640)).toBe(true);
    expect(call.sent).toEqual({ frames: 15, bytes: 15 * 640 });
  });

  it("records at the rate ready asks for, and resamples what it held", async () => {
    const { call, audio, last } = setup();
    await call.begin();
    last().open();
    audio.speak(3);
    last().frame({ ...READY, input_rate: 24000 });
    expect(audio.rate).toBe(24000);
    expect(last().binary().map((b) => b.byteLength)).toEqual([960, 960, 960]);
  });

  it("plays the voice's PCM and drops it on clear", async () => {
    const { audio, last } = await connected();
    last().pcm(960);
    last().pcm(960);
    expect(audio.played).toEqual([960, 960]);
    last().frame({ type: "clear" });
    expect(audio.clears).toBe(1);
  });

  it("is speaking while the voice plays, and the orb interrupts it", async () => {
    const { call, audio, last } = await connected();
    audio.event?.({ kind: "playing", on: true });
    expect(call.getSnapshot().phase).toEqual({ kind: "live", state: "speaking" });
    call.interruptSpeech();
    expect(last().texts().at(-1)).toEqual({ type: "interrupt" });
    expect(audio.clears).toBe(1);
  });

  it("keeps captions in the order said, and hears the user while their line is open", async () => {
    const { call, clock, last } = await connected();
    last().frame({ type: "transcript", id: "u1", role: "user", text: "帮我", final: false });
    expect(call.getSnapshot().phase).toEqual({ kind: "live", state: "hearing" });
    clock.advance(2100);
    expect(call.getSnapshot().phase).toEqual({ kind: "live", state: "listening" });
    last().frame({ type: "transcript", id: "a3", role: "assistant", text: "好，", final: false });
    last().frame({ type: "transcript", id: "u3", role: "user", text: "第一个", final: true, before: "a3" });
    expect(call.getSnapshot().captions.map((c) => c.text)).toEqual(["帮我", "第一个", "好，"]);
  });

  it("tracks runs, asks, link and tools", async () => {
    const { call, last } = await connected();
    last().frame({ type: "run", id: "voice-1", task: "fix", state: "sending", msg_ids: ["voice-1"], base_seq: 7, started_at: "2026-11-06T10:00:00Z" });
    last().frame({ type: "run", id: "voice-1", task: "fix", state: "working", msg_ids: ["voice-1"], base_seq: 7, started_at: "2026-11-06T10:00:00Z" });
    last().frame({ type: "ask", interaction_id: "ix1", state: "asking" });
    last().frame({ type: "link", state: "paused" });
    last().frame({ type: "tool", id: "t1", label: "Searching", done: false });
    let s = call.getSnapshot();
    expect(s.runs.map((r) => r.state)).toEqual(["working"]);
    expect(s.askingID).toBe("ix1");
    expect(s.link).toBe("paused");
    expect(s.tools).toEqual([{ id: "t1", label: "Searching" }]);
    last().frame({ type: "ask", interaction_id: "ix1", state: "answered", by: "voice" });
    last().frame({ type: "tool", id: "t1", done: true });
    s = call.getSnapshot();
    expect(s.askingID).toBeNull();
    expect(s.tools).toEqual([]);
  });

  it("resends a refused run only on the connection that reported it, Clear and Send with clearable", async () => {
    const { call, last, clock } = await connected();
    last().frame({ type: "run", id: "voice-2", task: "lint", state: "refused", code: "input_draft", draft: "[⧉ x] fix it", clearable: "fix it" });
    expect(call.getSnapshot().resendable).toEqual(["voice-2"]);
    expect(call.resend("voice-2", true)).toBe(true);
    expect(last().texts().at(-1)).toEqual({ type: "resend", id: "voice-2", clear_draft: "fix it" });
    last().drop();
    expect(call.getSnapshot().resendable).toEqual([]);
    clock.advance(1000);
    last().open();
    last().frame(READY);
    expect(call.canResend("voice-2")).toBe(false);
    expect(call.resend("voice-2")).toBe(false);
  });

  it("reconnects 1, 4 and 10 seconds after a drop, with history and the open runs", async () => {
    const { call, sockets, last, clock, audio } = await connected();
    last().frame({ type: "transcript", id: "u1", role: "user", text: "fix the tests", final: true });
    last().frame({ type: "run", id: "voice-1", task: "fix", state: "working", msg_ids: ["voice-1"], base_seq: 7, started_at: 1793872800123 });
    last().frame({ type: "run", id: "voice-0", task: "old", state: "done", msg_ids: ["voice-0"], base_seq: 3 });
    last().frame({ type: "link", state: "paused" });
    last().drop();
    expect(call.getSnapshot().phase).toEqual({ kind: "reconnecting", attempt: 1 });
    expect(audio.cues).toContain("reconnecting");
    audio.speak(4); // held while it reconnects
    clock.advance(999);
    expect(sockets).toHaveLength(1);
    clock.advance(1);
    expect(sockets).toHaveLength(2);
    // a new connection reports again what still holds
    expect(call.getSnapshot().link).toBe("live");
    last().open();
    expect(last().texts()[0]).toEqual({
      type: "start", protocol: 2, model: "rt/m1", voice: "soft", target: { machine_id: "m", session_id: "s" }, recent: [{ role: "user", text: "earlier" }],
      history: [{ role: "user", text: "fix the tests" }], echo_cancellation: true,
      resume: { runs: [{ id: "voice-1", msg_ids: ["voice-1"], task: "fix", base_seq: 7, started_at: 1793872800123, state: "working" }] },
    });
    last().frame(READY);
    expect(last().binary()).toHaveLength(4);
    expect(call.getSnapshot().phase).toEqual({ kind: "live", state: "listening" });
  });

  it("gives up after three failed reconnects: Continue picks it up again", async () => {
    const { call, sockets, last, clock } = await connected();
    last().drop();
    for (const wait of [1000, 4000, 10_000]) {
      clock.advance(wait);
      last().open();
      last().drop();
      await flush();
    }
    expect(call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "lost" } });
    expect(sockets).toHaveLength(4);
    await call.restart();
    last().open();
    expect(sockets).toHaveLength(5);
  });

  it("ends for good on a handshake the hub refused with 401 or 403", async () => {
    for (const status of [401, 403]) {
      const { call, last } = setup({ probe: status });
      await call.begin();
      last().drop();
      await flush();
      expect(call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "rejected", status } });
    }
  });

  it("ends on a first 404, but a 404 after a ready connection is one more try", async () => {
    const first = setup({ probe: 404 });
    await first.call.begin();
    first.last().drop();
    await flush();
    expect(first.call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "rejected", status: 404 } });
    const later = await connected({ probe: 404 });
    later.last().drop();
    later.clock.advance(1000);
    later.last().drop(); // the extension being applied again: refused before it opened
    await flush();
    expect(later.call.getSnapshot().phase).toEqual({ kind: "reconnecting", attempt: 2 });
  });

  it("gives a connection 15 seconds to say ready", async () => {
    const { call, sockets, last, clock } = setup();
    await call.begin();
    last().open();
    clock.advance(15_000);
    expect(sockets[0].closed).toBe(true);
    expect(call.getSnapshot().phase).toEqual({ kind: "reconnecting", attempt: 1 });
  });

  it("ends on a fatal error, keeping the extension's words for Details; a notice stays on the bar", async () => {
    const { call, last, audio } = await connected();
    last().frame({ type: "error", code: "machine_offline", message: "offline", fatal: false });
    expect(call.getSnapshot().notice).toEqual({ code: "machine_offline", message: "offline", fatal: false });
    last().frame({ type: "error", code: "upstream_forbidden", message: "401 from upstream", fatal: true });
    expect(call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "failed", code: "upstream_forbidden", message: "401 from upstream" } });
    expect(audio.stopped).toBe(true);
    expect(last().closed).toBe(true);
  });

  it("refuses an extension that is not protocol 2, or bound another conversation", async () => {
    const old = setup();
    await old.call.begin();
    old.last().open();
    old.last().frame({ type: "ready", input_rate: 16000 });
    expect(old.call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "incompatible" } });
    const other = setup();
    await other.call.begin();
    other.last().open();
    other.last().frame({ ...READY, target: { machine_id: "m", session_id: "elsewhere" } });
    expect(other.call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "incompatible" } });
  });

  it("says why the microphone did not open", async () => {
    for (const [reason, end] of [["permission", "permissionDenied"], ["noMicrophone", "noMicrophone"], ["failed", "audioFailed"]] as const) {
      const t = setup();
      t.audio.failWith = new VoiceAudioError(reason);
      await t.call.begin();
      expect(t.call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: end } });
      expect(t.sockets).toHaveLength(0);
    }
  });

  it("stops before connecting when the chosen model cannot be used, or the page signed out", async () => {
    const t = setup({ model: { ok: false, why: "读不到插件模型列表" } });
    await t.call.begin();
    expect(t.call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "modelUnavailable", why: "读不到插件模型列表" } });
    const out = setup({ token: false });
    await out.call.begin();
    expect(out.call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "signedOut" } });
  });

  it("ends when the browser suspends its audio", async () => {
    const { call, audio } = await connected();
    audio.event?.({ kind: "interrupted" });
    expect(call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "interrupted" } });
  });

  it("sends silence while muted (the microphone's frames go on) and says so", async () => {
    const { call, audio, last } = await connected();
    call.setMuted(true);
    expect(audio.muted).toBe(true);
    expect(call.getSnapshot().muted).toBe(true);
    audio.speak(2);
    expect(last().binary()).toHaveLength(2);
  });

  it("ends after ten idle minutes, but not while the conversation works", async () => {
    const busy = await connected({ session: { state: "working" } });
    busy.clock.advance(11 * 60_000);
    expect(busy.call.getSnapshot().phase.kind).toBe("live");
    const idle = await connected();
    idle.clock.advance(10 * 60_000 + 30_000);
    expect(idle.call.getSnapshot().phase).toEqual({ kind: "ended", end: { kind: "idle" } });
  });

  it("types a line, and changing the voice picks the call up on a new connection", async () => {
    const { call, sockets, last } = await connected();
    call.say("  hello ");
    expect(last().texts().at(-1)).toEqual({ type: "text", text: "hello" });
    call.changeVoice("calm");
    expect(sockets).toHaveLength(2);
    expect(sockets[0].closed).toBe(true);
    last().open();
    expect(last().texts()[0]).toMatchObject({ type: "start", voice: "calm" });
  });

  it("closes the socket and the microphone at the end, and ignores late events", async () => {
    const { call, audio, last } = await connected();
    const sock = last();
    call.end({ kind: "user" });
    expect(sock.closed).toBe(true);
    expect(audio.stopped).toBe(true);
    sock.frame({ type: "transcript", id: "a9", role: "assistant", text: "late", final: true });
    expect(call.getSnapshot().captions).toEqual([]);
  });
});

describe("the uplink", () => {
  it("holds at most three seconds before ready", () => {
    const u = new Uplink();
    for (let i = 0; i < HELD_FRAMES + 20; i++) u.push(new Int16Array(320).fill(i), 16000);
    expect(u.waiting).toBe(HELD_FRAMES);
    const sent: ArrayBuffer[] = [];
    u.open(16000, (b) => sent.push(b));
    expect(sent).toHaveLength(HELD_FRAMES);
    expect(new DataView(sent[0]).getInt16(0, true)).toBe(20);
  });
});

describe("the call's model", () => {
  it("is the use plugin.<id>'s, as Android and iOS decide", () => {
    expect(pickModel(null, null)).toEqual({ ok: true });
    expect(pickModel({ model: "voice-fast", status: "未验证" }, { models: [{ id: "voice-fast" }] })).toEqual({ ok: true, model: "voice-fast" });
    expect(pickModel({ model: "voice-fast", status: "读不到插件模型列表" }, null)).toEqual({ ok: false, why: "读不到插件模型列表" });
    expect(pickModel({ model: "voice-retired" }, { models: [{ id: "voice-deep" }] })).toEqual({ ok: false, why: "voice-retired" });
    expect(pickModel({ model: "voice-retired" }, { models: [] })).toEqual({ ok: true, model: "voice-retired" });
    expect(pickModel({}, null)).toEqual({ ok: true });
  });
});
