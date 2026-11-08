// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NotifyPrefs } from "./notify";

type Call = {
  body?: Partial<NotifyPrefs>;
  settled?: boolean;
  resolve: (value: unknown) => void;
  reject: (error: Error) => void;
};
const calls: Call[] = [];
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  api: (_path: string, init?: { json?: Partial<NotifyPrefs> }) =>
    new Promise((resolve, reject) => calls.push({ body: init?.json, resolve, reject })),
  enc: encodeURIComponent,
}));

const n = await import("./notify");
const night = { enabled: true, start: "23:00", end: "07:00", tz: "UTC" };
let hub: NotifyPrefs;

// Committing a PUT changes authoritative state; resolving a GET only returns its captured snapshot.
async function commit(index: number) {
  const call = calls[index];
  call.settled = true;
  hub = { ...hub, ...call.body };
  call.resolve(structuredClone(hub));
  await Promise.resolve();
}

async function reload() {
  const index = calls.length;
  const load = n.loadNotifyPrefs();
  calls[index].resolve(structuredClone(hub));
  await load;
  expect(n.notifyPrefs()).toEqual(n.normalize(hub));
}

beforeEach(async () => {
  localStorage.clear();
  calls.length = 0;
  hub = { input: true, done: "long", quiet: night, muted_managers: [], long_turn_seconds: 120, done_delay_seconds: 5 };
  await reload();
  calls.length = 0;
});
afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("notification preference write ordering (#1285)", () => {
  it("retains latest intent on a transport that commits the newest available PUT first", async () => {
    const saves = [n.saveNotifyPrefs({ input: false }), n.saveNotifyPrefs({ done: "always" })];
    for (let i = 0; i < saves.length; i++) {
      let index = calls.length - 1;
      while (calls[index].settled) index--;
      await commit(index);
    }
    await Promise.all(saves);
    expect(hub).toMatchObject({ input: false, done: "always" });
    await reload();
  });

  it("commits rapid different-field edits in order and retains them after reload", async () => {
    const first = n.saveNotifyPrefs({ input: false });
    const second = n.saveNotifyPrefs({ done: "always" });
    expect(n.notifyPrefs()).toMatchObject({ input: false, done: "always", quiet: night });
    expect(calls).toHaveLength(1); // A transport must never get two PUTs it could commit backwards.
    await commit(0);
    await first;
    expect(n.notifyPrefs()).toMatchObject({ input: false, done: "always" });
    expect(calls).toHaveLength(2);
    await commit(1);
    await second;
    expect(hub).toMatchObject({ input: false, done: "always", quiet: night, long_turn_seconds: 120 });
    await reload();
  });

  it("keeps the last of three rapid same-field edits", async () => {
    const saves = [n.saveNotifyPrefs({ done: "always" }), n.saveNotifyPrefs({ done: "off" }), n.saveNotifyPrefs({ done: "long" })];
    expect(calls).toHaveLength(1);
    for (let i = 0; i < saves.length; i++) {
      await commit(i);
      await saves[i];
      expect(n.notifyPrefs().done).toBe("long");
    }
    await reload();
  });

  it("rolls back an earlier rejected field without losing a queued edit, then allows an explicit retry", async () => {
    const first = n.saveNotifyPrefs({ input: false });
    const failure = expect(first).rejects.toThrow("offline");
    const second = n.saveNotifyPrefs({ done: "off" });
    calls[0].reject(new Error("offline"));
    await failure;
    expect(n.notifyPrefs()).toMatchObject({ input: true, done: "off" });
    await commit(1);
    await second;
    expect(hub).toMatchObject({ input: true, done: "off" });
    const retry = n.saveNotifyPrefs({ input: false });
    await commit(2);
    await retry;
    await reload();
    expect(hub).toMatchObject({ input: false, done: "off" });
  });

  it("rolls back a later failure to the preceding confirmed write", async () => {
    const first = n.saveNotifyPrefs({ done: "always" });
    const second = n.saveNotifyPrefs({ done: "off" });
    const failure = expect(second).rejects.toThrow("refused");
    await commit(0);
    await first;
    calls[1].reject(new Error("refused"));
    await failure;
    expect(n.notifyPrefs().done).toBe("always");
    await reload();
  });

  it("does not retain failed optimistic values when every queued write fails", async () => {
    const first = n.saveNotifyPrefs({ input: false });
    const firstFailure = expect(first).rejects.toThrow("first");
    const second = n.saveNotifyPrefs({ done: "off" });
    const secondFailure = expect(second).rejects.toThrow("second");
    calls[0].reject(new Error("first"));
    await firstFailure;
    calls[1].reject(new Error("second"));
    await secondFailure;
    expect(n.notifyPrefs()).toEqual(n.normalize(hub));
    await reload();
  });

  it("keeps queued quiet hours and manager intents across successful writes", async () => {
    const quiet = { ...night, start: "22:00" };
    const saves = [n.saveManagerPush("example-one", false), n.saveNotifyPrefs({ quiet }), n.saveManagerPush("example-two", false), n.saveManagerPush("example-one", true)];
    expect(n.notifyPrefs()).toMatchObject({ quiet, muted_managers: ["example-two"] });
    expect(calls).toHaveLength(1);
    for (let i = 0; i < saves.length; i++) {
      await commit(i);
      await saves[i];
      expect(n.notifyPrefs()).toMatchObject({ quiet, muted_managers: ["example-two"] });
    }
    expect(hub).toMatchObject({ quiet, input: true, done: "long", muted_managers: ["example-two"] });
    await reload();
  });

  it("does not retry a failed manager mute through another manager's queued save", async () => {
    const first = n.saveManagerPush("example-one", false);
    const failure = expect(first).rejects.toThrow("refused");
    const second = n.saveManagerPush("example-two", false);
    calls[0].reject(new Error("refused"));
    await failure;
    expect(n.notifyPrefs().muted_managers).toEqual(["example-two"]);
    await commit(1);
    await second;
    expect(hub.muted_managers).toEqual(["example-two"]);
    await reload();
  });

  it("keeps a successful notification edit when a queued manager write fails", async () => {
    const first = n.saveNotifyPrefs({ done: "always" });
    const second = n.saveManagerPush("example-one", false);
    const failure = expect(second).rejects.toThrow("refused");
    await commit(0);
    await first;
    calls[1].reject(new Error("refused"));
    await failure;
    expect(n.notifyPrefs()).toMatchObject({ done: "always", muted_managers: [] });
    await reload();
  });

  it("keeps a queued same-field edit after the preceding edit fails", async () => {
    const first = n.saveNotifyPrefs({ done: "always" });
    const failure = expect(first).rejects.toThrow("refused");
    const second = n.saveNotifyPrefs({ done: "off" });
    calls[0].reject(new Error("refused"));
    await failure;
    expect(n.notifyPrefs().done).toBe("off");
    await commit(1);
    await second;
    await reload();
    expect(hub.done).toBe("off");
  });

  it("does not let a failed manager save discard a queued notification edit", async () => {
    const first = n.saveManagerPush("example-one", false);
    const failure = expect(first).rejects.toThrow("refused");
    const second = n.saveNotifyPrefs({ input: false });
    calls[0].reject(new Error("refused"));
    await failure;
    expect(n.notifyPrefs()).toMatchObject({ input: false, muted_managers: [] });
    await commit(1);
    await second;
    await reload();
  });

  it("captures queued quiet hours before a caller mutates its patch", async () => {
    const first = n.saveNotifyPrefs({ input: false });
    const patch = { quiet: { ...night, start: "22:00" } };
    const second = n.saveNotifyPrefs(patch);
    patch.quiet.start = "21:00";
    await commit(0);
    await first;
    await commit(1);
    await second;
    expect(hub.quiet?.start).toBe("22:00");
    await reload();
  });

  it("ignores a load started before an edit", async () => {
    const load = n.loadNotifyPrefs();
    const stale = structuredClone(hub);
    const save = n.saveNotifyPrefs({ done: "always" });
    await commit(1);
    await save;
    calls[0].resolve(stale);
    await load;
    expect(n.notifyPrefs().done).toBe("always");
  });

  it.each([false, true])("ignores a load started during a save (response after save: %s)", async (afterSave) => {
    const save = n.saveNotifyPrefs({ done: "always" });
    const stale = structuredClone(hub);
    const load = n.loadNotifyPrefs();
    if (afterSave) {
      await commit(0);
      await save;
    }
    calls[1].resolve(stale);
    await load;
    expect(n.notifyPrefs().done).toBe("always");
    if (!afterSave) {
      await commit(0);
      await save;
    }
    await reload();
  });

  it("ignores an older load after a newer load has completed", async () => {
    const first = n.loadNotifyPrefs();
    const stale = structuredClone(hub);
    hub = { ...hub, done: "always" };
    const second = n.loadNotifyPrefs();
    calls[1].resolve(structuredClone(hub));
    await second;
    calls[0].resolve(stale);
    await first;
    expect(n.notifyPrefs().done).toBe("always");
  });

  it("keeps local pending intent over another tab's cache without writing the storage event back", async () => {
    const save = n.saveNotifyPrefs({ done: "always" });
    hub = { ...hub, input: false };
    localStorage.setItem("persona.notifyPrefs", JSON.stringify(hub));
    const write = vi.spyOn(Storage.prototype, "setItem");
    window.dispatchEvent(new StorageEvent("storage", { key: "persona.notifyPrefs" }));
    expect(write).not.toHaveBeenCalled();
    expect(n.notifyPrefs()).toMatchObject({ input: false, done: "always" });
    await commit(0);
    await save;
    await reload();
  });

  it("does not let a pending load replace another tab's newer cached preferences", async () => {
    const load = n.loadNotifyPrefs();
    const stale = structuredClone(hub);
    hub = { ...hub, done: "off" };
    localStorage.setItem("persona.notifyPrefs", JSON.stringify(hub));
    window.dispatchEvent(new StorageEvent("storage", { key: "persona.notifyPrefs" }));
    calls[0].resolve(stale);
    await load;
    expect(n.notifyPrefs().done).toBe("off");
  });
});
