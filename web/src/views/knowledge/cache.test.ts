// The knowledge page's search cache keeps only the most recent queries, so typing many queries while the page stays
// open does not keep every result list for good.
import { afterEach, describe, expect, it } from "vitest";
import type { KBResult } from "../../lib/kb";
import { SEARCHES_KEPT, searches } from "./cache";

const results = (q: string): KBResult[] => [{ entry: { kind: "docs", id: q, name: q, relevance: 0 }, count: 1 }];

afterEach(() => searches.clear());

describe("the knowledge search cache", () => {
  it("keeps only the most recent queries", () => {
    for (let i = 0; i < 500; i++) searches.set(`q${i}`, results(`q${i}`));
    expect(searches.size).toBe(SEARCHES_KEPT);
    expect(searches.get("q499")?.[0].entry.id).toBe("q499");
    expect(searches.get(`q${500 - SEARCHES_KEPT}`)).toBeDefined();
    expect(searches.get(`q${499 - SEARCHES_KEPT}`)).toBeUndefined();
    expect(searches.get("q0")).toBeUndefined();
  });

  it("counts a query read again as recent", () => {
    for (let i = 0; i < SEARCHES_KEPT; i++) searches.set(`q${i}`, results(`q${i}`));
    expect(searches.get("q0")).toBeDefined();
    searches.set("new", results("new"));
    expect(searches.get("q0")).toBeDefined();
    expect(searches.get("q1")).toBeUndefined();
    expect(searches.size).toBe(SEARCHES_KEPT);
  });

  it("counts a query written again as recent without growing", () => {
    for (let i = 0; i < SEARCHES_KEPT; i++) searches.set(`q${i}`, results(`q${i}`));
    searches.set("q0", results("again"));
    expect(searches.size).toBe(SEARCHES_KEPT);
    searches.set("new", results("new"));
    expect(searches.get("q0")?.[0].entry.id).toBe("again");
    expect(searches.get("q1")).toBeUndefined();
  });

  it("forgets everything on clear", () => {
    searches.set("ab", results("ab"));
    searches.clear();
    expect(searches.size).toBe(0);
    expect(searches.get("ab")).toBeUndefined();
  });
});
