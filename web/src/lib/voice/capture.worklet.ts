// The microphone's side of a call, in the audio thread (AudioWorkletGlobalScope): the context's samples resampled to the
// extension's input rate (resample.ts) and posted to the page as 20 ms PCM16 frames with their level, all-zero frames
// while muted, so the extension's clock never stops. Loaded by audio.ts as `?worker&url`: Vite bundles it into one
// self-contained file of the page's own origin (the hub serves /web with no Content-Security-Policy; one that allows
// the page's own scripts allows this too), and serves it as a module in development.
import { meterLevel, Resampler, rms } from "./resample";

/** Messages the page sends: mute, or a new input rate (`ready` named another one). */
export type CaptureCommand = { type: "mute"; on: boolean } | { type: "rate"; rate: number };
/** What it posts: one frame (PCM16, the machine's byte order: the page writes it little-endian), the rate it was made at
 *  (a frame posted before a `rate` command arrived is still at the old one) and its level 0…1. */
export interface CaptureFrame {
  pcm: ArrayBuffer;
  rate: number;
  level: number;
}

// The worklet scope's globals, typed here: the DOM library does not describe them.
const scope = globalThis as unknown as {
  sampleRate: number;
  AudioWorkletProcessor: new () => { readonly port: MessagePort };
  registerProcessor: (name: string, processor: unknown) => void;
};

class VoiceCapture extends scope.AudioWorkletProcessor {
  private resampler: Resampler;
  private muted = false;
  private silence = new Float32Array(128);

  constructor(options?: { processorOptions?: { rate?: number; muted?: boolean } }) {
    super();
    this.resampler = new Resampler(scope.sampleRate, options?.processorOptions?.rate || 16000);
    this.muted = Boolean(options?.processorOptions?.muted);
    this.port.onmessage = (e: MessageEvent<CaptureCommand>) => {
      const c = e.data;
      if (c.type === "mute") this.muted = c.on;
      else if (c.type === "rate" && c.rate > 0 && c.rate !== this.resampler.to) this.resampler = new Resampler(scope.sampleRate, c.rate);
    };
  }

  process(inputs: Float32Array[][]): boolean {
    const channel = inputs[0]?.[0];
    if (!channel) return true;
    let input = channel;
    if (this.muted) {
      if (this.silence.length !== channel.length) this.silence = new Float32Array(channel.length);
      input = this.silence;
    }
    for (const frame of this.resampler.push(input)) {
      const out: CaptureFrame = { pcm: frame.buffer as ArrayBuffer, rate: this.resampler.to, level: this.muted ? 0 : meterLevel(rms(frame)) };
      this.port.postMessage(out, [out.pcm]);
    }
    return true;
  }
}

scope.registerProcessor("persona-voice-capture", VoiceCapture);
