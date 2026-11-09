// Composer listeners per draft key: a box that unmounts leaves no empty set behind, and a key with no box still keeps
// what is sent to it in the stored draft.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { draftListenerKeysForTests, onDraftEvent, sendDraft, type DraftEvent } from "./drafts";

let store: Map<string, string>;

beforeEach(() => {
  store = new Map();
  vi.stubGlobal("localStorage", {
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => store.set(k, v),
    removeItem: (k: string) => store.delete(k),
  });
});

afterEach(() => vi.unstubAllGlobals());

describe("draft listeners", () => {
  it("leaves no key behind for a draft once its last composer goes", () => {
    const before = draftListenerKeysForTests();
    for (let i = 0; i < 100; i++) onDraftEvent(`m/s${i}`, () => {})();
    expect(draftListenerKeysForTests()).toBe(before);
  });

  it("keeps the key while another composer is left, and sendDraft reaches that one", () => {
    const before = draftListenerKeysForTests();
    const got: DraftEvent[] = [];
    let gone = 0;
    const offGone = onDraftEvent("m/kept", () => gone++);
    const offLive = onDraftEvent("m/kept", (e) => got.push(e));
    offGone();
    expect(draftListenerKeysForTests()).toBe(before + 1);

    expect(sendDraft("m/kept", { type: "replace", text: "r" })).toBe(true);
    expect(got).toEqual([{ type: "replace", text: "r" }]);
    expect(gone).toBe(0);
    expect(store.has("draft:m/kept")).toBe(false); // the composer has it, not the store

    offLive();
    expect(draftListenerKeysForTests()).toBe(before);
  });

  it("with its key gone a draft is kept in the store, and a composer mounted again hears events", () => {
    onDraftEvent("m/again", () => {})();
    expect(sendDraft("m/again", { type: "replace", text: "kept" })).toBe(true);
    expect(store.get("draft:m/again")).toBe("kept");
    expect(sendDraft("m/again", { type: "image", image: { name: "a.png", mime: "image/png", data: "AA" } })).toBe(false);

    const got: string[] = [];
    const off = onDraftEvent("m/again", (e) => got.push(e.type));
    expect(sendDraft("m/again", { type: "image", image: { name: "a.png", mime: "image/png", data: "AA" } })).toBe(true);
    expect(got).toEqual(["image"]);
    off();
  });
});
