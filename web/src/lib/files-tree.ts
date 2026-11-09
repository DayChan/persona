// The Files panel's tree (#2439, Moshi's form): folders open in place under their parent, what was listed is kept in
// a small cache so closing and reopening the panel (or a folder) shows it at once, and the open folders are kept per
// session. No DOM here: tested in files-tree.test.ts.
import { joinPath, type FsEntry, type FsListing } from "./files";

// ── cache ────────────────────────────────────────────────────────────────

/** How many folders and how many bytes of listings are kept per machine and session folder. */
export const CACHE_DIRS = 12;
export const CACHE_BYTES = 8 * 1024 * 1024;
/** How many session folders keep a cache; the least recently used one is let go (at most 8 x CACHE_BYTES). */
export const CACHE_FOLDERS = 8;

/** The bytes a listing takes in the cache (its JSON, which is what came over the wire). */
export function listingBytes(l: FsListing): number {
  return JSON.stringify(l).length * 2;
}

/** Listings by folder, least recently used out first once there are more than `maxDirs` or `maxBytes`. */
export class ListingCache {
  private items = new Map<string, { listing: FsListing; bytes: number }>(); // insertion order: oldest first
  private total = 0;

  constructor(
    readonly maxDirs = CACHE_DIRS,
    readonly maxBytes = CACHE_BYTES,
  ) {}

  get size() {
    return this.items.size;
  }
  get bytes() {
    return this.total;
  }
  dirs(): string[] {
    return [...this.items.keys()];
  }

  /** The cached listing, counted as used. */
  get(dir: string): FsListing | undefined {
    const it = this.items.get(dir);
    if (!it) return undefined;
    this.items.delete(dir);
    this.items.set(dir, it);
    return it.listing;
  }

  set(dir: string, listing: FsListing) {
    const old = this.items.get(dir);
    if (old) {
      this.total -= old.bytes;
      this.items.delete(dir);
    }
    const bytes = listingBytes(listing);
    if (bytes > this.maxBytes) return; // one listing larger than the whole cache is not kept
    this.items.set(dir, { listing, bytes });
    this.total += bytes;
    while (this.items.size > this.maxDirs || this.total > this.maxBytes) {
      const [oldest] = this.items.keys();
      this.total -= this.items.get(oldest)!.bytes;
      this.items.delete(oldest);
    }
  }

  delete(dir: string) {
    const it = this.items.get(dir);
    if (it) this.total -= it.bytes;
    this.items.delete(dir);
  }

  clear() {
    this.items.clear();
    this.total = 0;
  }
}

const caches = new Map<string, ListingCache>(); // insertion order: least recently used first

/**
 * The cache of one machine's session folder (listings of other folders or machines never mix). Only the
 * CACHE_FOLDERS most recently used are kept; a tree that is open keeps the instance it got, and a folder whose
 * cache was let go is listed from the hub again, as on its first open.
 */
export function listingCache(mid: string, cwd: string): ListingCache {
  const key = JSON.stringify([mid, cwd]);
  let c = caches.get(key);
  if (c) caches.delete(key);
  else c = new ListingCache();
  caches.set(key, c);
  while (caches.size > CACHE_FOLDERS) caches.delete(caches.keys().next().value!);
  return c;
}

// ── expanded folders, kept per session ──────────────────────────────────

export const expandedKey = (mid: string, sid: string) => `persona.files.expanded.${mid}.${sid}`;

type Store = Pick<Storage, "getItem" | "setItem" | "removeItem">;

export function readExpanded(store: Store, key: string): string[] {
  try {
    const v = JSON.parse(store.getItem(key) ?? "[]");
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string" && x !== "") : [];
  } catch {
    return [];
  }
}

export function writeExpanded(store: Store, key: string, dirs: string[]) {
  try {
    if (dirs.length) store.setItem(key, JSON.stringify(dirs));
    else store.removeItem(key);
  } catch {
    /* private mode / full: the folders just are not remembered */
  }
}

/** The folder opened (appended) or closed; closing a folder also closes the ones inside it. */
export function toggleExpanded(open: string[], dir: string): string[] {
  if (!open.includes(dir)) return [...open, dir];
  return open.filter((d) => d !== dir && !d.startsWith(dir.replace(/\/+$/, "") + "/"));
}

/** The folders on the way to `path` under `root` (outermost first), opened so the path shows. */
export function ancestors(root: string, stack: string[]): string[] {
  const out: string[] = [];
  let dir = root;
  for (const name of stack) {
    dir = joinPath(dir, name);
    out.push(dir);
  }
  return out;
}

/** `open` plus `more`, without repeats, order kept. */
export function withExpanded(open: string[], more: string[]): string[] {
  const add = more.filter((d) => !open.includes(d));
  return add.length ? [...open, ...add] : open;
}

// ── rows ─────────────────────────────────────────────────────────────────

export interface Listed {
  listing?: FsListing;
  error?: string;
}

export type TreeRow =
  | { kind: "entry"; key: string; path: string; depth: number; entry: FsEntry; expanded: boolean }
  | { kind: "loading"; key: string; path: string; depth: number }
  | { kind: "empty"; key: string; path: string; depth: number }
  | { kind: "error"; key: string; path: string; depth: number; error: string }
  | { kind: "more"; key: string; path: string; depth: number };

/**
 * The visible rows, depth first: the root's entries, each open folder's entries right under it. A folder that is open
 * but not listed yet has a loading row, one that failed an error row, an empty one an empty row.
 */
export function treeRows(root: string, open: ReadonlySet<string>, listed: (dir: string) => Listed | undefined, showHidden: boolean): TreeRow[] {
  const rows: TreeRow[] = [];
  const walk = (dir: string, depth: number) => {
    const l = listed(dir);
    if (!l?.listing) {
      rows.push(l?.error ? { kind: "error", key: `${dir}\0error`, path: dir, depth, error: l.error } : { kind: "loading", key: `${dir}\0loading`, path: dir, depth });
      return;
    }
    const entries = l.listing.entries.filter((e) => showHidden || !e.name.startsWith("."));
    if (!entries.length) rows.push({ kind: "empty", key: `${dir}\0empty`, path: dir, depth });
    for (const e of entries) {
      const path = joinPath(dir, e.name);
      const expanded = e.dir && open.has(path);
      rows.push({ kind: "entry", key: path, path, depth, entry: e, expanded });
      if (expanded) walk(path, depth + 1);
    }
    if (l.listing.truncated) rows.push({ kind: "more", key: `${dir}\0more`, path: dir, depth });
  };
  walk(root, 0);
  return rows;
}

/** The folders to list: the root and every open folder that shows (its parents are open too). */
export function visibleDirs(root: string, open: ReadonlySet<string>, listed: (dir: string) => Listed | undefined): string[] {
  const out = [root];
  const walk = (dir: string) => {
    for (const e of listed(dir)?.listing?.entries ?? []) {
      const path = joinPath(dir, e.name);
      if (e.dir && open.has(path)) {
        out.push(path);
        walk(path);
      }
    }
  };
  walk(root);
  return out;
}
