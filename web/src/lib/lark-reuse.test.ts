// A Feishu chat read again as fresh JSON keeps the objects of the messages and receipts that read the same.
import { describe, expect, it } from "vitest";
import type { LarkMessage } from "./inbox";
import type { ReadState } from "./lark-chat";
import { keepSameMessages, keepSameStates } from "./lark-reuse";

const msg = (n: number, extra: Partial<LarkMessage> = {}): LarkMessage => ({
  id: `m${n}`, position: String(n), sender_name: "Colleague", text: `Message ${n}`,
  parts: [{ kind: "text", text: `Message ${n}` }], reactions: [{ key: "OK", count: 1 }], ...extra,
});
const fresh = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

describe("keepSameMessages", () => {
  it("returns the list it had when every message reads the same", () => {
    const prev = [msg(1), msg(2), msg(3)];
    expect(keepSameMessages(prev, fresh(prev))).toBe(prev);
  });

  it("replaces only the message that changed and keeps the others' objects", () => {
    const prev = [msg(1), msg(2), msg(3)];
    const next = fresh(prev);
    next[1].reactions = [{ key: "OK", count: 2 }];
    const out = keepSameMessages(prev, next);
    expect(out).not.toBe(prev);
    expect(out[0]).toBe(prev[0]);
    expect(out[1]).toBe(next[1]);
    expect(out[1].reactions).toEqual([{ key: "OK", count: 2 }]);
    expect(out[2]).toBe(prev[2]);
  });

  it("keeps the old objects around a message that came or went", () => {
    const prev = [msg(1), msg(2)];
    const added = keepSameMessages(prev, [...fresh(prev), msg(3)]);
    expect(added).toHaveLength(3);
    expect(added[0]).toBe(prev[0]);
    expect(added[1]).toBe(prev[1]);
    const removed = keepSameMessages(prev, fresh(prev).slice(1));
    expect(removed).toEqual([prev[1]]);
    expect(removed[0]).toBe(prev[1]);
  });

  it("is a new list when only the order moved", () => {
    const prev = [msg(1), msg(2)];
    const out = keepSameMessages(prev, [prev[1], prev[0]]);
    expect(out).not.toBe(prev);
    expect(out[0]).toBe(prev[1]);
  });

  it("sees a field that is gone or new as a change", () => {
    const prev = [msg(1, { edited: true })];
    const next = fresh(prev);
    delete next[0].edited;
    expect(keepSameMessages(prev, next)[0]).toBe(next[0]);
    expect(keepSameMessages(prev, [{ ...fresh(prev[0]), recalled: true }])[0]).not.toBe(prev[0]);
  });
});

describe("keepSameStates", () => {
  const state = (id: string, read: number, unread: number): ReadState => ({ message_id: id, read_count: read, unread_count: unread });

  it("returns the map it had when no receipt changed", () => {
    const prev = new Map([["m1", state("m1", 1, 2)], ["m2", state("m2", 3, 0)]]);
    expect(keepSameStates(prev, new Map(fresh([...prev])))).toBe(prev);
  });

  it("copies a changed answer and keeps the unchanged receipts' objects", () => {
    const prev = new Map([["m1", state("m1", 1, 2)], ["m2", state("m2", 3, 0)]]);
    const next = new Map([["m1", state("m1", 2, 1)], ["m2", state("m2", 3, 0)], ["m3", state("m3", 0, 1)]]);
    const out = keepSameStates(prev, next);
    expect(out).not.toBe(prev);
    expect(out).not.toBe(next);
    expect(out.get("m1")).toBe(next.get("m1"));
    expect(out.get("m2")).toBe(prev.get("m2"));
    expect(out.get("m3")).toBe(next.get("m3"));
  });

  it("starts from an empty map", () => {
    const prev = new Map<string, ReadState>();
    expect(keepSameStates(prev, new Map())).toBe(prev);
    const out = keepSameStates(prev, new Map([["m1", state("m1", 1, 0)]]));
    expect([...out.keys()]).toEqual(["m1"]);
  });
});
