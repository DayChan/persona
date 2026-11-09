// The browser's microphone and speaker for a call (docs/web/web.md "Voice"): getUserMedia with the browser's echo
// cancellation, noise suppression and gain control; an AudioWorklet (capture.worklet.ts) that resamples the context's
// audio to the extension's input rate in 20 ms PCM16 frames; and a second AudioContext at the voice's output rate that
// plays the PCM it sends back to back, with no gap, until `clear` drops all of it.
//
// Both contexts are made in the constructor, which runs inside the click that starts the call: a browser lets audio
// start only from a user's gesture, and the microphone's permission prompt comes after.
import type { VoiceAudio, VoiceAudioEvent } from "./conversation";
import { VoiceAudioError } from "./conversation";
import { cueSamples, type VoiceCue } from "./cues";
import { meterLevel, pcmFloats } from "./resample";
import type { CaptureCommand, CaptureFrame } from "./capture.worklet";
import workletURL from "./capture.worklet.ts?worker&url";

/** How far ahead of now the first chunk after a gap is scheduled, so it does not start late and click. */
const LEAD_S = 0.05;
/** How long after the last chunk ends "playing" turns off (a chunk arriving just after does not flicker it). */
const DRAIN_GRACE_MS = 120;

export class BrowserVoiceAudio implements VoiceAudio {
  private readonly capture: AudioContext;
  private readonly player: AudioContext;
  private stream: MediaStream | null = null;
  private node: AudioWorkletNode | null = null;
  private source: MediaStreamAudioSourceNode | null = null;
  private stopped = false;
  private ownSuspend = false;
  /** Muted now: a mute tapped while the microphone's permission is being asked must reach the worklet made after it. */
  private muted = false;
  /** The input rate asked for last (`ready` may name another one while the microphone is still being opened). */
  private rate = 0;
  private inputLevel = 0;
  private onEvent: (e: VoiceAudioEvent) => void = () => {};
  /** Bumped by `clear`: a chunk scheduled before it, and its timers, belong to speech that was dropped. */
  private generation = 0;
  private next = 0;
  private sources = new Set<AudioBufferSourceNode>();
  /** The chunks' play times and levels, for the orb. */
  private spans: { start: number; end: number; level: number }[] = [];
  private playing = false;
  private drainTimer: ReturnType<typeof setTimeout> | undefined;
  cancelsEcho: boolean | undefined;

  constructor(outputRate: number) {
    this.capture = new AudioContext();
    let player: AudioContext;
    try {
      player = new AudioContext({ sampleRate: outputRate });
    } catch {
      // an older browser that cannot run a context at that rate: buffers carry their own rate, the context resamples
      player = new AudioContext();
    }
    this.player = player;
    void this.capture.resume().catch(() => {});
    void this.player.resume().catch(() => {});
  }

  async start(o: { inputRate: number; muted: boolean; onFrame: (frame: Int16Array, rate: number) => void; onEvent: (e: VoiceAudioEvent) => void }): Promise<void> {
    this.onEvent = o.onEvent;
    this.muted = o.muted;
    this.rate = o.inputRate;
    let stream: MediaStream;
    try {
      stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true, channelCount: 1 } });
    } catch (e) {
      const name = (e as { name?: string })?.name;
      throw new VoiceAudioError(name === "NotAllowedError" || name === "SecurityError" ? "permission" : name === "NotFoundError" || name === "OverconstrainedError" ? "noMicrophone" : "failed", String(e));
    }
    if (this.stopped) {
      stream.getTracks().forEach((t) => t.stop());
      return;
    }
    this.stream = stream;
    const track = stream.getAudioTracks()[0];
    if (!track) throw new VoiceAudioError("noMicrophone");
    this.cancelsEcho = typeof track.getSettings().echoCancellation === "boolean" ? track.getSettings().echoCancellation : undefined;
    try {
      await this.capture.audioWorklet.addModule(workletURL);
    } catch (e) {
      throw new VoiceAudioError("failed", String(e));
    }
    if (this.stopped) return;
    const node = new AudioWorkletNode(this.capture, "persona-voice-capture", {
      numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1], processorOptions: { rate: this.rate, muted: this.muted },
    });
    node.port.onmessage = (e: MessageEvent<CaptureFrame>) => {
      if (this.stopped) return;
      this.inputLevel = this.muted ? 0 : e.data.level;
      // the rate the worklet made it at: a frame already on its way when a new rate was asked for is still the old one
      o.onFrame(new Int16Array(e.data.pcm), e.data.rate);
    };
    const source = this.capture.createMediaStreamSource(stream);
    source.connect(node);
    // The worklet makes no sound; connected through a muted gain so the graph pulls it on every browser.
    const sink = this.capture.createGain();
    sink.gain.value = 0;
    node.connect(sink).connect(this.capture.destination);
    this.node = node;
    this.source = source;
    track.onended = () => {
      if (!this.stopped) this.onEvent({ kind: "failed" });
    };
    // A context the browser suspends (another app took the audio, the tab was put to sleep) ends the call: the
    // microphone would go on sending nothing.
    const watch = (ctx: AudioContext) => {
      ctx.onstatechange = () => {
        if (this.stopped || this.ownSuspend) return;
        if ((ctx.state as string) === "interrupted" || ctx.state === "suspended") this.onEvent({ kind: "interrupted" });
      };
    };
    watch(this.capture);
    watch(this.player);
  }

  setInputRate(rate: number): void {
    this.rate = rate;
    this.node?.port.postMessage({ type: "rate", rate } satisfies CaptureCommand);
  }

  setMuted(on: boolean): void {
    this.muted = on;
    this.node?.port.postMessage({ type: "mute", on } satisfies CaptureCommand);
    if (on) this.inputLevel = 0;
  }

  play(pcm: ArrayBuffer, rate: number): void {
    if (this.stopped) return;
    const samples = pcmFloats(pcm);
    if (!samples.length) return;
    const buffer = this.player.createBuffer(1, samples.length, rate);
    buffer.copyToChannel(samples, 0);
    const node = this.player.createBufferSource();
    node.buffer = buffer;
    node.connect(this.player.destination);
    const now = this.player.currentTime;
    const at = Math.max(now + (this.next > now ? 0 : LEAD_S), this.next);
    node.start(at);
    this.next = at + buffer.duration;
    this.sources.add(node);
    node.onended = () => this.sources.delete(node);
    this.spans.push({ start: at, end: this.next, level: meterLevel(rmsOf(samples)) });
    if (this.spans.length > 400) this.spans.splice(0, this.spans.length - 400);
    if (!this.playing) {
      this.playing = true;
      this.onEvent({ kind: "playing", on: true });
    }
    this.watchDrain();
  }

  clear(): void {
    this.generation++;
    for (const s of this.sources) {
      try {
        s.stop();
      } catch {
        // not started yet, or already over
      }
    }
    this.sources.clear();
    this.spans = [];
    this.next = 0;
    clearTimeout(this.drainTimer);
    if (this.playing) {
      this.playing = false;
      this.onEvent({ kind: "playing", on: false });
    }
  }

  cue(cue: VoiceCue): void {
    if (this.stopped) return;
    const samples = cueSamples(cue, this.player.sampleRate);
    if (!samples.length) return;
    const buffer = this.player.createBuffer(1, samples.length, this.player.sampleRate);
    buffer.copyToChannel(samples, 0);
    const node = this.player.createBufferSource();
    node.buffer = buffer;
    node.connect(this.player.destination);
    node.start();
  }

  levels(): { input: number; output: number } {
    const now = this.player.currentTime;
    const span = this.spans.find((s) => s.start <= now && now < s.end);
    return { input: this.inputLevel, output: span?.level ?? 0 };
  }

  stop(): void {
    if (this.stopped) return;
    this.stopped = true;
    this.clear();
    this.node?.port.close();
    this.node?.disconnect();
    this.source?.disconnect();
    this.stream?.getTracks().forEach((t) => t.stop());
    this.ownSuspend = true;
    void this.capture.close().catch(() => {});
    void this.player.close().catch(() => {});
  }

  /** "playing" off once the queue has played out. */
  private watchDrain(): void {
    clearTimeout(this.drainTimer);
    const generation = this.generation;
    const left = Math.max(0, (this.next - this.player.currentTime) * 1000) + DRAIN_GRACE_MS;
    this.drainTimer = setTimeout(() => {
      if (generation !== this.generation || this.stopped) return;
      if (this.player.currentTime < this.next) return this.watchDrain();
      this.playing = false;
      this.onEvent({ kind: "playing", on: false });
    }, left);
  }
}

function rmsOf(samples: Float32Array): number {
  let sum = 0;
  for (let i = 0; i < samples.length; i++) sum += samples[i] * samples[i];
  return Math.sqrt(sum / samples.length);
}

