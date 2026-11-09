// What the knowledge page keeps between visits: entries it has seen (a detail shows its row at once while it
// loads), the lists by kind and tag, and the results of the most recent searches by query (coming back to a search
// shows it again).
import type { KBEntry, KBList, KBResult } from "../../lib/kb";

const entries = new Map<string, KBEntry>();

export function remember(list: (KBEntry | undefined | null)[] | null | undefined) {
  for (const e of list ?? []) if (e) entries.set(`${e.kind}/${e.id}`, e);
}

export function known(kind: string, id: string): KBEntry | undefined {
  return entries.get(`${kind}/${id}`);
}

export const lists = new Map<string, KBList>();
export const listKey = (kind: string, tag?: string) => `${kind}|${tag ?? ""}`;

/** Queries whose results are kept. Every query typed and paused on adds one while the page stays open, so keep only
 * the latest: an older query typed again shows the searching state until the hub answers, as on a first visit. */
export const SEARCHES_KEPT = 30;

/** A map that keeps only its `limit` most recently read or written keys. */
class Recent<V> {
  private readonly map = new Map<string, V>();

  constructor(private readonly limit: number) {}

  get size() {
    return this.map.size;
  }

  get(key: string): V | undefined {
    const v = this.map.get(key);
    if (v !== undefined) this.set(key, v);
    return v;
  }

  set(key: string, value: V) {
    this.map.delete(key);
    this.map.set(key, value);
    if (this.map.size > this.limit) this.map.delete(this.map.keys().next().value!);
    return this;
  }

  clear() {
    this.map.clear();
  }
}

export const searches = new Recent<KBResult[]>(SEARCHES_KEPT);
