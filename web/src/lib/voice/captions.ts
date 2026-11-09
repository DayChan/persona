// The call's captions (docs/assistant/voice.md 1.2 C, 3.3 `transcript`): temporary rows, in this tab only, merged by id per
// connection and kept in the order they were said. Pure, as iOS's VoiceConversation.caption.
import { HISTORY_LINES, type VoiceLine, type VoiceTranscript } from "./wire";

/** One line the call heard or said. Ids carry the connection's generation: a new connection's `u1` is a new line. */
export interface VoiceCaption {
  id: string;
  generation: number;
  role: "user" | "assistant";
  text: string;
  final: boolean;
}

/** How many captions the call keeps (and hands back on a reconnect). */
export const CAPTION_LIMIT = HISTORY_LINES;
/** How many the thread shows at its end. */
export const CAPTIONS_SHOWN = 6;

const keyOf = (generation: number, id: string) => `${generation}:${id}`;

/**
 * The captions after one `transcript` frame of connection `generation`. A known id is updated in place; a new one is
 * added when it has words: before the row its `before` names when this connection's row is still here (a caption that
 * came after the voice began to answer it), else at the end. Unchanged input returns the same array.
 */
export function mergeCaption(captions: readonly VoiceCaption[], t: VoiceTranscript, generation: number, limit = CAPTION_LIMIT): readonly VoiceCaption[] {
  const id = keyOf(generation, t.id);
  const at = findLast(captions, (c) => c.id === id);
  if (at >= 0) {
    const c = captions[at];
    if (c.text === t.text && c.final === t.final) return captions;
    const next = captions.slice();
    next[at] = { ...c, text: t.text, final: t.final };
    return next;
  }
  if (!t.text) return captions;
  const caption: VoiceCaption = { id, generation, role: t.role === "user" ? "user" : "assistant", text: t.text, final: t.final };
  const next = captions.slice();
  const before = t.before ? findLast(captions, (c) => c.id === keyOf(generation, t.before!)) : -1;
  if (before >= 0) next.splice(before, 0, caption);
  else next.push(caption);
  return next.length > limit ? next.slice(next.length - limit) : next;
}

/** The captions a reconnect hands back as `history`: the last 40 with words. */
export function historyOf(captions: readonly VoiceCaption[]): VoiceLine[] {
  return captions.filter((c) => c.text).slice(-HISTORY_LINES).map((c) => ({ role: c.role, text: c.text }));
}

function findLast<T>(list: readonly T[], match: (x: T) => boolean): number {
  for (let i = list.length - 1; i >= 0; i--) if (match(list[i])) return i;
  return -1;
}
