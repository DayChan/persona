import { describe, expect, it } from "vitest";
import type { FsEntry, FsListing } from "./files";
import { syncBadge, type GitStatus } from "./files";
import { ancestors, CACHE_FOLDERS, expandedKey, ListingCache, listingBytes, listingCache, readExpanded, toggleExpanded, treeRows, visibleDirs, withExpanded, writeExpanded, type Listed } from "./files-tree";

const file = (name: string, extra: Partial<FsEntry> = {}): FsEntry => ({ name, dir: false, size: 1, ...extra });
const dir = (name: string, extra: Partial<FsEntry> = {}): FsEntry => ({ name, dir: true, size: 0, ...extra });
const list = (path: string, entries: FsEntry[], truncated = false): FsListing => ({ path, entries, truncated });

describe("expanded folders", () => {
  const mem = () => {
    const m = new Map<string, string>();
    return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k), m };
  };

  it("are kept per session and come back", () => {
    const s = mem();
    const a = expandedKey("m1", "s1"),
      b = expandedKey("m1", "s2");
    expect(a).not.toBe(b);
    writeExpanded(s, a, ["/w/src", "/w/doc"]);
    expect(readExpanded(s, a)).toEqual(["/w/src", "/w/doc"]);
    expect(readExpanded(s, b)).toEqual([]);
  });

  it("forget everything once none is open, and survive junk", () => {
    const s = mem();
    const k = expandedKey("m1", "s1");
    writeExpanded(s, k, ["/w/src"]);
    writeExpanded(s, k, []);
    expect(s.m.size).toBe(0);
    s.setItem(k, "not json");
    expect(readExpanded(s, k)).toEqual([]);
    s.setItem(k, JSON.stringify(["/ok", 3, "", null]));
    expect(readExpanded(s, k)).toEqual(["/ok"]);
    s.setItem(k, JSON.stringify({ a: 1 }));
    expect(readExpanded(s, k)).toEqual([]);
  });

  it("closing a folder closes the ones inside it, not its neighbours", () => {
    expect(toggleExpanded(["/w/src"], "/w/doc")).toEqual(["/w/src", "/w/doc"]);
    expect(toggleExpanded(["/w/src", "/w/src/a", "/w/srcx", "/w/doc"], "/w/src")).toEqual(["/w/srcx", "/w/doc"]);
  });

  it("opens the way to a path and does not repeat", () => {
    expect(ancestors("/w", ["a", "b"])).toEqual(["/w/a", "/w/a/b"]);
    expect(withExpanded(["/w/a"], ["/w/a", "/w/a/b"])).toEqual(["/w/a", "/w/a/b"]);
    const same = ["/w/a"];
    expect(withExpanded(same, ["/w/a"])).toBe(same);
  });
});

describe("rows", () => {
  const listings: Record<string, Listed> = {
    "/w": { listing: list("/w", [dir("src"), dir("empty"), dir("gone"), file("a.go"), file(".env"), file("build.log", { ignored: true })]) },
    "/w/src": { listing: list("/w/src", [dir("deep"), file("main.go")], true) },
    "/w/empty": { listing: list("/w/empty", []) },
    "/w/gone": { error: "permission denied" },
  };
  const at = (d: string) => listings[d];

  it("puts an open folder's entries right under it, indented one level", () => {
    const rows = treeRows("/w", new Set(["/w/src"]), at, false);
    expect(rows.map((r) => `${r.depth}:${r.kind}:${r.path}`)).toEqual([
      "0:entry:/w/src",
      "1:entry:/w/src/deep",
      "1:entry:/w/src/main.go",
      "1:more:/w/src",
      "0:entry:/w/empty",
      "0:entry:/w/gone",
      "0:entry:/w/a.go",
      "0:entry:/w/build.log",
    ]);
    const src = rows[0];
    expect(src.kind === "entry" && src.expanded).toBe(true);
  });

  it("hides dotfiles unless asked", () => {
    expect(treeRows("/w", new Set(), at, true).some((r) => r.path === "/w/.env")).toBe(true);
    expect(treeRows("/w", new Set(), at, false).some((r) => r.path === "/w/.env")).toBe(false);
  });

  it("carries the ignored flag through to the entry", () => {
    const row = treeRows("/w", new Set(), at, false).find((r) => r.path === "/w/build.log");
    expect(row?.kind === "entry" && row.entry.ignored).toBe(true);
  });

  it("shows loading, empty and failed folders in place", () => {
    const rows = treeRows("/w", new Set(["/w/empty", "/w/gone", "/w/src/deep"]), (d) => at(d), false);
    const kinds = rows.map((r) => `${r.kind}:${r.path}`);
    expect(kinds).toContain("empty:/w/empty");
    expect(kinds).toContain("error:/w/gone");
    expect(kinds.some((k) => k.endsWith("/w/src/deep"))).toBe(false); // /w/src is closed, so what is in it is not reached
    const open = treeRows("/w", new Set(["/w/src", "/w/src/deep"]), at, false);
    expect(open.map((r) => `${r.kind}:${r.path}`)).toContain("loading:/w/src/deep");
    const err = rows.find((r) => r.kind === "error");
    expect(err?.kind === "error" && err.error).toBe("permission denied");
  });

  it("is a loading row while the root itself is not listed", () => {
    expect(treeRows("/w", new Set(), () => undefined, false).map((r) => r.kind)).toEqual(["loading"]);
  });

  it("a file is never expanded even if its path is in the set", () => {
    const rows = treeRows("/w", new Set(["/w/a.go"]), at, false);
    const a = rows.find((r) => r.path === "/w/a.go");
    expect(a?.kind === "entry" && a.expanded).toBe(false);
  });

  it("lists the folders to load: the root and the open ones that show", () => {
    expect(visibleDirs("/w", new Set(["/w/src", "/w/src/deep", "/w/nowhere"]), at)).toEqual(["/w", "/w/src", "/w/src/deep"]);
    expect(visibleDirs("/w", new Set(["/w/src/deep"]), at)).toEqual(["/w"]); // its parent is closed
  });
});

describe("listing cache", () => {
  const sized = (n: number) => list("/x", [file("n".repeat(n))]);

  it("keeps 12 folders and drops the least recently used first", () => {
    const c = new ListingCache();
    for (let i = 0; i < 12; i++) c.set(`/d${i}`, list(`/d${i}`, []));
    expect(c.size).toBe(12);
    c.get("/d0"); // d0 is the most recent now
    c.set("/d12", list("/d12", []));
    expect(c.size).toBe(12);
    expect(c.get("/d1")).toBeUndefined();
    expect(c.get("/d0")).toBeDefined();
    expect(c.dirs()[c.dirs().length - 1]).toBe("/d0");
  });

  it("stays under its byte budget", () => {
    const one = listingBytes(sized(100));
    const c = new ListingCache(12, one * 3 + 1);
    for (let i = 0; i < 5; i++) c.set(`/d${i}`, sized(100));
    expect(c.size).toBe(3);
    expect(c.bytes).toBeLessThanOrEqual(one * 3 + 1);
    expect(c.dirs()).toEqual(["/d2", "/d3", "/d4"]);
  });

  it("replacing a folder does not double count it, and one too big for the cache is not kept", () => {
    const c = new ListingCache(12, 1000);
    c.set("/a", sized(10));
    const once = c.bytes;
    c.set("/a", sized(10));
    expect(c.bytes).toBe(once);
    c.set("/big", sized(5000));
    expect(c.get("/big")).toBeUndefined();
    expect(c.size).toBe(1);
    c.delete("/a");
    expect(c.bytes).toBe(0);
  });

  it("keeps the caches of the most recently used session folders only", () => {
    // Keys unique to this test: the map of caches is module state.
    const first = listingCache("lru-test", "/w/0");
    first.set("/w/0", list("/w/0", []));
    const kept = listingCache("lru-test", "/w/1");
    for (let i = 2; i <= CACHE_FOLDERS; i++) {
      listingCache("lru-test", `/w/${i}`);
      if (i === 4) listingCache("lru-test", "/w/1"); // used again: no longer the oldest
    }
    const again = listingCache("lru-test", "/w/0");
    expect(again).not.toBe(first);
    expect(again.get("/w/0")).toBeUndefined(); // listed from the hub again, as on a first open
    expect(listingCache("lru-test", "/w/1")).toBe(kept);
    expect(listingCache("lru-test", "/w/0")).toBe(again);
  });
});

describe("Git badge", () => {
  const st = (over: Partial<GitStatus>): GitStatus => ({ is_repo: true, files: [], commits: [], ...over });
  it("shows commits ahead, behind and a dirty tree", () => {
    expect(syncBadge(st({ ahead: 2, behind: 1, files: [{ path: "a", index: ".", worktree: "M", added: 0, deleted: 0 }] }))).toBe("↑2 ↓1 *");
    expect(syncBadge(st({ ahead: 3 }))).toBe("↑3");
    expect(syncBadge(st({ behind: 4 }))).toBe("↓4");
    expect(syncBadge(st({}))).toBe("");
    expect(syncBadge(st({ is_repo: false, ahead: 1 }))).toBe("");
    expect(syncBadge(null)).toBe("");
  });
});
