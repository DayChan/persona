// Structural sharing for a Feishu chat (views/inbox/LarkThread.tsx): every `lark` push naming the open chat reads its
// newest page again as fresh JSON, and every read-states reply is new objects, so each push handed every memoized row
// a new message and every row rendered again. A message or receipt that reads the same as the one we have keeps our
// object; only the ones that really changed reach React as new props.

import type { LarkMessage } from "./inbox";
import type { ReadState } from "./lark-chat";
import { sameValue } from "./stable-lines";

/** `next`, with each message that reads the same as `prev`'s of that id replaced by `prev`'s object; `prev` itself
 *  when nothing changed at all (same messages in the same order). */
export function keepSameMessages(prev: LarkMessage[], next: LarkMessage[]): LarkMessage[] {
  if (prev === next) return prev;
  const before = new Map(prev.map((m) => [m.id, m]));
  let all = prev.length === next.length;
  const out = next.map((m, i) => {
    const old = before.get(m.id);
    const kept = old && sameValue(old, m) ? old : m;
    if (kept !== prev[i]) all = false;
    return kept;
  });
  return all ? prev : out;
}

/** A copy of `next` (ReadReceipts keeps filling its own map), with each receipt that reads the same as `prev`'s of
 *  that message keeping `prev`'s object; `prev` itself when no receipt came, went or changed. */
export function keepSameStates(prev: Map<string, ReadState>, next: ReadonlyMap<string, ReadState>): Map<string, ReadState> {
  let all = prev.size === next.size;
  const out = new Map<string, ReadState>();
  for (const [id, s] of next) {
    const old = prev.get(id);
    const kept = old && sameValue(old, s) ? old : s;
    if (kept !== old) all = false;
    out.set(id, kept);
  }
  return all ? prev : out;
}
