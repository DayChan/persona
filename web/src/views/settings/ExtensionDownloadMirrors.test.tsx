// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { TAB_INFO, openedFromList, type Tab } from "./nav";

type Pending = { path: string; method: string; json?: unknown; resolve: (value: unknown) => void; reject: (error: unknown) => void };
const pending: Pending[] = [];
const roster = vi.hoisted(() => ({ managers: [{ id: "assistant", name: "Hub manager", online: true, builtin: true }, { id: "m-two", name: "Second manager", online: true }], default: "assistant" }));
vi.mock("../../lib/managers", async (original) => ({ ...(await original<typeof import("../../lib/managers")>()), useRoster: () => roster }));
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (path: string, init?: { method?: string; json?: unknown }) => new Promise((resolve, reject) => pending.push({ path, method: init?.method ?? "GET", json: init?.json, resolve, reject })),
}));
const viewport = vi.hoisted(() => ({ phone: false }));
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ get matches() { return viewport.phone && query === "(max-width: 639px)"; }, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const { Settings } = await import("../Settings");
const { HubSettings } = await import("./HubSettings");
const { ApiError } = await import("../../lib/api");
let root: Root;
let host: HTMLDivElement;
const empty = { registry_mirror: "", catalog_mirror: "" };
const configured = { registry_mirror: "https://registry.example.com/prefix", catalog_mirror: "https://catalog.example.com/persona-extensions.json" };
const button = (key: string) => [...host.querySelectorAll("button")].find((b) => b.textContent === t(`settings-hubsettings.${key}`))!;
const mirrors = () => pending.filter((p) => p.path.includes("market-settings"));
const answer = (p: Pending, value: unknown) => act(async () => p.resolve(value));
const fail = (p: Pending, status: number) => act(async () => p.reject(new ApiError(status, "Synthetic failure", { error: "Synthetic failure" })));
const edit = async (index: number, value: string) => act(async () => {
  const input = host.querySelectorAll("input")[index];
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
});
const changeManager = async (manager = "m-two") => act(async () => {
  const select = host.querySelector("select")!;
  select.value = manager;
  select.dispatchEvent(new Event("change", { bubbles: true }));
});

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  viewport.phone = false;
  resetManagerChoice();
  setRosterDefault("assistant");
  history.replaceState(null, "", "/settings?tab=hub");
  pending.length = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => {
    root.unmount();
    // Settle fixture writes even if the test stopped while a manager was retired.
    pending.forEach((p) => p.resolve(empty));
  });
  host.remove();
});
const render = () => act(async () => root.render(<HubSettings />));

describe("independent extension download mirror settings", () => {
  it.each([404, 503, null])("keeps mirrors visible when the updater returns %s or remains loading", async (status) => {
    await render();
    if (status) await fail(pending.find((p) => p.path === "/hub/update")!, status);
    expect(host.textContent).toContain(t("settings-hubsettings.mirrors"));
    await answer(mirrors()[0], empty);
    expect(host.querySelectorAll("input")).toHaveLength(2);
    expect(host.textContent).toContain(t("settings-hubsettings.mirror_security"));
  });

  it("does not show editable blank defaults after a failed read; retry only reads", async () => {
    await render();
    await fail(mirrors()[0], 503);
    expect(host.querySelectorAll("input")).toHaveLength(0);
    expect(host.querySelector('[role="alert"]')?.textContent).toContain(t("settings-hubsettings.mirror_read_failed", { error: "" }).split(":")[0]);
    await act(async () => button("retry").click());
    expect(mirrors().map((p) => p.method)).toEqual(["GET", "GET"]);
    await answer(mirrors()[1], configured);
    expect(host.querySelector("input")?.value).toBe(configured.registry_mirror);
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("saves both fields and confirms only after the response; clear persists empty strings", async () => {
    await render();
    await answer(mirrors()[0], empty);
    await edit(0, configured.registry_mirror);
    await edit(1, configured.catalog_mirror);
    await act(async () => button("mirror_save").click());
    expect(mirrors()[1]).toMatchObject({ method: "PUT", json: configured });
    expect(host.querySelector('p[role="status"]')).toBeNull();
    expect(button("mirror_save").disabled).toBe(true);
    await answer(mirrors()[1], configured);
    expect(host.querySelector('p[role="status"]')?.textContent).toBe(t("settings-hubsettings.mirror_saved"));
    await act(async () => button("mirror_clear").click());
    expect(mirrors()[2]).toMatchObject({ method: "PUT", json: empty });
    await answer(mirrors()[2], empty);
    expect(host.querySelector("input")?.value).toBe("");
    expect(button("mirror_clear").disabled).toBe(true);
  });

  it("retains the draft and does not claim success on write failure, allowing retry", async () => {
    await render();
    await answer(mirrors()[0], empty);
    await edit(0, configured.registry_mirror);
    await act(async () => button("mirror_save").click());
    await fail(mirrors()[1], 500);
    expect(host.querySelector('p[role="status"]')).toBeNull();
    expect(host.querySelector('[role="alert"]')).not.toBeNull();
    expect(host.querySelector("input")?.value).toBe(configured.registry_mirror);
    expect(button("mirror_save").disabled).toBe(false);
    await act(async () => button("mirror_save").click());
    await answer(mirrors()[2], { ...empty, registry_mirror: configured.registry_mirror });
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(host.querySelector('p[role="status"]')).not.toBeNull();
  });

  it.each(["read-success", "read-failure", "write-success", "write-failure"])("ignores late %s after switching managers", async (scenario) => {
    await render();
    let retired = mirrors()[0];
    if (scenario.startsWith("write")) {
      await answer(retired, empty);
      await edit(0, configured.registry_mirror);
      await act(async () => button("mirror_save").click());
      retired = mirrors()[1];
    }
    await changeManager();
    const active = mirrors().at(-1)!;
    expect(active.path).toBe("/extensions/-/market-settings?manager=m-two");
    await answer(active, empty);
    if (scenario.endsWith("failure")) await fail(retired, 500);
    else await answer(retired, configured);
    expect(host.querySelector("input")?.value).toBe("");
    expect(host.querySelector('p[role="status"]')).toBeNull();
    expect(host.querySelector('[role="alert"]')).toBeNull();
    await edit(0, "https://second.example.com");
    await act(async () => button("mirror_save").click());
    expect(mirrors().at(-1)?.path).toBe(active.path);
  });

  it.each(["success", "failure"])("waits for a retired A write before reading A again after A/B/A (%s)", async (outcome) => {
    await render();
    await answer(mirrors()[0], empty);
    await edit(0, configured.registry_mirror);
    await act(async () => button("mirror_save").click());
    const retired = mirrors()[1];
    const committed = { ...empty, registry_mirror: configured.registry_mirror };
    expect(retired).toMatchObject({ method: "PUT", json: committed });
    await changeManager();
    await answer(mirrors()[2], empty);
    await changeManager("assistant");
    // A GET here would return empty while the original PUT has not committed.
    expect(mirrors()).toHaveLength(3);
    expect(host.querySelectorAll("input")).toHaveLength(0);
    if (outcome === "success") await answer(retired, committed);
    else await fail(retired, 500);
    expect(mirrors()[3]).toMatchObject({ method: "GET", path: "/extensions/-/market-settings?manager=assistant" });
    await answer(mirrors()[3], outcome === "success" ? committed : empty);
    expect(host.querySelector('p[role="status"]')).toBeNull();
    await edit(1, configured.catalog_mirror);
    await act(async () => button("mirror_save").click());
    expect(mirrors()[4]).toMatchObject({ method: "PUT", json: {
      registry_mirror: outcome === "success" ? configured.registry_mirror : "",
      catalog_mirror: configured.catalog_mirror,
    } });
    await answer(mirrors()[4], mirrors()[4].json);
  });

  it("keeps the Hub editor's owner across real Settings category navigation", async () => {
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    await answer(mirrors()[0], empty);
    await changeManager();
    await answer(mirrors()[1], empty);
    const pick = (tab: Tab) => act(async () => {
      [...host.querySelectorAll<HTMLButtonElement>("nav button")].find((b) => b.textContent?.trim() === TAB_INFO[tab].label)!.click();
    });
    await pick("devices");
    await pick("hub");
    expect(location.search).toContain("manager=m-two");
    expect(host.querySelector("select")?.value).toBe("m-two");
    expect(mirrors().at(-1)?.path).toBe("/extensions/-/market-settings?manager=m-two");
    await answer(mirrors().at(-1)!, empty);
    await edit(0, configured.registry_mirror);
    await act(async () => button("mirror_save").click());
    expect(mirrors().at(-1)?.path).toBe("/extensions/-/market-settings?manager=m-two");
    await answer(mirrors().at(-1)!, configured);
  });

  it("keeps the mirror owner when phone navigation goes through the category list", async () => {
    viewport.phone = true;
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    await answer(mirrors()[0], empty);
    await changeManager();
    await answer(mirrors()[1], empty);
    const back = () => act(async () => {
      const pop = openedFromList(history.state) ? new Promise<void>((done, reject) => {
        const timeout = setTimeout(() => reject(new Error("Settings list navigation timed out")), 1_000);
        addEventListener("popstate", () => { clearTimeout(timeout); done(); }, { once: true });
      }) : null;
      host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings.back")}"]`)!.click();
      await pop;
    });
    const pick = (tab: Tab) => act(async () => {
      [...host.querySelectorAll<HTMLButtonElement>("nav button")].find((b) => b.textContent?.startsWith(TAB_INFO[tab].label))!.click();
    });
    await back();
    await pick("devices");
    await back();
    await pick("hub");
    expect(location.search).toContain("manager=m-two");
    expect(host.querySelector("select")?.value).toBe("m-two");
    expect(mirrors().at(-1)?.path).toBe("/extensions/-/market-settings?manager=m-two");
    await answer(mirrors().at(-1)!, empty);
  });

  it("does not add an owner to unselected Settings screens", async () => {
    history.replaceState(null, "", "/settings?tab=devices");
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    const hub = [...host.querySelectorAll<HTMLButtonElement>("nav button")].find((b) => b.textContent?.trim() === TAB_INFO.hub.label)!;
    await act(async () => hub.click());
    expect(location.search).toBe("?tab=hub");
    await answer(mirrors()[0], empty);
  });

  it("shows invalid response details instead of a network error and prevents overwriting unknown settings", async () => {
    await render();
    await answer(mirrors()[0], { registry_mirror: "" });
    expect(host.querySelector('[role="alert"]')?.textContent).toContain(t("settings-hubsettings.mirror_response_invalid"));
    expect(host.querySelector('[role="alert"]')?.textContent).not.toContain(t("lib-api.cant_reach_hub"));
    expect(host.querySelectorAll("input")).toHaveLength(0);
    expect(mirrors().every((p) => p.method === "GET")).toBe(true);
  });

  it("blocks insecure or credential-bearing addresses", async () => {
    await render();
    await answer(mirrors()[0], empty);
    await edit(0, "http://registry.example.com");
    expect(button("mirror_save").disabled).toBe(true);
    expect(host.querySelector('[role="alert"]')?.textContent).toBe(t("settings-hubsettings.mirror_invalid"));
  });
});
