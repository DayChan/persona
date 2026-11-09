// Several managers' installs (docs/assistant/managers.md "Clients"): an operation is its manager's. A push says whose, one this
// page starts is the manager it named, and every later request about it (its preview, retry, cancel) names that one;
// a list is one manager's, so each is asked for its own, and one that no longer has an operation lets it go (#1832).
import { beforeEach, describe, expect, it, vi } from "vitest";

const sent: { path: string; init: { method?: string; headers?: Record<string, string> } }[] = [];
const answers: ((v: unknown) => void)[] = [];
const rejects: ((e: unknown) => void)[] = [];
vi.mock("./api", async (orig) => ({
  ...(await orig<typeof import("./api")>()),
  api: vi.fn((path: string, init: { method?: string; headers?: Record<string, string> } = {}) => {
    sent.push({ path, init });
    return new Promise((resolve, reject) => {
      answers.push(resolve);
      rejects.push(reject);
    });
  }),
}));
vi.mock("./extensions", async (orig) => ({ ...(await orig<typeof import("./extensions")>()), changed: vi.fn((r) => r) }));
const hub = vi.hoisted(() => new Map<string, (msg: Record<string, unknown>) => void>());
vi.mock("./store", async (orig) => ({
  ...(await orig<typeof import("./store")>()),
  hubHas: () => true,
  onHubEvent: (type: string, l: (msg: Record<string, unknown>) => void) => {
    hub.set(type, l);
    return () => {};
  },
}));

import { cancelOperation, forget, watchOperations, getOperation, latestFor, operations, reloadOperations, retryOperation, startFromMarket, track, type ExtensionOperation } from "./extension-operations";
import { resetManagerChoice, setRosterDefault } from "./manager-choice";
import { resetRoster, setRoster } from "./managers";
import { ApiError } from "./api";

const op = (over: Partial<ExtensionOperation> = {}): ExtensionOperation => ({
  id: "op1", kind: "upgrade", extension: "voice", market: "official", state: "running", step: "download", steps: [], retryable: true,
  started_at: "2026-11-06T00:00:00Z", updated_at: "2026-11-06T00:00:01Z", ...over,
});
const header = (i: number) => sent[i].init.headers?.["X-Persona-Manager"];

beforeEach(() => {
  sent.length = 0;
  answers.length = 0;
  rejects.length = 0;
  resetRoster();
  for (const o of operations()) forget(o);
  resetManagerChoice();
  setRosterDefault("assistant");
});

describe("another manager's operations", () => {
  it("keeps two managers' operations of one extension apart, the same id too", () => {
    track(op(), "assistant");
    track(op({ state: "needs_consent" }), "m-1");
    expect(operations()).toHaveLength(2);
    expect(latestFor(operations(), "voice", "official")?.state).toBe("running");
    expect(latestFor(operations(), "voice", "official", "m-1")?.state).toBe("needs_consent");
    expect(latestFor(operations(), "voice", "official", "m-1")?.manager).toBe("m-1");
  });

  it("names the operation's manager on what is asked about it", async () => {
    const started = startFromMarket("official", "voice", "m-1");
    expect(sent[0].path).toMatch(/\/-\/catalog\/preview$/);
    expect(header(0)).toBe("m-1");
    answers[0]({ operation: op() });
    expect((await started).manager).toBe("m-1");
    void getOperation("op1", "m-1");
    void retryOperation("op1", "m-1");
    void cancelOperation("op1", "m-1");
    expect([header(1), header(2), header(3)]).toEqual(["m-1", "m-1", "m-1"]);
    void getOperation("op1");
    expect(header(4)).toBeUndefined();
  });

  it("lists each manager's operations with that manager, and lets go of what one no longer lists (#1832)", async () => {
    track(op({ id: "theirs" }), "m-1");
    track(op({ id: "ours" }), "assistant");
    const p = reloadOperations();
    expect([header(0), header(1)]).toEqual([undefined, "m-1"]);
    answers[0]({ operations: [op({ id: "ours" })] });
    answers[1]({ operations: [] }); // m-1 restarted: its install is gone
    await p;
    expect(operations().map((o) => o.id)).toEqual(["ours"]);
  });

  it("keeps another manager's operations when its list fails, drops them when the hub no longer has it", async () => {
    track(op({ id: "a" }), "m-1");
    track(op({ id: "b" }), "m-2");
    const p = reloadOperations();
    answers[0]({ operations: [] });
    rejects[1](new ApiError(503, "offline", { code: "assistant_offline" }));
    rejects[2](new ApiError(404, "gone", { code: "no_manager" }));
    await p;
    expect(operations().map((o) => `${o.manager}/${o.id}`)).toEqual(["m-1/a"]);
  });

  it("also lists the online managers of the roster, so an install started elsewhere shows", async () => {
    setRoster({ default: "assistant", managers: [{ id: "assistant", name: "A", online: true }, { id: "m-3", name: "C", online: true }, { id: "m-4", name: "D", online: false }] });
    const p = reloadOperations();
    expect(sent.map((_, i) => header(i))).toEqual([undefined, "m-3"]);
    answers[0]({ operations: [] });
    answers[1]({ operations: [op({ id: "there" })] });
    await p;
    expect(latestFor(operations(), "voice", "official", "m-3")?.id).toBe("there");
  });

  it("a GET, retry or cancel the manager answers 404 not_found for lets the operation go", async () => {
    track(op({ id: "x" }), "m-1");
    track(op({ id: "x" }), "assistant");
    const p = cancelOperation("x", "m-1");
    rejects[0](new ApiError(404, "No such install", { code: "not_found" }));
    await expect(p).rejects.toBeInstanceOf(ApiError);
    expect(operations().map((o) => o.manager)).toEqual([undefined]);
    const g = getOperation("x");
    rejects[1](new ApiError(500, "boom", { code: "internal" }));
    await expect(g).rejects.toBeInstanceOf(ApiError);
    expect(operations()).toHaveLength(1); // any other failure keeps it
    const r = retryOperation("x");
    rejects[2](new ApiError(404, "No such install", { code: "not_found" }));
    await expect(r).rejects.toBeInstanceOf(ApiError);
    expect(operations()).toHaveLength(0);
  });

  it("a managers push (a manager reconnected) lists the operations again, as a hello does", async () => {
    track(op({ id: "stuck" }), "m-1");
    const off = watchOperations(() => {});
    answers.splice(0).forEach((a) => a({ operations: [op({ id: "stuck" })] }));
    sent.length = 0;
    hub.get("managers")?.({ type: "managers" });
    expect(sent.map((_, i) => header(i))).toEqual([undefined, "m-1"]);
    answers[0]({ operations: [] });
    answers[1]({ operations: [] });
    await vi.waitFor(() => expect(operations()).toHaveLength(0));
    off();
  });
});
