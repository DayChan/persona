import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, handleUnauthorized } from "./api";
import { pair } from "./auth";
import { storeCredentials, storedCredentials } from "./compatibility";

// Exercise the real REST client and credential writer: a successful /machines check does not mean the
// one-time pairing code can still be exchanged, and its 401 must not sign out an existing credential.
let stored: Record<string, string>;
const writes = vi.fn();
const unauthorized = vi.fn();
const fetcher = vi.fn<typeof fetch>();

beforeEach(() => {
  stored = {};
  writes.mockClear();
  unauthorized.mockClear();
  fetcher.mockReset();
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => stored[key] ?? null,
    setItem: (key: string, value: string) => { writes(key, value); stored[key] = value; },
    removeItem: (key: string) => { writes(key); delete stored[key]; },
    get length() { return Object.keys(stored).length; },
    key: (index: number) => Object.keys(stored)[index] ?? null,
  });
  vi.stubGlobal("navigator", { userAgent: "" });
  vi.stubGlobal("fetch", fetcher);
  handleUnauthorized(unauthorized);
  fetcher.mockResolvedValueOnce(Response.json({ machines: [] }));
});

afterEach(() => {
  handleUnauthorized(() => {});
  vi.unstubAllGlobals();
});

function requests(token: string) {
  expect(fetcher.mock.calls.map(([url, init]) => [url, init?.method ?? "GET", new Headers(init?.headers).get("Authorization")])).toEqual([
    ["/api/v1/machines", "GET", `Bearer ${token}`],
    ["/api/v1/auth/tokens", "POST", `Bearer ${token}`],
  ]);
}

describe.each(["jp_example", "synthetic-master-key"])("pairing exchange for %s", (token) => {
  it.each([401, 403, 429, 500, 502])("propagates HTTP %s and preserves existing credentials", async (status) => {
    storeCredentials("jd_previous", "device-previous");
    const before = { ...stored };
    writes.mockClear();
    fetcher.mockResolvedValueOnce(Response.json({ error: "exchange rejected", code: "exchange_failed" }, { status }));

    const error = await pair(token).catch((e: unknown) => e);

    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status, message: "exchange rejected", code: "exchange_failed" });
    requests(token);
    expect(stored).toEqual(before);
    expect(writes).not.toHaveBeenCalled();
    expect(unauthorized).not.toHaveBeenCalled();
  });

  it("does not save a rejected credential when signed out", async () => {
    fetcher.mockResolvedValueOnce(Response.json({ error: "invalid token" }, { status: 401 }));
    await expect(pair(token)).rejects.toMatchObject({ status: 401 });
    expect(storedCredentials()).toEqual({ token: "", tokenId: "" });
    expect(writes).not.toHaveBeenCalled();
  });

  it.each([new TypeError("Failed to fetch"), new DOMException("Aborted", "AbortError"), new Error("unexpected exchange failure"), { status: 404 }])("propagates transport and non-API errors without saving: %s", async (error) => {
    storeCredentials("jd_previous", "device-previous");
    const before = { ...stored };
    writes.mockClear();
    fetcher.mockRejectedValueOnce(error);
    await expect(pair(token)).rejects.toBe(error);
    expect(stored).toEqual(before);
    expect(writes).not.toHaveBeenCalled();
    expect(unauthorized).not.toHaveBeenCalled();
  });

  it("keeps the legacy credential only when the exchange endpoint returns 404", async () => {
    storeCredentials("jd_previous", "device-previous");
    fetcher.mockResolvedValueOnce(new Response("404 page not found", { status: 404 }));
    await pair(token);
    requests(token);
    expect(storedCredentials()).toEqual({ token, tokenId: "" });
  });

  it("exchanges a pasted link and replaces the previous browser device", async () => {
    storeCredentials("jd_previous", "device-previous");
    fetcher.mockResolvedValueOnce(Response.json({ token: "jd_browser", id: "device-browser" }));
    await pair(`https://hub.example/#token=${token}`);
    requests(token);
    expect(JSON.parse(fetcher.mock.calls[1][1]?.body as string)).toMatchObject({ kind: "web", replaces: "device-previous" });
    expect(storedCredentials()).toEqual({ token: "jd_browser", tokenId: "device-browser" });
  });
});

it("checks and saves a device token directly without exchanging it", async () => {
  storeCredentials("jd_previous", "device-previous");
  await pair("jd_browser");
  expect(fetcher).toHaveBeenCalledTimes(1);
  expect(storedCredentials()).toEqual({ token: "jd_browser", tokenId: "" });
});
