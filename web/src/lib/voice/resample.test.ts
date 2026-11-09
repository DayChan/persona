import { describe, expect, it } from "vitest";
import { frameSamples, fromPCM16, lowPassTaps, meterLevel, pcmBytes, pcmFloats, reframe, Resampler, rms, toPCM16 } from "./resample";

/** `seconds` of a sine at `hz`, sampled at `rate`. */
function tone(hz: number, rate: number, seconds: number, amplitude = 0.5): Float32Array {
  const out = new Float32Array(Math.round(rate * seconds));
  for (let i = 0; i < out.length; i++) out[i] = amplitude * Math.sin((2 * Math.PI * hz * i) / rate);
  return out;
}

/** Push in the browser's render quanta of 128 samples, as the worklet gets them. */
function run(r: Resampler, input: Float32Array): Int16Array[] {
  const frames: Int16Array[] = [];
  for (let i = 0; i < input.length; i += 128) frames.push(...r.push(input.subarray(i, i + 128)));
  return frames;
}

function joined(frames: Int16Array[]): Float32Array {
  const all = new Int16Array(frames.reduce((n, f) => n + f.length, 0));
  let at = 0;
  for (const f of frames) {
    all.set(f, at);
    at += f.length;
  }
  return fromPCM16(all);
}

/** The amplitude of `hz` in `x` (sampled at `rate`), by correlating with a sine and a cosine. */
function amplitudeAt(x: Float32Array, hz: number, rate: number): number {
  let s = 0, c = 0;
  for (let i = 0; i < x.length; i++) {
    s += x[i] * Math.sin((2 * Math.PI * hz * i) / rate);
    c += x[i] * Math.cos((2 * Math.PI * hz * i) / rate);
  }
  return (2 * Math.hypot(s, c)) / x.length;
}

function rmsOf(x: Float32Array): number {
  return Math.sqrt(x.reduce((a, v) => a + v * v, 0) / x.length);
}

/** Zero crossings per second, over two: the frequency of a clean tone. */
function crossingsHz(x: Float32Array, rate: number): number {
  let n = 0;
  for (let i = 1; i < x.length; i++) if (x[i - 1] < 0 !== x[i] < 0) n++;
  return (n / 2) * (rate / x.length);
}

describe("the microphone's resampler", () => {
  it("turns 48 kHz into 20 ms frames of 16 kHz: a 1 kHz tone keeps its frequency and level", () => {
    const frames = run(new Resampler(48000, 16000), tone(1000, 48000, 1));
    expect(frames.every((f) => f.length === 320)).toBe(true);
    expect(frames.length).toBeGreaterThanOrEqual(48);
    expect(frames.length).toBeLessThanOrEqual(50);
    // past the filter's start-up
    const out = joined(frames).subarray(320);
    expect(crossingsHz(out, 16000)).toBeCloseTo(1000, -1);
    expect(amplitudeAt(out, 1000, 16000)).toBeCloseTo(0.5, 2);
    expect(rmsOf(out)).toBeCloseTo(0.5 / Math.SQRT2, 2);
  });

  it("does the same from 44.1 kHz (a fractional ratio)", () => {
    const frames = run(new Resampler(44100, 16000), tone(1000, 44100, 1));
    expect(frames.every((f) => f.length === 320)).toBe(true);
    expect(frames.length).toBeGreaterThanOrEqual(48);
    const out = joined(frames).subarray(320);
    expect(crossingsHz(out, 16000)).toBeCloseTo(1000, -1);
    expect(amplitudeAt(out, 1000, 16000)).toBeCloseTo(0.5, 2);
  });

  it("filters what would fold back: a 12 kHz tone at 48 kHz does not come out as 4 kHz", () => {
    const out = joined(run(new Resampler(48000, 16000), tone(12000, 48000, 1))).subarray(320);
    // dropping two samples of every three would leave it at full amplitude as a 4 kHz tone
    expect(amplitudeAt(out, 4000, 16000)).toBeLessThan(0.005);
    expect(rmsOf(out)).toBeLessThan(0.005);
  });

  it("keeps speech-band highs: a 5 kHz tone loses under 1 dB", () => {
    const out = joined(run(new Resampler(48000, 16000), tone(5000, 48000, 1))).subarray(320);
    expect(amplitudeAt(out, 5000, 16000)).toBeGreaterThan(0.5 * 0.89);
  });

  it("makes 24 kHz frames of 480 samples, and goes up as well as down", () => {
    const up = run(new Resampler(16000, 24000), tone(1000, 16000, 0.5));
    expect(up.every((f) => f.length === 480)).toBe(true);
    expect(amplitudeAt(joined(up).subarray(480), 1000, 24000)).toBeCloseTo(0.5, 1);
    expect(frameSamples(16000)).toBe(320);
    expect(frameSamples(24000)).toBe(480);
  });

  it("is gapless across pushes of any size", () => {
    const input = tone(440, 48000, 0.5);
    const whole = joined(new Resampler(48000, 16000).push(input));
    const r = new Resampler(48000, 16000);
    const parts: Int16Array[] = [];
    for (let i = 0; i < input.length; ) {
      const n = 1 + ((i * 7919) % 300);
      parts.push(...r.push(input.subarray(i, i + n)));
      i += n;
    }
    const pieces = joined(parts);
    expect(pieces.length).toBe(whole.length);
    expect(Math.max(...pieces.map((v, i) => Math.abs(v - whole[i])))).toBeLessThan(1e-4);
  });

  it("has a low-pass with unit gain at 0 Hz", () => {
    const taps = lowPassTaps(0.15, 121);
    expect(taps.reduce((a, b) => a + b, 0)).toBeCloseTo(1, 6);
  });
});

describe("PCM helpers", () => {
  it("clips and rounds to PCM16", () => {
    expect([toPCM16(2), toPCM16(-2), toPCM16(0), toPCM16(0.5)]).toEqual([32767, -32768, 0, 16384]);
  });

  it("writes frames little-endian and reads them back", () => {
    const bytes = pcmBytes(Int16Array.from([1, -2, 0x1234]));
    expect([...new Uint8Array(bytes)]).toEqual([1, 0, 0xfe, 0xff, 0x34, 0x12]);
    expect([...pcmFloats(bytes)].map((v) => Math.round(v * 32768))).toEqual([1, -2, 0x1234]);
  });

  it("drops an odd trailing byte", () => {
    expect(pcmFloats(new Uint8Array([0, 1, 2]).buffer)).toHaveLength(1);
  });

  it("measures level for the orb", () => {
    expect(rms(new Int16Array(320))).toBe(0);
    expect(meterLevel(0)).toBe(0);
    expect(meterLevel(1)).toBe(1);
    expect(meterLevel(0.001)).toBe(0);
    expect(meterLevel(0.05)).toBeGreaterThan(0.5);
  });

  it("reframes held audio to another rate, as many frames as came in", () => {
    const held = run(new Resampler(48000, 24000), tone(1000, 48000, 0.5));
    const again = reframe(held, 24000, 16000);
    expect(again).toHaveLength(held.length);
    expect(again.every((f) => f.length === 320)).toBe(true);
    expect(amplitudeAt(joined(again).subarray(640), 1000, 16000)).toBeCloseTo(0.5, 1);
    expect(reframe(held, 24000, 24000)).toEqual(held);
  });
});
