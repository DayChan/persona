// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../../lib/api", async (original) => ({ ...(await original<typeof import("../../lib/api")>()), api: vi.fn() }));
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { t } from "../../i18n";
import { api } from "../../lib/api";
import type { AuthPrompt, ExtensionAuth } from "../../lib/extension-auth";
import { toast } from "../../ui/atoms";
import { AuthGroup, SignInDialog } from "./ExtensionAuth";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const transport = vi.mocked(api);
const fields = (step = "code"): AuthPrompt => ({ step, fields: [{ name: "code", label: "Verification code" }] });
const waiting: AuthPrompt = { step: "wait", kind: "device_code", user_code: "SYNTHETIC", interval: 1 };
const deferred = () => {
  let resolve!: (value: AuthPrompt) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<AuthPrompt>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

let host: HTMLDivElement;
let root: Root;
const done = vi.fn();
const closed = vi.fn();
const dialog = () => document.querySelector<HTMLElement>('[role="dialog"]');
const input = () => dialog()?.querySelector("input");
const button = (label: string) => [...dialog()!.querySelectorAll("button")].find((b) => b.textContent === label)!;
const next = () => button(t("settings-extensionauth.next"));
const starts = () => transport.mock.calls.filter(([, init]) => (init?.json as { step: string }).step === "");
const show = (id = "example-a", onDone = done, onClose = closed, title = "Example sign-in") => act(async () => {
  root.render(<SignInDialog id={id} title={title} onDone={onDone} onClose={onClose} />);
});
const group = (message: string, onChange = done, state: ExtensionAuth["state"] = "pending") => act(async () => {
  root.render(<AuthGroup id="example-a" auth={{ title: "Example sign-in", state, message }} enabled start onChange={onChange} />);
});
const type = (value: string) => act(() => {
  const element = input()!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(element, value);
  element.dispatchEvent(new Event("input", { bubbles: true }));
});
const submit = () => dialog()!.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });

beforeEach(() => {
  transport.mockReset();
  // Every authentication request uses this synthetic transport; no fetch or hub connection is made.
  transport.mockResolvedValue(fields());
  vi.mocked(toast).mockClear();
  done.mockReset();
  closed.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
});

describe("extension sign-in lifecycle", () => {
  it("keeps one startup and the entered fields across real AuthGroup status-only rerenders", async () => {
    await group("Waiting");
    type("synthetic-code");
    await group("Waiting for a code");
    await group("Confirmation received", done, "signed_in");
    expect(starts()).toHaveLength(1);
    expect(input()?.value).toBe("synthetic-code");
    expect(next().disabled).toBe(false);
    expect(done).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it("retains a pending submission and its failed values, then completes one intentional retry", async () => {
    const pending = deferred();
    const retry = deferred();
    transport.mockResolvedValueOnce(fields()).mockReturnValueOnce(pending.promise).mockReturnValueOnce(retry.promise);
    await group("Waiting");
    type("synthetic-code");
    act(() => { next().click(); submit(); submit(); });
    const latest = vi.fn();
    await group("Verifying", latest);
    expect(transport).toHaveBeenCalledTimes(2);
    expect(input()?.disabled).toBe(true);
    await act(async () => pending.reject(new Error("Synthetic verification failure")));
    expect(dialog()?.querySelector('[role="alert"]')?.textContent).toBe("Synthetic verification failure");
    expect(input()?.value).toBe("synthetic-code");
    expect(next().disabled).toBe(false);
    act(() => { submit(); next()?.click(); submit(); });
    expect(transport).toHaveBeenCalledTimes(3);
    expect(transport.mock.calls[2]).toEqual(transport.mock.calls[1]);
    await act(async () => retry.resolve({ done: true }));
    expect(latest).toHaveBeenCalledTimes(1);
    expect(done).not.toHaveBeenCalled();
    expect(dialog()).toBeNull();
    expect(toast).toHaveBeenCalledExactlyOnceWith(t("settings-extensionauth.signed_in"), "ok");
  });

  it("uses the latest completion callbacks without restarting an unresolved startup", async () => {
    const pending = deferred();
    transport.mockReturnValue(pending.promise);
    await show();
    const latestDone = vi.fn();
    const latestClose = vi.fn();
    await show("example-a", latestDone, latestClose, "Updated title");
    expect(starts()).toHaveLength(1);
    await act(async () => pending.resolve({ done: true, account: "example account" }));
    expect(latestDone).toHaveBeenCalledTimes(1);
    expect(latestClose).toHaveBeenCalledTimes(1);
    expect(done).not.toHaveBeenCalled();
    expect(closed).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledExactlyOnceWith(t("settings-extensionauth.signed_in_as", { account: "example account" }), "ok");
  });

  it("sends exactly one new empty step for an explicit Restart", async () => {
    const pending = deferred();
    transport.mockResolvedValueOnce({ step: "unsupported", kind: "voice" }).mockReturnValue(pending.promise);
    await group("Waiting");
    const restart = button(t("settings-loginsteps.restart"));
    expect(restart).toBeDefined();
    act(() => { restart.click(); restart.click(); });
    await group("Restarting");
    expect(starts()).toHaveLength(2);
    expect(transport.mock.calls[1][1]?.json).toEqual({ step: "", values: {} });
    await act(async () => pending.resolve(fields("new-code")));
    expect(input()?.value).toBe("");
    type("new-synthetic-code");
    expect(next().disabled).toBe(false);
  });

  it.each(["prompt", "success", "error"] as const)("ignores late %s and finally from a retired identity, including A → B → A", async (result) => {
    const retired = deferred();
    const current = deferred();
    transport.mockReturnValueOnce(retired.promise).mockResolvedValueOnce(fields("b-code")).mockReturnValueOnce(current.promise);
    await show("example-a");
    await show("example-b");
    type("B-only value");
    await show("example-a");
    expect(input()).toBeNull();
    await act(async () => {
      if (result === "error") retired.reject(new Error("Retired failure"));
      else retired.resolve(result === "success" ? { done: true } : fields("retired-code"));
    });
    expect(dialog()?.textContent).toContain(t("settings-extensionauth.starting"));
    expect(input()).toBeNull();
    expect(dialog()?.querySelector('[role="alert"]')).toBeNull();
    expect(done).not.toHaveBeenCalled();
    expect(closed).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
    expect(starts()).toHaveLength(3);
    await act(async () => current.resolve(fields("current-code")));
    expect(input()?.value).toBe("");
    type("current-synthetic-code");
    expect(next().disabled).toBe(false);
  });

  it("starts only once under StrictMode effect replay", async () => {
    await act(async () => root.render(<StrictMode><AuthGroup id="example-a" auth={{ title: "Example sign-in", state: "pending" }} enabled start onChange={done} /></StrictMode>));
    expect(starts()).toHaveLength(1);
    expect(input()).not.toBeNull();
  });

  it("keeps a same-step field value and clears it only when a new step arrives", async () => {
    transport.mockResolvedValueOnce(fields()).mockResolvedValueOnce({ ...fields(), error: "Synthetic rejected code" }).mockResolvedValueOnce(fields("next-code"));
    await show();
    type("synthetic-code");
    await act(async () => submit());
    expect(input()?.value).toBe("synthetic-code");
    expect(dialog()?.querySelector('[role="alert"]')?.textContent).toBe("Synthetic rejected code");
    await act(async () => submit());
    expect(input()?.value).toBe("");
    expect(next().disabled).toBe(true);
  });

  it("allows a failed startup to be cancelled and intentionally reopened without an automatic retry", async () => {
    transport.mockRejectedValueOnce(new Error("Synthetic startup failure")).mockResolvedValue({ done: true });
    await group("Waiting");
    expect(dialog()?.querySelector('[role="alert"]')?.textContent).toBe("Synthetic startup failure");
    await group("Still waiting");
    expect(starts()).toHaveLength(1);
    act(() => button(t("common.cancel")).click());
    expect(dialog()).toBeNull();
    await act(async () => [...host.querySelectorAll("button")].find((b) => b.textContent === t("lib-extension-auth.sign_in"))!.click());
    expect(starts()).toHaveLength(2);
    expect(done).toHaveBeenCalledTimes(1);
    expect(dialog()).toBeNull();
  });

  it.each(["success", "error"] as const)("ignores a retired submission's late %s and finally while the next identity is starting", async (result) => {
    const retired = deferred();
    const current = deferred();
    transport.mockResolvedValueOnce(fields()).mockReturnValueOnce(retired.promise).mockReturnValueOnce(current.promise);
    await show();
    type("retired-synthetic-code");
    act(() => submit());
    await show("example-b");
    await act(async () => {
      if (result === "error") retired.reject(new Error("Retired submission failure"));
      else retired.resolve({ done: true });
    });
    expect(dialog()?.textContent).toContain(t("settings-extensionauth.starting"));
    expect(dialog()?.querySelector('[role="alert"]')).toBeNull();
    expect(done).not.toHaveBeenCalled();
    expect(closed).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
    await act(async () => current.resolve(fields()));
    expect(input()?.value).toBe("");
    type("current-synthetic-code");
    expect(next().disabled).toBe(false);
  });

  it("does not submit or complete again after success while its parent finishes closing", async () => {
    transport.mockResolvedValueOnce(fields()).mockResolvedValue({ done: true });
    await show();
    type("synthetic-code");
    await act(async () => submit());
    act(() => { submit(); submit(); });
    await show();
    expect(transport).toHaveBeenCalledTimes(2);
    expect(done).toHaveBeenCalledTimes(1);
    expect(closed).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledTimes(1);
  });

  it("polls through a transient failure and uses the latest callbacks without resetting the timer", async () => {
    vi.useFakeTimers();
    const completion = deferred();
    transport.mockResolvedValueOnce(waiting).mockRejectedValueOnce(new Error("Synthetic polling failure")).mockReturnValue(completion.promise);
    await show();
    await advance(500);
    const latestDone = vi.fn();
    const latestClose = vi.fn();
    await show("example-a", latestDone, latestClose);
    expect(starts()).toHaveLength(1);
    await advance(500);
    expect(transport).toHaveBeenCalledTimes(2);
    expect(transport.mock.calls[1][1]?.json).toEqual({ step: "wait", values: {} });
    expect(dialog()?.querySelector('[role="alert"]')?.textContent).toBe("Synthetic polling failure");
    await advance(1000);
    expect(transport).toHaveBeenCalledTimes(3);
    const newestDone = vi.fn();
    const newestClose = vi.fn();
    await show("example-a", newestDone, newestClose);
    await act(async () => completion.resolve({ done: true }));
    expect(newestDone).toHaveBeenCalledTimes(1);
    expect(newestClose).toHaveBeenCalledTimes(1);
    expect(latestDone).not.toHaveBeenCalled();
    expect(latestClose).not.toHaveBeenCalled();
    expect(done).not.toHaveBeenCalled();
    expect(closed).not.toHaveBeenCalled();
    await advance(10_000);
    expect(transport).toHaveBeenCalledTimes(3);
  });

  it("polls updated intervals and stops when a waiting step becomes fields", async () => {
    vi.useFakeTimers();
    transport.mockResolvedValueOnce(waiting).mockResolvedValueOnce({ ...waiting, interval: 2 }).mockResolvedValueOnce(fields("verification"));
    await show();
    await advance(1000);
    expect(transport).toHaveBeenCalledTimes(2);
    await advance(1999);
    expect(transport).toHaveBeenCalledTimes(2);
    await advance(1);
    expect(transport).toHaveBeenCalledTimes(3);
    expect(input()?.value).toBe("");
    type("synthetic-code");
    await advance(10_000);
    expect(transport).toHaveBeenCalledTimes(3);
    expect(input()?.value).toBe("synthetic-code");
  });

  it.each(["identity", "cancel", "unmount"] as const)("cleans up a scheduled poll on %s before it sends", async (action) => {
    vi.useFakeTimers();
    transport.mockResolvedValueOnce(waiting).mockResolvedValue(fields());
    await show();
    await advance(500);
    if (action === "identity") await show("example-b");
    else if (action === "cancel") act(() => button(t("common.cancel")).click());
    else act(() => root.render(null));
    await advance(10_000);
    expect(transport).toHaveBeenCalledTimes(action === "identity" ? 2 : 1);
  });

  describe.each(["success", "prompt", "error"] as const)("late polling %s", (result) => {
    it.each(["identity", "cancel", "unmount"] as const)("retires in-flight polling on %s and never schedules another retired poll", async (action) => {
      vi.useFakeTimers();
      const pending = deferred();
      transport.mockResolvedValueOnce(waiting).mockReturnValueOnce(pending.promise).mockResolvedValue(fields());
      await show();
      await advance(1000);
      expect(transport).toHaveBeenCalledTimes(2);
      if (action === "identity") await show("example-b");
      else if (action === "cancel") act(() => button(t("common.cancel")).click());
      else act(() => root.render(null));
      await act(async () => {
        if (result === "error") pending.reject(new Error("Retired polling failure"));
        else pending.resolve(result === "success" ? { done: true } : waiting);
      });
      expect(done).not.toHaveBeenCalled();
      expect(closed).toHaveBeenCalledTimes(action === "cancel" ? 1 : 0);
      expect(toast).not.toHaveBeenCalled();
      expect(dialog()?.querySelector('[role="alert"]') ?? null).toBeNull();
      if (action === "identity") expect(input()?.value).toBe("");
      const count = transport.mock.calls.length;
      await advance(10_000);
      expect(transport).toHaveBeenCalledTimes(count);
    });
  });
});
