import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { FeedCache } from "./feed-cache";
import type { Item } from "./types";

let cache: typeof import("./feed-cache");
let stored: Map<string, string>;
const token = "jd_example_browser_credential";
const item = (seq: number, role: Item["role"] = "assistant", patch: Partial<Item> = {}): Item => ({ id: `item-${seq}`, seq, role, kind: "text", text: `Message ${seq}`, ...patch });
const page = (patch: Partial<FeedCache> = {}): FeedCache => ({ machineID: "machine-demo", sessionID: "main-demo", items: [item(1, "user"), item(2)], hasMore: false, ...patch });
const entries = () => [...stored].filter(([key]) => key.startsWith("persona.feedCache."));

beforeEach(async () => {
  vi.resetModules();
  stored = new Map([["persona.token", token], ["persona.tokenId", "device-demo"]]);
  vi.stubGlobal("localStorage", {
    get length() { return stored.size; },
    key: (index: number) => [...stored.keys()][index] ?? null,
    getItem: (key: string) => stored.get(key) ?? null,
    setItem: (key: string, value: string) => stored.set(key, value),
    removeItem: (key: string) => stored.delete(key),
  });
  vi.stubGlobal("location", { origin: "https://hub.example.com" });
  cache = await import("./feed-cache");
});
afterEach(() => vi.unstubAllGlobals());

describe("main conversation feed cache", () => {
  it("partitions keys by hub, reverse-proxy prefix, device id and credential without exposing them", () => {
    const key = cache.feedCacheKey("https://hub.example.com", token, "device-demo")!;
    expect(new Set([
      key,
      cache.feedCacheKey("https://other.example.com", token, "device-demo"),
      cache.feedCacheKey("https://hub.example.com/other", token, "device-demo"),
      cache.feedCacheKey("https://hub.example.com", token, "other-device"),
      cache.feedCacheKey("https://hub.example.com", "jd_other_account", "device-demo"),
    ]).size).toBe(5);
    expect(key).not.toContain(token);
    expect(key).not.toContain("device-demo");
    expect(cache.feedCacheKey("https://hub.example.com", "")).toBeNull();
  });

  it("keeps fixed-width, stable keys distinct for short, long and Unicode credentials", () => {
    const secrets = [token, "x".repeat(65), "x".repeat(129), "fixture-\u{1f30d}-\u00e9", "fixture-\u{1f30e}-\u00e9"];
    const keys = secrets.map((secret) => cache.feedCacheKey("https://hub.example.com", secret)!);
    expect(new Set(keys).size).toBe(secrets.length);
    for (const [i, key] of keys.entries()) {
      expect(key).toMatch(/^persona\.feedCache\.v1:[0-9a-f]{16}:[0-9a-f]{16}$/);
      expect(key).toBe(cache.feedCacheKey("https://hub.example.com", secrets[i]));
      expect(key).not.toContain(secrets[i]);
    }
  });

  it("reads synchronously across a page reload, including manager and history", async () => {
    const value = page({ manager: "manager-demo", history: "history-demo", hasMore: true });
    expect(cache.writeFeedCache(cache.feedCacheScope(), value)).toBe(true);
    vi.resetModules();
    cache = await import("./feed-cache");
    expect(cache.readFeedCache()).toEqual(value);
    expect(JSON.stringify(entries())).not.toContain(token);
  });

  it("works without a device id on older hubs", () => {
    stored.delete("persona.tokenId");
    expect(cache.writeFeedCache(cache.feedCacheScope(), page())).toBe(true);
    expect(cache.readFeedCache()?.sessionID).toBe("main-demo");
    expect(JSON.stringify(entries())).not.toContain(token);
  });

  it("clears the old account and refuses its late writes even when the saved device id is stale", () => {
    const oldScope = cache.feedCacheScope();
    cache.writeFeedCache(oldScope, page());
    stored.set("persona.token", "jd_other_account");
    expect(cache.readFeedCache()).toBeNull();
    expect(entries()).toHaveLength(0);
    expect(cache.writeFeedCache(oldScope, page())).toBe(false);
    expect(cache.writeFeedCache(cache.feedCacheScope(), page({ sessionID: "new-main" }))).toBe(true);
    expect(cache.readFeedCache(oldScope)).toBeNull();
    expect(cache.readFeedCache()?.sessionID).toBe("new-main");
  });

  it("cannot read or write while signed out and does not restore cleared data on the same login", () => {
    const before = cache.feedCacheScope();
    cache.writeFeedCache(before, page());
    cache.clearFeedCache();
    stored.delete("persona.token");
    expect(cache.feedCacheScope()).toBeNull();
    expect(cache.readFeedCache()).toBeNull();
    expect(cache.writeFeedCache(before, page())).toBe(false);
    stored.set("persona.token", token);
    expect(cache.readFeedCache()).toBeNull();
    expect(cache.writeFeedCache(before, page())).toBe(false);
    expect(cache.writeFeedCache(cache.feedCacheScope(), page())).toBe(true);
  });

  it("invalidates an old writer when logout and the same login happen between two scope reads", () => {
    const before = cache.feedCacheScope();
    cache.clearFeedCache();
    expect(cache.writeFeedCache(before, page())).toBe(false);
    expect(cache.feedCacheScope()).not.toBe(before);
  });

  it("clears only feed keys, including cached feeds from another hub prefix", () => {
    cache.writeFeedCache(cache.feedCacheScope(), page());
    stored.set(cache.feedCacheKey("https://hub.example.com/other", token)!, "{}");
    stored.set("persona.language", "en");
    cache.clearFeedCache();
    expect(entries()).toHaveLength(0);
    expect(stored.get("persona.token")).toBe(token);
    expect(stored.get("persona.language")).toBe("en");
  });

  it("does not let supervision or compaction evict the last two user questions", () => {
    const items: Item[] = [];
    for (let question = 1; question <= 3; question++) {
      items.push(item(question * 10, "user", { text: `Question ${question}`, client_msg_id: `client-${question}` }));
      items.push(item(question * 10 + 1));
      if (question < 3) items.push(item(question * 10 + 2, "system", { kind: "notice", text: "Context compacted", compact: { trigger: "auto" } }));
      for (let n = 0; n < 3; n++) items.push(item(question * 10 + 3 + n, "user", {
        injected: { kind: "system", state: "supervise", line: "Review a synthetic task" }, text: "Automatic task supervision",
      }));
    }
    cache.writeFeedCache(cache.feedCacheScope(), page({ items }));
    const read = cache.readFeedCache()!;
    expect(read.items.filter((i) => i.client_msg_id).map((i) => i.text)).toEqual(["Question 2", "Question 3"]);
    expect(read.items.filter((i) => i.injected)).toHaveLength(6);
    expect(read.items.filter((i) => i.compact)).toHaveLength(1);
    expect(read.hasMore).toBe(true);
  });

  it("keeps only the newest two complete turns and their same-line image siblings", () => {
    const items = [item(1, "user"), item(2), item(3, "user", { id: "image-3", kind: "image", image: { mime: "image/png", lazy: true } }),
      item(3, "user"), item(4), item(5, "user"), item(6), item(7, "user", { queued: true })];
    cache.writeFeedCache(cache.feedCacheScope(), page({ items }));
    const read = cache.readFeedCache()!;
    expect(read.items.map((i) => i.id)).toEqual(items.slice(2).map((i) => i.id));
    expect(read.hasMore).toBe(true);
  });

  it("replaces the one cached main page instead of accumulating conversations", () => {
    const scope = cache.feedCacheScope();
    cache.writeFeedCache(scope, page());
    cache.writeFeedCache(scope, page({ sessionID: "other-main", items: [] }));
    expect(entries()).toHaveLength(1);
    expect(cache.readFeedCache()).toEqual(page({ sessionID: "other-main", items: [] }));
  });

  it("removes image bodies while preserving lazy image metadata and leaves the live items unchanged", () => {
    const items = [item(1, "user", { kind: "image", image: { mime: "image/png", data: "aGVsbG8=", width: 640, height: 480, size: 5 } }),
      item(2, "assistant", { kind: "tool_use", tool_input: { source: { type: "base64", media_type: "image/jpeg", data: "c2VjcmV0" } } }),
      item(3, "assistant", { text: "![image](data:image/png;base64,Ynl0ZXM=)" })];
    cache.writeFeedCache(cache.feedCacheScope(), page({ items }));
    const raw = JSON.stringify(entries());
    for (const body of ["aGVsbG8=", "c2VjcmV0", "Ynl0ZXM="]) expect(raw).not.toContain(body);
    expect(cache.readFeedCache()?.items[0].image).toEqual({ mime: "image/png", lazy: true, width: 640, height: 480, size: 5 });
    expect(items[0].image?.data).toBe("aGVsbG8=");
  });

  it("checks the size after removing image bytes", () => {
    expect(cache.writeFeedCache(cache.feedCacheScope(), page({ items: [item(1, "user", {
      kind: "image", image: { mime: "image/png", data: "a".repeat(cache.FEED_CACHE_MAX_BYTES * 2) },
    })] }))).toBe(true);
    expect(entries()[0][1].length).toBeLessThan(1000);
  });

  it("rejects a stored record containing image bytes rather than hydrating it", () => {
    cache.writeFeedCache(cache.feedCacheScope(), page());
    const key = entries()[0][0];
    stored.set(key, JSON.stringify(page({ items: [item(1, "user", { kind: "image", image: { mime: "image/png", data: "aGVsbG8=" } })] })));
    expect(cache.readFeedCache()).toBeNull();
    expect(entries()).toHaveLength(0);
  });

  it("rejects oversized UTF-8 pages and clears an older main rather than showing stale content", () => {
    const scope = cache.feedCacheScope();
    cache.writeFeedCache(scope, page());
    const text = "\u{1f30d}".repeat(cache.FEED_CACHE_MAX_BYTES / 3);
    expect(cache.writeFeedCache(scope, page({ items: [item(1, "user", { text })] }))).toBe(false);
    expect(cache.readFeedCache()).toBeNull();
    expect(entries()).toHaveLength(0);
  });

  it("rejects an enormous single turn instead of keeping a partial turn", () => {
    const items = Array.from({ length: cache.FEED_CACHE_MAX_ITEMS + 1 }, (_, i) => item(i + 1, i ? "assistant" : "user"));
    expect(cache.writeFeedCache(cache.feedCacheScope(), page({ items }))).toBe(false);
    expect(cache.readFeedCache()).toBeNull();
  });

  it("does not copy the authentication token from a message or URL into the cache", () => {
    expect(cache.writeFeedCache(cache.feedCacheScope(), page({ items: [item(1, "user", { text: `https://hub.example.com/image?token=${token}` })] }))).toBe(false);
    expect(entries()).toHaveLength(0);
  });

  it("ignores corrupt, oversized and incompatible stored data", () => {
    cache.writeFeedCache(cache.feedCacheScope(), page());
    const key = entries()[0][0];
    for (const raw of ["{", "null", "[]", JSON.stringify({ ...page(), items: [{ id: "a", seq: "bad" }] }),
      JSON.stringify({ ...page(), items: [item(1, "user", { text: {} as string })] }),
      JSON.stringify({ ...page(), items: [item(1, "user", { image: {} as Item["image"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { fold: {} as Item["fold"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { compact: { notes: {} } as Item["compact"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { injected: { line: {} } as Item["injected"] })] }),
      JSON.stringify({ ...page(), items: [item(1, "assistant", { fold: { items: 1, tools: 1, thoughts: 0, through_seq: 2, tasks: [{}] } as Item["fold"] })] }),
      JSON.stringify(page({ items: [item(1, "user", { text: "x".repeat(cache.FEED_CACHE_MAX_BYTES) })] }))]) {
      stored.set(key, raw);
      expect(cache.readFeedCache()).toBeNull();
    }
  });

  it("degrades quietly when browser storage is unavailable or its quota is exhausted", () => {
    const scope = cache.feedCacheScope();
    vi.spyOn(localStorage, "setItem").mockImplementation(() => { throw new DOMException("Full", "QuotaExceededError"); });
    expect(cache.writeFeedCache(scope, page())).toBe(false);
    vi.spyOn(localStorage, "getItem").mockImplementation(() => { throw new DOMException("Disabled", "SecurityError"); });
    expect(cache.feedCacheScope()).toBeNull();
    expect(cache.readFeedCache()).toBeNull();
    expect(() => cache.clearFeedCache()).not.toThrow();
  });
});

describe("memory pages of other chats", () => {
  const scope = () => cache.feedCacheScope()!;
  const write = (sid: string) => cache.writeMemoryPage(scope(), page({ sessionID: sid }));
  const has = (sid: string) => cache.readMemoryPage(scope(), "machine-demo", sid) !== null;

  it("keeps the most recently used chats and never touches storage", () => {
    for (let n = 0; n < cache.MEMORY_PAGES; n++) write(`s${n}`);
    expect(has("s0")).toBe(true); // reading it makes s1 the oldest
    write("newest");
    expect([has("s0"), has("s1"), has("newest")]).toEqual([true, false, true]);
    expect(entries()).toEqual([]);
  });

  it("never answers for another hub, another login, or after sign-out", () => {
    write("agent");
    expect(cache.readMemoryPage(scope(), "another-machine", "agent")).toBeNull();
    const old = scope();
    stored.set("persona.token", "jd_another_browser_credential");
    expect(cache.readMemoryPage(scope(), "machine-demo", "agent")).toBeNull();
    expect(cache.readMemoryPage(old, "machine-demo", "agent")).toBeNull();
    write("agent");
    cache.clearFeedCache();
    expect(has("agent")).toBe(false);
  });

  it("keeps only the newest turns, and no page at all for an empty chat", () => {
    cache.writeMemoryPage(scope(), page({ sessionID: "long", items: [item(1, "user"), item(2, "user"), item(3, "user")] }));
    const kept = cache.readMemoryPage(scope(), "machine-demo", "long")!;
    expect(kept.items.map((row) => row.seq)).toEqual([2, 3]);
    expect(kept.hasMore).toBe(true);
    cache.writeMemoryPage(scope(), page({ sessionID: "long", items: [] }));
    expect(has("long")).toBe(false);
  });
});
