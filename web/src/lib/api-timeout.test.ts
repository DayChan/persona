// @vitest-environment jsdom
// Reads the hub never answers give up after READ_TIMEOUT_MS with HubTimeoutError (#4492); writes keep waiting, a
// caller's own abort stays an AbortError, and an answer in time is unchanged.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, errorText, HubTimeoutError, isAbort, READ_TIMEOUT_MS } from "./api";

// A hub that never answers: the request ends only when its signal aborts.
function hang(_url: string, init?: RequestInit): Promise<Response> {
  return new Promise((_resolve, reject) => {
    init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
  });
}

describe("api's read timeout", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  it("gives up on a read the hub never answers, saying so", async () => {
    vi.stubGlobal("fetch", vi.fn(hang));
    const p = api("/settings");
    const caught = p.catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(READ_TIMEOUT_MS - 1);
    let settled = false;
    void p.then(() => (settled = true), () => (settled = true));
    await Promise.resolve();
    expect(settled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    const e = await caught;
    expect(e).toBeInstanceOf(HubTimeoutError);
    expect(isAbort(e)).toBe(false);
    expect(errorText(e)).toBe((e as Error).message);
  });

  it("asks again through a gap, and leaves the hub's own answers and writes alone", async () => {
    vi.stubEnv("MODE", "production"); // the retries are off under Vitest
    const answer = () => ({ ok: true, status: 200, text: async () => "{}" });
    const fetcher = vi.fn().mockRejectedValueOnce(new TypeError("flaky link")).mockResolvedValueOnce({ ok: false, status: 502, text: async () => "<html>" }).mockResolvedValueOnce(answer());
    vi.stubGlobal("fetch", fetcher);
    const read = api("/settings");
    await vi.advanceTimersByTimeAsync(1300);
    await read;
    expect(fetcher).toHaveBeenCalledTimes(3);

    const coded = vi.fn().mockResolvedValue({ ok: false, status: 502, text: async () => '{"error":"x","code":"machine_offline"}' });
    vi.stubGlobal("fetch", coded);
    await expect(api("/settings")).rejects.toMatchObject({ status: 502 });
    expect(coded).toHaveBeenCalledTimes(1);

    const write = vi.fn().mockRejectedValue(new TypeError("flaky link"));
    vi.stubGlobal("fetch", write);
    await expect(api("/todos", { method: "POST", json: {} })).rejects.toBeInstanceOf(TypeError);
    expect(write).toHaveBeenCalledTimes(1);
  });

  it("gives up after the last try, or with the time limit if that comes first", async () => {
    vi.stubEnv("MODE", "production");
    const down = vi.fn().mockRejectedValue(new TypeError("flaky link"));
    vi.stubGlobal("fetch", down);
    const out = api("/settings").catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(1300);
    expect(await out).toBeInstanceOf(TypeError);
    expect(down).toHaveBeenCalledTimes(3);

    const late = api("/settings", { timeout: 200 }).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(200);
    expect(await late).toBeInstanceOf(HubTimeoutError);
  });

  it("lets a write wait as long as it takes", async () => {
    vi.stubGlobal("fetch", vi.fn(hang));
    let settled = false;
    void api("/todos", { method: "POST", json: {} }).then(() => (settled = true), () => (settled = true));
    await vi.advanceTimersByTimeAsync(READ_TIMEOUT_MS * 3);
    expect(settled).toBe(false);
  });

  it("takes a read's own timeout, and 0 for none", async () => {
    vi.stubGlobal("fetch", vi.fn(hang));
    const short = api("/machines", { timeout: 1000 }).catch((e: unknown) => e);
    let none = false;
    void api("/machines", { timeout: 0 }).then(() => (none = true), () => (none = true));
    await vi.advanceTimersByTimeAsync(1000);
    expect(await short).toBeInstanceOf(HubTimeoutError);
    await vi.advanceTimersByTimeAsync(READ_TIMEOUT_MS * 2);
    expect(none).toBe(false);
  });

  it("keeps the caller's abort an AbortError", async () => {
    vi.stubGlobal("fetch", vi.fn(hang));
    const c = new AbortController();
    const p = api("/fs/find", { signal: c.signal }).catch((e: unknown) => e);
    c.abort();
    const e = await p;
    expect(isAbort(e)).toBe(true);
    expect(e).not.toBeInstanceOf(HubTimeoutError);
  });

  it("returns an answer that comes in time, and clears its timer", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ ok: true }), { status: 200 })));
    await expect(api("/hub")).resolves.toEqual({ ok: true });
    expect(vi.getTimerCount()).toBe(0);
  });
});
