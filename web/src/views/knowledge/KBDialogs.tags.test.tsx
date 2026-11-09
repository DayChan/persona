// @vitest-environment jsdom
// The tag editor saves on Enter in its tags field: a fast second Enter while the first save is out must not save again.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import type { KBEntry } from "../../lib/kb";
import { TagEditor } from "./KBDialogs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Request = { url: string; init: RequestInit; resolve: (value: Response) => void };
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const saved = vi.fn();
const closed = vi.fn();
const entry: KBEntry = { kind: "people", id: "example", name: "Example entry", importance: "P1", relevance: 50, tags: ["existing"] };

const puts = () => requests.filter((r) => r.init.method === "PUT");
const enter = () => act(() => {
  document.querySelector<HTMLInputElement>("[role=dialog] input[aria-label=Tags]")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
});

beforeEach(async () => {
  await initI18n("en");
  setToken("synthetic-owner-a");
  saved.mockClear();
  closed.mockClear();
  requests = [];
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }));
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init, resolve }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

it("saves once for a fast double Enter in the tags field", async () => {
  await act(async () => root.render(<TagEditor entry={entry} open onClose={closed} onSaved={saved} />));
  await enter();
  await enter();
  expect(puts()).toHaveLength(1);
  expect(puts()[0].url).toContain("/kb/people/example");
  await act(async () => {
    puts()[0].resolve(new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
  });
  expect(saved).toHaveBeenCalledTimes(1);
  expect(closed).toHaveBeenCalledTimes(1);
});
