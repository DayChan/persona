// @vitest-environment jsdom
// Exercise the real REST client: a mock api() cannot reveal a queued request borrowing the next login's token.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getToken, handleUnauthorized, setToken } from "./api";
import * as notify from "./notify";
import type { NotifyPrefs } from "./notify";

type Call = {
  method: string;
  authorization: string | null;
  body: unknown;
  settled: boolean;
  resolve: (response: Response) => void;
};
const calls: Call[] = [];
const initial: NotifyPrefs = { input: true, done: "long", muted_managers: [] };
const nextLogin: NotifyPrefs = { input: true, done: "off", muted_managers: ["example-new"] };
const outcomes: Promise<unknown>[] = [];
const result = (promise: Promise<unknown>) => {
  const outcome = promise.then(() => "saved", (error: unknown) => error);
  outcomes.push(outcome);
  return outcome;
};
const settle = async (index: number, body: unknown, status = 200) => {
  calls[index].settled = true;
  calls[index].resolve(new Response(JSON.stringify(body), { status }));
  // fetch(), response.text(), api(), and the preference queue each have their own promise continuation.
  for (let i = 0; i < 12; i++) await Promise.resolve();
};

beforeEach(async () => {
  localStorage.clear();
  calls.length = 0;
  outcomes.length = 0;
  setToken("test-token-a");
  vi.stubGlobal("fetch", vi.fn((_input: unknown, init?: RequestInit) => new Promise<Response>((resolve) => {
    calls.push({ method: init?.method ?? "GET", authorization: new Headers(init?.headers).get("Authorization"), body: init?.body ? JSON.parse(String(init.body)) : undefined, settled: false, resolve });
    // Deliberately retain late responses even if the client aborts: fencing must hold independently of cancellation.
  })));
  const load = notify.loadNotifyPrefs();
  await settle(0, initial);
  await load;
  calls.length = 0;
});

afterEach(async () => {
  // Drain unexpected requests too, so a pre-fix assertion failure does not leak pending work into the next case.
  for (let i = 0; i < calls.length; i++) {
    if (!calls[i].settled) await settle(i, initial);
  }
  await Promise.all(outcomes);
  handleUnauthorized(() => {});
  setToken("");
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  localStorage.clear();
});

describe("notification work belongs to the login that queued it (#1285)", () => {
  it.each(["notification", "manager", "mixed"])("discards old %s writes before they can borrow a new token", async (kind) => {
    const first = result(kind === "manager" ? notify.saveManagerPush("example-old", false) : notify.saveNotifyPrefs({ input: false }));
    const second = result(kind === "notification" ? notify.saveNotifyPrefs({ done: "always" }) : notify.saveManagerPush("example-queued", false));
    expect(calls.map((call) => call.authorization)).toEqual(["Bearer test-token-a"]);
    setToken("test-token-b");
    await settle(0, { ...initial, input: false, muted_managers: ["example-old"] });
    expect(calls.map((call) => call.authorization)).toEqual(["Bearer test-token-a"]);
    expect(localStorage.getItem("persona.notifyPrefs")).toBeNull();
    expect(await first).toMatchObject({ name: "AbortError" });
    expect(await second).toMatchObject({ name: "AbortError" });
    expect(notify.notifyPrefs()).toEqual(notify.DEFAULT_PREFS);
  });

  it.each([200, 500])("fences an old HTTP %s response from the new login's view and cache", async (status) => {
    const old = result(notify.saveNotifyPrefs({ input: false }));
    setToken("test-token-b");
    const load = notify.loadNotifyPrefs();
    await settle(1, nextLogin);
    await load;
    const cached = localStorage.getItem("persona.notifyPrefs");
    const write = vi.spyOn(Storage.prototype, "setItem");
    await settle(0, status === 200 ? { ...initial, input: false } : { error: "old request failed" }, status);
    expect(await old).toMatchObject({ name: "AbortError" });
    expect(notify.notifyPrefs()).toEqual(notify.normalize(nextLogin));
    expect(localStorage.getItem("persona.notifyPrefs")).toBe(cached);
    expect(write).not.toHaveBeenCalled();
  });

  it("lets the new login save without waiting for the retired queue", async () => {
    const old = result(notify.saveManagerPush("example-old", false));
    const queued = result(notify.saveManagerPush("example-queued", false));
    setToken("test-token-b");
    const save = result(notify.saveNotifyPrefs({ done: "always" }));
    expect(calls.map((call) => call.authorization)).toEqual(["Bearer test-token-a", "Bearer test-token-b"]);
    expect(calls[1].body).toEqual({ done: "always" });
    await settle(1, { ...nextLogin, done: "always" });
    expect(await save).toBe("saved");
    const cached = localStorage.getItem("persona.notifyPrefs");
    await settle(0, { ...initial, muted_managers: ["example-old"] });
    expect(await old).toMatchObject({ name: "AbortError" });
    expect(await queued).toMatchObject({ name: "AbortError" });
    expect(calls).toHaveLength(2);
    expect(notify.notifyPrefs()).toMatchObject({ ...nextLogin, done: "always" });
    expect(localStorage.getItem("persona.notifyPrefs")).toBe(cached);
  });

  it("retires writes across logout followed by the same token", async () => {
    const old = result(notify.saveNotifyPrefs({ input: false }));
    const queued = result(notify.saveNotifyPrefs({ done: "always" }));
    setToken("");
    setToken("test-token-a");
    await settle(0, { ...initial, input: false });
    expect(calls).toHaveLength(1);
    expect(await old).toMatchObject({ name: "AbortError" });
    expect(await queued).toMatchObject({ name: "AbortError" });
    expect(notify.notifyPrefs()).toEqual(notify.DEFAULT_PREFS);
  });

  it("detects a credential replaced directly in storage before dispatch", async () => {
    const old = result(notify.saveNotifyPrefs({ input: false }));
    const queued = result(notify.saveManagerPush("example-queued", false));
    localStorage.setItem("persona.token", "test-token-b");
    await settle(0, { ...initial, input: false });
    expect(calls).toHaveLength(1);
    expect(await old).toMatchObject({ name: "AbortError" });
    expect(await queued).toMatchObject({ name: "AbortError" });
  });

  it("discards an old preference load after re-pairing", async () => {
    const old = result(notify.loadNotifyPrefs());
    setToken("test-token-b");
    const load = notify.loadNotifyPrefs();
    await settle(1, nextLogin);
    await load;
    const cached = localStorage.getItem("persona.notifyPrefs");
    await settle(0, { ...initial, input: false });
    expect(await old).toMatchObject({ name: "AbortError" });
    expect(notify.notifyPrefs()).toEqual(notify.normalize(nextLogin));
    expect(localStorage.getItem("persona.notifyPrefs")).toBe(cached);
  });

  it("does not let an old 401 sign out the new login", async () => {
    const unauthorized = vi.fn(() => setToken(""));
    handleUnauthorized(unauthorized);
    const old = result(notify.saveNotifyPrefs({ input: false }));
    setToken("test-token-b");
    const load = notify.loadNotifyPrefs();
    await settle(1, nextLogin);
    await load;
    await settle(0, { error: "invalid token" }, 401);
    expect(await old).toMatchObject({ name: "AbortError" });
    expect(unauthorized).not.toHaveBeenCalled();
    expect(getToken()).toBe("test-token-b");
    expect(notify.notifyPrefs()).toEqual(notify.normalize(nextLogin));
  });

  it("still invokes sign-out for a current login's 401", async () => {
    const unauthorized = vi.fn(() => setToken(""));
    handleUnauthorized(unauthorized);
    const save = result(notify.saveNotifyPrefs({ input: false }));
    await settle(0, { error: "invalid token" }, 401);
    expect(unauthorized).toHaveBeenCalledOnce();
    expect(getToken()).toBe("");
    expect(await save).toMatchObject({ name: "AbortError" });
  });
});
