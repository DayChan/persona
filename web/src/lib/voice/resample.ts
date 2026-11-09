// The microphone's audio at the rate the voice extension asks for (`ready.input_rate`, 16 kHz or 24 kHz), from whatever
// rate the browser's audio context runs at (44.1 kHz, 48 kHz…). Used by the capture worklet (capture.worklet.ts) and by
// the page for audio held before `ready` at a rate that turned out wrong. Pure, so the tests can feed it tones.
//
// Downsampling first runs a windowed-sinc low-pass at the source rate (cut-off just under the target's Nyquist), so
// nothing above it folds back as aliasing; then each output sample is read at its fractional source position by linear
// interpolation, which is accurate once the band above the cut-off is gone. Upsampling needs no filter, only the
// interpolation. Dropping samples alone (48k → 16k by keeping every third) would fold 8–24 kHz hiss into the speech band.

/** 20 ms of audio: what one binary frame carries. */
export const FRAME_MS = 20;

export function frameSamples(rate: number): number {
  return Math.round((rate * FRAME_MS) / 1000);
}

/** The low-pass's taps: a Blackman-windowed sinc with its cut-off at `cutoff` (a share of the source rate, 0–0.5), its
 *  gain at 0 Hz exactly 1. */
export function lowPassTaps(cutoff: number, count: number): Float32Array {
  const taps = new Float32Array(count);
  const mid = (count - 1) / 2;
  let sum = 0;
  for (let i = 0; i < count; i++) {
    const x = i - mid;
    const sinc = x === 0 ? 2 * cutoff : Math.sin(2 * Math.PI * cutoff * x) / (Math.PI * x);
    const window = 0.42 - 0.5 * Math.cos((2 * Math.PI * i) / (count - 1)) + 0.08 * Math.cos((4 * Math.PI * i) / (count - 1));
    taps[i] = sinc * window;
    sum += taps[i];
  }
  for (let i = 0; i < count; i++) taps[i] /= sum;
  return taps;
}

/** Float samples (-1…1) to PCM16, rounded and clipped. */
export function toPCM16(sample: number): number {
  const s = Math.max(-1, Math.min(1, sample));
  return Math.round(s < 0 ? s * 32768 : s * 32767);
}

/** The root mean square of PCM16 samples, 0…1 of full scale. */
export function rms(pcm: Int16Array): number {
  if (!pcm.length) return 0;
  let sum = 0;
  for (let i = 0; i < pcm.length; i++) {
    const s = pcm[i] / 32768;
    sum += s * s;
  }
  return Math.sqrt(sum / pcm.length);
}

/** A level for the orb (0…1) from an RMS: -60 dBFS and below is 0, -10 dBFS and above is 1. */
export function meterLevel(value: number): number {
  if (value <= 0) return 0;
  const db = 20 * Math.log10(value);
  return Math.max(0, Math.min(1, (db + 60) / 50));
}

/**
 * A stream converter from `from` Hz to `to` Hz, PCM16 out in frames of 20 ms. State carries across `push` calls, so
 * the browser's 128-sample render quanta come out as gapless 320-sample (16 kHz) frames.
 */
export class Resampler {
  readonly from: number;
  readonly to: number;
  /** Samples per output frame (20 ms at `to`). */
  readonly frame: number;
  /** How many source samples the filter looks ahead: what a stream's end needs pushed after it to come out. */
  readonly latency: number;
  private readonly step: number;
  private readonly taps: Float32Array | null;
  private readonly half: number;
  /** Source samples not used up yet: the filter's history and what is still ahead of the read position. */
  private buf: Float32Array;
  /** Where the next output sample is read, in source samples after `half` of history at the start of `buf`. */
  private pos = 0;
  /** Output samples waiting to fill a frame. */
  private out: number[] = [];

  constructor(from: number, to: number) {
    if (!(from > 0) || !(to > 0)) throw new RangeError("sample rates must be positive");
    this.from = from;
    this.to = to;
    this.frame = frameSamples(to);
    this.step = from / to;
    // Only going down needs the filter. Its length grows with the ratio, so the transition band is the same share of
    // the target rate (about 14%) whatever the source; the cut-off sits that much under the target's Nyquist, so the
    // stop band starts at it (48k → 16k: flat to about 5.8 kHz, -74 dB from 8 kHz).
    this.taps = to < from ? lowPassTaps((CUTOFF * to) / from, Math.ceil((TAPS_PER_RATIO * from) / to) | 1) : null;
    this.half = this.taps ? (this.taps.length - 1) / 2 : 0;
    this.latency = this.taps ? this.taps.length + 2 : 2;
    // The filter's first outputs need `half` samples before them: silence, so the stream keeps its first milliseconds.
    this.buf = new Float32Array(this.half);
  }

  /** Feed source samples; returns the whole 20 ms frames that are ready (possibly none). */
  push(input: Float32Array): Int16Array[] {
    const merged = new Float32Array(this.buf.length + input.length);
    merged.set(this.buf);
    merged.set(input, this.buf.length);
    const half = this.half;
    const taps = this.taps;
    // The filtered value at source index i needs i-half … i+half: defined up to `last`.
    const last = merged.length - half - 1;
    const at = (i: number): number => {
      if (!taps) return merged[i];
      let acc = 0;
      const start = i - half;
      for (let k = 0; k < taps.length; k++) acc += taps[k] * merged[start + k];
      return acc;
    };
    let pos = this.pos + half;
    while (Math.floor(pos) + 1 <= last) {
      const i = Math.floor(pos);
      const frac = pos - i;
      const a = at(i);
      this.out.push(toPCM16(frac === 0 ? a : a + (at(i + 1) - a) * frac));
      pos += this.step;
    }
    // Keep what the next call needs: `half` samples of history before the next read position.
    const keepFrom = Math.min(merged.length, Math.floor(pos) - half);
    this.buf = merged.slice(keepFrom);
    this.pos = pos - keepFrom - half;
    const frames: Int16Array[] = [];
    while (this.out.length >= this.frame) frames.push(Int16Array.from(this.out.splice(0, this.frame)));
    return frames;
  }
}

/** The low-pass's cut-off as a share of the target rate, and its taps per unit of the rate ratio (see Resampler). */
const CUTOFF = 0.43;
const TAPS_PER_RATIO = 40;

/** PCM16 samples to floats (-1…1). */
export function fromPCM16(pcm: Int16Array): Float32Array<ArrayBuffer> {
  const out = new Float32Array(pcm.length);
  for (let i = 0; i < pcm.length; i++) out[i] = pcm[i] / 32768;
  return out;
}

/** 20 ms PCM16 frames recorded at `from` Hz, again at `to` Hz (audio held before `ready` named another rate): as many
 *  frames as came in. */
export function reframe(frames: readonly Int16Array[], from: number, to: number): Int16Array[] {
  if (from === to) return frames.slice();
  const r = new Resampler(from, to);
  const out = frames.flatMap((f) => r.push(fromPCM16(f)));
  // the stream's end: the filter's look-ahead and one more frame of silence, so the last frame comes out whole
  out.push(...r.push(new Float32Array(r.latency + frameSamples(from))));
  return out.slice(0, frames.length);
}

/** A frame's bytes as the socket sends them: PCM16, little-endian, whatever the machine's own order. */
export function pcmBytes(frame: Int16Array): ArrayBuffer {
  const out = new ArrayBuffer(frame.length * 2);
  const view = new DataView(out);
  for (let i = 0; i < frame.length; i++) view.setInt16(i * 2, frame[i], true);
  return out;
}

/** Bytes from the socket (PCM16 little-endian) as floats for an AudioBuffer. An odd trailing byte is dropped. */
export function pcmFloats(bytes: ArrayBuffer): Float32Array<ArrayBuffer> {
  const view = new DataView(bytes);
  const n = Math.floor(bytes.byteLength / 2);
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) out[i] = view.getInt16(i * 2, true) / 32768;
  return out;
}
