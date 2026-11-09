// @vitest-environment jsdom
// Reads the hub itself gives READ_TIMEOUT_MS or longer (#5035): a launcher's model list (70 s, the node starts its CLI),
// Model configuration's list (70 s), a launcher's quota (45 s), a computer's self-check (45 s), a Mac's browser (45 s) and its sign-ins (2 min 15 s), a document's
// conversion to PDF (2 min) and the extension catalog's refresh (a git market, 3 min). The page waits past the hub's
// own limit, so the hub's error (which computer, which launcher) is what shows, not "The hub took too long to respond"; and a list the
// hub answers after 45 s still shows. They still give up once the hub's limit is well past.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  const kept = new Map<string, string>();
  const g = globalThis as unknown as { localStorage: Pick<Storage, "getItem" | "setItem" | "removeItem"> };
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

import { HubTimeoutError, READ_TIMEOUT_MS } from "./api";
import { getCatalog } from "./extension-market";
import type { FileContent } from "./files";
import { launcherModels, providerModels } from "./launcher-models";
import type { Machine } from "./types";
import { LauncherQuota } from "../views/settings/LauncherQuota";
import { BrowserSection } from "../views/machines/BrowserSection";
import { NodeHealth } from "../views/machines/NodeHealth";
import { OfficeView } from "../views/session/files/OfficeView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// A hub that answers a path only when the test says so; until then the request ends only when its signal aborts.
type Pending = { path: string; answer: (status: number, body: unknown) => void };
let pending: Pending[] = [];
function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  return new Promise((resolve, reject) => {
    pending.push({ path: String(input).split("?")[0]!, answer: (status, body) => resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } })) });
    init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")), { once: true });
  });
}
const waitFor = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });

beforeEach(() => {
  pending = [];
  vi.useFakeTimers();
  vi.stubGlobal("fetch", vi.fn(hub));
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("a launcher's model lists", () => {
  for (const [name, read] of [
    ["新建 Agent's list", () => launcherModels("m1", "gw", { refresh: true })],
    ["模型配置's list", () => providerModels("launcher.gw", { refresh: true })],
    ["the extension catalog's refresh", () => getCatalog("", "", true)],
  ] as const) {
    it(`${name} waits for an answer the hub gives after ${READ_TIMEOUT_MS / 1000} s`, async () => {
      let settled: unknown = "pending";
      void read().then((v) => (settled = v), (e: unknown) => (settled = e));
      await waitFor(READ_TIMEOUT_MS + 10_000);
      expect(settled).toBe("pending");
      pending[0]!.answer(200, { agent: "claude", models: [{ id: "m-1", name: "M 1" }], markets: [], extensions: [{ id: "e-1" }] });
      await waitFor(0);
      expect(settled).not.toBe("pending");
      expect(settled).not.toBeInstanceOf(Error);
    });

    it(`${name} still gives up once the hub's own limit is well past`, async () => {
      let settled: unknown = "pending";
      void read().then((v) => (settled = v), (e: unknown) => (settled = e));
      await waitFor(200_000);
      expect(settled).toBeInstanceOf(HubTimeoutError);
    });
  }

  it("the extension catalog without a refresh keeps the read default", async () => {
    let settled: unknown = "pending";
    void getCatalog().then((v) => (settled = v), (e: unknown) => (settled = e));
    await waitFor(READ_TIMEOUT_MS);
    expect(settled).toBeInstanceOf(HubTimeoutError);
  });
});

describe("pages that read what the hub gives 45 s", () => {
  let host: HTMLDivElement;
  let root: Root;
  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
  });
  afterEach(() => {
    act(() => root.unmount());
    host.remove();
  });

  it("the launcher's quota shows the hub's error, not that the hub did not answer", async () => {
    act(() => root.render(<LauncherQuota name="gw" />));
    await waitFor(READ_TIMEOUT_MS + 100);
    expect(host.textContent).not.toContain("hub 太久没有回应");
    pending[0]!.answer(504, { error: "网关没有回应" });
    await waitFor(0);
    expect(host.textContent).toContain("网关没有回应");
  });

  it("the computer's self-check shows the hub's error, not that the hub did not answer", async () => {
    const machine = { id: "m1", name: "Mac", online: true } as Machine;
    act(() => root.render(<NodeHealth machine={machine} heading={null} />));
    await waitFor(READ_TIMEOUT_MS + 100);
    expect(host.textContent).not.toContain("hub 太久没有回应");
    pending[0]!.answer(504, { error: "这台电脑没有回应" });
    await waitFor(0);
    expect(host.textContent).toContain("这台电脑没有回应");
  });

  it("a Mac's browser section takes the hub's answer to its read after 45 s", async () => {
    act(() => root.render(<BrowserSection mid="m1" browser={{ enabled: true, installed: true, set_up: true, ready: true }} online />));
    await waitFor(READ_TIMEOUT_MS + 1_000);
    pending.find((p) => p.path.endsWith("/machines/m1/browser"))!.answer(200, { enabled: true, installed: false });
    await waitFor(0);
    expect([...host.querySelectorAll("button")].some((b) => b.textContent?.trim() === "安装 ego lite")).toBe(true);
  });

  it("a Mac's browser sign-ins show the hub's error, not that the hub did not answer", async () => {
    act(() => root.render(<BrowserSection mid="m1" browser={{ enabled: true, installed: true, set_up: true, ready: true }} online />));
    await waitFor(READ_TIMEOUT_MS + 30_000);
    expect(host.textContent).not.toContain("hub 太久没有回应");
    pending.find((p) => p.path.endsWith("/browser/cookies"))!.answer(409, { error: "浏览器没有打开" });
    await waitFor(0);
    expect(host.textContent).toContain("浏览器没有打开");
  });

  it("a document being converted to PDF waits for the hub's answer", async () => {
    const file = { path: "/work/report.odt", name: "report.odt", size: 1000, truncated: false, is_dir: false } as FileContent;
    act(() => root.render(<OfficeView mid="m1" file={file} onDownload={() => {}} />));
    await waitFor(READ_TIMEOUT_MS + 30_000);
    expect(host.textContent).not.toContain("hub 太久没有回应");
    pending.find((p) => p.path.endsWith("/file/convert"))!.answer(500, { error: "LibreOffice 没有转出来" });
    await waitFor(0);
    expect(host.textContent).toContain("LibreOffice 没有转出来");
  });
});
