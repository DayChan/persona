// The call's cues (docs/assistant/voice.md 1.2 A / I / M, 6.6), the same notes as iOS's Theme.VoiceCue: connected, a card
// that wants a tap, the voice's connection dropping. Played through the call's own speaker (audio.ts), so the
// browser's echo cancellation hears them as the page's own output.

export type VoiceCue = "ready" | "attention" | "reconnecting";

/** The notes, in hertz, one after the other. */
export const CUE_NOTES: Record<VoiceCue, number[]> = {
  ready: [659.25, 880],
  attention: [880, 1174.66, 880],
  reconnecting: [440],
};

/** How long each note sounds, the quiet between two, the ramp at each end (no click), and the loudness (a share of
 *  full scale: under the voice's own level). */
export const CUE_NOTE_S = 0.085;
export const CUE_GAP_S = 0.035;
export const CUE_RAMP_S = 0.012;
export const CUE_GAIN = 0.16;

/** The cue's samples (-1…1) at `rate`. */
export function cueSamples(cue: VoiceCue, rate: number): Float32Array<ArrayBuffer> {
  if (!(rate > 0)) return new Float32Array(0);
  const note = Math.floor(CUE_NOTE_S * rate);
  const gap = Math.floor(CUE_GAP_S * rate);
  const ramp = Math.max(1, Math.floor(CUE_RAMP_S * rate));
  const notes = CUE_NOTES[cue];
  const out = new Float32Array(notes.length * note + (notes.length - 1) * gap);
  let at = 0;
  notes.forEach((hz, n) => {
    if (n > 0) at += gap;
    for (let i = 0; i < note; i++) {
      const edge = Math.min(i, note - 1 - i);
      const envelope = edge < ramp ? edge / ramp : 1;
      out[at + i] = CUE_GAIN * envelope * Math.sin((2 * Math.PI * hz * i) / rate);
    }
    at += note;
  });
  return out;
}
