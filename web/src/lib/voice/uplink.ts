// Where the microphone's frames go (docs/assistant/voice.md 1.2 A.2, 6.3): straight to the socket once the extension said
// `ready`; before that (and while a dropped socket reconnects) the last 3 seconds are held and sent first when it is up,
// so words said right after tapping are not lost. Audio held at a rate `ready` did not ask for is resampled first.
import { FRAME_MS, pcmBytes, reframe } from "./resample";

/** 3 seconds of 20 ms frames. */
export const HELD_FRAMES = 3000 / FRAME_MS;

export class Uplink {
  private held: Int16Array[] = [];
  /** The rate the held frames were recorded at. */
  private heldRate = 0;
  private send: ((bytes: ArrayBuffer) => void) | null = null;
  private rate = 0;
  /** Frames sent since the call began, and their bytes (the e2e test reads them back from the mock). */
  sent = 0;
  bytes = 0;

  /** A frame the microphone made at `rate` Hz. */
  push(frame: Int16Array, rate: number): void {
    if (this.send) {
      // a frame the worklet made before it heard of the new rate
      for (const f of rate === this.rate ? [frame] : reframe([frame], rate, this.rate)) this.out(f);
      return;
    }
    if (this.held.length && rate !== this.heldRate) this.held = reframe(this.held, this.heldRate, rate);
    this.heldRate = rate;
    this.held.push(frame);
    if (this.held.length > HELD_FRAMES) this.held.splice(0, this.held.length - HELD_FRAMES);
  }

  /** The extension is ready for audio at `rate`: what was held goes first. */
  open(rate: number, send: (bytes: ArrayBuffer) => void): void {
    this.rate = rate;
    this.send = send;
    const held = this.held.length && this.heldRate !== rate ? reframe(this.held, this.heldRate, rate) : this.held;
    this.held = [];
    for (const f of held) this.out(f);
  }

  /** The socket went away: hold again until the next `ready`. */
  hold(): void {
    this.send = null;
  }

  /** How many frames are held now. */
  get waiting(): number {
    return this.held.length;
  }

  private out(frame: Int16Array): void {
    const bytes = pcmBytes(frame);
    this.sent++;
    this.bytes += bytes.byteLength;
    this.send!(bytes);
  }
}
