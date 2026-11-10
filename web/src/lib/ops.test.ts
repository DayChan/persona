import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { apiOp, isTransient, OP_HEADER, OP_OFFLINE_MS, OP_RETRY_MS, untilDelivered, type RetryClock } from "./ops";

function response(body: unknown, status = 200, json = true) {
  return { ok: status < 400, status, text: async () => (json ? JSON.stringify(body) : String(body)) };
}

/** A clock the test moves: every wait passes at once and is recorded. */
function fakeClock(online?: (t: number) => boolean): RetryClock & { waits: number[] } {
  let t = 0;
  const waits: number[] = [];
  return {
    waits,
    now: () => t,
    online: online && (() => online(t)),
    wait: async (ms) => {
      waits.push(ms);
      t += ms;
    },
  };
}

const OP = "0b6c1c9e-4f1a-4c3b-9d2e-7a5f8e1d2c3b";

beforeEach(() => vi.stubGlobal("localStorage", { getItem: () => "tok" }));
afterEach(() => vi.unstubAllGlobals());

describe("what counts as a gap", () => {
  it.each(["kb_disabled", "kb_unavailable", "kb_read_only", "assistant_offline"])("never retries a coded %s as a connection gap", (code) => {
    expect(isTransient(new ApiError(503, "Request failed", { code }))).toBe(false);
  });

  it("a failed fetch and a proxy's 502/503/504 are gaps; the hub's own answers are not", () => {
    expect(isTransient(new TypeError("Failed to fetch"))).toBe(true);
    expect(isTransient(new ApiError(502, "HTTP 502", { error: "<html>" }, false))).toBe(true);
    expect(isTransient(new ApiError(503, "HTTP 503", {}, true))).toBe(true);
    expect(isTransient(new ApiError(502, "offline", { code: "node_unreachable" }))).toBe(false);
    expect(isTransient(new ApiError(504, "timeout", { error: "timeout", delivered: "unknown" }))).toBe(false);
    expect(isTransient(new ApiError(500, "boom", {}, false))).toBe(false);
    expect(isTransient(new ApiError(409, "draft", { code: "input_draft" }))).toBe(false);
    expect(isTransient(new DOMException("aborted", "AbortError"))).toBe(false);
  });
});

describe("a write through a gap", () => {
  it("is sent again with the same op id until it goes", async () => {
    const fetcher = vi.fn()
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(response("Bad Gateway", 502, false))
      .mockResolvedValueOnce(response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    const clock = fakeClock();
    await expect(apiOp("/interactions/i1/resolve", { method: "POST", opId: OP, json: { answer: "yes" } }, clock)).resolves.toEqual({ ok: true });
    expect(fetcher).toHaveBeenCalledTimes(3);
    const ids = fetcher.mock.calls.map((c) => (c[1].headers as Headers).get(OP_HEADER));
    expect(ids).toEqual([OP, OP, OP]);
    expect(fetcher.mock.calls.every((c) => c[1].body === JSON.stringify({ answer: "yes" }))).toBe(true);
    expect(clock.waits).toEqual([500, 1000]);
  });

  it("makes one op id when none is given and keeps it for its retries", async () => {
    const fetcher = vi.fn().mockRejectedValueOnce(new TypeError("Failed to fetch")).mockResolvedValueOnce(response({}));
    vi.stubGlobal("fetch", fetcher);
    await apiOp("/sessions/m/s/interrupt", { method: "POST" }, fakeClock());
    const [a, b] = fetcher.mock.calls.map((c) => (c[1].headers as Headers).get(OP_HEADER));
    expect(a).toMatch(/^[0-9a-f-]{36}$/);
    expect(b).toBe(a);
  });

  it("gives up after a minute with the last error", async () => {
    const fetcher = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    vi.stubGlobal("fetch", fetcher);
    const clock = fakeClock();
    await expect(apiOp("/x", { method: "POST", opId: "5d1e2f3a-6b7c-4d8e-9f0a-1b2c3d4e5f6a" }, clock)).rejects.toBeInstanceOf(TypeError);
    expect(clock.now()).toBe(OP_RETRY_MS);
    expect(clock.waits.slice(0, 4)).toEqual([500, 1000, 2000, 4000]);
    expect(new Set(fetcher.mock.calls.map((c) => (c[1].headers as Headers).get(OP_HEADER)))).toEqual(new Set(["5d1e2f3a-6b7c-4d8e-9f0a-1b2c3d4e5f6a"]));
  });

  it("keeps trying past the minute while the hub is away, and goes once it is back (#4174)", async () => {
    // the hub redeploying for three minutes: the socket is down until then, the request fails until then
    const back = 180_000;
    const clock = fakeClock((t) => t >= back);
    const run = vi.fn(async () => {
      if (clock.now() < back) throw new TypeError("Failed to fetch");
      return "sent";
    });
    await expect(untilDelivered(run, clock)).resolves.toBe("sent");
    expect(clock.now()).toBeGreaterThanOrEqual(back);
    expect(clock.now()).toBeLessThan(back + 5_000);
  });

  it("gives up while the hub is away once the restart window is over", async () => {
    const clock = fakeClock(() => false);
    const run = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(untilDelivered(run, clock)).rejects.toBeInstanceOf(TypeError);
    expect(clock.now()).toBe(OP_OFFLINE_MS);
  });

  it("gives a whole minute again once the hub is back, when it still answers 502 for a while", async () => {
    // away 200 s, then the socket is open but a proxy answers 502 for another 30 s
    const clock = fakeClock((t) => t >= 200_000);
    const run = vi.fn(async () => {
      const t = clock.now();
      if (t < 200_000) throw new TypeError("Failed to fetch");
      if (t < 230_000) throw new ApiError(502, "HTTP 502", { error: "<html>" }, false);
      return "sent";
    });
    await expect(untilDelivered(run, clock)).resolves.toBe("sent");
  });

  it("keeps the minute while the socket is open (a proxy answering 502 to a hub that is there)", async () => {
    const clock = fakeClock(() => true);
    const run = vi.fn().mockRejectedValue(new ApiError(502, "HTTP 502", { error: "<html>" }, false));
    await expect(untilDelivered(run, clock)).rejects.toBeInstanceOf(ApiError);
    expect(clock.now()).toBe(OP_RETRY_MS);
  });

  it("does not retry an answer from the hub", async () => {
    const run = vi.fn().mockRejectedValue(new ApiError(409, "draft", { code: "input_draft" }));
    const clock = fakeClock();
    await expect(untilDelivered(run, clock)).rejects.toBeInstanceOf(ApiError);
    expect(run).toHaveBeenCalledTimes(1);
    expect(clock.waits).toEqual([]);
  });
});

describe("op ids the hub takes (#1651)", () => {
  it("are UUIDs even where crypto.randomUUID is missing (a hub opened over plain http)", async () => {
    const { uuid, isUUID } = await import("./format");
    const real = globalThis.crypto;
    vi.stubGlobal("crypto", { getRandomValues: (a: Uint8Array) => real.getRandomValues(a) });
    const ids = Array.from({ length: 50 }, () => uuid());
    expect(ids.every((id) => /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(id))).toBe(true);
    expect(new Set(ids).size).toBe(50);
    expect(isUUID("18f2a3b4c5d-9e8f7a6b5c4d")).toBe(false);
  });

  it("an id that is no UUID (an old outbox entry) goes without the header instead of being refused", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    await apiOp("/sessions/m/s/messages", { method: "POST", opId: "18f2a3b4c5d-9e8f7a6b5c4d", json: { text: "hi" } }, fakeClock());
    expect((fetcher.mock.calls[0][1].headers as Headers).has(OP_HEADER)).toBe(false);
  });
});
