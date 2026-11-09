// @vitest-environment jsdom
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { setToken } from "../lib/api";
import { getState, refresh, reset } from "../lib/store";
import { RequestPage } from "./RequestPage";
import type { Interaction } from "../lib/types";

let cards: Interaction[];
let writes: { url: string; body: unknown }[];
let root: ReturnType<typeof createRoot>;
let host: HTMLDivElement;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

beforeEach(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setToken("fixture-token");
  reset();
  writes = [];
  cards = [{ id: "script-request", machine_id: "example-computer", session_id: "", kind: "permission", tool_name: "personad_ask", summary: "Publish the draft?" }];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = new URL(url, "http://example.com").pathname;
    if (init?.method === "POST") {
      writes.push({ url: path, body: JSON.parse(String(init.body)) });
      return new Response("{}");
    }
    const value = path.endsWith("/interactions") ? { interactions: cards }
      : path.endsWith("/sessions") ? { sessions: [] } : path.endsWith("/machines") ? { machines: [] } : {};
    return new Response(JSON.stringify(value), { headers: { "Content-Type": "application/json" } });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await refresh();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

it("renders, answers and shows the receipt through the real store and API", async () => {
  act(() => root.render(<RequestPage id="script-request" onToggleSidebar={() => {}} />));
  await flush();
  expect(host.textContent).toContain("Publish the draft?");
  expect(host.querySelector("fieldset[disabled]")).toBeNull();
  const allow = [...host.querySelectorAll("button")].find((button) => button.textContent?.trim() === "允许");
  expect(allow).toBeDefined();
  act(() => allow!.click());
  await flush();
  expect(writes).toEqual([{ url: "/api/v1/interactions/script-request/resolve", body: { allow: true } }]);
  expect(getState().interactions).toEqual([]);
  expect(host.querySelector('[data-answered="script-request"]')).not.toBeNull();
});

it("shows an ended state for an unavailable id", async () => {
  act(() => root.render(<RequestPage id="no-longer-pending" onToggleSidebar={() => {}} />));
  await flush();
  expect(host.textContent).toContain("此请求已结束。");
  expect(writes).toEqual([]);
});
