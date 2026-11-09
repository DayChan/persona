// @vitest-environment jsdom
// Exercise the mounted Settings wizard through its real HTTP client and form controls.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((media: string) => ({ matches: false, media, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { t } from "../../i18n";
import type { ProviderView } from "../../lib/providers";
import { resetReadiness } from "../../lib/readiness";
import { getState } from "../../lib/store";
import { ProviderWizard } from "./ProviderWizard";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

type Editor = ProviderView | "new" | null;
type Call = { method: string; path: string; body?: Record<string, unknown> };
type Pending = Call & { resolve: (value: unknown, status?: number) => void; reject: (error: Error) => void };
const a: ProviderView = { name: "provider-a", base_url: "https://a.example.com", has_key: true, auth: "bearer" };
const b: ProviderView = { name: "provider-b", base_url: "https://b.example.com", has_key: true, auth: "bearer" };
let calls: Call[];
let pending: Pending[];
let host: HTMLDivElement;
let root: Root;
let saved: () => void;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const text = () => document.body.textContent ?? "";
function button(key: string) {
  const found = [...document.body.querySelectorAll("button")].find((el) => el.textContent?.trim().startsWith(t(key)));
  expect(found, key).toBeDefined();
  return found!;
}
function click(key: string) { act(() => button(key).click()); }
function input(selector: string) {
  const found = document.body.querySelector<HTMLInputElement>(selector);
  expect(found, selector).not.toBeNull();
  return found!;
}
function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const prototype = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  act(() => {
    Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
function render(existing: Editor) {
  root.render(<ProviderWizard existing={existing} onClose={() => render(null)} onSaved={saved} />);
}
async function draw(existing: Editor) {
  act(() => render(existing));
  await flush();
}
async function settle(request: Pending, outcome: "success" | "http error" | "network error", id = "a-only-model") {
  await act(async () => {
    if (outcome === "network error") request.reject(new Error("Retired request failed"));
    else if (outcome === "http error") request.resolve({ error: "Retired request failed" }, 502);
    else request.resolve(request.path.endsWith("/test") ? { ok: true, ms: 123, model: id } : { models: [{ id }] });
  });
  await flush();
}
async function addModel(id: string) {
  type(input(`[aria-label="${t("settings-providerwizard.add_model_id")}"]`), id);
  click("settings-providerwizard.add");
}
async function save() {
  click("common.save");
  await flush();
  expect(saved).toHaveBeenCalledOnce();
  return calls.find((c) => c.method === "PUT")!;
}

beforeEach(() => {
  calls = [];
  pending = [];
  saved = vi.fn();
  Object.assign(getState(), { features: ["builtin_presets"] });
  resetReadiness();
  vi.stubGlobal("fetch", vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
    const call = { method: init?.method ?? "GET", path: String(url).split("?")[0]!, body: init?.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(call);
    if (/\/launchers\/(discover|test)$/.test(call.path)) {
      return new Promise<Response>((resolve, reject) => pending.push({ ...call, resolve: (value, status) => resolve(json(value, status)), reject }));
    }
    return Promise.resolve(json(call.path.endsWith("/presets") ? { presets: [{ id: "example/preset", plugin: "example", name: "Synthetic provider", kind: "launcher", base_url: "https://preset.example.com", model: "preset-model" }] } : {}));
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("provider discovery ownership", () => {
  it("does not save A's deferred models into B after closing and reopening (#1314)", async () => {
    await draw(a);
    click("settings-providerwizard.discover");
    expect(pending[0]!.body).toEqual({ name: "provider-a", base_url: "https://a.example.com", auth: "bearer", api: "messages" });
    click("common.cancel");
    await draw(b);
    await settle(pending[0]!, "success");
    click("settings-providerwizard.next");
    expect(document.querySelector('[data-model="a-only-model"]')).toBeNull();
    await addModel("b-manual");
    expect(await save()).toMatchObject({ path: "/api/v1/launchers/provider-b", body: { name: "provider-b", base_url: "https://b.example.com", model: "b-manual", models: [{ id: "b-manual" }] } });
  });

  it.each(["switch provider", "reopen same provider"] as const)("retires a request when we %s, even with identical connection inputs", async (transition) => {
    await draw(a);
    click("settings-providerwizard.discover");
    if (transition === "reopen same provider") { await draw(null); await draw(a); }
    else await draw({ ...b, base_url: a.base_url });
    expect(button("settings-providerwizard.discover").disabled).toBe(false);
    click("settings-providerwizard.next");
    await addModel("current-edit");
    await settle(pending[0]!, "success");
    expect(document.querySelector('[data-model="a-only-model"]')).toBeNull();
    expect((await save()).body).toMatchObject({ model: "current-edit", models: [{ id: "current-edit" }] });
  });

  it.each(["success", "http error", "network error"] as const)("ignores retired %s and finally while the new editor is discovering", async (outcome) => {
    await draw(a);
    click("settings-providerwizard.discover");
    await draw(null);
    await draw(b);
    click("settings-providerwizard.discover");
    expect(pending).toHaveLength(2);
    await settle(pending[0]!, outcome);
    expect(button("settings-providerwizard.discover").getAttribute("aria-busy")).toBe("true");
    expect(text()).not.toContain("Retired request failed");
    expect(text()).not.toContain(t("settings-providerwizard.discover_found", { count: 1 }));
    await settle(pending[1]!, "success", "b-only-model");
    expect(button("settings-providerwizard.discover").disabled).toBe(false);
    click("settings-providerwizard.next");
    expect((await save()).body).toMatchObject({ model: "b-only-model", models: [{ id: "b-only-model" }] });
  });

  it.each(["success", "http error", "network error"] as const)("ignores %s and finally from an earlier connection in the same editor", async (outcome) => {
    await draw(a);
    click("settings-providerwizard.discover");
    type(input('input[inputmode="url"]'), "https://b.example.com");
    expect(button("settings-providerwizard.discover").disabled).toBe(false);
    click("settings-providerwizard.discover");
    expect(pending[1]!.body).toMatchObject({ name: "provider-a", base_url: "https://b.example.com" });
    await settle(pending[0]!, outcome);
    expect(button("settings-providerwizard.discover").getAttribute("aria-busy")).toBe("true");
    expect(text()).not.toContain("Retired request failed");
    await settle(pending[1]!, "success", "current-model");
    click("settings-providerwizard.next");
    expect((await save()).body).toMatchObject({ base_url: "https://b.example.com", model: "current-model", models: [{ id: "current-model" }] });
  });

  it("does not replace a completed fresh result with a retired failure", async () => {
    await draw(a);
    click("settings-providerwizard.discover");
    await draw(b);
    click("settings-providerwizard.discover");
    await settle(pending[1]!, "success", "b-model");
    await settle(pending[0]!, "network error");
    expect(text()).toContain(t("settings-providerwizard.discover_found", { count: 1 }));
    expect(text()).not.toContain("Retired request failed");
    click("settings-providerwizard.next");
    expect((await save()).body).toMatchObject({ model: "b-model", models: [{ id: "b-model" }] });
  });

  it("retires custom discovery when picking a preset and returning to the same custom form", async () => {
    await draw("new");
    click("settings-providerwizard.custom");
    type(input('input[placeholder*="my_gateway"]'), "my_gateway");
    type(input('input[inputmode="url"]'), "https://a.example.com");
    type(input('input[type="password"]'), "example-key-a");
    click("settings-providerwizard.discover");
    act(() => document.querySelector<HTMLButtonElement>("ol button")!.click());
    act(() => [...document.querySelectorAll("button")].find((el) => el.textContent?.startsWith("Synthetic provider"))!.click());
    act(() => document.querySelector<HTMLButtonElement>("ol button")!.click());
    click("settings-providerwizard.custom");
    await settle(pending[0]!, "success");
    expect(button("settings-providerwizard.discover").disabled).toBe(false);
    expect(input('input[type="password"]').value).toBe("example-key-a");
    click("settings-providerwizard.next");
    expect(document.querySelector('[data-model="a-only-model"]')).toBeNull();
  });

  it.each(["endpoint", "key", "auth", "type", "protocol", "headers", "endpoint changed back"] as const)("invalidates discovery on %s edits without resetting the form", async (field) => {
    await draw("new");
    click("settings-providerwizard.custom");
    type(input('input[placeholder="e.g. my_gateway"], input[placeholder*="my_gateway"]'), "my_gateway");
    type(input('input[inputmode="url"]'), "https://a.example.com");
    type(input('input[type="password"]'), "example-key-a");
    click("settings-providerwizard.discover");
    if (field.startsWith("endpoint")) {
      type(input('input[inputmode="url"]'), "https://b.example.com");
      if (field === "endpoint changed back") type(input('input[inputmode="url"]'), "https://a.example.com");
    } else if (field === "key") type(input('input[type="password"]'), "example-key-b");
    else if (field === "auth") act(() => [...document.querySelectorAll("button")].find((el) => el.textContent === "x-api-key")!.click());
    else if (field === "type" || field === "protocol") {
      click("settings-providerwizard.direct_api");
      if (field === "protocol") act(() => [...document.querySelectorAll("button")].find((el) => el.textContent === "OpenAI Responses")!.click());
    } else {
      click("settings-providerwizard.next");
      click("settings-providerwizard.advanced");
      const headers = document.querySelector<HTMLTextAreaElement>("textarea")!;
      type(headers, "X-Example: changed");
    }
    const expectedURL = field === "endpoint" ? "https://b.example.com" : "https://a.example.com";
    await settle(pending[0]!, "success");
    if (field !== "headers") {
      expect(input('input[inputmode="url"]').value).toBe(expectedURL);
      expect(input('input[type="password"]').value).toBe(field === "key" ? "example-key-b" : "example-key-a");
      click("settings-providerwizard.next");
    }
    expect(document.querySelector('[data-model="a-only-model"]')).toBeNull();
    await addModel("manual-model");
    click("common.save");
    await flush();
    const body = calls.find((c) => c.method === "POST" && c.path.endsWith("/launchers"))!.body;
    expect(body).toMatchObject({ base_url: expectedURL, model: "manual-model", models: [{ id: "manual-model" }] });
    if (field === "headers") expect(body?.headers).toBe("X-Example: changed");
  });

  it("allows retry after current discovery fails, then auto-selects models in the empty form", async () => {
    await draw(a);
    click("settings-providerwizard.discover");
    await settle(pending[0]!, "http error");
    expect(text()).toContain("Retired request failed");
    expect(button("settings-providerwizard.discover").disabled).toBe(false);
    click("settings-providerwizard.discover");
    expect(text()).not.toContain("Retired request failed");
    await settle(pending[1]!, "success", "discovered-model");
    click("settings-providerwizard.next");
    expect((await save()).body).toMatchObject({ model: "discovered-model", models: [{ id: "discovered-model" }] });
  });

  it("merges valid results with current model edits, checked choices and an intentional default", async () => {
    await draw({ ...a, model: "original", models: [{ id: "original" }, { id: "keep-unchecked" }] });
    click("settings-providerwizard.discover");
    click("settings-providerwizard.next");
    act(() => document.querySelector<HTMLButtonElement>('[data-model="keep-unchecked"] [role="checkbox"]')!.click());
    type(input(`[aria-label="${t("settings-providerwizard.context_window", { id: "original" })}"]`), "12345");
    await addModel("manual-default");
    act(() => [...document.querySelectorAll<HTMLButtonElement>('[data-model="manual-default"] button')].find((el) => el.textContent === t("settings-providerwizard.set_default"))!.click());
    await act(async () => pending[0]!.resolve({ models: [{ id: "original", context_window: 99999 }, { id: "keep-unchecked" }, { id: "new-unchecked" }] }));
    await flush();
    expect(document.querySelector('[data-model="new-unchecked"] [role="checkbox"]')?.getAttribute("aria-checked")).toBe("false");
    expect(document.querySelector('[data-model="keep-unchecked"] [role="checkbox"]')?.getAttribute("aria-checked")).toBe("false");
    expect((await save()).body).toMatchObject({ model: "manual-default", models: [{ id: "original", context_window: 12345 }, { id: "manual-default" }] });
  });
});

it.each(["discover", "test"] as const)("keeps the latest %s request's busy/result ownership when clicks overlap before rerender", async (operation) => {
  await draw(a);
  const key = `settings-providerwizard.${operation}`;
  act(() => { button(key).click(); button(key).click(); });
  expect(pending).toHaveLength(2);
  await settle(pending[0]!, "http error");
  expect(button(key).getAttribute("aria-busy")).toBe("true");
  expect(text()).not.toContain("Retired request failed");
  await settle(pending[1]!, "success", "latest-model");
  expect(button(key).disabled).toBe(false);
  if (operation === "test") expect(document.querySelector('[role="status"]')?.textContent).toContain("123");
  else {
    click("settings-providerwizard.next");
    expect((await save()).body).toMatchObject({ model: "latest-model", models: [{ id: "latest-model" }] });
  }
});

describe("connection test ownership", () => {
  it.each(["success", "http error", "network error"] as const)("ignores retired %s and finally while the new editor is testing", async (outcome) => {
    await draw(a);
    click("settings-providerwizard.test");
    await draw(null);
    await draw(b);
    click("settings-providerwizard.test");
    expect(pending).toHaveLength(2);
    await settle(pending[0]!, outcome);
    expect(button("settings-providerwizard.test").getAttribute("aria-busy")).toBe("true");
    expect(document.querySelector('[role="status"]')).toBeNull();
    await settle(pending[1]!, "http error");
    expect(text()).toContain("Retired request failed");
    click("settings-providerwizard.test");
    await settle(pending[2]!, "success", "b-model");
    expect(text()).not.toContain("Retired request failed");
    expect(document.querySelector('[role="status"]')?.textContent).toContain("123");
    expect(button("settings-providerwizard.test").disabled).toBe(false);
  });

  it.each(["endpoint", "key", "test model"] as const)("invalidates a test on %s edits and lets the current request complete", async (field) => {
    await draw(a);
    click("settings-providerwizard.test");
    if (field === "endpoint") type(input('input[inputmode="url"]'), "https://b.example.com");
    else if (field === "key") type(input('input[type="password"]'), "example-key-b");
    else type(input(`[aria-label="${t("settings-providerwizard.test_model")}"]`), "new-test-model");
    expect(button("settings-providerwizard.test").disabled).toBe(false);
    click("settings-providerwizard.test");
    await settle(pending[0]!, "network error");
    expect(button("settings-providerwizard.test").getAttribute("aria-busy")).toBe("true");
    expect(text()).not.toContain("Retired request failed");
    await settle(pending[1]!, "success");
    expect(document.querySelector('[role="status"]')?.textContent).toContain("123");
  });

  it("keeps discovery alive when only the test model changes, and keeps a current test alive when discovery fails", async () => {
    await draw(a);
    click("settings-providerwizard.discover");
    type(input(`[aria-label="${t("settings-providerwizard.test_model")}"]`), "explicit-model");
    click("settings-providerwizard.test");
    await settle(pending[0]!, "http error");
    expect(button("settings-providerwizard.test").getAttribute("aria-busy")).toBe("true");
    await settle(pending[1]!, "success");
    expect(document.querySelector('[role="status"]')?.textContent).toContain("123");
    click("settings-providerwizard.discover");
    await settle(pending[2]!, "success", "valid-model");
    click("settings-providerwizard.next");
    expect((await save()).body).toMatchObject({ model: "valid-model", models: [{ id: "valid-model" }] });
  });
});
