// @vitest-environment jsdom
// Exercise the Settings route, real REST client and native adapter. Only HTTP and
// the authenticated app-socket boundary are synthetic; no extension is installed.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { UIStreamRequest } from "../../lib/extension-wire";

type Envelope = { type: string; manager: string; request: UIStreamRequest };
const socket = vi.hoisted(() => ({ sent: [] as Envelope[], frames: new Set<(raw: string) => void>() }));
vi.mock("../../lib/store", async (original) => ({
  ...await original<typeof import("../../lib/store")>(),
  appSocketReady: () => true,
  onAppSocket: () => () => {},
  send: (envelope: Envelope) => socket.sent.push(envelope),
  onPrivateUIStream: (fn: (raw: string) => void) => {
    socket.frames.add(fn);
    return () => { socket.frames.delete(fn); };
  },
}));

import { getState } from "../../lib/store";
import { setRoster } from "../../lib/managers";
import { managerHeaderFor, resetManagerChoice } from "../../lib/manager-choice";
import { setToken } from "../../lib/api";
import { t } from "../../i18n";
import { Extensions } from "./Extensions";

const ROSTER = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "Personal", online: true, builtin: true },
    { id: "m-1", name: "Work", online: true },
  ],
};
type Request = { url: string; manager: string | null; body: Record<string, unknown> };
const requests: Request[] = [];
let listedOwners: string[];
let http: (request: Request) => Promise<Response>;
let host: HTMLDivElement;
let root: Root;
const initial = { ...getState() };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const view = (owner: string, ui = "main") => ({
  title: `${owner} ${ui}`,
  state: `${owner}-${ui}-state`,
  sections: [{ key: "section", items: [
    { key: "text", type: "text", text: `${owner} ${ui} content` },
    { key: "change", type: "button", title: "Change setting", action: { id: "change" } },
    { key: "next", type: "button", title: "Next page", action: { id: "next", push: { ui: "details", state: "selected-item" } } },
  ] }],
});
const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};
const render = async (route: string, onOpen = vi.fn()) => {
  await act(async () => root.render(<Extensions open={route} onOpen={onOpen} />));
  await act(async () => { await import("./NativeExtensionUI"); });
  await flush();
  return onOpen;
};
const click = async (label: string) => {
  const button = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label || b.getAttribute("aria-label") === label);
  expect(button, label).toBeDefined();
  await act(async () => button!.click());
  await flush();
};
const draws = () => requests.filter((r) => r.url.includes("/ui/") && !r.body.action);
const actions = () => requests.filter((r) => !!r.body.action);
const opens = () => socket.sent.filter((e) => e.request.request === "open");
const frame = async (body: object) => {
  await act(async () => [...socket.frames].forEach((fn) => fn(JSON.stringify(body))));
  await flush();
};
const snapshot = (open: Envelope, session: string) => ({
  type: "extension_ui_stream", manager: open.manager, kind: "snapshot", request_id: open.request.request_id,
  snapshot: { protocol: 1, session, epoch: `${session}-epoch`, version: 1, root_key: "root", view: view("Personal") },
});

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
  window.matchMedia = ((media: string) => ({ matches: false, media, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
  requests.length = 0;
  socket.sent.length = 0;
  socket.frames.clear();
  listedOwners = ["assistant", "m-1"];
  resetManagerChoice();
  setToken("synthetic-extension-owner");
  setRoster(ROSTER);
  Object.assign(getState(), { features: ["extensions", "extension_ui_stream_v1"], mesh: [], interactions: {} });
  http = async (r) => {
    const owner = r.manager === "m-1" ? "Work" : "Personal";
    if (r.body.action) return json({ view: view(`${owner} changed`, r.url.split("/").at(-1)) });
    return json({ view: view(owner, r.url.split("/").at(-1)) });
  };
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const r = { url, manager: new Headers(init?.headers).get("X-Persona-Manager"), body: JSON.parse(String(init?.body || "{}")) };
    requests.push(r);
    if (url.endsWith("/managers")) return json(ROSTER);
    if (url.endsWith("/extensions")) return json({ extensions: listedOwners.map((manager) => ({ id: "example", manager })) });
    if (/example\/-\/$/.test(url)) return json({
      id: "example", name: `${r.manager === "m-1" ? "Work" : "Personal"} extension`, kind: "process", status: "running", enabled: true,
      ui: [{ id: "main", title: "Main settings" }, { id: "details", title: "Detail settings" }],
    });
    if (url.includes("/ui/")) return http(r);
    throw new Error(`Unexpected request: ${url}`);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  setRoster(null);
  resetManagerChoice();
  setToken("");
  Object.assign(getState(), initial);
  vi.unstubAllGlobals();
});

it.each(["example@m-1/ui/main", "example/ui/main"])("keeps resolved owner content and actions on %s", async (route) => {
  if (!route.includes("@")) listedOwners = ["m-1"];
  const onOpen = await render(route);
  expect(host.textContent).toContain("Work main content");
  expect(host.textContent).not.toContain("Personal main content");
  expect(requests.find((r) => /example\/-\/$/.test(r.url))?.manager).toBe("m-1");
  expect(opens()).toHaveLength(0);
  await click("Change setting");
  expect(actions()).toHaveLength(1);
  expect(actions()[0]).toMatchObject({ manager: "m-1", body: { action: "change", state: "Work-main-state" } });
  expect(host.textContent).toContain("Work changed main content");
  await click(t("settings-extensions.redraw"));
  expect(draws()).toHaveLength(2);
  expect(draws().every((r) => r.manager === "m-1" && r.url.endsWith("/ui/main"))).toBe(true);
  await click("Next page");
  expect(draws().at(-1)).toMatchObject({ manager: "m-1", body: { state: "selected-item" } });
  expect(draws().at(-1)?.url).toMatch(/\/ui\/details$/);
  await click("Change setting");
  expect(actions().at(-1)).toMatchObject({ manager: "m-1", body: { action: "change", state: "Work-details-state" } });
  expect(actions().at(-1)?.url).toMatch(/\/ui\/details$/);
  await click("Work extension");
  expect(onOpen).toHaveBeenLastCalledWith("example@m-1");
  expect(socket.sent).toHaveLength(0);
});

it.each(["example@assistant/ui/main", "example/ui/main"])("retains default native page and action ownership on %s", async (route) => {
  await render(route);
  const open = opens()[0];
  expect(opens()).toHaveLength(1);
  expect(open).toMatchObject({ manager: "assistant", request: { request: "open", extension: "example", contribution: "main" } });
  expect(draws()).toHaveLength(0);
  await frame(snapshot(open, "personal-page"));
  expect(host.textContent).toContain("Personal main content");
  await click("Change setting");
  const event = socket.sent.find((e) => e.request.request === "event");
  expect(event).toMatchObject({ manager: "assistant", request: { interaction: { session: "personal-page", epoch: "personal-page-epoch", action: "change" } } });
  await click(t("settings-extensions.redraw"));
  expect(socket.sent).toContainEqual(expect.objectContaining({ manager: "assistant", request: expect.objectContaining({ request: "close", session: "personal-page" }) }));
  expect(opens()).toHaveLength(2);
  expect(opens()[1].request.request_id).not.toBe(open.request.request_id);
  await frame(snapshot(opens()[1], "redrawn-page"));
  await click("Change setting");
  expect(socket.sent.at(-1)).toMatchObject({ manager: "assistant", request: { request: "event", interaction: { session: "redrawn-page", action: "change" } } });
});

it("keeps HTTP read and action retries on the selected owner", async () => {
  const normal = http;
  let failRead = true;
  let failAction = true;
  http = async (r) => {
    if (!r.body.action && failRead) { failRead = false; return json({ error: "Synthetic read failure" }, 503); }
    if (r.body.action && failAction) { failAction = false; return json({ error: "Synthetic action failure" }, 503); }
    return normal(r);
  };
  await render("example@m-1/ui/main");
  expect(host.textContent).toContain("Synthetic read failure");
  await click(t("settings-extensions.redraw"));
  expect(host.textContent).toContain("Work main content");
  await click("Change setting");
  expect(host.textContent).toContain("Work main content");
  await click("Change setting");
  expect(actions()).toHaveLength(2);
  expect(actions().every((r) => r.manager === "m-1")).toBe(true);
  expect(host.textContent).toContain("Work changed main content");
  expect(socket.sent).toHaveLength(0);
});

it("retires native allocations that arrive after switching to another manager", async () => {
  await render("example@assistant/ui/main");
  const old = opens()[0];
  await render("example@m-1/ui/main");
  expect(host.textContent).toContain("Work main content");
  await frame(snapshot(old, "late-personal-page"));
  expect(socket.sent.at(-1)).toMatchObject({ manager: "assistant", request: { request: "close", session: "late-personal-page" } });
  expect(host.textContent).not.toContain("Personal main content");
  await click("Change setting");
  expect(actions().at(-1)?.manager).toBe("m-1");
  await render("example@assistant/ui/details");
  expect(managerHeaderFor("/extensions/example/-/ui/details")).toBeUndefined();
  expect(opens()).toHaveLength(2);
  expect(opens()[1]).toMatchObject({ manager: "assistant", request: { contribution: "details" } });
});

it("does not paint a retired HTTP response over a replacement native page", async () => {
  let finish!: (response: Response) => void;
  http = () => new Promise((resolve) => { finish = resolve; });
  await render("example@m-1/ui/main");
  expect(draws()).toHaveLength(1);
  await render("example@assistant/ui/main");
  await frame(snapshot(opens()[0], "replacement-page"));
  await act(async () => finish(json({ view: view("Late work") })));
  await flush();
  expect(host.textContent).toContain("Personal main content");
  expect(host.textContent).not.toContain("Late work");
  await click("Change setting");
  expect(socket.sent.at(-1)).toMatchObject({ manager: "assistant", request: { request: "event", interaction: { session: "replacement-page" } } });
  expect(actions()).toHaveLength(0);
});
