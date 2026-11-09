// BrowserVoiceAudio against fake Web Audio and getUserMedia: what reaches the capture worklet and the call (the real audio
// path is walked in a browser by web/e2e/voice.mjs).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BrowserVoiceAudio, KEEP_AWAKE_LEVEL } from "./audio";
import type { CaptureFrame } from "./capture.worklet";

/** A node of the fake graph: what it is and where its output goes. */
class FakeNode {
  outs: FakeNode[] = [];
  constructor(readonly kind: string) {}
  connect<T>(x: T): T {
    this.outs.push(x as unknown as FakeNode);
    return x;
  }
  disconnect() {
    this.outs = [];
  }
}
class FakeGain extends FakeNode {
  gain = { value: 1 };
  constructor() {
    super("gain");
  }
}
class FakeConstant extends FakeNode {
  offset = { value: 1 };
  started = false;
  stopped = false;
  constructor() {
    super("constant");
  }
  start() {
    this.started = true;
  }
  stop() {
    this.stopped = true;
  }
}

/** Every path from `from` to `to`, as the nodes along it. */
function paths(from: FakeNode, to: FakeNode, seen: FakeNode[] = []): FakeNode[][] {
  if (from === to) return [[...seen, from]];
  return from.outs.flatMap((next) => (seen.includes(next) ? [] : paths(next, to, [...seen, from])));
}

class FakePort {
  sent: unknown[] = [];
  onmessage: ((e: { data: CaptureFrame }) => void) | null = null;
  closed = false;
  postMessage(m: unknown) {
    this.sent.push(m);
  }
  close() {
    this.closed = true;
  }
}

const nodes: FakeWorkletNode[] = [];
class FakeWorkletNode extends FakeNode {
  port = new FakePort();
  constructor(
    readonly ctx: unknown,
    readonly name: string,
    readonly options: { processorOptions: { rate: number; muted: boolean } },
  ) {
    super("worklet");
    nodes.push(this);
  }
}

const contexts: FakeContext[] = [];
class FakeContext {
  state = "running";
  closed = false;
  sampleRate: number;
  currentTime = 0;
  destination = new FakeNode("destination");
  made: FakeNode[] = [];
  onstatechange: (() => void) | null = null;
  audioWorklet = { addModule: async () => {} };
  constructor(o?: { sampleRate?: number }) {
    this.sampleRate = o?.sampleRate ?? 48000;
    contexts.push(this);
  }
  resume() {
    return Promise.resolve();
  }
  close() {
    this.closed = true;
    return Promise.resolve();
  }
  private make<T extends FakeNode>(node: T): T {
    this.made.push(node);
    return node;
  }
  createMediaStreamSource() {
    return this.make(new FakeNode("microphone"));
  }
  createGain() {
    return this.make(new FakeGain());
  }
  createConstantSource() {
    return this.make(new FakeConstant());
  }
}

function fakeStream() {
  const track = { stopped: false, onended: null as null | (() => void), stop() { this.stopped = true; }, getSettings: () => ({ echoCancellation: true }) };
  return { track, stream: { getTracks: () => [track], getAudioTracks: () => [track] } };
}

let answer: (s: unknown) => void;
beforeEach(() => {
  nodes.length = 0;
  contexts.length = 0;
  vi.stubGlobal("AudioContext", FakeContext);
  vi.stubGlobal("AudioWorkletNode", FakeWorkletNode);
  vi.stubGlobal("navigator", { mediaDevices: { getUserMedia: () => new Promise((resolve) => (answer = resolve)) } });
});
/** Every audio a test made, stopped after it (a call's pace check runs on an interval). */
const audios: BrowserVoiceAudio[] = [];
const owned = (audio: BrowserVoiceAudio) => (audios.push(audio), audio);
afterEach(() => {
  audios.splice(0).forEach((a) => a.stop());
  vi.unstubAllGlobals();
});

const opts = (frames: [Int16Array, number][] = []) => ({ inputRate: 16000, muted: false, onFrame: (f: Int16Array, r: number) => frames.push([f, r]), onEvent: () => {} });

describe("BrowserVoiceAudio", () => {
  it("a mute tapped while the microphone's permission is asked reaches the worklet made after it", async () => {
    const audio = owned(new BrowserVoiceAudio(24000));
    const started = audio.start(opts());
    audio.setMuted(true);
    answer(fakeStream().stream);
    await started;
    expect(nodes).toHaveLength(1);
    expect(nodes[0].options.processorOptions).toEqual({ rate: 16000, muted: true });
  });

  it("passes each frame on at the rate the worklet made it, not the one asked for since", async () => {
    const frames: [Int16Array, number][] = [];
    const audio = owned(new BrowserVoiceAudio(24000));
    const started = audio.start(opts(frames));
    answer(fakeStream().stream);
    await started;
    audio.setInputRate(24000);
    expect(nodes[0].port.sent).toContainEqual({ type: "rate", rate: 24000 });
    // posted before the worklet heard of the new rate, then after
    nodes[0].port.onmessage!({ data: { pcm: new Int16Array(320).buffer, rate: 16000, level: 0.2 } });
    nodes[0].port.onmessage!({ data: { pcm: new Int16Array(480).buffer, rate: 24000, level: 0.2 } });
    expect(frames.map(([f, r]) => [f.length, r])).toEqual([[320, 16000], [480, 24000]]);
  });

  it("reports no input level while muted", async () => {
    const audio = owned(new BrowserVoiceAudio(24000));
    const started = audio.start(opts());
    answer(fakeStream().stream);
    await started;
    nodes[0].port.onmessage!({ data: { pcm: new Int16Array(320).buffer, rate: 16000, level: 0.5 } });
    expect(audio.levels().input).toBe(0.5);
    audio.setMuted(true);
    expect(nodes[0].port.sent).toContainEqual({ type: "mute", on: true });
    nodes[0].port.onmessage!({ data: { pcm: new Int16Array(320).buffer, rate: 16000, level: 0.5 } });
    expect(audio.levels().input).toBe(0);
  });

  it("keeps the capture context's output above zero and inaudible, and never plays the microphone back", async () => {
    const audio = owned(new BrowserVoiceAudio(24000));
    const [capture, player] = contexts;
    // from the click on, before the microphone's permission is answered
    const awake = capture.made.filter((n): n is FakeConstant => n instanceof FakeConstant);
    expect(awake).toHaveLength(1);
    expect(awake[0].started).toBe(true);
    expect(awake[0].outs).toEqual([capture.destination]);
    // not zero (Chrome's silent-sink suspender looks for exact zeros), under half a step of a 24-bit converter
    expect(awake[0].offset.value).toBe(KEEP_AWAKE_LEVEL);
    expect(KEEP_AWAKE_LEVEL).toBeGreaterThan(0);
    expect(KEEP_AWAKE_LEVEL).toBeLessThan(2 ** -24);

    const started = audio.start(opts());
    answer(fakeStream().stream);
    await started;
    const microphone = capture.made.find((n) => n.kind === "microphone")!;
    expect(microphone.outs).toEqual([nodes[0]]);
    // the only way from the microphone to the speaker is the worklet's unwritten output, through a gain of 0
    const toSpeaker = paths(microphone, capture.destination);
    expect(toSpeaker.map((p) => p.map((n) => n.kind))).toEqual([["microphone", "worklet", "gain", "destination"]]);
    expect((toSpeaker[0][2] as FakeGain).gain.value).toBe(0);
    // the player is not touched
    expect(player.made).toEqual([]);

    audio.stop();
    expect(awake[0].stopped).toBe(true);
    expect(awake[0].outs).toEqual([]);
  });

  it("logs at debug level when the microphone's frames fall behind the wall clock", async () => {
    vi.useFakeTimers();
    const debug = vi.spyOn(console, "debug").mockImplementation(() => {});
    try {
      const audio = owned(new BrowserVoiceAudio(24000));
      const started = audio.start(opts());
      answer(fakeStream().stream);
      await started;
      const capture = contexts[0];
      const frame = () => nodes[0].port.onmessage!({ data: { pcm: new Int16Array(320).buffer, rate: 16000, level: 0 } });
      // five seconds at 50 frames a second: quiet
      frame();
      for (let i = 0; i < 250; i++) {
        vi.advanceTimersByTime(20);
        capture.currentTime += 0.02;
        frame();
      }
      expect(debug).not.toHaveBeenCalled();
      // the context's clock at a quarter of real time: a frame every 80 ms
      for (let i = 0; i < 63; i++) {
        vi.advanceTimersByTime(80);
        capture.currentTime += 0.02;
        frame();
      }
      expect(debug).toHaveBeenCalledTimes(1);
      expect(String(debug.mock.calls[0][0])).toMatch(/^\[voice\] microphone frames fell behind: \d+ in 5\.0 s, 250 expected; the capture context's clock moved 1\.\d s/);
      audio.stop();
      vi.advanceTimersByTime(20_000);
      expect(debug).toHaveBeenCalledTimes(1);
    } finally {
      debug.mockRestore();
      vi.useRealTimers();
    }
  });

  it("releases the microphone and closes both contexts on stop, even when the microphone answers after it", async () => {
    const audio = owned(new BrowserVoiceAudio(24000));
    const started = audio.start(opts());
    audio.stop();
    const { track, stream } = fakeStream();
    answer(stream);
    await started;
    expect(track.stopped).toBe(true);
    expect(nodes).toHaveLength(0);
    expect(contexts.map((c) => c.closed)).toEqual([true, true]);

    const again = owned(new BrowserVoiceAudio(24000));
    const live = fakeStream();
    const going = again.start(opts());
    answer(live.stream);
    await going;
    again.stop();
    expect(live.track.stopped).toBe(true);
    expect(nodes[0].port.closed).toBe(true);
    expect(contexts.slice(2).map((c) => c.closed)).toEqual([true, true]);
  });
});
