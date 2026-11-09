// @vitest-environment jsdom
// "Disconnect this browser" (#1873): busy while the hub revokes; a failed revoke or nothing to revoke asks first, and
// cancelling leaves this browser signed in with its 401 handling back.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Revoke } from "../lib/disconnect";

const fake = vi.hoisted(() => ({
  revoke: vi.fn<() => Promise<Revoke>>(),
  signOut: vi.fn<(unrevoked?: string) => void>(),
  handler: (() => {}) as () => void,
}));
vi.mock("../lib/disconnect", () => ({ revokeThisBrowser: () => fake.revoke(), signOutHere: (id?: string) => fake.signOut(id) }));
vi.mock("../lib/api", () => ({
  handleUnauthorized: (fn: () => void) => {
    const before = fake.handler;
    fake.handler = fn;
    return before;
  },
}));

import { t } from "../i18n";
import { useDisconnect } from "./DisconnectBrowser";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const signedOutHandler = () => {};

function Probe() {
  const d = useDisconnect();
  return (
    <>
      <button type="button" data-start onClick={d.start}>
        {d.busy ? "busy" : "idle"}
      </button>
      {d.dialog}
    </>
  );
}

const startButton = () => document.querySelector<HTMLButtonElement>("button[data-start]")!;
const button = (label: string) => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((b) => b.textContent?.includes(label));
const dialogText = () => document.querySelector('[role="dialog"]')?.textContent ?? "";

beforeEach(() => {
  fake.revoke.mockReset();
  fake.signOut.mockReset();
  fake.handler = signedOutHandler;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root.render(<Probe />));
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("useDisconnect", () => {
  it("signs out once the hub revoked the token", async () => {
    let done!: (r: Revoke) => void;
    fake.revoke.mockReturnValueOnce(new Promise((resolve) => (done = resolve)));
    act(() => startButton().click());
    expect(startButton().textContent).toBe("busy");
    expect(fake.handler).not.toBe(signedOutHandler); // a 401 meanwhile does not say "signed out"
    await act(async () => done({ ok: true }));
    expect(fake.signOut).toHaveBeenCalledWith(undefined);
    expect(dialogText()).toBe("");
  });

  it("says why a revoke failed; Try again, then Sign out anyway keeps the id", async () => {
    fake.revoke.mockResolvedValueOnce({ ok: false, error: "HTTP 503: no database", id: "dev_browser" });
    await act(async () => startButton().click());
    expect(fake.signOut).not.toHaveBeenCalled();
    expect(fake.handler).toBe(signedOutHandler);
    expect(dialogText()).toContain(t("disconnect.failed_title"));
    expect(dialogText()).toContain("HTTP 503: no database");

    fake.revoke.mockResolvedValueOnce({ ok: false, error: "HTTP 502", id: "dev_browser" });
    await act(async () => button(t("disconnect.retry"))!.click());
    expect(fake.revoke).toHaveBeenCalledTimes(2);
    expect(dialogText()).toContain("HTTP 502");

    act(() => button(t("disconnect.sign_out_anyway"))!.click());
    expect(fake.signOut).toHaveBeenCalledWith("dev_browser");
  });

  it("stays signed in when cancelled", async () => {
    fake.revoke.mockResolvedValueOnce({ ok: false, error: "HTTP 500", id: "dev_browser" });
    await act(async () => startButton().click());
    act(() => button(t("common.cancel"))!.click());
    expect(fake.signOut).not.toHaveBeenCalled();
    expect(fake.handler).toBe(signedOutHandler);
    expect(startButton().textContent).toBe("idle");
  });

  it("says when there is nothing to revoke, and signs out only when asked", async () => {
    fake.revoke.mockResolvedValueOnce({ ok: false, nothing: true });
    await act(async () => startButton().click());
    expect(dialogText()).toContain(t("disconnect.nothing_title"));
    expect(button(t("disconnect.retry"))).toBeUndefined();
    act(() => button(t("disconnect.sign_out"))!.click());
    expect(fake.signOut).toHaveBeenCalledWith("");
  });
});
