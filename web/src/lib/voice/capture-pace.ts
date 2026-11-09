// Whether the microphone's frames keep up with real time. The capture worklet posts one 20 ms frame for every 20 ms its
// AudioContext renders, so a call sends 50 frames a second, muted or not, and the extension's clock depends on it. When
// the context's clock runs slow (Chrome moved it to its timer-driven fake sink: docs/lessons/voice.md, "The capture
// context must never look silent") fewer frames come and the extension hears only part of what was said. audio.ts counts
// the frames and checks every few seconds; this is the arithmetic, pure so the tests can hand it the times.
import { FRAME_MS } from "./resample";

/** A window in which fewer frames came than the wall clock says: how many came, how many should have, over how many
 *  seconds of wall clock, and how far the capture context's own clock moved in them. */
export interface CaptureLag {
  frames: number;
  expected: number;
  seconds: number;
  contextSeconds: number;
}

/** The shortest window judged: a check sooner after the last one waits for the next. */
export const MIN_WINDOW_MS = 2000;

export class CapturePace {
  private frames = 0;
  /** Where the window began: the wall clock (ms), the capture context's clock (s) and the frames counted by then. */
  private since: { wall: number; context: number; frames: number } | null = null;

  /** `share`: the part of the frames the wall clock expects that a window has to bring not to count as behind. Frames
   *  reach the page in bursts when its main thread is busy, so a window may come a few short without anything wrong. */
  constructor(private readonly share = 0.9) {}

  /** One frame reached the page at `wall` ms (performance.now) when the capture context's clock read `context` seconds.
   *  The first one starts the count: before it the microphone was still being opened. */
  frame(wall: number, context: number): void {
    if (!this.since) this.since = { wall, context, frames: 0 };
    else this.frames++;
  }

  /** The window since the last check (or the first frame), and a new one from now: a lag when it fell behind. */
  check(wall: number, context: number): CaptureLag | null {
    const since = this.since;
    if (!since || wall - since.wall < MIN_WINDOW_MS) return null;
    this.since = { wall, context, frames: this.frames };
    const frames = this.frames - since.frames;
    const seconds = (wall - since.wall) / 1000;
    const expected = Math.floor((wall - since.wall) / FRAME_MS);
    return frames < expected * this.share ? { frames, expected, seconds, contextSeconds: context - since.context } : null;
  }
}
