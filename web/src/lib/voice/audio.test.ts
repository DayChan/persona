// BrowserVoiceAudio against fake Web Audio and getUserMedia: what reaches the capture worklet and the call (the real audio
// path is walked in a browser by web/e2e/voice.mjs).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { BrowserVoiceAudio } from "./audio";
import type { CaptureFrame } from "./capture.worklet";

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
class FakeWorkletNode {
  port = new FakePort();
  constructor(
    readonly ctx: unknown,
    readonly name: string,
    readonly options: { processorOptions: { rate: number; muted: boolean } },
  ) {
    nodes.push(this);
  }
  connect(x: unknown) {
    return x;
  }
  disconnect() {}
}

const contexts: FakeContext[] = [];
class FakeContext {
  state = "running";
  closed = false;
  sampleRate: number;
  currentTime = 0;
  destination = {};
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
  createMediaStreamSource() {
    return { connect: () => {}, disconnect: () => {} };
  }
  createGain() {
    return { gain: { value: 1 }, connect: (x: unknown) => x };
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
afterEach(() => {
  vi.unstubAllGlobals();
});

const opts = (frames: [Int16Array, number][] = []) => ({ inputRate: 16000, muted: false, onFrame: (f: Int16Array, r: number) => frames.push([f, r]), onEvent: () => {} });

describe("BrowserVoiceAudio", () => {
  it("a mute tapped while the microphone's permission is asked reaches the worklet made after it", async () => {
    const audio = new BrowserVoiceAudio(24000);
    const started = audio.start(opts());
    audio.setMuted(true);
    answer(fakeStream().stream);
    await started;
    expect(nodes).toHaveLength(1);
    expect(nodes[0].options.processorOptions).toEqual({ rate: 16000, muted: true });
  });

  it("passes each frame on at the rate the worklet made it, not the one asked for since", async () => {
    const frames: [Int16Array, number][] = [];
    const audio = new BrowserVoiceAudio(24000);
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
    const audio = new BrowserVoiceAudio(24000);
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

  it("releases the microphone and closes both contexts on stop, even when the microphone answers after it", async () => {
    const audio = new BrowserVoiceAudio(24000);
    const started = audio.start(opts());
    audio.stop();
    const { track, stream } = fakeStream();
    answer(stream);
    await started;
    expect(track.stopped).toBe(true);
    expect(nodes).toHaveLength(0);
    expect(contexts.map((c) => c.closed)).toEqual([true, true]);

    const again = new BrowserVoiceAudio(24000);
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
