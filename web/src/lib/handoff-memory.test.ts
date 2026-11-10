import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/inbox/handoff_memory.json";
import { forgetHandOffs, handOffEntries, handOffSourceKey, HANDOFF_MEMORY_LIMIT, recallHandOff, rememberHandOff, resolveHandOff } from "./handoff-memory";

/** A scratch Storage. */
function memoryStore(): Storage {
  const m = new Map<string, string>();
  return {
    get length() { return m.size; },
    clear: () => m.clear(),
    getItem: (k) => m.get(k) ?? null,
    key: (i) => [...m.keys()][i] ?? null,
    removeItem: (k) => void m.delete(k),
    setItem: (k, v) => void m.set(k, String(v)),
  };
}

describe("Hand to AI memory per source (#2121, spec/fixtures/inbox/handoff_memory.json)", () => {
  it("keeps the shared limit", () => {
    expect(fixture.schema_version).toBe(1);
    expect(fixture.limit).toBe(HANDOFF_MEMORY_LIMIT);
  });

  for (const c of fixture.keys) {
    it(`source key: ${c.name}`, () => {
      expect(handOffSourceKey(c.source, "mutes" in c ? c.mutes : undefined)).toBe(c.key);
    });
  }

  for (const c of fixture.resolve) {
    it(`resolve: ${c.name}`, () => {
      const r = resolveHandOff(c.remembered, (path) => (c.listed as string[]).includes(path));
      expect(r?.target ?? null).toBe(c.target);
      expect(r?.lastTime ?? false).toBe(c.last_time);
      expect(r?.gone ?? false).toBe(c.gone);
    });
  }

  it("keeps one entry per source, latest first", () => {
    const store = memoryStore();
    for (const [key, target] of fixture.recency.remember) rememberHandOff(key, target, store);
    expect(handOffEntries(store).map((e) => e.source)).toEqual(fixture.recency.order);
    for (const [key, target] of Object.entries(fixture.recency.recall)) expect(recallHandOff(key, store)).toBe(target);
  });

  it("keeps the most recent sources and forgets on sign-out", () => {
    const store = memoryStore();
    for (let i = 0; i < HANDOFF_MEMORY_LIMIT + 5; i++) rememberHandOff(`github|repo:a/${i}`, "main", store);
    expect(handOffEntries(store)).toHaveLength(HANDOFF_MEMORY_LIMIT);
    expect(recallHandOff("github|repo:a/0", store)).toBeNull();
    forgetHandOffs(store);
    expect(handOffEntries(store)).toEqual([]);
  });
});
