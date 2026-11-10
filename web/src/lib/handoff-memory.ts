/** Where Hand to AI last sent an item from each source (#2121; docs/assistant/inbox.md "Item actions";
 *  spec/fixtures/inbox/handoff_memory.json, which the apps read too): the repository, else the chat, else the sender
 *  (the item's `mutes`). The next item from that source starts on that target, marked Last time; nothing is sent until
 *  the user sends. Kept in this browser for this hub (localStorage is per origin): ids only, the 200 most recent sources,
 *  never synced, forgotten on sign-out. */
import type { InboxMuteOption } from "./inbox";

export const HANDOFF_MEMORY_KEY = "inbox.handOffBySource";
export const HANDOFF_MEMORY_LIMIT = 200;
/** Most specific first: an item from a repository goes by the repository, not by who wrote it. */
const TAGS = ["repo", "chat", "sender"];

/** "main" | "new" | "agent:<machine>/<session>". */
export type StoredTarget = string;

interface Entry {
  source: string;
  target: StoredTarget;
}

/** What to preselect: the target, whether it is the remembered one (Last time), and whether a remembered Agent was gone
 *  so the main chat stands in (said once). */
export interface ResolvedHandOff {
  target: StoredTarget;
  lastTime: boolean;
  gone: boolean;
}

/** The item's source key ("github|repo:example/weather"); null when it has none of the three (a Lark doc, …). */
export function handOffSourceKey(source: string, mutes?: InboxMuteOption[] | null): string | null {
  for (const tag of TAGS) {
    const m = mutes?.find((o) => o.tag === tag && o.value);
    if (m) return `${source}|${tag}:${m.value}`;
  }
  return null;
}

export function agentTarget(machineId: string, sessionId: string): StoredTarget {
  return `agent:${machineId}/${sessionId}`;
}

/** The remembered target while it is still there. A remembered Agent no longer listed resolves to the main chat
 *  (`gone`); nothing remembered, or a value that doesn't read, is null: the dialog's own default. */
export function resolveHandOff(stored: string | null | undefined, isListed: (path: string) => boolean): ResolvedHandOff | null {
  if (stored === "main" || stored === "new") return { target: stored, lastTime: true, gone: false };
  if (!stored?.startsWith("agent:")) return null;
  const path = stored.slice("agent:".length);
  const slash = path.indexOf("/");
  if (slash <= 0 || slash === path.length - 1) return null;
  if (!isListed(path)) return { target: "main", lastTime: false, gone: true };
  return { target: stored, lastTime: true, gone: false };
}

function entries(store: Storage): Entry[] {
  try {
    const list = JSON.parse(store.getItem(HANDOFF_MEMORY_KEY) ?? "[]");
    return Array.isArray(list) ? list.filter((e): e is Entry => typeof e?.source === "string" && typeof e?.target === "string") : [];
  } catch {
    return [];
  }
}

/** Latest first, one per source. */
export function handOffEntries(store: Storage = localStorage): Entry[] {
  return entries(store);
}

export function recallHandOff(key: string, store: Storage = localStorage): StoredTarget | null {
  return entries(store).find((e) => e.source === key)?.target ?? null;
}

export function rememberHandOff(key: string, target: StoredTarget, store: Storage = localStorage) {
  const list = [{ source: key, target }, ...entries(store).filter((e) => e.source !== key)].slice(0, HANDOFF_MEMORY_LIMIT);
  try {
    store.setItem(HANDOFF_MEMORY_KEY, JSON.stringify(list));
  } catch {
    // a full or blocked storage only loses the memory
  }
}

export function forgetHandOffs(store: Storage = localStorage) {
  store.removeItem(HANDOFF_MEMORY_KEY);
}
