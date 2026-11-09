// Disconnect this browser (#1873): what the hub's answer to the revoke means, and the id a failed one leaves for the
// next sign-in's `replaces`.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./store", () => ({ reset: vi.fn() }));

import { ApiError, errorText, HubTimeoutError } from "./api";
import { pair, UNREVOKED_KEY } from "./auth";
import { storeCredentials, storedCredentials } from "./compatibility";
import { REVOKE_TIMEOUT_MS, revokeThisBrowser, signOutHere } from "./disconnect";

let stored: Record<string, string>;
const fetcher = vi.fn<typeof fetch>();
const calls = () => fetcher.mock.calls.map(([url, init]) => `${init?.method ?? "GET"} ${String(url)}`);

beforeEach(() => {
  stored = {};
  fetcher.mockReset();
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => stored[key] ?? null,
    setItem: (key: string, value: string) => void (stored[key] = value),
    removeItem: (key: string) => void delete stored[key],
    get length() { return Object.keys(stored).length; },
    key: (index: number) => Object.keys(stored)[index] ?? null,
  });
  vi.stubGlobal("navigator", { userAgent: "" });
  vi.stubGlobal("location", { href: "/settings" });
  vi.stubGlobal("fetch", fetcher);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("revokeThisBrowser", () => {
  it("revokes the device token this browser signed in with", async () => {
    storeCredentials("jd_browser", "dev_browser");
    fetcher.mockResolvedValueOnce(Response.json({ ok: true }));
    expect(await revokeThisBrowser()).toEqual({ ok: true });
    expect(calls()).toEqual(["DELETE /api/v1/auth/tokens/dev_browser"]);
  });

  it.each([404, 401])("counts HTTP %s as nothing valid left", async (status) => {
    storeCredentials("jd_browser", "dev_browser");
    fetcher.mockResolvedValueOnce(Response.json({ error: "no such device" }, { status }));
    expect(await revokeThisBrowser()).toEqual({ ok: true });
  });

  it.each([500, 503])("says why when the hub answers HTTP %s", async (status) => {
    storeCredentials("jd_browser", "dev_browser");
    fetcher.mockResolvedValueOnce(Response.json({ error: "no database" }, { status }));
    const r = await revokeThisBrowser();
    expect(r).toEqual({ ok: false, error: errorText(new ApiError(status, "no database", {})), id: "dev_browser" });
    expect("error" in r && r.error).toContain("no database");
  });

  it("fails when the hub can't be reached", async () => {
    storeCredentials("jd_browser", "dev_browser");
    fetcher.mockRejectedValueOnce(new TypeError("Failed to fetch"));
    expect(await revokeThisBrowser()).toEqual({ ok: false, error: errorText(new TypeError("Failed to fetch")), id: "dev_browser" });
  });

  it("gives up when the hub does not answer in time", async () => {
    vi.useFakeTimers();
    storeCredentials("jd_browser", "dev_browser");
    fetcher.mockImplementationOnce((_url, init) => new Promise((_resolve, reject) => init?.signal?.addEventListener("abort", () => reject(new DOMException("Aborted", "AbortError")))));
    const pending = revokeThisBrowser();
    await vi.advanceTimersByTimeAsync(REVOKE_TIMEOUT_MS);
    expect(await pending).toEqual({ ok: false, error: new HubTimeoutError().message, id: "dev_browser" });
  });

  it("asks the hub which credential a pasted device token is, and revokes that one", async () => {
    storeCredentials("jd_pasted");
    fetcher.mockResolvedValueOnce(Response.json({ tokens: [], current: "dev_pasted" })).mockResolvedValueOnce(Response.json({ ok: true }));
    expect(await revokeThisBrowser()).toEqual({ ok: true });
    expect(calls()).toEqual(["GET /api/v1/auth/tokens", "DELETE /api/v1/auth/tokens/dev_pasted"]);
  });

  it.each([
    ["the hub's own token", Response.json({ tokens: [], current: "" })],
    ["a pairing code an old hub kept", Response.json({ tokens: [], current: "pair_abc" })],
    ["a hub from before device tokens", new Response("404 page not found", { status: 404 })],
  ])("has nothing to revoke for %s", async (_what, answer) => {
    storeCredentials("synthetic-master-key");
    fetcher.mockResolvedValueOnce(answer);
    expect(await revokeThisBrowser()).toEqual({ ok: false, nothing: true });
    expect(calls()).toEqual(["GET /api/v1/auth/tokens"]);
  });
});

describe("signing out without the revoke", () => {
  it("keeps the id, and the next sign-in has the hub replace it", async () => {
    storeCredentials("jd_browser", "dev_browser");
    signOutHere("dev_browser");
    expect(storedCredentials()).toEqual({ token: "", tokenId: "" });
    expect(stored[UNREVOKED_KEY]).toBe("dev_browser");
    expect(location.href).toBe("/");

    fetcher.mockResolvedValueOnce(Response.json({ machines: [] })).mockResolvedValueOnce(Response.json({ token: "jd_new", id: "dev_new" }));
    await pair("jp_example");
    expect(JSON.parse(fetcher.mock.calls[1][1]?.body as string)).toMatchObject({ kind: "web", replaces: "dev_browser" });
    expect(storedCredentials()).toEqual({ token: "jd_new", tokenId: "dev_new" });
    expect(stored[UNREVOKED_KEY]).toBeUndefined();
  });

  it("keeps the id through a sign-in that fails", async () => {
    signOutHere("dev_browser");
    fetcher.mockResolvedValueOnce(Response.json({ machines: [] })).mockResolvedValueOnce(Response.json({ error: "exchange rejected" }, { status: 500 }));
    await expect(pair("jp_example")).rejects.toMatchObject({ status: 500 });
    expect(stored[UNREVOKED_KEY]).toBe("dev_browser");
  });

  it("keeps nothing after a revoke that worked", () => {
    storeCredentials("jd_browser", "dev_browser");
    signOutHere();
    expect(stored).toEqual({});
  });
});
