// Finding a message sent from here in the transcript. Claude Code keeps its own id for the user's message, so the hub
// ties the transcript's copy back to the client_msg_id it was sent with (Item.client_msg_id, on every item that came
// from it: the text, its images, a queued copy, or the notice it became). A tagged item settles the message with that
// id and no other; an untagged one (an older hub, or one the node could not tie) is found by its words: the same rules
// as iOS (PendingMessage.arrived / same) and Android (MessageEcho).

import type { Item } from "./types";

/** The computer's clock may be behind this one by this much. */
export const CLOCK_SLACK_MS = 120_000;

/** The words the hub adds where it cut a long text for the page (an ellipsis, then "truncated, N bytes in all" in Chinese, in full-width parentheses). */
// i18n-ignore: matches the words the hub writes into the text it cut
const CUT_MARK = "\n…（已截断";

/** Comparable form: the phone's image note, pasted wrappers and Claude's "[Image #21]" placeholders removed, no whitespace. */
export function normalizeWords(s: string): string {
  return s
    .replace(/<attached_images>[\s\S]*?<\/attached_images>/g, "")
    .replace(/<[\\/]*pasted_content[^>]*>/g, "")
    .replace(/\[Image #\d+\]/g, "")
    .replace(/\s+/g, "");
}

/** The transcript's copy of a message: equal once normalized, or one containing the other when both are long enough
 *  (a pending "OK" must not be taken for any message with "OK" in it). */
export function sameWords(a: string, b: string): boolean {
  const x = normalizeWords(a),
    y = normalizeWords(b);
  if (!x || !y) return false;
  return x === y || (Math.min([...x].length, [...y].length) >= 8 && (x.includes(y) || y.includes(x)));
}

export interface Sent {
  /** The client_msg_id it was sent with; none when it went in another way (the prompt that started a conversation),
   *  then only words can find it. */
  id?: string;
  /** The words as the transcript shows them (contexts already taken out, parseUserText). */
  text: string;
  images: unknown[];
  /** When it was written here (ms). */
  at: number;
}

export interface Seen {
  id?: string;
  /** The message it came from, as the hub tied it (Item.client_msg_id). */
  client_msg_id?: string;
  source?: Item["source"];
  injected?: Item["injected"];
  from?: string;
  text: string;
  images: unknown[];
  /** Photos it carries as `<attached_images>` paths (the node saved what was sent and wrote where): they count as its
   *  images, a message sent here with pictures comes back with paths, not image items. */
  photos?: string[];
  ts?: string;
}

/** `user` is the copy of `sent`. Tied to a message by the hub: that message's copy and no other's. Otherwise written
 *  after it (with slack for the computer's clock), with at least its images, and its words — when the page cut a long
 *  one, its start. An older message with the same words (a second "Continue") is not it. */
export function isEcho(sent: Sent, user: Seen): boolean {
  if (user.client_msg_id && sent.id) return user.client_msg_id === sent.id;
  if (user.source || user.injected || user.from) return false;
  const at = user.ts ? Date.parse(user.ts) : NaN;
  if (!Number.isNaN(at) && at < sent.at - CLOCK_SLACK_MS) return false;
  if (user.images.length + (user.photos?.length ?? 0) < sent.images.length) return false;
  const cut = user.text.lastIndexOf(CUT_MARK);
  if (cut >= 0) {
    const start = normalizeWords(user.text.slice(0, cut));
    return [...start].length >= 8 && normalizeWords(sent.text).startsWith(start);
  }
  // only pictures: the transcript's copy has no words either
  if (!normalizeWords(sent.text)) return sent.images.length > 0 && !normalizeWords(user.text);
  return sameWords(user.text, sent.text);
}

/** Which of `sent` the transcript's `users` hold: each user message answers one, and one already taken (`claimed`, by
 *  id, kept across calls) answers none, so the same words sent twice need two copies. The hub's ties go first; then the
 *  rest by words, each the oldest it can: a copy tied to a message here answers no other, and one tied to a message
 *  sent elsewhere only one that went in without an id. */
export function echoed<T>(sent: T[], users: Seen[], describe: (t: T) => Sent, claimed: Set<string>): Set<T> {
  const left = [...sent].sort((a, b) => describe(a).at - describe(b).at);
  const ids = new Set(left.flatMap((t) => describe(t).id ?? []));
  const found = new Set<T>();
  const take = (user: Seen, i: number) => {
    if (i < 0) return;
    found.add(left[i]);
    left.splice(i, 1);
    if (user.id) claimed.add(user.id);
  };
  const open = users.filter((u) => !u.id || !claimed.has(u.id));
  for (const user of open) {
    if (user.client_msg_id && ids.has(user.client_msg_id)) take(user, left.findIndex((t) => describe(t).id === user.client_msg_id));
  }
  for (const user of open) {
    if (user.client_msg_id && ids.has(user.client_msg_id)) continue;
    if (user.id && claimed.has(user.id)) continue;
    take(user, left.findIndex((t) => isEcho(describe(t), user)));
  }
  return found;
}

/** The items the hub tied to a sent message, one per message (any role or kind: the user row, its images, a queued
 *  copy, the notice a /compact became): what a caller passes to `echoed` / `isEcho` besides the user messages. */
export function taggedSeen(items: Item[]): Seen[] {
  const seen = new Map<string, Seen>();
  for (const it of items) {
    if (!it.client_msg_id || seen.has(it.client_msg_id)) continue;
    seen.set(it.client_msg_id, { id: it.id, client_msg_id: it.client_msg_id, text: it.text ?? "", images: it.kind === "image" ? [it] : [], ts: it.ts });
  }
  return [...seen.values()];
}
