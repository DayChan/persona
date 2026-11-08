// @vitest-environment jsdom
// A failed settings read can be retried in place, including a second failure and a slow response (#4493).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

type Pending = { resolve: (value: unknown) => void; reject: (error: unknown) => void };
const hub = vi.hoisted(() => ({ path: "", pending: [] as Pending[] }));
vi.mock("../lib/api", async (original) => ({
  ...(await original<typeof import("../lib/api")>()),
  api: (path: string) => {
    if (path === hub.path) return new Promise((resolve, reject) => hub.pending.push({ resolve, reject }));
    if (path === "/managers") return Promise.reject(new ApiError(404, "not found", {}));
    // The hub page's download mirrors load on their own; answer them so only the updater is under test.
    if (path.startsWith("/extensions/-/market-settings")) return Promise.resolve({ registry_mirror: "", catalog_mirror: "" });
    return Promise.resolve(path === "/looks" ? { looks: [], current: {} } : {});
  },
}));

import { ApiError } from "../lib/api";
import { resetManagerChoice } from "../lib/manager-choice";
import { resetRoster } from "../lib/managers";
import { settleRosterDefault } from "../lib/manager-choice";
import { t } from "../i18n";
import { Settings } from "./Settings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  localStorage.clear();
  resetManagerChoice();
  resetRoster();
  settleRosterDefault();
  hub.pending = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const retry = () => [...host.querySelectorAll("button")].find((button) => button.textContent === "重试");
const fail = (index: number, error: Error) => act(async () => hub.pending[index].reject(error));
const answer = (index: number, value: unknown) => act(async () => hub.pending[index].resolve(value));
async function render(tab: string, path: string) {
  hub.path = path;
  history.replaceState(null, "", `/settings?tab=${tab}`);
  await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
}

describe("settings read retries", () => {
  it.each([
    { tab: "devices", path: "/auth/tokens", keepsError: true, result: { tokens: [{ id: "browser", name: "Example browser", kind: "web", role: "full" }], current: "browser" }, content: "Example browser" },
    { tab: "models", path: "/launchers", keepsError: false, result: { launchers: [{ name: "example", display_name: "Example provider", source: "persona" }] }, content: "Example provider" },
    { tab: "hub", path: "/hub/update", keepsError: false, result: { version: "1.2.3", build: 123, channel: "release", can_update: true, state: "idle", busy: [], auto: false, auto_notice: false, confirmed: true }, content: "1.2.3" },
    { tab: "assistant", path: "/assistant", keepsError: false, result: { machine_id: "example-computer", notes_effective: "Example notes" }, content: "example-computer" },
  ])("retries $tab without changing categories and can recover after another failure", async ({ tab, path, keepsError, result, content }) => {
    await render(tab, path);
    expect(host.textContent).toContain(t("common.loading"));
    expect(retry()).toBeUndefined();
    expect(hub.pending).toHaveLength(1);

    await fail(0, new Error("hub unavailable"));
    expect(host.textContent).toContain("hub unavailable");
    expect(retry()).toBeDefined();
    expect(retry()!.disabled).toBe(false);

    await act(async () => retry()!.click());
    expect(hub.pending).toHaveLength(2);
    if (keepsError) {
      expect(retry()!.disabled).toBe(true);
      await act(async () => retry()!.click());
      expect(hub.pending).toHaveLength(2);
    } else {
      expect(retry()).toBeUndefined();
      expect(host.textContent).not.toContain("hub unavailable");
      expect(host.textContent).toContain(t("common.loading"));
    }

    await fail(1, new Error("still unavailable"));
    expect(host.textContent).toContain("still unavailable");
    expect(host.textContent).not.toContain("hub unavailable");
    expect(retry()!.disabled).toBe(false);

    await act(async () => retry()!.click());
    expect(hub.pending).toHaveLength(3);
    await answer(2, result);
    expect(host.textContent).toContain(content);
    expect(host.textContent).not.toContain("still unavailable");
    expect(retry()).toBeUndefined();
    expect(location.search).toBe(`?tab=${tab}`);
  });

  it("keeps the older hub's unsupported message when its update endpoint is missing", async () => {
    await render("hub", "/hub/update");
    await fail(0, new ApiError(404, "not found", {}));
    expect(host.textContent).toContain(t("settings-hubsettings.unsupported"));
    expect(retry()).toBeUndefined();
    expect(host.textContent).not.toContain(t("common.loading"));
  });
});
