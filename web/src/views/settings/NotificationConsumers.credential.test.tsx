// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import type { NotifyPrefs } from "../../lib/notify";
import { ManagersSettings } from "./ManagersSettings";
import { NotificationSettings } from "./NotificationSettings";

type Request = {
  method: string;
  authorization: string | null;
  body?: Partial<NotifyPrefs>;
  signal?: AbortSignal | null;
  resolve: (response: Response) => void;
};
const requests: Request[] = [];
const previous: NotifyPrefs = { input: false, done: "always", muted_managers: ["example-manager"] };
const current: NotifyPrefs = { input: true, done: "off", muted_managers: ["example-other"] };
let root: Root;
let host: HTMLDivElement;

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  localStorage.clear();
  requests.length = 0;
  setToken("test-token-a");
  await initI18n("en");
  vi.stubGlobal("fetch", vi.fn((input: unknown, init?: RequestInit) => {
    if (String(input).endsWith("/managers")) {
      return Promise.resolve(new Response(JSON.stringify({ default: "example-manager", managers: [{ id: "example-manager", name: "Example manager", default: true, online: true, host: "example-computer", os: "linux" }] })));
    }
    if (!String(input).endsWith("/prefs")) throw new Error(`Unexpected synthetic request: ${String(input)}`);
    return new Promise<Response>((resolve, reject) => {
      const signal = init?.signal;
      const aborted = () => reject(new DOMException("Synthetic fetch aborted", "AbortError"));
      if (signal?.aborted) aborted();
      else signal?.addEventListener("abort", aborted, { once: true });
      requests.push({ method: init?.method ?? "GET", authorization: new Headers(init?.headers).get("Authorization"), body: init?.body ? JSON.parse(String(init.body)) : undefined, signal, resolve });
    });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  setToken("");
  await act(async () => {});
  vi.unstubAllGlobals();
  localStorage.clear();
  await initI18n("zh-Hans");
});

const answer = (index: number, prefs: NotifyPrefs, status = 200) => act(async () => requests[index].resolve(new Response(JSON.stringify(prefs), { status })));
const replaceFromAnotherTab = () => act(async () => {
  // App's authed state can keep the same view mounted; notify observes the browser's storage event.
  localStorage.setItem("persona.token", "test-token-b");
  window.dispatchEvent(new StorageEvent("storage", { key: "persona.token" }));
});
const inputSwitch = () => host.querySelector<HTMLButtonElement>('[role="switch"][aria-label="When something needs you"]');
const selectedDone = () => host.querySelector('[role="radiogroup"][aria-label="When an agent finishes"] [aria-checked="true"]')?.textContent;
const managerSwitch = () => document.body.querySelector<HTMLButtonElement>('[role="switch"][aria-label="Notifications"]');

// No API/notify/component mocks and no manual loadNotifyPrefs(): each new read must come from the mounted consumer.
describe("mounted notification consumers follow credential generation", () => {
  it("restarts a pending initial notification GET after another tab replaces the token", async () => {
    await act(async () => root.render(<NotificationSettings />));
    expect(requests.map((request) => request.authorization)).toEqual(["Bearer test-token-a"]);
    await replaceFromAnotherTab();
    expect(requests[0].signal?.aborted).toBe(true);
    expect(requests.map((request) => request.authorization)).toEqual(["Bearer test-token-a", "Bearer test-token-b"]);
    expect(inputSwitch()).toBeNull();
    await answer(1, current);
    expect(inputSwitch()?.getAttribute("aria-checked")).toBe("true");
    expect(selectedDone()).toBe("Don't notify");
    expect(host.textContent).not.toContain("Couldn't load");
  });

  it("hides a loaded login's controls while automatically reading the next login's settings", async () => {
    await act(async () => root.render(<NotificationSettings />));
    await answer(0, previous);
    expect(inputSwitch()?.getAttribute("aria-checked")).toBe("false");
    expect(selectedDone()).toBe("Every time");
    await act(async () => setToken("test-token-b"));
    expect(requests).toHaveLength(2);
    expect(requests[1].authorization).toBe("Bearer test-token-b");
    expect(inputSwitch()).toBeNull();
    await answer(1, current);
    expect(inputSwitch()?.getAttribute("aria-checked")).toBe("true");
    expect(selectedDone()).toBe("Don't notify");
  });

  it("recovers a surviving manager control and saves against the new login's mute list", async () => {
    await act(async () => root.render(<ManagersSettings />));
    const manager = [...host.querySelectorAll("button")].find((button) => button.textContent?.includes("Example manager"));
    expect(manager).toBeDefined();
    await act(async () => manager!.click());
    expect(requests).toHaveLength(1);
    await answer(0, previous);
    expect(managerSwitch()?.getAttribute("aria-checked")).toBe("false");
    await replaceFromAnotherTab();
    expect(requests).toHaveLength(2);
    expect(requests[1].authorization).toBe("Bearer test-token-b");
    expect(managerSwitch()).toBeNull();
    await answer(1, current);
    expect(managerSwitch()?.getAttribute("aria-checked")).toBe("true");
    await act(async () => managerSwitch()!.click());
    expect(requests[2]).toMatchObject({ method: "PUT", authorization: "Bearer test-token-b", body: { muted_managers: ["example-other", "example-manager"] } });
    await answer(2, { ...current, muted_managers: ["example-other", "example-manager"] });
    expect(managerSwitch()?.getAttribute("aria-checked")).toBe("false");
  });

  it("shows a new login's read failure and retries through the mounted notification control", async () => {
    await act(async () => root.render(<NotificationSettings />));
    await replaceFromAnotherTab();
    expect(requests).toHaveLength(2);
    await answer(1, current, 500);
    const retry = [...host.querySelectorAll("button")].find((button) => button.textContent === "Retry");
    expect(retry).toBeDefined();
    await act(async () => retry!.click());
    expect(requests[2].authorization).toBe("Bearer test-token-b");
    await answer(2, current);
    expect(selectedDone()).toBe("Don't notify");
  });
});
