import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./error-reports", async (original) => ({ ...(await original<typeof import("./error-reports")>()), requestCrumb: vi.fn() }));
vi.mock("./edge-auth", async (original) => ({ ...(await original<typeof import("./edge-auth")>()), reloadForSignIn: vi.fn() }));

import { api } from "./api";
import { reloadForSignIn } from "./edge-auth";
import { requestCrumb } from "./error-reports";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.mocked(requestCrumb).mockClear();
  vi.mocked(reloadForSignIn).mockClear();
});

// docs/protocol/protocol.md "Inbox validator": a 304 to a request that sent If-None-Match keeps what the caller has.
describe("validators", () => {
  it("takes a 304 to a request with If-None-Match: no error, no breadcrumb, no sign-in reload", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 304, headers: { ETag: 'W/"i1-ab"' } })));
    const seen: [string | null, number][] = [];
    const r = await api("/inbox?sources=all", { token: "test-token", headers: { "If-None-Match": 'W/"i1-ab"' }, onResponse: (h, status) => seen.push([h.get("ETag"), status]) });
    expect(r).toEqual({});
    expect(seen).toEqual([['W/"i1-ab"', 304]]);
    expect(requestCrumb).not.toHaveBeenCalled();
    expect(reloadForSignIn).not.toHaveBeenCalled();
  });

  it("still reads a 3xx to a request without one as the sign-in layer's", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(null, { status: 304 })));
    await expect(api("/inbox?sources=all", { token: "test-token", timeout: 0 })).rejects.toMatchObject({ status: 304 });
    expect(reloadForSignIn).toHaveBeenCalledTimes(1);
    expect(requestCrumb).toHaveBeenCalledWith("GET", "/inbox?sources=all", 304);
  });

  it("passes the status of a full answer too", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response('{"items":[]}', { status: 200, headers: { ETag: 'W/"i1-cd"' } })));
    const seen: [string | null, number][] = [];
    expect(await api("/inbox?sources=all", { token: "test-token", headers: { "If-None-Match": 'W/"i1-"' }, onResponse: (h, status) => seen.push([h.get("ETag"), status]) })).toEqual({ items: [] });
    expect(seen).toEqual([['W/"i1-cd"', 200]]);
  });
});
