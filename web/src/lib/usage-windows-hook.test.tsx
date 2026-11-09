// @vitest-environment jsdom
// useUsageWindows with replies that come back out of order: the tab coming back to the front starts a second
// request while the first is still out, and only the newest one may decide what the bars show.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { UsageAccount } from "./usage-windows";

interface Deferred {
  resolve: (v: unknown) => void;
  reject: (e: unknown) => void;
}
const calls: Deferred[] = [];
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  api: () =>
    new Promise((resolve, reject) => {
      calls.push({ resolve, reject });
    }),
}));

import { useUsageWindows } from "./usage-windows";
import { ApiError } from "./api";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const account = (id: string): UsageAccount => ({ id, provider: "claude", name: id, windows: [], sampled_at: new Date().toISOString() });

function Bars() {
  const accounts = useUsageWindows();
  return <p>{accounts.map((a) => a.id).join(",")}</p>;
}

let root: Root | undefined;
let host: HTMLDivElement;
let visibility: DocumentVisibilityState = "visible";

beforeEach(() => {
  calls.length = 0;
  visibility = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
  host = document.createElement("div");
  root = createRoot(host);
  act(() => root!.render(<Bars />));
});

afterEach(() => {
  act(() => root?.unmount());
  root = undefined;
  vi.restoreAllMocks();
});

const backToFront = () => act(() => document.dispatchEvent(new Event("visibilitychange")));
const settle = async (f: () => void) => act(async () => f());

describe("useUsageWindows", () => {
  it("keeps the newer reply when the older one lands after it", async () => {
    expect(calls).toHaveLength(1); // the mount's request, still out
    backToFront();
    expect(calls).toHaveLength(2);
    await settle(() => calls[1].resolve({ accounts: [account("B")] }));
    expect(host.textContent).toBe("B");
    await settle(() => calls[0].resolve({ accounts: [account("A")] }));
    expect(host.textContent).toBe("B");
  });

  it("keeps the bars when an older request fails after a newer one answered", async () => {
    backToFront();
    await settle(() => calls[1].resolve({ accounts: [account("B")] }));
    await settle(() => calls[0].reject(new Error("hub unreachable")));
    expect(host.textContent).toBe("B");
  });

  it("clears the bars only on a 404: an older hub has no such route, anything else keeps them", async () => {
    await settle(() => calls[0].resolve({ accounts: [account("A")] }));
    expect(host.textContent).toBe("A");
    backToFront();
    await settle(() => calls[1].reject(new TypeError("flaky link")));
    expect(host.textContent).toBe("A");
    backToFront();
    await settle(() => calls[2].reject(new ApiError(404, "no such route", {})));
    expect(host.textContent).toBe("");
  });

  it("drops a reply that lands after unmount", async () => {
    act(() => root!.unmount());
    root = undefined;
    await settle(() => calls[0].resolve({ accounts: [account("A")] }));
    expect(host.textContent).toBe("");
  });

  it("does not ask while the tab is hidden", () => {
    visibility = "hidden";
    backToFront();
    expect(calls).toHaveLength(1);
  });
});
