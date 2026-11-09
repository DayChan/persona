// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, credentialOwner, getToken, handleUnauthorized, onCredentialChange, setToken, subscribeCredentialOwner } from "./api";

const unauthorized = vi.fn();
let respond: (response: Response) => void;

beforeEach(() => {
  localStorage.clear();
  setToken("synthetic-current-token");
  unauthorized.mockClear();
  handleUnauthorized(unauthorized);
  vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((resolve) => { respond = resolve; })));
});

afterEach(() => {
  handleUnauthorized(() => {});
  vi.unstubAllGlobals();
});

it.each([
  { mode: "ordinary", expected: 1 },
  { mode: "ordinary-owner-change", expected: 0 },
  { mode: "ordinary-retired-generation", expected: 0 },
  { mode: "owned", expected: 1 },
  { mode: "probe", expected: 0 },
  { mode: "different-token", expected: 0 },
  { mode: "retired-generation", expected: 0 },
])("401 recovery preserves the $mode credential contract", async ({ mode, expected }) => {
  const token = getToken(), tokenOwner = credentialOwner();
  const init = mode.startsWith("ordinary") ? {} : mode === "probe" ? { token } : {
    token: mode === "different-token" ? "synthetic-probe-token" : token, tokenOwner,
  };
  const metadata = vi.fn();
  const result = api("/machines", { ...init, onResponse: metadata }).catch((error: Error) => error);
  if (mode.includes("retired-generation")) {
    setToken("synthetic-other-token");
    setToken(token);
  } else if (mode === "ordinary-owner-change") setToken("synthetic-other-token");
  respond(new Response(JSON.stringify({ error: "invalid token" }), { status: 401, headers: { "Content-Type": "application/json" } }));
  expect((await result as Error).message).toBe("invalid token");
  expect(unauthorized).toHaveBeenCalledTimes(expected);
  expect(metadata).not.toHaveBeenCalled();
});

it("adapts React subscriptions to the shared credential publication without duplicate notifications", () => {
  const consumer = vi.fn(), react = vi.fn();
  const offConsumer = onCredentialChange(consumer), offReact = subscribeCredentialOwner(react);
  try {
    setToken("synthetic-other-token");
    expect(consumer).toHaveBeenCalledExactlyOnceWith(credentialOwner());
    expect(react).toHaveBeenCalledOnce();
    setToken("synthetic-other-token");
    expect(consumer).toHaveBeenCalledOnce();
    expect(react).toHaveBeenCalledOnce();
    localStorage.setItem("persona.token", "synthetic-current-token");
    window.dispatchEvent(new StorageEvent("storage", { key: "persona.token" }));
    expect(consumer).toHaveBeenCalledTimes(2);
    expect(consumer).toHaveBeenLastCalledWith(credentialOwner());
    expect(react).toHaveBeenCalledTimes(2);
  } finally {
    offConsumer(); offReact();
  }
  setToken("synthetic-next-token");
  expect(consumer).toHaveBeenCalledTimes(2);
  expect(react).toHaveBeenCalledTimes(2);
});


it("exposes successful response metadata through the authenticated API without forwarding its callback", async () => {
  const metadata = vi.fn();
  const result = api("/machines", { onResponse: metadata });
  respond(new Response(JSON.stringify({ machines: [] }), { headers: { "X-Persona-Computer-Apps-Owner": "example-manager" } }));
  expect(await result).toEqual({ machines: [] });
  expect(metadata).toHaveBeenCalledOnce();
  expect(metadata.mock.calls[0]![0].get("X-Persona-Computer-Apps-Owner")).toBe("example-manager");
  expect(vi.mocked(fetch).mock.calls[0]![1]).not.toHaveProperty("onResponse");
});
