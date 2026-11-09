// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});
vi.mock("../../lib/store", () => ({ onHubEvent: () => () => {} }));
import { initI18n, t } from "../../i18n";
import { ModelProfiles } from "./ModelProfiles";
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
afterEach(() => vi.unstubAllGlobals());
it("switches profiles and exposes rollback", async () => {
 await initI18n();
 const writes: string[] = [];
 let active = "performance";
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  const path = String(input);
  if (init?.method === "POST") { writes.push(path); active = path.includes("rollback") ? "performance" : "economy"; }
  return new Response(JSON.stringify({ revision: 1, active, profiles: ["performance", "economy"].map((id) => ({ id, name: id, active: id === active, changes: ["agent.new"], tiers: {} })), rollback: { to: "performance", changes: ["agent.new"] } }), { status: 200 });
 }));
 const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
 const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
 await act(async () => { root.render(<ModelProfiles onChanged={() => {}} />); }); await flush();
 const btn = (text: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.includes(text))!;
 act(() => btn("economy").click()); await flush();
 act(() => btn(t("settings-modelprofiles.apply"), document.querySelector('[role="dialog"]')!).click()); await flush();
 expect(writes).toEqual(["/api/v1/model-profiles/economy/apply"]);
 act(() => btn(t("settings-modelprofiles.rollback")).click()); await flush();
 expect(writes[1]).toBe("/api/v1/model-profiles/rollback");
 await act(async () => root.unmount()); host.remove();
});

it("shows apply failures inside the dialog and clears errors on cancel and close", async () => {
 await initI18n();
 const onChanged = vi.fn();
 vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
  if (init?.method === "POST") {
   expect(String(input)).toBe("/api/v1/model-profiles/economy/apply");
   return new Response(JSON.stringify({ error: "Provider is no longer available" }), { status: 400 });
  }
  return new Response(JSON.stringify({ revision: 1, profiles: ["economy", "performance"].map((id) => ({ id, name: id, changes: [], tiers: {} })) }), { status: 200 });
 }));
 const host = document.createElement("div"); document.body.append(host); const root = createRoot(host);
 const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
 const btn = (text: string, scope: ParentNode = document) => [...scope.querySelectorAll("button")].find((b) => b.textContent?.includes(text))!;
 try {
  await act(async () => { root.render(<ModelProfiles onChanged={onChanged} />); }); await flush();
  for (const close of ["cancel", "escape"]) {
   act(() => btn("economy", host).click()); await flush();
   expect(document.querySelector('[role="alert"]')).toBeNull();
   const dialog = document.querySelector('[role="dialog"]')!;
   act(() => btn(t("settings-modelprofiles.apply"), dialog).click()); await flush();
   expect(document.querySelector('[role="dialog"]')).toBe(dialog);
   expect(dialog.querySelector('[role="alert"]')?.textContent).toContain("Provider is no longer available");
   expect(document.querySelectorAll('[role="alert"]')).toHaveLength(1);
   expect(host.querySelector('[role="alert"]')).toBeNull();
   expect(onChanged).not.toHaveBeenCalled();
   act(() => {
    if (close === "cancel") btn(t("common.cancel"), dialog).click();
    else document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
   }); await flush();
   expect(document.querySelector('[role="dialog"]')).toBeNull();
   expect(document.querySelector('[role="alert"]')).toBeNull();
  }
  act(() => btn("performance", host).click()); await flush();
  expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  expect(document.querySelector('[role="alert"]')).toBeNull();
 } finally {
  await act(async () => root.unmount()); host.remove();
 }
});
